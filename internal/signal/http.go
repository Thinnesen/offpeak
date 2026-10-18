package signal

import (
	"context"
	"errors"
	"fmt"
	"io"
	"maps"
	"net/http"
	"net/url"
	"time"
)

const (
	// DefaultTimeout is the timeout of one HTTP request.
	DefaultTimeout = 20 * time.Second
	// DefaultMaxBytes is the largest response body that a provider reads.
	DefaultMaxBytes = 32 << 20
	// errorSnippetBytes is the part of an error body that goes into the error message.
	errorSnippetBytes = 200
)

// HTTPGetter is a Getter with a size limit. Its errors never show the URL, which can hold a token.
type HTTPGetter struct {
	Client   *http.Client
	MaxBytes int64
}

// NewHTTPGetter returns an HTTPGetter that does not follow redirects, so a token stays on its host.
func NewHTTPGetter() *HTTPGetter {
	return &HTTPGetter{
		Client: &http.Client{
			Timeout: DefaultTimeout,
			CheckRedirect: func(*http.Request, []*http.Request) error {
				return http.ErrUseLastResponse
			},
		},
		MaxBytes: DefaultMaxBytes,
	}
}

// Get sends a GET request and returns the body of a 2xx response.
func (g *HTTPGetter) Get(ctx context.Context, u *url.URL, header http.Header) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, errors.New("could not build the request")
	}
	maps.Copy(req.Header, header)
	resp, err := g.Client.Do(req)
	if err != nil {
		if ue, ok := errors.AsType[*url.Error](err); ok {
			err = ue.Err
		}
		return nil, fmt.Errorf("GET %s%s: %w", u.Host, u.Path, err)
	}
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(io.LimitReader(resp.Body, g.MaxBytes+1))
	if err != nil {
		return nil, fmt.Errorf("GET %s%s: read body: %w", u.Host, u.Path, err)
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return nil, fmt.Errorf("GET %s%s: status %d: %s", u.Host, u.Path, resp.StatusCode, snippet(body))
	}
	if int64(len(body)) > g.MaxBytes {
		return nil, fmt.Errorf("GET %s%s: body is larger than %d bytes", u.Host, u.Path, g.MaxBytes)
	}
	return body, nil
}

func snippet(b []byte) string {
	if len(b) > errorSnippetBytes {
		b = b[:errorSnippetBytes]
	}
	return string(b)
}
