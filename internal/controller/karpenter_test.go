package controller

import (
	"reflect"
	"testing"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	offpeakv1alpha1 "github.com/Thinnesen/offpeak/api/v1alpha1"
)

// nodePool returns an unstructured NodePool with the given requirements.
func nodePool(reqs ...map[string]any) *unstructured.Unstructured {
	list := make([]any, len(reqs))
	for i, r := range reqs {
		list[i] = r
	}
	return &unstructured.Unstructured{Object: map[string]any{
		"spec": map[string]any{"template": map[string]any{"spec": map[string]any{"requirements": list}}},
	}}
}

func req(key, op string, values ...string) map[string]any {
	vals := make([]any, len(values))
	for i, v := range values {
		vals[i] = v
	}
	return map[string]any{"key": key, "operator": op, "values": vals}
}

func TestNodePoolInfo(t *testing.T) {
	tests := []struct {
		name      string
		np        *unstructured.Unstructured
		spec      []string
		wantTypes []string
		wantSpot  bool
	}{
		{
			name:      "types from the NodePool",
			np:        nodePool(req(instanceTypeLabel, "In", "g6.xlarge", "g6.2xlarge"), req(capacityTypeKey, "In", "spot")),
			wantTypes: []string{"g6.xlarge", "g6.2xlarge"}, wantSpot: true,
		},
		{
			name:      "types from the spec win",
			np:        nodePool(req(instanceTypeLabel, "In", "g6.xlarge")),
			spec:      []string{"m7i.large"},
			wantTypes: []string{"m7i.large"},
		},
		{name: "no types", np: nodePool(req(instanceTypeLabel, "NotIn", "t3.nano"))},
		{name: "no requirements", np: &unstructured.Unstructured{Object: map[string]any{}}},
		{name: "on-demand only", np: nodePool(req(capacityTypeKey, "In", "on-demand"))},
		{name: "spot and on-demand", np: nodePool(req(capacityTypeKey, "In", "on-demand", "spot")), wantSpot: true},
		{name: "not on-demand", np: nodePool(req(capacityTypeKey, "NotIn", "on-demand")), wantSpot: true},
		{name: "not spot", np: nodePool(req(capacityTypeKey, "NotIn", "spot"))},
		{name: "any capacity type", np: nodePool(req(capacityTypeKey, "Exists")), wantSpot: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := nodePoolInfo(tt.np, tt.spec)
			if !reflect.DeepEqual(got.instanceTypes, tt.wantTypes) {
				t.Errorf("instanceTypes = %v, want %v", got.instanceTypes, tt.wantTypes)
			}
			if got.spotAllowed != tt.wantSpot {
				t.Errorf("spotAllowed = %v, want %v", got.spotAllowed, tt.wantSpot)
			}
		})
	}
}

func TestApplyKarpenter(t *testing.T) {
	typeExpr := corev1.NodeSelectorRequirement{
		Key: instanceTypeLabel, Operator: corev1.NodeSelectorOpIn, Values: []string{"g6.xlarge"},
	}
	zoneExpr := corev1.NodeSelectorRequirement{
		Key: "topology.kubernetes.io/zone", Operator: corev1.NodeSelectorOpIn, Values: []string{"eu-north-1a"},
	}
	userTypeExpr := corev1.NodeSelectorRequirement{
		Key: instanceTypeLabel, Operator: corev1.NodeSelectorOpIn, Values: []string{"p5.48xlarge"},
	}
	required := func(terms ...corev1.NodeSelectorTerm) *corev1.Affinity {
		return &corev1.Affinity{NodeAffinity: &corev1.NodeAffinity{
			RequiredDuringSchedulingIgnoredDuringExecution: &corev1.NodeSelector{NodeSelectorTerms: terms},
		}}
	}
	term := func(exprs ...corev1.NodeSelectorRequirement) corev1.NodeSelectorTerm {
		return corev1.NodeSelectorTerm{MatchExpressions: exprs}
	}

	tests := []struct {
		name         string
		spec         offpeakv1alpha1.KarpenterSpec
		in           corev1.PodSpec
		wantSelector map[string]string
		wantAffinity *corev1.Affinity
	}{
		{
			name:         "node pool only",
			spec:         offpeakv1alpha1.KarpenterSpec{NodePool: "gpu"},
			wantSelector: map[string]string{nodePoolKey: "gpu"},
		},
		{
			name:         "keeps the node pool of the user",
			spec:         offpeakv1alpha1.KarpenterSpec{NodePool: "gpu"},
			in:           corev1.PodSpec{NodeSelector: map[string]string{nodePoolKey: "other", "a": "b"}},
			wantSelector: map[string]string{nodePoolKey: "other", "a": "b"},
		},
		{
			name:         "adds an instance type term",
			spec:         offpeakv1alpha1.KarpenterSpec{NodePool: "gpu", InstanceTypes: []string{"g6.xlarge"}},
			wantSelector: map[string]string{nodePoolKey: "gpu"},
			wantAffinity: required(term(typeExpr)),
		},
		{
			name:         "adds the instance types to each term",
			spec:         offpeakv1alpha1.KarpenterSpec{NodePool: "gpu", InstanceTypes: []string{"g6.xlarge"}},
			in:           corev1.PodSpec{Affinity: required(term(zoneExpr), term(userTypeExpr))},
			wantSelector: map[string]string{nodePoolKey: "gpu"},
			wantAffinity: required(term(zoneExpr, typeExpr), term(userTypeExpr)),
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			pod := tt.in.DeepCopy()
			applyKarpenter(pod, &tt.spec, offpeakv1alpha1.CapacityAny)
			if !reflect.DeepEqual(pod.NodeSelector, tt.wantSelector) {
				t.Errorf("NodeSelector = %v, want %v", pod.NodeSelector, tt.wantSelector)
			}
			if !reflect.DeepEqual(pod.Affinity, tt.wantAffinity) {
				t.Errorf("Affinity = %+v, want %+v", pod.Affinity, tt.wantAffinity)
			}
		})
	}
}
