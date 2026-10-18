package controller

import (
	"context"
	"time"

	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	offpeakv1alpha1 "github.com/Thinnesen/offpeak/api/v1alpha1"
)

// sourceStatusInterval limits the status writes of a SignalSource that keeps working.
const sourceStatusInterval = 5 * time.Minute

// +kubebuilder:rbac:groups=offpeak.thinnesen.com,resources=signalsources/status,verbs=get;update;patch

// recordSource writes the fetch result to the SignalSource status. A failed write is only logged.
func (r *DeferrableJobReconciler) recordSource(
	ctx context.Context, ss *offpeakv1alpha1.SignalSource, unit string, fetchErr error,
) {
	if !setSourceStatus(ss, unit, fetchErr, r.Clock.Now()) {
		return
	}
	if err := r.Status().Update(ctx, ss); err != nil {
		logf.FromContext(ctx).V(1).Info("Could not update SignalSource status", "signalSource", ss.Name, "error", err.Error())
	}
}

// setSourceStatus updates the status. It returns false when no write is necessary.
func setSourceStatus(ss *offpeakv1alpha1.SignalSource, unit string, fetchErr error, now time.Time) bool {
	cond := meta.FindStatusCondition(ss.Status.Conditions, "Ready")
	if fetchErr != nil {
		if cond != nil && cond.Status == metav1.ConditionFalse && cond.Reason == "FetchFailed" {
			return false
		}
		meta.SetStatusCondition(&ss.Status.Conditions, metav1.Condition{
			Type: "Ready", Status: metav1.ConditionFalse, Reason: "FetchFailed", Message: fetchErr.Error(),
		})
		return true
	}
	recent := ss.Status.LastFetchTime != nil && now.Sub(ss.Status.LastFetchTime.Time) < sourceStatusInterval
	if cond != nil && cond.Status == metav1.ConditionTrue && ss.Status.Unit == unit && recent {
		return false
	}
	meta.SetStatusCondition(&ss.Status.Conditions, metav1.Condition{
		Type: "Ready", Status: metav1.ConditionTrue, Reason: "Fetched", Message: "The last fetch worked",
	})
	ss.Status.Unit = unit
	ss.Status.LastFetchTime = &metav1.Time{Time: now}
	return true
}
