package planner

import (
	"math"
	"testing"
	"time"

	"github.com/Thinnesen/offpeak/internal/forecast"
)

const priceName = "price"

var t0 = time.Date(2026, 10, 19, 0, 0, 0, 0, time.UTC)

func at(m int) time.Time { return t0.Add(time.Duration(m) * time.Minute) }

// hourly returns a series with one point per hour from t0.
func hourly(t *testing.T, unit string, values ...float64) forecast.Series {
	t.Helper()
	pts := make([]forecast.Point, len(values))
	for i, v := range values {
		pts[i] = forecast.Point{Start: at(i * 60), End: at((i + 1) * 60), Value: v}
	}
	s, err := forecast.NewSeries(unit, pts)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func series(t *testing.T, pts ...forecast.Point) forecast.Series {
	t.Helper()
	s, err := forecast.NewSeries("u", pts)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func near(a, b float64) bool { return math.Abs(a-b) < 1e-6 }

func TestPlan(t *testing.T) {
	price := hourly(t, "EUR/MWh", 50, 40, 30, 10, 30, 40, 50, 60)
	carbon := hourly(t, "g", 10, 100, 100, 100, 100, 100, 100, 100)
	carbonDip := hourly(t, "g", 100, 100, 50, 100, 100, 100, 100, 100)

	tests := []struct {
		name        string
		in          Input
		wantStart   time.Time
		wantLatest  time.Time
		wantAtRisk  bool
		wantSavings float64
	}{
		{
			name: "cheapest hour later",
			in: Input{
				Now: at(0), Deadline: at(8 * 60), Duration: time.Hour,
				Signals: []Signal{{Name: priceName, Weight: 1, Series: price}},
			},
			wantStart: at(3 * 60), wantLatest: at(7 * 60), wantSavings: 80,
		},
		{
			name: "not before moves the earliest start",
			in: Input{
				Now: at(0), NotBefore: at(4 * 60), Deadline: at(8 * 60), Duration: time.Hour,
				Signals: []Signal{{Name: priceName, Weight: 1, Series: price}},
			},
			wantStart: at(4 * 60), wantLatest: at(7 * 60), wantSavings: 0,
		},
		{
			name: "not before in the past is ignored",
			in: Input{
				Now: at(0), NotBefore: at(-60), Deadline: at(8 * 60), Duration: time.Hour,
				Signals: []Signal{{Name: priceName, Weight: 1, Series: price}},
			},
			wantStart: at(3 * 60), wantLatest: at(7 * 60), wantSavings: 80,
		},
		{
			name: "deadline at risk starts at once",
			in: Input{
				Now: at(30), Deadline: at(60), Duration: time.Hour,
				Signals: []Signal{{Name: priceName, Weight: 1, Series: price}},
			},
			wantStart: at(30), wantLatest: at(0), wantAtRisk: true,
		},
		{
			name: "latest equal to earliest is at risk",
			in: Input{
				Now: at(0), Deadline: at(60), Duration: time.Hour,
				Signals: []Signal{{Name: priceName, Weight: 1, Series: price}},
			},
			wantStart: at(0), wantLatest: at(0), wantAtRisk: true,
		},
		{
			name: "higher weight wins",
			in: Input{
				Now: at(0), Deadline: at(8 * 60), Duration: time.Hour,
				Signals: []Signal{
					{Name: priceName, Weight: 30, Series: price},
					{Name: "carbon", Weight: 70, Series: carbon},
				},
			},
			wantStart: at(0), wantLatest: at(7 * 60), wantSavings: 0,
		},
		{
			name: "never worse than now",
			in: Input{
				Now: at(0), Deadline: at(8 * 60), Duration: time.Hour,
				Signals: []Signal{
					{Name: priceName, Weight: 70, Series: price},
					{Name: "carbon", Weight: 30, Series: carbon},
				},
			},
			// At 03:00 price saves 80 % but carbon costs 900 %: (70*80 - 30*900) / 100 < 0.
			wantStart: at(0), wantLatest: at(7 * 60), wantSavings: 0,
		},
		{
			name: "weights trade relative savings: carbon first",
			in: Input{
				Now: at(0), Deadline: at(8 * 60), Duration: time.Hour,
				Signals: []Signal{
					{Name: priceName, Weight: 30, Series: price},
					{Name: "carbon", Weight: 70, Series: carbonDip},
				},
			},
			// 02:00: carbon -50 %, price -40 %: 0.7*50 + 0.3*40 = 47. 03:00: 0.3*80 = 24.
			wantStart: at(2 * 60), wantLatest: at(7 * 60), wantSavings: 47,
		},
		{
			name: "weights trade relative savings: price first",
			in: Input{
				Now: at(0), Deadline: at(8 * 60), Duration: time.Hour,
				Signals: []Signal{
					{Name: priceName, Weight: 70, Series: price},
					{Name: "carbon", Weight: 30, Series: carbonDip},
				},
			},
			// 02:00: 0.3*50 + 0.7*40 = 43. 03:00: 0.7*80 = 56.
			wantStart: at(3 * 60), wantLatest: at(7 * 60), wantSavings: 56,
		},
		{
			name: "a signal that barely moves has little pull",
			in: Input{
				Now: at(0), Deadline: at(8 * 60), Duration: time.Hour,
				Signals: []Signal{
					{Name: "spot", Weight: 90, Series: hourly(t, "USD/h", 1.00, 1.00, 1.00, 1.00, 1.00, 1.00, 0.99, 1.00)},
					{Name: "carbon", Weight: 10, Series: hourly(t, "g", 100, 100, 100, 40, 100, 100, 100, 100)},
				},
			},
			// 03:00: 0.1*60 = 6. 06:00: 0.9*1 = 0.9. Min-max scaling would pick 06:00.
			wantStart: at(3 * 60), wantLatest: at(7 * 60), wantSavings: 6,
		},
		{
			name: "flat signal starts at the earliest time",
			in: Input{
				Now: at(10), Deadline: at(8 * 60), Duration: time.Hour,
				Signals: []Signal{{Name: "flat", Weight: 1, Series: hourly(t, "u", 5, 5, 5, 5, 5, 5, 5, 5)}},
			},
			wantStart: at(10), wantLatest: at(7 * 60), wantSavings: 0,
		},
		{
			name: "cheapest window at the latest start",
			in: Input{
				Now: at(7), Deadline: at(127), Duration: time.Hour,
				Signals: []Signal{{Name: "rising", Weight: 1, Series: hourly(t, "u", 90, 80, 1)}},
			},
			wantStart: at(67), wantLatest: at(67), wantSavings: (88.8333333333 - (80*53+1*7)/60.0) / 88.8333333333 * 100,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := Plan(tt.in)
			if err != nil {
				t.Fatal(err)
			}
			if !got.Start.Equal(tt.wantStart) {
				t.Errorf("Start = %v, want %v", got.Start, tt.wantStart)
			}
			if !got.Latest.Equal(tt.wantLatest) {
				t.Errorf("Latest = %v, want %v", got.Latest, tt.wantLatest)
			}
			if got.DeadlineAtRisk != tt.wantAtRisk {
				t.Errorf("DeadlineAtRisk = %v, want %v", got.DeadlineAtRisk, tt.wantAtRisk)
			}
			if !near(got.SavingsPercent, tt.wantSavings) {
				t.Errorf("SavingsPercent = %v, want %v", got.SavingsPercent, tt.wantSavings)
			}
		})
	}
}

func TestPlanEstimates(t *testing.T) {
	price := hourly(t, "EUR/MWh", 50, 40, 30, 10, 30, 40, 50, 60)
	got, err := Plan(Input{
		Now: at(30), Deadline: at(8 * 60), Duration: time.Hour,
		Signals: []Signal{{Name: priceName, Weight: 1, Series: price}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Estimates) != 1 {
		t.Fatalf("Estimates = %v", got.Estimates)
	}
	e := got.Estimates[0]
	want := Estimate{Name: priceName, Unit: "EUR/MWh", Planned: 10, IfStartedNow: 45, Available: true}
	if e != want {
		t.Errorf("Estimate = %+v, want %+v", e, want)
	}
}

func TestPlanGapFill(t *testing.T) {
	// Data for the first two hours only. The neutral value is (20*60 + 40*60) / 120 = 30.
	s := series(t,
		forecast.Point{Start: at(0), End: at(60), Value: 20},
		forecast.Point{Start: at(60), End: at(90), Value: 40, Assumed: true},
		forecast.Point{Start: at(90), End: at(120), Value: 40},
	)
	got, err := Plan(Input{
		Now: at(0), Deadline: at(4 * 60), Duration: time.Hour,
		Signals: []Signal{{Name: "s", Weight: 1, Series: s}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !got.Start.Equal(at(0)) {
		t.Errorf("Start = %v, want %v", got.Start, at(0))
	}
	e := got.Estimates[0]
	if !near(e.Planned, 20) || e.AssumedPercent != 0 {
		t.Errorf("planned estimate = %+v, want 20 with 0 %% assumed", e)
	}

	// Force a start in the gap: the window [90, 150) is half known, half neutral.
	got, err = Plan(Input{
		Now: at(90), Deadline: at(150), Duration: time.Hour,
		Signals: []Signal{{Name: "s", Weight: 1, Series: s}},
	})
	if err != nil {
		t.Fatal(err)
	}
	e = got.Estimates[0]
	// The neutral value over [90, 150) is 40.
	if !near(e.Planned, 40) || !near(e.AssumedPercent, 50) {
		t.Errorf("gap estimate = %+v, want 40 with 50 %% assumed", e)
	}
}

func TestPlanAssumedPercent(t *testing.T) {
	s := series(t,
		forecast.Point{Start: at(0), End: at(30), Value: 20},
		forecast.Point{Start: at(30), End: at(60), Value: 20, Assumed: true},
	)
	got, err := Plan(Input{
		Now: at(0), Deadline: at(60), Duration: time.Hour,
		Signals: []Signal{{Name: "s", Weight: 1, Series: s}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if e := got.Estimates[0]; !near(e.AssumedPercent, 50) {
		t.Errorf("AssumedPercent = %v, want 50", e.AssumedPercent)
	}
}

func TestPlanSignalWithoutData(t *testing.T) {
	price := hourly(t, "EUR/MWh", 50, 40, 30, 10, 30, 40, 50, 60)
	empty := series(t)
	got, err := Plan(Input{
		Now: at(0), Deadline: at(8 * 60), Duration: time.Hour,
		Signals: []Signal{
			{Name: priceName, Weight: 1, Series: price},
			{Name: "empty", Weight: 100, Series: empty},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !got.Start.Equal(at(3 * 60)) {
		t.Errorf("Start = %v, want %v", got.Start, at(3*60))
	}
	if got.Estimates[1].Available {
		t.Errorf("empty signal is available: %+v", got.Estimates[1])
	}
	// The savings use only the available signal.
	if !near(got.SavingsPercent, 80) {
		t.Errorf("SavingsPercent = %v, want 80", got.SavingsPercent)
	}
}

func TestPlanNegativePrice(t *testing.T) {
	price := hourly(t, "EUR/MWh", 10, -10, 10)
	got, err := Plan(Input{
		Now: at(0), Deadline: at(3 * 60), Duration: time.Hour,
		Signals: []Signal{{Name: priceName, Weight: 1, Series: price}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !got.Start.Equal(at(60)) {
		t.Errorf("Start = %v, want %v", got.Start, at(60))
	}
	if !near(got.SavingsPercent, 200) {
		t.Errorf("SavingsPercent = %v, want 200", got.SavingsPercent)
	}
}

func TestPlanZeroNowValue(t *testing.T) {
	got, err := Plan(Input{
		Now: at(0), Deadline: at(2 * 60), Duration: time.Hour,
		Signals: []Signal{{Name: "s", Weight: 1, Series: hourly(t, "u", 0, 0)}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if got.SavingsPercent != 0 {
		t.Errorf("SavingsPercent = %v, want 0", got.SavingsPercent)
	}
}

func TestPlanInvalidInput(t *testing.T) {
	sig := []Signal{{Name: "s", Weight: 1, Series: series(t)}}
	tests := []struct {
		name string
		in   Input
	}{
		{"zero duration", Input{Now: at(0), Deadline: at(60), Signals: sig}},
		{"no signals", Input{Now: at(0), Deadline: at(60), Duration: time.Hour}},
		{"zero weight", Input{
			Now: at(0), Deadline: at(60), Duration: time.Hour,
			Signals: []Signal{{Name: "s", Series: series(t)}},
		}},
		{"negative step", Input{Now: at(0), Deadline: at(60), Duration: time.Hour, Step: -time.Minute, Signals: sig}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := Plan(tt.in); err == nil {
				t.Error("Plan() error = nil, want an error")
			}
		})
	}
}

func TestCandidates(t *testing.T) {
	tests := []struct {
		name             string
		earliest, latest time.Time
		step             time.Duration
		want             []time.Time
	}{
		{
			name: "unaligned ends", earliest: at(7), latest: at(52), step: 15 * time.Minute,
			want: []time.Time{at(7), at(15), at(30), at(45), at(52)},
		},
		{
			name: "aligned ends", earliest: at(0), latest: at(30), step: 15 * time.Minute,
			want: []time.Time{at(0), at(15), at(30)},
		},
		{
			name: "inside one step", earliest: at(1), latest: at(2), step: 15 * time.Minute,
			want: []time.Time{at(1), at(2)},
		},
		{
			name: "equal ends", earliest: at(5), latest: at(5), step: 15 * time.Minute,
			want: []time.Time{at(5)},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := candidates(tt.earliest, tt.latest, tt.step)
			if len(got) != len(tt.want) {
				t.Fatalf("candidates() = %v, want %v", got, tt.want)
			}
			for i := range got {
				if !got[i].Equal(tt.want[i]) {
					t.Errorf("candidates()[%d] = %v, want %v", i, got[i], tt.want[i])
				}
			}
		})
	}
}

func TestPlanDefaultStep(t *testing.T) {
	// With the default step of 15 minutes, a start at minute 20 is not a candidate.
	s := series(t,
		forecast.Point{Start: at(0), End: at(20), Value: 100},
		forecast.Point{Start: at(20), End: at(40), Value: 1},
		forecast.Point{Start: at(40), End: at(120), Value: 100},
	)
	got, err := Plan(Input{
		Now: at(0), Deadline: at(120), Duration: 20 * time.Minute,
		Signals: []Signal{{Name: "s", Weight: 1, Series: s}},
	})
	if err != nil {
		t.Fatal(err)
	}
	// Minute 15 has the mean (5*100 + 15*1) / 20, the lowest of all candidates.
	if !got.Start.Equal(at(15)) {
		t.Errorf("Start = %v, want %v", got.Start, at(15))
	}
}

func TestPlanSavingsWithNearZeroNow(t *testing.T) {
	// "Start now" is almost 0. Without a floor, a tiny change gives a huge saving.
	got, err := Plan(Input{
		Now: at(0), Deadline: at(3 * 60), Duration: time.Hour,
		Signals: []Signal{{Name: "s", Weight: 1, Series: hourly(t, "u", 0.001, 100, 100)}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !got.Start.Equal(at(0)) || got.SavingsPercent != 0 {
		t.Errorf("plan = %v with savings %v, want start now and 0", got.Start, got.SavingsPercent)
	}
	got, err = Plan(Input{
		Now: at(0), Deadline: at(3 * 60), Duration: time.Hour,
		Signals: []Signal{{Name: "s", Weight: 1, Series: hourly(t, "u", 0.001, -50, 100)}},
	})
	if err != nil {
		t.Fatal(err)
	}
	// Without a floor the saving is 5000100 %. The floor keeps it readable.
	if !got.Start.Equal(at(60)) || got.SavingsPercent > 5000 {
		t.Errorf("plan = %v with savings %v, want 01:00 and a bounded saving", got.Start, got.SavingsPercent)
	}
}
