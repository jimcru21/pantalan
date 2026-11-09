// Package httpapi exposes the REST API, the GitHub webhook and /healthz on
// the Dashboard listener, and mounts the Dashboard's own handler next to
// them. /api/v1 accepts either the Admin Token or the Dashboard's session
// cookie (ADR 0003), so the Dashboard and automation share one API.
package httpapi

import (
	"log/slog"
	"net/http"

	"github.com/pantalan/pantalan/internal/service"
)

type Deps struct {
	Apps          *service.AppService
	Deployer      *service.Deployer
	Sessions      Sessions // signs in and out, and checks the session cookie on /api/v1
	Hostname      string   // the Node's, for the sign-in screen
	AdminToken    string
	WebhookSecret string
	Dashboard     http.Handler // everything outside /api, /webhooks and /healthz
	Log           *slog.Logger
}

type handler struct{ deps Deps }

func NewHandler(deps Deps) http.Handler {
	h := &handler{deps: deps}

	api := http.NewServeMux()
	api.HandleFunc("GET /api/v1/apps", h.listApps)
	api.HandleFunc("POST /api/v1/apps", h.createApp)
	api.HandleFunc("GET /api/v1/apps/{id}", h.getApp)
	api.HandleFunc("GET /api/v1/apps/{id}/deployments", h.listDeployments)
	api.HandleFunc("POST /api/v1/apps/{id}/deployments", h.deploy)
	api.HandleFunc("GET /api/v1/deployments/{id}/logs", h.deploymentLogs)

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
	mux.Handle("/", deps.Dashboard)

	return logRequests(deps.Log, mux)
}
