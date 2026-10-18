package entsoe

import (
	"context"
	"errors"
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

const testToken = "tok-3f9a"

func staticToken(context.Context) (string, error) { return testToken, nil }

func near(a, b float64) bool { return math.Abs(a-b) < 1e-9 }

// fixtureServer serves a file from testdata with the given status and records the last query.
func fixtureServer(t *testing.T, file string, status int) (*httptest.Server, *url.Values) {
	t.Helper()
	body, err := os.ReadFile(filepath.Join("testdata", file))
	if err != nil {
		t.Fatal(err)
	}
	last := &url.Values{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		*last = r.URL.Query()
		w.Header().Set("Content-Type", "text/xml")
		w.WriteHeader(status)
		_, _ = w.Write(body)
	}))
	t.Cleanup(srv.Close)
	return srv, last
}

func newSource(t *testing.T, baseURL string, token TokenFunc) *Source {
	t.Helper()
	s, err := New(Config{BiddingZone: "10Y1001A1001A82H", BaseURL: baseURL}, token, signal.NewHTTPGetter())
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestNew(t *testing.T) {
	tests := []struct {
		zone    string
		base    string
		wantErr bool
	}{
		{zone: "10Y1001A1001A82H"},
		{zone: "10YDK-1--------W"},
		{zone: "10Y1001A1001A82H&securityToken=x", wantErr: true},
		{zone: "short", wantErr: true},
		{zone: "10YDK-1--------W", base: "://", wantErr: true},
	}
	for _, tt := range tests {
		_, err := New(Config{BiddingZone: tt.zone, BaseURL: tt.base}, staticToken, signal.NewHTTPGetter())
		if (err != nil) != tt.wantErr {
			t.Errorf("New(%q) error = %v, wantErr %v", tt.zone, err, tt.wantErr)
		}
	}
}

func TestParseA03AndMixedResolutions(t *testing.T) {
	body, err := os.ReadFile(filepath.Join("testdata", "a03_mixed.xml"))
	if err != nil {
		t.Fatal(err)
	}
	s, err := parse(body)
	if err != nil {
		t.Fatal(err)
	}
	if s.Unit != "EUR/MWh" {
		t.Errorf("Unit = %q", s.Unit)
	}
	at := func(hhmm string) time.Time {
		t.Helper()
		tm, err := time.Parse("2006-01-02T15:04Z", "2026-10-16T"+hhmm+"Z")
		if err != nil {
			t.Fatal(err)
		}
		return tm
	}
	if w := s.Window(at("22:00"), at("23:59")); w.Covered != 119*time.Minute {
		t.Errorf("covered = %v, want the full window: the PT60M series must not overlap the PT15M series", w.Covered)
	}
	tests := []struct {
		from, to string
		want     float64
	}{
		{"22:00", "22:15", 95.12},
		{"22:30", "22:45", 91.40}, // position 3 repeats position 2
		{"22:45", "23:00", 88.03},
		{"23:15", "23:45", -1.50}, // positions 6 and 7 repeat position 5
		{"23:45", "23:59", 80.00},
	}
	for _, tt := range tests {
		got, ok := s.Window(at(tt.from), at(tt.to)).Mean()
		if !ok || !near(got, tt.want) {
			t.Errorf("mean %s-%s = %v, %v, want %v", tt.from, tt.to, got, ok, tt.want)
		}
	}
}

func TestFetch(t *testing.T) {
	srv, last := fixtureServer(t, "a01_hourly.xml", http.StatusOK)
	src := newSource(t, srv.URL, staticToken)

	// One week after the first fixture day: only profile values exist.
	from := time.Date(2026, 10, 15, 22, 20, 0, 0, time.UTC)
	got, err := src.Fetch(context.Background(), signal.Query{From: from, To: from.Add(100 * time.Minute)})
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{
		"documentType":  "A44",
		"in_Domain":     "10Y1001A1001A82H",
		"out_Domain":    "10Y1001A1001A82H",
		"periodStart":   "202610012200",
		"periodEnd":     "202610182200",
		"securityToken": testToken,
	}
	for k, v := range want {
		if last.Get(k) != v {
			t.Errorf("query %s = %q, want %q", k, last.Get(k), v)
		}
	}
	// Thursday 22:00 has 52 (position 1 of day 0), Thursday 23:00 has 54.
	if len(got.Points) != 2 || !near(got.Points[0].Value, 52) || !near(got.Points[1].Value, 54) {
		t.Fatalf("points = %+v, want the profile values 52 and 54", got.Points)
	}
	for _, p := range got.Points {
		if !p.Assumed {
			t.Errorf("point %+v is not assumed", p)
		}
	}
}

func TestFetchErrors(t *testing.T) {
	tests := []struct {
		name   string
		file   string
		status int
		token  TokenFunc
		want   string
	}{
		{name: "acknowledgement", file: "acknowledgement.xml", status: http.StatusOK, token: staticToken, want: "No matching data"},
		{name: "unauthorized", file: "acknowledgement.xml", status: http.StatusUnauthorized, token: staticToken, want: "401"},
		{
			name: "token error", file: "a01_hourly.xml", status: http.StatusOK, want: "token",
			token: func(context.Context) (string, error) { return "", errors.New("Secret entsoe not found") },
		},
		{name: "not XML", file: "README.md", status: http.StatusOK, token: staticToken, want: "decode"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv, _ := fixtureServer(t, tt.file, tt.status)
			src := newSource(t, srv.URL, tt.token)
			now := time.Date(2026, 10, 17, 0, 0, 0, 0, time.UTC)
			_, err := src.Fetch(context.Background(), signal.Query{From: now, To: now.Add(time.Hour)})
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("Fetch() error = %v, want it to contain %q", err, tt.want)
			}
			if strings.Contains(err.Error(), testToken) {
				t.Errorf("error %q leaks the token", err)
			}
		})
	}
}

func TestParseErrors(t *testing.T) {
	tests := []struct {
		name, body string
	}{
		{"unknown document", `<Other_MarketDocument/>`},
		{"bad resolution", doc("P1D", "2026-10-16T22:00Z", "2026-10-17T22:00Z", "1", "5")},
		{"bad start", doc("PT60M", "yesterday", "2026-10-17T22:00Z", "1", "5")},
		{"bad end", doc("PT60M", "2026-10-16T22:00Z", "tomorrow", "1", "5")},
		{"bad position", doc("PT60M", "2026-10-16T22:00Z", "2026-10-16T23:00Z", "7", "5")},
		{"bad price", doc("PT60M", "2026-10-16T22:00Z", "2026-10-16T23:00Z", "1", "cheap")},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := parse([]byte(tt.body)); err == nil {
				t.Error("parse() error = nil")
			}
		})
	}
}

func TestParseSameResolutionTwice(t *testing.T) {
	one := doc("PT60M", "2026-10-16T22:00Z", "2026-10-16T23:00Z", "1", "5")
	two := strings.Replace(one, "<price.amount>5<", "<price.amount>7<", 1)
	body := strings.Replace(one, "</Publication_MarketDocument>", "", 1) +
		two[strings.Index(two, "<TimeSeries>"):]
	s, err := parse([]byte(body))
	if err != nil {
		t.Fatal(err)
	}
	if len(s.Points) != 1 || s.Points[0].Value != 5 {
		t.Errorf("points = %+v, want only the first series", s.Points)
	}
}

func doc(resolution, start, end, position, price string) string {
	return `<Publication_MarketDocument><TimeSeries><curveType>A01</curveType><Period>` +
		`<timeInterval><start>` + start + `</start><end>` + end + `</end></timeInterval>` +
		`<resolution>` + resolution + `</resolution>` +
		`<Point><position>` + position + `</position><price.amount>` + price + `</price.amount></Point>` +
		`</Period></TimeSeries></Publication_MarketDocument>`
}
