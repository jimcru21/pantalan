//go:build installtest

package installtest

import (
	"fmt"
	"strings"
	"testing"
)

// The Docker conflicts: hosts whose Docker the installer refuses, each an
// image variant on top of Bare. Every conflicting kind is in the unit
// table (TestOnlyAnAnsweringDockerCE28OrNewerIsUsedAsItIs); these are its paths on real packages.
const (
	// DockerConflictDockerIO runs the distro's docker.io.
	DockerConflictDockerIO Variant = "docker-conflict-docker-io"
	// DockerConflictOldDockerCE runs docker-ce 27 from Docker's repo.
	DockerConflictOldDockerCE Variant = "docker-conflict-old-docker-ce"
)

func TestDockerIOAbortsAndLeavesTheHostAlone(t *testing.T) {
	t.Parallel()
	forEachDistro(t, func(t *testing.T, d Distro) {
		n := substrate.Boot(t, d, DockerConflictDockerIO)
		before := hostState(t, n)

		r := installer{}.run(t, n, "--admin-user", "alice")

		assertFailedNaming(t, r, "docker.io is installed", "Docker CE", "never removes")
		assertUntouched(t, n, before)
	})
}

func TestDockerCEOlderThan28AbortsAndIsNotUpgraded(t *testing.T) {
	t.Parallel()
	forEachDistro(t, func(t *testing.T, d Distro) {
		// Only these two have a docker-ce < 28 in Docker's repo.
		onlyOn(t, d, "debian-12", "ubuntu-24.04")
		n := substrate.Boot(t, d, DockerConflictOldDockerCE)
		version := sh(t, n, "dpkg-query -W -f '${Version}' docker-ce")
		before := hostState(t, n)

		r := installer{}.run(t, n, "--admin-user", "alice")

		assertFailedNaming(t, r, "docker-ce "+version+" is installed", "28 or newer", "never upgrades")
		assertUntouched(t, n, before)
	})
}

func TestDockerCEWhoseDaemonDoesNotAnswerAborts(t *testing.T) {
	t.Parallel()
	forEachDistro(t, func(t *testing.T, d Distro) {
		n := substrate.Boot(t, d, DockerOK)
		// The socket too, or the docker CLI would start the daemon again.
		sh(t, n, "systemctl stop docker.socket docker.service")
		before := hostState(t, n)

		r := installer{}.run(t, n, "--admin-user", "alice")

		assertFailedNaming(t, r, "Docker daemon doesn't answer", "systemctl status docker")
		assertUntouched(t, n, before)
		if got := n.Run(t, Cmd{Script: "systemctl is-active docker.service"}); got.Code == 0 {
			t.Errorf("the installer started the stopped Docker daemon")
		}
	})
}

func TestDashboardPortTakenWithNoTTYAbortsNamingTheHolderAndTheFlag(t *testing.T) {
	t.Parallel()
	forEachDistro(t, func(t *testing.T, d Distro) {
		n := substrate.Boot(t, d, Bare)
		occupy(t, n, 8443)
		before := hostState(t, n)

		r := installer{}.run(t, n, "--admin-user", "alice")

		assertFailedNaming(t, r, "8443", "in use by perl", "--dashboard-port 8444")
		assertUntouched(t, n, before)
	})
}

func TestDashboardPortTakenAtATerminalAsksAndInstalls(t *testing.T) {
	t.Parallel()
	forEachDistro(t, func(t *testing.T, d Distro) {
		onlyOn(t, d, "debian-12", "ubuntu-24.04")
		if nodeArch != "amd64" {
			t.Skip("this path runs on amd64 only")
		}
		n := substrate.Boot(t, d, Bare)
		occupy(t, n, 8443)

		// A refused port, then Enter for the suggested one; an invalid
		// username, then a good one.
		r := installer{typed: []string{"443", "", "9lives", "alice"}}.run(t, n)
		if r.Code != 0 {
			t.Fatalf("install exited %d", r.Code)
		}
		out := strings.ReplaceAll(r.Output(), "\r", "")
		for _, want := range []string{
			"port 8443 is in use by perl",
			"Dashboard port [8444]: ",
			"port 443 is kept for app HTTPS",
			"start with a letter",
		} {
			if !strings.Contains(out, want) {
				t.Errorf("the installer's output doesn't mention %q", want)
			}
		}
		s := parseSummary(t, out)
		if want := "https://" + n.Addr() + ":8444"; s.DashboardURL != want {
			t.Errorf("the summary's Dashboard is %s, want %s", s.DashboardURL, want)
		}
		assertInstalled(t, n, installed{
			Version:       tag,
			DashboardPort: 8444,
			Fingerprint:   s.Fingerprint,
			AdminUser:     "alice",
			AdminPassword: s.AdminPassword,
		})
	})
}

// The refused Dashboard ports are a table in the unit tests
// (TestEachRefusedDashboardPortSaysWhy); this is the one that needs the host: an SSH
// port found on a real Node, above 1024 where no other rule catches it.
func TestADashboardPortSSHListensOnIsRefused(t *testing.T) {
	t.Parallel()
	forEachDistro(t, func(t *testing.T, d Distro) {
		n := substrate.Boot(t, d, Bare)
		sh(t, n, "mkdir -p /run/sshd && /usr/sbin/sshd -p 2222")
		before := hostState(t, n)

		r := installer{}.run(t, n, "--admin-user", "alice", "--dashboard-port", "2222")

		assertFailedNaming(t, r, "--dashboard-port 2222: SSH listens on port 2222")
		assertUntouched(t, n, before)
	})
}

// occupy has some other program, perl, listen on port on n, the way a
// web server already running there would.
func occupy(t *testing.T, n Node, port int) {
	t.Helper()
	sh(t, n, fmt.Sprintf(`systemd-run --quiet --unit=squatter-%[1]d perl -MIO::Socket::INET -e '
		my $s = IO::Socket::INET->new(LocalPort => %[1]d, Listen => 5, ReuseAddr => 1) or die "listen: $!";
		sleep' &&
		for i in $(seq 50); do ss -Hltn "sport = :%[1]d" | grep -q . && exit 0; sleep 0.1; done
		echo "nothing listens on %[1]d" >&2; exit 1`, port))
}
