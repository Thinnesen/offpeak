package energycharts

import (
	"context"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Thinnesen/offpeak/internal/signal"
)

func TestNew(t *testing.T) {
	for _, tt := range []struct {
		country string
		wantErr bool
	}{{"de", false}, {"se", false}, {"ie", true}, {"DE", true}, {"de&x=1", true}} {
		_, err := New(Config{Country: tt.country}, signal.NewHTTPGetter())
		if (err != nil) != tt.wantErr {
			t.Errorf("New(%q) error = %v, wantErr %v", tt.country, err, tt.wantErr)
		}
	}
	if _, err := New(Config{Country: "de", BaseURL: "://"}, signal.NewHTTPGetter()); err == nil {
		t.Error("New() with a bad base URL: error = nil")
	}
}

func TestFetch(t *testing.T) {
	body, err := os.ReadFile(filepath.Join("testdata", "signal_de.json"))
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/signal" || r.URL.Query().Get("country") != "de" {
			t.Errorf("request = %s", r.URL)
		}
		_, _ = w.Write(body)
	}))
	defer srv.Close()
	src, err := New(Config{Country: "de", BaseURL: srv.URL}, signal.NewHTTPGetter())
	if err != nil {
		t.Fatal(err)
	}
	// The fixture starts at 2026-10-17 22:00 UTC (00:00 in Germany) with a renewable share of 68.6 %.
	from := time.Date(2026, 10, 17, 22, 0, 0, 0, time.UTC)
	got, err := src.Fetch(context.Background(), signal.Query{From: from, To: from.Add(2 * time.Hour)})
	if err != nil {
		t.Fatal(err)
	}
	if got.Unit != Unit || len(got.Points) != 8 {
		t.Fatalf("got unit %q and %d points, want %q and 8", got.Unit, len(got.Points), Unit)
	}
	p := got.Points[0]
	if !p.Start.Equal(from) || p.End.Sub(p.Start) != 15*time.Minute || math.Abs(p.Value-31.4) > 1e-9 {
		t.Errorf("first point = %+v, want 31.4 %% not renewable", p)
	}
}

func TestParse(t *testing.T) {
	got, err := parse([]byte(`{"unix_seconds":[0,3600,7200],"share":[50,null,80]}`))
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Points) != 2 || got.Points[1].Value != 20 || got.Points[1].End.Sub(got.Points[1].Start) != time.Hour {
		t.Errorf("points = %+v", got.Points)
	}
	for _, tt := range []struct{ name, body, want string }{
		{"bad JSON", `{"share":`, "decode"},
		{"empty", `{"unix_seconds":[],"share":[]}`, "no forecast"},
		{"short arrays", `{"unix_seconds":[0],"share":[]}`, "length"},
	} {
		if _, err := parse([]byte(tt.body)); err == nil || !strings.Contains(err.Error(), tt.want) {
			t.Errorf("%s: parse() error = %v, want it to contain %q", tt.name, err, tt.want)
		}
	}
}

func TestFetchHTTPError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()
	src, err := New(Config{Country: "de", BaseURL: srv.URL}, signal.NewHTTPGetter())
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	if _, err := src.Fetch(context.Background(), signal.Query{From: now, To: now.Add(time.Hour)}); err == nil {
		t.Error("Fetch() error = nil")
	}
}
