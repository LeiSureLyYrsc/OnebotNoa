-- 0001_init: management plane (users/sessions/settings/audit) and the relay
-- entity graph (accounts = QQ instances, bots = downstream applications,
-- bindings = per-bot account grants with filter scope, listeners = dedicated
-- endpoints, endpoints = relay-dialed upstreams).

CREATE TABLE users (
    id            INTEGER PRIMARY KEY AUTOINCREMENT,
    username      TEXT    NOT NULL UNIQUE,
    password_hash TEXT    NOT NULL,
    role          TEXT    NOT NULL DEFAULT 'admin',
    created_at    INTEGER NOT NULL,
    updated_at    INTEGER NOT NULL
);

CREATE TABLE sessions (
    token_hash   TEXT    PRIMARY KEY,
    user_id      INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    csrf_token   TEXT    NOT NULL,
    created_at   INTEGER NOT NULL,
    expires_at   INTEGER NOT NULL,
    last_seen_at INTEGER NOT NULL,
    user_agent   TEXT    NOT NULL DEFAULT '',
    ip           TEXT    NOT NULL DEFAULT ''
);
CREATE INDEX idx_sessions_expires ON sessions(expires_at);
CREATE INDEX idx_sessions_user ON sessions(user_id);

CREATE TABLE settings (
    key        TEXT    PRIMARY KEY,
    value      TEXT    NOT NULL,
    updated_at INTEGER NOT NULL
);

-- One row per QQ instance. self_id is TEXT so no integer precision is gambled.
CREATE TABLE accounts (
    id           INTEGER PRIMARY KEY AUTOINCREMENT,
    self_id      TEXT    NOT NULL UNIQUE,
    name         TEXT    NOT NULL DEFAULT '',
    nickname     TEXT    NOT NULL DEFAULT '',
    avatar       TEXT    NOT NULL DEFAULT '',
    enabled      INTEGER NOT NULL DEFAULT 1,
    status       TEXT    NOT NULL DEFAULT 'offline',
    tags         TEXT    NOT NULL DEFAULT '[]',
    token_hash   TEXT,
    source       TEXT    NOT NULL DEFAULT '',
    last_seen_at INTEGER,
    created_at   INTEGER NOT NULL,
    updated_at   INTEGER NOT NULL
);
CREATE UNIQUE INDEX idx_accounts_token ON accounts(token_hash) WHERE token_hash IS NOT NULL;

-- One row per downstream application. The token is only ever stored hashed.
CREATE TABLE bots (
    id            INTEGER PRIMARY KEY AUTOINCREMENT,
    name          TEXT    NOT NULL UNIQUE,
    token_hash    TEXT    NOT NULL UNIQUE,
    enabled       INTEGER NOT NULL DEFAULT 1,
    rate_limit    TEXT    NOT NULL DEFAULT '{}',
    action_policy TEXT    NOT NULL DEFAULT '{}',
    note          TEXT    NOT NULL DEFAULT '',
    last_seen_at  INTEGER,
    created_at    INTEGER NOT NULL,
    updated_at    INTEGER NOT NULL
);

CREATE TABLE bindings (
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    bot_id     INTEGER NOT NULL REFERENCES bots(id) ON DELETE CASCADE,
    account_id INTEGER NOT NULL REFERENCES accounts(id) ON DELETE CASCADE,
    priority   INTEGER NOT NULL DEFAULT 100,
    is_default INTEGER NOT NULL DEFAULT 0,
    enabled    INTEGER NOT NULL DEFAULT 1,
    scope      TEXT    NOT NULL DEFAULT '{}',
    created_at INTEGER NOT NULL,
    updated_at INTEGER NOT NULL,
    UNIQUE (bot_id, account_id)
);
CREATE INDEX idx_bindings_account ON bindings(account_id);
CREATE INDEX idx_bindings_bot ON bindings(bot_id);

CREATE TABLE listeners (
    id            INTEGER PRIMARY KEY AUTOINCREMENT,
    name          TEXT    NOT NULL,
    kind          TEXT    NOT NULL,
    bind_addr     TEXT    NOT NULL,
    path          TEXT    NOT NULL,
    account_id    INTEGER REFERENCES accounts(id) ON DELETE SET NULL,
    bot_id        INTEGER REFERENCES bots(id) ON DELETE SET NULL,
    fixed_self_id TEXT    NOT NULL DEFAULT '',
    tls_cert      TEXT    NOT NULL DEFAULT '',
    tls_key       TEXT    NOT NULL DEFAULT '',
    enabled       INTEGER NOT NULL DEFAULT 1,
    created_at    INTEGER NOT NULL,
    updated_at    INTEGER NOT NULL
);
CREATE UNIQUE INDEX idx_listeners_addr_path ON listeners(bind_addr, path);

CREATE TABLE endpoints (
    id           INTEGER PRIMARY KEY AUTOINCREMENT,
    name         TEXT    NOT NULL,
    kind         TEXT    NOT NULL,
    url          TEXT    NOT NULL,
    mode         TEXT    NOT NULL DEFAULT 'universal',
    account_hint TEXT    NOT NULL DEFAULT '',
    bot_id       INTEGER REFERENCES bots(id) ON DELETE SET NULL,
    reconnect    TEXT    NOT NULL DEFAULT '{}',
    enabled      INTEGER NOT NULL DEFAULT 1,
    created_at   INTEGER NOT NULL,
    updated_at   INTEGER NOT NULL
);

CREATE TABLE audit_log (
    id     INTEGER PRIMARY KEY AUTOINCREMENT,
    at     INTEGER NOT NULL,
    actor  TEXT    NOT NULL DEFAULT '',
    action TEXT    NOT NULL,
    target TEXT    NOT NULL DEFAULT '',
    detail TEXT    NOT NULL DEFAULT '',
    ip     TEXT    NOT NULL DEFAULT ''
);
CREATE INDEX idx_audit_at ON audit_log(at DESC);
