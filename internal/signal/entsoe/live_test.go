//go:build live

package entsoe

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/Thinnesen/offpeak/internal/signal"
)

// TestLive calls the real API. Run: ENTSOE_TOKEN=... make test-live
func TestLive(t *testing.T) {
	token := os.Getenv("ENTSOE_TOKEN")
	if token == "" {
		t.Skip("ENTSOE_TOKEN is not set")
	}
	src, err := New(Config{BiddingZone: "10Y1001A1001A82H"},
		func(context.Context) (string, error) { return token, nil }, signal.NewHTTPGetter())
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
	t.Logf("%d points, %d real", len(got.Points), real)
	if real == 0 {
		t.Error("no real data for the next 24 hours")
	}
}
