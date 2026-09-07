#!/usr/bin/env bash
# Package a reproducible, installable Helm chart without publishing it.
# Publishing remains the responsibility of .github/workflows/release.yaml.

set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
CHART_DIR="$ROOT_DIR/deployment/operator"
VERSION=""
OUTPUT_DIR="$ROOT_DIR/dist"

usage() {
  cat <<'EOF'
Usage: ./scripts/package-operator.sh [OPTIONS]

Options:
  --version VERSION     Chart and app version (default: Chart.yaml version)
  --output-dir DIR      Package destination (default: ./dist)
  -h, --help            Show this help

The script runs helm lint and writes mycedrive-operator-<version>.tgz plus a
SHA-256 checksum. It does not push images, modify a Helm repository, or create
a release.
EOF
}

while [[ $# -gt 0 ]]; do
  case "$1" in
    --version) VERSION="$2"; shift 2 ;;
    --output-dir) OUTPUT_DIR="$2"; shift 2 ;;
    -h|--help) usage; exit 0 ;;
    *) echo "Unknown option: $1" >&2; usage >&2; exit 1 ;;
  esac
done

command -v helm >/dev/null || { echo "helm is required" >&2; exit 1; }

if [[ -z "$VERSION" ]]; then
  VERSION="$(awk -F': ' '$1 == "version" { print $2; exit }' "$CHART_DIR/Chart.yaml")"
fi
[[ "$VERSION" =~ ^[0-9]+\.[0-9]+\.[0-9]+([-.][0-9A-Za-z.-]+)?$ ]] \
  || { echo "invalid chart version: $VERSION" >&2; exit 1; }

mkdir -p "$OUTPUT_DIR"
helm lint "$CHART_DIR"
helm package "$CHART_DIR" --version "$VERSION" --app-version "$VERSION" --destination "$OUTPUT_DIR"

ARCHIVE="$OUTPUT_DIR/mycedrive-operator-$VERSION.tgz"
if command -v sha256sum >/dev/null; then
  sha256sum "$ARCHIVE" > "$ARCHIVE.sha256"
else
  shasum -a 256 "$ARCHIVE" > "$ARCHIVE.sha256"
fi

echo "Package:  $ARCHIVE"
echo "Checksum: $ARCHIVE.sha256"
