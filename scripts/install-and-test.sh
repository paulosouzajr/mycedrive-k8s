#!/usr/bin/env bash
# Build local MyceDrive images, make them available to a local Kubernetes
# cluster, install the operator Helm chart, then run a real migration scenario.

set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
NAMESPACE="mig-ready"
SCENARIO="process-only"
TAG="dev"
BUILD_IMAGES=true
LOAD_IMAGES=true
CLEANUP=false

usage() {
  cat <<'EOF'
Usage: ./scripts/install-and-test.sh [OPTIONS]

Options:
  --scenario NAME       process-only (default), volume-only, or both
  --namespace NAME      Namespace for the operator and scenario (default: mig-ready)
  --tag TAG             Local image tag to build/install (default: dev)
  --skip-build          Use images already present locally or in a registry
  --skip-image-load     Do not load local images; nodes must pull them from a registry
  --cleanup             Remove the scenario and Helm release after a successful test
  -h, --help            Show this help

Requirements: an already-running Kubernetes cluster with at least two
schedulable Linux nodes, kubectl, Helm, and Docker when building/loading
images. Image loading is automatic for kind and minikube. For another cluster,
push the images to a registry first and pass --skip-image-load.
EOF
}

fail() { echo "ERROR: $*" >&2; exit 1; }
say() { printf '\n==> %s\n' "$*"; }

while [[ $# -gt 0 ]]; do
  case "$1" in
    --scenario) SCENARIO="$2"; shift 2 ;;
    --namespace) NAMESPACE="$2"; shift 2 ;;
    --tag) TAG="$2"; shift 2 ;;
    --skip-build) BUILD_IMAGES=false; shift ;;
    --skip-image-load) LOAD_IMAGES=false; shift ;;
    --cleanup) CLEANUP=true; shift ;;
    -h|--help) usage; exit 0 ;;
    *) fail "unknown option: $1" ;;
  esac
done

case "$SCENARIO" in
  process-only|volume-only|both) ;;
  *) fail "invalid scenario '$SCENARIO'" ;;
esac

command -v kubectl >/dev/null || fail "kubectl is required"
command -v helm >/dev/null || fail "helm is required"
kubectl get ns >/dev/null 2>&1 || fail "kubectl cannot reach a Kubernetes cluster"

mapfile -t NODES < <(kubectl get nodes -o jsonpath='{range .items[?(@.spec.unschedulable!=true)]}{.metadata.name}{"\n"}{end}')
[[ "${#NODES[@]}" -ge 2 ]] || fail "need at least two schedulable nodes; found ${#NODES[@]}"

IMAGES=(
  "mycedrive/operator:${TAG}"
  "mycedrive/dmtcp:${TAG}"
  "mycedrive/mosquitto:${TAG}"
)

if $BUILD_IMAGES; then
  command -v docker >/dev/null || fail "docker is required unless --skip-build is used"
  say "Building operator, agent, DMTCP, and Mosquitto images"
  make -C "$ROOT_DIR" TAG="$TAG" build-agent build-dmtcp build-example build-operator
fi

if $LOAD_IMAGES; then
  command -v docker >/dev/null || fail "docker is required to load images; use --skip-image-load for registry-hosted images"
  if command -v kind >/dev/null && [[ -n "$(kind get clusters 2>/dev/null || true)" ]]; then
    KIND_CLUSTER="${KIND_CLUSTER:-$(kind get clusters | head -n 1)}"
    say "Loading images into kind cluster $KIND_CLUSTER"
    kind load docker-image --name "$KIND_CLUSTER" "${IMAGES[@]}"
  elif command -v minikube >/dev/null && minikube status --format '{{.Host}}' 2>/dev/null | grep -qx Running; then
    say "Loading images into minikube"
    minikube image load "${IMAGES[@]}"
  else
    fail "automatic image loading supports kind or minikube; push images to a registry and use --skip-image-load"
  fi
fi

say "Installing MyceDrive operator into $NAMESPACE"
helm upgrade --install mycedrive-operator "$ROOT_DIR/deployment/operator" \
  --namespace "$NAMESPACE" --create-namespace \
  --set image.repository=mycedrive/operator \
  --set image.tag="$TAG" \
  --set image.pullPolicy=IfNotPresent \
  --wait --timeout 180s

say "Running $SCENARIO migration scenario"
"$ROOT_DIR/examples/run-scenario.sh" "$SCENARIO" -n "$NAMESPACE"

if $CLEANUP; then
  say "Cleaning up test resources"
  "$ROOT_DIR/examples/run-scenario.sh" "$SCENARIO" -n "$NAMESPACE" --cleanup
  helm uninstall mycedrive-operator --namespace "$NAMESPACE"
fi

echo "PASS: operator installation and $SCENARIO migration scenario completed"
