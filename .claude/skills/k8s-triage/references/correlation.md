# Correlation rules

Apply these in order. The first rule that fits gives the lead hypothesis.
Confirm it before reporting.

## 1. A recent change on the affected workload wins
`diagnose` JSON `changes[]` lists changes in the window, with `changed_by`
and `timestamp`. If a change touches the failing workload or its ConfigMap,
Secret, Service, HPA or PVC, and the first failure event comes after it,
lead with the change. Evidence: the diff line, plus the first failing event
timestamp. The fix is usually reverting that change. Name the rollback, but
leave it for a human to run.

## 2. Backend down, then frontend 5xx
A readiness or liveness failure with **502/503/504**, `connection refused`,
or timeouts means the container's proxy answered but its upstream did not.
Look in the same namespace for a stateful or backing pod (StatefulSet pod
`name-N`, databases, Elasticsearch, Redis, Kafka) that is not Ready, or has
empty endpoints.
- Check: `confirm.sh deps <ns> <frontend-pod>`, then `confirm.sh pod <ns> <backend-pod>`.
- Proof: frontend logs naming the backend host, and the backend being not Ready or having empty endpoints.
- **Report the backend as the root cause.** The frontend is impact.

A 502 with a healthy backend usually means the app process inside the pod
is dead or still starting behind its own proxy. Check `confirm.sh pod` logs
and the probe `initialDelaySeconds`.

## 3. Many pods on one node
Two or more affected pods on the same node point to the node, not the
apps. Suspects: pressure, kubelet, CNI IPs, disk, or a Spot interruption.
Run `confirm.sh node <node>` and `k8s-doctor node pressure -o json`. Report
the node condition as the root cause.

## 4. One secret or config missing across namespaces
The same pull secret or ConfigMap missing in several namespaces points to a
provisioning or rotation job that failed, not per-app mistakes. Report it
once, as a shared cause.

## 5. Everything pending at once
Many Pending pods cluster-wide, or across unrelated namespaces, points to
capacity or autoscaling. Check Karpenter NodeClaims and NodePool limits, and
any subnet IP exhaustion (see fleet-context.md). One Pending pod usually
means that pod's constraints: requests, selectors, a PVC in another AZ.

## 6. Noise filters (never a root cause on their own)
- `FailedToRetrieveImagePullSecret` on a **Ready** pod is LATENT: the image is
  already on the node. It only becomes impacting on a reschedule, a new
  node, or `imagePullPolicy: Always` plus a restart.
- Events on pods that no longer exist are STALE. Use them for history only.
- `Unhealthy` with a low count on a pod that is Ready now is a single blip
  unless it repeats.
- Warnings in `kube-system` DaemonSets during a node rotation are expected
  churn.

## 7. Conflicts
If the evidence points two ways, report the one with direct proof as the
root cause at medium confidence. List the other under UNCONFIRMED, with the
check that would settle it.
