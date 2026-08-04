# Kustomize

Manages the cluster add-on layer only: `PriorityClass`es, the mock device
plugin `DaemonSet`, and the scheduler extender `Deployment` — infrastructure
that has to exist on any cluster regardless of how the app tier gets
deployed. The app tier itself (apiserver, operator, Postgres, NATS, RBAC,
webhook TLS) is the Helm chart's job (`deploy/helm`) — that's the real,
live-tested deploy path for this project. Duplicating the app tier here too
would just be a second copy of the same YAML with no reason to exist.

```
kubectl apply -k deploy/kustomize/overlays/kind
```

`overlays/kind` patches in an explicit `imagePullPolicy: IfNotPresent` (so a
locally `kind load`-ed image is never mistakenly re-pulled from a registry)
on top of `base`.
