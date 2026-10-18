#!/usr/bin/env bash
# Fails when the CRDs in the Helm chart differ from the generated CRDs in config/crd/bases.
# To fix a difference, run: kubebuilder edit --plugins=helm/v2-alpha
set -euo pipefail
cd "$(dirname "$0")/.."

rc=0
for base in config/crd/bases/*.yaml; do
  name="$(sed -n 's/^  name: //p' "$base" | head -1)"
  chart="dist/chart/templates/crd/$name.yaml"
  if [ ! -f "$chart" ]; then
    echo "missing in the chart: $chart" >&2; rc=1; continue
  fi
  if ! diff -q <(sed -n '/^spec:/,$p' "$base") <(sed -n '/^spec:/,$p' "$chart" | sed '/^{{- end }}$/d') >/dev/null; then
    echo "the chart CRD differs from $base" >&2; rc=1
  fi
done
[ $rc -eq 0 ] && echo "chart CRDs are up to date" || echo "run: kubebuilder edit --plugins=helm/v2-alpha" >&2
exit $rc
