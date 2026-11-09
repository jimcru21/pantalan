package httpapi

import (
	"net/http"

	"github.com/pantalan/pantalan/internal/domain"
)

// AppSettings is what an Admin chooses for an App, and the body of
// PUT /api/v1/apps/{id}. Branch defaults to "main" and BuildType to
// "dockerfile". Env vars have their own endpoint, and an App's ID and
// creation time never change.
type AppSettings struct {
	Name      string           `json:"name"`
	RepoURL   string           `json:"repo_url"`
	Branch    string           `json:"branch,omitempty"`
	BuildType domain.BuildType `json:"build_type,omitempty" tstype:"domain.BuildType"`
	Domain    string           `json:"domain"`
	Port      int              `json:"port"`
}

// CreateAppRequest is the body of POST /api/v1/apps: the settings, and
// optionally the App's first env vars.
type CreateAppRequest struct {
	Name      string            `json:"name"`
	RepoURL   string            `json:"repo_url"`
	Branch    string            `json:"branch,omitempty"`
	BuildType domain.BuildType  `json:"build_type,omitempty" tstype:"domain.BuildType"`
	Domain    string            `json:"domain"`
	Port      int               `json:"port"`
	Env       map[string]string `json:"env,omitempty"`
}

func (s AppSettings) app() domain.App {
	return domain.App{
		Name:      s.Name,
		RepoURL:   s.RepoURL,
		Branch:    s.Branch,
		BuildType: s.BuildType,
		Domain:    s.Domain,
		Port:      s.Port,
	}
}

// CreateAppRequest spells the settings out rather than embedding
// AppSettings, so a decode error names "port", not "AppSettings.port".
func (c CreateAppRequest) app() domain.App {
	app := AppSettings{c.Name, c.RepoURL, c.Branch, c.BuildType, c.Domain, c.Port}.app()
	app.Env = c.Env
	return app
}

func (h *handler) createApp(w http.ResponseWriter, r *http.Request) {
	var req CreateAppRequest
	if err := decodeJSON(w, r, &req); err != nil {
		h.writeError(w, err)
		return
	}
	app, err := h.deps.Apps.Create(r.Context(), req.app())
	if err != nil {
		h.writeError(w, err)
		return
	}
	h.writeJSON(w, http.StatusCreated, app)
}

// updateApp changes an App's settings. It never starts a Deployment.
func (h *handler) updateApp(w http.ResponseWriter, r *http.Request) {
	var req AppSettings
	if err := decodeJSON(w, r, &req); err != nil {
		h.writeError(w, err)
		return
	}
	app, err := h.deps.Apps.Update(r.Context(), r.PathValue("id"), req.app())
	if err != nil {
		h.writeError(w, err)
		return
	}
	h.writeJSON(w, http.StatusOK, app)
}
