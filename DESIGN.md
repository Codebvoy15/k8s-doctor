# k8s-doctor: fleet detection engine

Status: Phase 1 implemented (`scan`, `fleet`), v3.6 calibrated on the first real fleet run; v3.7 adds resource-pressure attribution · Last updated: 2026-10-06

## Problem

We run 170+ EKS and Rancher clusters. Most of our incidents were not hard to
fix once found. They were hard because they were **invisible until they
hurt**:

| Incident | Silent for | What made it hard |
|---|---|---|
| ECR pull secret expired (custom scheduler) | 2.5 days | 45 pods Pending; nothing alerted on the secret |
| Registry secret with a malformed auth scope | ~3 years | Worked until a new node needed to pull |
| `dynatrace-bootstrapper-config` missing in several namespaces | 14+ days | Pods kept running; 10k warning events nobody read |
| Karpenter interruption queue not configured | until a Spot reclaim | Config gap, no runtime symptom |
| Storage operator RBAC after an EKS upgrade | until upgrade | Broke on upgrade, found by ticket |

The existing tools (k8s-doctor `triage`, `kubectl`) answer *"what is broken
right now in this cluster?"* across one cluster at a time. The question we
need answered daily is *"what will break, where, and how long has it been
waiting?"* across the whole fleet.

## Goals

1. Detect **latent** failures (things that will break on the next restart,
   reschedule or upgrade), not only active ones.
2. Rank by **impact** (is it down right now?), not by event counts.
3. Link **causes to symptoms** only when there is proof, and never guess.
4. Sweep the **whole fleet** in minutes; one bad cluster never stops the sweep.
5. **Read-only and safe by construction**; secret data is never collected.
6. **Testable offline**: every detector is covered by golden tests built from
   real incidents.

## Non-goals (for now)

- Auto-remediation. The tool proposes fix plans; humans run anything that
  mutates. Mutating steps are marked in the output and in tests.
- Replacing Dynatrace or Sysdig. This is config and state drift detection,
  not metrics or APM.
- Multi-user service. It's a CLI on the jump server first; a service comes
  later if the data model holds up.

## Architecture

```
kube context ──► collect ──► Snapshot ──► detectors (pure) ──► Findings ──► correlate ──► Report
                 (client-go,     (JSON,        missing-reference      fingerprint,     cause→symptom       render / JSON
                 read-only,      no secret     workload-unavailable   impact tier,     links via pods
                 paginated,      data)         node-unhealthy         evidence, plan   a cause blocks
                 per-context)                                                         │
fleet: N contexts, bounded parallelism, per-cluster timeout ────────────────────────────────► Patterns (same PatternKey across clusters/namespaces)
```

### Snapshot (`internal/model`)
A small, versioned, serializable view of the cluster (`schema_version: 1`):
pods (status, owner, every object they reference, container requests/limits,
QoS), nodes (conditions, curated labels, allocatable), secret and configmap
**names**, PVCs, service accounts, warning events, workloads (with the operator
CR or Helm release that owns them), HPAs, and one metrics-server usage sample.

- Fields added in v3.7 are additive and optional. A snapshot without them
  (older file, metrics-server missing) leaves the resource detectors silent:
  `resources: nil` means "not recorded", never "none set".

- It has **no Kubernetes library dependency**. The whole detection engine
  builds and tests with the standard library only.
- `collected` records which kinds were listed successfully. A detector
  never reports an object as missing when its kind wasn't collected (for
  example, when RBAC forbids listing secrets). This prevents false
  positives and is covered by a test.
- `k8s-doctor scan --save` writes one; `scan --from` replays it offline.
  Snapshots are how incidents become test fixtures.

### Collector (`internal/collect`)
- One list per kind, paginated (500 per page), all kinds in parallel.
- Secrets and ConfigMaps are listed via the **metadata API**: names only,
  so data never crosses the wire.
- Per-invocation kube context through config overrides. **The kubeconfig
  current-context is never modified**, which makes parallel fleet sweeps
  safe. (Legacy commands switch it; they now also accept `--context`.)
- A fast connectivity probe (15s) gives a clear error for unreachable
  clusters or expired credentials.
- Only a pod-list failure fails the snapshot; any other kind degrades
  gracefully and is recorded in `errors`.

### Detectors (`internal/detect`)
```go
type Detector interface {
    ID() string
    Description() string
    Detect(ix *model.Index) []Finding   // pure, deterministic (uses snapshot time as "now")
}
```
| Detector | Finds | Tiering |
|---|---|---|
| `missing-reference` | Pods depending on a missing Secret/ConfigMap/PVC/ServiceAccount (volumes, env, envFrom, image pull secrets, including ones inherited from the ServiceAccount) | IMPACTING if it blocks a pod now; otherwise LATENT. Infers "existed then deleted/unsynced around T" when pods predate the first failure by 15m or more |
| `workload-unavailable` | Workloads below desired availability, judged from the **controller's own status** (desired vs ready); pods only explain *why* (crash, OOM, image pull, unschedulable, probe, mount, no pods at all). Also chronic restarts on Ready pods | 0 ready = IMPACTING; some ready, or restarted/OOMKilled within the last hour = DEGRADED. Scaled to 0 or rollout leftovers are not outages |
| `node-unhealthy` | NotReady / pressure / cordoned nodes | NotReady with pods = IMPACTING; pressure = DEGRADED; cordoned = INFO |
| `failed-pods` | Pods left in phase Failed (evicted, node shutdown), classified by cause | Evicted for the workload's **own** storage limit (emptyDir sizeLimit, ephemeral-storage) = LATENT, since it recurs; node-pressure evictions and other leftovers = INFO |
| `resource-requests` | Workloads using far more memory/CPU than they request, or requesting none (BestEffort). Thresholds: >=1Gi memory or >=1 core used, and no request or usage >=2x the request. The fix targets whoever owns the pod template: an operator CR (CFK `spec.podTemplate.resources`; others via `kubectl explain`), Helm values, or the workload | LATENT: nothing is down, but nodes are packed blind and these pods are evicted first |
| `node-saturation` | Nodes at >=85% of allocatable memory or CPU, with requested vs used and the pods using unrequested capacity | Memory = LATENT (eviction/OOM next); CPU = INFO (compressible: throttles, never evicts) |
| `cpu-hotspot` | Deployments/StatefulSets where one replica uses >=1 core and >=50% of its node's CPU, with HPA state, replica imbalance (>=2x) and restarts | No HPA, or HPA at max = LATENT; HPA with headroom, or HPAs not collected = INFO |

**Resource sizing.** Proposed numbers are a starting point from one sample:
memory = peak replica x1.2 (rounded to 256Mi), also as the limit, since memory
is not compressible; CPU = **mean** across replicas (sizing every replica for
the busiest one reserves CPU the others never use), no CPU limit. Every plan
says to confirm against p95/peak history first.

**Finding identity.** `id` is a fingerprint of detector + cluster + subject,
so the same problem gets the same ID every run. Phase 2's findings store
relies on this (first seen, trend, resolved/regressed).

**Pattern key.** `pattern_key` leaves out the cluster and namespace (for
example `missing-reference|Secret|dynatrace-bootstrapper-config`), so the
same failure across the fleet aggregates into one line with one fix.

**Correlation.** A workload finding is `caused_by` a dependency finding only
if one of its unavailable pods is *provably blocked* by that dependency (a
pull-secret miss on a pod in ImagePullBackOff, for example). A pod that
merely references a missing secret is not linked. Neither is a pod on a
node with disk pressure, or a 502 readiness failure next to a broken
backend: that link needs the service graph (Phase 3), and we do not guess.
The cause inherits the symptom's tier and sorts first.

Resource pressure uses the same rule with arithmetic as the proof: a
`node-saturation` finding is `caused_by` a `resource-requests` or `cpu-hotspot`
finding only when one of that workload's pods is on the node and its measured
usage beyond its request, **for the same resource**, is listed. A memory-only
finding is never linked to a CPU-saturated node.

### Fleet (`internal/fleet`)
Bounded parallelism (default 8), a per-cluster timeout (default 2m),
contexts honored for cancellation, panics contained per cluster, stable
output order, and progress callbacks. Output: a per-cluster table plus
patterns (occurring 2+ times), sorted by worst tier, then breadth.

## Safety model

1. Read-only verbs only (`list`, `get`); there are no write code paths in
   `scan` or `fleet`. v3.7 additionally reads `horizontalpodautoscalers`
   (autoscaling) and `pods`/`nodes` in `metrics.k8s.io`; when either is
   forbidden, the kind is recorded as not collected and the dependent checks
   are skipped.
2. No secret data is collected (metadata API). Snapshot files are written
   `0600`.
3. Remediation steps that mutate are flagged `mutating: true` and shown
   with ✎. A test fails if a delete/drain/apply/patch/scale/rollback step
   is not flagged.
4. The kubeconfig is never modified by the new commands.

## Testing

- Golden tests: incident-derived snapshots (`dynatrace-gap`, `shop-stage`,
  `node-notready`, `secrets-forbidden`) produce reviewed golden reports.
  To change behavior, run `go test ./internal/detect -update` and review
  the diff.
- Behavior tests: no false positives without collection coverage;
  correlation only with proof; stable, unique fingerprints; mutating steps
  flagged; failed, hung and panicking clusters isolated in a fleet sweep.
- CI: gofmt, vet, `go test -race`, and the linux/amd64 build on every push.
  Releases are built by goreleaser on tag, with checksums.

## Calibration log

| Date | Real-run finding | Fix | Test |
|---|---|---|---|
| 2026-09-29 | Evicted pod of a Deployment scaled to 0/0 reported as an IMPACTING outage | Availability from controller status; Failed pods are tombstones classified by cause | `TestEvictedTombstoneOfScaledDownDeployment` |
| 2026-09-29 | 915-restart OOM loop reported as "recent OOMKills, down since 2m" | Chronic restart detection (restarts ≥ 10) on Ready pods | `TestChronicOOMIsCalledChronic` |
| 2026-09-29 | A single-cluster IMPACTING finding was hidden in the fleet view | "Needs attention" list of root causes not covered by patterns | `TestFleetShowsSingleClusterImpacting` |
| 2026-10-06 | Weekly review: 26 high-memory alerts on a dev cluster; a node at 100% memory had 41% requested. Root cause found by hand: a Kafka platform (CFK) deployed with no requests/limits | `resource-requests` + `node-saturation` with operator-aware fixes and node attribution | `TestMemoryOvercommitAttribution`, `TestResizeGoesWhereItSticks` |
| 2026-10-06 | Weekly review: 75 intermittent high-CPU alerts on a prod cluster; one API replica used 2.5 of 4 cores, no requests, no HPA, 2x replica imbalance | `cpu-hotspot` with HPA/imbalance/restart context; CPU sizing from the replica mean | `TestCPUHotspotScalingContext` |

Every false positive or understated finding from a real run becomes a row here and a test.

## Metrics we will track

- **Time-to-detect** for latent failures. Target: the next daily sweep, not
  the next incident. The ECR case (2.5 days) is the baseline.
- **Precision**: the share of IMPACTING findings that were real, reviewed
  weekly. Every false positive becomes a test.
- **Incident coverage**: the share of past incidents that a detector
  catches when replayed.
- **Fleet sweep duration** at 170 clusters and parallel=8.

## Roadmap

| Phase | Scope |
|---|---|
| 1 ✅ | Snapshot, detector framework, 3 detectors, `scan`, `fleet`, `--context`, CI, goreleaser |
| 2 | Findings store (SQLite): first seen, last seen, trend, resolved/regressed; daily digest; cordon and secret age; per-namespace owner routing (team report for app owners); p95/peak usage from Sysdig to replace the single metrics-server sample in sizing |
| 3 | Resource graph (service → endpoints → pods, owner chains) for RCA; pre/post-upgrade diff gate; detectors from incident history (Karpenter interruption queue, operator RBAC, addon image drift, ExternalName/CoreDNS) |
| 4 | MCP server over scan/fleet; k8s-triage skill on top; eval harness replaying incident snapshots; fix plans with server-side dry-run |

## Open questions

- Where the Phase 2 store lives: jump-server SQLite, or a shared store for
  the team.
- Snapshot retention and redaction policy before snapshots leave the jump
  server (names can be sensitive).
- Security approval for sending findings to an external LLM (Phase 4), with
  local Qwen as the fallback.
