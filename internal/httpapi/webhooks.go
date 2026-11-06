package httpapi

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/pantalan/pantalan/internal/domain"
)

type githubPush struct {
	Ref        string `json:"ref"`
	After      string `json:"after"`
	Deleted    bool   `json:"deleted"`
	Repository struct {
		CloneURL string `json:"clone_url"`
	} `json:"repository"`
}

// githubWebhook deploys every app tracking the pushed branch.
// TODO: per-app secrets, plus GitLab/Gitea/Forgejo payloads.
func (h *handler) githubWebhook(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxBodyBytes))
	if err != nil {
		h.writeError(w, fmt.Errorf("%w: %w", domain.ErrInvalid, err))
		return
	}
	if !validSignature(h.deps.WebhookSecret, body, r.Header.Get("X-Hub-Signature-256")) {
		http.Error(w, "invalid signature", http.StatusUnauthorized)
		return
	}
	if r.Header.Get("X-GitHub-Event") != "push" {
		w.WriteHeader(http.StatusNoContent) // ping and events we don't handle
		return
	}

	var push githubPush
	if err := json.Unmarshal(body, &push); err != nil {
		h.writeError(w, fmt.Errorf("%w: %w", domain.ErrInvalid, err))
		return
	}
	branch, isBranch := strings.CutPrefix(push.Ref, "refs/heads/")
	if !isBranch || push.Deleted {
		w.WriteHeader(http.StatusNoContent)
		return
	}

	apps, err := h.deps.Apps.FindByRepo(r.Context(), push.Repository.CloneURL, branch)
	if err != nil {
		h.writeError(w, err)
		return
	}
	ids := make([]string, 0, len(apps))
	for _, app := range apps {
		dep, err := h.deps.Deployer.Start(r.Context(), app.ID, push.After)
		if err != nil {
			h.writeError(w, err)
			return
		}
		ids = append(ids, dep.ID)
	}
	h.writeJSON(w, http.StatusAccepted, map[string][]string{"deployments": ids})
}

// validSignature checks GitHub's "sha256=<hex hmac>" header. An empty secret
// rejects everything, so webhooks are off until one is configured.
func validSignature(secret string, body []byte, header string) bool {
	if secret == "" {
		return false
	}
	sig, ok := strings.CutPrefix(header, "sha256=")
	if !ok {
		return false
	}
	got, err := hex.DecodeString(sig)
	if err != nil {
		return false
	}
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	return hmac.Equal(got, mac.Sum(nil))
}
