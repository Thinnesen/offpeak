package controller

import (
	"context"
	"errors"
	"fmt"
	"slices"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/client"

	offpeakv1alpha1 "github.com/Thinnesen/offpeak/api/v1alpha1"
)

const (
	nodePoolKey     = "karpenter.sh/nodepool"
	capacityTypeKey = "karpenter.sh/capacity-type"
	// instanceTypeLabel is the well-known node label for the instance type.
	instanceTypeLabel = "node.kubernetes.io/instance-type"
)

var nodePoolGVK = schema.GroupVersionKind{Group: "karpenter.sh", Version: "v1", Kind: "NodePool"}

// karpenterInfo is what the planner needs from the Karpenter NodePool.
type karpenterInfo struct {
	instanceTypes   []string
	spotAllowed     bool
	onDemandAllowed bool
}

// +kubebuilder:rbac:groups=karpenter.sh,resources=nodepools,verbs=get;list;watch

// karpenter reads the NodePool of dj. An empty reason with an error means a transient error.
func (r *DeferrableJobReconciler) karpenter(ctx context.Context, dj *offpeakv1alpha1.DeferrableJob) (
	*karpenterInfo, string, error,
) {
	ks := dj.Spec.Karpenter
	np := &unstructured.Unstructured{}
	np.SetGroupVersionKind(nodePoolGVK)
	err := r.Get(ctx, client.ObjectKey{Name: ks.NodePool}, np)
	switch {
	case meta.IsNoMatchError(err):
		return nil, "KarpenterNotInstalled", errors.New("the API karpenter.sh/v1 NodePool is not installed")
	case apierrors.IsNotFound(err):
		return nil, "NodePoolNotFound", fmt.Errorf("NodePool %s does not exist", ks.NodePool)
	case err != nil:
		return nil, "", fmt.Errorf("get NodePool %s: %w", ks.NodePool, err)
	}
	info := nodePoolInfo(np, ks.InstanceTypes)
	if err := info.allows(ks.CapacityType); err != nil {
		return nil, "CapacityTypeNotAllowed", fmt.Errorf("NodePool %s: %w", ks.NodePool, err)
	}
	return &info, "", nil
}

// allows returns an error when the NodePool does not allow the capacity type.
func (info karpenterInfo) allows(ct offpeakv1alpha1.CapacityType) error {
	needSpot := ct == offpeakv1alpha1.CapacitySpot || ct == offpeakv1alpha1.CapacitySpotWithFallback
	needOnDemand := ct == offpeakv1alpha1.CapacityOnDemand || ct == offpeakv1alpha1.CapacitySpotWithFallback
	switch {
	case needSpot && !info.spotAllowed:
		return fmt.Errorf("capacity type %s needs spot capacity, and the NodePool does not allow it", ct)
	case needOnDemand && !info.onDemandAllowed:
		return fmt.Errorf("capacity type %s needs on-demand capacity, and the NodePool does not allow it", ct)
	}
	return nil
}

// nodePoolInfo reads the instance types and the capacity types from the NodePool requirements.
func nodePoolInfo(np *unstructured.Unstructured, specTypes []string) karpenterInfo {
	reqs, _, _ := unstructured.NestedSlice(np.Object, "spec", "template", "spec", "requirements")
	// Karpenter uses on-demand when a NodePool has no capacity-type requirement.
	info := karpenterInfo{instanceTypes: specTypes, onDemandAllowed: true}
	for _, raw := range reqs {
		m, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		key, _, _ := unstructured.NestedString(m, "key")
		op, _, _ := unstructured.NestedString(m, "operator")
		values, _, _ := unstructured.NestedStringSlice(m, "values")
		switch {
		case key == instanceTypeLabel && op == string(corev1.NodeSelectorOpIn) && len(specTypes) == 0:
			info.instanceTypes = values
		case key == capacityTypeKey:
			info.spotAllowed = allows(op, values, "spot")
			info.onDemandAllowed = allows(op, values, "on-demand")
		}
	}
	return info
}

// allows reports if a requirement allows value. Without a capacity-type requirement, Karpenter uses on-demand.
func allows(op string, values []string, value string) bool {
	switch corev1.NodeSelectorOperator(op) {
	case corev1.NodeSelectorOpIn:
		return slices.Contains(values, value)
	case corev1.NodeSelectorOpNotIn:
		return !slices.Contains(values, value)
	case corev1.NodeSelectorOpExists:
		return true
	}
	return false
}

// capacityLabels are the values of the label karpenter.sh/capacity-type.
var capacityLabels = map[offpeakv1alpha1.CapacityType]string{
	offpeakv1alpha1.CapacitySpot:     "spot",
	offpeakv1alpha1.CapacityOnDemand: "on-demand",
}

// applyKarpenter adds the NodePool, capacity type and instance types to the pod. It keeps user keys.
func applyKarpenter(pod *corev1.PodSpec, ks *offpeakv1alpha1.KarpenterSpec, ct offpeakv1alpha1.CapacityType) {
	if pod.NodeSelector == nil {
		pod.NodeSelector = map[string]string{}
	}
	if _, ok := pod.NodeSelector[nodePoolKey]; !ok {
		pod.NodeSelector[nodePoolKey] = ks.NodePool
	}
	if label, ok := capacityLabels[ct]; ok {
		if _, set := pod.NodeSelector[capacityTypeKey]; !set {
			pod.NodeSelector[capacityTypeKey] = label
		}
	}
	if len(ks.InstanceTypes) == 0 {
		return
	}
	expr := corev1.NodeSelectorRequirement{
		Key: instanceTypeLabel, Operator: corev1.NodeSelectorOpIn, Values: slices.Clone(ks.InstanceTypes),
	}
	if pod.Affinity == nil {
		pod.Affinity = &corev1.Affinity{}
	}
	if pod.Affinity.NodeAffinity == nil {
		pod.Affinity.NodeAffinity = &corev1.NodeAffinity{}
	}
	na := pod.Affinity.NodeAffinity
	if na.RequiredDuringSchedulingIgnoredDuringExecution == nil {
		na.RequiredDuringSchedulingIgnoredDuringExecution = &corev1.NodeSelector{}
	}
	sel := na.RequiredDuringSchedulingIgnoredDuringExecution
	if len(sel.NodeSelectorTerms) == 0 {
		sel.NodeSelectorTerms = []corev1.NodeSelectorTerm{{}}
	}
	// The terms are ORed, so each term needs the expression.
	for i := range sel.NodeSelectorTerms {
		t := &sel.NodeSelectorTerms[i]
		if !slices.ContainsFunc(t.MatchExpressions, func(e corev1.NodeSelectorRequirement) bool {
			return e.Key == instanceTypeLabel
		}) {
			t.MatchExpressions = append(t.MatchExpressions, expr)
		}
	}
}

func karpenterMessage(ks *offpeakv1alpha1.KarpenterSpec, info *karpenterInfo) string {
	if !info.spotAllowed {
		return fmt.Sprintf("NodePool %s does not allow spot capacity, so awsSpot signals have no effect", ks.NodePool)
	}
	return fmt.Sprintf("NodePool %s allows spot capacity", ks.NodePool)
}

// ignoreSpotInterruptions keeps spot interruptions out of backoffLimit. It keeps a user policy.
func ignoreSpotInterruptions(job *batchv1.Job) {
	if job.Spec.PodFailurePolicy != nil || job.Spec.Template.Spec.RestartPolicy != corev1.RestartPolicyNever {
		return
	}
	job.Spec.PodFailurePolicy = &batchv1.PodFailurePolicy{Rules: []batchv1.PodFailurePolicyRule{{
		Action: batchv1.PodFailurePolicyActionIgnore,
		OnPodConditions: []batchv1.PodFailurePolicyOnPodConditionsPattern{
			{Type: corev1.DisruptionTarget, Status: corev1.ConditionTrue},
		},
	}}}
}
