package controller

import (
	"context"
	"fmt"
	"time"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	offpeakv1alpha1 "github.com/Thinnesen/offpeak/api/v1alpha1"
)

// DefaultFallbackMargin is the default safety time before the deadline for the on-demand fallback.
const DefaultFallbackMargin = 15 * time.Minute

// fallbackSuffix is the suffix of the name of the on-demand fallback Job.
const fallbackSuffix = "-od"

func capacityType(dj *offpeakv1alpha1.DeferrableJob) offpeakv1alpha1.CapacityType {
	if dj.Spec.Karpenter == nil || dj.Spec.Karpenter.CapacityType == "" {
		return offpeakv1alpha1.CapacityAny
	}
	return dj.Spec.Karpenter.CapacityType
}

func usesFallback(dj *offpeakv1alpha1.DeferrableJob) bool {
	return capacityType(dj) == offpeakv1alpha1.CapacitySpotWithFallback
}

// fallbackAt is the last time at which an on-demand run still meets the deadline.
func fallbackAt(dj *offpeakv1alpha1.DeferrableJob) time.Time {
	margin := DefaultFallbackMargin
	if m := dj.Spec.Karpenter.FallbackMargin; m != nil {
		margin = m.Duration
	}
	return dj.Spec.Deadline.Add(-dj.Spec.EstimatedDuration.Duration - margin)
}

// planDeadline is the planner deadline. With SpotWithFallback, the spot run must end at fallbackAt.
func planDeadline(dj *offpeakv1alpha1.DeferrableJob) time.Time {
	if usesFallback(dj) {
		return fallbackAt(dj)
	}
	return dj.Spec.Deadline.Time
}

// startCapacity is the capacity type of the first Job.
func startCapacity(dj *offpeakv1alpha1.DeferrableJob, now time.Time) offpeakv1alpha1.CapacityType {
	ct := capacityType(dj)
	if ct != offpeakv1alpha1.CapacitySpotWithFallback {
		return ct
	}
	// The same comparison as the planner: a spot run needs a latest start after now.
	if fallbackAt(dj).Add(-dj.Spec.EstimatedDuration.Duration).After(now) {
		return offpeakv1alpha1.CapacitySpot
	}
	return offpeakv1alpha1.CapacityOnDemand
}

// mirrorJob copies the Job state to the status. It replaces a failed, missing or slow spot Job.
func (r *DeferrableJobReconciler) mirrorJob(ctx context.Context, dj *offpeakv1alpha1.DeferrableJob) (ctrl.Result, error) {
	job := &batchv1.Job{}
	err := r.Get(ctx, client.ObjectKey{Namespace: dj.Namespace, Name: dj.Status.JobName}, job)
	if err != nil && !apierrors.IsNotFound(err) {
		return ctrl.Result{}, fmt.Errorf("get Job: %w", err)
	}
	// A Job that is being deleted (for example with an orphan finalizer) counts as missing.
	missing := err != nil || job.DeletionTimestamp != nil
	now := r.Clock.Now()

	if cur := currentAttempt(dj); usesFallback(dj) && cur != nil && cur.CapacityType == offpeakv1alpha1.CapacitySpot {
		switch {
		case missing:
			return r.fallback(ctx, dj, nil, offpeakv1alpha1.AttemptReplaced, "SpotJobMissing")
		case jobPhase(job) == offpeakv1alpha1.PhaseFailed:
			return r.fallback(ctx, dj, job, offpeakv1alpha1.AttemptFailed, "SpotFailed")
		case jobPhase(job) == offpeakv1alpha1.PhaseRunning && !now.Before(fallbackAt(dj)):
			return r.fallback(ctx, dj, job, offpeakv1alpha1.AttemptReplaced, "SpotTooSlow")
		case jobPhase(job) == offpeakv1alpha1.PhaseRunning:
			return ctrl.Result{RequeueAfter: fallbackAt(dj).Sub(now)}, r.updatePhase(ctx, dj, job, now)
		}
	}
	if missing {
		return ctrl.Result{}, nil // Someone deleted the Job. Keep the last known phase.
	}
	return ctrl.Result{}, r.updatePhase(ctx, dj, job, now)
}

// updatePhase writes the phase of the Job, and the result of a finished attempt.
func (r *DeferrableJobReconciler) updatePhase(
	ctx context.Context, dj *offpeakv1alpha1.DeferrableJob, job *batchv1.Job, now time.Time,
) error {
	phase := jobPhase(job)
	if phase == dj.Status.Phase {
		return nil
	}
	dj.Status.Phase = phase
	switch phase {
	case offpeakv1alpha1.PhaseSucceeded:
		finishAttempt(dj, offpeakv1alpha1.AttemptSucceeded, now)
	case offpeakv1alpha1.PhaseFailed:
		finishAttempt(dj, offpeakv1alpha1.AttemptFailed, now)
	}
	return r.Status().Update(ctx, dj)
}

// fallback creates or adopts the on-demand Job, then deletes the spot Job. Each step is safe to repeat.
func (r *DeferrableJobReconciler) fallback(
	ctx context.Context, dj *offpeakv1alpha1.DeferrableJob, spot *batchv1.Job,
	result offpeakv1alpha1.AttemptResult, reason string,
) (ctrl.Result, error) {
	od, err := r.buildJob(dj, dj.Name+fallbackSuffix, offpeakv1alpha1.CapacityOnDemand)
	if err != nil {
		return ctrl.Result{}, err
	}
	if err := r.Create(ctx, od); apierrors.IsAlreadyExists(err) {
		existing := &batchv1.Job{}
		if err := r.Get(ctx, client.ObjectKeyFromObject(od), existing); err != nil {
			return ctrl.Result{}, fmt.Errorf("get fallback Job: %w", err)
		}
		if !metav1.IsControlledBy(existing, dj) {
			setCondition(dj, offpeakv1alpha1.ConditionFallback, false, "NameConflict",
				fmt.Sprintf("Job %s exists and is not controlled by this DeferrableJob", od.Name))
			return ctrl.Result{RequeueAfter: retryInterval}, r.Status().Update(ctx, dj)
		}
	} else if err != nil {
		return ctrl.Result{}, fmt.Errorf("create fallback Job: %w", err)
	}
	if spot != nil {
		err := r.Delete(ctx, spot, client.PropagationPolicy(metav1.DeletePropagationBackground))
		if client.IgnoreNotFound(err) != nil {
			return ctrl.Result{}, fmt.Errorf("delete spot Job: %w", err)
		}
	}

	now := r.Clock.Now()
	logf.FromContext(ctx).Info("Started on-demand fallback Job", "job", od.Name, "reason", reason)
	r.Recorder.Eventf(dj, od, corev1.EventTypeNormal, "Fallback", "CreateJob",
		"Replaced the spot Job with the on-demand Job %s (%s)", od.Name, reason)
	finishAttempt(dj, result, now)
	recordAttempt(dj, od.Name, offpeakv1alpha1.CapacityOnDemand, now)
	setCondition(dj, offpeakv1alpha1.ConditionFallback, true, reason, "Started the on-demand Job "+od.Name)
	return ctrl.Result{}, r.Status().Update(ctx, dj)
}

func currentAttempt(dj *offpeakv1alpha1.DeferrableJob) *offpeakv1alpha1.JobAttempt {
	for i := range dj.Status.Attempts {
		if dj.Status.Attempts[i].JobName == dj.Status.JobName {
			return &dj.Status.Attempts[i]
		}
	}
	return nil
}

func finishAttempt(dj *offpeakv1alpha1.DeferrableJob, result offpeakv1alpha1.AttemptResult, now time.Time) {
	if cur := currentAttempt(dj); cur != nil {
		cur.Result = result
		cur.FinishedAt = &metav1.Time{Time: now}
	}
}
