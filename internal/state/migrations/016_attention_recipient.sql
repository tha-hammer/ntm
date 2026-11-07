-- Names the agent a mail event is addressed to, so one agent's events can be
-- queried. Existing rows and events from other publishers stay empty.
ALTER TABLE attention_events ADD COLUMN recipient_project TEXT NOT NULL DEFAULT '';
ALTER TABLE attention_events ADD COLUMN recipient_agent TEXT NOT NULL DEFAULT '';
ALTER TABLE attention_events ADD COLUMN message_id INTEGER;

CREATE INDEX IF NOT EXISTS idx_attention_events_recipient
    ON attention_events(recipient_agent COLLATE NOCASE, cursor);
