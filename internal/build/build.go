// Package build turns a source checkout into a container image.
package build

import (
	"context"
	"errors"
	"fmt"
	"os/exec"

	"github.com/pantalan/pantalan/internal/execx"
	"github.com/pantalan/pantalan/internal/service"
)

// Dockerfile builds with the Dockerfile at the root of the repository.
type Dockerfile struct{}

func (Dockerfile) Build(ctx context.Context, req service.BuildRequest) error {
	return execx.Stream(exec.CommandContext(ctx, "docker", "build", "--tag", req.Image, req.Dir), req.Logs)
}

// ErrNoNixpacks fails a nixpacks build on a Node without the nixpacks
// binary. The installer never installs it: v1 Nodes build from
// Dockerfiles only.
var ErrNoNixpacks = errors.New("nixpacks not installed on this Node")

// Nixpacks detects the language and builds without a Dockerfile. It needs
// the nixpacks binary on the PATH, which only a hand-built setup has.
type Nixpacks struct{}

func (Nixpacks) Build(ctx context.Context, req service.BuildRequest) error {
	if _, err := exec.LookPath("nixpacks"); err != nil {
		fmt.Fprintln(req.Logs, ErrNoNixpacks)
		return ErrNoNixpacks
	}
	return execx.Stream(exec.CommandContext(ctx, "nixpacks", "build", req.Dir, "--name", req.Image), req.Logs)
}
