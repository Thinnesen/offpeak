// Package energidataservice reads Danish day-ahead prices and CO2 prognoses. It needs no key.
package energidataservice

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"time"

	"github.com/Thinnesen/offpeak/internal/forecast"
	"github.com/Thinnesen/offpeak/internal/signal"
)

// DefaultBaseURL is the base URL of the Energi Data Service API.
const DefaultBaseURL = "https://api.energidataservice.dk"

// Series names, equal to the API values of the SignalSource.
const (
	DayAheadPrice = "DayAheadPrice"
	CO2Prognosis  = "CO2Prognosis"
)

// fetchAhead is fixed, so that all requests in one hour are equal and the cache can share them.
const fetchAhead = 72 * time.Hour

// timeLayout is the format of the start and end query parameters.
const timeLayout = "2006-01-02T15:04"

type dataset struct {
	name, timeField, valueField, unit string
	resolution                        time.Duration
}

var datasets = map[string]dataset{
	DayAheadPrice: {
		name: "DayAheadPrices", timeField: "TimeUTC", valueField: "DayAheadPriceEUR",
		unit: "EUR/MWh", resolution: 15 * time.Minute,
	},
	CO2Prognosis: {
		name: "CO2EmisProg", timeField: "Minutes5UTC", valueField: "CO2Emission",
		unit: "gCO2eq/kWh", resolution: 5 * time.Minute,
	},
}

// Config selects the data.
type Config struct {
	// Area is DK1 or DK2.
	Area string
	// Series is DayAheadPrice or CO2Prognosis.
	Series string
	// BaseURL is the API base URL. The default is DefaultBaseURL.
	BaseURL string
}

// Source is the Energi Data Service provider.
type Source struct {
	area    string
	ds      dataset
	baseURL *url.URL
	getter  signal.Getter
}

// New validates cfg and returns a Source.
func New(cfg Config, getter signal.Getter) (*Source, error) {
	if cfg.Area != "DK1" && cfg.Area != "DK2" {
		return nil, fmt.Errorf("area %q is not DK1 or DK2", cfg.Area)
	}
	ds, ok := datasets[cfg.Series]
	if !ok {
		return nil, fmt.Errorf("series %q is not %s or %s", cfg.Series, DayAheadPrice, CO2Prognosis)
	}
	base := cfg.BaseURL
	if base == "" {
		base = DefaultBaseURL
	}
	u, err := url.Parse(base)
	if err != nil {
		return nil, fmt.Errorf("parse base URL: %w", err)
	}
	return &Source{area: cfg.Area, ds: ds, baseURL: u, getter: getter}, nil
}

// Fetch returns the series for [q.From, q.To), with profile values from 14 days of history.
func (s *Source) Fetch(ctx context.Context, q signal.Query) (forecast.Series, error) {
	start := q.From.UTC().Truncate(time.Hour)
	body, err := s.getter.Get(ctx, s.requestURL(start.Add(-signal.HistoryWindow), start.Add(fetchAhead)), nil)
	if err != nil {
		return forecast.Series{}, fmt.Errorf("energidataservice %s: %w", s.ds.name, err)
	}
	all, err := s.parse(body)
	if err != nil {
		return forecast.Series{}, fmt.Errorf("energidataservice %s: %w", s.ds.name, err)
	}
	return all.Complete(q.From, q.To), nil
}

func (s *Source) requestURL(start, end time.Time) *url.URL {
	filter, _ := json.Marshal(map[string][]string{"PriceArea": {s.area}})
	u := s.baseURL.JoinPath("dataset", s.ds.name)
	u.RawQuery = url.Values{
		"start":    {start.Format(timeLayout)},
		"end":      {end.Format(timeLayout)},
		"timezone": {"UTC"},
		"filter":   {string(filter)},
		"columns":  {s.ds.timeField + ",PriceArea," + s.ds.valueField},
		"sort":     {s.ds.timeField + " asc"},
		"limit":    {"0"},
	}.Encode()
	return u
}

func (s *Source) parse(body []byte) (forecast.Series, error) {
	var resp struct {
		Records []map[string]any `json:"records"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		return forecast.Series{}, fmt.Errorf("decode response: %w", err)
	}
	points := make([]forecast.Point, 0, len(resp.Records))
	for _, r := range resp.Records {
		v, ok := r[s.ds.valueField].(float64)
		if !ok {
			continue // null value
		}
		ts, _ := r[s.ds.timeField].(string)
		t, err := time.ParseInLocation("2006-01-02T15:04:05", ts, time.UTC)
		if err != nil {
			return forecast.Series{}, fmt.Errorf("parse time %q: %w", ts, err)
		}
		points = append(points, forecast.Point{Start: t, End: t.Add(s.ds.resolution), Value: v})
	}
	return forecast.NewSeries(s.ds.unit, points)
}
