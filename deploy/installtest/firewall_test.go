//go:build installtest

package installtest

import (
	"slices"
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

		// A re-run without the flag keeps what's stored. The firewall is
		// left alone, but a container published on every address still
		// gets a warning: nothing on the Node filters it.
		runPublishedContainer(t, n)
		r = installer{}.run(t, n)
		if r.Code != 0 {
			t.Fatalf("re-run exited %d", r.Code)
		}
		assertFirewallLine(t, r.Output(), "off (PANTALAN_FIREWALL=off)")
		assertPublishedWarning(t, r, "which Docker forwards ahead of any firewall on this Node")
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
		defaultsBefore := sh(t, n, "cat /etc/default/ufw")

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
		if got := sh(t, n, "cat /etc/default/ufw"); got != defaultsBefore {
			t.Errorf("the installer changed /etc/default/ufw")
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

		// --uninstall takes only Pantalan's rules away; the operator's own
		// 443 rule stays.
		r = installer{}.run(t, n, "--uninstall")
		if r.Code != 0 {
			t.Fatalf("--uninstall exited %d", r.Code)
		}
		assertFirewallAfterRemoval(t, n, r, []int{22})
		if got := operatorRules(t, n); got != rulesBefore {
			t.Errorf("--uninstall changed the operator's rules:\nbefore:\n%s\nafter:\n%s", rulesBefore, got)
		}
	})
}

// A re-run converges the firewall to the env file: a new Dashboard port's
// rule replaces the old one's, and a ufw disabled by hand comes back on.
func TestReRunConvergesTheFirewallToTheEnvFile(t *testing.T) {
	t.Parallel()
	forEachDistro(t, func(t *testing.T, d Distro) {
		n := substrate.Boot(t, d, Bare)
		first := installAs(t, n, "alice")
		sh(t, n, "ufw disable && sed -i 's/^PANTALAN_DASHBOARD_ADDR=.*/PANTALAN_DASHBOARD_ADDR=:9443/' /etc/pantalan/pantalan.env")
		before := hostState(t, n) + sh(t, n, "ufw status; ufw show added")

		// Flags that disagree with the env file stop the run before any
		// change.
		for flag, names := range map[string][]string{
			"--no-firewall":         {"PANTALAN_FIREWALL=ufw", "pantalan.env"},
			"--dashboard-port=8443": {"PANTALAN_DASHBOARD_ADDR=:9443", "pantalan.env"},
		} {
			r := installer{}.run(t, n, flag)
			assertFailedNaming(t, r, names...)
		}
		if after := hostState(t, n) + sh(t, n, "ufw status; ufw show added"); after != before {
			t.Errorf("a refused run changed the host:\nbefore:\n%s\nafter:\n%s", before, after)
		}

		r := installer{}.run(t, n)
		if r.Code != 0 {
			t.Fatalf("re-run exited %d", r.Code)
		}
		assertFirewallLine(t, r.Output(), "ufw enabled: allows SSH 22, 80, 443, 9443; added 9443, removed 8443")
		assertInstalled(t, n, installed{
			Version:       tag,
			DashboardPort: 9443,
			Fingerprint:   first.Fingerprint,
			AdminUser:     "alice",
			AdminPassword: first.AdminPassword,
		})
		assertEnvFirewall(t, n, "ufw")
	})
}

// Ubuntu's socket-activated SSH listening on a port sshd's own config
// doesn't know (an ssh.socket.d override) is found and allowed, so enabling
// ufw doesn't lock out logins on it.
func TestSSHSocketOnAnotherPortIsAllowed(t *testing.T) {
	t.Parallel()
	forEachDistro(t, func(t *testing.T, d Distro) {
		onlyOn(t, d, "ubuntu-24.04", "ubuntu-26.04")
		n := substrate.Boot(t, d, Bare)
		// With ssh.service stopped, only systemd holds the sockets, so
		// ssh.socket is the one source that knows 2222 (and sshd -T must
		// cope with sshd not running).
		sh(t, n, `install -d /etc/systemd/system/ssh.socket.d
			printf '[Socket]\nListenStream=0.0.0.0:2222\n' > /etc/systemd/system/ssh.socket.d/zz-listen.conf
			systemctl daemon-reload && systemctl restart ssh.socket && systemctl stop ssh.service
			ss -Hltnp 'sport = :2222' | grep -q '"systemd"'`)

		r := installer{}.run(t, n, "--admin-user", "alice")
		if r.Code != 0 {
			t.Fatalf("install exited %d", r.Code)
		}
		if !strings.Contains(r.Output(), "listening: 22 2222; sshd -T: 22;") {
			t.Errorf("the installer didn't report 2222 from ssh.socket and only 22 from sshd -T")
		}
		s := parseSummary(t, r.Output())
		assertFirewallLine(t, r.Output(), "ufw enabled: allows SSH 22, 2222, 80, 443, 8443")
		assertInstalled(t, n, installed{
			Version:       tag,
			DashboardPort: 8443,
			Fingerprint:   s.Fingerprint,
			AdminUser:     "alice",
			AdminPassword: s.AdminPassword,
			Firewall:      firewall{SSH: []int{22, 2222}},
		})
	})
}

// Hosts where enabling ufw is risky get the rules staged and ufw left off,
// and the install still succeeds. One Debian and one Ubuntu each. Every
// reason is in the unit table (TestRiskyHostsHaveAReasonToStageUfw); these are the ones whose
// probes ask real software: Docker, and an SSH server that's gone.
func TestRiskyHostsGetTheFirewallStaged(t *testing.T) {
	t.Parallel()
	for _, risk := range []struct {
		name    string
		variant Variant
		setup   string // makes the Node risky
		reason  string
		ssh     []int
	}{
		{
			name:    "docker-nftables",
			variant: DockerOK,
			setup:   `printf '{"firewall-backend": "nftables"}\n' > /etc/docker/daemon.json && systemctl restart docker && docker info --format '{{.FirewallBackend.Driver}}' | grep -qx nftables`,
			reason:  "Docker uses its nftables firewall backend",
		},
		{
			name:    "no-ssh",
			variant: Bare,
			// Ubuntu's ssh.socket outlives the package unless stopped first.
			setup:  "systemctl disable --now --quiet ssh.socket ssh.service 2>/dev/null; DEBIAN_FRONTEND=noninteractive apt-get purge -y -q openssh-server >/dev/null",
			reason: "no SSH server found",
			ssh:    []int{},
		},
	} {
		t.Run(risk.name, func(t *testing.T) {
			t.Parallel()
			forEachDistro(t, func(t *testing.T, d Distro) {
				onlyOn(t, d, "debian-13", "ubuntu-24.04")
				n := substrate.Boot(t, d, risk.variant)
				sh(t, n, risk.setup)

				r := installer{}.run(t, n, "--admin-user", "alice")
				if r.Code != 0 {
					t.Fatalf("install exited %d, want 0: a staged firewall is still a working install", r.Code)
				}
				s := parseSummary(t, r.Output())
				assertFirewallLine(t, r.Output(), "ufw NOT enabled ("+risk.reason+"): rules are staged; check SSH, then run: ufw --force enable")
				assertInstalled(t, n, installed{
					Version:       tag,
					DashboardPort: 8443,
					Fingerprint:   s.Fingerprint,
					AdminUser:     "alice",
					AdminPassword: s.AdminPassword,
					Firewall:      firewall{State: ufwStaged, SSH: risk.ssh},
				})
			})
		})
	}
}

// Another firewall manager means ufw is skipped entirely: not installed, no
// rules, and the operator is told which ports to open. nftables on Debian,
// firewalld on Ubuntu; x64 only.
func TestAnotherFirewallManagerSkipsUfw(t *testing.T) {
	t.Parallel()
	forEachDistro(t, func(t *testing.T, d Distro) {
		onlyOn(t, d, "debian-12", "ubuntu-26.04")
		if nodeArch != "amd64" {
			t.Skip("this path runs on amd64 only")
		}
		n := substrate.Boot(t, d, DockerOK)
		// Docker forwards it ahead of the other firewall too. Started
		// first: a firewall coming up after it may flush Docker's rules.
		runPublishedContainer(t, n)
		var reason, open string
		if strings.HasPrefix(d.Name, "debian") {
			// The operator's own ruleset (the package's stock one, which
			// Docker CE's dependencies enable, doesn't count).
			sh(t, n, `command -v nft >/dev/null || { apt-get update -q && DEBIAN_FRONTEND=noninteractive apt-get install -y -q --no-install-recommends nftables; } >/dev/null
				printf 'table inet operator {\n\tchain input {\n\t\ttype filter hook input priority filter; policy accept;\n\t}\n}\n' > /etc/nftables.conf
				systemctl enable --now --quiet nftables.service`)
			reason = "nftables.service is enabled with its own ruleset"
		} else {
			sh(t, n, `{ apt-get update -q && DEBIAN_FRONTEND=noninteractive apt-get install -y -q --no-install-recommends firewalld; } >/dev/null
				firewall-cmd --state`)
			reason = "firewalld is active"
			open = "firewall-cmd --add-port=80/tcp --add-port=443/tcp --add-port=8443/tcp"
		}

		r := installer{}.run(t, n, "--admin-user", "alice")
		if r.Code != 0 {
			t.Fatalf("install exited %d, want 0: a skipped firewall is still a working install", r.Code)
		}
		s := parseSummary(t, r.Output())
		assertFirewallLine(t, r.Output(), "skipped: "+reason+"; open 80, 443, 8443 in it")
		assertPublishedWarning(t, r, "which Docker forwards ahead of any firewall on this Node")
		if !strings.Contains(r.Output(), "warning: "+reason) {
			t.Errorf("the installer didn't warn that %s", reason)
		}
		if open != "" {
			sh(t, n, open) // what the operator does next
		}
		assertInstalled(t, n, installed{
			Version:       tag,
			DashboardPort: 8443,
			Fingerprint:   s.Fingerprint,
			AdminUser:     "alice",
			AdminPassword: s.AdminPassword,
			Firewall:      firewall{State: noUfw},
		})
	})
}

// onlyOn skips the test unless d is one of names.
func onlyOn(t *testing.T, d Distro, names ...string) {
	t.Helper()
	if !slices.Contains(names, d.Name) {
		t.Skipf("this path runs on %s only", strings.Join(names, ", "))
	}
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
