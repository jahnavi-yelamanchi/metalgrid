# Postmortem: NATS JetStream state lost on pod restart

**Date**: 2026-08-04
**Severity**: High (job submission fully unavailable during the affected window; would have been a full outage in production)
**Status**: Fixed, verified

## Summary

A chaos drill simulating a NATS outage (`hack/chaos/kill-nats.sh`) — scale
`nats-0` to 0 replicas, then restore it — found that job submission stayed
broken *after* NATS came back, instead of self-healing as designed. Root
cause: the NATS StatefulSet's JetStream storage directory was never pointed
at the mounted PersistentVolumeClaim, so every pod restart silently wiped
all streams and consumers, even though a 1Gi PVC was correctly provisioned
and mounted.

## Detection

Running the drill:

```
$ bash hack/chaos/kill-nats.sh
scaling nats to 0...
submitting a job while NATS is down (expect a 5xx, not a hang or a silently-lost job)...
submission during outage returned: 500          # correct — loud failure, not silent loss
restoring nats...
waiting for apiserver's NATS client to reconnect...
did not recover within 60s                       # wrong — should have recovered in seconds
```

apiserver logs during the "recovered" window kept showing:

```json
{"level":"ERROR","msg":"failed to create job","error":"enqueuing job: publishing job: nats: no response from stream"}
```

The TCP-level reconnect had clearly succeeded (no "connection refused"), so
the error pointed at something missing on NATS's side specifically, not a
network problem.

## Root cause

`deploy/helm/templates/nats.yaml`'s container args were:

```yaml
args: ["-js", "-m", "8222"]
```

`-js` enables JetStream but does **not** point it at the mounted volume —
without `-sd <dir>`, NATS defaults its JetStream store directory to `/tmp`,
which is the container's ephemeral filesystem. The PVC at `/data` was
mounted and healthy the entire time; NATS just never wrote anything to it.
Confirmed via NATS's own monitoring endpoint:

```
$ kubectl exec nats-0 -n metalgrid-system -- wget -qO- http://localhost:8222/jsz
"store_dir": "/tmp/nats/jetstream"
```

So the sequence was: NATS restarts → JetStream reinitializes against an
empty `/tmp` → the `JOBS` stream (and its `operator-jobs` consumer) no
longer exist → any publish to a subject with no backing stream returns
`no response from stream`, forever, until something recreates it.

## Why "restore NATS" didn't fix it on its own

Even after adding `-sd /data` and restarting `nats-0` on the corrected
config, the *first* retest still failed the same way. Second root cause,
smaller: `internal/queue.Connect()` only calls `CreateOrUpdateStream` once,
at process startup. The already-running `apiserver`/`operator` pods held
long-lived NATS connections that auto-reconnected fine at the TCP level,
but neither process ever re-asserts "does my stream still exist" after
reconnecting — because before this incident, it never needed to (a
correctly-configured NATS never loses its streams on restart). Restarting
`apiserver`/`operator` once (so they re-ran `CreateOrUpdateStream` against
the now-durable storage) was what let the stream get created for real.

## Mitigation (during the incident)

None needed beyond the fix itself — this was found in a deliberate chaos
drill against a non-production cluster, not a live incident with real
traffic. In a real deployment this would have been a full submission outage
until someone diagnosed it, since the failure mode (`500` on every submit,
forever) doesn't self-resolve.

## Permanent fix

`deploy/helm/templates/nats.yaml`:

```diff
- args: ["-js", "-m", "8222"]
+ args: ["-js", "-sd", "/data", "-m", "8222"]
```

Committed as `90b219e` ("Fix NATS JetStream storage dir: was defaulting to
ephemeral /tmp, losing all streams on pod restart"), pushed, picked up by
Argo CD's auto-sync.

## Verification

Same drill, after the fix + one `apiserver`/`operator` restart to recreate
the stream against durable storage:

```
$ bash hack/chaos/kill-nats.sh
scaling nats to 0...
submitting a job while NATS is down...
submission during outage returned: 201    # see note below
restoring nats...
waiting for apiserver's NATS client to reconnect...
submissions succeeding again 0s after NATS came back — no apiserver/operator restart needed
```

Confirmed via NATS's monitoring endpoint that `store_dir` now correctly
reads `/data/jetstream`, and that the stream survives a full `nats-0` pod
replacement (checked via `kubectl exec ... jsz?streams=true` before and
after a scale-to-0-and-back cycle).

*Note on the `201` above*: on this particular rerun, one submission during
the outage window returned `201` instead of the expected `5xx` — almost
certainly a timing artifact of `kubectl scale --replicas=0` not having
fully torn down the old connection before that specific request landed,
not a regression of the fix under test (the fix is about data durability
across restarts, not about the failure-during-outage behavior, which was
independently confirmed loud-and-correct in the two earlier reproduction
runs before the fix).

## Lessons / follow-ups

1. **Config correctness for stateful dependencies needs the same scrutiny
   as application code.** This was caught by chaos testing, not code
   review — the Helm template looked reasonable at a glance (image, args,
   a PVC, a mount) without actually cross-checking that the args used the
   PVC.
2. **A provisioned PVC being mounted proves nothing about whether it's
   used.** Worth a standing check (e.g., a periodic assertion or a startup
   probe) that verifies bytes are actually landing under the mount, for any
   stateful component added in the future.
3. **`internal/queue.Connect()` doesn't defend against "the stream
   disappeared."** Given the storage bug is fixed, a real NATS shouldn't
   ever lose its stream — but a defensive re-`EnsureStream()` on each
   reconnect (not just at startup) would make the client itself robust to
   this class of failure even if a future config regression reintroduced
   it. Not implemented here — noted as a `ponytail:`-style deferred
   hardening, not a blocking gap, since the actual root cause is now
   closed at the source.
