package electricitymaps

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Thinnesen/offpeak/internal/signal"
)

const testToken = "em-7c1d"

func staticToken(context.Context) (string, error) { return testToken, nil }

var start = time.Date(2026, 10, 17, 14, 0, 0, 0, time.UTC)

// fixtureServer serves a file from testdata. It checks the path, the zone and the token header.
func fixtureServer(t *testing.T, file string) *httptest.Server {
	t.Helper()
	body, err := os.ReadFile(filepath.Join("testdata", file))
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v3/carbon-intensity/forecast" || r.URL.Query().Get("zone") != "SE-SE3" {
			t.Errorf("request = %s", r.URL)
		}
		if r.Header.Get("auth-token") != testToken {
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"status":"error","message":"Invalid authentication"}`))
			return
		}
		_, _ = w.Write(body)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func newSource(t *testing.T, srv *httptest.Server, token TokenFunc) *Source {
	t.Helper()
	s, err := New(Config{Zone: "SE-SE3", BaseURL: srv.URL + "/v3"}, token, signal.NewHTTPGetter())
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestNew(t *testing.T) {
	tests := []struct {
		zone, base string
		wantErr    bool
	}{
		{zone: "SE-SE3"},
		{zone: "US-CAL-CISO"},
		{zone: "DE"},
		{zone: "se-se3", wantErr: true},
		{zone: "SE&zone=DE", wantErr: true},
		{zone: "DE", base: "http://api.example.com", wantErr: true},
		{zone: "DE", base: "https://api.example.com/v4"},
	}
	for _, tt := range tests {
		_, err := New(Config{Zone: tt.zone, BaseURL: tt.base}, staticToken, signal.NewHTTPGetter())
		if (err != nil) != tt.wantErr {
			t.Errorf("New(%q, %q) error = %v, wantErr %v", tt.zone, tt.base, err, tt.wantErr)
		}
	}
}

func TestNewAllowsPlainHTTPForLoopback(t *testing.T) {
	if _, err := New(Config{Zone: "DE", BaseURL: "http://127.0.0.1:8080/v3"}, staticToken, nil); err != nil {
		t.Errorf("New() error = %v, want plain HTTP to loopback for tests", err)
	}
}

func TestFetch(t *testing.T) {
	tests := []struct {
		file string
		want []float64
	}{
		{file: "forecast_v3.json", want: []float64{38, 35, 33, 30}},
		{file: "forecast_data.json", want: []float64{38, 35, 33, 30}},
	}
	for _, tt := range tests {
		t.Run(tt.file, func(t *testing.T) {
			src := newSource(t, fixtureServer(t, tt.file), staticToken)
			got, err := src.Fetch(context.Background(), signal.Query{
				From: start.Add(90 * time.Minute), To: start.Add(4*time.Hour + 30*time.Minute),
			})
			if err != nil {
				t.Fatal(err)
			}
			if got.Unit != "gCO2eq/kWh" {
				t.Errorf("Unit = %q", got.Unit)
			}
			if len(got.Points) != len(tt.want) {
				t.Fatalf("points = %+v, want %v", got.Points, tt.want)
			}
			for i, w := range tt.want {
				p := got.Points[i]
				if p.Value != w || p.Assumed || p.End.Sub(p.Start) != time.Hour {
					t.Errorf("point %d = %+v, want a real hourly value %v", i, p, w)
				}
			}
		})
	}
}

func TestFetchBeyondForecast(t *testing.T) {
	src := newSource(t, fixtureServer(t, "forecast_data.json"), staticToken)
	got, err := src.Fetch(context.Background(), signal.Query{From: start.Add(5 * time.Hour), To: start.Add(48 * time.Hour)})
	if err != nil {
		t.Fatal(err)
	}
	// No history and no profile: after the forecast, the series has a gap for the neutral fill.
	if len(got.Points) != 1 {
		t.Errorf("points = %+v, want only the last forecast hour", got.Points)
	}
}

func TestFetchErrors(t *testing.T) {
	tests := []struct {
		name  string
		body  string
		token TokenFunc
		want  string
	}{
		{name: "unauthorized", token: func(context.Context) (string, error) { return "wrong", nil }, want: "401"},
		{name: "token error", token: func(context.Context) (string, error) { return "", errors.New("no Secret") }, want: "token"},
		{name: "bad JSON", body: `{"forecast":[`, token: staticToken, want: "decode"},
		{name: "bad time", body: `{"data":[{"datetime":"soon","carbonIntensity":1}]}`, token: staticToken, want: "time"},
		{name: "empty", body: `{"zone":"SE-SE3"}`, token: staticToken, want: "no forecast"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Header.Get("auth-token") != testToken {
					w.WriteHeader(http.StatusUnauthorized)
					return
				}
				_, _ = w.Write([]byte(tt.body))
			}))
			defer srv.Close()
			src, err := New(Config{Zone: "SE-SE3", BaseURL: srv.URL}, tt.token, signal.NewHTTPGetter())
			if err != nil {
				t.Fatal(err)
			}
			_, err = src.Fetch(context.Background(), signal.Query{From: start, To: start.Add(time.Hour)})
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("Fetch() error = %v, want it to contain %q", err, tt.want)
			}
			if strings.Contains(err.Error(), testToken) {
				t.Errorf("error %q leaks the token", err)
			}
		})
	}
}

func TestPointDurations(t *testing.T) {
	src := newSource(t, fixtureServer(t, "forecast_v3.json"), staticToken)
	body := []byte(`{"data":[
		{"datetime":"2026-10-17T14:00:00Z","carbonIntensity":1},
		{"datetime":"2026-10-17T14:15:00Z","carbonIntensity":2},
		{"datetime":"2026-10-17T14:30:00Z","carbonIntensity":3}]}`)
	got, err := src.parse(body)
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range got.Points {
		if p.End.Sub(p.Start) != 15*time.Minute {
			t.Errorf("point %+v does not last 15 minutes", p)
		}
	}
}
