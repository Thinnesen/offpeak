// Package forecast holds signal time series and the gap fill rules for unknown future slots.
package forecast

import (
	"cmp"
	"fmt"
	"math"
	"slices"
	"time"
)

// Point is the value of a signal over the half-open interval [Start, End).
type Point struct {
	Start, End time.Time
	Value      float64
	// Assumed is true when the value is not real data, for example a profile value.
	Assumed bool
}

// Series is a time series. The points are sorted and do not overlap. Gaps are allowed.
type Series struct {
	Unit   string
	Points []Point
}

// NewSeries returns a validated Series. It sorts a copy of points by start.
func NewSeries(unit string, points []Point) (Series, error) {
	sorted := slices.Clone(points)
	slices.SortFunc(sorted, func(a, b Point) int { return a.Start.Compare(b.Start) })
	for i, p := range sorted {
		if !p.End.After(p.Start) {
			return Series{}, fmt.Errorf("point at %s: end is not after start", p.Start)
		}
		if math.IsNaN(p.Value) || math.IsInf(p.Value, 0) {
			return Series{}, fmt.Errorf("point at %s: value is not finite", p.Start)
		}
		if i > 0 && p.Start.Before(sorted[i-1].End) {
			return Series{}, fmt.Errorf("point at %s: overlaps the previous point", p.Start)
		}
	}
	return Series{Unit: unit, Points: sorted}, nil
}

// WindowStats describes the part of a time range that a Series covers.
type WindowStats struct {
	// WeightedSum is the sum of value times overlap in seconds.
	WeightedSum float64
	// Covered is the total overlap of all points with the range.
	Covered time.Duration
	// Assumed is the part of Covered that comes from assumed points.
	Assumed time.Duration
}

// Mean returns the time-weighted mean, or false when no point covers the range.
func (w WindowStats) Mean() (float64, bool) {
	if w.Covered <= 0 {
		return 0, false
	}
	return w.WeightedSum / w.Covered.Seconds(), true
}

// Window returns the statistics of the series over [from, to).
func (s Series) Window(from, to time.Time) WindowStats {
	var w WindowStats
	// The first point that ends after from.
	i, _ := slices.BinarySearchFunc(s.Points, from, func(p Point, t time.Time) int {
		return cmp.Compare(p.End.UnixNano(), t.UnixNano()+1)
	})
	for ; i < len(s.Points) && s.Points[i].Start.Before(to); i++ {
		p := s.Points[i]
		overlap := minTime(p.End, to).Sub(maxTime(p.Start, from))
		if overlap <= 0 {
			continue
		}
		w.WeightedSum += p.Value * overlap.Seconds()
		w.Covered += overlap
		if p.Assumed {
			w.Assumed += overlap
		}
	}
	return w
}

func minTime(a, b time.Time) time.Time {
	if a.Before(b) {
		return a
	}
	return b
}

func maxTime(a, b time.Time) time.Time {
	if a.After(b) {
		return a
	}
	return b
}

// Between returns the points that overlap [from, to).
func (s Series) Between(from, to time.Time) Series {
	var out []Point
	for _, p := range s.Points {
		if p.End.After(from) && p.Start.Before(to) {
			out = append(out, p)
		}
	}
	return Series{Unit: s.Unit, Points: out}
}
