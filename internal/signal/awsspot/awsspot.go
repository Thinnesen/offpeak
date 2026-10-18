// Package awsspot reads the EC2 spot price history with a signed EC2 Query API call. It has no forecast.
package awsspot

import (
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	v4 "github.com/aws/aws-sdk-go-v2/aws/signer/v4"
	"github.com/aws/aws-sdk-go-v2/config"
	"k8s.io/utils/clock"

	"github.com/Thinnesen/offpeak/internal/forecast"
	"github.com/Thinnesen/offpeak/internal/signal"
)

const (
	// DefaultProductDescription is the default product of the spot price history.
	DefaultProductDescription = "Linux/UNIX"
	unit                      = "USD/h"
	apiVersion                = "2016-11-15"
	// emptyPayloadHash is the SHA-256 of an empty body.
	emptyPayloadHash = "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"
	// maxPages limits the pagination loop.
	maxPages = 100
)

var regionPattern = regexp.MustCompile(`^[a-z]{2}(-[a-z]+)+-[0-9]+$`)

// DefaultCredentials returns the default AWS credential chain (Pod Identity or IRSA). No static keys.
func DefaultCredentials(ctx context.Context, region string) (aws.CredentialsProvider, error) {
	cfg, err := config.LoadDefaultConfig(ctx, config.WithRegion(region))
	if err != nil {
		return nil, fmt.Errorf("load AWS configuration: %w", err)
	}
	return cfg.Credentials, nil
}

// Config selects the spot prices.
type Config struct {
	Region             string
	ProductDescription string
	// Endpoint is the EC2 endpoint. The default is the regional endpoint.
	Endpoint string
}

// Source is the AWS spot price provider.
type Source struct {
	region   string
	product  string
	endpoint *url.URL
	creds    aws.CredentialsProvider
	getter   signal.Getter
	clock    clock.PassiveClock
	signer   *v4.Signer

	mu    sync.Mutex
	cache map[string]cachedHistory
}

type cachedHistory struct {
	series  forecast.Series
	expires time.Time
}

// New validates cfg and returns a Source.
func New(cfg Config, creds aws.CredentialsProvider, getter signal.Getter, clk clock.PassiveClock) (*Source, error) {
	if !regionPattern.MatchString(cfg.Region) {
		return nil, fmt.Errorf("region %q is not valid", cfg.Region)
	}
	product := cfg.ProductDescription
	if product == "" {
		product = DefaultProductDescription
	}
	endpoint := cfg.Endpoint
	if endpoint == "" {
		endpoint = "https://ec2." + cfg.Region + ".amazonaws.com"
		if strings.HasPrefix(cfg.Region, "cn-") {
			endpoint += ".cn"
		}
	}
	u, err := url.Parse(endpoint)
	if err != nil {
		return nil, fmt.Errorf("parse endpoint: %w", err)
	}
	return &Source{
		region: cfg.Region, product: product, endpoint: u, creds: creds, getter: getter, clock: clk,
		signer: v4.NewSigner(), cache: map[string]cachedHistory{},
	}, nil
}

// Fetch returns the profile of the lowest spot price over the instance types and zones.
func (s *Source) Fetch(ctx context.Context, q signal.Query) (forecast.Series, error) {
	if len(q.InstanceTypes) == 0 {
		return forecast.Series{}, errors.New("awsspot: no instance types")
	}
	end := s.clock.Now().UTC().Truncate(time.Hour)
	history, err := s.history(ctx, q.InstanceTypes, end.Add(-signal.HistoryWindow), end)
	if err != nil {
		return forecast.Series{}, err
	}
	// The history ends now, so every point in the planning window comes from the profile.
	return history.Complete(q.From, q.To), nil
}

// history returns the hourly price history, from the cache when possible.
func (s *Source) history(ctx context.Context, instanceTypes []string, start, end time.Time) (forecast.Series, error) {
	sorted := slices.Sorted(slices.Values(instanceTypes))
	key := strings.Join(sorted, ",") + "|" + start.Format(time.RFC3339)
	now := s.clock.Now()

	s.mu.Lock()
	c, ok := s.cache[key]
	s.mu.Unlock()
	if ok && now.Before(c.expires) {
		return c.series, nil
	}

	recs, err := s.describe(ctx, sorted, start, end)
	if err != nil {
		return forecast.Series{}, fmt.Errorf("awsspot: %w", err)
	}
	series, err := hourly(recs, start, end)
	if err != nil {
		return forecast.Series{}, fmt.Errorf("awsspot: %w", err)
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	for k, old := range s.cache {
		if !now.Before(old.expires) {
			delete(s.cache, k)
		}
	}
	s.cache[key] = cachedHistory{series: series, expires: now.Add(signal.DefaultCacheTTL)}
	return series, nil
}

// spotPrice is one record of the spot price history.
type spotPrice struct {
	InstanceType     string    `xml:"instanceType"`
	AvailabilityZone string    `xml:"availabilityZone"`
	SpotPrice        string    `xml:"spotPrice"`
	Timestamp        time.Time `xml:"timestamp"`
}

type describeResponse struct {
	Items     []spotPrice `xml:"spotPriceHistorySet>item"`
	NextToken string      `xml:"nextToken"`
}

// describe calls DescribeSpotPriceHistory and follows the pages.
func (s *Source) describe(ctx context.Context, instanceTypes []string, start, end time.Time) ([]spotPrice, error) {
	params := url.Values{
		"Action":               {"DescribeSpotPriceHistory"},
		"Version":              {apiVersion},
		"ProductDescription.1": {s.product},
		"StartTime":            {start.Format(time.RFC3339)},
		"EndTime":              {end.Format(time.RFC3339)},
		"MaxResults":           {"1000"},
	}
	for i, t := range instanceTypes {
		params.Set("InstanceType."+strconv.Itoa(i+1), t)
	}
	var recs []spotPrice
	for range maxPages {
		resp, err := s.call(ctx, params)
		if err != nil {
			return nil, err
		}
		recs = append(recs, resp.Items...)
		if resp.NextToken == "" {
			return recs, nil
		}
		params.Set("NextToken", resp.NextToken)
	}
	return nil, fmt.Errorf("DescribeSpotPriceHistory: more than %d pages", maxPages)
}

func (s *Source) call(ctx context.Context, params url.Values) (*describeResponse, error) {
	u := *s.endpoint
	u.Path = "/"
	u.RawQuery = params.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, fmt.Errorf("build request: %w", err)
	}
	creds, err := s.creds.Retrieve(ctx)
	if err != nil {
		return nil, fmt.Errorf("get AWS credentials: %w", err)
	}
	if err := s.signer.SignHTTP(ctx, creds, req, emptyPayloadHash, "ec2", s.region, s.clock.Now()); err != nil {
		return nil, fmt.Errorf("sign request: %w", err)
	}
	body, err := s.getter.Get(ctx, &u, req.Header)
	if err != nil {
		return nil, err
	}
	var resp describeResponse
	if err := xml.Unmarshal(body, &resp); err != nil {
		return nil, fmt.Errorf("decode DescribeSpotPriceHistory response: %w", err)
	}
	return &resp, nil
}

type change struct {
	at    time.Time
	price float64
}

// hourly builds hourly points. A record is valid until the next record of the same type and zone.
func hourly(recs []spotPrice, start, end time.Time) (forecast.Series, error) {
	changes := map[string][]change{}
	for _, r := range recs {
		price, err := strconv.ParseFloat(r.SpotPrice, 64)
		if err != nil {
			return forecast.Series{}, fmt.Errorf("parse spot price %q: %w", r.SpotPrice, err)
		}
		k := r.InstanceType + "/" + r.AvailabilityZone
		changes[k] = append(changes[k], change{at: r.Timestamp, price: price})
	}
	for _, c := range changes {
		slices.SortFunc(c, func(a, b change) int { return a.at.Compare(b.at) })
	}

	var points []forecast.Point
	for h := start; h.Before(end); h = h.Add(time.Hour) {
		lowest, found := 0.0, false
		for _, c := range changes {
			// i is the number of changes at or before h.
			i, _ := slices.BinarySearchFunc(c, h, func(c change, t time.Time) int {
				if c.at.After(t) {
					return 1
				}
				return -1
			})
			if i == 0 {
				continue
			}
			if p := c[i-1].price; !found || p < lowest {
				lowest, found = p, true
			}
		}
		if found {
			points = append(points, forecast.Point{Start: h, End: h.Add(time.Hour), Value: lowest})
		}
	}
	return forecast.NewSeries(unit, points)
}
