package controller

import (
	"context"
	"fmt"
	"maps"
	"strings"
	"time"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	offpeakv1alpha1 "github.com/Thinnesen/offpeak/api/v1alpha1"
)

// buildJob returns the Job with the given name and capacity type for dj, with dj as its controller.
func (r *DeferrableJobReconciler) buildJob(
	dj *offpeakv1alpha1.DeferrableJob, name string, ct offpeakv1alpha1.CapacityType,
) (*batchv1.Job, error) {
	tmpl := dj.Spec.JobTemplate.DeepCopy()
	labels := maps.Clone(tmpl.Labels)
	if labels == nil {
		labels = map[string]string{}
	}
	labels[offpeakv1alpha1.LabelDeferrableJob] = dj.Name
	job := &batchv1.Job{
		Name:        name,
		Namespace:   dj.Namespace,
		Labels:      labels,
		Annotations: tmpl.Annotations,
		Spec:        tmpl.Spec,
	}
	if dj.Spec.Karpenter != nil {
		applyKarpenter(&job.Spec.Template.Spec, dj.Spec.Karpenter, ct)
	}
	if ct == offpeakv1alpha1.CapacitySpot {
		ignoreSpotInterruptions(job)
	}
	if err := controllerutil.SetControllerReference(dj, job, r.Scheme); err != nil {
		return nil, fmt.Errorf("set owner reference: %w", err)
	}
	return job, nil
}

// startJob creates the first Job and records the start in the status.
func (r *DeferrableJobReconciler) startJob(
	ctx context.Context, dj *offpeakv1alpha1.DeferrableJob, now time.Time, ct offpeakv1alpha1.CapacityType,
) error {
	job, err := r.buildJob(dj, dj.Name, ct)
	if err != nil {
		return err
	}
	if err := r.Create(ctx, job); err != nil {
		// On AlreadyExists, the next reconcile adopts the Job or reports the conflict.
		return fmt.Errorf("create Job: %w", err)
	}
	logf.FromContext(ctx).Info("Created Job", "job", job.Name, "capacityType", ct)
	r.Recorder.Eventf(dj, job, corev1.EventTypeNormal, "Started", "CreateJob",
		"Started Job %s. %s", job.Name, estimatesSummary(dj))

	dj.Status.StartedAt = &metav1.Time{Time: now}
	recordAttempt(dj, job.Name, ct, now)
	setCondition(dj, offpeakv1alpha1.ConditionJobCreated, true, "Created", "Created Job "+job.Name)
	return r.Status().Update(ctx, dj)
}

// recordAttempt makes the Job the current Job. A repeated call does not add a second attempt.
func recordAttempt(dj *offpeakv1alpha1.DeferrableJob, name string, ct offpeakv1alpha1.CapacityType, at time.Time) {
	dj.Status.JobName = name
	dj.Status.Phase = offpeakv1alpha1.PhaseRunning
	if cur := currentAttempt(dj); cur != nil {
		cur.Result, cur.FinishedAt = offpeakv1alpha1.AttemptRunning, nil
		return
	}
	dj.Status.Attempts = append(dj.Status.Attempts, offpeakv1alpha1.JobAttempt{
		JobName: name, CapacityType: ct, StartedAt: &metav1.Time{Time: at}, Result: offpeakv1alpha1.AttemptRunning,
	})
}

// adoptOrConflict adopts a Job that dj controls. Else it reports the conflict.
func (r *DeferrableJobReconciler) adoptOrConflict(
	ctx context.Context, dj *offpeakv1alpha1.DeferrableJob, job *batchv1.Job,
) (ctrl.Result, error) {
	if !metav1.IsControlledBy(job, dj) {
		setCondition(dj, offpeakv1alpha1.ConditionJobCreated, false, "NameConflict",
			fmt.Sprintf("Job %s exists and is not controlled by this DeferrableJob", job.Name))
		return ctrl.Result{RequeueAfter: r.replanInterval()}, r.Status().Update(ctx, dj)
	}
	dj.Status.StartedAt = &job.CreationTimestamp
	recordAttempt(dj, job.Name, jobCapacity(job), job.CreationTimestamp.Time)
	setCondition(dj, offpeakv1alpha1.ConditionJobCreated, true, "Adopted", "Adopted Job "+job.Name)
	if err := r.Status().Update(ctx, dj); err != nil {
		return ctrl.Result{}, err
	}
	return r.mirrorJob(ctx, dj)
}

// jobCapacity reads the capacity type from the node selector of the Job.
func jobCapacity(job *batchv1.Job) offpeakv1alpha1.CapacityType {
	for ct, label := range capacityLabels {
		if job.Spec.Template.Spec.NodeSelector[capacityTypeKey] == label {
			return ct
		}
	}
	return offpeakv1alpha1.CapacityAny
}

func jobPhase(job *batchv1.Job) offpeakv1alpha1.DeferrableJobPhase {
	for _, c := range job.Status.Conditions {
		if c.Status != corev1.ConditionTrue {
			continue
		}
		switch c.Type {
		case batchv1.JobComplete:
			return offpeakv1alpha1.PhaseSucceeded
		case batchv1.JobFailed:
			return offpeakv1alpha1.PhaseFailed
		}
	}
	return offpeakv1alpha1.PhaseRunning
}

func estimatesSummary(dj *offpeakv1alpha1.DeferrableJob) string {
	if len(dj.Status.Estimates) == 0 {
		return "No estimates."
	}
	parts := make([]string, len(dj.Status.Estimates))
	for i, e := range dj.Status.Estimates {
		parts[i] = fmt.Sprintf("%s: %s %s (now %s)", e.Source, e.Planned, e.Unit, e.IfStartedNow)
	}
	return "Planned means: " + strings.Join(parts, ", ") + "."
}
