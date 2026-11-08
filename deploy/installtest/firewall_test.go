//go:build installtest

package installtest

import (
	"testing"
)

// The firewall paths. Fresh installs from Bare (ufw inactive, so enabled)
// are covered by TestFreshInstallFromBareWithNoTTY through assertInstalled.

func TestNoFirewallIsStoredAndKeptOnReRuns(t *testing.T) {
	t.Parallel()
	forEachDistro(t, func(t *testing.T, d Distro) {
		n := substrate.Boot(t, d, Bare)

		r := installer{}.run(t, n, "--admin-user", "alice", "--no-firewall")
		if r.Code != 0 {
			t.Fatalf("install --no-firewall exited %d", r.Code)
		}
		s := parseSummary(t, r.Output())
		assertFirewallLine(t, r.Output(), "off (PANTALAN_FIREWALL=off)")
		assertEnvFirewall(t, n, "off")

		// A re-run without the flag keeps what's stored.
		r = installer{}.run(t, n)
		if r.Code != 0 {
			t.Fatalf("re-run exited %d", r.Code)
		}
		assertFirewallLine(t, r.Output(), "off (PANTALAN_FIREWALL=off)")
		assertInstalled(t, n, installed{
			Version:       tag,
			DashboardPort: 8443,
			Fingerprint:   s.Fingerprint,
			AdminUser:     "alice",
			AdminPassword: s.AdminPassword,
			Firewall:      firewall{State: noUfw},
		})
		assertEnvFirewall(t, n, "off")
	})
}

// assertEnvFirewall: the env file stores PANTALAN_FIREWALL=mode, once.
func assertEnvFirewall(t *testing.T, n Node, mode string) {
	t.Helper()
	if got := sh(t, n, "grep '^PANTALAN_FIREWALL=' /etc/pantalan/pantalan.env || :"); got != "PANTALAN_FIREWALL="+mode {
		t.Errorf("the env file stores %q, want PANTALAN_FIREWALL=%s", got, mode)
	}
}
