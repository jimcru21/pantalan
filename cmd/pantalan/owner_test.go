package main

import (
	"io/fs"
	"testing"
)

// The switch itself needs root and is exercised by the installer harness;
// these cover the decision of whether, and to whom, to switch.

func ownedBy(owner ids) func(string) (ids, error) {
	return func(string) (ids, error) { return owner, nil }
}

func TestOwnerSwitchIsANoOpWhenNotRoot(t *testing.T) {
	statted := false
	ownerOf := func(string) (ids, error) { statted = true; return ids{UID: 999, GID: 999}, nil }

	_, ok, err := ownerSwitch(1000, "/var/lib/pantalan", ownerOf)

	if err != nil || ok {
		t.Errorf("ownerSwitch as uid 1000 = ok %v, err %v; want no switch", ok, err)
	}
	if statted {
		t.Error("looked up the data dir owner although not root")
	}
}

func TestOwnerSwitchAsRootBecomesTheDataDirOwner(t *testing.T) {
	to, ok, err := ownerSwitch(0, "/var/lib/pantalan", ownedBy(ids{UID: 999, GID: 998}))

	if err != nil || !ok {
		t.Fatalf("ownerSwitch as root = ok %v, err %v; want a switch", ok, err)
	}
	if to != (ids{UID: 999, GID: 998}) {
		t.Errorf("switch to %+v, want 999:998", to)
	}
}

func TestOwnerSwitchAsRootStaysRootForARootOwnedDataDir(t *testing.T) {
	_, ok, err := ownerSwitch(0, "/var/lib/pantalan", ownedBy(ids{UID: 0, GID: 0}))

	if err != nil || ok {
		t.Errorf("ownerSwitch = ok %v, err %v; want no switch", ok, err)
	}
}

func TestOwnerSwitchAsRootRefusesAMissingDataDir(t *testing.T) {
	missing := func(string) (ids, error) { return ids{}, fs.ErrNotExist }

	_, _, err := ownerSwitch(0, "/var/lib/pantalan", missing)

	if err == nil {
		t.Error("ownerSwitch with a missing data dir succeeded; want an error so root never creates it")
	}
}
