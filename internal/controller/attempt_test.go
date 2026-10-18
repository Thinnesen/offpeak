package controller

import (
	"testing"
	"time"

	offpeakv1alpha1 "github.com/Thinnesen/offpeak/api/v1alpha1"
)

func TestRecordAttemptIsIdempotent(t *testing.T) {
	dj := &offpeakv1alpha1.DeferrableJob{}
	now := time.Date(2026, 10, 18, 12, 0, 0, 0, time.UTC)
	recordAttempt(dj, "a", offpeakv1alpha1.CapacitySpot, now)
	finishAttempt(dj, offpeakv1alpha1.AttemptFailed, now)
	recordAttempt(dj, "a-od", offpeakv1alpha1.CapacityOnDemand, now)
	recordAttempt(dj, "a-od", offpeakv1alpha1.CapacityOnDemand, now.Add(time.Minute))

	if len(dj.Status.Attempts) != 2 {
		t.Fatalf("attempts = %+v, want 2 entries", dj.Status.Attempts)
	}
	if got := dj.Status.Attempts[1]; got.Result != offpeakv1alpha1.AttemptRunning || !got.StartedAt.Time.Equal(now) {
		t.Errorf("repeated attempt = %+v, want the first start time and Running", got)
	}
	if dj.Status.JobName != "a-od" {
		t.Errorf("JobName = %q", dj.Status.JobName)
	}
}
