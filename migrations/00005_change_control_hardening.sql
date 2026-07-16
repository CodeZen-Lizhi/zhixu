-- +goose Up
ALTER TABLE change_control.proposal
    ADD COLUMN IF NOT EXISTS idempotency_key text,
    ADD COLUMN IF NOT EXISTS request_hash text;

UPDATE change_control.proposal
SET idempotency_key = 'legacy:' || id::text,
    request_hash = repeat('0', 64)
WHERE idempotency_key IS NULL OR request_hash IS NULL;

ALTER TABLE change_control.proposal
    ALTER COLUMN idempotency_key SET NOT NULL,
    ALTER COLUMN request_hash SET NOT NULL;

ALTER TABLE change_control.proposal
    DROP CONSTRAINT IF EXISTS ck_proposal_idempotency_key,
    ADD CONSTRAINT ck_proposal_idempotency_key CHECK (btrim(idempotency_key) <> '' AND length(idempotency_key) <= 128),
    DROP CONSTRAINT IF EXISTS ck_proposal_request_hash,
    ADD CONSTRAINT ck_proposal_request_hash CHECK (request_hash ~ '^[0-9a-f]{64}$');

CREATE UNIQUE INDEX IF NOT EXISTS uq_proposal_workspace_idempotency
    ON change_control.proposal(workspace_id, idempotency_key);

INSERT INTO core.schema_meta(key,value) VALUES ('change_control','m5.1')
ON CONFLICT(key) DO UPDATE SET value=EXCLUDED.value,updated_at=now();

-- +goose Down
DROP INDEX IF EXISTS change_control.uq_proposal_workspace_idempotency;
ALTER TABLE change_control.proposal
    DROP CONSTRAINT IF EXISTS ck_proposal_request_hash,
    DROP CONSTRAINT IF EXISTS ck_proposal_idempotency_key,
    DROP COLUMN IF EXISTS request_hash,
    DROP COLUMN IF EXISTS idempotency_key;
UPDATE core.schema_meta SET value='m5',updated_at=now() WHERE key='change_control';
