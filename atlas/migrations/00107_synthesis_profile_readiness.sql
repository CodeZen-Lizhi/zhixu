-- Retain a usable immutable profile during retries and rebuilds.
CREATE OR REPLACE FUNCTION organizing.freeze_synthesis_source_profile()
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
        -- Any committed immutable revision remains valid for its exact source
        -- and parser projection, even while the mutable profile is rebuilding.
        SELECT revision.id INTO frozen_profile
        FROM learning.document_knowledge_profile_revision revision
        WHERE revision.workspace_id=NEW.workspace_id
          AND revision.source_version_id=NEW.source_version_id
          AND revision.parse_projection_id=NEW.parse_projection_id
        ORDER BY revision.created_at DESC,revision.id DESC LIMIT 1;
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

