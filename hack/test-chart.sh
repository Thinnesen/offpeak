#!/usr/bin/env bash
# Installs the Helm chart on a throwaway Kind cluster and checks that the controller works.
# It uses its own kubeconfig (bin/chart-kubeconfig) and always deletes the cluster.
set -euo pipefail
cd "$(dirname "$0")/.."

export PATH="$PWD/bin:$PATH"
export KUBECONFIG="$PWD/bin/chart-kubeconfig"
CLUSTER=offpeak-chart
IMG_REPO=offpeak
IMG_TAG=chart

cleanup() { kind delete cluster --name "$CLUSTER" --kubeconfig "$KUBECONFIG" >/dev/null 2>&1 || true; }
trap cleanup EXIT

helm lint dist/chart
kind create cluster --name "$CLUSTER" --image "$(make -s print-kind-node-image)" --kubeconfig "$KUBECONFIG"
make docker-build IMG="$IMG_REPO:$IMG_TAG"
kind load docker-image "$IMG_REPO:$IMG_TAG" --name "$CLUSTER"

helm upgrade --install offpeak dist/chart --namespace offpeak-system --create-namespace \
  --set manager.image.repository="$IMG_REPO" --set manager.image.tag="$IMG_TAG" --wait --timeout 5m
helm status offpeak --namespace offpeak-system

# The CRDs work and the controller plans a DeferrableJob with a source that needs no key.
kubectl apply -f - <<YAML
apiVersion: offpeak.thinnesen.com/v1alpha1
kind: SignalSource
metadata:
  name: chart-test-weather
spec:
  weatherProxy:
    cloudRegion: aws:eu-north-1
---
apiVersion: offpeak.thinnesen.com/v1alpha1
kind: DeferrableJob
metadata:
  name: chart-test
  namespace: default
spec:
  deadline: "$(date -u -d '+30 hours' +%Y-%m-%dT%H:%M:%SZ)"
  notBefore: "$(date -u -d '+1 hour' +%Y-%m-%dT%H:%M:%SZ)"
  estimatedDuration: 1h
  signals:
    - sourceRef: chart-test-weather
      weight: 100
  jobTemplate:
    spec:
      template:
        spec:
          restartPolicy: Never
          containers:
            - name: main
              image: busybox:1.38.0
              command: ["true"]
YAML
for _ in $(seq 1 60); do
  phase="$(kubectl get djob chart-test -o jsonpath='{.status.phase}')"
  [ "$phase" = "Waiting" ] && break
  sleep 2
done
kubectl get djob chart-test
[ "$phase" = "Waiting" ] || { echo "the DeferrableJob has no plan" >&2; kubectl -n offpeak-system logs deploy/offpeak-controller-manager --tail=50; exit 1; }

helm uninstall offpeak --namespace offpeak-system --wait
echo "Helm chart test passed"
