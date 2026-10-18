package signal

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	clocktesting "k8s.io/utils/clock/testing"
)

type countingGetter struct {
	calls int
	err   error
}

func (g *countingGetter) Get(_ context.Context, u *url.URL, h http.Header) ([]byte, error) {
	g.calls++
	if g.err != nil {
		return nil, g.err
	}
	return []byte(u.String() + strings.Join(h["Auth-Token"], "")), nil
}

func TestCachingGetter(t *testing.T) {
	clk := clocktesting.NewFakeClock(time.Date(2026, 10, 17, 0, 0, 0, 0, time.UTC))
	next := &countingGetter{}
	c := NewCachingGetter(next, clk)
	ctx := context.Background()
	a, _ := url.Parse("https://example.com/a")
	b, _ := url.Parse("https://example.com/b")

	get := func(u *url.URL, token string) string {
		t.Helper()
		body, err := c.Get(ctx, u, http.Header{"Auth-Token": {token}})
		if err != nil {
			t.Fatal(err)
		}
		return string(body)
	}

	if got := get(a, "t1"); got != "https://example.com/at1" {
		t.Errorf("Get() = %q", got)
	}
	get(a, "t1")
	if next.calls != 1 {
		t.Errorf("calls = %d after a repeated request, want 1", next.calls)
	}
	get(b, "t1")
	get(a, "t2")
	if next.calls != 3 {
		t.Errorf("calls = %d, want 3: the URL and the header are part of the key", next.calls)
	}

	clk.Step(DefaultCacheTTL + time.Second)
	get(a, "t1")
	if next.calls != 4 {
		t.Errorf("calls = %d after the TTL, want 4", next.calls)
	}
	if n := c.len(); n != 1 {
		t.Errorf("cache holds %d entries, want 1: expired entries must go", n)
	}
}

func TestCachingGetterErrors(t *testing.T) {
	clk := clocktesting.NewFakeClock(time.Date(2026, 10, 17, 0, 0, 0, 0, time.UTC))
	next := &countingGetter{err: errors.New("status 429")}
	c := NewCachingGetter(next, clk)
	ctx := context.Background()
	u, _ := url.Parse("https://example.com/a")

	for range 2 {
		if _, err := c.Get(ctx, u, nil); err == nil {
			t.Fatal("Get() error = nil")
		}
	}
	if next.calls != 1 {
		t.Errorf("calls = %d, want 1: an error is cached for a short time", next.calls)
	}
	clk.Step(DefaultErrorTTL + time.Second)
	next.err = nil
	if _, err := c.Get(ctx, u, nil); err != nil {
		t.Errorf("Get() after the error TTL: %v", err)
	}
}

func TestCachingGetterDoesNotCacheCancel(t *testing.T) {
	clk := clocktesting.NewFakeClock(time.Date(2026, 10, 17, 0, 0, 0, 0, time.UTC))
	next := &countingGetter{err: context.Canceled}
	c := NewCachingGetter(next, clk)
	u, _ := url.Parse("https://example.com/a")
	_, _ = c.Get(context.Background(), u, nil)
	next.err = nil
	if _, err := c.Get(context.Background(), u, nil); err != nil {
		t.Errorf("Get() = %v, want no cached cancel error", err)
	}
}
