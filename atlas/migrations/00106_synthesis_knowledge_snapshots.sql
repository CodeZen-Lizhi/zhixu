-- Keep the profile identity with the already immutable source-reference row.
-- Existing rows remain NULL: today's profile cannot stand in for past evidence.
ALTER TABLE organizing.synthesis_revision_source
    ADD COLUMN profile_revision_id uuid,
    ADD CONSTRAINT fk_synthesis_source_profile_revision
    FOREIGN KEY (profile_revision_id,workspace_id,source_version_id)
    REFERENCES learning.document_knowledge_profile_revision(id,workspace_id,source_version_id)
    ON DELETE RESTRICT;

CREATE FUNCTION organizing.freeze_synthesis_source_profile()
RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE
    frozen_profile uuid;
BEGIN
    -- An inherited exact source keeps its prior knowledge-point identities.
    SELECT prior.profile_revision_id INTO frozen_profile
    FROM organizing.synthesis_revision revision
    JOIN organizing.synthesis_revision_source prior
      ON prior.revision_id=revision.parent_revision_id
     AND prior.workspace_id=revision.workspace_id AND prior.note_id=revision.note_id
    WHERE revision.id=NEW.revision_id AND revision.workspace_id=NEW.workspace_id
      AND revision.note_id=NEW.note_id AND prior.source_id=NEW.source_id
      AND prior.source_version_id=NEW.source_version_id
      AND prior.parse_projection_id=NEW.parse_projection_id
      AND prior.source_span_id=NEW.source_span_id
      AND prior.content_artifact_id=NEW.content_artifact_id
      AND prior.content_hash=NEW.content_hash AND prior.excerpt_hash=NEW.excerpt_hash;

    IF frozen_profile IS NULL THEN
        SELECT profile.current_revision_id INTO frozen_profile
        FROM learning.document_knowledge_profile profile
        JOIN learning.document_knowledge_profile_revision revision
          ON revision.id=profile.current_revision_id AND revision.profile_id=profile.id
         AND revision.workspace_id=profile.workspace_id
         AND revision.source_version_id=profile.source_version_id
        WHERE profile.workspace_id=NEW.workspace_id
          AND profile.source_version_id=NEW.source_version_id
          AND profile.status IN ('READY','STALE')
          AND revision.parse_projection_id=NEW.parse_projection_id;
    END IF;
    IF NEW.profile_revision_id IS NOT NULL
       AND NEW.profile_revision_id IS DISTINCT FROM frozen_profile THEN
        RAISE EXCEPTION 'synthesis knowledge snapshot differs from owner profile'
            USING ERRCODE='23514';
    END IF;
    NEW.profile_revision_id := frozen_profile;
    RETURN NEW;
END;
$$;

CREATE TRIGGER synthesis_source_profile_snapshot
    BEFORE INSERT ON organizing.synthesis_revision_source
    FOR EACH ROW EXECUTE FUNCTION organizing.freeze_synthesis_source_profile();
