package energidataservice

import (
	"context"
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

// fixtureServer serves a file from testdata and records the last request.
func fixtureServer(t *testing.T, file string, status int) (*httptest.Server, *url.URL) {
	t.Helper()
	body, err := os.ReadFile(filepath.Join("testdata", file))
	if err != nil {
		t.Fatal(err)
	}
	last := &url.URL{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		*last = *r.URL
		w.WriteHeader(status)
		_, _ = w.Write(body)
	}))
	t.Cleanup(srv.Close)
	return srv, last
}

func newSource(t *testing.T, cfg Config) *Source {
	t.Helper()
	s, err := New(cfg, signal.NewHTTPGetter())
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestNew(t *testing.T) {
	tests := []struct {
		name    string
		cfg     Config
		wantErr bool
	}{
		{name: "DK1 price", cfg: Config{Area: "DK1", Series: DayAheadPrice}},
		{name: "DK2 carbon", cfg: Config{Area: "DK2", Series: CO2Prognosis}},
		{name: "bad area", cfg: Config{Area: "DK1&limit=5", Series: CO2Prognosis}, wantErr: true},
		{name: "bad series", cfg: Config{Area: "DK1", Series: "Wind"}, wantErr: true},
		{name: "bad base URL", cfg: Config{Area: "DK1", Series: CO2Prognosis, BaseURL: "://x"}, wantErr: true},
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

func TestFetchDayAheadPrice(t *testing.T) {
	srv, last := fixtureServer(t, "dayaheadprices.json", http.StatusOK)
	src := newSource(t, Config{Area: "DK1", Series: DayAheadPrice, BaseURL: srv.URL})

	from := time.Date(2026, 10, 17, 0, 20, 0, 0, time.UTC)
	got, err := src.Fetch(context.Background(), signal.Query{From: from, To: from.Add(2 * time.Hour)})
	if err != nil {
		t.Fatal(err)
	}

	if last.Path != "/dataset/DayAheadPrices" {
		t.Errorf("path = %q", last.Path)
	}
	q := last.Query()
	wantQuery := map[string]string{
		"start":    "2026-10-03T00:00",
		"end":      "2026-10-20T00:00",
		"timezone": "UTC",
		"filter":   `{"PriceArea":["DK1"]}`,
		"columns":  "TimeUTC,PriceArea,DayAheadPriceEUR",
		"sort":     "TimeUTC asc",
		"limit":    "0",
	}
	for k, v := range wantQuery {
		if q.Get(k) != v {
			t.Errorf("query %s = %q, want %q", k, q.Get(k), v)
		}
	}

	if got.Unit != "EUR/MWh" {
		t.Errorf("Unit = %q", got.Unit)
	}
	// The fixture has 12 quarter hours from 00:00. Points that overlap [00:20, 02:20) start at 00:15.
	if len(got.Points) != 9 {
		t.Fatalf("got %d points, want 9: %+v", len(got.Points), got.Points)
	}
	first := got.Points[0]
	if !first.Start.Equal(time.Date(2026, 10, 17, 0, 15, 0, 0, time.UTC)) ||
		first.End.Sub(first.Start) != 15*time.Minute || first.Value != 162.889999 || first.Assumed {
		t.Errorf("first point = %+v", first)
	}
}

func TestFetchCO2Prognosis(t *testing.T) {
	srv, last := fixtureServer(t, "co2emisprog.json", http.StatusOK)
	src := newSource(t, Config{Area: "DK2", Series: CO2Prognosis, BaseURL: srv.URL})

	from := time.Date(2026, 10, 17, 0, 0, 0, 0, time.UTC)
	got, err := src.Fetch(context.Background(), signal.Query{From: from, To: from.Add(time.Hour)})
	if err != nil {
		t.Fatal(err)
	}
	if last.Path != "/dataset/CO2EmisProg" || last.Query().Get("filter") != `{"PriceArea":["DK2"]}` {
		t.Errorf("request = %v", last)
	}
	if got.Unit != "gCO2eq/kWh" || len(got.Points) != 12 {
		t.Fatalf("got %+v", got)
	}
	if d := got.Points[0].End.Sub(got.Points[0].Start); d != 5*time.Minute {
		t.Errorf("point duration = %v, want 5m", d)
	}
}

func TestFetchFillsFromProfile(t *testing.T) {
	srv, _ := fixtureServer(t, "dayaheadprices.json", http.StatusOK)
	src := newSource(t, Config{Area: "DK1", Series: DayAheadPrice, BaseURL: srv.URL})

	// One week after the fixture data, only the profile is available.
	from := time.Date(2026, 10, 24, 0, 0, 0, 0, time.UTC)
	got, err := src.Fetch(context.Background(), signal.Query{From: from, To: from.Add(4 * time.Hour)})
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Points) != 3 {
		t.Fatalf("got %d points, want 3 hourly profile points: %+v", len(got.Points), got.Points)
	}
	for _, p := range got.Points {
		if !p.Assumed || p.End.Sub(p.Start) != time.Hour {
			t.Errorf("point %+v is not an assumed hourly point", p)
		}
	}
}

func TestFetchErrors(t *testing.T) {
	tests := []struct {
		name, body string
		status     int
		want       string
	}{
		{name: "rate limit", body: `{"error":"too many requests"}`, status: http.StatusTooManyRequests, want: "429"},
		{name: "bad JSON", body: `{"records":[`, status: http.StatusOK, want: "decode"},
		{name: "bad time", body: `{"records":[{"TimeUTC":"yesterday","DayAheadPriceEUR":1}]}`, status: http.StatusOK, want: "time"},
		{
			name:   "duplicate time",
			body:   `{"records":[{"TimeUTC":"2026-10-17T00:00:00","DayAheadPriceEUR":1},{"TimeUTC":"2026-10-17T00:00:00","DayAheadPriceEUR":2}]}`,
			status: http.StatusOK, want: "overlap",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(tt.status)
				_, _ = w.Write([]byte(tt.body))
			}))
			defer srv.Close()
			src := newSource(t, Config{Area: "DK1", Series: DayAheadPrice, BaseURL: srv.URL})
			now := time.Date(2026, 10, 17, 0, 0, 0, 0, time.UTC)
			_, err := src.Fetch(context.Background(), signal.Query{From: now, To: now.Add(time.Hour)})
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Errorf("Fetch() error = %v, want it to contain %q", err, tt.want)
			}
		})
	}
}

func TestFetchSkipsNullValues(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"records":[
			{"TimeUTC":"2026-10-17T00:00:00","DayAheadPriceEUR":null},
			{"TimeUTC":"2026-10-17T00:15:00","DayAheadPriceEUR":3.5}]}`))
	}))
	defer srv.Close()
	src := newSource(t, Config{Area: "DK1", Series: DayAheadPrice, BaseURL: srv.URL})
	now := time.Date(2026, 10, 17, 0, 0, 0, 0, time.UTC)
	got, err := src.Fetch(context.Background(), signal.Query{From: now, To: now.Add(30 * time.Minute)})
	if err != nil {
		t.Fatal(err)
	}
	// The null slot gets the profile value of its hour, which is 3.5.
	if len(got.Points) != 2 || !got.Points[0].Assumed || got.Points[1].Assumed || got.Points[1].Value != 3.5 {
		t.Errorf("Points = %+v, want an assumed point and the real point", got.Points)
	}
}
