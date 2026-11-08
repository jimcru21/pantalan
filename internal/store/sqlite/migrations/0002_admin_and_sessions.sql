-- There is at most one Admin, so its row is pinned to id 1. The username
-- can't be changed once created.
CREATE TABLE admin (
    id                  INTEGER PRIMARY KEY CHECK (id = 1),
    username            TEXT NOT NULL COLLATE NOCASE,
    password_hash       TEXT NOT NULL, -- PHC string
    failed_logins       INTEGER NOT NULL DEFAULT 0,
    last_failed_at      TEXT,
    created_at          TEXT NOT NULL,
    password_changed_at TEXT NOT NULL
);

-- Dashboard sessions. Only a hash of the cookie's token is kept, and
-- nothing is stored for display (no user agent or IP).
CREATE TABLE sessions (
    token_hash   TEXT PRIMARY KEY, -- hex SHA-256 of the cookie token
    created_at   TEXT NOT NULL,
    last_seen_at TEXT NOT NULL
);
