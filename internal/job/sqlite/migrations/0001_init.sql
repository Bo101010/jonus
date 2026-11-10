-- +goose Up

-- jobs holds one row per generation request. params_json is the authoritative
-- copy of the engine request, so any job can be replayed exactly from it; the
-- individual columns beside it exist for listing and filtering without
-- decoding JSON.
CREATE TABLE jobs (
    id             TEXT    PRIMARY KEY,
    kind           TEXT    NOT NULL,
    state          TEXT    NOT NULL,
    engine         TEXT    NOT NULL,
    title          TEXT    NOT NULL DEFAULT '',
    prompt         TEXT    NOT NULL DEFAULT '',
    lyrics         TEXT    NOT NULL DEFAULT '',
    instrumental   INTEGER NOT NULL DEFAULT 0,
    duration_ms    INTEGER NOT NULL DEFAULT 0,
    language       TEXT    NOT NULL DEFAULT '',
    seed           INTEGER NOT NULL DEFAULT 0,
    params_json    TEXT    NOT NULL DEFAULT '{}',
    engine_task_id TEXT    NOT NULL DEFAULT '',
    phase          TEXT    NOT NULL DEFAULT '',
    progress       REAL    NOT NULL DEFAULT 0,
    error          TEXT    NOT NULL DEFAULT '',
    priority       INTEGER NOT NULL DEFAULT 0,
    attempt        INTEGER NOT NULL DEFAULT 0,
    created_at     INTEGER NOT NULL,
    started_at     INTEGER,
    finished_at    INTEGER
);

-- The queue claims work with "highest priority, then oldest first", and the
-- job list filters by state and orders by recency.
CREATE INDEX idx_jobs_queue   ON jobs(state, priority DESC, created_at);
CREATE INDEX idx_jobs_created ON jobs(created_at DESC);

CREATE TABLE tracks (
    id          TEXT    PRIMARY KEY,
    job_id      TEXT    NOT NULL REFERENCES jobs(id) ON DELETE CASCADE,
    title       TEXT    NOT NULL DEFAULT '',
    path        TEXT    NOT NULL,
    format      TEXT    NOT NULL,
    duration_ms INTEGER NOT NULL DEFAULT 0,
    size_bytes  INTEGER NOT NULL DEFAULT 0,
    sample_rate INTEGER NOT NULL DEFAULT 0,
    seed        INTEGER NOT NULL DEFAULT 0,
    is_stem     INTEGER NOT NULL DEFAULT 0,
    stem_name   TEXT    NOT NULL DEFAULT '',
    peaks_path  TEXT    NOT NULL DEFAULT '',
    created_at  INTEGER NOT NULL
);

CREATE INDEX idx_tracks_job     ON tracks(job_id);
CREATE INDEX idx_tracks_created ON tracks(created_at DESC);

-- An append-only history per job, so the UI can explain what happened to a
-- job that failed or was interrupted.
CREATE TABLE job_events (
    id      INTEGER PRIMARY KEY AUTOINCREMENT,
    job_id  TEXT    NOT NULL REFERENCES jobs(id) ON DELETE CASCADE,
    at      INTEGER NOT NULL,
    kind    TEXT    NOT NULL,
    payload TEXT    NOT NULL DEFAULT ''
);

CREATE INDEX idx_job_events_job ON job_events(job_id, id);

CREATE TABLE settings (
    key   TEXT PRIMARY KEY,
    value TEXT NOT NULL
);

-- +goose Down

DROP TABLE job_events;
DROP TABLE tracks;
DROP TABLE jobs;
DROP TABLE settings;
