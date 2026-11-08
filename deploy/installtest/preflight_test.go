//go:build installtest

package installtest

import (
	"fmt"
	"slices"
	"testing"
)

// The Docker conflicts: hosts whose Docker the installer refuses, each an
// image variant on top of Bare.
const (
	// DockerConflictDockerIO runs the distro's docker.io.
	DockerConflictDockerIO Variant = "docker-conflict-docker-io"
	// DockerConflictOldDockerCE runs docker-ce 27 from Docker's repo.
	DockerConflictOldDockerCE Variant = "docker-conflict-old-docker-ce"
	// DockerConflictPodmanDocker has podman-docker as its docker.
	DockerConflictPodmanDocker Variant = "docker-conflict-podman-docker"
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
		only(t, d, "debian-12", "ubuntu-24.04")
		n := substrate.Boot(t, d, DockerConflictOldDockerCE)
		version := sh(t, n, "dpkg-query -W -f '${Version}' docker-ce")
		before := hostState(t, n)

		r := installer{}.run(t, n, "--admin-user", "alice")

		assertFailedNaming(t, r, "docker-ce "+version+" is installed", "28 or newer", "never upgrades")
		assertUntouched(t, n, before)
	})
}

func TestPodmanDockerAborts(t *testing.T) {
	t.Parallel()
	forEachDistro(t, func(t *testing.T, d Distro) {
		only(t, d, "debian-12", "ubuntu-24.04")
		n := substrate.Boot(t, d, DockerConflictPodmanDocker)
		before := hostState(t, n)

		r := installer{}.run(t, n, "--admin-user", "alice")

		assertFailedNaming(t, r, "podman-docker is installed", "never removes")
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

func TestRefusedDashboardPortsAreRejectedWithAReason(t *testing.T) {
	t.Parallel()
	forEachDistro(t, func(t *testing.T, d Distro) {
		n := substrate.Boot(t, d, Bare)
		// A second sshd, on a port the below-1024 rule wouldn't catch.
		sh(t, n, "mkdir -p /run/sshd && /usr/sbin/sshd -p 2222")
		occupy(t, n, 9000)
		before := hostState(t, n)

		for port, reason := range map[string]string{
			"80":    "app proxy",
			"443":   "kept for app HTTPS",
			"1023":  "below 1024",
			"22":    "SSH listens on port 22",
			"2222":  "SSH listens on port 2222",
			"9000":  "in use by perl",
			"8x443": "isn't a port number",
			"65536": "isn't a port number",
		} {
			r := installer{}.run(t, n, "--admin-user", "alice", "--dashboard-port", port)
			assertFailedNaming(t, r, "--dashboard-port "+port+": ", reason)
		}
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

// only skips t unless the Node is one of the named distros: for the paths
// the installer test strategy runs on a subset of the matrix.
func only(t *testing.T, d Distro, names ...string) {
	t.Helper()
	if !slices.Contains(names, d.Name) {
		t.Skipf("this path runs on %v only", names)
	}
}
