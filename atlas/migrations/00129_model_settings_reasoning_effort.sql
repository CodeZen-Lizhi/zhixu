-- Freeze reasoning effort with each append-only model settings revision.
-- An empty value preserves all pre-existing revisions' provider defaults.
ALTER TABLE ops.model_settings_revisions
    ADD COLUMN chat_reasoning_effort text NOT NULL DEFAULT '',
    ADD CONSTRAINT model_settings_chat_reasoning_effort_check
        CHECK (chat_reasoning_effort IN ('', 'low', 'medium', 'high', 'xhigh', 'max')),
    ADD CONSTRAINT model_settings_chat_reasoning_provider_check
        CHECK (chat_provider = 'openai-compatible' OR chat_reasoning_effort = '');
