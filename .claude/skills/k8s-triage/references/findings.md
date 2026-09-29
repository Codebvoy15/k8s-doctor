# Finding types

How to read each k8s-doctor finding (`title`), and what evidence settles
it. Titles in the pods/nodes categories come from k8s-doctor's checks.
Event titles are the raw Kubernetes event `reason`.

## Pod findings

**CrashLoopBackOff** (score 90)
Container starts, exits, and backs off.
Confirm: `confirm.sh pod` and read the **PREVIOUS** logs; the crash reason
is there, not in the current logs.
Common causes: a missing config or env var, a bad secret, a dependency
unreachable at startup, the wrong command, or a failing liveness probe
killing a slow starter (see `lastState.terminated.reason`/`exitCode`:
137 = killed, often OOM or liveness; 1 = app error).

**OOMKilled** (score 85)
`lastState.terminated.reason=OOMKilled`. Compare the memory limit with
actual use (`kubectl top pod`). It's a real problem if restarts keep
climbing. A single old OOM on a now-stable pod is DEGRADED at most.

**ImagePullBackOff / ErrImagePull** (score 75)
Check the image name and tag, pull-secret existence (`confirm.sh pullsecret`),
and registry reachability. In this fleet, most images come from Artifactory
(see fleet-context.md). The event message distinguishes `not found` (bad
tag), `unauthorized` (credentials), and timeouts (network or proxy).

**Pending Pod** (score 70-85)
k8s-doctor includes the FailedScheduling message in `detail`.
`Insufficient cpu/memory` means capacity or Karpenter. `had taint` means
tolerations or the NodePool. `volume node affinity conflict` means a PVC in
a different AZ. `unbound PersistentVolumeClaim` means storage.
Confirm: `confirm.sh pending`.

**Stuck Terminating** (score 60)
Usually finalizers, an unreachable node, or a volume that won't detach.
Check whether the node is NotReady first. k8s-doctor's remedy is a force
delete; that stays a human decision (it can orphan volumes, and Portworx
volumes need care).

**Frequent Restarts** (score 65)
Restarts > 3 on a container. Judge by trend: is it still restarting now
(recent `lastState` timestamp), or are these old restarts? Check it with
the CrashLoopBackOff approach.

## Event findings (score 40, or 70 if count > 10)

**Unhealthy**
The probe failed. The detail says readiness, liveness, or startup, plus
the error:
- HTTP 5xx means the app or its upstream is failing. Apply correlation rule 2.
- `connection refused` means the process is not listening yet or crashed.
  Check the port and `initialDelaySeconds`.
- `context deadline exceeded` / timeout means the app is slow, CPU is
  throttled, or the probe timeout is too tight.
A readiness failure takes the pod out of Service endpoints without
restarting it; a liveness failure restarts it.

**FailedToRetrieveImagePullSecret**
A referenced pull secret (pod spec or ServiceAccount) does not exist in the
namespace. It's latent if the pod is Ready, and impacting if the pod can't
pull. Confirm: `confirm.sh pullsecret` shows MISSING/EXISTS and whether
the image is already pulled. Fix: recreate the secret (the usual
provisioning path for that namespace) or remove the stale reference.

**BackOff**
"Back-off restarting failed container" is the restart side of
CrashLoopBackOff. "Back-off pulling image" is the pull side.

**FailedScheduling**
Same interpretation as Pending Pod.

**FailedMount / FailedAttachVolume**
Storage. Identify the PVC, its StorageClass, and whether the volume is
attached elsewhere. For Portworx, see fleet-context.md.

**FailedCreatePodSandBox**
The CNI failed to set up networking, often VPC CNI IP exhaustion or an
aws-node problem on that node. Apply correlation rule 3.

**Evicted**
Node pressure. Identify which resource from the message and check the node.

**NodeNotReady / Rebooted / NodeHasDiskPressure**
Node-level. Use the node findings below.

## Node findings

**Node NotReady** (score 95)
`node pressure -o json` gives `reason`, `root_cause`, and `evidence` per
node, plus Karpenter/Spot flags. Distinguish kubelet dead, EC2 impaired or
Spot interrupted, pressure, and a Karpenter node that never initialized.

**MemoryPressure / DiskPressure / PIDPressure** (88+)
The node is evicting or about to. `node pressure` includes top pods and
disk usage. Name the pod or path consuming the resource.
