package installtest

// Unit tests of the VM substrate's pure parts: no KVM, libvirt or network
// needed, so they carry no build tag and run in `make check`.

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestSeedUserDataLetsTheHarnessInAsRootThroughSudo(t *testing.T) {
	const key = "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIBb installtest"
	ud := seedUserData(key)

	body, ok := strings.CutPrefix(ud, "#cloud-config\n")
	if !ok {
		t.Fatalf("user-data doesn't start with #cloud-config:\n%s", ud)
	}
	var cfg struct {
		Users []struct {
			Name              string
			Sudo              string
			Shell             string
			LockPasswd        bool     `json:"lock_passwd"`
			SSHAuthorizedKeys []string `json:"ssh_authorized_keys"`
		}
		SSHPwauth bool `json:"ssh_pwauth"`
		Packages  []string
	}
	if err := json.Unmarshal([]byte(body), &cfg); err != nil {
		t.Fatalf("user-data isn't the JSON form of YAML: %v\n%s", err, body)
	}
	if len(cfg.Users) != 1 {
		t.Fatalf("user-data creates users %+v, want exactly the harness's", cfg.Users)
	}
	u := cfg.Users[0]
	if u.Name != vmUser || u.Sudo != "ALL=(ALL) NOPASSWD:ALL" || !u.LockPasswd || !slices.Equal(u.SSHAuthorizedKeys, []string{key}) {
		t.Errorf("the harness's user is %+v, want %s with passwordless sudo, a locked password and only key %q", u, vmUser, key)
	}
	if cfg.SSHPwauth {
		t.Errorf("user-data turns SSH password logins on")
	}
	// The installer arrives through `curl | sh`, so curl must be there.
	if !slices.Contains(cfg.Packages, "curl") {
		t.Errorf("user-data packages are %v, want curl among them", cfg.Packages)
	}

	// cloud-init itself, where it's installed, is the judge of the schema.
	if _, err := exec.LookPath("cloud-init"); err != nil {
		t.Log("cloud-init isn't installed here; schema not validated")
		return
	}
	file := filepath.Join(t.TempDir(), "user-data")
	if err := os.WriteFile(file, []byte(ud), 0o600); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command("cloud-init", "schema", "--config-file", file).CombinedOutput(); err != nil {
		t.Errorf("cloud-init schema rejects the user-data: %v\n%s", err, out)
	}
}

func TestSeedMetaDataNamesTheInstanceAndHost(t *testing.T) {
	md := seedMetaData("pantalan-installtest-debian-12-ab12cd34", "node-debian12")
	for _, want := range []string{
		"instance-id: pantalan-installtest-debian-12-ab12cd34\n",
		"local-hostname: node-debian12\n",
	} {
		if !strings.Contains(md, want) {
			t.Errorf("meta-data lacks %q:\n%s", want, md)
		}
	}
}

func TestExpectedSumFindsTheImageLineInEitherSumsFormat(t *testing.T) {
	// Excerpts of the real files: Debian's SHA512SUMS (two spaces) and
	// Ubuntu's SHA256SUMS (binary-mode `*`).
	debian := "aaaa  debian-12-generic-amd64.qcow2\n" +
		"0464b5d4  debian-12-genericcloud-amd64.qcow2\n" +
		"bbbb  debian-12-genericcloud-amd64.raw\n"
	ubuntu := "1111 *ubuntu-24.04-server-cloudimg-amd64-disk-kvm.img\n" +
		"04e8f246 *ubuntu-24.04-server-cloudimg-amd64.img\n"
	for _, c := range []struct{ sums, file, want string }{
		{debian, "debian-12-genericcloud-amd64.qcow2", "0464b5d4"},
		{ubuntu, "ubuntu-24.04-server-cloudimg-amd64.img", "04e8f246"},
	} {
		got, err := expectedSum(c.sums, c.file)
		if err != nil || got != c.want {
			t.Errorf("expectedSum(%s) = %q, %v; want %q", c.file, got, err, c.want)
		}
	}
	if got, err := expectedSum(debian, "debian-13-genericcloud-amd64.qcow2"); err == nil {
		t.Errorf("expectedSum of a file the sums don't list = %q, want an error", got)
	}
}

func TestCloudImageChecksumsAreNextToTheImage(t *testing.T) {
	img := cloudImage{URL: "https://cloud.debian.org/images/cloud/bookworm/latest/debian-12-genericcloud-amd64.qcow2", Sums: "SHA512SUMS"}
	if got, want := img.sumsURL(), "https://cloud.debian.org/images/cloud/bookworm/latest/SHA512SUMS"; got != want {
		t.Errorf("sumsURL = %q, want %q", got, want)
	}
	if got, want := img.file(), "debian-12-genericcloud-amd64.qcow2"; got != want {
		t.Errorf("file = %q, want %q", got, want)
	}
}

func TestVirtInstallBootsTheOverlayWithTheSeedOnTheDefaultNetwork(t *testing.T) {
	s := vmSpec{
		Name:      "pantalan-installtest-ubuntu-24.04-ab12cd34",
		Dir:       "/var/lib/libvirt/images/pantalan-installtest/ab12cd34",
		BaseImage: "/var/lib/libvirt/images/pantalan-installtest/ubuntu-24.04-server-cloudimg-amd64.img",
		MAC:       "52:54:00:ab:12:cd",
	}
	args := s.virtInstallArgs()
	for flag, want := range map[string]string{
		"--connect":  "qemu:///system",
		"--name":     s.Name,
		"--memory":   "2048",
		"--vcpus":    "2",
		"--network":  "network=default,model=virtio,mac=52:54:00:ab:12:cd",
		"--osinfo":   "detect=on,require=off",
		"--graphics": "none",
		"--serial":   "file,path=/var/lib/libvirt/images/pantalan-installtest/ab12cd34/console.log",
	} {
		if got := flagValues(args, flag); !slices.Equal(got, []string{want}) {
			t.Errorf("virt-install %s = %q, want %q", flag, got, want)
		}
	}
	wantDisks := []string{
		"path=/var/lib/libvirt/images/pantalan-installtest/ab12cd34/disk.qcow2,format=qcow2,bus=virtio",
		// virtio, not a CD-ROM: Debian's cloud kernel has no driver for
		// q35's SATA CD-ROM, so cloud-init would never find the seed.
		"path=/var/lib/libvirt/images/pantalan-installtest/ab12cd34/seed.iso,device=disk,format=raw,bus=virtio,readonly=on",
	}
	if got := flagValues(args, "--disk"); !slices.Equal(got, wantDisks) {
		t.Errorf("virt-install disks = %q, want %q", got, wantDisks)
	}
	// Boot the disk as it is (no installer) and return at once: Boot waits
	// for SSH itself.
	for _, flag := range []string{"--import", "--noautoconsole"} {
		if !slices.Contains(args, flag) {
			t.Errorf("virt-install has no %s: %q", flag, args)
		}
	}

	// The disk is a copy-on-write overlay on the shared cloud image, grown
	// so Docker and its images fit.
	wantOverlay := []string{"create", "-q", "-f", "qcow2", "-F", "qcow2", "-b", s.BaseImage,
		"/var/lib/libvirt/images/pantalan-installtest/ab12cd34/disk.qcow2", "20G"}
	if got := s.overlayArgs(); !slices.Equal(got, wantOverlay) {
		t.Errorf("qemu-img %q, want %q", got, wantOverlay)
	}
	wantSeed := []string{"/var/lib/libvirt/images/pantalan-installtest/ab12cd34/seed.iso",
		"/var/lib/libvirt/images/pantalan-installtest/ab12cd34/user-data",
		"/var/lib/libvirt/images/pantalan-installtest/ab12cd34/meta-data"}
	if got := s.seedArgs(); !slices.Equal(got, wantSeed) {
		t.Errorf("cloud-localds %q, want %q", got, wantSeed)
	}
}

func TestLeaseAddrIsTheIPv4OfTheVMsMAC(t *testing.T) {
	// `virsh domifaddr --source lease`, as libvirt 10 prints it.
	out := ` Name       MAC address          Protocol     Address
-------------------------------------------------------------------------------
 vnet3      52:54:00:00:00:01    ipv4         192.168.122.7/24
 vnet4      52:54:00:ab:12:cd    ipv6         fd00::5/64
 vnet4      52:54:00:ab:12:cd    ipv4         192.168.122.45/24
`
	if got := leaseAddr(out, "52:54:00:AB:12:CD"); got != "192.168.122.45" {
		t.Errorf("leaseAddr = %q, want 192.168.122.45", got)
	}
	header := " Name       MAC address          Protocol     Address\n----------\n\n"
	if got := leaseAddr(header, "52:54:00:ab:12:cd"); got != "" {
		t.Errorf("leaseAddr with no lease yet = %q, want empty", got)
	}
}

func TestNetworkAddrIsTheBridgesIPv4(t *testing.T) {
	// `virsh net-dumpxml default` on Ubuntu 24.04, with an IPv6 range added.
	xml := `<network>
  <name>default</name>
  <uuid>4c2f8a2e-0d3b-4c1e-9d2b-6f0b1b7c9a11</uuid>
  <forward mode='nat'>
    <nat>
      <port start='1024' end='65535'/>
    </nat>
  </forward>
  <bridge name='virbr0' stp='on' delay='0'/>
  <mac address='52:54:00:5e:3a:91'/>
  <ip family='ipv6' address='fd00:122::1' prefix='64'/>
  <ip address='192.168.122.1' netmask='255.255.255.0'>
    <dhcp>
      <range start='192.168.122.2' end='192.168.122.254'/>
    </dhcp>
  </ip>
</network>
`
	addr, bridge, err := networkAddr(xml)
	if err != nil || addr != "192.168.122.1" || bridge != "virbr0" {
		t.Errorf("networkAddr = %q, %q, %v; want 192.168.122.1 on virbr0", addr, bridge, err)
	}
	if _, _, err := networkAddr("<network><name>isolated</name></network>"); err == nil {
		t.Errorf("networkAddr of a network with no IPv4 address: want an error")
	}
}

func TestPreviousReleaseIsTheNewestPublishedOneOlderThanTheTarget(t *testing.T) {
	for _, c := range []struct {
		published []string
		target    string
		want      string
	}{
		{nil, "v0.1.0", ""},                                             // nothing published yet
		{[]string{"v0.1.0"}, "v0.1.0", ""},                              // only the target itself
		{[]string{"v0.9.0", "v0.10.0", "v0.2.1"}, "v0.10.1", "v0.10.0"}, // by version, not by string
		{[]string{"v1.0.0", "v0.3.0", "v0.4.2"}, "v0.4.3", "v0.4.2"},    // a newer line is not "previous"
		{[]string{"v0.3.0", "nightly", "v0.4.0-rc.1"}, "v0.5.0", "v0.3.0"},
	} {
		if got := previousRelease(c.published, c.target); got != c.want {
			t.Errorf("previousRelease(%q, %s) = %q, want %q", c.published, c.target, got, c.want)
		}
	}
}

func TestFakeReleasesAreNewerThanEveryPublishedOne(t *testing.T) {
	for _, c := range []struct {
		published []string
		want      []string
	}{
		{nil, []string{"v0.0.1", "v0.0.2"}},
		{[]string{"v0.9.0", "v0.10.3", "junk"}, []string{"v0.10.4", "v0.10.5"}},
	} {
		if got := fakeReleaseTags(c.published); !slices.Equal(got, c.want) {
			t.Errorf("fakeReleaseTags(%q) = %q, want %q", c.published, got, c.want)
		}
	}
}

// flagValues is every value given to flag in args, as `--flag value`.
func flagValues(args []string, flag string) []string {
	var values []string
	for i, a := range args {
		if a == flag && i+1 < len(args) {
			values = append(values, args[i+1])
		}
	}
	return values
}
