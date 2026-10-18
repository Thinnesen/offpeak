// Package weatherproxy estimates clean power from the Open-Meteo sun and wind forecast. It is a proxy.
package weatherproxy

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/url"
	"strconv"
	"time"

	"github.com/Thinnesen/offpeak/internal/forecast"
	"github.com/Thinnesen/offpeak/internal/region"
	"github.com/Thinnesen/offpeak/internal/signal"
)

// DefaultBaseURL is free for non-commercial use. Else host Open-Meteo and set --open-meteo-url.
const DefaultBaseURL = "https://api.open-meteo.com"

// Unit is the unit of the clean power index.
const Unit = "index"

const (
	// fullSun is the radiation (W/m2) that counts as full solar output.
	fullSun = 800.0
	// cutIn and rated are the wind speeds (km/h, at 100 m) of a typical wind turbine.
	cutIn = 11.0
	rated = 45.0
	// forecastDays is constant, so that all jobs share one cached response per place.
	forecastDays = 16
)

// Config selects the place. Set CloudRegion, or Latitude and Longitude.
type Config struct {
	// CloudRegion is a key of the region table, for example aws:eu-north-1.
	CloudRegion string
	// Latitude and Longitude are decimal degrees.
	Latitude, Longitude string
	// BaseURL is the Open-Meteo base URL. The default is DefaultBaseURL.
	BaseURL string
}

// Source is the weather proxy provider.
type Source struct {
	lat, lon string
	baseURL  *url.URL
	getter   signal.Getter
}

// New validates cfg and returns a Source.
func New(cfg Config, getter signal.Getter) (*Source, error) {
	lat, lon, err := place(cfg)
	if err != nil {
		return nil, err
	}
	base := cfg.BaseURL
	if base == "" {
		base = DefaultBaseURL
	}
	u, err := url.Parse(base)
	if err != nil {
		return nil, fmt.Errorf("parse base URL: %w", err)
	}
	return &Source{lat: lat, lon: lon, baseURL: u, getter: getter}, nil
}

func place(cfg Config) (lat, lon string, err error) {
	hasCoord := cfg.Latitude != "" || cfg.Longitude != ""
	switch {
	case cfg.CloudRegion != "" && hasCoord:
		return "", "", errors.New("set cloudRegion, or latitude and longitude, not both")
	case cfg.CloudRegion != "":
		info, err := region.Lookup(cfg.CloudRegion)
		if err != nil {
			return "", "", err
		}
		return strconv.FormatFloat(info.Latitude, 'f', -1, 64), strconv.FormatFloat(info.Longitude, 'f', -1, 64), nil
	case !hasCoord:
		return "", "", errors.New("set cloudRegion, or latitude and longitude")
	}
	if err := checkDegrees(cfg.Latitude, 90); err != nil {
		return "", "", fmt.Errorf("latitude: %w", err)
	}
	if err := checkDegrees(cfg.Longitude, 180); err != nil {
		return "", "", fmt.Errorf("longitude: %w", err)
	}
	return cfg.Latitude, cfg.Longitude, nil
}

func checkDegrees(s string, limit float64) error {
	v, err := strconv.ParseFloat(s, 64)
	if err != nil || math.Abs(v) > limit {
		return fmt.Errorf("%q is not a number from -%v to %v", s, limit, limit)
	}
	return nil
}

// Fetch returns the hourly clean power index for [q.From, q.To).
func (s *Source) Fetch(ctx context.Context, q signal.Query) (forecast.Series, error) {
	u := s.baseURL.JoinPath("v1", "forecast")
	u.RawQuery = url.Values{
		"latitude":      {s.lat},
		"longitude":     {s.lon},
		"hourly":        {"shortwave_radiation,wind_speed_100m"},
		"timezone":      {"UTC"},
		"forecast_days": {strconv.Itoa(forecastDays)},
	}.Encode()
	body, err := s.getter.Get(ctx, u, nil)
	if err != nil {
		return forecast.Series{}, fmt.Errorf("weatherproxy: %w", err)
	}
	all, err := parse(body)
	if err != nil {
		return forecast.Series{}, fmt.Errorf("weatherproxy: %w", err)
	}
	return all.Between(q.From, q.To), nil
}

// parse reads the forecast. Radiation is the mean of the preceding hour, so time T gives [T-1h, T).
func parse(body []byte) (forecast.Series, error) {
	var resp struct {
		Hourly struct {
			Time      []string   `json:"time"`
			Radiation []*float64 `json:"shortwave_radiation"`
			Wind      []*float64 `json:"wind_speed_100m"`
		} `json:"hourly"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		return forecast.Series{}, fmt.Errorf("decode response: %w", err)
	}
	h := resp.Hourly
	if len(h.Radiation) != len(h.Time) || len(h.Wind) != len(h.Time) {
		return forecast.Series{}, errors.New("the hourly arrays do not have the same length")
	}
	points := make([]forecast.Point, 0, len(h.Time))
	for i, ts := range h.Time {
		t, err := time.ParseInLocation("2006-01-02T15:04", ts, time.UTC)
		if err != nil {
			return forecast.Series{}, fmt.Errorf("parse time %q: %w", ts, err)
		}
		if h.Radiation[i] == nil || h.Wind[i] == nil {
			continue
		}
		points = append(points, forecast.Point{
			Start: t.Add(-time.Hour), End: t, Value: cleanIndex(*h.Radiation[i], *h.Wind[i]),
		})
	}
	return forecast.NewSeries(Unit, points)
}

// cleanIndex is 100 minus the solar share and the wind share (cubic turbine curve), each 0 to 50.
func cleanIndex(radiation, windKmh float64) float64 {
	solar := math.Min(math.Max(radiation, 0)/fullSun, 1)
	wind := 0.0
	switch {
	case windKmh >= rated:
		wind = 1
	case windKmh > cutIn:
		wind = (cube(windKmh) - cube(cutIn)) / (cube(rated) - cube(cutIn))
	}
	return 100 - 50*(solar+wind)
}

func cube(x float64) float64 { return x * x * x }
