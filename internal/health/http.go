// Package health decides when a freshly started container can take traffic.
package health

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"time"
)

type HTTP struct {
	Path     string
	Timeout  time.Duration // total time to wait for the app to come up
	Interval time.Duration
	Client   *http.Client
}

func NewHTTP() HTTP {
	return HTTP{
		Path:     "/",
		Timeout:  60 * time.Second,
		Interval: time.Second,
		Client:   &http.Client{Timeout: 5 * time.Second},
	}
}

// Wait polls upstream until it answers with a status below 500. A 404 counts
// as healthy: many apps have no route at "/", but answering at all means the
// process is up and listening.
func (h HTTP) Wait(ctx context.Context, upstream string) error {
	ctx, cancel := context.WithTimeout(ctx, h.Timeout)
	defer cancel()
	ticker := time.NewTicker(h.Interval)
	defer ticker.Stop()

	url := "http://" + upstream + h.Path
	var lastErr error
	for {
		lastErr = h.probe(ctx, url)
		if lastErr == nil {
			return nil
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("%s not healthy after %s: %w", upstream, h.Timeout, lastErr)
		case <-ticker.C:
		}
	}
}

func (h HTTP) probe(ctx context.Context, url string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	resp, err := h.Client.Do(req)
	if err != nil {
		return err
	}
	closeErr := resp.Body.Close()
	if resp.StatusCode >= 500 {
		return errors.Join(fmt.Errorf("status %d", resp.StatusCode), closeErr)
	}
	return nil
}
