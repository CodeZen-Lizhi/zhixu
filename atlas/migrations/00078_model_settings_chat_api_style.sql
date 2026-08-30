ALTER TABLE ops.model_settings_revisions
    ADD COLUMN chat_api_style text NOT NULL DEFAULT 'chat_completions',
    ADD CONSTRAINT ops_model_settings_chat_api_style
        CHECK (chat_api_style IN ('chat_completions','responses'));
