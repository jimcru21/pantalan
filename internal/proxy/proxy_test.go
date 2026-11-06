package proxy_test

import (
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/pantalan/pantalan/internal/proxy"
)

func TestProxyRoutesByHostAndPreservesHostHeader(t *testing.T) {
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, r.Host)
	}))
	defer backend.Close()

	p := proxy.New(slog.New(slog.DiscardHandler))
	p.SetRoute("App.Example.test", strings.TrimPrefix(backend.URL, "http://"))

	rec := httptest.NewRecorder()
	p.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "http://app.example.test:8080/", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if got := rec.Body.String(); got != "app.example.test:8080" {
		t.Errorf("upstream saw Host %q, want the public host", got)
	}

	rec = httptest.NewRecorder()
	p.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "http://unknown.test/", nil))
	if rec.Code != http.StatusNotFound {
		t.Errorf("unknown host status = %d, want 404", rec.Code)
	}
}
