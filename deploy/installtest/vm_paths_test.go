//go:build installtest

package installtest

import (
	"net"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"
)

// The VM-only rows of the path × gate table: what containers can't show.
// `make installtest-vm` runs these (and the ssh.socket path) on KVM VMs;
// on the containers they skip. Every other path is the PR gate's.

// vmOnly skips a path unless the Nodes are VMs.
func vmOnly(t *testing.T) {
	t.Helper()
	if _, ok := substrate.(*vms); !ok {
		t.Skip("a VM-tier path (INSTALLTEST_SUBSTRATE=vm, make installtest-vm): containers have no real boot, distro kernel or global IPv6")
	}
}

// The VM sequence, once per distro on one VM: fresh install (run over SSH
// with sudo, the way an operator runs it) → SSH lockout check from the
// runner over virbr0 → reboot → recheck → upgrade → --uninstall → --purge.
// Each step needs the one before, so the first failure ends the sequence.
func TestVMInstallRebootUpgradeUninstallPurge(t *testing.T) {
	t.Parallel()
	vmOnly(t)
	forEachDistro(t, func(t *testing.T, d Distro) {
		n := substrate.Boot(t, d, Bare)
		want := installed{Version: tag, DashboardPort: 8443, AdminUser: "alice"}
		var app appState
		var session *http.Cookie
		dashboard := net.JoinHostPort(n.Addr(), "8443")

		steps := []struct {
			name string
			run  func(t *testing.T)
		}{
			{"fresh install", func(t *testing.T) {
				// freshInstallFromBare checks SSH from outside on a new login
				// after ufw came up: the lockout check.
				s := freshInstallFromBare(t, n)
				want.Fingerprint, want.AdminPassword = s.Fingerprint, s.AdminPassword
				// Over SSH, through sudo (which drops SSH_CONNECTION), the
				// installer still finds the port its own session came in on.
				if log := sh(t, n, "cat /var/log/pantalan-install.log"); !strings.Contains(log, "this session: 22)") {
					t.Errorf("the install log doesn't name this session's SSH port 22:\n%s", grepLines(log, "SSH: "))
				}
				app = plantAppState(t, n)
				session = signIn(t, dashboard, want.Fingerprint, "alice", want.AdminPassword)
			}},
			{"reboot", func(t *testing.T) {
				n.(Rebooter).Reboot(t)
				// ufw and nftables.service have no ordering between them at
				// boot, and nftables' stock ruleset flushes everything: the
				// rules must be in the kernel, not just in ufw's files.
				assertUfwRulesLoaded(t, n, 22, 80, 443, 8443)
				assertInstalled(t, n, want)
				if got := openDashboard(t, dashboard, want.Fingerprint, session); got != http.StatusOK {
					t.Errorf("the session from before the reboot opens the Dashboard with %d, want 200", got)
				}
				assertAppStatePresent(t, n, app)
			}},
			{"upgrade", func(t *testing.T) {
				needNewerRelease(t) // the release gate upgrades from the previous release instead
				r := installer{release: newerTag}.run(t, n)
				if r.Code != 0 {
					t.Fatalf("upgrade exited %d", r.Code)
				}
				if from, to := parseUpgradeSummary(t, r.Output()); from != tag || to != newerTag {
					t.Errorf("the summary says Upgraded %s → %s, want %s → %s", from, to, tag, newerTag)
				}
				want.Version = newerTag
				assertInstalled(t, n, want)
				if got := openDashboard(t, dashboard, want.Fingerprint, session); got != http.StatusOK {
					t.Errorf("the session from before the upgrade opens the Dashboard with %d, want 200", got)
				}
				if apps := apiGet(t, n, "/api/v1/apps"); !strings.Contains(apps, app.ID) {
					t.Errorf("after the upgrade the API lists %s, want the kept app %s", apps, app.ID)
				}
			}},
			{"uninstall", func(t *testing.T) {
				r := installer{release: want.Version}.run(t, n, "--uninstall")
				if r.Code != 0 {
					t.Fatalf("--uninstall exited %d", r.Code)
				}
				assertServiceGone(t, n)
				assertFirewallAfterRemoval(t, n, r, []int{22})
				assertAppStatePresent(t, n, app)
				assertSSHFromOutside(t, n, 22)
			}},
			{"purge", func(t *testing.T) {
				r := installer{release: want.Version}.run(t, n, "--purge", "--yes")
				if r.Code != 0 {
					t.Fatalf("--purge --yes exited %d", r.Code)
				}
				assertPurged(t, n, app)
				assertFirewallAfterRemoval(t, n, r, []int{22})
				assertDockerStillRuns(t, n)
				assertSSHFromOutside(t, n, 22)
			}},
		}
		for _, step := range steps {
			if !t.Run(step.name, step.run) {
				return
			}
		}
	})
}

// IPV6=no in /etc/default/ufw on a Node with a global IPv6 address: ufw
// would come up IPv4-only, and an SSH session over IPv6 would drop, so the
// rules are staged and ufw is left off. The address is a documentation
// prefix added by hand, the way a provider's static IPv6 config adds one;
// the Docker bridge the containers sit on has no IPv6 at all.
func TestVMIPv6NoWithAGlobalAddressStagesTheFirewall(t *testing.T) {
	t.Parallel()
	vmOnly(t)
	forEachDistro(t, func(t *testing.T, d Distro) {
		n := substrate.Boot(t, d, Bare)
		r := n.Run(t, Cmd{Script: `set -e
			command -v ufw >/dev/null || { apt-get update -q && DEBIAN_FRONTEND=noninteractive apt-get install -y -q ufw; } >/dev/null
			sed -i 's/^IPV6=.*/IPV6=no/' /etc/default/ufw
			grep -qx 'IPV6=no' /etc/default/ufw
			dev=$(ip -o route show default | awk '{print $5; exit}')
			ip -6 addr add 2001:db8:53::2/64 dev "$dev" nodad
			ip -6 addr show scope global | grep -q 2001:db8:53::2`, Timeout: 5 * time.Minute})
		if r.Code != 0 {
			t.Fatalf("setting up IPV6=no with a global address exited %d:\n%s", r.Code, r.Output())
		}
		defaultsBefore := sh(t, n, "cat /etc/default/ufw")

		r = installer{}.run(t, n, "--admin-user", "alice")
		if r.Code != 0 {
			t.Fatalf("install exited %d, want 0: a staged firewall is still a working install", r.Code)
		}
		s := parseSummary(t, r.Output())
		assertFirewallLine(t, r.Output(), "ufw NOT enabled (IPV6=no in /etc/default/ufw on a Node with a global IPv6 address): rules are staged; check SSH, then run: ufw --force enable")
		assertInstalled(t, n, installed{
			Version:       tag,
			DashboardPort: 8443,
			Fingerprint:   s.Fingerprint,
			AdminUser:     "alice",
			AdminPassword: s.AdminPassword,
			Firewall:      firewall{State: ufwStaged},
		})
		if got := sh(t, n, "cat /etc/default/ufw"); got != defaultsBefore {
			t.Errorf("the installer changed /etc/default/ufw")
		}
	})
}

// Snap Docker on Ubuntu 24.04 (containers can't run snapd): the installer
// refuses it and changes nothing, the snap included.
func TestVMSnapDockerAbortsAndIsLeftAlone(t *testing.T) {
	t.Parallel()
	vmOnly(t)
	forEachDistro(t, func(t *testing.T, d Distro) {
		onlyOn(t, d, "ubuntu-24.04")
		n := substrate.Boot(t, d, Bare)
		r := n.Run(t, Cmd{Script: `set -e
			snap wait system seed.loaded
			snap install docker >/dev/null
			for i in $(seq 60); do /snap/bin/docker version >/dev/null 2>&1 && break; sleep 1; done
			/snap/bin/docker version --format '{{.Server.Version}}'`, Timeout: 10 * time.Minute})
		if r.Code != 0 {
			t.Fatalf("installing snap Docker exited %d:\n%s", r.Code, r.Output())
		}
		const snapState = "snap list docker; /snap/bin/docker version --format '{{.Server.Version}}'"
		before, snapBefore := hostState(t, n), sh(t, n, snapState)

		r = installer{}.run(t, n, "--admin-user", "alice")

		assertFailedNaming(t, r, "snap", "Pantalan never removes a Docker for you", "Docker CE")
		assertUntouched(t, n, before)
		if got := sh(t, n, snapState); got != snapBefore {
			t.Errorf("the snap Docker changed:\nbefore:\n%s\nafter:\n%s", snapBefore, got)
		}
	})
}

// An operator on the previous published release runs the new install.sh:
// the upgrade to the release under test. Skipped while no older release is
// published (CI lists them in INSTALLTEST_PUBLISHED_TAGS).
func TestVMUpgradeFromThePreviousRelease(t *testing.T) {
	t.Parallel()
	vmOnly(t)
	if previousTag == "" {
		t.Skip(noPreviousReason)
	}
	forEachDistro(t, func(t *testing.T, d Distro) {
		n := substrate.Boot(t, d, Bare)
		r := installer{published: previousTag}.run(t, n, "--admin-user", "alice")
		if r.Code != 0 {
			t.Fatalf("installing the published %s exited %d", previousTag, r.Code)
		}
		first := parseSummary(t, r.Output())
		assertBinaryVersion(t, n, previousTag)
		app := plantAppState(t, n)

		r = installer{}.run(t, n)
		if r.Code != 0 {
			t.Fatalf("upgrade from %s exited %d", previousTag, r.Code)
		}
		if from, to := parseUpgradeSummary(t, r.Output()); from != previousTag || to != tag {
			t.Errorf("the summary says Upgraded %s → %s, want %s → %s", from, to, previousTag, tag)
		}
		assertInstalled(t, n, installed{
			Version:       tag,
			DashboardPort: 8443,
			Fingerprint:   first.Fingerprint,
			AdminUser:     "alice",
			AdminPassword: first.AdminPassword,
		})
		if apps := apiGet(t, n, "/api/v1/apps"); !strings.Contains(apps, app.ID) {
			t.Errorf("after the upgrade the API lists %s, want the kept app %s", apps, app.ID)
		}
		assertAppStatePresent(t, n, app)
	})
}

// assertUfwRulesLoaded: ufw is running and its user rules for ports are in
// the kernel's ruleset now, so they came back with the boot.
func assertUfwRulesLoaded(t *testing.T, n Node, ports ...int) {
	t.Helper()
	if got := sh(t, n, "systemctl is-active ufw || :"); got != "active" {
		t.Errorf("ufw.service is %q after the reboot, want active", got)
	}
	r := n.Run(t, Cmd{Script: "iptables -S ufw-user-input"})
	if r.Code != 0 {
		t.Errorf("no ufw-user-input chain in the kernel after the reboot:\n%s", r.Output())
		return
	}
	for _, port := range ports {
		accept := regexp.MustCompile(`(?m)^-A ufw-user-input .*--dport ` + strconv.Itoa(port) + `\b.*-j ACCEPT`)
		if !accept.MatchString(r.Stdout) {
			t.Errorf("after the reboot the kernel's ufw-user-input doesn't accept port %d:\n%s", port, r.Stdout)
		}
	}
}

// grepLines is the lines of s that contain sub.
func grepLines(s, sub string) string {
	var lines []string
	for _, line := range strings.Split(s, "\n") {
		if strings.Contains(line, sub) {
			lines = append(lines, line)
		}
	}
	return strings.Join(lines, "\n")
}
