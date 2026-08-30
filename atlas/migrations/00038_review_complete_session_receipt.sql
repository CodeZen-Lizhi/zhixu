ALTER TABLE learning.review_command
    DROP CONSTRAINT review_command_command_type_check;
ALTER TABLE learning.review_command
    ADD CONSTRAINT review_command_command_type_check
    CHECK (command_type IN ('CREATE_DECK', 'CREATE_CARD', 'DECIDE_CARD', 'CHANGE_DECK_SCHEDULE', 'COMPLETE_SESSION'));
