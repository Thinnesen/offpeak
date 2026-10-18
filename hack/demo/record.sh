#!/usr/bin/env bash
# Records the README demos with VHS (Docker image, pinned by digest) on the demo cluster.
# Run hack/demo/cluster.sh first. Usage: hack/demo/record.sh [plan|run|fallback]...
set -euo pipefail
cd "$(dirname "$0")/../.."

VHS_IMAGE="ghcr.io/charmbracelet/vhs:v0.12.1@sha256:ea49a6a1c529be83153e88321892b5585964418f0b9055e8c1e0d732194234a3"
KUBECTL="$(readlink -f bin/kubectl)"
tapes=("$@")
[ ${#tapes[@]} -gt 0 ] || tapes=(plan run fallback)

for t in "${tapes[@]}"; do
  echo "Recording docs/demo/$t.gif"
  docker run --rm --network host \
    -u "$(id -u):$(id -g)" -e HOME=/tmp \
    -v "$PWD:/vhs" -w /vhs \
    -v "$KUBECTL:/usr/local/bin/kubectl:ro" \
    -e KUBECONFIG=/vhs/bin/demo-kubeconfig \
    "$VHS_IMAGE" "docs/demo/$t.tape"
done
