package controller

import (
	"context"
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"

	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	offpeakv1alpha1 "github.com/Thinnesen/offpeak/api/v1alpha1"
	"github.com/Thinnesen/offpeak/internal/forecast"
	"github.com/Thinnesen/offpeak/internal/planner"
	"github.com/Thinnesen/offpeak/internal/signal"
)

// planOutcome is the result of one planning step. result is nil when no signal is available.
type planOutcome struct {
	result *planner.Result
	latest time.Time
}

type signalFailure struct {
	name string
	err  error
}

// plan fetches the signals, runs the planner and writes the plan into dj.Status (not to the API).
func (r *DeferrableJobReconciler) plan(
	ctx context.Context, dj *offpeakv1alpha1.DeferrableJob, now time.Time, kinfo *karpenterInfo,
) planOutcome {
	spec := dj.Spec
	earliest := now
	if spec.NotBefore != nil && spec.NotBefore.After(now) {
		earliest = spec.NotBefore.Time
	}
	duration := spec.EstimatedDuration.Duration
	deadline := planDeadline(dj)
	latest := deadline.Add(-duration)
	query := signal.Query{From: earliest, To: maxTime(deadline, earliest.Add(duration))}

	signals, failures := r.fetchSignals(ctx, dj, query, kinfo)

	st := &dj.Status
	st.Phase = offpeakv1alpha1.PhaseWaiting
	st.LatestStart = &metav1.Time{Time: latest}
	st.LastPlanTime = &metav1.Time{Time: now}
	setSignalsCondition(dj, failures, len(signals))

	if len(signals) == 0 {
		st.PlannedStart, st.Estimates, st.SavingsPercent = nil, nil, nil
		setCondition(dj, offpeakv1alpha1.ConditionPlanned, false, "NoSignals", "No signal is available")
		return planOutcome{latest: latest}
	}
	res, err := planner.Plan(planner.Input{
		Now: now, NotBefore: earliest, Deadline: deadline, Duration: duration, Signals: signals,
	})
	if err != nil {
		setCondition(dj, offpeakv1alpha1.ConditionPlanned, false, "PlanFailed", err.Error())
		return planOutcome{latest: latest}
	}
	writePlan(dj, res)
	setCondition(dj, offpeakv1alpha1.ConditionPlanned, true, "Planned",
		"Planned start "+res.Start.UTC().Format(time.RFC3339))
	if res.DeadlineAtRisk {
		setCondition(dj, offpeakv1alpha1.ConditionDeadlineAtRisk, true, "NotEnoughTime",
			"The Job cannot wait: the latest start is not after the earliest start")
	} else {
		setCondition(dj, offpeakv1alpha1.ConditionDeadlineAtRisk, false, "EnoughTime", "")
	}
	return planOutcome{result: &res, latest: latest}
}

// fetchSignals returns the planner input for each signal that works, and the failures.
func (r *DeferrableJobReconciler) fetchSignals(
	ctx context.Context, dj *offpeakv1alpha1.DeferrableJob, q signal.Query, kinfo *karpenterInfo,
) ([]planner.Signal, []signalFailure) {
	var signals []planner.Signal
	var failures []signalFailure
	for _, ref := range dj.Spec.Signals {
		series, err := r.fetchSignal(ctx, ref.SourceRef, q, kinfo)
		if err != nil {
			failures = append(failures, signalFailure{name: ref.SourceRef, err: err})
			continue
		}
		signals = append(signals, planner.Signal{Name: ref.SourceRef, Weight: float64(ref.Weight), Series: series})
	}
	return signals, failures
}

// fetchSignal fetches one signal. awsSpot uses the NodePool instance types, and has no effect without spot.
func (r *DeferrableJobReconciler) fetchSignal(
	ctx context.Context, name string, q signal.Query, kinfo *karpenterInfo,
) (forecast.Series, error) {
	ss := &offpeakv1alpha1.SignalSource{}
	if err := r.Get(ctx, client.ObjectKey{Name: name}, ss); err != nil {
		return forecast.Series{}, fmt.Errorf("get SignalSource: %w", err)
	}
	if ss.Spec.AWSSpot != nil {
		switch {
		case kinfo != nil && !kinfo.spotAllowed:
			return forecast.Series{}, nil
		case kinfo == nil || len(kinfo.instanceTypes) == 0:
			return forecast.Series{}, errors.New(
				"no instance types: set spec.karpenter.instanceTypes or an instance-type requirement on the NodePool")
		}
		q.InstanceTypes = kinfo.instanceTypes
	}
	src, err := r.Sources.Source(ctx, ss)
	if err != nil {
		r.recordSource(ctx, ss, "", err)
		return forecast.Series{}, err
	}
	series, err := src.Fetch(ctx, q)
	r.recordSource(ctx, ss, series.Unit, err)
	return series, err
}

func setSignalsCondition(dj *offpeakv1alpha1.DeferrableJob, failures []signalFailure, ok int) {
	if len(failures) == 0 {
		setCondition(dj, offpeakv1alpha1.ConditionSignalsAvailable, true, "AllSignalsAvailable", "")
		return
	}
	msgs := make([]string, len(failures))
	for i, f := range failures {
		msgs[i] = f.name + ": " + f.err.Error()
	}
	reason := "SomeSignalsFailed"
	if ok == 0 {
		reason = "AllSignalsFailed"
	}
	setCondition(dj, offpeakv1alpha1.ConditionSignalsAvailable, false, reason, strings.Join(msgs, "; "))
}

func setCondition(dj *offpeakv1alpha1.DeferrableJob, condType string, status bool, reason, msg string) {
	s := metav1.ConditionFalse
	if status {
		s = metav1.ConditionTrue
	}
	meta.SetStatusCondition(&dj.Status.Conditions, metav1.Condition{
		Type: condType, Status: s, Reason: reason, Message: msg, ObservedGeneration: dj.Generation,
	})
}

func writePlan(dj *offpeakv1alpha1.DeferrableJob, res planner.Result) {
	st := &dj.Status
	st.PlannedStart = &metav1.Time{Time: res.Start}
	st.SavingsPercent = new(int32(math.Round(res.SavingsPercent)))
	st.Estimates = nil
	for _, e := range res.Estimates {
		if !e.Available {
			continue
		}
		st.Estimates = append(st.Estimates, offpeakv1alpha1.SignalEstimate{
			Source:         e.Name,
			Unit:           e.Unit,
			Planned:        formatValue(e.Planned),
			IfStartedNow:   formatValue(e.IfStartedNow),
			AssumedPercent: int32(math.Round(e.AssumedPercent)),
		})
	}
}

// formatValue rounds v to two decimals.
func formatValue(v float64) string {
	return strconv.FormatFloat(math.Round(v*100)/100, 'f', -1, 64)
}

func maxTime(a, b time.Time) time.Time {
	if a.After(b) {
		return a
	}
	return b
}
