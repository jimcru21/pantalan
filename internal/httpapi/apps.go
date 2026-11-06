package httpapi

import (
	"errors"
	"io"
	"net/http"

	"github.com/pantalan/pantalan/internal/domain"
)

type createAppRequest struct {
	Name      string            `json:"name"`
	RepoURL   string            `json:"repo_url"`
	Branch    string            `json:"branch"`
	BuildType domain.BuildType  `json:"build_type"`
	Domain    string            `json:"domain"`
	Port      int               `json:"port"`
	Env       map[string]string `json:"env"`
}

func (h *handler) createApp(w http.ResponseWriter, r *http.Request) {
	var req createAppRequest
	if err := decodeJSON(w, r, &req); err != nil {
		h.writeError(w, err)
		return
	}
	app, err := h.deps.Apps.Create(r.Context(), domain.App{
		Name:      req.Name,
		RepoURL:   req.RepoURL,
		Branch:    req.Branch,
		BuildType: req.BuildType,
		Domain:    req.Domain,
		Port:      req.Port,
		Env:       req.Env,
	})
	if err != nil {
		h.writeError(w, err)
		return
	}
	h.writeJSON(w, http.StatusCreated, app)
}

func (h *handler) listApps(w http.ResponseWriter, r *http.Request) {
	apps, err := h.deps.Apps.List(r.Context())
	if err != nil {
		h.writeError(w, err)
		return
	}
	h.writeJSON(w, http.StatusOK, apps)
}

func (h *handler) getApp(w http.ResponseWriter, r *http.Request) {
	app, err := h.deps.Apps.Get(r.Context(), r.PathValue("id"))
	if err != nil {
		h.writeError(w, err)
		return
	}
	h.writeJSON(w, http.StatusOK, app)
}

func (h *handler) listDeployments(w http.ResponseWriter, r *http.Request) {
	deps, err := h.deps.Deployer.List(r.Context(), r.PathValue("id"))
	if err != nil {
		h.writeError(w, err)
		return
	}
	h.writeJSON(w, http.StatusOK, deps)
}

// deploy queues a deployment. The body is optional: {"ref": "<branch|tag|sha>"}.
func (h *handler) deploy(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Ref string `json:"ref"`
	}
	if err := decodeJSON(w, r, &req); err != nil && !errors.Is(err, io.EOF) {
		h.writeError(w, err)
		return
	}
	dep, err := h.deps.Deployer.Start(r.Context(), r.PathValue("id"), req.Ref)
	if err != nil {
		h.writeError(w, err)
		return
	}
	h.writeJSON(w, http.StatusAccepted, dep)
}

// TODO: stream with Server-Sent Events while the deployment is running.
func (h *handler) deploymentLogs(w http.ResponseWriter, r *http.Request) {
	f, err := h.deps.Deployer.OpenLog(r.Context(), r.PathValue("id"))
	if err != nil {
		h.writeError(w, err)
		return
	}
	defer func() {
		if err := f.Close(); err != nil {
			h.deps.Log.Warn("close log", "err", err)
		}
	}()
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	if _, err := io.Copy(w, f); err != nil {
		h.deps.Log.Warn("send log", "err", err)
	}
}
