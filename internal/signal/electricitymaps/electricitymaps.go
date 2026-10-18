// Package electricitymaps reads the Electricity Maps carbon forecast. It builds no profile.
package electricitymaps

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"time"

	"github.com/Thinnesen/offpeak/internal/forecast"
	"github.com/Thinnesen/offpeak/internal/signal"
)

// DefaultBaseURL is the default API base URL.
const DefaultBaseURL = "https://api.electricitymaps.com/v3"

const unit = "gCO2eq/kWh"

var zonePattern = regexp.MustCompile(`^[A-Z]{2}(-[A-Z0-9]+)*$`)

// TokenFunc returns the API key.
type TokenFunc func(ctx context.Context) (string, error)

// Config selects the data.
type Config struct {
	// Zone is the Electricity Maps zone, for example SE-SE3.
	Zone string
	// BaseURL is the API base URL. The default is DefaultBaseURL.
	BaseURL string
}

// Source is the Electricity Maps provider.
type Source struct {
	zone    string
	baseURL *url.URL
	token   TokenFunc
	getter  signal.Getter
}

// New validates cfg. The base URL must use HTTPS (or loopback), because the request has the key.
func New(cfg Config, token TokenFunc, getter signal.Getter) (*Source, error) {
	if !zonePattern.MatchString(cfg.Zone) {
		return nil, fmt.Errorf("zone %q is not valid", cfg.Zone)
	}
	base := cfg.BaseURL
	if base == "" {
		base = DefaultBaseURL
	}
	u, err := url.Parse(base)
	if err != nil {
		return nil, fmt.Errorf("parse base URL: %w", err)
	}
	if u.Scheme != "https" && !isLoopback(u.Hostname()) {
		return nil, errors.New("base URL must use https")
	}
	return &Source{zone: cfg.Zone, baseURL: u, token: token, getter: getter}, nil
}

func isLoopback(host string) bool {
	ip := net.ParseIP(host)
	return host == "localhost" || (ip != nil && ip.IsLoopback())
}

// Fetch returns the forecast points that overlap [q.From, q.To).
func (s *Source) Fetch(ctx context.Context, q signal.Query) (forecast.Series, error) {
	token, err := s.token(ctx)
	if err != nil {
		return forecast.Series{}, fmt.Errorf("electricitymaps: get token: %w", err)
	}
	u := s.baseURL.JoinPath("carbon-intensity", "forecast")
	u.RawQuery = url.Values{"zone": {s.zone}}.Encode()
	body, err := s.getter.Get(ctx, u, http.Header{"auth-token": {token}})
	if err != nil {
		return forecast.Series{}, fmt.Errorf("electricitymaps: %w", err)
	}
	all, err := s.parse(body)
	if err != nil {
		return forecast.Series{}, fmt.Errorf("electricitymaps: %w", err)
	}
	return all.Between(q.From, q.To), nil
}

type item struct {
	Datetime        string  `json:"datetime"`
	CarbonIntensity float64 `json:"carbonIntensity"`
}

// parse reads the "forecast" (v3) or "data" list. A point lasts until the next point.
func (s *Source) parse(body []byte) (forecast.Series, error) {
	var resp struct {
		Forecast []item `json:"forecast"`
		Data     []item `json:"data"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		return forecast.Series{}, fmt.Errorf("decode response: %w", err)
	}
	items := resp.Forecast
	if len(items) == 0 {
		items = resp.Data
	}
	if len(items) == 0 {
		return forecast.Series{}, errors.New("the response has no forecast")
	}
	times := make([]time.Time, len(items))
	for i, it := range items {
		t, err := time.Parse(time.RFC3339, it.Datetime)
		if err != nil {
			return forecast.Series{}, fmt.Errorf("parse time %q: %w", it.Datetime, err)
		}
		times[i] = t
	}
	step := time.Hour
	points := make([]forecast.Point, len(items))
	for i, it := range items {
		if i+1 < len(items) {
			step = times[i+1].Sub(times[i])
		}
		points[i] = forecast.Point{Start: times[i], End: times[i].Add(step), Value: it.CarbonIntensity}
	}
	return forecast.NewSeries(unit, points)
}
