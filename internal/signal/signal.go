// Package signal defines the signal providers and their shared HTTP code.
package signal

import (
	"context"
	"net/http"
	"net/url"
	"time"

	"github.com/Thinnesen/offpeak/internal/forecast"
)

// HistoryWindow is the history that a provider uses to build the hour-of-week profile.
const HistoryWindow = 14 * 24 * time.Hour

// Query selects the time range of a fetch.
type Query struct {
	// From and To are the planning window. The returned series covers it where data or a profile exists.
	From, To time.Time
	// InstanceTypes are the EC2 instance types (awsSpot only).
	InstanceTypes []string
}

// Source fetches a signal time series.
type Source interface {
	Fetch(ctx context.Context, q Query) (forecast.Series, error)
}

// Getter fetches a URL and returns the response body.
type Getter interface {
	Get(ctx context.Context, u *url.URL, header http.Header) ([]byte, error)
}
