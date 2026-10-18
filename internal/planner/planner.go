// Package planner picks the best start time before a deadline. It has no I/O and no clock.
package planner

import (
	"errors"
	"fmt"
	"math"
	"time"

	"github.com/Thinnesen/offpeak/internal/forecast"
)

// DefaultStep is the distance between candidate start times.
const DefaultStep = 15 * time.Minute

// tieEpsilon is the largest score difference that counts as a tie.
const tieEpsilon = 1e-9

// Signal is one weighted input series.
type Signal struct {
	Name   string
	Weight float64
	// Series holds real and assumed points. The planner fills gaps with a neutral value.
	Series forecast.Series
}

// Input is the planner input.
type Input struct {
	Now time.Time
	// NotBefore is the earliest allowed start. The zero value means Now.
	NotBefore time.Time
	// Deadline is the time by which the job must finish.
	Deadline time.Time
	// Duration is the expected run time of the job.
	Duration time.Duration
	// Step is the distance between candidate starts. The zero value means DefaultStep.
	Step    time.Duration
	Signals []Signal
}

// Estimate is the expected mean of one signal over the run window.
type Estimate struct {
	Name, Unit string
	// Planned is the mean over the planned run window.
	Planned float64
	// IfStartedNow is the mean over the run window that starts at the earliest time.
	IfStartedNow float64
	// AssumedPercent is the part of the planned run window without real data.
	AssumedPercent float64
	// Available is false when the signal has no data in [earliest, deadline).
	Available bool
}

// Result is the plan.
type Result struct {
	// Start is the planned start time.
	Start time.Time
	// Latest is the last start time that meets the deadline.
	Latest time.Time
	// DeadlineAtRisk is true when Latest is not after the earliest start.
	DeadlineAtRisk bool
	Estimates      []Estimate
	// SavingsPercent is the weighted mean of the per-signal savings against a start at the earliest time.
	SavingsPercent float64
}

// signalMeans holds the mean and assumed fraction of one signal for each candidate.
type signalMeans struct {
	means, assumed []float64
	available      bool
}

// Plan returns the best start for in.
func Plan(in Input) (Result, error) {
	if err := validate(in); err != nil {
		return Result{}, err
	}
	step := in.Step
	if step == 0 {
		step = DefaultStep
	}
	earliest := in.Now
	if in.NotBefore.After(earliest) {
		earliest = in.NotBefore
	}
	res := Result{Latest: in.Deadline.Add(-in.Duration)}

	var starts []time.Time
	if res.Latest.After(earliest) {
		starts = candidates(earliest, res.Latest, step)
	} else {
		res.DeadlineAtRisk = true
		starts = []time.Time{earliest}
	}
	windowEnd := maxTime(in.Deadline, earliest.Add(in.Duration))

	all := make([]signalMeans, len(in.Signals))
	for i, sig := range in.Signals {
		all[i] = evaluate(sig.Series, starts, in.Duration, earliest, windowEnd)
	}
	best := pick(in.Signals, all)
	res.Start = starts[best]
	res.Estimates, res.SavingsPercent = estimates(in.Signals, all, best)
	return res, nil
}

func validate(in Input) error {
	if in.Duration <= 0 {
		return errors.New("duration must be positive")
	}
	if in.Step < 0 {
		return errors.New("step must not be negative")
	}
	if len(in.Signals) == 0 {
		return errors.New("at least one signal is required")
	}
	for _, s := range in.Signals {
		if !(s.Weight > 0) {
			return fmt.Errorf("signal %q: weight must be positive", s.Name)
		}
	}
	return nil
}

// candidates returns earliest, each step boundary (aligned in UTC) between earliest and latest, and latest.
func candidates(earliest, latest time.Time, step time.Duration) []time.Time {
	out := []time.Time{earliest}
	for t := earliest.Truncate(step).Add(step); t.Before(latest); t = t.Add(step) {
		out = append(out, t)
	}
	if latest.After(earliest) {
		out = append(out, latest)
	}
	return out
}

// evaluate computes the mean of s over each run window. Gaps get the mean of s over [from, to).
func evaluate(s forecast.Series, starts []time.Time, d time.Duration, from, to time.Time) signalMeans {
	neutral, ok := s.Window(from, to).Mean()
	sm := signalMeans{
		means:     make([]float64, len(starts)),
		assumed:   make([]float64, len(starts)),
		available: ok,
	}
	for i, start := range starts {
		w := s.Window(start, start.Add(d))
		gap := d - w.Covered
		sm.means[i] = (w.WeightedSum + neutral*gap.Seconds()) / d.Seconds()
		sm.assumed[i] = (w.Assumed + gap).Seconds() / d.Seconds()
	}
	return sm
}

// pick returns the candidate with the highest weighted saving against "start now". A tie picks the earliest.
func pick(signals []Signal, all []signalMeans) int {
	scores := make([]float64, len(all[0].means))
	for i, sm := range all {
		scale := sm.scale()
		if scale == 0 {
			continue
		}
		for c, m := range sm.means {
			scores[c] += signals[i].Weight * m / scale
		}
	}
	best := 0
	for c, score := range scores {
		if score < scores[best]-tieEpsilon {
			best = c
		}
	}
	return best
}

// scaleFloor stops a "start now" mean near 0 from giving a huge saving.
const scaleFloor = 0.1

// scale is |start now mean|, but at least scaleFloor times the mean size of all candidates.
func (sm signalMeans) scale() float64 {
	var sum float64
	for _, m := range sm.means {
		sum += math.Abs(m)
	}
	return max(math.Abs(sm.means[0]), scaleFloor*sum/float64(len(sm.means)))
}

func estimates(signals []Signal, all []signalMeans, best int) ([]Estimate, float64) {
	out := make([]Estimate, len(signals))
	var weighted, weights float64
	for i, sig := range signals {
		sm := all[i]
		out[i] = Estimate{Name: sig.Name, Unit: sig.Series.Unit, Available: sm.available}
		if !sm.available {
			continue
		}
		out[i].Planned = sm.means[best]
		out[i].IfStartedNow = sm.means[0]
		out[i].AssumedPercent = sm.assumed[best] * 100
		weights += sig.Weight
		if scale := sm.scale(); scale != 0 {
			weighted += sig.Weight * (sm.means[0] - sm.means[best]) / scale * 100
		}
	}
	if weights == 0 {
		return out, 0
	}
	return out, weighted / weights
}

func maxTime(a, b time.Time) time.Time {
	if a.After(b) {
		return a
	}
	return b
}
