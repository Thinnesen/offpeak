package controller

import (
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	offpeakv1alpha1 "github.com/Thinnesen/offpeak/api/v1alpha1"
)

// withCapacity sets spec.karpenter on the DeferrableJob of h.
func (h *harness) withCapacity(nodePool string, ct offpeakv1alpha1.CapacityType) {
	GinkgoHelper()
	dj := h.get()
	dj.Spec.Karpenter = &offpeakv1alpha1.KarpenterSpec{NodePool: nodePool, CapacityType: ct}
	Expect(k8sClient.Update(ctx, dj)).To(Succeed())
}

func (h *harness) jobNamed(name string) (*batchv1.Job, error) {
	job := &batchv1.Job{}
	return job, k8sClient.Get(ctx, types.NamespacedName{Namespace: h.key.Namespace, Name: name}, job)
}

func capacitySelector(job *batchv1.Job) string {
	return job.Spec.Template.Spec.NodeSelector[capacityTypeKey]
}

// cheapNow returns a source with the lowest value in the first hour.
func cheapNow() fakeSource { return hourlySource("g", 10, 50, 50, 50, 50, 50, 50, 50) }

var _ = Describe("Spot capacity and on-demand fallback", func() {
	It("puts a Spot Job on spot capacity and ignores spot interruptions", func() {
		createNodePool("fb-spot", "spot", "on-demand")
		h := newHarness("cap-spot", t0.Add(8*time.Hour), time.Hour, fakeResolver{"carbon-f1": cheapNow()})
		h.withCapacity("fb-spot", offpeakv1alpha1.CapacitySpot)

		h.reconcile()
		job, err := h.job()
		Expect(err).NotTo(HaveOccurred())
		Expect(capacitySelector(job)).To(Equal("spot"))
		Expect(job.Spec.PodFailurePolicy).NotTo(BeNil())
		Expect(job.Spec.PodFailurePolicy.Rules).To(ConsistOf(batchv1.PodFailurePolicyRule{
			Action: batchv1.PodFailurePolicyActionIgnore,
			OnPodConditions: []batchv1.PodFailurePolicyOnPodConditionsPattern{
				{Type: corev1.DisruptionTarget, Status: corev1.ConditionTrue},
			},
		}))
		Expect(h.get().Status.Attempts).To(HaveLen(1))
	})

	It("keeps the pod failure policy of the user", func() {
		createNodePool("fb-user", "spot")
		h := newHarness("cap-user-pfp", t0.Add(8*time.Hour), time.Hour, fakeResolver{"carbon-f2": cheapNow()})
		dj := h.get()
		dj.Spec.Karpenter = &offpeakv1alpha1.KarpenterSpec{NodePool: "fb-user", CapacityType: offpeakv1alpha1.CapacitySpot}
		dj.Spec.JobTemplate.Spec.PodFailurePolicy = &batchv1.PodFailurePolicy{Rules: []batchv1.PodFailurePolicyRule{{
			Action:      batchv1.PodFailurePolicyActionFailJob,
			OnExitCodes: &batchv1.PodFailurePolicyOnExitCodesRequirement{Operator: "In", Values: []int32{42}},
		}}}
		Expect(k8sClient.Update(ctx, dj)).To(Succeed())

		h.reconcile()
		job, err := h.job()
		Expect(err).NotTo(HaveOccurred())
		Expect(job.Spec.PodFailurePolicy.Rules).To(HaveLen(1))
		Expect(job.Spec.PodFailurePolicy.Rules[0].Action).To(Equal(batchv1.PodFailurePolicyActionFailJob))
	})

	It("waits when the NodePool does not allow the capacity type", func() {
		createNodePool("fb-od-only", "on-demand")
		h := newHarness("cap-not-allowed", t0.Add(8*time.Hour), time.Hour, fakeResolver{"carbon-f3": cheapNow()})
		h.withCapacity("fb-od-only", offpeakv1alpha1.CapacitySpotWithFallback)

		Expect(h.reconcile().RequeueAfter).To(Equal(time.Minute))
		_, err := h.job()
		Expect(apierrors.IsNotFound(err)).To(BeTrue())
		c := condition(h.get(), offpeakv1alpha1.ConditionKarpenterReady)
		Expect(c.Status).To(Equal(metav1.ConditionFalse))
		Expect(c.Reason).To(Equal("CapacityTypeNotAllowed"))
	})

	Context("SpotWithFallback", func() {
		// Deadline t0+8h, duration 1h, margin 15m: fallbackAt is t0+6h45m, the last spot start is t0+5h45m.
		var h *harness
		var counter int
		BeforeEach(func() {
			counter++
			np := "fb-both-" + string(rune('a'+counter))
			createNodePool(np, "spot", "on-demand")
			h = newHarness("fallback-"+string(rune('a'+counter)), t0.Add(8*time.Hour), time.Hour,
				fakeResolver{"carbon-f4": cheapNow()})
			h.withCapacity(np, offpeakv1alpha1.CapacitySpotWithFallback)
		})

		It("succeeds on spot without a fallback", func() {
			h.reconcile()
			job, err := h.job()
			Expect(err).NotTo(HaveOccurred())
			Expect(capacitySelector(job)).To(Equal("spot"))
			Expect(h.get().Status.LatestStart.Time).To(BeTemporally("==", t0.Add(5*time.Hour+45*time.Minute)))

			By("requeuing at fallbackAt while the spot Job runs")
			Expect(h.reconcile().RequeueAfter).To(Equal(6*time.Hour + 45*time.Minute))

			setJobConditions(job, batchv1.JobSuccessCriteriaMet, batchv1.JobComplete)
			h.reconcile()
			dj := h.get()
			Expect(dj.Status.Phase).To(Equal(offpeakv1alpha1.PhaseSucceeded))
			Expect(dj.Status.Attempts).To(HaveLen(1))
			Expect(dj.Status.Attempts[0].Result).To(Equal(offpeakv1alpha1.AttemptSucceeded))
		})

		It("falls back to on-demand when the spot Job fails", func() {
			h.reconcile()
			job, err := h.job()
			Expect(err).NotTo(HaveOccurred())
			setJobConditions(job, batchv1.JobFailureTarget, batchv1.JobFailed)

			h.clock.SetTime(t0.Add(2 * time.Hour))
			h.reconcile()
			dj := h.get()
			od, err := h.jobNamed(h.key.Name + "-od")
			Expect(err).NotTo(HaveOccurred())
			Expect(capacitySelector(od)).To(Equal("on-demand"))
			Expect(od.Spec.PodFailurePolicy).To(BeNil())
			Expect(metav1.IsControlledBy(od, dj)).To(BeTrue())
			_, err = h.job()
			Expect(apierrors.IsNotFound(err)).To(BeTrue(), "the spot Job must be deleted")

			Expect(dj.Status.Phase).To(Equal(offpeakv1alpha1.PhaseRunning))
			Expect(dj.Status.JobName).To(Equal(od.Name))
			Expect(dj.Status.Attempts).To(HaveLen(2))
			Expect(dj.Status.Attempts[0].Result).To(Equal(offpeakv1alpha1.AttemptFailed))
			Expect(dj.Status.Attempts[1].CapacityType).To(Equal(offpeakv1alpha1.CapacityOnDemand))
			c := condition(dj, offpeakv1alpha1.ConditionFallback)
			Expect(c.Status).To(Equal(metav1.ConditionTrue))
			Expect(c.Reason).To(Equal("SpotFailed"))

			By("mirroring the on-demand Job")
			setJobConditions(od, batchv1.JobSuccessCriteriaMet, batchv1.JobComplete)
			h.reconcile()
			dj = h.get()
			Expect(dj.Status.Phase).To(Equal(offpeakv1alpha1.PhaseSucceeded))
			Expect(dj.Status.Attempts[1].Result).To(Equal(offpeakv1alpha1.AttemptSucceeded))
		})

		It("falls back to on-demand when the spot Job is too slow", func() {
			h.reconcile()
			h.clock.SetTime(t0.Add(6*time.Hour + 45*time.Minute))
			h.reconcile()
			dj := h.get()
			Expect(dj.Status.Attempts).To(HaveLen(2))
			Expect(dj.Status.Attempts[0].Result).To(Equal(offpeakv1alpha1.AttemptReplaced))
			Expect(condition(dj, offpeakv1alpha1.ConditionFallback).Reason).To(Equal("SpotTooSlow"))
			_, err := h.jobNamed(h.key.Name + "-od")
			Expect(err).NotTo(HaveOccurred())
		})

		It("falls back when the spot Job is gone, and adopts an existing fallback Job", func() {
			h.reconcile()
			spot, err := h.job()
			Expect(err).NotTo(HaveOccurred())
			Expect(k8sClient.Delete(ctx, spot)).To(Succeed())

			// A fallback Job from an earlier reconcile already exists.
			od, err := h.r.buildJob(h.get(), h.key.Name+"-od", offpeakv1alpha1.CapacityOnDemand)
			Expect(err).NotTo(HaveOccurred())
			Expect(k8sClient.Create(ctx, od)).To(Succeed())

			h.reconcile()
			dj := h.get()
			Expect(dj.Status.JobName).To(Equal(od.Name))
			Expect(condition(dj, offpeakv1alpha1.ConditionFallback).Reason).To(Equal("SpotJobMissing"))
		})

		It("keeps the spot Job and retries when the fallback name is taken", func() {
			h.reconcile()
			spot, err := h.job()
			Expect(err).NotTo(HaveOccurred())
			foreign := &batchv1.Job{
				Name: h.key.Name + "-od", Namespace: "default",
				Spec: *h.get().Spec.JobTemplate.Spec.DeepCopy(),
			}
			Expect(k8sClient.Create(ctx, foreign)).To(Succeed())
			DeferCleanup(func() { _ = k8sClient.Delete(ctx, foreign) })
			setJobConditions(spot, batchv1.JobFailureTarget, batchv1.JobFailed)

			Expect(h.reconcile().RequeueAfter).To(Equal(time.Minute))
			_, err = h.job()
			Expect(err).NotTo(HaveOccurred(), "the spot Job must stay until the fallback can start")
			dj := h.get()
			Expect(dj.Status.JobName).To(Equal(h.key.Name))
			Expect(condition(dj, offpeakv1alpha1.ConditionFallback).Reason).To(Equal("NameConflict"))
		})

		It("starts on-demand at once when there is no time for a spot attempt", func() {
			h.clock.SetTime(t0.Add(6 * time.Hour))
			h.reconcile()
			job, err := h.job()
			Expect(err).NotTo(HaveOccurred())
			Expect(capacitySelector(job)).To(Equal("on-demand"))
			dj := h.get()
			Expect(dj.Status.Attempts).To(HaveLen(1))
			Expect(dj.Status.Attempts[0].CapacityType).To(Equal(offpeakv1alpha1.CapacityOnDemand))
			Expect(condition(dj, offpeakv1alpha1.ConditionDeadlineAtRisk).Status).To(Equal(metav1.ConditionTrue))

			By("not falling back from an on-demand Job")
			setJobConditions(job, batchv1.JobFailureTarget, batchv1.JobFailed)
			h.reconcile()
			Expect(h.get().Status.Phase).To(Equal(offpeakv1alpha1.PhaseFailed))
			_, err = h.jobNamed(h.key.Name + "-od")
			Expect(apierrors.IsNotFound(err)).To(BeTrue())
		})
	})
})
