CREATE TABLE projects (
  id          TEXT PRIMARY KEY,
  path        TEXT NOT NULL UNIQUE,
  name        TEXT NOT NULL,
  last_opened INTEGER NOT NULL DEFAULT 0,
  setup_cmds  TEXT NOT NULL DEFAULT '[]' -- JSON array of strings
);

CREATE TABLE sessions (
  id                  TEXT PRIMARY KEY,
  project_id          TEXT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
  title               TEXT NOT NULL DEFAULT '',
  provider            TEXT NOT NULL,            -- claude | cursor
  provider_session_id TEXT NOT NULL DEFAULT '',
  model               TEXT NOT NULL DEFAULT '',
  mode                TEXT NOT NULL DEFAULT '',
  effort              TEXT NOT NULL DEFAULT '',
  scope_kind          TEXT NOT NULL DEFAULT 'main' CHECK (scope_kind IN ('main','worktree')),
  worktree_path       TEXT NOT NULL DEFAULT '',
  branch              TEXT NOT NULL DEFAULT '',
  base_ref            TEXT NOT NULL DEFAULT '',
  forked_from         TEXT NOT NULL DEFAULT '',
  status              TEXT NOT NULL DEFAULT 'idle',
  pr_url              TEXT NOT NULL DEFAULT '',
  pr_state            TEXT NOT NULL DEFAULT '',
  created             INTEGER NOT NULL,
  updated             INTEGER NOT NULL,
  archived_at         INTEGER
);
CREATE INDEX sessions_project ON sessions(project_id);

CREATE TABLE runs (
  id         TEXT PRIMARY KEY,
  session_id TEXT NOT NULL REFERENCES sessions(id) ON DELETE CASCADE,
  host_pid   INTEGER NOT NULL DEFAULT 0,
  socket     TEXT NOT NULL DEFAULT '',
  jsonl_path TEXT NOT NULL DEFAULT '',
  "offset"   INTEGER NOT NULL DEFAULT 0,
  status     TEXT NOT NULL DEFAULT 'starting',
  started    INTEGER NOT NULL,
  ended      INTEGER
);
CREATE INDEX runs_session ON runs(session_id);

CREATE TABLE ui_state (
  scope_key TEXT PRIMARY KEY,
  json      TEXT NOT NULL
);

CREATE TABLE kv (
  key  TEXT PRIMARY KEY,
  json TEXT NOT NULL
);
