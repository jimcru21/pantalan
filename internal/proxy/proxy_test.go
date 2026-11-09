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

func TestMoveRouteServesTheUpstreamUnderTheNewHostOnly(t *testing.T) {
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(r.Host))
	}))
	defer backend.Close()

	p := proxy.New(slog.New(slog.DiscardHandler))
	p.SetRoute("old.example.test", strings.TrimPrefix(backend.URL, "http://"))
	p.MoveRoute("old.example.test", "New.Example.test")
	p.MoveRoute("nothing.example.test", "other.example.test") // no route to move

	for host, want := range map[string]int{
		"old.example.test":   http.StatusNotFound,
		"new.example.test":   http.StatusOK,
		"other.example.test": http.StatusNotFound,
	} {
		rec := httptest.NewRecorder()
		p.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "http://"+host+"/", nil))
		if rec.Code != want {
			t.Errorf("%s status = %d, want %d", host, rec.Code, want)
		}
		if want == http.StatusOK && rec.Body.String() != host {
			t.Errorf("%s reached the upstream as Host %q, want its own", host, rec.Body.String())
		}
	}
}

func TestRemoveRoutesToRemovesEveryHostOfThatUpstreamOnly(t *testing.T) {
	backend := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	defer backend.Close()
	other := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	defer other.Close()

	p := proxy.New(slog.New(slog.DiscardHandler))
	p.SetRoute("old.example.test", strings.TrimPrefix(backend.URL, "http://"))
	p.SetRoute("new.example.test", strings.TrimPrefix(backend.URL, "http://"))
	p.SetRoute("other.example.test", strings.TrimPrefix(other.URL, "http://"))

	p.RemoveRoutesTo(strings.TrimPrefix(backend.URL, "http://"))

	for host, want := range map[string]int{
		"old.example.test":   http.StatusNotFound,
		"new.example.test":   http.StatusNotFound,
		"other.example.test": http.StatusOK,
	} {
		rec := httptest.NewRecorder()
		p.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "http://"+host+"/", nil))
		if rec.Code != want {
			t.Errorf("%s status = %d, want %d", host, rec.Code, want)
		}
	}
}
