-- +goose Up
ALTER TABLE ops.model_settings_revisions
    ADD COLUMN chat_api_style text NOT NULL DEFAULT 'chat_completions',
    ADD CONSTRAINT ops_model_settings_chat_api_style
        CHECK (chat_api_style IN ('chat_completions','responses'));

-- +goose Down
LOCK TABLE ops.model_settings_revisions IN ACCESS EXCLUSIVE MODE;

-- +goose StatementBegin
DO $$
BEGIN
    IF EXISTS (
        SELECT 1
        FROM ops.model_settings_revisions
        WHERE chat_api_style <> 'chat_completions'
        LIMIT 1
    ) THEN
        RAISE EXCEPTION 'responses model settings revisions exist; migrate them before rollback'
            USING ERRCODE = '55000';
    END IF;
END
$$;
-- +goose StatementEnd

ALTER TABLE ops.model_settings_revisions
    DROP CONSTRAINT IF EXISTS ops_model_settings_chat_api_style,
    DROP COLUMN IF EXISTS chat_api_style;
