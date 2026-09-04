-- Soulacy Postgres schema — managed by goose migrations.
-- Apply with: goose -dir internal/storage/postgres postgres <DSN> up
--
-- +goose Up

CREATE TABLE IF NOT EXISTS agent_events (
    id          BIGSERIAL PRIMARY KEY,
    agent_id    TEXT        NOT NULL,
    session_id  TEXT        NOT NULL DEFAULT '',
    type        TEXT        NOT NULL,
    -- JSONB lets us index into payload fields later (e.g. GIN index on payload).
    payload     JSONB,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_events_agent_created
    ON agent_events (agent_id, created_at DESC);

CREATE INDEX IF NOT EXISTS idx_events_session
    ON agent_events (agent_id, session_id, created_at);

CREATE INDEX IF NOT EXISTS idx_events_type
    ON agent_events (type, created_at DESC);

-- ---------------------------------------------------------------------------

CREATE TABLE IF NOT EXISTS memories (
    id          TEXT        PRIMARY KEY,
    agent_id    TEXT        NOT NULL,
    session_id  TEXT        NOT NULL DEFAULT '',
    scope       TEXT        NOT NULL,
    provenance  TEXT        NOT NULL,
    key         TEXT,
    content     TEXT        NOT NULL,
    metadata    JSONB,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    expires_at  TIMESTAMPTZ
);

CREATE INDEX IF NOT EXISTS idx_memories_agent
    ON memories (agent_id, created_at DESC);

CREATE INDEX IF NOT EXISTS idx_memories_session
    ON memories (agent_id, session_id);

CREATE INDEX IF NOT EXISTS idx_memories_scope
    ON memories (scope);

-- +goose Down

DROP TABLE IF EXISTS memories;
DROP TABLE IF EXISTS agent_events;
