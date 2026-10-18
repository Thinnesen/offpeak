package ukcarbon

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Thinnesen/offpeak/internal/signal"
)

var start = time.Date(2026, 10, 18, 12, 0, 0, 0, time.UTC)

func fixtureServer(t *testing.T, file, wantPath string) *httptest.Server {
	t.Helper()
	body, err := os.ReadFile(filepath.Join("testdata", file))
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != wantPath {
			t.Errorf("path = %q, want %q", r.URL.Path, wantPath)
		}
		_, _ = w.Write(body)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestNew(t *testing.T) {
	for _, tt := range []struct {
		id      int32
		wantErr bool
	}{{0, false}, {13, false}, {17, false}, {18, true}, {-1, true}} {
		_, err := New(Config{RegionID: tt.id}, signal.NewHTTPGetter())
		if (err != nil) != tt.wantErr {
			t.Errorf("New(%d) error = %v, wantErr %v", tt.id, err, tt.wantErr)
		}
	}
	if _, err := New(Config{BaseURL: "://"}, signal.NewHTTPGetter()); err == nil {
		t.Error("New() with a bad base URL: error = nil")
	}
}

func TestFetch(t *testing.T) {
	tests := []struct {
		name, file, path string
		id               int32
		first            float64
	}{
		{name: "national", file: "national_fw48h.json", path: "/intensity/2026-10-18T12:00Z/fw48h", first: 60},
		{
			name: "London", file: "regional_13_fw48h.json", id: 13,
			path: "/regional/intensity/2026-10-18T12:00Z/fw48h/regionid/13", first: 86,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv := fixtureServer(t, tt.file, tt.path)
			src, err := New(Config{RegionID: tt.id, BaseURL: srv.URL}, signal.NewHTTPGetter())
			if err != nil {
				t.Fatal(err)
			}
			got, err := src.Fetch(context.Background(), signal.Query{From: start.Add(10 * time.Minute), To: start.Add(24 * time.Hour)})
			if err != nil {
				t.Fatal(err)
			}
			if got.Unit != "gCO2eq/kWh" || len(got.Points) != 48 {
				t.Fatalf("got unit %q and %d points, want gCO2eq/kWh and 48", got.Unit, len(got.Points))
			}
			p := got.Points[0]
			if !p.Start.Equal(start) || p.End.Sub(p.Start) != 30*time.Minute || p.Assumed {
				t.Errorf("first point = %+v", p)
			}
			// The fixture starts with the half hour before the start.
			all, err := parse(mustRead(t, tt.file))
			if err != nil {
				t.Fatal(err)
			}
			if all.Points[0].Value != tt.first {
				t.Errorf("first fixture value = %v, want %v", all.Points[0].Value, tt.first)
			}
		})
	}
}

func mustRead(t *testing.T, file string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", file))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestParseErrors(t *testing.T) {
	tests := []struct{ name, body, want string }{
		{"bad JSON", `{"data":`, "decode"},
		{"no data", `{"data":[]}`, "no forecast"},
		{"bad time", `{"data":[{"from":"soon","to":"2026-10-18T12:00Z","intensity":{"forecast":1}}]}`, "time"},
		{"null forecast", `{"data":[{"from":"2026-10-18T11:30Z","to":"2026-10-18T12:00Z","intensity":{"forecast":null}}]}`,
			"no forecast"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := parse([]byte(tt.body)); err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Errorf("parse() error = %v, want it to contain %q", err, tt.want)
			}
		})
	}
}

func TestFetchHTTPError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
	}))
	defer srv.Close()
	src, err := New(Config{BaseURL: srv.URL}, signal.NewHTTPGetter())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := src.Fetch(context.Background(), signal.Query{From: start, To: start.Add(time.Hour)}); err == nil {
		t.Error("Fetch() error = nil")
	}
}
