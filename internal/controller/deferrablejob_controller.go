package controller

import (
	"context"
	"fmt"
	"time"

	batchv1 "k8s.io/api/batch/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/tools/events"
	"k8s.io/utils/clock"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	offpeakv1alpha1 "github.com/Thinnesen/offpeak/api/v1alpha1"
	"github.com/Thinnesen/offpeak/internal/signal"
)

const (
	// DefaultReplanInterval is the time between two plans of a waiting DeferrableJob.
	DefaultReplanInterval = 15 * time.Minute
	// startSlack starts the Job when the planned start is this close.
	startSlack = 30 * time.Second
	// retryInterval is the requeue time when no signal or no NodePool is available.
	retryInterval = time.Minute
)

// SourceResolver returns the signal source for a SignalSource.
type SourceResolver interface {
	Source(ctx context.Context, ss *offpeakv1alpha1.SignalSource) (signal.Source, error)
}

// DeferrableJobReconciler reconciles a DeferrableJob object
type DeferrableJobReconciler struct {
	client.Client
	Scheme         *runtime.Scheme
	Clock          clock.PassiveClock
	Sources        SourceResolver
	Recorder       events.EventRecorder
	ReplanInterval time.Duration
}

// +kubebuilder:rbac:groups=offpeak.thinnesen.com,resources=deferrablejobs,verbs=get;list;watch
// +kubebuilder:rbac:groups=offpeak.thinnesen.com,resources=deferrablejobs/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=offpeak.thinnesen.com,resources=deferrablejobs/finalizers,verbs=update
// +kubebuilder:rbac:groups=offpeak.thinnesen.com,resources=signalsources,verbs=get;list;watch
// +kubebuilder:rbac:groups=batch,resources=jobs,verbs=get;list;watch;create;delete
// +kubebuilder:rbac:groups=events.k8s.io,resources=events,verbs=create;patch

// Reconcile plans a DeferrableJob, starts its Job at the planned time and mirrors the Job state.
func (r *DeferrableJobReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	dj := &offpeakv1alpha1.DeferrableJob{}
	if err := r.Get(ctx, req.NamespacedName, dj); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	if dj.Status.JobName != "" {
		return r.mirrorJob(ctx, dj)
	}

	// A Job from an earlier reconcile can exist when the status update after its creation failed.
	existing := &batchv1.Job{}
	err := r.Get(ctx, client.ObjectKey{Namespace: dj.Namespace, Name: dj.Name}, existing)
	switch {
	case err == nil:
		return r.adoptOrConflict(ctx, dj, existing)
	case !apierrors.IsNotFound(err):
		return ctrl.Result{}, fmt.Errorf("get Job: %w", err)
	}

	var kinfo *karpenterInfo
	if ks := dj.Spec.Karpenter; ks != nil {
		info, reason, err := r.karpenter(ctx, dj)
		switch {
		case err != nil && reason == "":
			return ctrl.Result{}, err
		case err != nil:
			dj.Status.Phase = offpeakv1alpha1.PhaseWaiting
			setCondition(dj, offpeakv1alpha1.ConditionKarpenterReady, false, reason, err.Error())
			return ctrl.Result{RequeueAfter: retryInterval}, r.Status().Update(ctx, dj)
		}
		kinfo = info
		setCondition(dj, offpeakv1alpha1.ConditionKarpenterReady, true, "NodePoolFound", karpenterMessage(ks, info))
	}

	now := r.Clock.Now()
	p := r.plan(ctx, dj, now, kinfo)
	switch {
	case p.result == nil && now.Before(p.latest):
		return ctrl.Result{RequeueAfter: retryInterval}, r.Status().Update(ctx, dj)
	case p.result == nil:
		logf.FromContext(ctx).Info("Starting Job without signals because the deadline is near")
	case p.result.Start.After(now.Add(startSlack)):
		wait := min(p.result.Start.Sub(now), r.replanInterval())
		return ctrl.Result{RequeueAfter: wait}, r.Status().Update(ctx, dj)
	}
	return ctrl.Result{}, r.startJob(ctx, dj, now, startCapacity(dj, now))
}

func (r *DeferrableJobReconciler) replanInterval() time.Duration {
	if r.ReplanInterval > 0 {
		return r.ReplanInterval
	}
	return DefaultReplanInterval
}

// SetupWithManager sets up the controller with the Manager.
func (r *DeferrableJobReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&offpeakv1alpha1.DeferrableJob{}).
		Owns(&batchv1.Job{}).
		Named("deferrablejob").
		Complete(r)
}
