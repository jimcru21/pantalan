// Package nodeinfo reads what the Dashboard shows about the Node. It is
// read-only and stdlib-only: the host from /proc and statfs, sampled in the
// background into an in-memory ring (Sampler), and Docker through the
// Engine API on its unix socket (Docker). Nothing is persisted.
package nodeinfo

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// Host reads the Node's host: /proc under ProcRoot, and statfs.
type Host struct {
	ProcRoot string // usually /proc
	// Statfs is syscall.Statfs, or a fake in tests.
	Statfs func(path string, buf *syscall.Statfs_t) error
}

// Reading is one read of the host.
type Reading struct {
	Hostname string
	OS       string // kernel name and release, e.g. "Linux 6.8.0-49-generic"
	Uptime   time.Duration
	Load     [3]float64 // 1, 5 and 15 minute load averages
	CPU      CPUTimes
	Memory   Memory
	Disk     Disk // the filesystem holding /
}

// CPUTimes are the aggregate CPU counters of /proc/stat, in clock ticks.
// A CPU % needs two of them (see Sample).
type CPUTimes struct {
	Busy, Total uint64
}

// Memory is /proc/meminfo, in bytes.
type Memory struct {
	Total, Available    uint64
	SwapTotal, SwapFree uint64
}

// Used is what the Dashboard counts as used: MemTotal − MemAvailable.
func (m Memory) Used() uint64 { return m.Total - min(m.Available, m.Total) }

// SwapUsed is the swap in use; zero means none is.
func (m Memory) SwapUsed() uint64 { return m.SwapTotal - min(m.SwapFree, m.SwapTotal) }

// Disk is one filesystem's size and the space left on it for unprivileged
// writers, in bytes.
type Disk struct {
	Path        string
	Total, Free uint64
	// FSID is the statfs filesystem ID, which SameFilesystem compares.
	FSID syscall.Fsid
}

// SameFilesystem reports whether d and o are the same filesystem, by
// their statfs filesystem IDs.
func (d Disk) SameFilesystem(o Disk) bool { return d.FSID == o.FSID }

// readDisk is statfs on path.
func readDisk(statfs func(string, *syscall.Statfs_t) error, path string) (Disk, error) {
	var st syscall.Statfs_t
	if err := statfs(path, &st); err != nil {
		return Disk{}, fmt.Errorf("statfs %s: %w", path, err)
	}
	bsize := uint64(st.Bsize)
	return Disk{Path: path, Total: uint64(st.Blocks) * bsize, Free: uint64(st.Bavail) * bsize, FSID: st.Fsid}, nil
}

// diskPath is the filesystem the host tiles report on.
const diskPath = "/"

// Read reads the host once.
func (h Host) Read() (Reading, error) {
	var r Reading
	var errs []error
	read := func(name string, parse func([]byte) error) {
		data, err := os.ReadFile(filepath.Join(h.ProcRoot, name))
		if err == nil {
			err = parse(data)
		}
		if err != nil {
			errs = append(errs, fmt.Errorf("read %s: %w", name, err))
		}
	}
	read("sys/kernel/hostname", func(b []byte) error { r.Hostname = string(bytes.TrimSpace(b)); return nil })
	var ostype, release string
	read("sys/kernel/ostype", func(b []byte) error { ostype = string(bytes.TrimSpace(b)); return nil })
	read("sys/kernel/osrelease", func(b []byte) error { release = string(bytes.TrimSpace(b)); return nil })
	r.OS = strings.TrimSpace(ostype + " " + release)
	read("uptime", func(b []byte) error { return parseUptime(b, &r.Uptime) })
	read("loadavg", func(b []byte) error { return parseLoad(b, &r.Load) })
	read("stat", func(b []byte) error { return parseStat(b, &r.CPU) })
	read("meminfo", func(b []byte) error { return parseMeminfo(b, &r.Memory) })

	statfs := h.Statfs
	if statfs == nil {
		statfs = syscall.Statfs
	}
	disk, err := readDisk(statfs, diskPath)
	if err != nil {
		errs = append(errs, err)
	}
	r.Disk = disk
	return r, errors.Join(errs...)
}

func parseUptime(b []byte, out *time.Duration) error {
	f := strings.Fields(string(b))
	if len(f) < 1 {
		return errors.New("empty")
	}
	secs, err := strconv.ParseFloat(f[0], 64)
	if err != nil {
		return err
	}
	*out = time.Duration(secs * float64(time.Second))
	return nil
}

func parseLoad(b []byte, out *[3]float64) error {
	f := strings.Fields(string(b))
	if len(f) < 3 {
		return errors.New("want three load averages")
	}
	for i := range 3 {
		v, err := strconv.ParseFloat(f[i], 64)
		if err != nil {
			return err
		}
		out[i] = v
	}
	return nil
}

// parseStat reads the aggregate "cpu" line: user nice system idle iowait
// irq softirq steal (guest time is already counted in user). Idle time is
// idle plus iowait.
func parseStat(b []byte, out *CPUTimes) error {
	line, _, _ := bytes.Cut(b, []byte("\n"))
	f := strings.Fields(string(line))
	if len(f) < 5 || f[0] != "cpu" {
		return errors.New("no aggregate cpu line")
	}
	var total, idle uint64
	for i, s := range f[1:min(len(f), 9)] {
		v, err := strconv.ParseUint(s, 10, 64)
		if err != nil {
			return err
		}
		total += v
		if i == 3 || i == 4 { // idle, iowait
			idle += v
		}
	}
	*out = CPUTimes{Busy: total - idle, Total: total}
	return nil
}

func parseMeminfo(b []byte, out *Memory) error {
	fields := map[string]*uint64{
		"MemTotal":     &out.Total,
		"MemAvailable": &out.Available,
		"SwapTotal":    &out.SwapTotal,
		"SwapFree":     &out.SwapFree,
	}
	found := 0
	sc := bufio.NewScanner(bytes.NewReader(b))
	for sc.Scan() {
		key, rest, ok := strings.Cut(sc.Text(), ":")
		dst, want := fields[key]
		if !ok || !want {
			continue
		}
		f := strings.Fields(rest)
		if len(f) < 1 {
			return fmt.Errorf("%s has no value", key)
		}
		v, err := strconv.ParseUint(f[0], 10, 64)
		if err != nil {
			return fmt.Errorf("%s: %w", key, err)
		}
		if len(f) > 1 && f[1] == "kB" {
			v *= 1024
		}
		*dst = v
		found++
	}
	if out.Total == 0 {
		return errors.New("no MemTotal")
	}
	if found < len(fields) {
		return errors.New("missing MemAvailable or swap fields")
	}
	return sc.Err()
}
