#!/usr/bin/env bash
# confirm.sh - read-only evidence collection for one k8s-triage hypothesis.
#
# Every kubectl call goes through `k`, which refuses anything except
# get / describe / logs / top / events / auth can-i / api-resources / version.
# Nothing in this script can change the cluster.
#
# Usage:
#   confirm.sh pod        <ns> <pod>     status, conditions, last state, events, recent logs
#   confirm.sh probe      <ns> <pod>     readiness/liveness spec, Unhealthy events, logs, endpoints
#   confirm.sh pullsecret <ns> <pod>     referenced pull secrets (pod + SA) and whether they exist
#   confirm.sh pending    <ns> <pod>     scheduling events, requests, selectors/tolerations, Karpenter claims
#   confirm.sh deps       <ns> <pod>     what this pod talks to (env/config hints) and state of backends in the ns
#   confirm.sh node       <node>         conditions, capacity, pods on node, recent node events
#   confirm.sh ns         <ns>           namespace overview: not-Ready pods, workloads, recent warnings
#
# Env: LOG_LINES (default 60), KUBECTL (default kubectl)

set -uo pipefail
LOG_LINES="${LOG_LINES:-60}"
KUBECTL="${KUBECTL:-kubectl}"

guard() {
  case "${1:-}" in
    get|describe|logs|top|events|api-resources|version) return 0 ;;
    auth) [[ "${2:-}" == "can-i" ]] && return 0 ;;
  esac
  echo "confirm.sh: refused non-read kubectl: $*" >&2
  return 97
}
# k  = display: errors are shown inline so the reader sees NotFound/Forbidden
k()  { guard "$@" || return 97; "$KUBECTL" "$@" --request-timeout=20s 2>&1; }
# kq = capture: stderr dropped so error text never becomes a value
kq() { guard "$@" || return 97; "$KUBECTL" "$@" --request-timeout=20s 2>/dev/null; }

hdr() { printf '\n=== %s ===\n' "$*"; }
need() { [[ -n "${1:-}" ]] || { echo "usage: $USAGE" >&2; exit 2; }; }
has_crd() { kq api-resources -o name 2>/dev/null | grep -qx "$1"; }

pod_events() {
  hdr "events for pod $2 (newest last)"
  k get events -n "$1" --field-selector "involvedObject.name=$2" \
    --sort-by=.lastTimestamp -o custom-columns='LAST:.lastTimestamp,TYPE:.type,REASON:.reason,COUNT:.count,MESSAGE:.message' | tail -20
}

pod_logs() {
  local ns="$1" pod="$2" c
  for c in $(kq get pod "$pod" -n "$ns" -o jsonpath='{.spec.containers[*].name}'); do
    hdr "logs $pod [$c] last $LOG_LINES"
    k logs "$pod" -n "$ns" -c "$c" --tail="$LOG_LINES" | tail -"$LOG_LINES"
    if [[ "$(kq get pod "$pod" -n "$ns" -o jsonpath="{.status.containerStatuses[?(@.name==\"$c\")].lastState.terminated.reason}")" != "" ]]; then
      hdr "PREVIOUS logs $pod [$c] (last crash)"
      k logs "$pod" -n "$ns" -c "$c" --previous --tail="$LOG_LINES" | tail -"$LOG_LINES"
    fi
  done
}

cmd="${1:-}"; shift || true
case "$cmd" in
  pod)
    USAGE="confirm.sh pod <ns> <pod>"; need "${1:-}"; need "${2:-}"
    ns="$1"; pod="$2"
    hdr "pod $ns/$pod"
    k get pod "$pod" -n "$ns" -o wide
    hdr "conditions / container state"
    k get pod "$pod" -n "$ns" -o jsonpath='{range .status.conditions[*]}{.type}={.status} {.reason}{"\n"}{end}{range .status.containerStatuses[*]}container={.name} ready={.ready} restarts={.restartCount} state={.state} last={.lastState}{"\n"}{end}'
    pod_events "$ns" "$pod"
    pod_logs "$ns" "$pod"
    ;;

  probe)
    USAGE="confirm.sh probe <ns> <pod>"; need "${1:-}"; need "${2:-}"
    ns="$1"; pod="$2"
    hdr "probe spec"
    k get pod "$pod" -n "$ns" -o jsonpath='{range .spec.containers[*]}container={.name}{"\n"}  readiness={.readinessProbe}{"\n"}  liveness={.livenessProbe}{"\n"}  startup={.startupProbe}{"\n"}  ports={.ports}{"\n"}{end}'
    hdr "ready state"
    k get pod "$pod" -n "$ns" -o wide
    pod_events "$ns" "$pod"
    pod_logs "$ns" "$pod"
    hdr "endpoints in $ns (is this pod in or out of rotation?)"
    k get endpoints -n "$ns" -o wide
    ;;

  pullsecret)
    USAGE="confirm.sh pullsecret <ns> <pod>"; need "${1:-}"; need "${2:-}"
    ns="$1"; pod="$2"
    sa="$(kq get pod "$pod" -n "$ns" -o jsonpath='{.spec.serviceAccountName}')"
    pod_secrets="$(kq get pod "$pod" -n "$ns" -o jsonpath='{.spec.imagePullSecrets[*].name}')"
    sa_secrets="$(kq get sa "${sa:-default}" -n "$ns" -o jsonpath='{.imagePullSecrets[*].name}')"
    hdr "pull secrets referenced"
    echo "serviceAccount=${sa:-default}"
    echo "pod.spec.imagePullSecrets=[${pod_secrets}]"
    echo "sa.imagePullSecrets=[${sa_secrets}]"
    hdr "do they exist in $ns?"
    for s in $pod_secrets $sa_secrets; do
      if kq get secret "$s" -n "$ns" -o name >/dev/null; then
        echo "  EXISTS   $s  type=$(kq get secret "$s" -n "$ns" -o jsonpath='{.type}')"
      else
        echo "  MISSING  $s"
      fi
    done
    hdr "images and whether they are already pulled"
    k get pod "$pod" -n "$ns" -o jsonpath='{range .status.containerStatuses[*]}{.name}  image={.image}  imageID={.imageID}  ready={.ready}{"\n"}{end}'
    k get pod "$pod" -n "$ns" -o jsonpath='imagePullPolicy={.spec.containers[*].imagePullPolicy}  node={.spec.nodeName}{"\n"}'
    echo "(imageID set + ready=true => running from an already-pulled image; missing secret is latent until the pod lands on a node without the image, or pullPolicy=Always restarts it)"
    ;;

  pending)
    USAGE="confirm.sh pending <ns> <pod>"; need "${1:-}"; need "${2:-}"
    ns="$1"; pod="$2"
    pod_events "$ns" "$pod"
    hdr "requests / selectors / tolerations / affinity"
    k get pod "$pod" -n "$ns" -o jsonpath='{range .spec.containers[*]}{.name} requests={.resources.requests} limits={.resources.limits}{"\n"}{end}nodeSelector={.spec.nodeSelector}{"\n"}tolerations={.spec.tolerations}{"\n"}affinity={.spec.affinity}{"\n"}pvcs={.spec.volumes[*].persistentVolumeClaim.claimName}{"\n"}'
    for pvc in $(kq get pod "$pod" -n "$ns" -o jsonpath='{.spec.volumes[*].persistentVolumeClaim.claimName}'); do
      hdr "pvc $pvc"; k get pvc "$pvc" -n "$ns" -o wide
    done
    if has_crd nodeclaims.karpenter.sh; then
      hdr "karpenter nodeclaims (not Ready / recent)"
      k get nodeclaims -o wide | head -30
    fi
    hdr "allocatable vs requested (top nodes)"
    k top nodes 2>/dev/null | head -20
    ;;

  deps)
    USAGE="confirm.sh deps <ns> <pod>"; need "${1:-}"; need "${2:-}"
    ns="$1"; pod="$2"
    hdr "connection hints in env (names only; values of secretKeyRef are not read)"
    k get pod "$pod" -n "$ns" -o jsonpath='{range .spec.containers[*].env[*]}{.name}={.value}{"\n"}{end}' \
      | grep -Ei 'host|url|uri|endpoint|elastic|es_|redis|mongo|postgres|mysql|db_|kafka|rabbit|svc|service' \
      | sed -E 's/(password|passwd|secret|token|apikey|api_key)=.*/\1=<redacted>/I; s#://[^/@ ]+@#://<redacted>@#g' | head -40
    hdr "services in $ns"
    k get svc -n "$ns" -o wide
    hdr "endpoints in $ns (empty ENDPOINTS = backend down)"
    k get endpoints -n "$ns"
    hdr "statefulsets and deployments readiness in $ns"
    k get sts,deploy -n "$ns" -o wide
    ;;

  node)
    USAGE="confirm.sh node <node>"; need "${1:-}"
    node="$1"
    hdr "node $node"
    k get node "$node" -o wide --show-labels | cut -c1-600
    hdr "conditions"
    k get node "$node" -o jsonpath='{range .status.conditions[*]}{.type}={.status} {.reason}: {.message}{"\n"}{end}'
    hdr "taints / capacity / allocatable"
    k get node "$node" -o jsonpath='taints={.spec.taints}{"\n"}capacity={.status.capacity}{"\n"}allocatable={.status.allocatable}{"\n"}'
    hdr "pods on node"
    k get pods -A --field-selector "spec.nodeName=$node" -o wide
    hdr "node events"
    k get events -A --field-selector "involvedObject.name=$node" --sort-by=.lastTimestamp | tail -20
    k top node "$node" 2>/dev/null
    ;;

  ns)
    USAGE="confirm.sh ns <ns>"; need "${1:-}"
    ns="$1"
    hdr "pods in $ns that are not fully Ready"
    k get pods -n "$ns" -o wide | awk 'NR==1 || $2 !~ /^([0-9]+)\/\1$/ || $3 != "Running"'
    hdr "workloads"
    k get deploy,sts,ds -n "$ns" -o wide
    hdr "warning events (newest last)"
    k get events -n "$ns" --field-selector type=Warning --sort-by=.lastTimestamp \
      -o custom-columns='LAST:.lastTimestamp,REASON:.reason,OBJECT:.involvedObject.name,COUNT:.count,MESSAGE:.message' | tail -25
    ;;

  *)
    sed -n '2,20p' "$0"; exit 2 ;;
esac
