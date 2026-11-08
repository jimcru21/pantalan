package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"path/filepath"
	"time"

	"github.com/pantalan/pantalan/internal/config"
	"github.com/pantalan/pantalan/internal/dashtls"
)

// tlsDir is where the Dashboard's certificate lives under the data dir.
func tlsDir(dataDir string) string {
	return filepath.Join(dataDir, "tls")
}

// renewCertificate replaces the Dashboard certificate in service when it
// nears expiry. A failure keeps the current one serving and is logged, so
// a full disk doesn't take the Dashboard down with it.
func renewCertificate(log *slog.Logger, certs *dashtls.Manager) {
	renewed, err := certs.RenewIfExpiring(time.Now())
	if err != nil {
		log.Error("tls certificate renewal failed", "err", err, "expires", certs.Current().Leaf().NotAfter)
		return
	}
	if renewed {
		c := certs.Current()
		log.Info("tls certificate renewed", "fingerprint", c.Fingerprint(), "expires", c.Leaf().NotAfter)
	}
}

// tlsCmd is `pantalan tls fingerprint|regenerate`.
func tlsCmd(_ context.Context, inv invocation) int {
	if len(inv.args) != 1 {
		return tlsUsage(inv)
	}
	dir := tlsDir(config.DataDir(inv.getenv))
	switch inv.args[0] {
	case "fingerprint":
		c, err := dashtls.Load(dir)
		if errors.Is(err, dashtls.ErrNoCertificate) {
			fmt.Fprintln(inv.stdio.Err, "pantalan: no TLS certificate yet; the service creates one when it starts")
			return 1
		}
		if err != nil {
			fmt.Fprintln(inv.stdio.Err, "pantalan:", err)
			return 1
		}
		fmt.Fprintln(inv.stdio.Out, c.Fingerprint())
		return 0
	case "regenerate":
		c, err := dashtls.Generate(dir, time.Now())
		if err != nil {
			fmt.Fprintln(inv.stdio.Err, "pantalan:", err)
			return 1
		}
		fmt.Fprintln(inv.stdio.Out, c.Fingerprint())
		// The service reads the files only at startup and renewal; there
		// is no file watching.
		fmt.Fprintln(inv.stdio.Err, "New certificate written. Restart the service to serve it: sudo systemctl restart pantalan")
		return 0
	default:
		return tlsUsage(inv)
	}
}

func tlsUsage(inv invocation) int {
	fmt.Fprint(inv.stdio.Err, `Usage: pantalan tls <command>

Commands:
  fingerprint   print the Dashboard certificate's SHA-256 fingerprint
  regenerate    replace the Dashboard certificate with a new one
`)
	return 2
}
