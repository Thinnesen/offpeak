package forecast

import (
	"math"
	"testing"
	"time"
)

var t0 = time.Date(2026, 10, 19, 0, 0, 0, 0, time.UTC) // a Monday

func at(m int) time.Time { return t0.Add(time.Duration(m) * time.Minute) }

func pt(fromMin, toMin int, v float64) Point {
	return Point{Start: at(fromMin), End: at(toMin), Value: v}
}

func TestNewSeries(t *testing.T) {
	tests := []struct {
		name    string
		points  []Point
		want    []Point
		wantErr bool
	}{
		{name: "empty", points: nil, want: nil},
		{
			name:   "sorts by start",
			points: []Point{pt(60, 120, 2), pt(0, 60, 1)},
			want:   []Point{pt(0, 60, 1), pt(60, 120, 2)},
		},
		{
			name:   "gaps are allowed",
			points: []Point{pt(0, 15, 1), pt(30, 45, 2)},
			want:   []Point{pt(0, 15, 1), pt(30, 45, 2)},
		},
		{name: "overlap", points: []Point{pt(0, 60, 1), pt(30, 90, 2)}, wantErr: true},
		{name: "empty point", points: []Point{pt(0, 0, 1)}, wantErr: true},
		{name: "end before start", points: []Point{pt(60, 0, 1)}, wantErr: true},
		{name: "NaN value", points: []Point{pt(0, 60, math.NaN())}, wantErr: true},
		{name: "infinite value", points: []Point{pt(0, 60, math.Inf(1))}, wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s, err := NewSeries("EUR/MWh", tt.points)
			if (err != nil) != tt.wantErr {
				t.Fatalf("NewSeries() error = %v, wantErr %v", err, tt.wantErr)
			}
			if tt.wantErr {
				return
			}
			if s.Unit != "EUR/MWh" {
				t.Errorf("Unit = %q", s.Unit)
			}
			if len(s.Points) != len(tt.want) {
				t.Fatalf("Points = %v, want %v", s.Points, tt.want)
			}
			for i := range tt.want {
				if !s.Points[i].Start.Equal(tt.want[i].Start) || s.Points[i].Value != tt.want[i].Value {
					t.Errorf("Points[%d] = %v, want %v", i, s.Points[i], tt.want[i])
				}
			}
		})
	}
}

func TestNewSeriesDoesNotChangeInput(t *testing.T) {
	in := []Point{pt(60, 120, 2), pt(0, 60, 1)}
	if _, err := NewSeries("u", in); err != nil {
		t.Fatal(err)
	}
	if in[0].Value != 2 {
		t.Errorf("input slice was changed: %v", in)
	}
}

func TestSeriesWindow(t *testing.T) {
	assumed := pt(120, 180, 30)
	assumed.Assumed = true
	s, err := NewSeries("u", []Point{pt(0, 60, 10), pt(60, 120, 20), assumed, pt(240, 300, 50)})
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name               string
		from, to           int
		wantSum            float64 // value * seconds
		wantKnown, wantAss time.Duration
	}{
		{name: "one full point", from: 0, to: 60, wantSum: 10 * 3600, wantKnown: time.Hour},
		{name: "partial overlap", from: 30, to: 90, wantSum: 10*1800 + 20*1800, wantKnown: time.Hour},
		{
			name: "assumed point", from: 90, to: 150, wantSum: 20*1800 + 30*1800,
			wantKnown: time.Hour, wantAss: 30 * time.Minute,
		},
		{name: "inside a gap", from: 185, to: 235},
		{
			name: "across a gap", from: 150, to: 270, wantSum: 30*1800 + 50*1800,
			wantKnown: time.Hour, wantAss: 30 * time.Minute,
		},
		{name: "before all points", from: -60, to: 0},
		{name: "after all points", from: 300, to: 400},
		{name: "empty range", from: 30, to: 30},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := s.Window(at(tt.from), at(tt.to))
			if math.Abs(got.WeightedSum-tt.wantSum) > 1e-6 {
				t.Errorf("WeightedSum = %v, want %v", got.WeightedSum, tt.wantSum)
			}
			if got.Covered != tt.wantKnown {
				t.Errorf("Covered = %v, want %v", got.Covered, tt.wantKnown)
			}
			if got.Assumed != tt.wantAss {
				t.Errorf("Assumed = %v, want %v", got.Assumed, tt.wantAss)
			}
		})
	}
}

func TestWindowStatsMean(t *testing.T) {
	w := WindowStats{WeightedSum: 3600 * 10, Covered: time.Hour}
	if got, ok := w.Mean(); !ok || got != 10 {
		t.Errorf("Mean() = %v, %v, want 10, true", got, ok)
	}
	if _, ok := (WindowStats{}).Mean(); ok {
		t.Error("Mean() of empty stats is ok, want not ok")
	}
}

func TestSeriesBetween(t *testing.T) {
	s, err := NewSeries("u", []Point{pt(0, 60, 1), pt(60, 120, 2), pt(120, 180, 3)})
	if err != nil {
		t.Fatal(err)
	}
	got := s.Between(at(90), at(120))
	if got.Unit != "u" || len(got.Points) != 1 || got.Points[0].Value != 2 {
		t.Errorf("Between() = %+v, want only the second point", got)
	}
	if got := s.Between(at(30), at(150)); len(got.Points) != 3 {
		t.Errorf("Between() = %+v, want all points that overlap", got)
	}
}
