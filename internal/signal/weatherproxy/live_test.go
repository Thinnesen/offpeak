//go:build live

package weatherproxy

import (
	"context"
	"testing"
	"time"

	"github.com/Thinnesen/offpeak/internal/signal"
)

// TestLive calls the public Open-Meteo API. It needs no key.
func TestLive(t *testing.T) {
	for _, region := range []string{"aws:us-east-1", "gcp:asia-south1", "azure:australiaeast"} {
		src, err := New(Config{CloudRegion: region}, signal.NewHTTPGetter())
		if err != nil {
			t.Fatal(err)
		}
		now := time.Now()
		got, err := src.Fetch(context.Background(), signal.Query{From: now, To: now.Add(48 * time.Hour)})
		if err != nil {
			t.Fatal(err)
		}
		t.Logf("%s: %d points", region, len(got.Points))
		if len(got.Points) < 40 {
			t.Errorf("%s: %d points for the next 48 hours, want at least 40", region, len(got.Points))
		}
	}
}
