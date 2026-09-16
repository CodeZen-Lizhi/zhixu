-- Per-function overrides are immutable revision data, not a runtime side table.
CREATE FUNCTION ops.valid_model_reasoning_overrides(value jsonb) RETURNS boolean
LANGUAGE sql IMMUTABLE PARALLEL SAFE AS $$
 SELECT CASE WHEN jsonb_typeof(value) = 'object' THEN NOT EXISTS (
  SELECT 1 FROM jsonb_each(value) entry
  WHERE entry.key NOT IN ('file_profile','knowledge_organization','anchor_scope','main_note_synthesis',
   'manuscript_source_review','knowledge_qna','workspace_analysis','note_interview')
   OR jsonb_typeof(entry.value) <> 'string'
   OR entry.value #>> '{}' NOT IN ('','low','medium','high','xhigh','max')
 ) ELSE false END
$$;
ALTER TABLE ops.model_settings_revisions
 ADD COLUMN chat_reasoning_effort_by_function jsonb NOT NULL DEFAULT '{}'::jsonb,
 ADD CONSTRAINT model_settings_reasoning_functions_check CHECK (ops.valid_model_reasoning_overrides(chat_reasoning_effort_by_function)),
 ADD CONSTRAINT model_settings_reasoning_functions_provider_check CHECK (chat_provider='openai-compatible' OR chat_reasoning_effort_by_function='{}'::jsonb);
