---
name: k8s-triage
description: Triage Kubernetes problems on EKS/Rancher clusters with k8s-doctor - rank findings by real impact, correlate them, confirm with read-only checks, and write a root-cause summary. Use when asked to triage, diagnose, or explain what is wrong in a cluster or namespace, or when given k8s-doctor output.
---

# k8s-triage

Turn k8s-doctor findings into a verified root cause. k8s-doctor detects;
this skill decides what matters, proves it, and explains it.

## Hard rules (read first)

1. **Read-only, always.** Allowed: `k8s-doctor` diagnostic commands with
   `-o json`, `scripts/summarize.py`, `scripts/confirm.sh`, and kubectl
   `get / describe / logs / top / events / auth can-i`.
   Never run: `kubectl apply|create|delete|edit|patch|replace|scale|rollout|
   cordon|uncordon|drain|label|annotate|taint|exec|cp|port-forward|debug`,
   `k8s-doctor node cordon`, `k8s-doctor scale`, `k8s-doctor rollback`,
   or anything with `--force`. If a finding's `remedy` is a write command,
   put it in the report for a human to run. Do not run it, and do not
   offer to run it.
2. **Never read secrets.** Check whether a secret exists only with
   `scripts/confirm.sh pullsecret`, which reports EXISTS/MISSING and the type.
   Do not run `kubectl get secret` or `kubectl describe secret` directly.
3. **One cluster at a time.** `k8s-doctor --cluster X` switches the shell's
   current kube context. State which context you are on at the top of the
   report. Do not switch contexts mid-investigation unless asked.
4. **No jq on the jump server.** Use `python3` (`summarize.py`, or
   `python3 -m json.tool`).
5. **Evidence over guesses.** Every root-cause claim cites a command output
   you actually ran. If you could not confirm it, say "unconfirmed" and name
   the check that would confirm it.

## Workflow

### 1. Scope
Get the cluster/context and namespace from the request. If no namespace is
given, triage cluster-wide, then narrow to the namespaces that surface.
Default event window is 30m for triage and 1h for diagnose. Widen it
(`--window 2h`) if the user says the problem started earlier.

### 2. Collect
```bash
./k8s-doctor triage   -n <ns> -o json > /tmp/k8st-triage.json
./k8s-doctor diagnose -n <ns> -o json > /tmp/k8st-diagnose.json   # adds recent changes + correlated root cause
```
Add `node pressure -o json` if any finding is node-level, or if pods are
Pending or evicted. Use `network dns` / `network svc <name>` for
connectivity symptoms. k8s-doctor must be v3.4.0 or newer for
single-document JSON. `summarize.py` tolerates older output, but
`diagnose` JSON needs 3.4.0.

### 3. Rank
```bash
cat /tmp/k8st-triage.json /tmp/k8st-diagnose.json | python3 scripts/summarize.py
python3 scripts/summarize.py /tmp/k8st-triage.json --format json   # machine-readable
```
It looks up live pod state (read-only `kubectl get pods`) and assigns tiers:
IMPACTING > DEGRADED > UNKNOWN > LATENT > STALE. **Ignore k8s-doctor's raw
`score` for ordering.** Every warning event with count > 10 scores 70, so
it does not separate real outages from noise. Use the tiers.

Only IMPACTING and DEGRADED items lead the report. LATENT items go in a
separate "risks" section. Mention STALE items only if they explain history
(for example, a crash-looping pod that was since replaced).

### 4. Correlate
Before confirming anything, read `references/correlation.md` and apply its
rules to the worklist and the hints `summarize.py` printed. The goal is the
*upstream* cause. Symptoms that share a namespace, node, secret, or recent
change usually share a cause. If `diagnose` shows a change with
`correlated_fault` or a change on the same workload within the window,
treat that as the lead hypothesis.

### 5. Confirm
For the top 1-3 hypotheses, collect evidence with `scripts/confirm.sh`:

| Symptom | Check |
|---|---|
| pod not Ready / crashing | `confirm.sh pod <ns> <pod>` |
| readiness or liveness failures (Unhealthy) | `confirm.sh probe <ns> <pod>` |
| pull-secret / image pull errors | `confirm.sh pullsecret <ns> <pod>` |
| Pending / FailedScheduling | `confirm.sh pending <ns> <pod>` |
| a pod may depend on another (5xx, timeouts) | `confirm.sh deps <ns> <pod>` |
| node NotReady / pressure / several pods on one node | `confirm.sh node <node>` |
| broad namespace picture | `confirm.sh ns <ns>` |

`references/findings.md` explains how to read each finding type and what
evidence settles it. Read the entry for every finding type you report on.
Stop confirming once a hypothesis is proven or disproven. Don't dump every
check.

### 6. Report
Use this shape:

```
Context: <kube context>   Namespace: <ns|all>   Window: <30m/1h>   k8s-doctor <version>

ROOT CAUSE  (confidence: high|medium|low - one line on why)
<one or two sentences: what is broken, and why>

EVIDENCE
- <command> -> <the specific line that proves it>
- ...

IMPACT
- <what is down or degraded, since when if known>

FIX  (for a human to run - nothing below has been executed)
1. <exact command or action>
2. <verification command>

RISKS (not impacting yet)
- <LATENT items, one line each, with the fix>

UNCONFIRMED / NEXT CHECKS
- <anything you could not prove, and the check that would>
```

Confidence: **high** means the evidence directly shows the mechanism (for
example, a logs error naming the backend, or a MISSING secret on a pod that
cannot pull). **Medium** means strong correlation without direct proof.
**Low** means the best guess from symptoms only.

If nothing is IMPACTING or DEGRADED, say so plainly, list LATENT risks,
and stop.

## Stack context
`references/fleet-context.md` has the environment-specific patterns:
Karpenter, VPC CNI, Portworx, Artifactory pull secrets, and AL2023. Read
it when a finding touches nodes, scheduling, storage, networking, or image
pulls.
