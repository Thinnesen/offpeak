package signal

import (
	"context"
	"errors"
	"maps"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"sync"
	"time"

	"k8s.io/utils/clock"
)

const (
	// DefaultCacheTTL is how long a response stays in the cache.
	DefaultCacheTTL = 10 * time.Minute
	// DefaultErrorTTL is how long an error stays in the cache. It protects rate-limited APIs.
	DefaultErrorTTL = time.Minute
)

// CachingGetter keeps responses and errors in memory for a TTL. It is safe for concurrent use.
type CachingGetter struct {
	next     Getter
	clock    clock.PassiveClock
	ttl      time.Duration
	errorTTL time.Duration

	mu      sync.Mutex
	entries map[string]cacheEntry
}

type cacheEntry struct {
	body    []byte
	err     error
	expires time.Time
}

// NewCachingGetter returns a CachingGetter with the default TTLs.
func NewCachingGetter(next Getter, clk clock.PassiveClock) *CachingGetter {
	return &CachingGetter{
		next:     next,
		clock:    clk,
		ttl:      DefaultCacheTTL,
		errorTTL: DefaultErrorTTL,
		entries:  map[string]cacheEntry{},
	}
}

// Get returns the cached response for the URL and header, or fetches it.
func (c *CachingGetter) Get(ctx context.Context, u *url.URL, header http.Header) ([]byte, error) {
	key := cacheKey(u, header)
	now := c.clock.Now()

	c.mu.Lock()
	e, ok := c.entries[key]
	c.mu.Unlock()
	if ok && now.Before(e.expires) {
		return e.body, e.err
	}

	body, err := c.next.Get(ctx, u, header)
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return nil, err
	}
	ttl := c.ttl
	if err != nil {
		ttl = c.errorTTL
	}

	c.mu.Lock()
	defer c.mu.Unlock()
	for k, old := range c.entries {
		if !now.Before(old.expires) {
			delete(c.entries, k)
		}
	}
	c.entries[key] = cacheEntry{body: body, err: err, expires: now.Add(ttl)}
	return body, err
}

func (c *CachingGetter) len() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.entries)
}

func cacheKey(u *url.URL, header http.Header) string {
	var b strings.Builder
	b.WriteString(u.String())
	for _, k := range slices.Sorted(maps.Keys(header)) {
		b.WriteString("\n" + k + ": " + strings.Join(header[k], ","))
	}
	return b.String()
}
