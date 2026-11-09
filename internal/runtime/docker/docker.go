// Package docker runs app containers through the docker CLI.
//
// The CLI keeps the binary dependency-free and matches what an operator would
// type when debugging. Container lifecycle stays here in v1; the Node
// overview's reads (stats, events, disk usage) go through the Engine API in
// internal/nodeinfo instead.
package docker

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"os"
	"os/exec"
	"slices"
	"strings"

	"github.com/pantalan/pantalan/internal/execx"
	"github.com/pantalan/pantalan/internal/service"
)

type Runtime struct{ bin string }

func New() *Runtime { return &Runtime{bin: "docker"} }

// Start runs the container with its port published on an ephemeral loopback
// port, which becomes the proxy's upstream.
func (r *Runtime) Start(ctx context.Context, spec service.ContainerSpec) (service.Container, error) {
	args := []string{
		"run", "--detach",
		"--name", spec.Name,
		"--restart", "unless-stopped",
		"--publish", fmt.Sprintf("127.0.0.1::%d", spec.Port),
	}
	for _, k := range slices.Sorted(maps.Keys(spec.Labels)) {
		args = append(args, "--label", k+"="+spec.Labels[k])
	}
	// Only variable names go on the command line; docker copies the values
	// from its own environment, so secrets never appear in `ps` output.
	env := os.Environ()
	for _, k := range slices.Sorted(maps.Keys(spec.Env)) {
		args = append(args, "--env", k)
		env = append(env, k+"="+spec.Env[k])
	}
	args = append(args, spec.Image)

	cmd := exec.CommandContext(ctx, r.bin, args...)
	cmd.Env = env
	id, err := execx.Output(cmd)
	if err != nil {
		return service.Container{}, err
	}

	upstream, err := r.Upstream(ctx, id, spec.Port)
	if err != nil {
		if rmErr := r.Remove(context.WithoutCancel(ctx), id); rmErr != nil {
			err = errors.Join(err, rmErr)
		}
		return service.Container{}, err
	}
	return service.Container{ID: id, Upstream: upstream}, nil
}

// Upstream asks Docker where the container's port is published now. Docker
// picks a new ephemeral port whenever it restarts the container.
func (r *Runtime) Upstream(ctx context.Context, containerID string, port int) (string, error) {
	out, err := execx.Output(exec.CommandContext(ctx, r.bin, "port", containerID, fmt.Sprintf("%d/tcp", port)))
	if err != nil {
		return "", fmt.Errorf("look up published port: %w", err)
	}
	// `docker port` prints one line per address family; the first is IPv4.
	upstream, _, _ := strings.Cut(out, "\n")
	if upstream = strings.TrimSpace(upstream); upstream == "" {
		return "", fmt.Errorf("container %s publishes no %d/tcp", containerID, port)
	}
	return upstream, nil
}

func (r *Runtime) Remove(ctx context.Context, containerID string) error {
	_, err := execx.Output(exec.CommandContext(ctx, r.bin, "rm", "--force", containerID))
	return err
}

// RemoveLabelled force-removes the containers labelled key=value, running
// or not.
func (r *Runtime) RemoveLabelled(ctx context.Context, key, value string) error {
	out, err := execx.Output(exec.CommandContext(ctx, r.bin, "ps", "--all", "--quiet", "--no-trunc", "--filter", "label="+key+"="+value))
	if err != nil {
		return fmt.Errorf("list containers: %w", err)
	}
	ids := strings.Fields(out)
	if len(ids) == 0 {
		return nil
	}
	_, err = execx.Output(exec.CommandContext(ctx, r.bin, append([]string{"rm", "--force"}, ids...)...))
	return err
}

// RemoveImages removes repository's tags by name, so an image that is
// also tagged as something else keeps that tag.
func (r *Runtime) RemoveImages(ctx context.Context, repository string) error {
	out, err := execx.Output(exec.CommandContext(ctx, r.bin, "image", "ls", "--filter", "reference="+repository, "--format", "{{.Repository}}:{{.Tag}}"))
	if err != nil {
		return fmt.Errorf("list images: %w", err)
	}
	tags := strings.Fields(out)
	if len(tags) == 0 {
		return nil
	}
	_, err = execx.Output(exec.CommandContext(ctx, r.bin, append([]string{"image", "rm"}, tags...)...))
	return err
}
