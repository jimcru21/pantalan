// Package httpapi exposes the REST API, the GitHub webhook and /healthz on
// the Dashboard listener, and mounts the Dashboard's own handler next to
// them. /api/v1 accepts either the Admin Token or the Dashboard's session
// cookie (ADR 0003), so the Dashboard and automation share one API.
package httpapi

import (
	"log/slog"
	"net/http"
	"time"

	"github.com/pantalan/pantalan/internal/service"
)

type Deps struct {
	Apps     *service.AppService
	Deployer *service.Deployer
	Sessions Sessions // signs in and out, and checks the session cookie on /api/v1
	Hostname string   // the Node's, for the sign-in screen
	// Host, Docker and Events are what the Node overview reads; nil
	// shows that source as unavailable.
	Host   NodeHost
	Docker NodeDocker
	Events NodeEvents
	// Now is the clock disk usage is shared by; nil means time.Now.
	Now           func() time.Time
	AdminToken    string
	WebhookSecret string
	Dashboard     http.Handler // everything outside /api, /webhooks and /healthz
	Log           *slog.Logger
}

type handler struct {
	deps      Deps
	diskUsage sharedDiskUsage
}

func NewHandler(deps Deps) http.Handler {
	if deps.Now == nil {
		deps.Now = time.Now
	}
	h := &handler{deps: deps, diskUsage: sharedDiskUsage{now: deps.Now}}

	api := http.NewServeMux()
	api.HandleFunc("GET /api/v1/apps", h.listApps)
	api.HandleFunc("POST /api/v1/apps", h.createApp)
	api.HandleFunc("GET /api/v1/apps/{id}", h.getApp)
	api.HandleFunc("GET /api/v1/apps/{id}/deployments", h.listDeployments)
	api.HandleFunc("POST /api/v1/apps/{id}/deployments", h.deploy)
	api.HandleFunc("GET /api/v1/deployments/{id}/logs", h.deploymentLogs)
	api.HandleFunc("GET /api/v1/node", h.node)
	api.HandleFunc("GET /api/v1/node/containers", h.nodeContainers)
	api.HandleFunc("GET /api/v1/node/images", h.nodeImages)
	api.HandleFunc("GET /api/v1/node/docker", h.nodeDocker)
	api.HandleFunc("GET /api/v1/node/events", h.nodeEvents)

	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
	// Signing in takes no credential. The session routes set and clear the
	// cookie, so only a same-origin request may call them, whatever
	// credential it carries.
	sameOrigin := http.NewCrossOriginProtection()
	mux.HandleFunc("GET /api/v1/login-info", h.loginInfo)
	mux.Handle("POST /api/v1/sessions", sameOrigin.Handler(http.HandlerFunc(h.signIn)))
	mux.Handle("DELETE /api/v1/sessions/current", sameOrigin.Handler(http.HandlerFunc(h.signOut)))
	// Everything else under /api: the Admin Token or a live session cookie.
	mux.Handle("/api/", requireCredential(deps.AdminToken, deps.Sessions, deps.Log, api))
	// Authenticated by HMAC signature, not the admin token.
	mux.HandleFunc("POST /webhooks/github", h.githubWebhook)
	// No other path under /webhooks/ is a Dashboard page.
	mux.Handle("/webhooks/", http.NotFoundHandler())
	// Every other path is the Dashboard's: a page, or a hashed asset.
	mux.Handle("/", deps.Dashboard)

	return logRequests(deps.Log, noStoreAPI(mux))
}
