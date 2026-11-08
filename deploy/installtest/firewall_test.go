//go:build installtest

package installtest

import (
	"strings"
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

func TestActiveUfwOnlyGetsRulesAdded(t *testing.T) {
	t.Parallel()
	forEachDistro(t, func(t *testing.T, d Distro) {
		n := substrate.Boot(t, d, UFW)
		// The operator's own firewall: a non-default policy, an app-profile
		// SSH rule, their own rule for 443 and rules Pantalan has no
		// business with.
		sh(t, n, `ufw default reject incoming
			ufw allow OpenSSH
			ufw allow 443/tcp comment web
			ufw deny 3306/tcp
			ufw allow from 10.0.0.0/8 to any port 9000 proto tcp
			ufw --force enable`)
		rulesBefore, policyBefore := operatorRules(t, n), ufwPolicy(t, n)

		r := installer{}.run(t, n, "--admin-user", "alice")
		if r.Code != 0 {
			t.Fatalf("install exited %d", r.Code)
		}
		s := parseSummary(t, r.Output())
		assertFirewallLine(t, r.Output(), "ufw already active: added 80, 8443 and SSH 22 (443 was already allowed); your existing rules are unchanged")
		if got := operatorRules(t, n); got != rulesBefore {
			t.Errorf("the operator's rules changed:\nbefore:\n%s\nafter:\n%s", rulesBefore, got)
		}
		if got := ufwPolicy(t, n); got != policyBefore {
			t.Errorf("ufw's default policy changed from %q to %q", policyBefore, got)
		}
		if strings.Contains(r.Output(), "default incoming policy") {
			t.Errorf("the installer warned about a reject incoming policy, which is fine")
		}
		want := installed{
			Version:       tag,
			DashboardPort: 8443,
			Fingerprint:   s.Fingerprint,
			AdminUser:     "alice",
			AdminPassword: s.AdminPassword,
			Firewall:      firewall{Pantalan: []int{80, 8443}},
		}
		assertInstalled(t, n, want)

		// An incoming policy that lets everything in gets a warning, and is
		// still the operator's to change.
		sh(t, n, "ufw default allow incoming")
		r = installer{}.run(t, n)
		if r.Code != 0 {
			t.Fatalf("re-run exited %d", r.Code)
		}
		assertFirewallLine(t, r.Output(), "ufw active: allows SSH 22, 80, 443, 8443; unchanged")
		if !strings.Contains(r.Output(), "default incoming policy is allow") {
			t.Errorf("the installer didn't warn about the allow incoming policy")
		}
		if got := ufwPolicy(t, n); !strings.Contains(got, "allow (incoming)") {
			t.Errorf("ufw's default policy is %q, want the operator's allow (incoming) kept", got)
		}
		if got := operatorRules(t, n); got != rulesBefore {
			t.Errorf("the operator's rules changed on the re-run:\nbefore:\n%s\nafter:\n%s", rulesBefore, got)
		}
	})
}

// ufwPolicy is ufw's "Default: ..." line.
func ufwPolicy(t *testing.T, n Node) string {
	t.Helper()
	return sh(t, n, "ufw status verbose | grep '^Default:'")
}

// assertEnvFirewall: the env file stores PANTALAN_FIREWALL=mode, once.
func assertEnvFirewall(t *testing.T, n Node, mode string) {
	t.Helper()
	if got := sh(t, n, "grep '^PANTALAN_FIREWALL=' /etc/pantalan/pantalan.env || :"); got != "PANTALAN_FIREWALL="+mode {
		t.Errorf("the env file stores %q, want PANTALAN_FIREWALL=%s", got, mode)
	}
}
