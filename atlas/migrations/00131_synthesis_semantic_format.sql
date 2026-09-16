-- Bind the new semantic-only prompt to immutable prepared input; never rewrite historical executions.
CREATE OR REPLACE FUNCTION organizing.synthesis_model_contract_matches(
 workspace uuid, run uuid, stage text, prompt text, prompt_version text, schema_id text, schema_version text)
RETURNS boolean LANGUAGE sql STABLE AS $$
 WITH expected AS (
 SELECT CASE
 WHEN stage='VALIDATE' AND e.input_document->>'semantic_prompt_version'='v6' THEN 'v6'
 WHEN jsonb_typeof(e.input_document->'body_refresh')='object' AND p.body_refresh_request_id IS NOT NULL THEN 'v5'
 WHEN jsonb_typeof(e.input_document->'goal')='object' THEN 'v3'
 WHEN EXISTS(SELECT 1 FROM jsonb_array_elements(e.input_document->'notes') note WHERE NULLIF(note->>'publication_id','') IS NOT NULL) THEN 'v4'
 WHEN EXISTS(SELECT 1 FROM jsonb_array_elements(e.input_document->'notes') note WHERE jsonb_typeof(note->'anchor')='object') THEN 'v2'
 ELSE 'v1' END AS version
 FROM organizing.synthesis_execution e JOIN organizing.synthesis_processing p ON p.id=e.processing_id AND p.workspace_id=e.workspace_id
 WHERE e.workspace_id=workspace AND e.workflow_run_id=run AND e.input_document IS NOT NULL
 AND (NOT (e.input_document ? 'semantic_prompt_version')
      OR e.input_document->'semantic_prompt_version' IN ('""'::jsonb, '"v6"'::jsonb))
 ) SELECT EXISTS(SELECT 1 FROM expected WHERE prompt_version=version AND (
 (stage='GENERATE' AND prompt='synthesis-delta' AND schema_id='agent.synthesis-delta'
 AND schema_version=CASE version WHEN 'v5' THEN 'v3' WHEN 'v4' THEN 'v2' ELSE 'v1' END)
 OR (stage='VALIDATE' AND prompt='synthesis-semantic-review' AND schema_id='agent.synthesis-semantic-review' AND schema_version='v1')))
$$;
