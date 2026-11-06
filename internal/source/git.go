// Package source fetches application source code.
package source

import (
	"context"
	"os"
	"os/exec"

	"github.com/pantalan/pantalan/internal/execx"
)

// Git fetches a single revision with the git CLI.
//
// TODO: private repositories — per-app deploy keys via GIT_SSH_COMMAND.
type Git struct{}

// Fetch uses init+fetch rather than clone because clone --branch cannot
// check out a bare commit SHA, which is what push webhooks send.
func (Git) Fetch(ctx context.Context, repoURL, ref, dir string) (string, error) {
	steps := [][]string{
		{"init", "--quiet", dir},
		{"-C", dir, "fetch", "--quiet", "--depth", "1", repoURL, ref},
		{"-C", dir, "checkout", "--quiet", "FETCH_HEAD"},
	}
	for _, args := range steps {
		if _, err := execx.Output(git(ctx, args...)); err != nil {
			return "", err
		}
	}
	return execx.Output(git(ctx, "-C", dir, "rev-parse", "HEAD"))
}

func git(ctx context.Context, args ...string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, "git", args...)
	// Fail instead of hanging forever on a credentials prompt.
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
	return cmd
}
