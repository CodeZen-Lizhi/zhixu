-- Bind the fusion-only model contract to its immutable approved target. Older contracts are unchanged.
CREATE OR REPLACE FUNCTION organizing.synthesis_model_contract_matches(
 workspace uuid, run uuid, stage text, prompt text, prompt_version text, schema_id text, schema_version text)
RETURNS boolean LANGUAGE sql STABLE AS $$
 WITH original AS (
 SELECT CASE
 WHEN jsonb_typeof(e.input_document->'body_refresh')='object' AND p.body_refresh_request_id IS NOT NULL THEN 'v5'
 WHEN jsonb_typeof(e.input_document->'goal')='object' THEN 'v3'
 WHEN EXISTS(SELECT 1 FROM jsonb_array_elements(e.input_document->'notes') note WHERE NULLIF(note->>'publication_id','') IS NOT NULL) THEN 'v4'
 WHEN EXISTS(SELECT 1 FROM jsonb_array_elements(e.input_document->'notes') note WHERE jsonb_typeof(note->'anchor')='object') THEN 'v2'
 ELSE 'v1' END AS version,
 COALESCE(e.input_document->>'generation_prompt_version','') AS generation,
 COALESCE(e.input_document->>'semantic_prompt_version','') AS semantic,
 (p.fusion_request_id IS NOT NULL
 AND (e.input_document#>>'{source_event,fusion,request_id}')=p.fusion_request_id::text
 AND jsonb_array_length(e.input_document->'notes')=1
 AND (e.input_document#>>'{source_event,fusion,note_id}')=(e.input_document#>>'{notes,0,note,id}')
 AND (e.input_document#>>'{source_event,fusion,anchor_id}')=(e.input_document#>>'{notes,0,anchor,anchor_id}')
 AND (e.input_document#>'{source_event,fusion,scope_version}')=(e.input_document#>'{notes,0,anchor,scope_version}')
 AND jsonb_typeof((e.input_document#>'{source_event,fusion,allowed_sources}'))='array'
 AND jsonb_typeof((e.input_document#>'{notes,0,anchor,allowed_sources}'))='array'
 AND jsonb_array_length((e.input_document#>'{source_event,fusion,allowed_sources}'))=jsonb_array_length((e.input_document#>'{notes,0,anchor,allowed_sources}'))
 AND (e.input_document#>'{source_event,fusion,allowed_sources}') @> (e.input_document#>'{notes,0,anchor,allowed_sources}')
 AND (e.input_document#>'{notes,0,anchor,allowed_sources}') @> (e.input_document#>'{source_event,fusion,allowed_sources}')) IS TRUE AS fusion_bound
 FROM organizing.synthesis_execution e JOIN organizing.synthesis_processing p ON p.id=e.processing_id AND p.workspace_id=e.workspace_id
 WHERE e.workspace_id=workspace AND e.workflow_run_id=run AND e.input_document IS NOT NULL
 AND (NOT (e.input_document ? 'generation_prompt_version') OR jsonb_typeof(e.input_document->'generation_prompt_version')='string')
 AND (NOT (e.input_document ? 'semantic_prompt_version') OR jsonb_typeof(e.input_document->'semantic_prompt_version')='string')
 ), paired AS (
 SELECT * FROM original WHERE
 (generation='' AND semantic IN ('','v6')) OR
 (semantic='v7' AND generation=CASE version WHEN 'v1' THEN 'v7' WHEN 'v2' THEN 'v8' WHEN 'v3' THEN 'v9' WHEN 'v4' THEN 'v10' WHEN 'v5' THEN '' END) OR
 (semantic='v8' AND fusion_bound AND generation=CASE version WHEN 'v2' THEN 'v11' WHEN 'v4' THEN 'v12' END)
 ), expected AS (
 SELECT version AS original_version,
 CASE WHEN stage='GENERATE' AND generation<>'' THEN generation
      WHEN stage='VALIDATE' AND semantic<>'' THEN semantic ELSE version END AS version
 FROM paired
 ) SELECT EXISTS(SELECT 1 FROM expected WHERE prompt_version=version AND (
 (stage='GENERATE' AND prompt='synthesis-delta' AND schema_id='agent.synthesis-delta'
 AND schema_version=CASE original_version WHEN 'v5' THEN 'v3' WHEN 'v4' THEN 'v2' ELSE 'v1' END)
 OR (stage='VALIDATE' AND prompt='synthesis-semantic-review' AND schema_id='agent.synthesis-semantic-review' AND schema_version='v1')))
$$;
