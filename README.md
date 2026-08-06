# MetalGrid

Accelerator-aware Kubernetes control plane for scheduling AI/ML workloads.
Adds per-team quotas, priority queues, fair-share dequeue, bin-pack/spread
scoring, gang scheduling, and automatic retry/checkpoint/DLQ handling on top
of vanilla Kubernetes, exposed through a REST + gRPC API.

Runs entirely on local infra (kind cluster on a laptop), $0 cost. No real
GPUs, accelerators are simulated via a mock Kubernetes device plugin.

## Tech stack

**Core**: Go, Kubernetes, controller-runtime, custom CRDs

**Data**: PostgreSQL, NATS JetStream

**API**: REST, gRPC, JWT auth

**Observability**: Prometheus, Grafana, OpenTelemetry, Jaeger

**Deployment**: Helm, Kustomize, Argo CD, Terraform, Kyverno, GitHub Actions

**Supply chain**: Trivy, Sigstore cosign, SBOM

**Local dev**: kind, colima, docker compose

## Features

- Custom CRDs (`AcceleratorJob`, `AcceleratorPool`, `QuotaPolicy`,
  `InferenceService`) reconciled by a leader-elected operator with
  finalizers, owner references, and an admission webhook
- Custom kube-scheduler HTTP extender: bin-pack/spread node scoring, gang
  (all-or-nothing) scheduling, native `PriorityClass` preemption
- Per-team quotas and fair-share dequeue over NATS JetStream
- Exponential backoff retry, checkpoint/resume via PVC, dead-letter queue,
  idempotent submission, graceful cancellation and timeout
- REST + gRPC API on one shared service layer, JWT auth, rate limiting,
  cursor pagination, audit log
- Prometheus metrics, Grafana dashboard, OpenTelemetry tracing end to end
  (API to queue to operator to pod), documented SLOs
- Helm chart, Kustomize overlay, Argo CD GitOps, Kyverno policy enforcement,
  Terraform, multi-arch CI with image scanning and keyless signing
- Load-tested and chaos-tested with a real incident postmortem

## Quickstart

Requires `go`, `docker`, `kind`, `kubectl`, `helm` (`colima start` first on
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
curl localhost:8080/v1/jobs/<id>   # Pending -> Running -> Succeeded
```

Full in-cluster deployment (webhook TLS, quotas, all CRDs):

```bash
kubectl apply -f https://github.com/cert-manager/cert-manager/releases/latest/download/cert-manager.yaml
helm install metalgrid deploy/helm --namespace metalgrid-system --create-namespace
```

Or via Argo CD (`deploy/argocd/application.yaml`) for the full GitOps loop.

```bash
make test
python3 tools/jobgen/jobgen.py --count 500 --concurrency 30   # load test
k6 run tools/k6/load-test.js                                   # API latency test
make chaos-kill-leader                                          # and other hack/chaos/*.sh drills
```

## Architecture

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
   +-- AcceleratorJob / AcceleratorPool / InferenceService / QuotaPolicy CRDs
   +-- Admission webhook (quota enforcement)
   +-- Leader-elected reconcilers
        |
 Kubernetes Workers
   +-- Mock device plugin (DaemonSet)
   +-- Scheduler extender (bin-pack/spread scoring, gang scheduling)
   +-- Job pods
   +-- Mock inference backend (vLLM-shaped /v1/completions)
```

## Repo layout

| Path | What |
|---|---|
| `api/v1alpha1/` | CRD types |
| `cmd/` | `apiserver`, `operator`, `scheduler` (extender), `devplugin`, `mockinference` |
| `internal/` | controllers, webhook, queue, store, service layer, REST/gRPC, metrics, tracing |
| `deploy/helm/` | main deploy path, full stack |
| `deploy/kustomize/`, `deploy/argocd/`, `deploy/terraform/`, `deploy/kyverno/` | GitOps, IaC, policy |
| `tools/jobgen/`, `tools/k6/` | load generators |
| `hack/chaos/` | reproducible failure-injection drills |
| `docs/` | SLOs, scale-test results, postmortems |

## Results

Real numbers from `docs/scale-test-results.md`, not projections:

- 1,346 jobs submitted across two load-test runs, 0 failures
- API latency p95 ~120ms under load
- Full drain of a backlog 40x over available accelerator capacity
- A real incident found via chaos testing (NATS JetStream defaulting to
  ephemeral storage), fixed and documented in `docs/postmortems/`

## Docs

- [`docs/openapi.yaml`](docs/openapi.yaml): REST API spec
- [`docs/slo.md`](docs/slo.md): SLIs/SLOs
- [`docs/scale-test-results.md`](docs/scale-test-results.md): load test results
- [`docs/postmortems/`](docs/postmortems/): incident writeups

## Design notes

Custom scheduler extender instead of the scheduler-plugins framework:
kind's Kubernetes version at build time was new enough to carry real
version-skew risk with the plugin framework.

Kustomize is scoped to the cluster add-on layer only (priorityclasses,
devplugin, scheduler), not the whole app tier, so it doesn't duplicate what
Helm owns.

gRPC has no auth interceptor yet. Documented gap, not an oversight.
