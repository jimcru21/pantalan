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
		items[i] = AppListItem{App: appOf(app)}
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
	h.writeJSON(w, http.StatusOK, appOf(app))
}

// deleteApp removes the App, what it left on the Node and its
// Deployments. It is refused with a 409 while a Deployment of it runs.
func (h *handler) deleteApp(w http.ResponseWriter, r *http.Request) {
	if err := h.deps.Deployer.DeleteApp(r.Context(), r.PathValue("id")); err != nil {
		h.writeError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
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
