-- One row per agent per message the agent was told about, so a turn-end hook
-- tells each message once. Agent names compare without case, as Agent Mail's do.
CREATE TABLE IF NOT EXISTS told_records (
    project_key TEXT NOT NULL,
    agent_name  TEXT NOT NULL COLLATE NOCASE,
    message_id  INTEGER NOT NULL,
    told_by     TEXT NOT NULL,
    told_at     TEXT NOT NULL,
    PRIMARY KEY (project_key, agent_name, message_id)
);

CREATE INDEX IF NOT EXISTS idx_told_records_told_at ON told_records (told_at);
