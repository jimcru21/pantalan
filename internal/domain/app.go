// Package domain holds Pantalan's core entities and rules. It imports nothing
// from the rest of the module, so every other layer can depend on it.
package domain

import (
	"fmt"
	"strings"
	"time"
)

// BuildType selects how an app's source is turned into a container image.
type BuildType string

const (
	BuildDockerfile BuildType = "dockerfile"
	BuildNixpacks   BuildType = "nixpacks"
)

// App is a deployable service: one git repository served on one domain.
type App struct {
	ID        string            `json:"id"`
	Name      string            `json:"name"`
	RepoURL   string            `json:"repo_url"`
	Branch    string            `json:"branch"`
	BuildType BuildType         `json:"build_type"`
	Domain    string            `json:"domain"`
	Port      int               `json:"port"` // port the app listens on inside its container
	Env       map[string]string `json:"env,omitempty"`
	CreatedAt time.Time         `json:"created_at"`
}

// Validate checks user-supplied fields. Values that become command-line
// arguments must not start with "-", or git and docker would read them as flags.
func (a App) Validate() error {
	switch {
	case strings.TrimSpace(a.Name) == "":
		return invalid("name is required")
	case a.RepoURL == "" || strings.HasPrefix(a.RepoURL, "-"):
		return invalid("repo_url must be a git URL")
	case a.Branch == "" || strings.HasPrefix(a.Branch, "-"):
		return invalid("branch is invalid")
	case a.Domain == "" || strings.ContainsAny(a.Domain, "/: "):
		return invalid("domain must be a bare hostname")
	case a.Port < 1 || a.Port > 65535:
		return invalid("port must be between 1 and 65535")
	}
	for k := range a.Env {
		if k == "" || strings.HasPrefix(k, "-") || strings.Contains(k, "=") {
			return invalid(fmt.Sprintf("env name %q is invalid", k))
		}
	}
	switch a.BuildType {
	case BuildDockerfile, BuildNixpacks:
		return nil
	default:
		return invalid(fmt.Sprintf("unknown build_type %q", a.BuildType))
	}
}

// SameRepo reports whether two git URLs name the same repository, ignoring
// case, a trailing slash and a ".git" suffix.
func SameRepo(a, b string) bool { return normalizeRepo(a) == normalizeRepo(b) }

func normalizeRepo(u string) string {
	u = strings.ToLower(strings.TrimSuffix(u, "/"))
	return strings.TrimSuffix(u, ".git")
}
