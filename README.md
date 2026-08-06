# MetalGrid

## Accelerator-aware Kubernetes control plane

Value proposition: <br>
MetalGrid is a multi-tenant control plane for scheduling AI/ML workloads onto
scarce accelerator capacity (GPUs/TPUs/NPUs) on Kubernetes. It adds what the
default scheduler doesn't have out of the box: per-team quotas, priority
queues, fair-share dequeue, bin-pack/spread scoring, gang (all-or-nothing)
scheduling, and automatic retry/checkpoint/DLQ handling for failed jobs —
all exposed through a REST + gRPC platform API with auth, rate limiting, and
audit logging.

Status Quo: Teams either hand-roll scheduling scripts around raw Kubernetes
Jobs (no fairness, no quotas, no gang semantics, silent failures) or adopt a
heavyweight managed platform (Slurm-on-k8s, Run:ai) that's overkill for a
small team and not self-hostable at $0 cost. There's no lightweight,
inspectable, self-hosted option between "raw kubectl" and "enterprise GPU
orchestrator."

Business metrics:
- **Utilization**: bin-pack vs. spread node scoring keeps accelerator slots
  full instead of fragmented across nodes.
- **Reliability**: exponential-backoff retry + checkpoint/resume turns
  transient failures into automatic recovery instead of a page and a manual
  resubmit; a dead-letter queue makes unrecoverable failures visible instead
  of silently dropped.
- **Fairness**: per-team `QuotaPolicy` and fair-share dequeue stop one team's
  burst from starving everyone else's queue.
- **Operability**: Prometheus/Grafana/OTel + documented SLOs (`docs/slo.md`)
  mean an on-call engineer can diagnose a real incident from dashboards
  alone — proven live in `docs/postmortems/`, not just claimed.

## Contributors

Solo project.

| Name | Responsible for | Commits |
|---|---|---|
| Jahnavi Yelamanchi | Everything — CRDs/operator, scheduler, platform API, observability, deployment/GitOps, scale & chaos testing | https://github.com/jahnavi-yelamanchi/metalgrid/commits/main |

## System diagram

```
CLI / curl / grpcurl
        |
  Go API server  (REST :8080, gRPC :9090)
        |
 PostgreSQL + NATS JetStream
        |
 Kubernetes API server
        |
 MetalGrid Operator (controller-runtime)
   ├── AcceleratorJob / AcceleratorPool / InferenceService / QuotaPolicy CRDs
   ├── Admission webhook (quota enforcement)
   └── Leader-elected reconcilers
        |
 Kubernetes Workers
   ├── Mock device plugin (DaemonSet)
   ├── Scheduler extender (bin-pack/spread scoring, gang scheduling)
   ├── Job pods
   └── Mock inference backend (vLLM-shaped /v1/completions)
```

## Summary of outside materials

| | How it's used | Conditions of use |
|---|---|---|
| Kubernetes / kind | Cluster runtime; kind provides a real multi-node control plane locally | Apache 2.0 |
| controller-runtime / Kubebuilder conventions | Operator reconcile loops, CRD/webhook scaffolding patterns | Apache 2.0 |
| PostgreSQL | Job/audit-log system of record | PostgreSQL license |
| NATS JetStream | Durable job queue, fair-share dequeue, DLQ | Apache 2.0 |
| Prometheus / Grafana | Metrics collection + dashboards | Apache 2.0 |
| OpenTelemetry / Jaeger | Distributed tracing across API → queue → operator → pod | Apache 2.0 |
| cert-manager | TLS issuance for the admission webhook | Apache 2.0 |
| Kyverno | Cluster policy enforcement (no `:latest`, signed images, resource limits) | Apache 2.0 |
| Trivy / Sigstore cosign | Image vulnerability scanning + keyless signing in CI | Apache 2.0 |
| Argo CD | GitOps continuous delivery | Apache 2.0 |
| Terraform | Wraps `kind`/`docker compose` cluster bring-up as IaC | MPL 2.0 |
| `busybox` | Default image for demo/chaos-drill job pods (no real accelerator workload available) | BSD-style |

No trained models or datasets are involved — MetalGrid schedules and
observes workloads, it doesn't run ML training itself. Accelerator hardware
is simulated by a mock Kubernetes device plugin (`metalgrid.dev/accelerator`)
since no GPU access was available at $0 budget.

## Summary of infrastructure requirements

| Requirement | How much/when | Justification |
|---|---|---|
| Local machine (Apple Silicon, colima-backed Docker) | 1, for the whole project | No cloud budget; kind runs a real multi-node cluster on a laptop |
| kind cluster | 4 nodes (1 control-plane + 3 workers), up for the duration of dev/test | `hack/kind-config.yaml` — enough nodes for gang scheduling and spread-scoring to be meaningful |
| Mock accelerator capacity | 4 devices/worker × 3 workers = 12 slots (`deploy/helm/values.yaml`) | Demo-scale value, not a hard ceiling — raising it doesn't require a code change |
| docker-compose (Postgres, NATS, Jaeger) | 1 stack, local | Backing services for the API/operator during non-in-cluster dev |
| GitHub Actions runners | Per CI run | Multi-arch (amd64+arm64) build, Trivy scan, SBOM, cosign sign — no self-hosted infra needed |

No cloud VMs, floating IPs, GPUs, or paid object/block storage were used —
the entire project runs at $0 on local infra, by design.

## Implementation

Built in phases; each phase's real numbers/results are documented in
`docs/`, not just described here.

### Phase 1 — CRDs & Operator

```
api/v1alpha1/          # AcceleratorJob, AcceleratorPool, QuotaPolicy, InferenceService CRD types
internal/controller/   # reconcilers: acceleratorjob, inferenceservice
cmd/operator/          # operator binary — leader election, RBAC, metrics/submission runnables
cmd/devplugin/         # mock Kubernetes device plugin (kubelet extended resources)
```

Core reconcile loop: finalizers, owner references, leader-elected controller.
`AcceleratorJob` status is driven off real pod phases (`syncStatusFromPods`),
not assumed — including gang jobs, where all pods in the group must succeed
together.

### Phase 2 — Scheduling

```
internal/scheduler/    # score.go (bin-pack/spread scoring, gang feasibility), extender.go (HTTP extender)
internal/webhook/      # admission webhook — quota enforcement at submit time
deploy/helm/templates/priorityclasses.yaml   # metalgrid-high/normal/low
```

A custom kube-scheduler HTTP extender (Filter/Prioritize) rather than the
full scheduler-plugins framework — a deliberate call given kind's newer
Kubernetes version at build time. Preemption uses native `PriorityClass`,
not a custom priority queue.

### Phase 3 — Reliability

```
internal/controller/acceleratorjob_controller.go   # backoffDuration, scheduleRetryOrFail, ensureCheckpointPVC
internal/queue/                                    # PublishDLQ, PeekDLQ, deliveriesExhausted
```

Exponential backoff retry (capped at 60s), checkpoint/resume via PVC,
dead-letter queue for exhausted retries, idempotent submission (dedup on
create), graceful cancellation and `ActiveDeadlineSeconds` timeout.

### Phase 4 — Platform API

```
internal/api/         # REST handlers, JWT auth, per-team rate limiting
internal/grpcapi/      # gRPC service over the same internal/service layer
internal/service/      # shared business logic used by both REST and gRPC
internal/store/        # Postgres access, cursor pagination, audit log
proto/metalgrid/v1/    # gRPC service definition
docs/openapi.yaml       # REST API spec
```

One shared service layer behind both transports. JWT (HS256) auth + rate
limiting on REST; the gRPC service intentionally has no auth interceptor yet
— documented gap, not an oversight.

### Phase 5 — Observability

```
internal/metrics/      # Prometheus metric definitions
internal/tracing/      # OpenTelemetry SDK wiring, W3C traceparent propagation through NATS message bodies
deploy/helm/dashboards/          # Grafana dashboard
deploy/helm/prometheus-rules.yaml # alerting rules
docs/slo.md             # SLIs/SLOs backing the alerts
```

Traces span API → queue → operator → pod, not just the API layer. SLOs are
real Prometheus queries, checked against real load-test data in
`docs/scale-test-results.md`.

### Phase 6 — Deployment / Continuous X

```
deploy/helm/            # the real deploy path — full stack, CRDs, RBAC, cert-manager webhook wiring
deploy/kustomize/       # cluster add-on layer only (priorityclasses, devplugin, scheduler) — not duplicating Helm's app tier
deploy/argocd/          # Argo CD Application — automated sync + self-heal
deploy/kyverno/         # 3 enforced policies: no :latest, signed images required, resource limits required
deploy/terraform/       # wraps kind + docker-compose bring-up as IaC
.github/workflows/ci.yaml # test job + multi-arch build/scan/sign job
```

GitOps loop verified live: a Git push updates the manifest, Argo CD detects
drift and self-heals. CI builds natively for both amd64 and arm64 (no QEMU),
scans with Trivy, generates an SBOM, and signs images keylessly via cosign +
GitHub OIDC.

### Phase 7 — Scale & Chaos

```
tools/jobgen/           # Python load generator
tools/k6/                # k6 sustained-load script
hack/chaos/               # 8 chaos drills (kill-leader, drain-node, kill-nats, exhaust-quota, ...)
docs/scale-test-results.md
docs/postmortems/
```

Real measured numbers, not projections: 1,346 jobs submitted across two load
runs, 0 failures, API p95 ~120ms under load, full drain of a 40:1
demand/capacity backlog. A real incident — NATS JetStream silently
defaulting to ephemeral `/tmp` storage — was found via chaos drill, fixed,
and written up as a full postmortem in `docs/postmortems/`.

## Quickstart

Requires: `go`, `docker`, `kind`, `kubectl`, `helm` (`colima start` first on
macOS).

```bash
make kind-up   # 4-node kind cluster
make dev-up    # Postgres + NATS + Jaeger via docker-compose

go run ./cmd/operator &
go run ./cmd/apiserver &

curl -X POST localhost:8080/v1/jobs -d '{
  "team": "platform", "image": "busybox:1.36",
  "command": ["sh", "-c", "echo hello"],
  "acceleratorType": "mock-gpu", "acceleratorCount": 1
}'
curl localhost:8080/v1/jobs/<id>   # watch it go Pending -> Running -> Succeeded
```

Full in-cluster deployment (webhook TLS, quotas, all CRDs) via Helm:

```bash
kubectl apply -f https://github.com/cert-manager/cert-manager/releases/latest/download/cert-manager.yaml
helm install metalgrid deploy/helm --namespace metalgrid-system --create-namespace
```

Or via Argo CD (`deploy/argocd/application.yaml`) for the full GitOps loop.

```bash
make test                              # go test ./...
python3 tools/jobgen/jobgen.py --count 500 --concurrency 30   # load test
k6 run tools/k6/load-test.js           # API latency test
make chaos-kill-leader                 # and the other hack/chaos/*.sh drills
```
