// Package energycharts reads the Energy-Charts forecast of the non-renewable share, in percent.
package energycharts

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"slices"
	"time"

	"github.com/Thinnesen/offpeak/internal/forecast"
	"github.com/Thinnesen/offpeak/internal/signal"
)

// DefaultBaseURL is the API base URL.
const DefaultBaseURL = "https://api.energy-charts.info"

// Unit is the unit of the values.
const Unit = "% not renewable"

// Countries are the country codes with data, checked on 2026-10-18.
var Countries = []string{"at", "be", "ch", "cz", "de", "dk", "es", "fi", "fr", "it", "lu", "nl", "no", "pl", "pt", "se"}

// Config selects the data.
type Config struct {
	// Country is a lower-case country code from Countries.
	Country string
	// BaseURL is the API base URL. The default is DefaultBaseURL.
	BaseURL string
}

// Source is the Energy-Charts provider.
type Source struct {
	country string
	baseURL *url.URL
	getter  signal.Getter
}

// New validates cfg and returns a Source.
func New(cfg Config, getter signal.Getter) (*Source, error) {
	if !slices.Contains(Countries, cfg.Country) {
		return nil, fmt.Errorf("country %q has no Energy-Charts data; use one of %v", cfg.Country, Countries)
	}
	base := cfg.BaseURL
	if base == "" {
		base = DefaultBaseURL
	}
	u, err := url.Parse(base)
	if err != nil {
		return nil, fmt.Errorf("parse base URL: %w", err)
	}
	return &Source{country: cfg.Country, baseURL: u, getter: getter}, nil
}

// Fetch returns the forecast points that overlap [q.From, q.To).
func (s *Source) Fetch(ctx context.Context, q signal.Query) (forecast.Series, error) {
	u := s.baseURL.JoinPath("signal")
	u.RawQuery = url.Values{"country": {s.country}}.Encode()
	body, err := s.getter.Get(ctx, u, nil)
	if err != nil {
		return forecast.Series{}, fmt.Errorf("energycharts: %w", err)
	}
	all, err := parse(body)
	if err != nil {
		return forecast.Series{}, fmt.Errorf("energycharts: %w", err)
	}
	return all.Between(q.From, q.To), nil
}

// parse reads the time stamps and shares. A point lasts until the next time stamp.
func parse(body []byte) (forecast.Series, error) {
	var resp struct {
		UnixSeconds []int64    `json:"unix_seconds"`
		Share       []*float64 `json:"share"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		return forecast.Series{}, fmt.Errorf("decode response: %w", err)
	}
	if len(resp.Share) != len(resp.UnixSeconds) {
		return forecast.Series{}, errors.New("the arrays do not have the same length")
	}
	step := 15 * time.Minute
	points := make([]forecast.Point, 0, len(resp.UnixSeconds))
	for i, sec := range resp.UnixSeconds {
		if i+1 < len(resp.UnixSeconds) {
			step = time.Duration(resp.UnixSeconds[i+1]-sec) * time.Second
		}
		if resp.Share[i] == nil {
			continue
		}
		start := time.Unix(sec, 0).UTC()
		points = append(points, forecast.Point{Start: start, End: start.Add(step), Value: 100 - *resp.Share[i]})
	}
	if len(points) == 0 {
		return forecast.Series{}, errors.New("the response has no forecast")
	}
	return forecast.NewSeries(Unit, points)
}
