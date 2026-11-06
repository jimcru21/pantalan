// Package build turns a source checkout into a container image.
package build

import (
	"context"
	"os/exec"

	"github.com/pantalan/pantalan/internal/execx"
	"github.com/pantalan/pantalan/internal/service"
)

// Dockerfile builds with the Dockerfile at the root of the repository.
type Dockerfile struct{}

func (Dockerfile) Build(ctx context.Context, req service.BuildRequest) error {
	return execx.Stream(exec.CommandContext(ctx, "docker", "build", "--tag", req.Image, req.Dir), req.Logs)
}

// Nixpacks detects the language and builds without a Dockerfile.
// Requires the nixpacks binary on the host.
type Nixpacks struct{}

func (Nixpacks) Build(ctx context.Context, req service.BuildRequest) error {
	return execx.Stream(exec.CommandContext(ctx, "nixpacks", "build", req.Dir, "--name", req.Image), req.Logs)
}
