//go:build live

package ukcarbon

import (
	"context"
	"testing"
	"time"

	"github.com/Thinnesen/offpeak/internal/signal"
)

// TestLive calls the real API. It needs no key.
func TestLive(t *testing.T) {
	for _, id := range []int32{0, 13} {
		src, err := New(Config{RegionID: id}, signal.NewHTTPGetter())
		if err != nil {
			t.Fatal(err)
		}
		now := time.Now()
		got, err := src.Fetch(context.Background(), signal.Query{From: now, To: now.Add(24 * time.Hour)})
		if err != nil {
			t.Fatal(err)
		}
		t.Logf("region %d: %d points", id, len(got.Points))
		if len(got.Points) < 40 {
			t.Errorf("region %d: %d points for 24 hours, want at least 40", id, len(got.Points))
		}
	}
}
