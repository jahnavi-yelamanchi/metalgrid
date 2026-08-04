# MetalGrid

Accelerator-aware Kubernetes control plane. Engineers submit AI/ML workloads
through a REST/gRPC API or CLI; MetalGrid schedules them onto simulated
accelerator hardware, tracks execution, retries failures, enforces per-team
quotas, and exposes metrics/traces for the whole pipeline.

No real GPUs/Tenstorrent hardware — accelerators are a mock Kubernetes
device plugin (`metalgrid.dev/accelerator`), so the whole thing runs for
$0 on a laptop via [kind](https://kind.sigs.k8s.io/).

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

Prometheus + Grafana + OpenTelemetry (Jaeger) for observability. Helm +
Argo CD for GitOps deployment; Kyverno for policy enforcement; GitHub
Actions builds, scans, SBOMs, and cosign-signs every image.

## Features

- **CRDs & operator**: `AcceleratorJob`, `AcceleratorPool`, `QuotaPolicy`,
  `InferenceService` — reconciled with finalizers, owner references, leader
  election, and a validating admission webhook.
- **Scheduling**: bin-pack vs. spread node scoring and gang (all-or-nothing)
  scheduling via a custom kube-scheduler HTTP extender; native
  `PriorityClass`-based preemption; per-team fair-share dequeue over NATS.
- **Reliability**: exponential backoff retry, checkpoint/resume via PVC,
  dead-letter queue, idempotent submission, graceful cancellation/timeout.
- **Platform API**: REST + gRPC (one shared service layer), JWT auth,
  per-team rate limiting, cursor pagination, audit log.
- **Observability**: Prometheus metrics, Grafana dashboard, OpenTelemetry
  traces, SLO doc + alerting rules.
- **Deployment**: Helm chart, Kustomize overlay, Argo CD GitOps, Kyverno
  policies (no `:latest`, signed images, resource limits required),
  Terraform, multi-arch CI with Trivy scanning and keyless cosign signing.

## Quickstart

Requires: `go`, `docker`, `kind`, `kubectl`, `helm`. (`brew install go kind
kubectl helm colima docker docker-compose docker-buildx` on macOS, plus
`colima start`.)

```bash
make kind-up   # 4-node kind cluster (1 control-plane + 3 workers)
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

## Testing it

```bash
make test                              # go test ./...
python3 tools/jobgen/jobgen.py --count 500 --concurrency 30   # load test
k6 run tools/k6/load-test.js           # API latency test
make chaos-kill-leader                 # and the other hack/chaos/*.sh drills
```

See `docs/scale-test-results.md` for real measured numbers and
`docs/postmortems/` for a real incident found via chaos testing.

## Repo layout

| Path | What |
|---|---|
| `api/v1alpha1/` | CRD types |
| `cmd/` | `apiserver`, `operator`, `scheduler` (extender), `devplugin`, `mockinference` |
| `internal/` | controllers, webhook, queue, store, service layer, REST/gRPC, metrics, tracing |
| `deploy/helm/` | the real deploy path — full stack |
| `deploy/kustomize/`, `deploy/argocd/`, `deploy/terraform/`, `deploy/kyverno/` | GitOps/IaC/policy |
| `tools/jobgen/`, `tools/k6/` | load generators |
| `hack/chaos/` | reproducible failure-injection drills |
| `docs/` | SLOs, scale-test results, postmortems |
