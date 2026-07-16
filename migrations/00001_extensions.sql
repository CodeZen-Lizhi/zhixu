-- +goose Up
CREATE EXTENSION IF NOT EXISTS vector;

CREATE SCHEMA IF NOT EXISTS core;
CREATE SCHEMA IF NOT EXISTS change_control;
CREATE SCHEMA IF NOT EXISTS workflow;
CREATE SCHEMA IF NOT EXISTS retrieval;
CREATE SCHEMA IF NOT EXISTS learning;
CREATE SCHEMA IF NOT EXISTS ops;

CREATE TABLE IF NOT EXISTS core.schema_meta (
    key text PRIMARY KEY,
    value text NOT NULL,
    updated_at timestamptz NOT NULL DEFAULT now()
);

INSERT INTO core.schema_meta (key, value)
VALUES ('foundation', 'm1')
ON CONFLICT (key) DO NOTHING;

-- +goose Down
DROP TABLE IF EXISTS core.schema_meta;
DROP SCHEMA IF EXISTS ops;
DROP SCHEMA IF EXISTS learning;
DROP SCHEMA IF EXISTS retrieval;
DROP SCHEMA IF EXISTS workflow;
DROP SCHEMA IF EXISTS change_control;
DROP SCHEMA IF EXISTS core;
