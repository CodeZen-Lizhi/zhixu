-- A local file can return to earlier bytes. Content artifacts remain deduped,
-- while a new immutable observation records that return without changing the
-- original version or any historical citations. Ordinary command registrations
-- retain one canonical version per source/hash.
ALTER TABLE core.source_version
    ADD COLUMN observation_predecessor_id uuid,
    ADD CONSTRAINT source_version_observation_predecessor_fk
        FOREIGN KEY (observation_predecessor_id,source_id)
        REFERENCES core.source_version(id,source_id) ON DELETE RESTRICT,
    DROP CONSTRAINT uq_source_version_source_hash;

CREATE UNIQUE INDEX uq_source_version_source_hash
    ON core.source_version(source_id,content_hash)
    WHERE observation_predecessor_id IS NULL;
CREATE UNIQUE INDEX uq_source_version_observation_predecessor
    ON core.source_version(observation_predecessor_id)
    WHERE observation_predecessor_id IS NOT NULL;

-- Serialize the return-to-content observation with all registrations of this
-- source. Only the current predecessor can be advanced and times never change
-- on immutable historical rows.
CREATE FUNCTION core.validate_source_content_reappearance() RETURNS trigger
LANGUAGE plpgsql AS $$
DECLARE previous core.source_version%ROWTYPE;
BEGIN
    IF NEW.observation_predecessor_id IS NULL THEN RETURN NEW; END IF;
    PERFORM 1 FROM core.source WHERE id=NEW.source_id AND workspace_id=NEW.workspace_id FOR UPDATE;
    SELECT * INTO previous FROM core.source_version
        WHERE source_id=NEW.source_id AND workspace_id=NEW.workspace_id
        ORDER BY captured_at DESC,id DESC LIMIT 1;
    IF previous.id IS DISTINCT FROM NEW.observation_predecessor_id
        OR previous.content_hash=NEW.content_hash
        OR NEW.captured_at<=previous.captured_at
        OR NOT EXISTS (SELECT 1 FROM core.source_version original
            WHERE original.source_id=NEW.source_id AND original.workspace_id=NEW.workspace_id
              AND original.content_hash=NEW.content_hash AND original.observation_predecessor_id IS NULL
              AND original.byte_size=NEW.byte_size AND original.mime_type=NEW.mime_type
              AND original.content_artifact_id=NEW.content_artifact_id
              AND original.original_content_location=NEW.original_content_location)
    THEN
        RAISE EXCEPTION 'invalid source content reappearance' USING ERRCODE='23514';
    END IF;
    RETURN NEW;
END;
$$;
CREATE TRIGGER source_version_validate_reappearance
    BEFORE INSERT ON core.source_version
    FOR EACH ROW EXECUTE FUNCTION core.validate_source_content_reappearance();
