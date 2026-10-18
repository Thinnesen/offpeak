// Package ukcarbon reads the 48-hour carbon forecast for Great Britain (National Grid ESO). No key.
package ukcarbon

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strconv"
	"time"

	"github.com/Thinnesen/offpeak/internal/forecast"
	"github.com/Thinnesen/offpeak/internal/signal"
)

// DefaultBaseURL is the API base URL.
const DefaultBaseURL = "https://api.carbonintensity.org.uk"

const (
	unit       = "gCO2eq/kWh"
	timeLayout = "2006-01-02T15:04Z"
	// maxRegionID is the largest region id. 0 means the whole of Great Britain.
	maxRegionID = 17
)

// Config selects the data.
type Config struct {
	// RegionID is 0 for Great Britain, or a regional id from 1 to 17 (for example 13 for London).
	RegionID int32
	// BaseURL is the API base URL. The default is DefaultBaseURL.
	BaseURL string
}

// Source is the UK Carbon Intensity provider.
type Source struct {
	regionID int32
	baseURL  *url.URL
	getter   signal.Getter
}

// New validates cfg and returns a Source.
func New(cfg Config, getter signal.Getter) (*Source, error) {
	if cfg.RegionID < 0 || cfg.RegionID > maxRegionID {
		return nil, fmt.Errorf("region id %d is not from 0 to %d", cfg.RegionID, maxRegionID)
	}
	base := cfg.BaseURL
	if base == "" {
		base = DefaultBaseURL
	}
	u, err := url.Parse(base)
	if err != nil {
		return nil, fmt.Errorf("parse base URL: %w", err)
	}
	return &Source{regionID: cfg.RegionID, baseURL: u, getter: getter}, nil
}

// Fetch returns the points in [q.From, q.To). The request starts at the full hour, for the cache.
func (s *Source) Fetch(ctx context.Context, q signal.Query) (forecast.Series, error) {
	from := q.From.UTC().Truncate(time.Hour).Format(timeLayout)
	u := s.baseURL.JoinPath("intensity", from, "fw48h")
	if s.regionID > 0 {
		u = s.baseURL.JoinPath("regional", "intensity", from, "fw48h", "regionid", strconv.Itoa(int(s.regionID)))
	}
	body, err := s.getter.Get(ctx, u, nil)
	if err != nil {
		return forecast.Series{}, fmt.Errorf("ukcarbon: %w", err)
	}
	all, err := parse(body)
	if err != nil {
		return forecast.Series{}, fmt.Errorf("ukcarbon: %w", err)
	}
	return all.Between(q.From, q.To), nil
}

type period struct {
	From      string `json:"from"`
	To        string `json:"to"`
	Intensity struct {
		Forecast *float64 `json:"forecast"`
	} `json:"intensity"`
}

// parse reads the national or the regional response shape.
func parse(body []byte) (forecast.Series, error) {
	var resp struct {
		Data json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		return forecast.Series{}, fmt.Errorf("decode response: %w", err)
	}
	var periods []period
	if err := json.Unmarshal(resp.Data, &periods); err != nil {
		var regional struct {
			Data []period `json:"data"`
		}
		if err := json.Unmarshal(resp.Data, &regional); err != nil {
			return forecast.Series{}, fmt.Errorf("decode response: %w", err)
		}
		periods = regional.Data
	}
	points := make([]forecast.Point, 0, len(periods))
	for _, p := range periods {
		from, err := time.Parse(timeLayout, p.From)
		if err != nil {
			return forecast.Series{}, fmt.Errorf("parse time %q: %w", p.From, err)
		}
		to, err := time.Parse(timeLayout, p.To)
		if err != nil {
			return forecast.Series{}, fmt.Errorf("parse time %q: %w", p.To, err)
		}
		if p.Intensity.Forecast == nil {
			continue
		}
		points = append(points, forecast.Point{Start: from, End: to, Value: *p.Intensity.Forecast})
	}
	if len(points) == 0 {
		return forecast.Series{}, errors.New("the response has no forecast")
	}
	return forecast.NewSeries(unit, points)
}
