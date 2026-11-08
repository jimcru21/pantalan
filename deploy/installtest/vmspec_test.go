package installtest

// The VM substrate's pure parts (vm_test.go drives them): where each
// distro's cloud image comes from, the cloud-init seed a VM boots with, the
// virt-install command line, and reading libvirt's answers. No build tag, so
// their unit tests (vmspec_unit_test.go) run in `make check` without KVM.

import (
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"strconv"
	"strings"
)

// vmUser is the account the harness logs in to a VM as. Node.Run goes
// through `sudo`, the way an operator runs the installer over SSH.
const vmUser = "installtest"

// seedUserData is the cloud-config a VM boots with: the harness's user,
// reachable with authorizedKey only and with passwordless sudo, and curl
// (which `curl | sh` needs; the cloud images ship it, so this is a no-op
// unless one stops). Everything else is the cloud image as shipped. JSON
// is YAML, so the config is written with encoding/json.
func seedUserData(authorizedKey string) string {
	cfg := map[string]any{
		"users": []map[string]any{{
			"name":                vmUser,
			"sudo":                "ALL=(ALL) NOPASSWD:ALL",
			"shell":               "/bin/bash",
			"lock_passwd":         true,
			"ssh_authorized_keys": []string{authorizedKey},
		}},
		"ssh_pwauth": false,
		"packages":   []string{"curl"},
	}
	body, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		panic(err) // a map of strings and bools always marshals
	}
	return "#cloud-config\n" + string(body) + "\n"
}

// seedMetaData is the NoCloud meta-data. The instance ID stays the same
// across reboots, so cloud-init doesn't redo its first-boot work.
func seedMetaData(instanceID, hostname string) string {
	return "instance-id: " + instanceID + "\nlocal-hostname: " + hostname + "\n"
}

// cloudImage is where a distro's official x64 cloud image is published.
type cloudImage struct {
	URL  string // the qcow2 image
	Sums string // the checksum file next to it: SHA256SUMS or SHA512SUMS
}

// file is the image's file name, as the checksums list it.
func (c cloudImage) file() string { return c.URL[strings.LastIndex(c.URL, "/")+1:] }

// sumsURL is the checksum file's URL, in the image's directory.
func (c cloudImage) sumsURL() string { return c.URL[:strings.LastIndex(c.URL, "/")+1] + c.Sums }

// expectedSum is file's checksum in sums, a sha256sum/sha512sum listing
// ("<hex>  <name>" or "<hex> *<name>").
func expectedSum(sums, file string) (string, error) {
	for _, line := range strings.Split(sums, "\n") {
		fields := strings.Fields(line)
		if len(fields) == 2 && strings.TrimPrefix(fields[1], "*") == file {
			return fields[0], nil
		}
	}
	return "", fmt.Errorf("the checksums list no %s", file)
}

// vmSpec is one VM's files and identity. Every file lives in Dir, which
// libvirt's QEMU must be able to reach (see vm_test.go).
type vmSpec struct {
	Name      string // the libvirt domain, unique per Boot
	Dir       string // this VM's own directory
	BaseImage string // the verified cloud image its disk is an overlay on
	MAC       string // its NIC's address, to find its DHCP lease by
}

const (
	vmMemoryMiB = "2048"
	vmVCPUs     = "2"
	vmDiskSize  = "20G"
)

func (s vmSpec) disk() string    { return s.Dir + "/disk.qcow2" }
func (s vmSpec) seed() string    { return s.Dir + "/seed.iso" }
func (s vmSpec) console() string { return s.Dir + "/console.log" }

// overlayArgs is `qemu-img` making the VM's disk: copy-on-write over the
// cloud image, so every Boot starts from the image as shipped, grown to
// vmDiskSize (cloud-init grows the root filesystem to fit at first boot).
func (s vmSpec) overlayArgs() []string {
	return []string{"create", "-q", "-f", "qcow2", "-F", "qcow2", "-b", s.BaseImage, s.disk(), vmDiskSize}
}

// seedArgs is `cloud-localds` making the NoCloud seed ISO from the
// user-data and meta-data files in Dir. The seed stays attached, so
// reboots find the same instance.
func (s vmSpec) seedArgs() []string {
	return []string{s.seed(), s.Dir + "/user-data", s.Dir + "/meta-data"}
}

// virtInstallArgs is `virt-install` defining and starting the VM on
// libvirt's default NAT network (virbr0), which the machine running the
// tests is on: that's where the release server listens and where SSH from
// outside comes from. The serial console goes to a file, for when a VM
// never comes up.
func (s vmSpec) virtInstallArgs() []string {
	return []string{
		"--connect", "qemu:///system",
		"--name", s.Name,
		"--memory", vmMemoryMiB,
		"--vcpus", vmVCPUs,
		"--import",
		"--disk", "path=" + s.disk() + ",format=qcow2,bus=virtio",
		// A virtio disk, not a CD-ROM: Debian's cloud kernel has no
		// driver for q35's SATA CD-ROM. cloud-init finds the seed by its
		// cidata label on any block device.
		"--disk", "path=" + s.seed() + ",device=disk,format=raw,bus=virtio,readonly=on",
		"--network", "network=default,model=virtio,mac=" + s.MAC,
		"--osinfo", "detect=on,require=off",
		"--graphics", "none",
		// No display, but still a video device: trixie's GRUB
		// (terminal_output gfxterm serial) can't boot a kernel without
		// one and loops on "Booting `Debian GNU/Linux'" (Debian #1111240).
		"--video", "vga",
		"--serial", "file,path=" + s.console(),
		"--noautoconsole",
	}
}

// leaseAddr is the IPv4 address `virsh domifaddr --source lease` (out)
// shows for mac, or "" while the VM has no lease yet.
func leaseAddr(out, mac string) string {
	for _, line := range strings.Split(out, "\n") {
		f := strings.Fields(line)
		if len(f) == 4 && strings.EqualFold(f[1], mac) && f[2] == "ipv4" {
			addr, _, _ := strings.Cut(f[3], "/")
			return addr
		}
	}
	return ""
}

// networkAddr is the host's IPv4 address on a libvirt network and the
// bridge it's on, from `virsh net-dumpxml`.
func networkAddr(netXML string) (addr, bridge string, err error) {
	var n struct {
		Bridge struct {
			Name string `xml:"name,attr"`
		} `xml:"bridge"`
		IPs []struct {
			Family  string `xml:"family,attr"`
			Address string `xml:"address,attr"`
		} `xml:"ip"`
	}
	if err := xml.Unmarshal([]byte(netXML), &n); err != nil {
		return "", "", fmt.Errorf("read the libvirt network: %w", err)
	}
	for _, ip := range n.IPs {
		if (ip.Family == "" || ip.Family == "ipv4") && ip.Address != "" {
			return ip.Address, n.Bridge.Name, nil
		}
	}
	return "", "", errors.New("the libvirt network has no IPv4 address")
}

// previousRelease is the newest of the published tags that is older than
// target: the release an operator upgrades to target from. "" means none
// is published yet, and the upgrade-from-the-previous-release path skips.
func previousRelease(published []string, target string) string {
	want, ok := releaseVersion(target)
	if !ok {
		return ""
	}
	best, bestV := "", [3]int{-1}
	for _, tag := range published {
		v, ok := releaseVersion(tag)
		if ok && versionLess(v, want) && versionLess(bestV, v) {
			best, bestV = tag, v
		}
	}
	return best
}

// fakeReleaseTags are the tags the harness builds the working tree as (N
// and N+1). They follow the newest published release, so the VM tier can
// upgrade from that release to N: v0.0.1 and v0.0.2 until one exists.
func fakeReleaseTags(published []string) []string {
	newest := [3]int{0, 0, 0}
	for _, tag := range published {
		if v, ok := releaseVersion(tag); ok && versionLess(newest, v) {
			newest = v
		}
	}
	return []string{
		fmt.Sprintf("v%d.%d.%d", newest[0], newest[1], newest[2]+1),
		fmt.Sprintf("v%d.%d.%d", newest[0], newest[1], newest[2]+2),
	}
}

// releaseVersion reads a plain vX.Y.Z tag, the only kind release.yml
// publishes.
func releaseVersion(tag string) ([3]int, bool) {
	var v [3]int
	parts := strings.Split(strings.TrimPrefix(tag, "v"), ".")
	if !strings.HasPrefix(tag, "v") || len(parts) != 3 {
		return v, false
	}
	for i, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil || n < 0 || p != strconv.Itoa(n) {
			return v, false
		}
		v[i] = n
	}
	return v, true
}

func versionLess(a, b [3]int) bool {
	for i := range a {
		if a[i] != b[i] {
			return a[i] < b[i]
		}
	}
	return false
}
