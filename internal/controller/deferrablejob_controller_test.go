package controller

import (
	"context"
	"errors"
	"maps"
	"slices"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/events"
	clocktesting "k8s.io/utils/clock/testing"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	offpeakv1alpha1 "github.com/Thinnesen/offpeak/api/v1alpha1"
	"github.com/Thinnesen/offpeak/internal/forecast"
	"github.com/Thinnesen/offpeak/internal/signal"
)

var t0 = time.Date(2026, 10, 19, 0, 0, 0, 0, time.UTC)

type fakeSource struct {
	series forecast.Series
	err    error
}

func (f fakeSource) Fetch(context.Context, signal.Query) (forecast.Series, error) {
	return f.series, f.err
}

type fakeResolver map[string]signal.Source

func (f fakeResolver) Source(_ context.Context, ss *offpeakv1alpha1.SignalSource) (signal.Source, error) {
	if s, ok := f[ss.Name]; ok {
		return s, nil
	}
	return nil, errors.New("no fake source")
}

// hourlySource returns a source with one point per hour from t0.
func hourlySource(unit string, values ...float64) fakeSource {
	pts := make([]forecast.Point, len(values))
	for i, v := range values {
		start := t0.Add(time.Duration(i) * time.Hour)
		pts[i] = forecast.Point{Start: start, End: start.Add(time.Hour), Value: v}
	}
	s, err := forecast.NewSeries(unit, pts)
	Expect(err).NotTo(HaveOccurred())
	return fakeSource{series: s}
}

func failingSource() fakeSource { return fakeSource{err: errors.New("status 503")} }

// ensureSignalSource creates a SignalSource with the given name if it does not exist.
func ensureSignalSource(name string) {
	GinkgoHelper()
	err := k8sClient.Create(ctx, validSignalSource(name))
	if !apierrors.IsAlreadyExists(err) {
		Expect(err).NotTo(HaveOccurred())
	}
}

type harness struct {
	clock    *clocktesting.FakeClock
	recorder *events.FakeRecorder
	r        *DeferrableJobReconciler
	key      types.NamespacedName
}

// newHarness creates a DeferrableJob with all sources (weight 50) and a reconciler with a fake clock.
func newHarness(name string, deadline time.Time, duration time.Duration, sources fakeResolver) *harness {
	GinkgoHelper()
	dj := validDeferrableJob(name)
	dj.Spec.Deadline = metav1.NewTime(deadline)
	dj.Spec.EstimatedDuration = metav1.Duration{Duration: duration}
	dj.Spec.Signals = nil
	for _, src := range slices.Sorted(maps.Keys(sources)) {
		ensureSignalSource(src)
		dj.Spec.Signals = append(dj.Spec.Signals, offpeakv1alpha1.SignalRef{SourceRef: src, Weight: 50})
	}
	dj.Spec.JobTemplate.Labels = map[string]string{"team": "ml"}
	dj.Spec.JobTemplate.Annotations = map[string]string{"note": "from template"}
	Expect(k8sClient.Create(ctx, dj)).To(Succeed())
	DeferCleanup(func() {
		_ = k8sClient.Delete(ctx, dj)
		_ = k8sClient.Delete(ctx, &batchv1.Job{Name: name, Namespace: "default"},
			client.PropagationPolicy(metav1.DeletePropagationBackground))
	})

	clk := clocktesting.NewFakeClock(t0)
	rec := events.NewFakeRecorder(10)
	return &harness{
		clock:    clk,
		recorder: rec,
		key:      types.NamespacedName{Name: name, Namespace: "default"},
		r: &DeferrableJobReconciler{
			Client:         k8sClient,
			Scheme:         k8sClient.Scheme(),
			Clock:          clk,
			Sources:        sources,
			Recorder:       rec,
			ReplanInterval: 15 * time.Minute,
		},
	}
}

func (h *harness) reconcile() ctrl.Result {
	GinkgoHelper()
	res, err := h.r.Reconcile(ctx, ctrl.Request{NamespacedName: h.key})
	Expect(err).NotTo(HaveOccurred())
	return res
}

func (h *harness) get() *offpeakv1alpha1.DeferrableJob {
	GinkgoHelper()
	dj := &offpeakv1alpha1.DeferrableJob{}
	Expect(k8sClient.Get(ctx, h.key, dj)).To(Succeed())
	return dj
}

func (h *harness) job() (*batchv1.Job, error) {
	job := &batchv1.Job{}
	return job, k8sClient.Get(ctx, h.key, job)
}

func condition(dj *offpeakv1alpha1.DeferrableJob, t string) *metav1.Condition {
	return meta.FindStatusCondition(dj.Status.Conditions, t)
}

// setJobConditions writes a finished Job status, as the Job controller would.
func setJobConditions(job *batchv1.Job, condTypes ...batchv1.JobConditionType) {
	GinkgoHelper()
	now := metav1.NewTime(time.Now())
	job.Status.StartTime = &now
	for _, t := range condTypes {
		job.Status.Conditions = append(job.Status.Conditions,
			batchv1.JobCondition{Type: t, Status: corev1.ConditionTrue, LastTransitionTime: now})
		switch t {
		case batchv1.JobComplete:
			job.Status.CompletionTime = &now
			job.Status.Succeeded = 1
		case batchv1.JobFailed:
			job.Status.Failed = 1
		}
	}
	Expect(k8sClient.Status().Update(ctx, job)).To(Succeed())
}

var _ = Describe("DeferrableJob controller", func() {
	It("starts the Job now when now is the best window", func() {
		h := newHarness("start-now", t0.Add(8*time.Hour), time.Hour,
			fakeResolver{"carbon-a": hourlySource("gCO2eq/kWh", 10, 50, 50, 50, 50, 50, 50, 50)})

		res := h.reconcile()
		Expect(res.RequeueAfter).To(BeZero())

		job, err := h.job()
		Expect(err).NotTo(HaveOccurred())
		Expect(metav1.IsControlledBy(job, h.get())).To(BeTrue())
		Expect(job.Labels).To(HaveKeyWithValue(offpeakv1alpha1.LabelDeferrableJob, "start-now"))
		Expect(job.Labels).To(HaveKeyWithValue("team", "ml"))
		Expect(job.Annotations).To(HaveKeyWithValue("note", "from template"))
		Expect(job.Spec.Template.Spec.Containers[0].Image).To(Equal("busybox"))

		dj := h.get()
		Expect(dj.Status.Phase).To(Equal(offpeakv1alpha1.PhaseRunning))
		Expect(dj.Status.JobName).To(Equal("start-now"))
		Expect(dj.Status.StartedAt.Time).To(BeTemporally("==", t0))
		Expect(condition(dj, offpeakv1alpha1.ConditionJobCreated).Status).To(Equal(metav1.ConditionTrue))
		Expect(h.recorder.Events).To(Receive(ContainSubstring("Started")))
	})

	It("waits for a later window and starts the Job then", func() {
		h := newHarness("wait-later", t0.Add(8*time.Hour), time.Hour,
			fakeResolver{"carbon-b": hourlySource("gCO2eq/kWh", 50, 40, 30, 10, 30, 40, 50, 60)})

		res := h.reconcile()
		Expect(res.RequeueAfter).To(Equal(15 * time.Minute))
		_, err := h.job()
		Expect(apierrors.IsNotFound(err)).To(BeTrue())

		dj := h.get()
		Expect(dj.Status.Phase).To(Equal(offpeakv1alpha1.PhaseWaiting))
		Expect(dj.Status.PlannedStart.Time).To(BeTemporally("==", t0.Add(3*time.Hour)))
		Expect(dj.Status.LatestStart.Time).To(BeTemporally("==", t0.Add(7*time.Hour)))
		Expect(dj.Status.LastPlanTime.Time).To(BeTemporally("==", t0))
		Expect(*dj.Status.SavingsPercent).To(Equal(int32(80)))
		Expect(dj.Status.Estimates).To(ConsistOf(offpeakv1alpha1.SignalEstimate{
			Source: "carbon-b", Unit: "gCO2eq/kWh", Planned: "10", IfStartedNow: "50",
		}))
		Expect(condition(dj, offpeakv1alpha1.ConditionPlanned).Status).To(Equal(metav1.ConditionTrue))
		Expect(condition(dj, offpeakv1alpha1.ConditionSignalsAvailable).Status).To(Equal(metav1.ConditionTrue))
		Expect(condition(dj, offpeakv1alpha1.ConditionDeadlineAtRisk).Status).To(Equal(metav1.ConditionFalse))

		By("writing the status of the SignalSource")
		ss := &offpeakv1alpha1.SignalSource{}
		Expect(k8sClient.Get(ctx, types.NamespacedName{Name: "carbon-b"}, ss)).To(Succeed())
		Expect(ss.Status.Unit).To(Equal("gCO2eq/kWh"))
		Expect(ss.Status.LastFetchTime.Time).To(BeTemporally("==", t0))
		ready := meta.FindStatusCondition(ss.Status.Conditions, "Ready")
		Expect(ready.Status).To(Equal(metav1.ConditionTrue))

		By("requeuing at the planned start when it is closer than the replan interval")
		h.clock.SetTime(t0.Add(2*time.Hour + 50*time.Minute))
		Expect(h.reconcile().RequeueAfter).To(Equal(10 * time.Minute))

		By("starting the Job at the planned start")
		h.clock.SetTime(t0.Add(3 * time.Hour))
		h.reconcile()
		_, err = h.job()
		Expect(err).NotTo(HaveOccurred())
		Expect(h.get().Status.Phase).To(Equal(offpeakv1alpha1.PhaseRunning))
	})

	It("starts the Job at once under deadline pressure", func() {
		h := newHarness("deadline-pressure", t0.Add(90*time.Minute), 2*time.Hour,
			fakeResolver{"carbon-c": hourlySource("gCO2eq/kWh", 50, 10, 10)})

		h.reconcile()
		_, err := h.job()
		Expect(err).NotTo(HaveOccurred())
		dj := h.get()
		Expect(condition(dj, offpeakv1alpha1.ConditionDeadlineAtRisk).Status).To(Equal(metav1.ConditionTrue))
		Expect(dj.Status.Phase).To(Equal(offpeakv1alpha1.PhaseRunning))
	})

	It("waits when all signals fail, and starts the Job at the latest start", func() {
		h := newHarness("all-fail", t0.Add(4*time.Hour), time.Hour, fakeResolver{"broken-a": failingSource()})

		Expect(h.reconcile().RequeueAfter).To(Equal(time.Minute))
		_, err := h.job()
		Expect(apierrors.IsNotFound(err)).To(BeTrue())
		dj := h.get()
		c := condition(dj, offpeakv1alpha1.ConditionSignalsAvailable)
		Expect(c.Status).To(Equal(metav1.ConditionFalse))
		Expect(c.Reason).To(Equal("AllSignalsFailed"))
		Expect(c.Message).To(ContainSubstring("broken-a"))
		ss := &offpeakv1alpha1.SignalSource{}
		Expect(k8sClient.Get(ctx, types.NamespacedName{Name: "broken-a"}, ss)).To(Succeed())
		ready := meta.FindStatusCondition(ss.Status.Conditions, "Ready")
		Expect(ready.Status).To(Equal(metav1.ConditionFalse))
		Expect(ready.Reason).To(Equal("FetchFailed"))
		Expect(ready.Message).To(ContainSubstring("503"))
		Expect(dj.Status.Phase).To(Equal(offpeakv1alpha1.PhaseWaiting))
		Expect(dj.Status.LatestStart.Time).To(BeTemporally("==", t0.Add(3*time.Hour)))

		By("starting the Job when the deadline wins")
		h.clock.SetTime(t0.Add(3 * time.Hour))
		h.reconcile()
		_, err = h.job()
		Expect(err).NotTo(HaveOccurred())
	})

	It("plans with the other signals when some signals fail", func() {
		h := newHarness("some-fail", t0.Add(8*time.Hour), time.Hour, fakeResolver{
			"carbon-d": hourlySource("gCO2eq/kWh", 50, 40, 30, 10, 30, 40, 50, 60),
			"broken-b": failingSource(),
		})

		h.reconcile()
		dj := h.get()
		Expect(dj.Status.PlannedStart.Time).To(BeTemporally("==", t0.Add(3*time.Hour)))
		c := condition(dj, offpeakv1alpha1.ConditionSignalsAvailable)
		Expect(c.Status).To(Equal(metav1.ConditionFalse))
		Expect(c.Reason).To(Equal("SomeSignalsFailed"))
		Expect(c.Message).To(ContainSubstring("broken-b"))
		Expect(dj.Status.Estimates).To(HaveLen(1))
	})

	It("reports a missing SignalSource as a failed signal", func() {
		h := newHarness("missing-source", t0.Add(8*time.Hour), time.Hour,
			fakeResolver{"carbon-e": hourlySource("g", 50, 40, 30, 10, 30, 40, 50, 60)})
		dj := h.get()
		dj.Spec.Signals = append(dj.Spec.Signals, offpeakv1alpha1.SignalRef{SourceRef: "does-not-exist", Weight: 1})
		Expect(k8sClient.Update(ctx, dj)).To(Succeed())

		h.reconcile()
		c := condition(h.get(), offpeakv1alpha1.ConditionSignalsAvailable)
		Expect(c.Status).To(Equal(metav1.ConditionFalse))
		Expect(c.Message).To(ContainSubstring("does-not-exist"))
	})

	It("mirrors the Job state to the phase", func() {
		h := newHarness("mirror", t0.Add(8*time.Hour), time.Hour,
			fakeResolver{"carbon-f": hourlySource("g", 10, 50, 50, 50, 50, 50, 50, 50)})
		h.reconcile()
		Expect(h.get().Status.Phase).To(Equal(offpeakv1alpha1.PhaseRunning))

		job, err := h.job()
		Expect(err).NotTo(HaveOccurred())
		setJobConditions(job, batchv1.JobSuccessCriteriaMet, batchv1.JobComplete)

		h.reconcile()
		Expect(h.get().Status.Phase).To(Equal(offpeakv1alpha1.PhaseSucceeded))
	})

	It("maps a failed Job to the phase Failed", func() {
		h := newHarness("mirror-failed", t0.Add(8*time.Hour), time.Hour,
			fakeResolver{"carbon-g": hourlySource("g", 10, 50, 50, 50, 50, 50, 50, 50)})
		h.reconcile()

		job, err := h.job()
		Expect(err).NotTo(HaveOccurred())
		setJobConditions(job, batchv1.JobFailureTarget, batchv1.JobFailed)

		h.reconcile()
		Expect(h.get().Status.Phase).To(Equal(offpeakv1alpha1.PhaseFailed))
	})

	It("does not take a Job with the same name that someone else owns", func() {
		h := newHarness("taken", t0.Add(8*time.Hour), time.Hour,
			fakeResolver{"carbon-h": hourlySource("g", 10, 50, 50, 50, 50, 50, 50, 50)})
		foreign := &batchv1.Job{
			Name: "taken", Namespace: "default", Labels: map[string]string{"owner": "other"},
			Spec: *h.get().Spec.JobTemplate.Spec.DeepCopy(),
		}
		Expect(k8sClient.Create(ctx, foreign)).To(Succeed())

		h.reconcile()
		dj := h.get()
		c := condition(dj, offpeakv1alpha1.ConditionJobCreated)
		Expect(c).NotTo(BeNil())
		Expect(c.Status).To(Equal(metav1.ConditionFalse))
		Expect(c.Reason).To(Equal("NameConflict"))
		Expect(dj.Status.JobName).To(BeEmpty())

		job, err := h.job()
		Expect(err).NotTo(HaveOccurred())
		Expect(job.OwnerReferences).To(BeEmpty())
		Expect(job.Labels).To(HaveKeyWithValue("owner", "other"))
		Expect(job.Labels).NotTo(HaveKey(offpeakv1alpha1.LabelDeferrableJob))
	})

	It("adopts a Job that it created before a failed status update", func() {
		h := newHarness("adopt", t0.Add(8*time.Hour), time.Hour,
			fakeResolver{"carbon-i": hourlySource("g", 50, 40, 30, 10, 30, 40, 50, 60)})
		job, err := h.r.buildJob(h.get(), "adopt", offpeakv1alpha1.CapacityAny)
		Expect(err).NotTo(HaveOccurred())
		Expect(k8sClient.Create(ctx, job)).To(Succeed())

		// The plan says "wait", but the Job exists, so the controller adopts it.
		h.reconcile()
		dj := h.get()
		Expect(dj.Status.JobName).To(Equal("adopt"))
		Expect(dj.Status.Phase).To(Equal(offpeakv1alpha1.PhaseRunning))
	})

	It("ignores a DeferrableJob that does not exist", func() {
		r := &DeferrableJobReconciler{Client: k8sClient, Clock: clocktesting.NewFakeClock(t0)}
		_, err := r.Reconcile(ctx, ctrl.Request{Name: "gone", Namespace: "default"})
		Expect(err).NotTo(HaveOccurred())
	})
})
