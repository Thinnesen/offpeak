package controller

import (
	"context"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	offpeakv1alpha1 "github.com/Thinnesen/offpeak/api/v1alpha1"
	"github.com/Thinnesen/offpeak/internal/forecast"
	"github.com/Thinnesen/offpeak/internal/signal"
)

// recordingSource returns a series and records the last query.
type recordingSource struct {
	fakeSource
	last *signal.Query
}

func (s recordingSource) Fetch(ctx context.Context, q signal.Query) (forecast.Series, error) {
	*s.last = q
	return s.fakeSource.Fetch(ctx, q)
}

// createNodePool creates a Karpenter NodePool with the given capacity types.
func createNodePool(name string, capacityTypes ...string) {
	GinkgoHelper()
	np := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "karpenter.sh/v1",
		"kind":       "NodePool",
		"metadata":   map[string]any{"name": name},
		"spec": map[string]any{"template": map[string]any{"spec": map[string]any{
			"nodeClassRef": map[string]any{"group": "karpenter.k8s.aws", "kind": "EC2NodeClass", "name": "default"},
			"requirements": []any{
				req(instanceTypeLabel, "In", "g6.xlarge", "g6.2xlarge"),
				req(capacityTypeKey, "In", capacityTypes...),
			},
		}}},
	}}
	Expect(k8sClient.Create(ctx, np)).To(Succeed())
	DeferCleanup(func() { _ = k8sClient.Delete(ctx, np) })
}

// ensureSpotSource creates an awsSpot SignalSource with the given name.
func ensureSpotSource(name string) {
	GinkgoHelper()
	ss := validSignalSource(name)
	ss.Spec = offpeakv1alpha1.SignalSourceSpec{AWSSpot: &offpeakv1alpha1.AWSSpotSource{Region: "eu-north-1"}}
	err := k8sClient.Create(ctx, ss)
	if !apierrors.IsAlreadyExists(err) {
		Expect(err).NotTo(HaveOccurred())
	}
}

// withSpot adds an awsSpot signal to the DeferrableJob of h and sets spec.karpenter.
func (h *harness) withSpot(name string, src signal.Source, ks *offpeakv1alpha1.KarpenterSpec) {
	GinkgoHelper()
	ensureSpotSource(name)
	h.r.Sources.(fakeResolver)[name] = src
	dj := h.get()
	dj.Spec.Signals = append(dj.Spec.Signals, offpeakv1alpha1.SignalRef{SourceRef: name, Weight: 50})
	dj.Spec.Karpenter = ks
	Expect(k8sClient.Update(ctx, dj)).To(Succeed())
}

var _ = Describe("Karpenter integration", func() {
	It("waits while the NodePool does not exist", func() {
		h := newHarness("np-missing", t0.Add(8*time.Hour), time.Hour,
			fakeResolver{"carbon-k1": hourlySource("g", 10, 50, 50, 50, 50, 50, 50, 50)})
		dj := h.get()
		dj.Spec.Karpenter = &offpeakv1alpha1.KarpenterSpec{NodePool: "missing"}
		Expect(k8sClient.Update(ctx, dj)).To(Succeed())

		Expect(h.reconcile().RequeueAfter).To(Equal(time.Minute))
		_, err := h.job()
		Expect(apierrors.IsNotFound(err)).To(BeTrue())
		c := condition(h.get(), offpeakv1alpha1.ConditionKarpenterReady)
		Expect(c.Status).To(Equal(metav1.ConditionFalse))
		Expect(c.Reason).To(Equal("NodePoolNotFound"))
	})

	It("puts the Job on the NodePool and the instance types", func() {
		createNodePool("gpu-a", "spot", "on-demand")
		h := newHarness("np-job", t0.Add(8*time.Hour), time.Hour,
			fakeResolver{"carbon-k2": hourlySource("g", 10, 50, 50, 50, 50, 50, 50, 50)})
		dj := h.get()
		dj.Spec.Karpenter = &offpeakv1alpha1.KarpenterSpec{NodePool: "gpu-a", InstanceTypes: []string{"g6.xlarge"}}
		Expect(k8sClient.Update(ctx, dj)).To(Succeed())

		h.reconcile()
		job, err := h.job()
		Expect(err).NotTo(HaveOccurred())
		pod := job.Spec.Template.Spec
		Expect(pod.NodeSelector).To(HaveKeyWithValue(nodePoolKey, "gpu-a"))
		terms := pod.Affinity.NodeAffinity.RequiredDuringSchedulingIgnoredDuringExecution.NodeSelectorTerms
		Expect(terms).To(ConsistOf(corev1.NodeSelectorTerm{MatchExpressions: []corev1.NodeSelectorRequirement{{
			Key: instanceTypeLabel, Operator: corev1.NodeSelectorOpIn, Values: []string{"g6.xlarge"},
		}}}))
		Expect(condition(h.get(), offpeakv1alpha1.ConditionKarpenterReady).Status).To(Equal(metav1.ConditionTrue))
	})

	It("passes the NodePool instance types to an awsSpot signal", func() {
		createNodePool("gpu-b", "spot")
		h := newHarness("np-spot", t0.Add(8*time.Hour), time.Hour,
			fakeResolver{"carbon-k3": hourlySource("g", 50, 40, 30, 10, 30, 40, 50, 60)})
		var q signal.Query
		h.withSpot("spot-k3", recordingSource{fakeSource: hourlySource("USD/h", 1, 1, 1, 1, 1, 1, 1, 1), last: &q},
			&offpeakv1alpha1.KarpenterSpec{NodePool: "gpu-b"})

		h.reconcile()
		Expect(q.InstanceTypes).To(Equal([]string{"g6.xlarge", "g6.2xlarge"}))
		dj := h.get()
		Expect(condition(dj, offpeakv1alpha1.ConditionSignalsAvailable).Status).To(Equal(metav1.ConditionTrue))
		Expect(dj.Status.Estimates).To(HaveLen(2))
	})

	It("ignores an awsSpot signal when the NodePool does not allow spot", func() {
		createNodePool("gpu-c", "on-demand")
		h := newHarness("np-on-demand", t0.Add(8*time.Hour), time.Hour,
			fakeResolver{"carbon-k4": hourlySource("g", 50, 40, 30, 10, 30, 40, 50, 60)})
		var q signal.Query
		h.withSpot("spot-k4", recordingSource{fakeSource: hourlySource("USD/h", 1, 9, 9, 9, 9, 9, 9, 9), last: &q},
			&offpeakv1alpha1.KarpenterSpec{NodePool: "gpu-c"})

		h.reconcile()
		dj := h.get()
		Expect(q.InstanceTypes).To(BeNil(), "the spot source must not be called")
		Expect(dj.Status.PlannedStart.Time).To(BeTemporally("==", t0.Add(3*time.Hour)))
		Expect(dj.Status.Estimates).To(HaveLen(1))
		c := condition(dj, offpeakv1alpha1.ConditionKarpenterReady)
		Expect(c.Status).To(Equal(metav1.ConditionTrue))
		Expect(c.Message).To(ContainSubstring("spot"))
	})

	It("fails an awsSpot signal without instance types", func() {
		h := newHarness("spot-no-types", t0.Add(8*time.Hour), time.Hour,
			fakeResolver{"carbon-k5": hourlySource("g", 50, 40, 30, 10, 30, 40, 50, 60)})
		h.withSpot("spot-k5", hourlySource("USD/h", 1), nil)

		h.reconcile()
		c := condition(h.get(), offpeakv1alpha1.ConditionSignalsAvailable)
		Expect(c.Status).To(Equal(metav1.ConditionFalse))
		Expect(c.Message).To(ContainSubstring("set spec.karpenter.instanceTypes"))
	})
})
