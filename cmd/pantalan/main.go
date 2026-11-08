// Command pantalan is the control plane: dashboard, REST API, deploy workers
// and the reverse proxy, all in one process, plus the CLI that operates it.
package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/pantalan/pantalan/internal/config"
)

// Stdio is the standard streams a command reads from and writes to.
type Stdio struct {
	In       io.Reader
	Out, Err io.Writer
}

// Node is where pantalan finds the Node's outside world. main passes the
// host's real paths; tests point them at fixtures in a temp dir.
type Node struct {
	ProcRoot     string // usually /proc
	DockerSocket string // the Docker Engine API unix socket
	// Statfs reports the host's disk space; nil means syscall.Statfs.
	Statfs func(path string, buf *syscall.Statfs_t) error
	// SampleTicks makes the Node sampler read the host; nil means every
	// nodeinfo.SampleInterval. Tests send on it to take a sample now.
	SampleTicks <-chan time.Time
	// EventsBackoff waits out the delay before Docker's event stream
	// reconnects; nil means time.After. Tests see each delay and fire it.
	EventsBackoff func(time.Duration) <-chan time.Time
	// MaintenanceTicks runs the daily maintenance (TLS renewal check and
	// session pruning); nil means once a day. Tests send on it.
	MaintenanceTicks <-chan time.Time
	// Clock is the time Dashboard sessions and the Docker tab's shared
	// system/df are timed by; nil means time.Now. Tests move it forward
	// instead of waiting.
	Clock func() time.Time
}

// now is the Node's clock.
func (n Node) now() time.Time {
	if n.Clock == nil {
		return time.Now()
	}
	return n.Clock()
}

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	code := run(ctx, os.Args[1:], os.Environ(), Stdio{In: os.Stdin, Out: os.Stdout, Err: os.Stderr}, Node{
		ProcRoot:     "/proc",
		DockerSocket: "/var/run/docker.sock",
		Statfs:       syscall.Statfs,
		Clock:        time.Now,
	})
	stop()
	os.Exit(code)
}

// run is the whole pantalan command: it takes the arguments after the
// program name, the environment as os.Environ formats it, the standard
// streams and the Node's sources, and returns the process exit code.
// Cancelling ctx stops a running service.
func run(ctx context.Context, args, env []string, stdio Stdio, node Node) int {
	if len(args) == 0 {
		return usage(stdio.Err)
	}
	cmd, ok := commands[args[0]]
	if !ok {
		return usage(stdio.Err)
	}
	inv := invocation{args: args[1:], getenv: lookupIn(env), stdio: stdio, node: node}
	if cmd.opensDataDir {
		if err := becomeDataDirOwner(config.DataDir(inv.getenv)); err != nil {
			fmt.Fprintln(stdio.Err, "pantalan:", err)
			return 1
		}
	}
	return cmd.run(ctx, inv)
}

// commands are the subcommands, by name.
var commands = map[string]command{
	"admin":   {run: adminCmd}, // opens the data dir itself, except for `create --check`
	"serve":   {opensDataDir: true, run: serveCmd},
	"tls":     {opensDataDir: true, run: tlsCmd},
	"version": {run: versionCmd},
}

type command struct {
	// opensDataDir makes run switch to the data directory owner first when
	// running as root (ADR 0001). Set it on every command that touches
	// $PANTALAN_DATA_DIR. A command that opens it only sometimes calls
	// becomeDataDirOwner itself, just before it does.
	opensDataDir bool
	run          func(context.Context, invocation) int
}

// invocation is what a subcommand gets to work with.
type invocation struct {
	args   []string // after the subcommand's name
	getenv func(string) string
	stdio  Stdio
	node   Node
}

// lookupIn turns an os.Environ-style list into an os.Getenv-style lookup.
// A later entry for the same key wins, as it does for a process.
func lookupIn(env []string) func(string) string {
	vars := make(map[string]string, len(env))
	for _, kv := range env {
		if k, v, ok := strings.Cut(kv, "="); ok {
			vars[k] = v
		}
	}
	return func(key string) string { return vars[key] }
}

const usageText = `Usage: pantalan <command>

Commands:
  admin     create the Admin, show its username or reset its password
  serve     run the Pantalan service (what the systemd unit runs)
  tls       show or replace the Dashboard's TLS certificate
  version   print the version
`

// usage refuses to guess: a bare or mistyped command must never start a
// second control plane, least of all from a stray `sudo pantalan`.
func usage(w io.Writer) int {
	io.WriteString(w, usageText)
	return 2
}
