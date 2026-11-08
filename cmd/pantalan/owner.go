package main

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/user"
	"strconv"
	"syscall"
)

// ids is a uid/gid pair.
type ids struct{ UID, GID int }

// ownerSwitch decides whether a command about to open dataDir, running with
// effective uid euid, must first switch to the directory owner's identity
// (ADR 0001): when run as root (`sudo pantalan …`) it must not leave
// root-owned files the service can't write. Not being root is a no-op, and so
// is a root-owned directory. A missing directory is an error as root, so that
// root never creates it.
func ownerSwitch(euid int, dataDir string, ownerOf func(string) (ids, error)) (ids, bool, error) {
	if euid != 0 {
		return ids{}, false, nil
	}
	owner, err := ownerOf(dataDir)
	if errors.Is(err, fs.ErrNotExist) {
		return ids{}, false, fmt.Errorf("data directory %s does not exist; install Pantalan first", dataDir)
	}
	if err != nil {
		return ids{}, false, fmt.Errorf("data directory: %w", err)
	}
	if owner.UID == 0 {
		return ids{}, false, nil
	}
	return owner, true, nil
}

// becomeDataDirOwner applies ownerSwitch to this process.
func becomeDataDirOwner(dataDir string) error {
	to, ok, err := ownerSwitch(os.Geteuid(), dataDir, statOwner)
	if err != nil || !ok {
		return err
	}
	// Drop root's supplementary groups for the owner's own (the service user
	// is in the docker group), then the gid while still allowed to, then the
	// uid. Go applies each to every thread of the process.
	if err := syscall.Setgroups(groupsOf(to)); err != nil {
		return fmt.Errorf("switch to data directory owner: setgroups: %w", err)
	}
	if err := syscall.Setgid(to.GID); err != nil {
		return fmt.Errorf("switch to data directory owner: setgid %d: %w", to.GID, err)
	}
	if err := syscall.Setuid(to.UID); err != nil {
		return fmt.Errorf("switch to data directory owner: setuid %d: %w", to.UID, err)
	}
	return nil
}

func statOwner(path string) (ids, error) {
	info, err := os.Stat(path)
	if err != nil {
		return ids{}, err
	}
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return ids{}, fmt.Errorf("cannot read the owner of %s", path)
	}
	return ids{UID: int(st.Uid), GID: int(st.Gid)}, nil
}

// groupsOf lists the owner's groups from the user database, falling back to
// the directory's group alone when the uid has no entry.
func groupsOf(to ids) []int {
	groups := []int{to.GID}
	u, err := user.LookupId(strconv.Itoa(to.UID))
	if err != nil {
		return groups
	}
	gids, err := u.GroupIds()
	if err != nil {
		return groups
	}
	for _, g := range gids {
		if n, err := strconv.Atoi(g); err == nil && n != to.GID {
			groups = append(groups, n)
		}
	}
	return groups
}
