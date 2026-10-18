package controller

import (
	"errors"
	"testing"
	"time"

	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	offpeakv1alpha1 "github.com/Thinnesen/offpeak/api/v1alpha1"
)

func TestSourceStatusChanged(t *testing.T) {
	now := time.Date(2026, 10, 18, 12, 0, 0, 0, time.UTC)
	ready := func(ss *offpeakv1alpha1.SignalSource, status metav1.ConditionStatus, reason, unit string, at time.Time) {
		meta.SetStatusCondition(&ss.Status.Conditions, metav1.Condition{Type: "Ready", Status: status, Reason: reason})
		ss.Status.Unit = unit
		ss.Status.LastFetchTime = &metav1.Time{Time: at}
	}
	tests := []struct {
		name  string
		setup func(*offpeakv1alpha1.SignalSource)
		unit  string
		err   error
		want  bool
	}{
		{name: "no status yet", setup: func(*offpeakv1alpha1.SignalSource) {}, unit: "g", want: true},
		{name: "same and recent", unit: "g", setup: func(ss *offpeakv1alpha1.SignalSource) {
			ready(ss, metav1.ConditionTrue, "Fetched", "g", now.Add(-time.Minute))
		}},
		{name: "same but old", unit: "g", want: true, setup: func(ss *offpeakv1alpha1.SignalSource) {
			ready(ss, metav1.ConditionTrue, "Fetched", "g", now.Add(-sourceStatusInterval))
		}},
		{name: "new unit", unit: "EUR/MWh", want: true, setup: func(ss *offpeakv1alpha1.SignalSource) {
			ready(ss, metav1.ConditionTrue, "Fetched", "g", now.Add(-time.Minute))
		}},
		{name: "now failing", err: errors.New("x"), want: true, setup: func(ss *offpeakv1alpha1.SignalSource) {
			ready(ss, metav1.ConditionTrue, "Fetched", "g", now.Add(-time.Minute))
		}},
		{name: "still failing", err: errors.New("x"), setup: func(ss *offpeakv1alpha1.SignalSource) {
			ready(ss, metav1.ConditionFalse, "FetchFailed", "g", now.Add(-time.Minute))
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ss := &offpeakv1alpha1.SignalSource{}
			tt.setup(ss)
			if got := setSourceStatus(ss, tt.unit, tt.err, now); got != tt.want {
				t.Errorf("setSourceStatus() = %v, want %v", got, tt.want)
			}
		})
	}
}
