-- Initial schema (PLAN.md section 10).
--
-- Conventions:
--   * timestamps are INTEGER unix milliseconds (UTC); NULL = not set;
--   * sizes/offsets are INTEGER bytes, -1 = unknown;
--   * booleans are INTEGER 0/1 with CHECK constraints;
--   * enums are TEXT with CHECK constraints matching the Go constants
--     (core.State, accounts.Kind, hosters.QuotaScope ...).
--   * secrets are NEVER stored here: accounts.secret_ref / egresses.secret_ref
--     point into the SecretStore (keyring or vault).

-- +goose Up

CREATE TABLE settings (
    key        TEXT PRIMARY KEY,
    value      TEXT NOT NULL,
    updated_at INTEGER NOT NULL
) STRICT;

CREATE TABLE accounts (
    id             TEXT PRIMARY KEY,                   -- random UUID, SecretStore key
    hoster         TEXT NOT NULL,
    label          TEXT NOT NULL DEFAULT '',
    kind           TEXT NOT NULL CHECK (kind IN ('password', 'cookie', 'oauth', 'apikey')),
    username       TEXT NOT NULL DEFAULT '',
    enabled        INTEGER NOT NULL DEFAULT 1 CHECK (enabled IN (0, 1)),
    allow_rotation INTEGER NOT NULL DEFAULT 0 CHECK (allow_rotation IN (0, 1)),
    secret_ref     TEXT NOT NULL DEFAULT '',
    status         TEXT NOT NULL DEFAULT 'unknown',
    status_msg     TEXT NOT NULL DEFAULT '',
    premium        INTEGER NOT NULL DEFAULT 0 CHECK (premium IN (0, 1)),
    premium_until  INTEGER,
    quota_total    INTEGER NOT NULL DEFAULT -1,
    quota_used     INTEGER NOT NULL DEFAULT -1,
    checked_at     INTEGER,
    created_at     INTEGER NOT NULL,
    updated_at     INTEGER NOT NULL
) STRICT;
CREATE INDEX accounts_hoster ON accounts (hoster, enabled);

-- Egresses discovered at runtime (imported proxy lists, WireGuard confs, Tor
-- circuits). Config-defined commands (router script, system VPN) are NOT
-- stored here: they only live in config.toml.
CREATE TABLE egresses (
    id            TEXT PRIMARY KEY,                    -- stable id, e.g. "socks5:host:port"
    kind          TEXT NOT NULL CHECK (kind IN ('direct', 'http', 'socks5', 'tor', 'wireguard', 'router', 'systemvpn')),
    label         TEXT NOT NULL DEFAULT '',
    source        TEXT NOT NULL DEFAULT 'config' CHECK (source IN ('config', 'import', 'auto')),
    address       TEXT NOT NULL DEFAULT '',            -- proxy URL without credentials, conf path...
    settings      TEXT NOT NULL DEFAULT '{}',          -- kind-specific non-secret JSON
    secret_ref    TEXT NOT NULL DEFAULT '',            -- credentials in the SecretStore, if any
    enabled       INTEGER NOT NULL DEFAULT 1 CHECK (enabled IN (0, 1)),
    healthy       INTEGER NOT NULL DEFAULT 1 CHECK (healthy IN (0, 1)),
    public_ip     TEXT NOT NULL DEFAULT '',
    last_error    TEXT NOT NULL DEFAULT '',
    last_check_at INTEGER,
    created_at    INTEGER NOT NULL,
    updated_at    INTEGER NOT NULL
) STRICT;

-- QuotaBook: (hoster, scope, subject) exhausted until `until`.
-- subject = egress id (scope ip), account id (scope account) or hoster file
-- id (scope file).
CREATE TABLE quota_marks (
    hoster     TEXT NOT NULL,
    scope      TEXT NOT NULL CHECK (scope IN ('ip', 'account', 'file')),
    subject    TEXT NOT NULL,
    until      INTEGER NOT NULL,
    reason     TEXT NOT NULL DEFAULT '',
    created_at INTEGER NOT NULL,
    PRIMARY KEY (hoster, scope, subject)
) STRICT;
CREATE INDEX quota_marks_until ON quota_marks (until);

-- One package per shared folder or per batch of links.
CREATE TABLE packages (
    id            INTEGER PRIMARY KEY AUTOINCREMENT,
    name          TEXT NOT NULL,
    dest_dir      TEXT NOT NULL,                       -- absolute directory of the package
    source_url    TEXT NOT NULL DEFAULT '',            -- folder link, for rescans
    hoster        TEXT NOT NULL DEFAULT '',
    is_folder     INTEGER NOT NULL DEFAULT 0 CHECK (is_folder IN (0, 1)),
    priority      INTEGER NOT NULL DEFAULT 0,
    exists_policy TEXT NOT NULL DEFAULT 'skip' CHECK (exists_policy IN ('skip', 'overwrite', 'rename')),
    created_at    INTEGER NOT NULL,
    updated_at    INTEGER NOT NULL
) STRICT;

CREATE TABLE downloads (
    id             INTEGER PRIMARY KEY AUTOINCREMENT,
    package_id     INTEGER NOT NULL REFERENCES packages (id) ON DELETE CASCADE,
    position       INTEGER NOT NULL DEFAULT 0,         -- order inside the package
    hoster         TEXT NOT NULL,
    url            TEXT NOT NULL,                      -- FileInfo.Link (re-resolve on resume)
    file_id        TEXT NOT NULL DEFAULT '',           -- FileInfo.ID
    name           TEXT NOT NULL,
    rel_path       TEXT NOT NULL DEFAULT '',           -- sanitized, slash separated, includes name
    dest_path      TEXT NOT NULL DEFAULT '',           -- absolute final path
    meta           TEXT NOT NULL DEFAULT '{}',         -- FileInfo.Meta JSON (non-secret)
    size           INTEGER NOT NULL DEFAULT -1,        -- remote size
    done_bytes     INTEGER NOT NULL DEFAULT 0,         -- bytes fsynced to disk
    resumable      INTEGER CHECK (resumable IN (0, 1)), -- NULL = unknown
    etag           TEXT NOT NULL DEFAULT '',
    last_modified  TEXT NOT NULL DEFAULT '',
    checksum_algo  TEXT NOT NULL DEFAULT '',
    checksum_value TEXT NOT NULL DEFAULT '',
    state          TEXT NOT NULL DEFAULT 'queued' CHECK (state IN (
                       'queued', 'resolving', 'waiting', 'downloading', 'verifying',
                       'postprocessing', 'completed', 'failed', 'paused')),
    error          TEXT NOT NULL DEFAULT '',
    error_kind     TEXT NOT NULL DEFAULT '',
    attempts       INTEGER NOT NULL DEFAULT 0,
    priority       INTEGER NOT NULL DEFAULT 0,
    egress_id      TEXT NOT NULL DEFAULT '',           -- no FK: direct/config egresses are not rows
    public_ip      TEXT NOT NULL DEFAULT '',
    account_id     TEXT REFERENCES accounts (id) ON DELETE SET NULL,
    waiting_until  INTEGER,
    created_at     INTEGER NOT NULL,
    updated_at     INTEGER NOT NULL,
    started_at     INTEGER,
    completed_at   INTEGER,
    CHECK (done_bytes >= 0),
    CHECK (size = -1 OR size >= 0)
) STRICT;
CREATE INDEX downloads_package ON downloads (package_id, position);
CREATE INDEX downloads_state ON downloads (state);
CREATE INDEX downloads_account ON downloads (account_id);
-- Dedupe/rescan: a hoster file appears at most once per package.
CREATE UNIQUE INDEX downloads_package_file ON downloads (package_id, hoster, file_id) WHERE file_id <> '';
CREATE INDEX downloads_dest_path ON downloads (dest_path) WHERE dest_path <> '';

-- Byte ranges of a download. [start_off, end_off) with end_off = -1 for an
-- open-ended single stream (no Range support / unknown size). done counts
-- bytes from start_off that are fsynced to the .part file.
CREATE TABLE segments (
    download_id INTEGER NOT NULL REFERENCES downloads (id) ON DELETE CASCADE,
    idx         INTEGER NOT NULL,
    start_off   INTEGER NOT NULL CHECK (start_off >= 0),
    end_off     INTEGER NOT NULL,
    done        INTEGER NOT NULL DEFAULT 0 CHECK (done >= 0),
    PRIMARY KEY (download_id, idx),
    CHECK (end_off = -1 OR end_off >= start_off),
    CHECK (end_off = -1 OR done <= end_off - start_off)
) STRICT;

-- Finished / failed / deleted downloads, for duplicate detection
-- (clipboard monitor, LinkGrabber "known") and the history view.
CREATE TABLE history (
    id             INTEGER PRIMARY KEY AUTOINCREMENT,
    url            TEXT NOT NULL,
    hoster         TEXT NOT NULL DEFAULT '',
    file_id        TEXT NOT NULL DEFAULT '',
    name           TEXT NOT NULL,
    package_name   TEXT NOT NULL DEFAULT '',
    dest_path      TEXT NOT NULL DEFAULT '',
    size           INTEGER NOT NULL DEFAULT -1,
    checksum_algo  TEXT NOT NULL DEFAULT '',
    checksum_value TEXT NOT NULL DEFAULT '',
    outcome        TEXT NOT NULL CHECK (outcome IN ('completed', 'failed', 'deleted')),
    error          TEXT NOT NULL DEFAULT '',
    finished_at    INTEGER NOT NULL
) STRICT;
CREATE INDEX history_url ON history (url);
CREATE INDEX history_hoster_file ON history (hoster, file_id) WHERE file_id <> '';
CREATE INDEX history_finished ON history (finished_at);

-- LinkGrabber: pasted links and their (possibly partial) expansion.
CREATE TABLE linkgrabber_entries (
    id           INTEGER PRIMARY KEY AUTOINCREMENT,
    url          TEXT NOT NULL UNIQUE,
    hoster       TEXT NOT NULL DEFAULT '',
    state        TEXT NOT NULL DEFAULT 'pending' CHECK (state IN ('pending', 'resolving', 'ready', 'error', 'unsupported')),
    error        TEXT NOT NULL DEFAULT '',
    package_name TEXT NOT NULL DEFAULT '',
    is_folder    INTEGER NOT NULL DEFAULT 0 CHECK (is_folder IN (0, 1)),
    source       TEXT NOT NULL DEFAULT '',
    auto_confirm INTEGER NOT NULL DEFAULT 0 CHECK (auto_confirm IN (0, 1)),
    dest_dir     TEXT NOT NULL DEFAULT '',
    created_at   INTEGER NOT NULL,
    updated_at   INTEGER NOT NULL
) STRICT;

CREATE TABLE linkgrabber_files (
    entry_id       INTEGER NOT NULL REFERENCES linkgrabber_entries (id) ON DELETE CASCADE,
    idx            INTEGER NOT NULL,
    file_id        TEXT NOT NULL DEFAULT '',
    name           TEXT NOT NULL,
    rel_path       TEXT NOT NULL DEFAULT '',
    size           INTEGER NOT NULL DEFAULT -1,
    checksum_algo  TEXT NOT NULL DEFAULT '',
    checksum_value TEXT NOT NULL DEFAULT '',
    link           TEXT NOT NULL DEFAULT '',
    meta           TEXT NOT NULL DEFAULT '{}',
    PRIMARY KEY (entry_id, idx)
) STRICT;

-- +goose Down
DROP TABLE linkgrabber_files;
DROP TABLE linkgrabber_entries;
DROP TABLE history;
DROP TABLE segments;
DROP TABLE downloads;
DROP TABLE packages;
DROP TABLE quota_marks;
DROP TABLE egresses;
DROP TABLE accounts;
DROP TABLE settings;
