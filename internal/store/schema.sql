-- rex-tg schema, version 1. See docs/design.md section 10.
-- PRAGMAs (WAL, foreign_keys, busy_timeout) are set by Open, not here.

CREATE TABLE conversation (
  id            INTEGER PRIMARY KEY,
  runtime       TEXT NOT NULL CHECK (runtime IN ('pi', 'claude', 'codex')),
  native_id     TEXT NOT NULL,
  transcript    TEXT NOT NULL,
  cwd           TEXT,
  title         TEXT,
  thread_id     INTEGER REFERENCES thread (id),
  first_seen_at INTEGER NOT NULL,
  UNIQUE (runtime, native_id)
) STRICT;

CREATE TABLE pane (
  id                   INTEGER PRIMARY KEY,
  rex_host             TEXT NOT NULL,
  rex_block            TEXT NOT NULL,
  rex_session          TEXT NOT NULL,
  label                TEXT,
  conversation_id      INTEGER REFERENCES conversation (id),
  last_conversation_id INTEGER REFERENCES conversation (id),
  pid                  INTEGER,
  process_name         TEXT,
  source               TEXT CHECK (source IN ('hook', 'scan', 'guess')),
  state                TEXT,
  state_kind           TEXT,
  state_at             INTEGER,
  busy_until           INTEGER,
  seen_at              INTEGER NOT NULL,
  gone_at              INTEGER,
  UNIQUE (rex_host, rex_block)
) STRICT;
CREATE UNIQUE INDEX pane_conversation ON pane (conversation_id) WHERE conversation_id IS NOT NULL;

CREATE TABLE thread (
  id                      INTEGER PRIMARY KEY,
  chat_id                 INTEGER NOT NULL,
  message_thread_id       INTEGER NOT NULL,
  current_conversation_id INTEGER NOT NULL REFERENCES conversation (id),
  name                    TEXT NOT NULL,
  mode                    TEXT NOT NULL DEFAULT 'progress' CHECK (mode IN ('progress', 'block', 'final')),
  verbose                 TEXT NOT NULL DEFAULT 'off' CHECK (verbose IN ('off', 'on', 'full')),
  reasoning               INTEGER NOT NULL DEFAULT 0 CHECK (reasoning IN (0, 1)),
  muted                   INTEGER NOT NULL DEFAULT 0 CHECK (muted IN (0, 1)),
  status                  TEXT NOT NULL DEFAULT 'live' CHECK (status IN ('live', 'offline', 'closed')),
  created_at              INTEGER NOT NULL,
  closed_at               INTEGER,
  UNIQUE (chat_id, message_thread_id)
) STRICT;
CREATE UNIQUE INDEX thread_current ON thread (current_conversation_id) WHERE status <> 'closed';

CREATE TABLE cursor (
  transcript    TEXT PRIMARY KEY,
  dev_ino       TEXT NOT NULL,
  offset        INTEGER NOT NULL,
  last_event_at INTEGER,
  updated_at    INTEGER NOT NULL
) STRICT;

CREATE TABLE outbox (
  id         INTEGER PRIMARY KEY,
  thread_id  INTEGER NOT NULL REFERENCES thread (id),
  event_key  TEXT NOT NULL,
  kind       TEXT NOT NULL,
  body       TEXT NOT NULL,
  state      TEXT NOT NULL CHECK (state IN ('pending', 'sending', 'sent', 'unknown', 'failed', 'skipped')),
  silent     INTEGER NOT NULL DEFAULT 0 CHECK (silent IN (0, 1)),
  attempts   INTEGER NOT NULL DEFAULT 0,
  last_error TEXT,
  created_at INTEGER NOT NULL,
  sent_at    INTEGER,
  UNIQUE (thread_id, event_key)
) STRICT;

CREATE TABLE outbox_part (
  outbox_id     INTEGER NOT NULL REFERENCES outbox (id),
  part          INTEGER NOT NULL,
  state         TEXT NOT NULL CHECK (state IN ('pending', 'sending', 'sent', 'unknown', 'failed')),
  content       TEXT NOT NULL,
  format        TEXT NOT NULL CHECK (format IN ('html', 'plain', 'document')),
  filename      TEXT,
  tg_message_id INTEGER,
  PRIMARY KEY (outbox_id, part)
) STRICT;
CREATE INDEX outbox_part_state ON outbox_part (state);

CREATE TABLE progress (
  thread_id     INTEGER PRIMARY KEY REFERENCES thread (id),
  turn_key      TEXT NOT NULL,
  state         TEXT NOT NULL CHECK (state IN ('waiting', 'sending', 'shown', 'unknown')),
  tg_message_id INTEGER,
  headline      TEXT,
  rows          TEXT NOT NULL DEFAULT '[]' CHECK (json_valid(rows)),
  started_at    INTEGER NOT NULL,
  edited_at     INTEGER
) STRICT;

CREATE TABLE alert (
  id            INTEGER PRIMARY KEY,
  thread_id     INTEGER NOT NULL REFERENCES thread (id),
  kind          TEXT NOT NULL CHECK (kind IN ('permission', 'question')),
  screen_hash   TEXT NOT NULL,
  options       TEXT NOT NULL CHECK (json_valid(options)),
  tg_message_id INTEGER,
  state         TEXT NOT NULL CHECK (state IN ('open', 'answered', 'gone')),
  created_at    INTEGER NOT NULL
) STRICT;

CREATE TABLE inbound (
  update_id         INTEGER PRIMARY KEY,
  chat_id           INTEGER NOT NULL,
  message_thread_id INTEGER,
  tg_message_id     INTEGER,
  from_id           INTEGER NOT NULL,
  kind              TEXT NOT NULL,
  body              TEXT NOT NULL,
  body_hash         TEXT,
  thread_id         INTEGER REFERENCES thread (id),
  state             TEXT NOT NULL CHECK (state IN ('queued', 'typing', 'delivered', 'acked', 'refused', 'expired', 'unknown', 'done')),
  reason            TEXT,
  created_at        INTEGER NOT NULL,
  delivered_at      INTEGER,
  acked_event_key   TEXT
) STRICT;
CREATE INDEX inbound_thread ON inbound (thread_id, state);

CREATE TABLE kv (key TEXT PRIMARY KEY, value TEXT NOT NULL) STRICT;
