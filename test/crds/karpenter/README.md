# Karpenter NodePool CRD for envtest

The envtest suite installs this CRD. offpeak reads NodePools as `unstructured`, so the test checks the real schema.

| File | Source | SHA-256 |
|---|---|---|
| `karpenter.sh_nodepools.yaml` | https://raw.githubusercontent.com/kubernetes-sigs/karpenter/v1.14.2/pkg/apis/crds/karpenter.sh_nodepools.yaml | `62990043212a239704c0195bbd37abd0fddad90a586f5b37dfb403a0a0aefe1e` |

Karpenter v1.14.2 is a tag of the core repository `kubernetes-sigs/karpenter`. The AWS provider repository has no v1.14.2 tag. The core repository owns the `karpenter.sh` CRDs.

To check the file: `sha256sum karpenter.sh_nodepools.yaml`.
