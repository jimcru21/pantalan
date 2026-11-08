//go:build installtest

package installtest

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"crypto/sha512"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"
)

// vms is the VM-tier Substrate (nightly and the release gate): each Node
// is a KVM VM booted from the distro's official cloud image, on libvirt's
// default NAT network (virbr0), so the installer meets a distro kernel, the
// packages the image really ships and real reboots. Tests reach a VM the
// way an operator reaches a server: over SSH, with sudo.
//
// Only x64 runners have KVM, so arm64 stays container-only. Without KVM (or
// libvirt and its tools) every Boot skips, unless INSTALLTEST_VM_REQUIRED
// is set, which CI sets so a runner without KVM fails rather than passes
// having tested nothing.
//
// INSTALLTEST_VM_DIR is where cloud images and VM disks go (default: under
// the temp dir). libvirt's QEMU must be able to reach it: CI uses a
// directory under /var/lib/libvirt/images.
type vms struct {
	dir         string
	hostAddr    string // the host's address on virbr0
	signer      ssh.Signer
	authorized  string // signer's public key, as authorized_keys has it
	unavailable string // why VMs can't boot here; empty when they can

	mu     sync.Mutex
	images map[string]*imageBuild // by distro; imageBuild.tag holds the image's path
}

// cloudImages are the official images the VM tier boots, the released
// builds each project points cloud users at.
var cloudImages = map[string]cloudImage{
	"debian-12":    {"https://cloud.debian.org/images/cloud/bookworm/latest/debian-12-genericcloud-amd64.qcow2", "SHA512SUMS"},
	"debian-13":    {"https://cloud.debian.org/images/cloud/trixie/latest/debian-13-genericcloud-amd64.qcow2", "SHA512SUMS"},
	"ubuntu-24.04": {"https://cloud-images.ubuntu.com/releases/noble/release/ubuntu-24.04-server-cloudimg-amd64.img", "SHA256SUMS"},
	"ubuntu-26.04": {"https://cloud-images.ubuntu.com/releases/resolute/release/ubuntu-26.04-server-cloudimg-amd64.img", "SHA256SUMS"},
}

// vmTools are the commands the VM substrate drives.
var vmTools = []string{"virsh", "virt-install", "qemu-img", "cloud-localds"}

// newVMs sets up the VM substrate. It never fails: when VMs can't run
// here, unavailable says why and every Boot skips.
func newVMs() *vms {
	v := &vms{images: map[string]*imageBuild{}}
	if nodeArch != "amd64" {
		v.unavailable = "the VM tier is x64 only (arm64 stays container-only)"
		return v
	}
	if fi, err := os.Stat("/dev/kvm"); err != nil || fi.Mode()&os.ModeCharDevice == 0 {
		v.unavailable = "no /dev/kvm on this machine"
		return v
	}
	for _, tool := range vmTools {
		if _, err := exec.LookPath(tool); err != nil {
			v.unavailable = "the VM tier needs " + strings.Join(vmTools, ", ") + "; " + tool + " is missing"
			return v
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	netXML, err := virsh(ctx, "net-dumpxml", "default")
	if err != nil {
		v.unavailable = "libvirt's default network: " + err.Error()
		return v
	}
	if v.hostAddr, _, err = networkAddr(netXML); err != nil {
		v.unavailable = err.Error()
		return v
	}
	v.dir = os.Getenv("INSTALLTEST_VM_DIR")
	if v.dir == "" {
		v.dir = filepath.Join(os.TempDir(), "pantalan-installtest-vm")
	}
	if err := os.MkdirAll(filepath.Join(v.dir, "images"), 0o755); err != nil {
		v.unavailable = err.Error()
		return v
	}
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		v.unavailable = err.Error()
		return v
	}
	v.signer, _ = ssh.NewSignerFromKey(priv)
	sshPub, _ := ssh.NewPublicKey(pub)
	v.authorized = strings.TrimSpace(string(ssh.MarshalAuthorizedKey(sshPub))) + " pantalan-installtest"
	return v
}

func (v *vms) HostAddr() string { return v.hostAddr }

func (v *vms) Boot(t *testing.T, d Distro, variant Variant) Node {
	t.Helper()
	if v.unavailable != "" {
		t.Skip("VM tier unavailable: " + v.unavailable)
	}
	if variant != Bare {
		t.Skipf("the VM tier boots cloud images as shipped (%s); %s Nodes are container-only", Bare, variant)
	}
	base, err := v.image(d)
	if err != nil {
		t.Fatalf("cloud image for %s: %v", d.Name, err)
	}
	id := randomHex(4)
	spec := vmSpec{
		Name:      "pantalan-installtest-" + d.Name + "-" + id,
		BaseImage: base,
		MAC:       fmt.Sprintf("52:54:00:%s:%s:%s", id[0:2], id[2:4], id[4:6]),
	}
	spec.Dir = filepath.Join(v.dir, spec.Name)
	// QEMU runs as libvirt's own user and writes the console log here.
	if err := os.MkdirAll(spec.Dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(spec.Dir, 0o777); err != nil {
		t.Fatal(err)
	}
	hostname := "node-" + strings.ReplaceAll(d.Name, ".", "")
	for name, content := range map[string]string{
		"user-data": seedUserData(v.authorized),
		"meta-data": seedMetaData(spec.Name, hostname),
	} {
		if err := os.WriteFile(filepath.Join(spec.Dir, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	n := &vm{v: v, spec: spec}
	t.Cleanup(func() {
		if t.Failed() {
			n.dumpDiagnostics(t)
		}
		ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
		defer cancel()
		virsh(ctx, "destroy", spec.Name)
		virsh(ctx, "undefine", spec.Name)
		if err := os.RemoveAll(spec.Dir); err != nil {
			t.Logf("remove %s: %v", spec.Dir, err)
		}
	})
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	for _, c := range [][]string{
		append([]string{"qemu-img"}, spec.overlayArgs()...),
		append([]string{"cloud-localds"}, spec.seedArgs()...),
		append([]string{"virt-install"}, spec.virtInstallArgs()...),
	} {
		if _, err := run(ctx, spec.Dir, nil, c[0], c[1:]...); err != nil {
			t.Fatalf("boot %s: %v", d.Name, err)
		}
	}
	start := time.Now()
	n.waitUp(t, "")
	t.Logf("booted %s (%s) at %s in %s", spec.Name, filepath.Base(base), n.addr, time.Since(start).Round(time.Second))
	return n
}

// image returns the path of d's cloud image, downloading it on first use
// (or reusing one already in the directory) and checking it against the
// checksums published next to it.
func (v *vms) image(d Distro) (string, error) {
	img, ok := cloudImages[d.Name]
	if !ok {
		return "", fmt.Errorf("no cloud image known for %s", d.Name)
	}
	v.mu.Lock()
	b, ok := v.images[d.Name]
	if !ok {
		b = &imageBuild{tag: filepath.Join(v.dir, "images", img.file())}
		v.images[d.Name] = b
	}
	v.mu.Unlock()
	b.once.Do(func() { b.err = fetchVerified(img, b.tag) })
	return b.tag, b.err
}

// fetchVerified makes file the image img, verified against its published
// checksums: kept if it already matches, downloaded otherwise.
func fetchVerified(img cloudImage, file string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
	defer cancel()
	sums, err := httpGet(ctx, img.sumsURL())
	if err != nil {
		return err
	}
	want, err := expectedSum(string(sums), img.file())
	if err != nil {
		return err
	}
	newHash := sha256.New
	if img.Sums == "SHA512SUMS" {
		newHash = sha512.New
	}
	if f, err := os.Open(file); err == nil {
		h := newHash()
		_, err := io.Copy(h, f)
		f.Close()
		if err == nil && hex.EncodeToString(h.Sum(nil)) == want {
			return nil
		}
	}
	req, err := http.NewRequestWithContext(ctx, "GET", img.URL, nil)
	if err != nil {
		return err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("GET %s: %s", img.URL, resp.Status)
	}
	part := file + ".part"
	out, err := os.Create(part)
	if err != nil {
		return err
	}
	h := newHash()
	if _, err := io.Copy(io.MultiWriter(out, h), resp.Body); err != nil {
		out.Close()
		return fmt.Errorf("download %s: %w", img.URL, err)
	}
	if err := out.Close(); err != nil {
		return err
	}
	if got := hex.EncodeToString(h.Sum(nil)); got != want {
		os.Remove(part)
		return fmt.Errorf("%s has %s %s, want %s", img.URL, img.Sums, got, want)
	}
	return os.Rename(part, file)
}

func httpGet(ctx context.Context, url string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return nil, err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("GET %s: %s", url, resp.Status)
	}
	return io.ReadAll(resp.Body)
}

// vm is one Node of the VM substrate.
type vm struct {
	v    *vms
	spec vmSpec
	addr string // from its DHCP lease on virbr0
}

func (n *vm) Addr() string { return n.addr }

// Run runs c over a new SSH login as vmUser, through `sudo`, with no
// terminal: `ssh node sudo sh -c ...`, the way an operator would run the
// installer. A new login each time also means every command is a check
// that the firewall hasn't locked SSH out.
func (n *vm) Run(t *testing.T, c Cmd) Result {
	t.Helper()
	timeout := c.Timeout
	if timeout == 0 {
		timeout = 2 * time.Minute
	}
	r, err := n.run(c, timeout)
	if err != nil {
		t.Fatalf("on the VM %s, %q: %v\n%s", n.spec.Name, c.Script, err, r.Output())
	}
	return r
}

// run is Run without a test: an error means the command didn't get to
// finish (no SSH, a dropped connection or the timeout), not that it failed.
func (n *vm) run(c Cmd, timeout time.Duration) (Result, error) {
	client, err := ssh.Dial("tcp", net.JoinHostPort(n.addr, "22"), &ssh.ClientConfig{
		User:            vmUser,
		Auth:            []ssh.AuthMethod{ssh.PublicKeys(n.v.signer)},
		HostKeyCallback: ssh.InsecureIgnoreHostKey(), // a throwaway VM
		Timeout:         10 * time.Second,
	})
	if err != nil {
		return Result{}, fmt.Errorf("SSH to %s: %w", n.addr, err)
	}
	defer client.Close()
	session, err := client.NewSession()
	if err != nil {
		return Result{}, fmt.Errorf("SSH session: %w", err)
	}
	var stdout, stderr bytes.Buffer
	session.Stdout, session.Stderr = &stdout, &stderr
	if c.Stdin != "" {
		session.Stdin = strings.NewReader(c.Stdin)
	}
	command := "sudo -n env" + quoteAll(c.Env) + " sh -c " + shellQuote(c.Script)
	done := make(chan error, 1)
	go func() { done <- session.Run(command) }()
	var runErr error
	select {
	case runErr = <-done:
	case <-time.After(timeout):
		client.Close()
		<-done
		return Result{Stdout: stdout.String(), Stderr: stderr.String()}, fmt.Errorf("timed out after %s", timeout)
	}
	r := Result{Stdout: stdout.String(), Stderr: stderr.String()}
	var exit *ssh.ExitError
	switch {
	case runErr == nil:
	case errors.As(runErr, &exit):
		r.Code = exit.ExitStatus()
	default:
		return r, fmt.Errorf("SSH: %w", runErr)
	}
	return r, nil
}

// Reboot reboots the VM for real and waits until it's back: a new boot ID,
// SSH answering and systemd done starting units.
func (n *vm) Reboot(t *testing.T) {
	t.Helper()
	before := sh(t, n, "cat /proc/sys/kernel/random/boot_id")
	// Detached, so this login ends cleanly before the reboot cuts it.
	sh(t, n, "systemd-run --quiet --on-active=2 systemctl reboot")
	start := time.Now()
	n.waitUp(t, before)
	t.Logf("rebooted %s in %s", n.spec.Name, time.Since(start).Round(time.Second))
}

// waitUp waits for the VM to have an address, answer SSH with a boot ID
// other than notBootID, and finish booting (cloud-init included).
func (n *vm) waitUp(t *testing.T, notBootID string) {
	t.Helper()
	deadline := time.Now().Add(6 * time.Minute)
	var last error
	for {
		if time.Now().After(deadline) {
			t.Fatalf("%s didn't come up within 6 minutes: %v", n.spec.Name, last)
		}
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		out, err := virsh(ctx, "domifaddr", n.spec.Name, "--source", "lease")
		cancel()
		if addr := leaseAddr(out, n.spec.MAC); addr != "" {
			n.addr = addr
		} else if err != nil {
			last = err
		}
		if n.addr != "" {
			r, err := n.run(Cmd{Script: "cat /proc/sys/kernel/random/boot_id"}, 20*time.Second)
			switch {
			case err != nil:
				last = err
			case r.Code != 0:
				last = fmt.Errorf("boot_id: %s", r.Output())
			case strings.TrimSpace(r.Stdout) == notBootID:
				last = errors.New("still the boot before the reboot")
			default:
				last = nil
			}
			if last == nil {
				break
			}
		}
		time.Sleep(3 * time.Second)
	}
	// cloud-init exits 2 for "done, with recoverable errors" (deprecated
	// keys and the like): still a booted server.
	r := n.Run(t, Cmd{Script: `cloud-init status --wait >/dev/null; rc=$?
		[ "$rc" -le 2 ] || { cloud-init status --long; exit "$rc"; }
		state=$(systemctl is-system-running --wait)
		echo "$state"
		case $state in running|degraded) ;; *) exit 1 ;; esac`, Timeout: 6 * time.Minute})
	if r.Code != 0 {
		t.Fatalf("%s didn't finish booting:\n%s", n.spec.Name, r.Output())
	}
}

// dumpDiagnostics logs what a failing test most likely needs: the same as
// for a container over SSH, or the serial console when SSH never came up.
func (n *vm) dumpDiagnostics(t *testing.T) {
	r, err := n.run(Cmd{Script: `
		echo "--- systemctl status pantalan"; systemctl status --no-pager pantalan 2>&1
		echo "--- journalctl -u pantalan"; journalctl -u pantalan -n 80 --no-pager 2>&1
		echo "--- /var/log/pantalan-install.log (tail)"; tail -n 80 /var/log/pantalan-install.log 2>&1
		echo "--- ufw status verbose"; ufw status verbose 2>&1
		echo "--- systemctl --failed"; systemctl --failed --no-pager 2>&1
	`}, time.Minute)
	if err == nil {
		t.Logf("diagnostics from %s:\n%s", n.spec.Name, r.Output())
		return
	}
	console, cerr := os.ReadFile(n.spec.console())
	if cerr != nil {
		t.Logf("no diagnostics from %s: %v; console: %v", n.spec.Name, err, cerr)
		return
	}
	if len(console) > 16<<10 {
		console = console[len(console)-16<<10:]
	}
	t.Logf("no SSH to %s (%v); serial console (tail):\n%s", n.spec.Name, err, console)
}

// virsh runs virsh against the system libvirt and returns its stdout.
func virsh(ctx context.Context, args ...string) (string, error) {
	return run(ctx, "", nil, "virsh", append([]string{"--connect", "qemu:///system"}, args...)...)
}
