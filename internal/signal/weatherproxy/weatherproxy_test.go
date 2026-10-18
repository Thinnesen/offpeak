package weatherproxy

import (
	"context"
	"math"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Thinnesen/offpeak/internal/signal"
)

func near(a, b float64) bool { return math.Abs(a-b) < 1e-9 }

func TestCleanIndex(t *testing.T) {
	tests := []struct {
		name           string
		radiation, kmh float64
		want           float64
	}{
		{name: "night without wind", radiation: 0, kmh: 5, want: 100},
		{name: "full sun, no wind", radiation: 800, kmh: 0, want: 50},
		{name: "more than full sun", radiation: 1000, kmh: 0, want: 50},
		{name: "rated wind at night", radiation: 0, kmh: 45, want: 50},
		{name: "storm above rated wind", radiation: 0, kmh: 90, want: 50},
		{name: "sun and wind", radiation: 800, kmh: 45, want: 0},
		{name: "half sun", radiation: 400, kmh: 0, want: 75},
		{
			name: "wind between cut-in and rated", radiation: 0, kmh: 30,
			want: 100 - 50*(30.0*30*30-11*11*11)/(45.0*45*45-11*11*11),
		},
		{name: "negative radiation is zero", radiation: -3, kmh: 0, want: 100},
	}
	for _, tt := range tests {
		if got := cleanIndex(tt.radiation, tt.kmh); !near(got, tt.want) {
			t.Errorf("%s: cleanIndex(%v, %v) = %v, want %v", tt.name, tt.radiation, tt.kmh, got, tt.want)
		}
	}
}

func TestNew(t *testing.T) {
	tests := []struct {
		name    string
		cfg     Config
		wantErr bool
	}{
		{name: "cloud region", cfg: Config{CloudRegion: "aws:eu-north-1"}},
		{name: "coordinate", cfg: Config{Latitude: "-33.87", Longitude: "151.21"}},
		{name: "unknown region", cfg: Config{CloudRegion: "aws:mars-1"}, wantErr: true},
		{name: "both", cfg: Config{CloudRegion: "aws:eu-north-1", Latitude: "1", Longitude: "2"}, wantErr: true},
		{name: "neither", cfg: Config{}, wantErr: true},
		{name: "latitude out of range", cfg: Config{Latitude: "91", Longitude: "0"}, wantErr: true},
		{name: "longitude not a number", cfg: Config{Latitude: "0", Longitude: "east"}, wantErr: true},
		{name: "bad base URL", cfg: Config{CloudRegion: "aws:eu-north-1", BaseURL: "://"}, wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := New(tt.cfg, signal.NewHTTPGetter())
			if (err != nil) != tt.wantErr {
				t.Errorf("New() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestFetch(t *testing.T) {
	body, err := os.ReadFile(filepath.Join("testdata", "forecast_stockholm.json"))
	if err != nil {
		t.Fatal(err)
	}
	var query url.Values
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/forecast" {
			t.Errorf("path = %q", r.URL.Path)
		}
		query = r.URL.Query()
		_, _ = w.Write(body)
	}))
	defer srv.Close()

	src, err := New(Config{CloudRegion: "aws:eu-north-1", BaseURL: srv.URL}, signal.NewHTTPGetter())
	if err != nil {
		t.Fatal(err)
	}
	from := time.Date(2026, 10, 18, 10, 30, 0, 0, time.UTC)
	got, err := src.Fetch(context.Background(), signal.Query{From: from, To: from.Add(24 * time.Hour)})
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{
		"latitude": "59.33", "longitude": "18.07", "timezone": "UTC", "forecast_days": "16",
		"hourly": "shortwave_radiation,wind_speed_100m",
	}
	for k, v := range want {
		if query.Get(k) != v {
			t.Errorf("query %s = %q, want %q", k, query.Get(k), v)
		}
	}
	if got.Unit != Unit {
		t.Errorf("Unit = %q", got.Unit)
	}
	// Hourly points that overlap [10:30, 10:30+24h): 25 points.
	if len(got.Points) != 25 {
		t.Fatalf("got %d points, want 25", len(got.Points))
	}
	// The value for [10:00, 11:00) comes from the 11:00 record: radiation is the mean of the preceding hour.
	p := got.Points[0]
	if !p.Start.Equal(time.Date(2026, 10, 18, 10, 0, 0, 0, time.UTC)) || p.Assumed {
		t.Errorf("first point = %+v", p)
	}
	for _, p := range got.Points {
		if p.Value < 0 || p.Value > 100 {
			t.Errorf("index %v is outside [0, 100]", p.Value)
		}
	}
}

func TestFetchSkipsNullHours(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"hourly":{"time":["2026-10-18T01:00","2026-10-18T02:00"],
			"shortwave_radiation":[0,null],"wind_speed_100m":[10,20]}}`))
	}))
	defer srv.Close()
	src, err := New(Config{Latitude: "0", Longitude: "0", BaseURL: srv.URL}, signal.NewHTTPGetter())
	if err != nil {
		t.Fatal(err)
	}
	from := time.Date(2026, 10, 18, 0, 0, 0, 0, time.UTC)
	got, err := src.Fetch(context.Background(), signal.Query{From: from, To: from.Add(3 * time.Hour)})
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Points) != 1 {
		t.Errorf("points = %+v, want only the hour with both values", got.Points)
	}
}

func TestFetchErrors(t *testing.T) {
	tests := []struct {
		name, body string
		status     int
		want       string
	}{
		{name: "API error", body: `{"error":true,"reason":"Latitude must be in range"}`, status: 400, want: "400"},
		{name: "bad JSON", body: `{"hourly":`, status: 200, want: "decode"},
		{name: "bad time", body: `{"hourly":{"time":["soon"],"shortwave_radiation":[1],"wind_speed_100m":[1]}}`,
			status: 200, want: "time"},
		{name: "short arrays", body: `{"hourly":{"time":["2026-10-18T01:00"],"shortwave_radiation":[],"wind_speed_100m":[1]}}`,
			status: 200, want: "length"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(tt.status)
				_, _ = w.Write([]byte(tt.body))
			}))
			defer srv.Close()
			src, err := New(Config{Latitude: "0", Longitude: "0", BaseURL: srv.URL}, signal.NewHTTPGetter())
			if err != nil {
				t.Fatal(err)
			}
			now := time.Date(2026, 10, 18, 0, 0, 0, 0, time.UTC)
			_, err = src.Fetch(context.Background(), signal.Query{From: now, To: now.Add(time.Hour)})
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Errorf("Fetch() error = %v, want it to contain %q", err, tt.want)
			}
		})
	}
}
