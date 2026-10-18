//go:build live

package energycharts

import (
	"context"
	"testing"
	"time"

	"github.com/Thinnesen/offpeak/internal/signal"
)

// TestLive calls the real API. It needs no key.
func TestLive(t *testing.T) {
	for _, c := range []string{"de", "fr", "se"} {
		src, err := New(Config{Country: c}, signal.NewHTTPGetter())
		if err != nil {
			t.Fatal(err)
		}
		now := time.Now()
		got, err := src.Fetch(context.Background(), signal.Query{From: now, To: now.Add(time.Hour)})
		if err != nil {
			t.Fatal(err)
		}
		t.Logf("%s: %d points", c, len(got.Points))
		if len(got.Points) == 0 {
			t.Errorf("%s: no forecast for the next hour", c)
		}
	}
}
