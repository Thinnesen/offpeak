//go:build live

package electricitymaps

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/Thinnesen/offpeak/internal/signal"
)

// TestLive calls the real API. Run: ELECTRICITYMAPS_TOKEN=... make test-live
func TestLive(t *testing.T) {
	token := os.Getenv("ELECTRICITYMAPS_TOKEN")
	if token == "" {
		t.Skip("ELECTRICITYMAPS_TOKEN is not set")
	}
	src, err := New(Config{Zone: "SE-SE3"}, func(context.Context) (string, error) { return token, nil },
		signal.NewHTTPGetter())
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	got, err := src.Fetch(context.Background(), signal.Query{From: now, To: now.Add(24 * time.Hour)})
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("%d points", len(got.Points))
	if len(got.Points) == 0 {
		t.Error("no forecast for the next 24 hours")
	}
}
