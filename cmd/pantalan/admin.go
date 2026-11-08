package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"

	"github.com/pantalan/pantalan/internal/config"
	"github.com/pantalan/pantalan/internal/domain"
	"github.com/pantalan/pantalan/internal/password"
	"github.com/pantalan/pantalan/internal/service"
	"github.com/pantalan/pantalan/internal/store/sqlite"
)

// exitNoAdmin is the exit code of `admin show` (and `reset-password`) when
// there is no Admin, so that scripts (the installer) can tell "none yet"
// from a failure.
const exitNoAdmin = 3

// adminCmd is `pantalan admin create|show|reset-password`. None of them
// reads stdin; generated passwords are printed once, alone on stdout.
func adminCmd(ctx context.Context, inv invocation) int {
	if len(inv.args) == 0 {
		return adminUsage(inv.stdio.Err)
	}
	sub, args := inv.args[0], inv.args[1:]
	switch sub {
	case "create":
		return adminCreate(ctx, inv, args)
	case "show":
		if len(args) != 0 {
			return adminUsage(inv.stdio.Err)
		}
		// Read-only: the installer asks this before it changes the host.
		return withAdminsReadOnly(ctx, inv, func(admins *service.AdminService) int {
			admin, err := admins.Get(ctx)
			if err != nil {
				return failAdmin(inv, err)
			}
			fmt.Fprintln(inv.stdio.Out, admin.Username)
			return 0
		})
	case "reset-password":
		if len(args) != 0 {
			return adminUsage(inv.stdio.Err)
		}
		return withAdmins(ctx, inv, func(admins *service.AdminService) int {
			pw, err := admins.ResetPassword(ctx)
			if err != nil {
				return failAdmin(inv, err)
			}
			fmt.Fprintln(inv.stdio.Out, pw)
			fmt.Fprintln(inv.stdio.Err, "Password reset; every Dashboard session is signed out and sign-in throttling is cleared.")
			return 0
		})
	default:
		return adminUsage(inv.stdio.Err)
	}
}

func adminCreate(ctx context.Context, inv invocation, args []string) int {
	flags := flag.NewFlagSet("admin create", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	username := flags.String("username", "", "")
	check := flags.Bool("check", false, "")
	if err := flags.Parse(args); err != nil || flags.NArg() != 0 || !isSet(flags, "username") {
		return adminUsage(inv.stdio.Err)
	}
	if *check {
		// The installer checks input before it changes the host, so this
		// must not need (or create) the data directory.
		if _, err := domain.NormalizeUsername(*username); err != nil {
			return fail(inv, err)
		}
		return 0
	}
	return withAdmins(ctx, inv, func(admins *service.AdminService) int {
		admin, pw, err := admins.Create(ctx, *username)
		if errors.Is(err, domain.ErrConflict) {
			fmt.Fprintln(inv.stdio.Err, "pantalan: an Admin already exists; recover it with `sudo pantalan admin show` and `sudo pantalan admin reset-password`")
			return 1
		}
		if err != nil {
			return fail(inv, err)
		}
		fmt.Fprintln(inv.stdio.Out, pw)
		fmt.Fprintf(inv.stdio.Err, "Created the Admin %s. Keep the password above; it is not shown again.\n", admin.Username)
		return 0
	})
}

// withAdmins opens the database as the data directory's owner (ADR 0001)
// and hands f the Admin service on it.
func withAdmins(ctx context.Context, inv invocation, f func(*service.AdminService) int) int {
	dataDir := config.DataDir(inv.getenv)
	if err := becomeDataDirOwner(dataDir); err != nil {
		return fail(inv, err)
	}
	store, err := sqlite.Open(ctx, dataDir)
	if err != nil {
		return fail(inv, err)
	}
	defer store.Close()
	return f(service.NewAdminService(store, password.Hasher{}))
}

// withAdminsReadOnly is withAdmins on a database opened only to read: it
// isn't created, migrated or snapshotted. A missing database has no Admin.
func withAdminsReadOnly(ctx context.Context, inv invocation, f func(*service.AdminService) int) int {
	dataDir := config.DataDir(inv.getenv)
	if err := becomeDataDirOwner(dataDir); err != nil {
		return fail(inv, err)
	}
	store, err := sqlite.OpenReadOnly(ctx, dataDir)
	if err != nil {
		return failAdmin(inv, err)
	}
	defer store.Close()
	return f(service.NewAdminService(store, password.Hasher{}))
}

func isSet(flags *flag.FlagSet, name string) bool {
	set := false
	flags.Visit(func(f *flag.Flag) { set = set || f.Name == name })
	return set
}

// failAdmin is fail, except that a missing Admin gets the create hint and
// exitNoAdmin.
func failAdmin(inv invocation, err error) int {
	if errors.Is(err, domain.ErrNotFound) {
		fmt.Fprintln(inv.stdio.Err, "pantalan: no Admin yet; create one with: sudo pantalan admin create --username NAME")
		return exitNoAdmin
	}
	return fail(inv, err)
}

// fail reports err on stderr and returns exit code 1.
func fail(inv invocation, err error) int {
	fmt.Fprintln(inv.stdio.Err, "pantalan:", err)
	return 1
}

func adminUsage(w io.Writer) int {
	fmt.Fprint(w, `Usage: pantalan admin <command>

Commands:
  create --username NAME [--check]
                   create the Admin and print its generated password once;
                   with --check, only validate NAME
  show             print the Admin's username
  reset-password   print a new generated password and sign out every session
`)
	return 2
}
