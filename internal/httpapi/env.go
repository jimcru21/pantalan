package httpapi

import (
	"net/http"

	"github.com/pantalan/pantalan/internal/domain"
)

// AppEnv is GET and PUT /api/v1/apps/{id}/env: an App's env vars as
// dotenv text, one KEY=value per line. A PUT may hold blank lines and "#"
// comments, which aren't kept; the text read back is the pairs alone,
// sorted by name. This is the only response that carries env values.
type AppEnv struct {
	Env string `json:"env"`
}

func (h *handler) getEnv(w http.ResponseWriter, r *http.Request) {
	app, err := h.deps.Apps.Get(r.Context(), r.PathValue("id"))
	if err != nil {
		h.writeError(w, err)
		return
	}
	h.writeJSON(w, http.StatusOK, AppEnv{Env: domain.FormatEnv(app.Env)})
}

// putEnv replaces an App's env vars. It never starts a Deployment.
func (h *handler) putEnv(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Env *string `json:"env"` // required: a body without it must not clear them
	}
	if err := decodeJSON(w, r, &req); err != nil {
		h.writeError(w, err)
		return
	}
	if req.Env == nil {
		h.writeError(w, domain.InvalidField("env", "env is required"))
		return
	}
	env, err := domain.ParseEnv(*req.Env)
	if err != nil {
		h.writeError(w, err)
		return
	}
	id := r.PathValue("id")
	if err := h.deps.Apps.SetEnv(r.Context(), id, env); err != nil {
		h.writeError(w, err)
		return
	}
	h.writeJSON(w, http.StatusOK, AppEnv{Env: domain.FormatEnv(env)})
}
