#!/usr/bin/env python3
"""
summarize.py - turn k8s-doctor JSON into a ranked triage worklist.

Reads k8s-doctor `-o json` output (triage, report, predict, network, aws,
diagnose, node pressure) from stdin or a file, looks up live pod state with
read-only kubectl calls, and classifies every finding:

  IMPACTING  object is down right now (not Ready, crashing, pending, node NotReady)
  DEGRADED   object is Ready but flapping (probe failures, restarts)
  LATENT     config risk that is not hurting traffic yet (e.g. missing pull secret on a Running pod)
  STALE      the object no longer exists (event outlived the pod)
  UNKNOWN    could not check (not a pod, or kubectl unavailable)

It also emits correlation hints (same namespace, same node, shared secret,
stateful backend). It never changes the cluster: the only kubectl verb used is `get`.

Usage:
  ./k8s-doctor triage -n bom-stage -o json | python3 summarize.py
  python3 summarize.py triage.json --format json
  python3 summarize.py triage.json --no-enrich        # skip kubectl lookups

Python 3.6+ standard library only (no jq, no pip installs needed on the jump server).
"""
import argparse
import json
import re
import subprocess
import sys

TIER_BASE = {"IMPACTING": 100, "DEGRADED": 60, "UNKNOWN": 40, "LATENT": 20, "STALE": 10}

# Titles that are service-impacting on their own, regardless of pod lookup.
ALWAYS_IMPACTING = {
    "CrashLoopBackOff", "ImagePullBackOff", "Pending Pod", "Node NotReady",
    "MemoryPressure", "DiskPressure", "PIDPressure", "FailedScheduling",
}
# Event reasons that are only config risk when the pod is otherwise Ready.
LATENT_IF_READY = {
    "FailedToRetrieveImagePullSecret", "DNSConfigForming", "FailedGetScale",
    "FailedComputeMetricsReplicas",
}
# Event reasons that mean "flapping" when the pod is currently Ready.
DEGRADED_IF_READY = {"Unhealthy", "BackOff", "Frequent Restarts", "OOMKilled", "ProbeWarning"}

COUNT_RE = re.compile(r"^\[(\d+)x\]")
PARENS_RE = re.compile(r"\(([^()]+)\)")
STS_POD_RE = re.compile(r"^(.+)-(\d+)$")
DEPLOY_POD_RE = re.compile(r"^(.+)-[a-z0-9]{8,10}-[a-z0-9]{5}$")


def load_docs(text):
    """Parse one or more concatenated JSON documents (tolerates pre-v3.4 output)."""
    dec = json.JSONDecoder()
    docs, i, n = [], 0, len(text)
    while i < n:
        while i < n and text[i].isspace():
            i += 1
        if i >= n:
            break
        obj, i = dec.raw_decode(text, i)
        docs.append(obj)
    return docs


def extract_findings(docs):
    out, seen = [], set()

    def add(f, source):
        if not isinstance(f, dict) or f.get("score", 0) <= 0:
            return
        key = (f.get("title"), f.get("namespace", ""), f.get("object", ""))
        if key in seen:
            return
        seen.add(key)
        g = dict(f)
        g["source"] = source
        out.append(g)

    meta = {}
    for d in docs:
        if isinstance(d, list):  # pre-v3.4 bare findings array
            for f in d:
                add(f, "legacy")
            continue
        if not isinstance(d, dict):
            continue
        for k in ("command", "cluster", "namespace", "generated_at", "window"):
            if d.get(k) and k not in meta:
                meta[k] = d[k]
        for s in d.get("sections", []) or []:
            for f in s.get("findings", []) or []:
                add(f, s.get("name", "section"))
        for f in d.get("root_cause", []) if isinstance(d.get("root_cause"), list) else []:
            add(f, "root_cause")
        for f in d.get("top_findings", []) or []:
            add(f, "root_cause")
        for f in d.get("faults", []) or []:
            add(f, "diagnose")
        if isinstance(d.get("root_cause"), dict):
            meta["diagnose_root_cause"] = d["root_cause"]
        if d.get("changes"):
            meta["changes"] = d["changes"]
        for nd in d.get("nodes", []) or []:
            if not nd.get("ready", True) or nd.get("memory_pressure") or nd.get("disk_pressure") or nd.get("pid_pressure"):
                add({
                    "severity": nd.get("severity") or "CRITICAL", "category": "nodes",
                    "title": "Node NotReady" if not nd.get("ready", True) else (nd.get("reason") or "NodePressure"),
                    "detail": nd.get("root_cause", ""), "remedy": nd.get("remedy", ""),
                    "score": 95, "object": nd.get("node_name", ""),
                }, "node pressure")
    return out, meta


def kubectl_json(args):
    try:
        p = subprocess.run(["kubectl"] + args + ["-o", "json", "--request-timeout=20s"],
                           stdout=subprocess.PIPE, stderr=subprocess.PIPE, timeout=40)
    except (OSError, subprocess.TimeoutExpired):
        return None
    if p.returncode != 0:
        return None
    try:
        return json.loads(p.stdout.decode("utf-8", "replace"))
    except ValueError:
        return None


def pod_state(pod):
    st = pod.get("status", {}) or {}
    conds = {c.get("type"): c.get("status") for c in st.get("conditions", []) or []}
    restarts, waiting = 0, []
    for cs in st.get("containerStatuses", []) or []:
        restarts += cs.get("restartCount", 0)
        w = (cs.get("state") or {}).get("waiting")
        if w:
            waiting.append(w.get("reason", "Waiting"))
    spec = pod.get("spec", {}) or {}
    owner = ""
    for o in pod.get("metadata", {}).get("ownerReferences", []) or []:
        owner = "%s/%s" % (o.get("kind", ""), o.get("name", ""))
    return {
        "phase": st.get("phase", "Unknown"),
        "ready": conds.get("Ready") == "True",
        "restarts": restarts,
        "waiting": waiting,
        "node": spec.get("nodeName", ""),
        "owner": owner,
        "deleting": bool(pod.get("metadata", {}).get("deletionTimestamp")),
    }


def enrich(findings):
    namespaces = sorted({f.get("namespace") for f in findings if f.get("namespace")})
    pods = {}
    ok = True
    for ns in namespaces:
        data = kubectl_json(["get", "pods", "-n", ns])
        if data is None:
            ok = False
            continue
        for p in data.get("items", []):
            pods[(ns, p["metadata"]["name"])] = pod_state(p)
    return pods, ok


def classify(f, pods, enriched):
    title = f.get("title", "")
    ns, obj = f.get("namespace", ""), f.get("object", "")
    state = pods.get((ns, obj))
    f["pod_state"] = state
    if f.get("category") == "nodes" or title in ("Node NotReady", "MemoryPressure", "DiskPressure", "PIDPressure"):
        return "IMPACTING", "node condition"
    if state is None:
        if not enriched:
            return ("IMPACTING", "impacting by type") if title in ALWAYS_IMPACTING else ("UNKNOWN", "pod state not checked")
        if ns and obj and (DEPLOY_POD_RE.match(obj) or STS_POD_RE.match(obj)):
            return "STALE", "pod no longer exists; event outlived it"
        return "UNKNOWN", "object is not a pod or was not found"
    if state["phase"] == "Succeeded":
        return "STALE", "pod completed"
    if state["deleting"]:
        return "DEGRADED", "pod is terminating"
    if not state["ready"]:
        why = ",".join(state["waiting"]) or state["phase"]
        return "IMPACTING", "pod not Ready (%s)" % why
    if title in LATENT_IF_READY:
        return "LATENT", "pod is Ready; risk on next reschedule/new node"
    if title in DEGRADED_IF_READY or state["restarts"] > 3:
        return "DEGRADED", "pod Ready now but flapping (restarts=%d)" % state["restarts"]
    if title in ALWAYS_IMPACTING:
        return "DEGRADED", "was %s, pod Ready now" % title
    return "LATENT", "pod is Ready"


def correlate(items, changes=None):
    hints = []
    bad_ns = {it.get("namespace") for it in items if it["tier"] in ("IMPACTING", "DEGRADED")}
    for c in changes or []:
        if c.get("namespace") in bad_ns or c.get("correlated_fault"):
            hints.append("RECENT CHANGE in %s: %s/%s %s changed by %s at %s - correlation rule 1, check this first"
                         % (c.get("namespace", "?"), c.get("kind", ""), c.get("name", ""), c.get("field", ""),
                            c.get("changed_by") or "unknown", c.get("timestamp") or "?"))
    by_ns = {}
    for it in items:
        if it.get("namespace"):
            by_ns.setdefault(it["namespace"], []).append(it)
    for ns, group in sorted(by_ns.items()):
        bad = [g for g in group if g["tier"] in ("IMPACTING", "DEGRADED")]
        sts = [g for g in group if STS_POD_RE.match(g.get("object", "")) and g["tier"] != "STALE"]
        if bad and sts:
            for s in sts:
                others = [b["object"] for b in bad if b["object"] != s["object"]]
                if others:
                    state = "not Ready" if s["tier"] == "IMPACTING" else "Ready but has findings"
                    hints.append("%s: stateful pod %s (%s) may be a backend for %s - check it first"
                                 % (ns, s["object"], state, ", ".join(sorted(set(others)))))
        if len({g["object"] for g in bad}) >= 3:
            hints.append("%s: %d distinct objects affected - look for a shared cause (config change, dependency, quota)"
                         % (ns, len({g['object'] for g in bad})))
    by_node = {}
    for it in items:
        st = it.get("pod_state") or {}
        if st.get("node") and it["tier"] in ("IMPACTING", "DEGRADED"):
            by_node.setdefault(st["node"], set()).add("%s/%s" % (it["namespace"], it["object"]))
    for node, objs in sorted(by_node.items()):
        if len(objs) >= 2:
            hints.append("node %s hosts %d affected pods (%s) - run confirm.sh node %s"
                         % (node, len(objs), ", ".join(sorted(objs)), node))
    secrets = {}
    for it in items:
        if it.get("title") == "FailedToRetrieveImagePullSecret" and it["tier"] != "STALE":
            m = PARENS_RE.search(it.get("detail", ""))
            if m:
                for name in m.group(1).split(","):
                    secrets.setdefault(name.strip(), set()).add(it["namespace"])
    for name, nss in sorted(secrets.items()):
        hints.append("pull secret '%s' missing in namespace(s): %s" % (name, ", ".join(sorted(nss))))
    return hints


def main():
    ap = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    ap.add_argument("file", nargs="?", help="k8s-doctor JSON file (default: stdin)")
    ap.add_argument("--format", choices=["text", "json"], default="text")
    ap.add_argument("--no-enrich", action="store_true", help="do not call kubectl")
    ap.add_argument("--top", type=int, default=15, help="max items to print in text mode")
    a = ap.parse_args()

    text = open(a.file).read() if a.file else sys.stdin.read()
    if not text.strip():
        sys.exit("summarize.py: empty input - did k8s-doctor fail? (check stderr)")
    try:
        docs = load_docs(text)
    except ValueError as e:
        sys.exit("summarize.py: input is not JSON (%s). Run k8s-doctor with -o json (v3.4.0+)." % e)

    findings, meta = extract_findings(docs)
    pods, enriched = ({}, False) if a.no_enrich else enrich(findings)

    items = []
    for f in findings:
        tier, why = classify(f, pods, enriched)
        m = COUNT_RE.match(f.get("detail", ""))
        count = int(m.group(1)) if m else 1
        f["tier"], f["tier_reason"], f["event_count"] = tier, why, count
        f["priority"] = TIER_BASE[tier] + min(count, 50) // 5 + f.get("score", 0) // 10
        items.append(f)
    items.sort(key=lambda x: (-x["priority"], x.get("namespace", ""), x.get("object", "")))
    hints = correlate(items, meta.get("changes"))

    if a.format == "json":
        json.dump({"meta": meta, "enriched": enriched, "items": items, "hints": hints}, sys.stdout, indent=2)
        print()
        return

    print("TRIAGE WORKLIST  cluster=%s  ns=%s  generated=%s  pod-state=%s"
          % (meta.get("cluster", "?"), meta.get("namespace") or "all", meta.get("generated_at", "?"),
             "live" if enriched else "NOT CHECKED"))
    if not items:
        print("  no findings with score > 0")
    tally = {}
    for it in items:
        tally[it["tier"]] = tally.get(it["tier"], 0) + 1
    if tally:
        print("  " + "  ".join("%s=%d" % (t, tally[t]) for t in TIER_BASE if t in tally))
    for it in items[:a.top]:
        ref = "%s/%s" % (it.get("namespace", ""), it.get("object", "")) if it.get("namespace") else it.get("object", "")
        print("\n[%s p=%d] %s  %s" % (it["tier"], it["priority"], it.get("title", ""), ref))
        print("    why     %s" % it["tier_reason"])
        if it.get("detail"):
            print("    detail  %s" % it["detail"][:220])
        if it.get("remedy"):
            print("    k8sd    %s" % it["remedy"])
    if len(items) > a.top:
        print("\n  ... %d more (use --top or --format json)" % (len(items) - a.top))
    if hints:
        print("\nCORRELATION HINTS")
        for h in hints:
            print("  - " + h)
    changes = meta.get("changes") or []
    if changes:
        print("\nRECENT CHANGES (from diagnose, newest first as reported)")
        for c in changes[:10]:
            print("  %s  %s/%s ns=%s  field=%s  by=%s" % (c.get("timestamp") or "?", c.get("kind", ""), c.get("name", ""),
                  c.get("namespace", ""), c.get("field", ""), c.get("changed_by") or "?"))
            if c.get("old_value") or c.get("new_value"):
                print("      %s -> %s" % ((c.get("old_value") or "")[:60], (c.get("new_value") or "")[:60]))
        if len(changes) > 10:
            print("  ... %d more" % (len(changes) - 10))
    rc = meta.get("diagnose_root_cause")
    if rc:
        print("\nDIAGNOSE ROOT CAUSE  confidence=%s%%\n  %s" % (rc.get("confidence"), rc.get("conclusion", "")))
        if rc.get("evidence"):
            print("  evidence  %s" % rc["evidence"])


if __name__ == "__main__":
    main()
