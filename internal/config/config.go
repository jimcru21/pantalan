// Package config loads settings from PANTALAN_* environment variables.
//
// The environment is passed in rather than read from the process, so the
// entrypoint can be driven in-process with an environment of its own.
package config

import (
	"errors"
	"fmt"
	"strconv"
)

type Config struct {
	APIAddr       string // dashboard + REST API
	ProxyAddr     string // public traffic to deployed apps
	DataDir       string
	AdminToken    string
	WebhookSecret string // empty disables /webhooks/github
	Workers       int    // concurrent deployments
}

// Load reads the service's settings through getenv, which returns "" for an
// unset variable, like os.Getenv.
func Load(getenv func(string) string) (Config, error) {
	cfg := Config{
		APIAddr:       or(getenv("PANTALAN_API_ADDR"), ":3000"),
		ProxyAddr:     or(getenv("PANTALAN_PROXY_ADDR"), ":80"),
		DataDir:       DataDir(getenv),
		AdminToken:    getenv("PANTALAN_ADMIN_TOKEN"),
		WebhookSecret: getenv("PANTALAN_WEBHOOK_SECRET"),
		Workers:       2,
	}
	if v := getenv("PANTALAN_WORKERS"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 {
			return Config{}, fmt.Errorf("PANTALAN_WORKERS must be a positive integer, got %q", v)
		}
		cfg.Workers = n
	}
	if cfg.AdminToken == "" {
		return Config{}, errors.New("PANTALAN_ADMIN_TOKEN is required")
	}
	return cfg, nil
}

// DataDir is the data directory alone, for commands that open it without
// needing the rest of the service's settings (such as the Admin Token).
func DataDir(getenv func(string) string) string {
	return or(getenv("PANTALAN_DATA_DIR"), "/var/lib/pantalan")
}

func or(v, fallback string) string {
	if v != "" {
		return v
	}
	return fallback
}
