#!/usr/bin/env bash
# run-scenario.sh — Deploy a MyceDrive example scenario, seed observable
# state, migrate the pod to another node, and verify the state survived.
#
# Usage:
#   ./examples/run-scenario.sh SCENARIO [OPTIONS]
#
# Scenarios:
#   both           DMTCP process + overlay volume migration
#   process-only   DMTCP only (state lives in broker memory)
#   volume-only    overlay only (state lives in mosquitto.db)
#
# Options:
#   -n NAMESPACE    Namespace to run in (default: mig-ready)
#   -t TARGET_NODE  Destination node (default: auto-pick another Ready node)
#   --skip-deploy   Assume the scenario is already deployed and seeded state
#                   is in place; only trigger + verify the migration
#   --cleanup       Delete the scenario's resources and exit
#   -h              Show this help
#
# Prerequisites: operator installed (CRDs + REST API), images
# mycedrive/{dmtcp,go-agent,mosquitto}:dev available on the nodes, and at
# least two schedulable Linux nodes. See examples/scenarios/README.md.

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
NAMESPACE="mig-ready"
TARGET_NODE=""
SKIP_DEPLOY=false
CLEANUP=false
TOPIC="demo/state"
MIGRATION_TIMEOUT=600

usage() { sed -n '2,24p' "$0" | sed 's/^# \{0,1\}//'; exit "${1:-0}"; }

SCENARIO="${1:-}"
[ -z "$SCENARIO" ] && usage 1
shift
case "$SCENARIO" in
  both)         APP=mq-both ;;
  process-only) APP=mq-proc ;;
  volume-only)  APP=mq-vol  ;;
  -h|--help)    usage ;;
  *) echo "Unknown scenario '$SCENARIO' (want: both | process-only | volume-only)"; exit 1 ;;
esac

while [ $# -gt 0 ]; do
  case "$1" in
    -n) NAMESPACE="$2"; shift 2 ;;
    -t) TARGET_NODE="$2"; shift 2 ;;
    --skip-deploy) SKIP_DEPLOY=true; shift ;;
    --cleanup) CLEANUP=true; shift ;;
    -h|--help) usage ;;
    *) echo "Unknown option: $1"; usage 1 ;;
  esac
done

POD="${APP}-0"
DIR="${SCRIPT_DIR}/scenarios/${SCENARIO}"
say()  { printf '\n\033[1;34m==> %s\033[0m\n' "$*"; }
fail() { printf '\033[1;31mFAIL: %s\033[0m\n' "$*"; exit 1; }

###############################################################################
# Cleanup mode
###############################################################################
if $CLEANUP; then
  say "Deleting scenario '$SCENARIO' from namespace $NAMESPACE"
  kubectl delete -n "$NAMESPACE" -f "$DIR" --ignore-not-found
  for m in $(kubectl get migration -n "$NAMESPACE" -o name 2>/dev/null | grep "/${APP}-" || true); do
    kubectl delete -n "$NAMESPACE" "$m" --ignore-not-found
  done
  exit 0
fi

###############################################################################
# Preflight
###############################################################################
say "Preflight checks"
command -v kubectl >/dev/null || fail "kubectl not found"
if ! kubectl get ns >/dev/null 2>&1; then
  kubectl get ns 2>&1 | head -3
  fail "kubectl cannot reach the cluster — check credentials. On k3s hosts,
kubectl may default to the root-owned /etc/rancher/k3s/k3s.yaml; try
'export KUBECONFIG=\$HOME/.kube/config' or fix the file permissions."
fi
kubectl get crd migrations.mycedrive.io >/dev/null 2>&1 \
  || fail "Migration CRD not found — install the operator first (see README)"
kubectl get ns "$NAMESPACE" >/dev/null 2>&1 \
  || kubectl create namespace "$NAMESPACE"

mapfile -t NODES < <(kubectl get nodes \
  -o jsonpath='{range .items[?(@.spec.unschedulable!=true)]}{.metadata.name}{"\n"}{end}')
[ "${#NODES[@]}" -ge 2 ] || fail "need at least 2 schedulable nodes, found ${#NODES[@]}"
echo "Schedulable nodes: ${NODES[*]}"

###############################################################################
# Deploy and seed
###############################################################################
if ! $SKIP_DEPLOY; then
  say "Deploying scenario '$SCENARIO' ($APP) to namespace $NAMESPACE"
  kubectl apply -n "$NAMESPACE" -f "$DIR"
  kubectl rollout status "statefulset/$APP" -n "$NAMESPACE" --timeout=180s
fi
kubectl wait pod "$POD" -n "$NAMESPACE" --for=condition=Ready --timeout=120s

MESSAGE="migrated-$(date +%s)"
say "Seeding state: retained MQTT message '$MESSAGE' on topic $TOPIC"
kubectl exec -n "$NAMESPACE" "$POD" -c mosquitto -- \
  mosquitto_pub -h 127.0.0.1 -t "$TOPIC" -m "$MESSAGE" -r
if [ "$SCENARIO" != "process-only" ]; then
  echo "Waiting 6s for mosquitto autosave to flush the persistence DB..."
  sleep 6
fi

###############################################################################
# Trigger the migration
###############################################################################
SOURCE_NODE=$(kubectl get pod "$POD" -n "$NAMESPACE" -o jsonpath='{.spec.nodeName}')
if [ -z "$TARGET_NODE" ]; then
  for n in "${NODES[@]}"; do
    [ "$n" != "$SOURCE_NODE" ] && TARGET_NODE="$n" && break
  done
fi
[ -n "$TARGET_NODE" ] || fail "could not pick a target node different from $SOURCE_NODE"
[ "$TARGET_NODE" != "$SOURCE_NODE" ] || fail "target node equals source node ($SOURCE_NODE)"

say "Migrating $POD: $SOURCE_NODE → $TARGET_NODE"
MIG=$(kubectl create -n "$NAMESPACE" -o name -f - <<EOF
apiVersion: mycedrive.io/v1alpha1
kind: Migration
metadata:
  generateName: ${APP}-
spec:
  workloadName: ${APP}
  podName: ${POD}
  sourceNode: ${SOURCE_NODE}
  targetNode: ${TARGET_NODE}
EOF
)
MIG=${MIG#*/}
echo "Created Migration '$MIG'"

###############################################################################
# Watch phases until Completed/Failed
###############################################################################
say "Watching migration phases (timeout ${MIGRATION_TIMEOUT}s)"
deadline=$((SECONDS + MIGRATION_TIMEOUT))
last=""
while [ $SECONDS -lt $deadline ]; do
  phase=$(kubectl get migration "$MIG" -n "$NAMESPACE" -o jsonpath='{.status.phase}' 2>/dev/null || true)
  if [ "$phase" != "$last" ] && [ -n "$phase" ]; then
    echo "  phase: $phase"
    last="$phase"
  fi
  case "$phase" in
    Completed) break ;;
    Failed)
      kubectl get migration "$MIG" -n "$NAMESPACE" -o yaml | sed -n '/status:/,$p'
      fail "migration entered Failed phase" ;;
  esac
  sleep 3
done
[ "$last" = "Completed" ] || fail "migration did not complete within ${MIGRATION_TIMEOUT}s (last phase: ${last:-none})"

###############################################################################
# Verify
###############################################################################
say "Verifying pod placement and state"
kubectl wait pod "$POD" -n "$NAMESPACE" --for=condition=Ready --timeout=180s
NEW_NODE=$(kubectl get pod "$POD" -n "$NAMESPACE" -o jsonpath='{.spec.nodeName}')
echo "Pod $POD now runs on: $NEW_NODE"
[ "$NEW_NODE" = "$TARGET_NODE" ] || fail "pod is on $NEW_NODE, expected $TARGET_NODE"

GOT=$(kubectl exec -n "$NAMESPACE" "$POD" -c mosquitto -- \
  mosquitto_sub -h 127.0.0.1 -t "$TOPIC" -C 1 -W 20 2>/dev/null || true)
echo "Retained message after migration: '${GOT:-<none>}'"
if [ "$GOT" = "$MESSAGE" ]; then
  printf '\n\033[1;32mPASS: state survived the %s migration (%s → %s)\033[0m\n' \
    "$SCENARIO" "$SOURCE_NODE" "$TARGET_NODE"
else
  fail "expected retained message '$MESSAGE', got '${GOT:-<none>}'"
fi
