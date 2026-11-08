-- IDs are TEXT. Times are UTC RFC 3339 TEXT with fixed-width nanoseconds,
-- so they sort correctly as strings.
CREATE TABLE apps (
    id         TEXT PRIMARY KEY,
    name       TEXT NOT NULL,
    repo_url   TEXT NOT NULL,
    branch     TEXT NOT NULL,
    build_type TEXT NOT NULL,
    domain     TEXT NOT NULL UNIQUE COLLATE NOCASE,
    port       INTEGER NOT NULL,
    env        TEXT NOT NULL DEFAULT '{}', -- JSON object, name to value
    created_at TEXT NOT NULL
);

CREATE TABLE deployments (
    id           TEXT PRIMARY KEY,
    app_id       TEXT NOT NULL REFERENCES apps (id),
    ref          TEXT NOT NULL,
    commit_sha   TEXT NOT NULL DEFAULT '',
    image        TEXT NOT NULL DEFAULT '',
    container_id TEXT NOT NULL DEFAULT '',
    upstream     TEXT NOT NULL DEFAULT '',
    status       TEXT NOT NULL,
    error        TEXT NOT NULL DEFAULT '',
    created_at   TEXT NOT NULL,
    finished_at  TEXT
);

CREATE INDEX deployments_by_app ON deployments (app_id, created_at);
