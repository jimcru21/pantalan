# One SQLite file, shared directly by the service and the CLI

All persistent state (the Admin, sessions, login-failure counts, apps, deployments) lives in one SQLite file, `$PANTALAN_DATA_DIR/pantalan.db`. The running service and `pantalan admin …` subcommands both open it directly; WAL mode plus `busy_timeout` serialises their writes, and the service never caches auth state in memory, so a `reset-password` run while the service is up takes effect on its next request. Every process that opens the file applies pending migrations (embedded, numbered, forward-only, versioned in `PRAGMA user_version`) in one transaction, after a `VACUUM INTO` snapshot, and refuses to start against a schema newer than it knows.

## Considered Options

- **CLI talks to the running service over a root-only unix socket.** Rejected: a second, privileged API surface to secure and version, for one or two commands.
- **CLI requires the service to be stopped.** Rejected: recovering from an Admin lockout would also take the Dashboard down.
- **Only an explicit `pantalan migrate` step, run by the installer.** Rejected: the CLI subcommands open the database too, and a manually started binary would run against an old schema.
- **A migration library (goose, golang-migrate).** Rejected: a dependency to replace roughly 40 lines.

## Consequences

- Auth reads hit SQLite on every request; adding an in-process cache of the Admin or sessions breaks `reset-password` while the service runs.
- No down migrations: rolling back a release means restoring a `pantalan.db.bak-<version>` snapshot (the two most recent are kept).
- The in-memory store is gone; tests run against SQLite in a temp directory.
- The service runs as the `pantalan` user, but the CLI is run as `sudo pantalan …`. A subcommand running as root switches to the data directory owner's uid/gid before opening the file, so it never leaves root-owned `-wal`, `-shm` or `.bak-*` files the service can't write.
- `admin show` is the one exception to "every process migrates": it opens the file read-only and never migrates or snapshots it, so the installer can ask whether an Admin exists before it changes anything on the host. It still refuses a schema newer than it knows, so the `admin` table's username must stay readable by every later release.
- Two processes opening a brand-new file at once race to switch it to WAL, and SQLite answers the loser with `SQLITE_BUSY` without waiting out `busy_timeout`; opening retries the migration while it meets `SQLITE_BUSY`, for up to 15 s.
