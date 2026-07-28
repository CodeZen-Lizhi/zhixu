-- +goose Up

ALTER TABLE learning.review_command
    DROP CONSTRAINT review_command_command_type_check;
ALTER TABLE learning.review_command
    ADD CONSTRAINT review_command_command_type_check
    CHECK (command_type IN ('CREATE_DECK', 'CREATE_CARD', 'DECIDE_CARD', 'CHANGE_DECK_SCHEDULE', 'COMPLETE_SESSION'));

-- +goose Down

-- A downgrade cannot silently discard a receipt that the older constraint
-- cannot represent. Operators must archive it before moving the schema back.
-- +goose StatementBegin
DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM learning.review_command WHERE command_type = 'COMPLETE_SESSION') THEN
        RAISE EXCEPTION 'complete review session receipts exist; archive before rollback'
            USING ERRCODE = '55000';
    END IF;
END
$$;
-- +goose StatementEnd

ALTER TABLE learning.review_command
    DROP CONSTRAINT review_command_command_type_check;
ALTER TABLE learning.review_command
    ADD CONSTRAINT review_command_command_type_check
    CHECK (command_type IN ('CREATE_DECK', 'CREATE_CARD', 'DECIDE_CARD', 'CHANGE_DECK_SCHEDULE'));
