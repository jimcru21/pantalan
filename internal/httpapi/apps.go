package httpapi

import (
	"errors"
	"io"
	"net/http"
)

func (h *handler) listApps(w http.ResponseWriter, r *http.Request) {
	apps, err := h.deps.Apps.List(r.Context())
	if err != nil {
		h.writeError(w, err)
		return
	}
	latest, err := h.deps.Deployer.Latest(r.Context())
	if err != nil {
		h.writeError(w, err)
		return
	}
	items := make([]AppListItem, len(apps))
	for i, app := range apps {
		items[i] = AppListItem{App: app}
		if dep, ok := latest[app.ID]; ok {
			items[i].LatestDeployment = &dep
		}
	}
	h.writeJSON(w, http.StatusOK, items)
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

// getDeployment reads one Deployment under its App, so its page and its
// status polling need nothing but the address.
func (h *handler) getDeployment(w http.ResponseWriter, r *http.Request) {
	dep, err := h.deps.Deployer.Get(r.Context(), r.PathValue("id"), r.PathValue("deploymentID"))
	if err != nil {
		h.writeError(w, err)
		return
	}
	h.writeJSON(w, http.StatusOK, dep)
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

// redeploy queues a new Deployment of an earlier Deployment's commit: a
// rollback. It takes no body.
func (h *handler) redeploy(w http.ResponseWriter, r *http.Request) {
	dep, err := h.deps.Deployer.Redeploy(r.Context(), r.PathValue("id"), r.PathValue("deploymentID"))
	if err != nil {
		h.writeError(w, err)
		return
	}
	h.writeJSON(w, http.StatusAccepted, dep)
}
