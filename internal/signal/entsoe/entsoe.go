// Package entsoe reads day-ahead prices from the ENTSO-E Transparency Platform.
package entsoe

import (
	"context"
	"encoding/xml"
	"fmt"
	"maps"
	"net/url"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/Thinnesen/offpeak/internal/forecast"
	"github.com/Thinnesen/offpeak/internal/signal"
)

// DefaultBaseURL is the API endpoint of the Transparency Platform.
const DefaultBaseURL = "https://web-api.tp.entsoe.eu/api"

const (
	unit = "EUR/MWh"
	// fetchAhead is how far after the start of the planning window the provider asks for data.
	fetchAhead   = 72 * time.Hour
	periodLayout = "200601021504"
	timeLayout   = "2006-01-02T15:04Z"
)

var eicPattern = regexp.MustCompile(`^[0-9]{2}[A-Z][0-9A-Z-]{12}[0-9A-Z]$`)

var resolutions = map[string]time.Duration{"PT15M": 15 * time.Minute, "PT30M": 30 * time.Minute, "PT60M": time.Hour}

// TokenFunc returns the API token.
type TokenFunc func(ctx context.Context) (string, error)

// Config selects the data.
type Config struct {
	// BiddingZone is the EIC code of the bidding zone.
	BiddingZone string
	// BaseURL is the API endpoint. The default is DefaultBaseURL.
	BaseURL string
}

// Source is the ENTSO-E provider.
type Source struct {
	zone    string
	baseURL *url.URL
	token   TokenFunc
	getter  signal.Getter
}

// New validates cfg and returns a Source.
func New(cfg Config, token TokenFunc, getter signal.Getter) (*Source, error) {
	if !eicPattern.MatchString(cfg.BiddingZone) {
		return nil, fmt.Errorf("bidding zone %q is not an EIC code", cfg.BiddingZone)
	}
	base := cfg.BaseURL
	if base == "" {
		base = DefaultBaseURL
	}
	u, err := url.Parse(base)
	if err != nil {
		return nil, fmt.Errorf("parse base URL: %w", err)
	}
	return &Source{zone: cfg.BiddingZone, baseURL: u, token: token, getter: getter}, nil
}

// Fetch returns the prices with profile values. The token is in the query, so errors never show the URL.
func (s *Source) Fetch(ctx context.Context, q signal.Query) (forecast.Series, error) {
	token, err := s.token(ctx)
	if err != nil {
		return forecast.Series{}, fmt.Errorf("entsoe: get token: %w", err)
	}
	start := q.From.UTC().Truncate(time.Hour)
	u := *s.baseURL
	u.RawQuery = url.Values{
		"documentType":  {"A44"},
		"in_Domain":     {s.zone},
		"out_Domain":    {s.zone},
		"periodStart":   {start.Add(-signal.HistoryWindow).Format(periodLayout)},
		"periodEnd":     {start.Add(fetchAhead).Format(periodLayout)},
		"securityToken": {token},
	}.Encode()
	body, err := s.getter.Get(ctx, &u, nil)
	if err != nil {
		return forecast.Series{}, fmt.Errorf("entsoe: %w", err)
	}
	all, err := parse(body)
	if err != nil {
		return forecast.Series{}, fmt.Errorf("entsoe: %w", err)
	}
	return all.Complete(q.From, q.To), nil
}

type document struct {
	XMLName    xml.Name
	TimeSeries []struct {
		Periods []period `xml:"Period"`
	} `xml:"TimeSeries"`
	Reasons []struct {
		Code string `xml:"code"`
		Text string `xml:"text"`
	} `xml:"Reason"`
}

type period struct {
	Start      string `xml:"timeInterval>start"`
	End        string `xml:"timeInterval>end"`
	Resolution string `xml:"resolution"`
	Points     []struct {
		Position int    `xml:"position"`
		Price    string `xml:"price.amount"`
	} `xml:"Point"`
}

// parse reads a Publication_MarketDocument. On overlap, the finest resolution wins.
func parse(body []byte) (forecast.Series, error) {
	var doc document
	if err := xml.Unmarshal(body, &doc); err != nil {
		return forecast.Series{}, fmt.Errorf("decode response: %w", err)
	}
	switch doc.XMLName.Local {
	case "Publication_MarketDocument":
	case "Acknowledgement_MarketDocument":
		texts := make([]string, len(doc.Reasons))
		for i, r := range doc.Reasons {
			texts[i] = r.Code + " " + r.Text
		}
		return forecast.Series{}, fmt.Errorf("acknowledgement: %s", strings.Join(texts, "; "))
	default:
		return forecast.Series{}, fmt.Errorf("unexpected document %q", doc.XMLName.Local)
	}

	byRes := map[time.Duration][]period{}
	for _, ts := range doc.TimeSeries {
		for _, p := range ts.Periods {
			byRes[resolutions[p.Resolution]] = append(byRes[resolutions[p.Resolution]], p)
		}
	}
	kept := forecast.Series{Unit: unit}
	for _, res := range slices.Sorted(maps.Keys(byRes)) {
		var add []forecast.Point
		for _, p := range byRes[res] {
			pts, err := p.points()
			if err != nil {
				return forecast.Series{}, err
			}
			for _, pt := range pts {
				if kept.Window(pt.Start, pt.End).Covered == 0 {
					add = append(add, pt)
				}
			}
		}
		var err error
		if kept, err = forecast.NewSeries(unit, append(kept.Points, firstOfOverlaps(add)...)); err != nil {
			return forecast.Series{}, err
		}
	}
	return kept, nil
}

// points returns the points of the period. A missing position (curve A03) repeats the previous price.
func (p period) points() ([]forecast.Point, error) {
	res, ok := resolutions[p.Resolution]
	if !ok {
		return nil, fmt.Errorf("unsupported resolution %q", p.Resolution)
	}
	start, err := time.Parse(timeLayout, p.Start)
	if err != nil {
		return nil, fmt.Errorf("parse period start: %w", err)
	}
	end, err := time.Parse(timeLayout, p.End)
	if err != nil {
		return nil, fmt.Errorf("parse period end: %w", err)
	}
	out := make([]forecast.Point, 0, len(p.Points))
	for i, pt := range p.Points {
		price, err := strconv.ParseFloat(pt.Price, 64)
		if err != nil {
			return nil, fmt.Errorf("parse price %q: %w", pt.Price, err)
		}
		from := start.Add(time.Duration(pt.Position-1) * res)
		to := end
		if i+1 < len(p.Points) {
			to = start.Add(time.Duration(p.Points[i+1].Position-1) * res)
		}
		if !from.Before(to) || from.Before(start) {
			return nil, fmt.Errorf("position %d is outside the period", pt.Position)
		}
		out = append(out, forecast.Point{Start: from, End: to, Value: price})
	}
	return out, nil
}

// firstOfOverlaps sorts points by start and drops each point that overlaps an earlier one.
func firstOfOverlaps(points []forecast.Point) []forecast.Point {
	slices.SortStableFunc(points, func(a, b forecast.Point) int { return a.Start.Compare(b.Start) })
	out := points[:0]
	for _, p := range points {
		if len(out) == 0 || !p.Start.Before(out[len(out)-1].End) {
			out = append(out, p)
		}
	}
	return out
}
