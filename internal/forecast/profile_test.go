package forecast

import (
	"testing"
	"time"
)

func TestHourOfWeek(t *testing.T) {
	tests := []struct {
		t    time.Time
		want int
	}{
		{time.Date(2026, 10, 18, 0, 0, 0, 0, time.UTC), 0},   // Sunday 00:00
		{time.Date(2026, 10, 19, 0, 30, 0, 0, time.UTC), 24}, // Monday 00:30
		{time.Date(2026, 10, 24, 23, 59, 0, 0, time.UTC), 167},
		// 02:00 in UTC+2 is Monday 00:00 UTC.
		{time.Date(2026, 10, 19, 2, 0, 0, 0, time.FixedZone("CEST", 2*3600)), 24},
	}
	for _, tt := range tests {
		if got := hourOfWeek(tt.t); got != tt.want {
			t.Errorf("hourOfWeek(%v) = %d, want %d", tt.t, got, tt.want)
		}
	}
}

func TestNewProfile(t *testing.T) {
	week := 7 * 24 * 60
	assumed := pt(week*2, week*2+60, 1000)
	assumed.Assumed = true
	s, err := NewSeries("u", []Point{
		// Monday 00:00 bucket: values 10, 30, 20 (median 20).
		pt(0, 15, 10), pt(15, 30, 30), pt(week, week+60, 20),
		// The assumed point must not count.
		assumed,
		// Monday 01:00 bucket: values 1 and 3 (median 2).
		pt(60, 120, 1), pt(week+60, week+120, 3),
	})
	if err != nil {
		t.Fatal(err)
	}
	p := NewProfile(s)
	tests := []struct {
		bucket int
		want   float64
		wantOK bool
	}{
		{bucket: 24, want: 20, wantOK: true},
		{bucket: 25, want: 2, wantOK: true},
		{bucket: 26},
	}
	for _, tt := range tests {
		got, ok := p.Value(t0.Add(time.Duration(tt.bucket-24) * time.Hour))
		if ok != tt.wantOK || got != tt.want {
			t.Errorf("bucket %d: Value() = %v, %v, want %v, %v", tt.bucket, got, ok, tt.want, tt.wantOK)
		}
	}
}

func TestFill(t *testing.T) {
	s, err := NewSeries("u", []Point{pt(0, 15, 10), pt(15, 30, 30), pt(90, 120, 7)})
	if err != nil {
		t.Fatal(err)
	}
	p := NewProfile(s) // bucket Mon 00 = 20, Mon 01 = 7

	week := 7 * 24 * 60
	future, err := NewSeries("u", []Point{pt(week+30, week+60, 5)})
	if err != nil {
		t.Fatal(err)
	}
	got := future.Fill(p, at(week-30), at(week+180))

	assumed := func(p Point) Point { p.Assumed = true; return p }
	want := []Point{
		// Sunday 23:30 to 00:00: no profile value, gap stays.
		assumed(pt(week, week+30, 20)),
		pt(week+30, week+60, 5),
		assumed(pt(week+60, week+120, 7)),
		// Monday 02:00 to 03:00: no profile value, gap stays.
	}
	if got.Unit != "u" {
		t.Errorf("Unit = %q", got.Unit)
	}
	if len(got.Points) != len(want) {
		t.Fatalf("Fill() = %v, want %v", got.Points, want)
	}
	for i := range want {
		g := got.Points[i]
		if !g.Start.Equal(want[i].Start) || !g.End.Equal(want[i].End) ||
			g.Value != want[i].Value || g.Assumed != want[i].Assumed {
			t.Errorf("Points[%d] = %+v, want %+v", i, g, want[i])
		}
	}
}

func TestFillEmptyProfile(t *testing.T) {
	s, err := NewSeries("u", []Point{pt(0, 60, 1)})
	if err != nil {
		t.Fatal(err)
	}
	got := s.Fill(Profile{}, at(-60), at(120))
	if len(got.Points) != 1 {
		t.Errorf("Fill() with empty profile = %v, want the input", got.Points)
	}
}

func TestComplete(t *testing.T) {
	week := 7 * 24 * 60
	s, err := NewSeries("u", []Point{pt(0, 30, 4), pt(30, 60, 6), pt(week, week+30, 9)})
	if err != nil {
		t.Fatal(err)
	}
	got := s.Complete(at(week), at(week+120))
	want := []Point{pt(week, week+30, 9), {Start: at(week + 30), End: at(week + 60), Value: 6, Assumed: true}}
	if len(got.Points) != len(want) {
		t.Fatalf("Complete() = %+v, want %+v", got.Points, want)
	}
	for i := range want {
		if got.Points[i] != want[i] {
			t.Errorf("Points[%d] = %+v, want %+v", i, got.Points[i], want[i])
		}
	}
}
