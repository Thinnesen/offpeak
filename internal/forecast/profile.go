package forecast

import (
	"slices"
	"time"
)

const hoursPerWeek = 7 * 24

// Profile holds the median value for each hour of the week, in UTC.
type Profile struct {
	values [hoursPerWeek]float64
	ok     [hoursPerWeek]bool
}

// hourOfWeek returns the bucket of t: 0 is Sunday 00:00 to 01:00 UTC, 167 is Saturday 23:00 to 24:00 UTC.
func hourOfWeek(t time.Time) int {
	t = t.UTC()
	return int(t.Weekday())*24 + t.Hour()
}

// NewProfile computes the hour-of-week median of the real points. A point counts for the hour of its start.
func NewProfile(history Series) Profile {
	var buckets [hoursPerWeek][]float64
	for _, p := range history.Points {
		if p.Assumed {
			continue
		}
		h := hourOfWeek(p.Start)
		buckets[h] = append(buckets[h], p.Value)
	}
	var prof Profile
	for h, vals := range buckets {
		if len(vals) == 0 {
			continue
		}
		prof.values[h] = median(vals)
		prof.ok[h] = true
	}
	return prof
}

// Value returns the profile value for the hour of the week of t.
func (p Profile) Value(t time.Time) (float64, bool) {
	h := hourOfWeek(t)
	return p.values[h], p.ok[h]
}

// Fill returns a copy of s with profile values in the gaps of [from, to), in full UTC hours.
func (s Series) Fill(p Profile, from, to time.Time) Series {
	points := slices.Clone(s.Points)
	cursor := from
	for _, pt := range s.Points {
		if !pt.End.After(cursor) {
			continue
		}
		if !pt.Start.Before(to) {
			break
		}
		points = p.appendGap(points, cursor, minTime(pt.Start, to))
		cursor = pt.End
	}
	points = p.appendGap(points, cursor, to)
	slices.SortFunc(points, func(a, b Point) int { return a.Start.Compare(b.Start) })
	return Series{Unit: s.Unit, Points: points}
}

// appendGap appends assumed points for the gap [from, to) to points.
func (p Profile) appendGap(points []Point, from, to time.Time) []Point {
	for t := from; t.Before(to); {
		end := minTime(t.Truncate(time.Hour).Add(time.Hour), to)
		if v, ok := p.Value(t); ok {
			points = append(points, Point{Start: t, End: end, Value: v, Assumed: true})
		}
		t = end
	}
	return points
}

// median returns the median of vals. It reorders vals.
func median(vals []float64) float64 {
	slices.Sort(vals)
	n := len(vals)
	if n%2 == 1 {
		return vals[n/2]
	}
	return (vals[n/2-1] + vals[n/2]) / 2
}

// Complete fills the gaps in [from, to) from the profile of s and returns the points in that range.
func (s Series) Complete(from, to time.Time) Series {
	return s.Fill(NewProfile(s), from, to).Between(from, to)
}
