// Package config loads settings from PANTALAN_* environment variables.
package config

import (
	"errors"
	"fmt"
	"os"
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

func Load() (Config, error) {
	cfg := Config{
		APIAddr:       getenv("PANTALAN_API_ADDR", ":3000"),
		ProxyAddr:     getenv("PANTALAN_PROXY_ADDR", ":80"),
		DataDir:       getenv("PANTALAN_DATA_DIR", "/var/lib/pantalan"),
		AdminToken:    os.Getenv("PANTALAN_ADMIN_TOKEN"),
		WebhookSecret: os.Getenv("PANTALAN_WEBHOOK_SECRET"),
		Workers:       2,
	}
	if v := os.Getenv("PANTALAN_WORKERS"); v != "" {
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

func getenv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
