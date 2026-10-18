#!/usr/bin/env bash
# Creates the Kind cluster "offpeak-demo" for the README demos and deploys offpeak to it.
# It uses its own kubeconfig (bin/demo-kubeconfig), so it never changes ~/.kube/config.
set -euo pipefail
cd "$(dirname "$0")/../.."

export PATH="$PWD/bin:$PATH"
export KUBECONFIG="$PWD/bin/demo-kubeconfig"
CLUSTER=offpeak-demo
IMG=offpeak:demo
NODE_IMAGE="$(make -s print-kind-node-image)"

if ! kind get clusters | grep -qx "$CLUSTER"; then
  kind create cluster --name "$CLUSTER" --image "$NODE_IMAGE" --kubeconfig "$KUBECONFIG"
fi
# make deploy writes the image into config/manager/kustomization.yaml. Stop if that file has
# local changes, because the script restores it with git afterwards.
if ! git diff --quiet -- config/manager/kustomization.yaml; then
  echo "config/manager/kustomization.yaml has local changes; commit or stash them first" >&2
  exit 1
fi
make docker-build IMG="$IMG"
kind load docker-image "$IMG" --name "$CLUSTER"
make install deploy IMG="$IMG"
git checkout -- config/manager/kustomization.yaml
kubectl -n offpeak-system rollout status deployment/offpeak-controller-manager --timeout=3m

# A NodePool for the fallback demo. Kind has no Karpenter, so no spot node ever comes.
# The node has the on-demand labels, so only the on-demand fallback Job can run.
kubectl apply --server-side -f test/crds/karpenter/karpenter.sh_nodepools.yaml
kubectl wait --for=condition=Established crd/nodepools.karpenter.sh --timeout=1m
kubectl apply -f hack/demo/cluster-objects.yaml
kubectl label node "$CLUSTER-control-plane" --overwrite \
  karpenter.sh/nodepool=demo karpenter.sh/capacity-type=on-demand
