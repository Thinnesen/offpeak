//go:build live

package energidataservice

import (
	"context"
	"testing"
	"time"

	"github.com/Thinnesen/offpeak/internal/signal"
)

// TestLive calls the real API. Run it with "make test-live".
func TestLive(t *testing.T) {
	for i, series := range []string{DayAheadPrice, CO2Prognosis} {
		t.Run(series, func(t *testing.T) {
			if i > 0 {
				time.Sleep(5 * time.Second) // The API has a strict rate limit.
			}
			src, err := New(Config{Area: "DK1", Series: series}, signal.NewHTTPGetter())
			if err != nil {
				t.Fatal(err)
			}
			now := time.Now()
			got, err := src.Fetch(context.Background(), signal.Query{From: now, To: now.Add(24 * time.Hour)})
			if err != nil {
				t.Fatal(err)
			}
			var real int
			for _, p := range got.Points {
				if !p.Assumed {
					real++
				}
			}
			t.Logf("%s: %d points, %d real", series, len(got.Points), real)
			if real == 0 {
				t.Errorf("no real data for the next 24 hours")
			}
			if w := got.Window(now, now.Add(24*time.Hour)); w.Covered < 20*time.Hour {
				t.Errorf("series covers only %v of the next 24 hours", w.Covered)
			}
		})
	}
}
