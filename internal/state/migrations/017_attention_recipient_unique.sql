-- Allows one mail event per project, recipient and message. A build with only
-- migration 016 wrote one event per dashboard restart, so duplicates are removed
-- first, keeping the oldest. Rows with no message id aren't addressed and are left
-- alone.
DELETE FROM attention_events
WHERE message_id IS NOT NULL
  AND cursor NOT IN (
    SELECT MIN(cursor) FROM attention_events
    WHERE message_id IS NOT NULL
    GROUP BY recipient_project, recipient_agent COLLATE NOCASE, message_id
  );

CREATE UNIQUE INDEX IF NOT EXISTS idx_attention_events_addressed
    ON attention_events(recipient_project, recipient_agent COLLATE NOCASE, message_id)
    WHERE message_id IS NOT NULL;
