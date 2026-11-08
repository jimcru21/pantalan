//go:build installtest

package installtest

import (
	"regexp"
	"strings"
	"testing"
)

func TestFreshInstallFromBareWithNoTTY(t *testing.T) {
	t.Parallel()
	forEachDistro(t, func(t *testing.T, d Distro) {
		freshInstallFromBare(t, substrate.Boot(t, d, Bare))
	})
}

// freshInstallFromBare installs on a just-booted Bare Node n with no tty
// and checks everything a fresh install must leave, SSH from outside
// included; it returns the summary. The VM tier's sequence starts with it.
func freshInstallFromBare(t *testing.T, n Node) summary {
	t.Helper()
	r := installer{}.run(t, n, "--admin-user", "alice")
	if r.Code != 0 {
		t.Fatalf("install exited %d", r.Code)
	}
	s := parseSummary(t, r.Output())
	if s.Version != tag {
		t.Errorf("the summary says %s, want %s", s.Version, tag)
	}
	if want := "https://" + n.Addr() + ":8443"; s.DashboardURL != want {
		t.Errorf("the summary's Dashboard is %s, want the default-route address %s", s.DashboardURL, want)
	}
	if s.Admin != "alice" {
		t.Errorf("the summary's Admin is %q, want alice", s.Admin)
	}
	assertFirewallLine(t, r.Output(), "ufw enabled: allows SSH 22, 80, 443, 8443")
	if !strings.Contains(r.Output(), "Docker-published container ports bypass ufw") {
		t.Errorf("the summary doesn't say that Docker-published ports bypass ufw")
	}
	if !strings.Contains(r.Output(), "Behind NAT?") {
		t.Errorf("the summary doesn't flag the private address %s with Behind NAT?", n.Addr())
	}
	assertInstalled(t, n, installed{
		Version:       tag,
		DashboardPort: 8443,
		Fingerprint:   s.Fingerprint,
		AdminUser:     "alice",
		AdminPassword: s.AdminPassword,
	})

	log := sh(t, n, "cat /var/log/pantalan-install.log")
	if strings.Contains(log, s.AdminPassword) {
		t.Errorf("the install log contains the Admin password")
	}
	if !strings.Contains(log, "Admin password: shown on screen only") {
		t.Errorf("the install log doesn't say the Admin password was shown on screen only")
	}
	token := adminTokenLine.FindStringSubmatch(sh(t, n, "cat /etc/pantalan/pantalan.env"))
	if token != nil && (strings.Contains(r.Output(), token[1]) || strings.Contains(log, token[1])) {
		t.Errorf("the Admin Token was printed")
	}
	if got := sh(t, n, "cat /etc/docker/daemon.json"); !strings.Contains(strings.ReplaceAll(got, " ", ""), `"live-restore":true`) {
		t.Errorf("the Docker it installed has daemon.json %q, want live-restore on", got)
	}
	sh(t, n, "command -v git && dpkg-query -W ca-certificates")
	return s
}

func TestFreshInstallKeepsAnAcceptableDocker(t *testing.T) {
	t.Parallel()
	forEachDistro(t, func(t *testing.T, d Distro) {
		n := substrate.Boot(t, d, DockerOK)
		// The operator's own daemon.json, formatted their way.
		sh(t, n, `mkdir -p /etc/docker && printf '{ "log-driver":"local",\n\t"max-concurrent-downloads": 4 }\n' >/etc/docker/daemon.json && systemctl restart docker`)
		const dockerState = `dpkg-query -W docker-ce docker-ce-cli containerd.io
			docker version --format '{{.Server.Version}}'
			sha256sum /etc/docker/daemon.json; ls -l --time-style=full-iso /etc/docker`
		dockerBefore := sh(t, n, dockerState)
		// Someone else's container, published on every address the way
		// Docker does by default: it bypasses ufw, so the summary warns.
		runPublishedContainer(t, n)

		r := installer{}.run(t, n, "--admin-user", "alice")
		if r.Code != 0 {
			t.Fatalf("install exited %d", r.Code)
		}
		s := parseSummary(t, r.Output())
		assertPublishedWarning(t, r, "which bypass ufw")
		assertInstalled(t, n, installed{
			Version:       tag,
			DashboardPort: 8443,
			Fingerprint:   s.Fingerprint,
			AdminUser:     "alice",
			AdminPassword: s.AdminPassword,
		})
		if after := sh(t, n, dockerState); after != dockerBefore {
			t.Errorf("the installer changed the existing Docker:\nbefore: %s\nafter:  %s", dockerBefore, after)
		}
	})
}

func TestRefusesWithoutRootBeforeAnyChange(t *testing.T) {
	t.Parallel()
	forEachDistro(t, func(t *testing.T, d Distro) {
		n := substrate.Boot(t, d, Bare)
		before := hostState(t, n)

		r := installer{asUser: "65534:65534"}.run(t, n, "--admin-user", "alice")

		assertFailedNaming(t, r, "root", "| sudo sh -s -- --admin-user alice")
		assertUntouched(t, n, before)
		if got := n.Run(t, Cmd{Script: "test -e /var/log/pantalan-install.log"}); got.Code == 0 {
			t.Errorf("a refused run left an install log")
		}
	})
}

func TestMissingAdminUserWithNoTTYNamesTheFlag(t *testing.T) {
	t.Parallel()
	forEachDistro(t, func(t *testing.T, d Distro) {
		n := substrate.Boot(t, d, Bare)
		before := hostState(t, n)

		r := installer{}.run(t, n)

		assertFailedNaming(t, r, "--admin-user")
		assertUntouched(t, n, before)
	})
}

func TestInvalidUsernameFailsBeforeAnyChange(t *testing.T) {
	t.Parallel()
	forEachDistro(t, func(t *testing.T, d Distro) {
		n := substrate.Boot(t, d, Bare)
		before := hostState(t, n)

		r := installer{}.run(t, n, "--admin-user", "9lives")

		assertFailedNaming(t, r, "9lives", "start with a letter")
		assertUntouched(t, n, before)
	})
}

func TestChecksumMismatchAbortsBeforeInstallingAnything(t *testing.T) {
	t.Parallel()
	forEachDistro(t, func(t *testing.T, d Distro) {
		n := substrate.Boot(t, d, Bare)
		before := hostState(t, n)

		r := installer{origin: corruptOrigin}.run(t, n, "--admin-user", "alice")

		assertFailedNaming(t, r, "checksum", "pantalan_"+tag+"_linux_"+nodeArch)
		assertUntouched(t, n, before)
	})
}

// runPublishedContainer starts published-web on n, publishing port 8081
// on every address the way Docker does by default. Its image is just sleep
// and the libraries it needs. n must have Docker running.
func runPublishedContainer(t *testing.T, n Node) {
	t.Helper()
	sh(t, n, `tar -chf - /usr/bin/sleep $(ldd /usr/bin/sleep | grep -o '/[^ ]*') 2>/dev/null | docker import - published:local >/dev/null
		docker run --detach --name published-web --publish 8081:80 published:local /usr/bin/sleep infinity >/dev/null`)
}

// assertPublishedWarning fails unless r's summary warns that
// published-web's 0.0.0.0:8081 is reachable, for the reason why.
func assertPublishedWarning(t *testing.T, r Result, why string) {
	t.Helper()
	want := regexp.MustCompile(`(?m)^ +Warning: these running containers publish ports on all addresses, ` + regexp.QuoteMeta(why) + `: published-web \(0\.0\.0\.0:8081->80/tcp`)
	if !want.MatchString(r.Output()) {
		t.Errorf("the summary doesn't warn that published-web's 0.0.0.0:8081 is published on all addresses, %s", why)
	}
}
