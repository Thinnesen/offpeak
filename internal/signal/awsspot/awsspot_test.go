package awsspot

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

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	clocktesting "k8s.io/utils/clock/testing"

	"github.com/Thinnesen/offpeak/internal/signal"
)

// now is a Saturday, shortly after the end of the fixture data.
var now = time.Date(2026, 10, 17, 0, 0, 0, 0, time.UTC)

var testCreds = credentials.NewStaticCredentialsProvider("AKIDEXAMPLE", "secret", "")

func near(a, b float64) bool { return math.Abs(a-b) < 1e-9 }

func TestNew(t *testing.T) {
	tests := []struct {
		name         string
		cfg          Config
		wantEndpoint string
		wantErr      bool
	}{
		{name: "valid", cfg: Config{Region: "eu-north-1"}, wantEndpoint: "https://ec2.eu-north-1.amazonaws.com"},
		{name: "China", cfg: Config{Region: "cn-north-1"}, wantEndpoint: "https://ec2.cn-north-1.amazonaws.com.cn"},
		{name: "GovCloud", cfg: Config{Region: "us-gov-west-1"}, wantEndpoint: "https://ec2.us-gov-west-1.amazonaws.com"},
		{name: "bad region", cfg: Config{Region: "eu-north-1.evil.com/x"}, wantErr: true},
		{name: "bad endpoint", cfg: Config{Region: "eu-north-1", Endpoint: "://"}, wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s, err := New(tt.cfg, testCreds, signal.NewHTTPGetter(), clocktesting.NewFakeClock(now))
			if (err != nil) != tt.wantErr {
				t.Fatalf("New() error = %v, wantErr %v", err, tt.wantErr)
			}
			if err == nil && s.endpoint.String() != tt.wantEndpoint {
				t.Errorf("endpoint = %s, want %s", s.endpoint, tt.wantEndpoint)
			}
		})
	}
}

// fixtureServer serves the two XML pages and counts the calls.
func fixtureServer(t *testing.T, calls *int) *httptest.Server {
	t.Helper()
	page1, err := os.ReadFile(filepath.Join("testdata", "page1.xml"))
	if err != nil {
		t.Fatal(err)
	}
	page2, err := os.ReadFile(filepath.Join("testdata", "page2.xml"))
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		*calls++
		q := r.URL.Query()
		want := url.Values{
			"Action": {"DescribeSpotPriceHistory"}, "Version": {"2016-11-15"},
			"InstanceType.1": {"g6.2xlarge"}, "InstanceType.2": {"g6.xlarge"},
			"ProductDescription.1": {"Linux/UNIX"},
			"StartTime":            {"2026-10-03T00:00:00Z"}, "EndTime": {"2026-10-17T00:00:00Z"},
		}
		for k := range want {
			if q.Get(k) != want.Get(k) {
				t.Errorf("query %s = %q, want %q", k, q.Get(k), want.Get(k))
			}
		}
		auth := r.Header.Get("Authorization")
		if !strings.HasPrefix(auth, "AWS4-HMAC-SHA256 Credential=AKIDEXAMPLE/20261017/eu-north-1/ec2/aws4_request") {
			t.Errorf("Authorization = %q, want a SigV4 signature for ec2", auth)
		}
		w.Header().Set("Content-Type", "text/xml")
		if q.Get("NextToken") == "page2" {
			_, _ = w.Write(page2)
			return
		}
		_, _ = w.Write(page1)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestFetch(t *testing.T) {
	var calls int
	srv := fixtureServer(t, &calls)
	src, err := New(Config{Region: "eu-north-1", Endpoint: srv.URL}, testCreds, signal.NewHTTPGetter(),
		clocktesting.NewFakeClock(now))
	if err != nil {
		t.Fatal(err)
	}
	got, err := src.Fetch(context.Background(), signal.Query{
		From: now, To: now.Add(24 * time.Hour), InstanceTypes: []string{"g6.xlarge", "g6.2xlarge"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if calls != 2 {
		t.Errorf("API calls = %d, want 2 pages", calls)
	}
	if got.Unit != "USD/h" || len(got.Points) != 24 {
		t.Fatalf("got unit %q and %d points, want USD/h and 24", got.Unit, len(got.Points))
	}
	for _, p := range got.Points {
		if !p.Assumed {
			t.Fatalf("point %+v is not assumed", p)
		}
	}
	// Saturday 03:00: night prices of 2026-10-03 (0.282) and 2026-10-10 (0.280). The median is 0.281.
	if v := got.Points[3].Value; !near(v, 0.281) {
		t.Errorf("03:00 value = %v, want 0.281", v)
	}
	// Saturday 12:00: day prices 0.352 and 0.350.
	if v := got.Points[12].Value; !near(v, 0.351) {
		t.Errorf("12:00 value = %v, want 0.351", v)
	}

	// The second fetch comes from the cache. The order of the instance types does not matter.
	_, err = src.Fetch(context.Background(), signal.Query{
		From: now, To: now.Add(time.Hour), InstanceTypes: []string{"g6.2xlarge", "g6.xlarge"},
	})
	if err != nil || calls != 2 {
		t.Errorf("second Fetch made %d calls in total (err %v), want 2", calls, err)
	}
}

func TestFetchCacheExpires(t *testing.T) {
	var calls int
	srv := fixtureServer(t, &calls)
	clk := clocktesting.NewFakeClock(now)
	src, err := New(Config{Region: "eu-north-1", Endpoint: srv.URL}, testCreds, signal.NewHTTPGetter(), clk)
	if err != nil {
		t.Fatal(err)
	}
	q := signal.Query{From: now, To: now.Add(time.Hour), InstanceTypes: []string{"g6.xlarge", "g6.2xlarge"}}
	if _, err := src.Fetch(context.Background(), q); err != nil {
		t.Fatal(err)
	}
	clk.Step(signal.DefaultCacheTTL + time.Minute)
	if _, err := src.Fetch(context.Background(), q); err != nil {
		t.Fatal(err)
	}
	if calls != 4 {
		t.Errorf("calls = %d, want 4: two pages, two times", calls)
	}
}

func TestFetchErrors(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("InstanceType.1") == "bad.xml" {
			_, _ = w.Write([]byte("<DescribeSpotPriceHistoryResponse><spotPriceHistorySet><item>"))
			return
		}
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte("<Response><Errors><Error><Code>UnauthorizedOperation</Code></Error></Errors></Response>"))
	}))
	defer srv.Close()
	src, err := New(Config{Region: "eu-north-1", Endpoint: srv.URL}, testCreds, signal.NewHTTPGetter(),
		clocktesting.NewFakeClock(now))
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name  string
		types []string
		want  string
	}{
		{name: "no instance types", want: "no instance types"},
		{name: "API error", types: []string{"g6.xlarge"}, want: "UnauthorizedOperation"},
		{name: "bad XML", types: []string{"bad.xml"}, want: "decode"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := src.Fetch(context.Background(), signal.Query{From: now, To: now.Add(time.Hour), InstanceTypes: tt.types})
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Errorf("Fetch() error = %v, want it to contain %q", err, tt.want)
			}
		})
	}
}

func TestFetchCredentialError(t *testing.T) {
	failing := aws.CredentialsProviderFunc(func(context.Context) (aws.Credentials, error) {
		return aws.Credentials{}, context.DeadlineExceeded
	})
	src, err := New(Config{Region: "eu-north-1"}, failing, signal.NewHTTPGetter(), clocktesting.NewFakeClock(now))
	if err != nil {
		t.Fatal(err)
	}
	_, err = src.Fetch(context.Background(), signal.Query{From: now, To: now.Add(time.Hour), InstanceTypes: []string{"a.b"}})
	if err == nil || !strings.Contains(err.Error(), "credentials") {
		t.Errorf("Fetch() error = %v, want a credentials error", err)
	}
}

func record(itype, zone, price string, ts time.Time) spotPrice {
	return spotPrice{InstanceType: itype, AvailabilityZone: zone, SpotPrice: price, Timestamp: ts}
}

func TestHourly(t *testing.T) {
	start := now.Add(-4 * time.Hour)
	recs := []spotPrice{
		// Zone a: 1.0 from 19:30, 0.5 from 21:30.
		record("g6.xlarge", "a", "1.0", start.Add(-30*time.Minute)),
		record("g6.xlarge", "a", "0.5", start.Add(90*time.Minute)),
		// Another type in zone b: 0.8 from 21:00, so it is the minimum at 21:00.
		record("g6.2xlarge", "b", "0.8", start.Add(time.Hour)),
	}
	got, err := hourly(recs, start, now)
	if err != nil {
		t.Fatal(err)
	}
	want := []float64{1.0, 0.8, 0.5, 0.5}
	if len(got.Points) != len(want) {
		t.Fatalf("hourly() = %+v, want %d points", got.Points, len(want))
	}
	for i, w := range want {
		p := got.Points[i]
		if !p.Start.Equal(start.Add(time.Duration(i)*time.Hour)) || !near(p.Value, w) || p.Assumed {
			t.Errorf("point %d = %+v, want value %v", i, p, w)
		}
	}
}

func TestHourlyStartsAtFirstKnownPrice(t *testing.T) {
	start := now.Add(-3 * time.Hour)
	got, err := hourly([]spotPrice{record("g6.xlarge", "a", "1", start.Add(90*time.Minute))}, start, now)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Points) != 1 || !got.Points[0].Start.Equal(start.Add(2*time.Hour)) {
		t.Errorf("hourly() = %+v, want one point from the first hour with a price", got.Points)
	}
}

func TestHourlyBadPrice(t *testing.T) {
	if _, err := hourly([]spotPrice{record("g6.xlarge", "a", "cheap", now.Add(-2*time.Hour))},
		now.Add(-time.Hour), now); err == nil {
		t.Error("hourly() error = nil for a bad price")
	}
}
