package signal

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

func TestHTTPGetter(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/ok":
			if r.Header.Get("auth-token") != "secret" {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			_, _ = w.Write([]byte(`{"a":1}`))
		case "/big":
			_, _ = w.Write([]byte(strings.Repeat("x", 100)))
		case "/fail":
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = w.Write([]byte(strings.Repeat("slow down ", 200)))
		case "/slow":
			time.Sleep(200 * time.Millisecond)
		}
	}))
	defer srv.Close()

	g := &HTTPGetter{Client: srv.Client(), MaxBytes: 50}
	get := func(path, query string, h http.Header) ([]byte, error) {
		u, err := url.Parse(srv.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		u.RawQuery = query
		return g.Get(context.Background(), u, h)
	}

	t.Run("body and header", func(t *testing.T) {
		body, err := get("/ok", "", http.Header{"auth-token": {"secret"}})
		if err != nil || string(body) != `{"a":1}` {
			t.Errorf("Get() = %q, %v", body, err)
		}
	})
	t.Run("body too large", func(t *testing.T) {
		if _, err := get("/big", "", nil); err == nil || !strings.Contains(err.Error(), "larger than") {
			t.Errorf("Get() error = %v, want a size error", err)
		}
	})
	t.Run("status error hides the URL", func(t *testing.T) {
		_, err := get("/fail", "securityToken=hunter2", nil)
		if err == nil {
			t.Fatal("Get() error = nil")
		}
		msg := err.Error()
		if !strings.Contains(msg, "429") {
			t.Errorf("error %q does not name the status", msg)
		}
		if strings.Contains(msg, "hunter2") || strings.Contains(msg, srv.URL) {
			t.Errorf("error %q leaks the URL", msg)
		}
		if len(msg) > 400 {
			t.Errorf("error is %d bytes long, want a short snippet", len(msg))
		}
	})
	t.Run("transport error hides the URL", func(t *testing.T) {
		g := &HTTPGetter{Client: &http.Client{Timeout: 50 * time.Millisecond}, MaxBytes: 50}
		u, _ := url.Parse(srv.URL + "/slow?securityToken=hunter2")
		_, err := g.Get(context.Background(), u, nil)
		if err == nil {
			t.Fatal("Get() error = nil")
		}
		if strings.Contains(err.Error(), "hunter2") {
			t.Errorf("error %q leaks the token", err)
		}
	})
}

func TestNewHTTPGetter(t *testing.T) {
	g := NewHTTPGetter()
	if g.Client.Timeout != DefaultTimeout || g.MaxBytes != DefaultMaxBytes {
		t.Errorf("NewHTTPGetter() = %+v", g)
	}

	var redirected bool
	target := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { redirected = true }))
	defer target.Close()
	srv := httptest.NewServer(http.RedirectHandler(target.URL, http.StatusFound))
	defer srv.Close()

	u, _ := url.Parse(srv.URL)
	if _, err := g.Get(context.Background(), u, http.Header{"auth-token": {"secret"}}); err == nil {
		t.Error("Get() of a redirect: error = nil, want a status error")
	}
	if redirected {
		t.Error("Get() followed the redirect")
	}
}
