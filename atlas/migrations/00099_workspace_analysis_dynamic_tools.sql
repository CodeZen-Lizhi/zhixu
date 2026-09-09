-- TODO2 dynamic tool receipts. Historical v1 definitions, hashes and guards
-- remain unchanged. v2 uses independent strict receipt and alias checks.
ALTER TABLE workflow.tool_result_receipt DROP CONSTRAINT tool_result_receipt_exact_contract;
ALTER TABLE workflow.tool_result_receipt ADD CONSTRAINT tool_result_receipt_exact_contract CHECK (

        (
            tool_name='ReadGitStatus'
            AND tool_version=2
            AND definition_hash='b5dd1fcca72d5bb41fd9ad3f74006d3706e4184ad39e2a3409b565d1ff896bbd'
            AND output_schema_id='tool.read_git_status.output'
            AND output_schema_version=1
            AND max_output_bytes=4096
            AND max_private_binding_bytes=1024
            AND (
                server_binding_schema_id IS NULL
                OR (
                    server_binding_schema_id='tool.read_git_status.private_binding'
                    AND server_binding_schema_version=1
                )
            )
        )
        OR (
            tool_name='SearchKnowledge'
            AND tool_version=2
            AND definition_hash='db7180086adb06a18a4be8d1eb80a208fa6385c70f1a71d6d67c4f263fbd1807'
            AND output_schema_id='tool.search_knowledge.output'
            AND output_schema_version=2
            AND max_output_bytes=32768
            AND max_private_binding_bytes=16384
            AND server_binding_schema_id='tool.search_knowledge.private_binding'
            AND server_binding_schema_version=1
        )
        OR (
            tool_name='ReadSource'
            AND tool_version=3
            AND definition_hash='d41dabac535261b885e453b64e11fe5bb779838e31a255aea4798e27ead54d32'
            AND output_schema_id='tool.read_source.output'
            AND output_schema_version=2
            AND max_output_bytes=8192
            AND max_private_binding_bytes=4096
            AND server_binding_schema_id='tool.read_source.private_binding'
            AND server_binding_schema_version=1
        )
        OR (
            tool_name='ValidateCitation'
            AND tool_version=3
            AND definition_hash='bf5e643c47d57478b46d258eca250dc30e810ee7a0693569f44edaab19037d54'
            AND output_schema_id='tool.validate_citation.output'
            AND output_schema_version=2
            AND max_output_bytes=16384
            AND max_private_binding_bytes=16384
            AND server_binding_schema_id='tool.validate_citation.private_binding'
            AND server_binding_schema_version=1
        )

 OR (tool_name='ReadGitStatus' AND tool_version=3 AND definition_hash='2aa8ae138367486c4f59e1459a47c9a86f3adca78a0d5c3305849fe7f68a6e19' AND output_schema_id='tool.read_git_status.output' AND output_schema_version=1 AND max_output_bytes=4096 AND max_private_binding_bytes=1024 AND (server_binding_schema_id IS NULL OR (server_binding_schema_id='tool.read_git_status.private_binding' AND server_binding_schema_version=1)))
 OR (tool_name='SearchKnowledge' AND tool_version=3 AND definition_hash='cb3f96130645ebafc8500d6a54685a073f6b75d622f1348f194c8e88f871d832' AND output_schema_id='tool.search_knowledge.output' AND output_schema_version=3 AND max_output_bytes=32768 AND max_private_binding_bytes=16384 AND (server_binding_schema_id='tool.search_knowledge.private_binding' AND server_binding_schema_version=2))
 OR (tool_name='ReadSource' AND tool_version=4 AND definition_hash='c6d5ffdc62d1837c3ca728a862bfc1968fc553cfe0493827e51d942abab8182c' AND output_schema_id='tool.read_source.output' AND output_schema_version=3 AND max_output_bytes=8192 AND max_private_binding_bytes=4096 AND (server_binding_schema_id='tool.read_source.private_binding' AND server_binding_schema_version=2))
 OR (tool_name='ValidateCitation' AND tool_version=4 AND definition_hash='037f389043481ef4b5f8344d4ff35b4a032b1646adc03e7afff98418ac6f3333' AND output_schema_id='tool.validate_citation.output' AND output_schema_version=3 AND max_output_bytes=16384 AND max_private_binding_bytes=16384 AND (server_binding_schema_id='tool.validate_citation.private_binding' AND server_binding_schema_version=2))
);


CREATE FUNCTION workflow.workspace_analysis_dynamic_identity_valid(document jsonb)
RETURNS boolean LANGUAGE sql IMMUTABLE STRICT AS $$
 SELECT COALESCE(
   jsonb_typeof(document->'evidence_ref')='string'
   AND document->>'evidence_ref' ~ '^E([1-9]|[12][0-9]|3[0-2])$'
   AND workflow.workspace_analysis_receipt_identity_valid(document || '{"evidence_ref":"E1"}'::jsonb,1),false)
$$;

CREATE FUNCTION workflow.workspace_analysis_dynamic_citation_id(workspace uuid, document jsonb)
RETURNS text LANGUAGE sql IMMUTABLE STRICT AS $$
 SELECT 'cite-' || encode(sha256(
   convert_to(workspace::text,'UTF8') || decode('00','hex') ||
   convert_to(document->>'index_version_id','UTF8') || decode('00','hex') ||
   convert_to(document->>'chunk_id','UTF8') || decode('00','hex') ||
   convert_to(document->>'source_version_id','UTF8') || decode('00','hex') ||
   convert_to(document->>'source_span_id','UTF8')),'hex')
$$;

CREATE FUNCTION workflow.workspace_analysis_dynamic_source_bound(workspace uuid, document jsonb)
RETURNS boolean LANGUAGE sql STABLE STRICT AS $$
 SELECT document->>'citation_id'=workflow.workspace_analysis_dynamic_citation_id(workspace,document)
 AND EXISTS (
   SELECT 1 FROM retrieval.index_manifest_chunk manifest
   JOIN ingestion.canonical_chunk chunk ON chunk.id=manifest.chunk_id
     AND chunk.workspace_id=manifest.workspace_id AND chunk.content_hash=manifest.content_hash
   JOIN retrieval.index_manifest_source source_manifest
     ON source_manifest.index_version_id=manifest.index_version_id
     AND source_manifest.workspace_id=manifest.workspace_id
     AND source_manifest.parse_projection_id=chunk.parse_projection_id
     AND source_manifest.source_version_id=(document->>'source_version_id')::uuid
     AND source_manifest.selection_status='included'
   JOIN core.source_version source_version ON source_version.id=source_manifest.source_version_id
     AND source_version.source_id=source_manifest.source_id
   JOIN core.source source ON source.id=source_version.source_id AND source.workspace_id=manifest.workspace_id
   JOIN core.content_artifact artifact ON artifact.id=source_version.content_artifact_id
     AND artifact.workspace_id=manifest.workspace_id AND artifact.content_hash=source_version.content_hash
     AND artifact.byte_size=source_version.byte_size
   JOIN ingestion.parse_projection projection ON projection.id=source_manifest.parse_projection_id
     AND projection.workspace_id=manifest.workspace_id AND projection.content_artifact_id=artifact.id
   JOIN ingestion.source_span span ON span.id=chunk.source_span_id
     AND span.workspace_id=manifest.workspace_id AND span.parse_projection_id=projection.id
     AND span.content_artifact_id=artifact.id
   WHERE manifest.workspace_id=workspace
     AND manifest.index_version_id=(document->>'index_version_id')::uuid
     AND manifest.chunk_id=(document->>'chunk_id')::uuid
     AND span.id=(document->>'source_span_id')::uuid
     AND span.excerpt_hash=document->>'content_hash'
 )
$$;

-- This helper proves a Read's complete immutable alias/Search chain. The
-- identity is global in the Read receipt and local in the Search receipt.
CREATE FUNCTION workflow.workspace_analysis_dynamic_read_bound(workspace uuid, run_id uuid, analysis_id uuid, document jsonb)
RETURNS boolean LANGUAGE sql STABLE STRICT AS $$
 SELECT EXISTS (
   SELECT 1 FROM agent.workspace_analysis_evidence evidence
   JOIN workflow.tool_result_receipt search ON search.id=evidence.search_receipt_id
     AND search.workspace_id=evidence.workspace_id AND search.workflow_run_id=run_id
     AND search.tool_name='SearchKnowledge' AND search.tool_version=3
     AND search.output_hash=evidence.search_receipt_hash
   JOIN agent.workspace_analysis_operation operation ON operation.id=evidence.search_operation_id
     AND operation.workspace_id=evidence.workspace_id AND operation.analysis_run_id=evidence.analysis_run_id
     AND operation.workflow_run_id=run_id AND operation.node_key='decide_next'
     AND operation.operation_kind='KNOWLEDGE_SEARCH' AND operation.status='SUCCEEDED'
     AND operation.tool_call_id=search.tool_call_id AND operation.result_id=search.id
     AND operation.result_hash=search.output_hash AND operation.result_kind='TOOL_RESULT_RECEIPT'
   WHERE evidence.workspace_id=workspace AND evidence.analysis_run_id=analysis_id
     AND 'E'||evidence.reference_no::text=document->>'evidence_ref'
     AND evidence.search_receipt_id=(document->>'search_receipt_id')::uuid
     AND evidence.search_receipt_hash=document->>'search_receipt_hash'
     AND evidence.local_evidence_ref=document->>'search_evidence_ref'
     AND evidence.citation_id=document->>'citation_id'
     AND evidence.index_version_id=(document->>'index_version_id')::uuid
     AND evidence.chunk_id=(document->>'chunk_id')::uuid
     AND evidence.source_version_id=(document->>'source_version_id')::uuid
     AND evidence.source_span_id=(document->>'source_span_id')::uuid
     AND evidence.content_hash=document->>'content_hash'
     AND EXISTS (
       SELECT 1 FROM jsonb_array_elements(convert_from(search.server_binding_document,'UTF8')::jsonb->'items') item
       WHERE item=(document-ARRAY['search_receipt_id','search_receipt_hash','search_evidence_ref','read_receipt_id','read_receipt_hash'])
         || jsonb_build_object('evidence_ref',document->>'search_evidence_ref')
     )
 )
$$;
CREATE FUNCTION workflow.guard_tool_result_receipt_insert_v2()
RETURNS trigger
LANGUAGE plpgsql
AS $$
DECLARE
    call_row workflow.tool_call%ROWTYPE;
    operation_row agent.workspace_analysis_operation%ROWTYPE;
    analysis_status_value text;
    output_document json;
    binding_document json;
    output_text text;
    binding_text text;
    output_json jsonb;
    binding_json jsonb;
    ordered_values text[];
    sorted_values text[];
    value_count bigint;
    distinct_value_count bigint;
BEGIN
    SELECT analysis.status
      INTO analysis_status_value
      FROM agent.workspace_analysis_operation AS operation
      JOIN agent.workspace_analysis_run AS analysis
        ON analysis.id=operation.analysis_run_id
       AND analysis.workspace_id=operation.workspace_id
       AND analysis.workflow_run_id=operation.workflow_run_id
       AND analysis.definition_version=2
     WHERE operation.tool_call_id=NEW.tool_call_id
       AND operation.workspace_id=NEW.workspace_id
       AND operation.workflow_run_id=NEW.workflow_run_id
       AND operation.node_run_id=NEW.node_run_id
       AND operation.first_node_attempt_id=NEW.node_attempt_id
     FOR UPDATE OF analysis;
    SELECT operation.*
      INTO operation_row
      FROM agent.workspace_analysis_operation AS operation
     WHERE operation.tool_call_id=NEW.tool_call_id
       AND operation.workspace_id=NEW.workspace_id
       AND operation.workflow_run_id=NEW.workflow_run_id
       AND operation.node_run_id=NEW.node_run_id
       AND operation.first_node_attempt_id=NEW.node_attempt_id
     FOR UPDATE;
    SELECT * INTO call_row
      FROM workflow.tool_call
     WHERE id=NEW.tool_call_id
       AND workspace_id=NEW.workspace_id
       AND workflow_run_id=NEW.workflow_run_id
       AND node_run_id=NEW.node_run_id
       AND node_attempt_id=NEW.node_attempt_id
     FOR UPDATE;

    IF operation_row.id IS NULL
       OR operation_row.call_kind<>'TOOL'
       OR operation_row.status<>'STARTED'
       OR analysis_status_value IS DISTINCT FROM 'running'
       OR EXISTS (
            SELECT 1 FROM workflow.tool_result_receipt_failure
             WHERE tool_call_id=NEW.tool_call_id
       )
       OR call_row.id IS NULL
       OR call_row.status<>'SUCCEEDED'
       OR call_row.requested_tool_name IS DISTINCT FROM NEW.tool_name
       OR call_row.tool_version IS DISTINCT FROM NEW.tool_version
       OR call_row.output_schema_id IS DISTINCT FROM NEW.output_schema_id
       OR call_row.output_schema_version IS DISTINCT FROM NEW.output_schema_version
       OR call_row.definition_hash IS DISTINCT FROM NEW.definition_hash
       OR call_row.capability IS DISTINCT FROM 'READ_LOCAL'
       OR call_row.side_effect_level IS DISTINCT FROM 'NONE'
       OR call_row.invocation_policy IS DISTINCT FROM 'TRUSTED_WORKFLOW_ONLY'
       OR call_row.response_hash IS DISTINCT FROM NEW.output_hash
       OR call_row.response_bytes IS DISTINCT FROM NEW.output_bytes
       OR call_row.result_ref IS NOT NULL
       OR call_row.side_effect_type IS NOT NULL
       OR call_row.side_effect_id IS NOT NULL
       OR call_row.completed_at IS NULL
       OR NEW.created_at<call_row.completed_at THEN
        RAISE EXCEPTION 'tool result receipt call binding is invalid' USING ERRCODE='55000';
    END IF;

    IF (NEW.tool_name='ReadGitStatus' AND (
            call_row.input_schema_id IS DISTINCT FROM 'tool.read_git_status.input'
            OR call_row.input_schema_version IS DISTINCT FROM 1
       )) OR (NEW.tool_name='SearchKnowledge' AND (
            call_row.input_schema_id IS DISTINCT FROM 'tool.search_knowledge.input'
            OR call_row.input_schema_version IS DISTINCT FROM 2
       )) OR (NEW.tool_name='ReadSource' AND (
            call_row.input_schema_id IS DISTINCT FROM 'tool.read_source.input'
            OR call_row.input_schema_version IS DISTINCT FROM 3
       )) OR (NEW.tool_name='ValidateCitation' AND (
            call_row.input_schema_id IS DISTINCT FROM 'tool.validate_citation.input'
            OR call_row.input_schema_version IS DISTINCT FROM 3
       )) THEN
        RAISE EXCEPTION 'tool result receipt input contract is invalid' USING ERRCODE='55000';
    END IF;

    output_text := convert_from(NEW.output_document,'UTF8');
    BEGIN
        output_document := output_text::json;
    EXCEPTION WHEN OTHERS THEN
        RAISE EXCEPTION 'tool result receipt output is not valid JSON' USING ERRCODE='23514';
    END;
    IF workflow.workspace_analysis_receipt_canonical_json(output_document)<>output_text THEN
        RAISE EXCEPTION 'tool result receipt output is not canonical JSON' USING ERRCODE='23514';
    END IF;
    output_json := output_document::jsonb;
    IF jsonb_typeof(output_json)<>'object' THEN
        RAISE EXCEPTION 'tool result receipt output is not an object' USING ERRCODE='23514';
    END IF;
    IF NEW.server_binding_document IS NOT NULL THEN
        binding_text := convert_from(NEW.server_binding_document,'UTF8');
        BEGIN
            binding_document := binding_text::json;
        EXCEPTION WHEN OTHERS THEN
            RAISE EXCEPTION 'tool result receipt binding is not valid JSON' USING ERRCODE='23514';
        END;
        IF workflow.workspace_analysis_receipt_canonical_json(binding_document)<>binding_text THEN
            RAISE EXCEPTION 'tool result receipt binding is not canonical JSON' USING ERRCODE='23514';
        END IF;
        binding_json := binding_document::jsonb;
        IF jsonb_typeof(binding_json)<>'object' THEN
            RAISE EXCEPTION 'tool result receipt binding is not an object' USING ERRCODE='23514';
        END IF;
    END IF;

    IF NEW.tool_name='ReadGitStatus' THEN
        IF NOT workflow.workspace_analysis_receipt_exact_object_keys(output_json, ARRAY[
            'branch','head','object_format','clean','staged_count','unstaged_count','untracked_count','conflict_count'
        ])
           OR jsonb_typeof(output_json->'branch')<>'string'
           OR jsonb_typeof(output_json->'head')<>'string'
           OR jsonb_typeof(output_json->'object_format')<>'string'
           OR jsonb_typeof(output_json->'clean')<>'boolean'
           OR jsonb_typeof(output_json->'staged_count')<>'number'
           OR jsonb_typeof(output_json->'unstaged_count')<>'number'
           OR jsonb_typeof(output_json->'untracked_count')<>'number'
           OR jsonb_typeof(output_json->'conflict_count')<>'number'
           OR NOT workflow.workspace_analysis_receipt_reference(output_json->>'branch', 255)
           OR (output_json->>'object_format') NOT IN ('sha1','sha256')
           OR ((output_json->>'object_format')='sha1' AND (output_json->>'head') !~ '^[0-9a-f]{40}$')
           OR ((output_json->>'object_format')='sha256' AND (output_json->>'head') !~ '^[0-9a-f]{64}$')
           OR (output_json->>'staged_count') !~ '^(0|[1-9][0-9]*)$'
           OR (output_json->>'unstaged_count') !~ '^(0|[1-9][0-9]*)$'
           OR (output_json->>'untracked_count') !~ '^(0|[1-9][0-9]*)$'
           OR (output_json->>'conflict_count') !~ '^(0|[1-9][0-9]*)$'
           OR (output_json->>'staged_count')::bigint>100000
           OR (output_json->>'unstaged_count')::bigint>100000
           OR (output_json->>'untracked_count')::bigint>100000
           OR (output_json->>'conflict_count')::bigint>100000
           OR (output_json->>'clean')::boolean<>(
                (output_json->>'staged_count')::bigint
                +(output_json->>'unstaged_count')::bigint
                +(output_json->>'untracked_count')::bigint
                +(output_json->>'conflict_count')::bigint=0
           ) THEN
            RAISE EXCEPTION 'ReadGitStatus receipt document is invalid' USING ERRCODE='23514';
        END IF;
        IF binding_json IS NOT NULL AND (
            NOT workflow.workspace_analysis_receipt_exact_object_keys(binding_json, ARRAY['workspace_root_hash','repository_identity_hash'])
            OR jsonb_typeof(binding_json->'workspace_root_hash')<>'string'
            OR jsonb_typeof(binding_json->'repository_identity_hash')<>'string'
            OR NOT workflow.workspace_analysis_receipt_hash(binding_json->>'workspace_root_hash')
            OR NOT workflow.workspace_analysis_receipt_hash(binding_json->>'repository_identity_hash')
        ) THEN
            RAISE EXCEPTION 'ReadGitStatus receipt binding is invalid' USING ERRCODE='23514';
        END IF;
    ELSIF NEW.tool_name='SearchKnowledge' THEN
        IF NOT workflow.workspace_analysis_receipt_exact_object_keys(output_json, ARRAY['effective_mode','items','degradations'])
           OR jsonb_typeof(output_json->'effective_mode')<>'string'
           OR jsonb_typeof(output_json->'items')<>'array'
           OR jsonb_typeof(output_json->'degradations')<>'array'
           OR (output_json->>'effective_mode') NOT IN ('keyword','semantic','hybrid')
           OR jsonb_array_length(output_json->'items')>5
           OR jsonb_array_length(output_json->'degradations')>16
           OR EXISTS (
                SELECT 1
                  FROM jsonb_array_elements(output_json->'items') WITH ORDINALITY AS item(document, ordinal)
                 WHERE NOT workflow.workspace_analysis_receipt_exact_object_keys(item.document, ARRAY['evidence_ref','rank','snippet'])
                    OR jsonb_typeof(item.document->'evidence_ref')<>'string'
                    OR jsonb_typeof(item.document->'rank')<>'number'
                    OR jsonb_typeof(item.document->'snippet')<>'string'
                    OR item.document->>'evidence_ref'<>'E'||item.ordinal::text
                    OR item.document->>'rank'<>item.ordinal::text
                    OR NOT workflow.workspace_analysis_receipt_text(item.document->>'snippet', 4096)
           )
           OR EXISTS (
                SELECT 1
                  FROM jsonb_array_elements(output_json->'degradations') AS degradation(value)
                 WHERE jsonb_typeof(degradation.value)<>'string'
                    OR degradation.value #>> '{}' !~ '^[A-Z][A-Z0-9_]{0,127}$'
           ) THEN
            RAISE EXCEPTION 'SearchKnowledge receipt output is invalid' USING ERRCODE='23514';
        END IF;
        SELECT array_agg(value #>> '{}' ORDER BY ordinal), array_agg(value #>> '{}' ORDER BY value #>> '{}'), count(*), count(DISTINCT value #>> '{}')
          INTO ordered_values, sorted_values, value_count, distinct_value_count
          FROM jsonb_array_elements(output_json->'degradations') WITH ORDINALITY AS degradation(value, ordinal);
        IF ordered_values IS DISTINCT FROM sorted_values OR value_count<>distinct_value_count THEN
            RAISE EXCEPTION 'SearchKnowledge receipt degradations are invalid' USING ERRCODE='23514';
        END IF;
        IF binding_json IS NULL OR NOT workflow.workspace_analysis_receipt_exact_object_keys(binding_json, ARRAY['items','selected_refs'])
           OR jsonb_typeof(binding_json->'items')<>'array'
           OR jsonb_typeof(binding_json->'selected_refs')<>'array'
           OR jsonb_array_length(binding_json->'items')<>jsonb_array_length(output_json->'items')
           OR jsonb_array_length(binding_json->'items')>5
           OR jsonb_array_length(binding_json->'selected_refs')<>jsonb_array_length(binding_json->'items')
           OR EXISTS (
                SELECT 1 FROM jsonb_array_elements(binding_json->'items') WITH ORDINALITY AS item(document, ordinal)
                 WHERE NOT workflow.workspace_analysis_receipt_identity_valid(item.document, 5)
                    OR NOT workflow.workspace_analysis_dynamic_source_bound(NEW.workspace_id,item.document)
                    OR item.document->>'evidence_ref'<>'E'||item.ordinal::text
           )
           OR EXISTS (
                SELECT 1 FROM jsonb_array_elements(binding_json->'selected_refs') WITH ORDINALITY AS selection(value, ordinal)
                 WHERE jsonb_typeof(selection.value)<>'string'
                    OR selection.value #>> '{}' <>'E'||selection.ordinal::text
           )
           OR EXISTS (
                SELECT 1
                  FROM jsonb_array_elements(output_json->'items') WITH ORDINALITY AS output_item(document, ordinal)
                  JOIN jsonb_array_elements(binding_json->'items') WITH ORDINALITY AS binding_item(document, ordinal)
                    USING (ordinal)
                 WHERE output_item.document->>'evidence_ref'<>binding_item.document->>'evidence_ref'
           ) THEN
            RAISE EXCEPTION 'SearchKnowledge receipt binding is invalid' USING ERRCODE='23514';
        END IF;
        SELECT count(*), count(DISTINCT document->>'citation_id')
          INTO value_count, distinct_value_count
          FROM jsonb_array_elements(binding_json->'items') AS item(document);
        IF value_count<>distinct_value_count THEN
            RAISE EXCEPTION 'SearchKnowledge receipt citation identities are duplicated' USING ERRCODE='23514';
        END IF;

        IF (SELECT count(DISTINCT item->>'index_version_id')
              FROM jsonb_array_elements(binding_json->'items') item)>1 THEN
            RAISE EXCEPTION 'dynamic Search receipt spans indexes' USING ERRCODE='23514';
        END IF;
    ELSIF NEW.tool_name='ReadSource' THEN
        IF NOT workflow.workspace_analysis_receipt_exact_object_keys(output_json,ARRAY['evidence_ref','content_hash','truncated','excerpt'])
           OR jsonb_typeof(output_json->'evidence_ref')<>'string'
           OR jsonb_typeof(output_json->'content_hash')<>'string'
           OR jsonb_typeof(output_json->'truncated')<>'boolean'
           OR jsonb_typeof(output_json->'excerpt')<>'string'
           OR output_json->>'evidence_ref' !~ '^E([1-9]|[12][0-9]|3[0-2])$'
           OR NOT workflow.workspace_analysis_receipt_hash(output_json->>'content_hash')
           OR NOT workflow.workspace_analysis_receipt_text(output_json->>'excerpt',4096) THEN
            RAISE EXCEPTION 'dynamic Read output is invalid' USING ERRCODE='23514';
        END IF;
        IF binding_json IS NULL OR NOT workflow.workspace_analysis_receipt_exact_object_keys(binding_json,ARRAY[
             'search_receipt_id','search_receipt_hash','search_evidence_ref','evidence_ref','citation_id','index_version_id','chunk_id','source_version_id','source_span_id','content_hash'])
           OR jsonb_typeof(binding_json->'search_receipt_id')<>'string'
           OR jsonb_typeof(binding_json->'search_receipt_hash')<>'string'
           OR jsonb_typeof(binding_json->'search_evidence_ref')<>'string'
           OR NOT workflow.workspace_analysis_receipt_canonical_uuid(binding_json->>'search_receipt_id')
           OR NOT workflow.workspace_analysis_receipt_hash(binding_json->>'search_receipt_hash')
           OR binding_json->>'search_evidence_ref' !~ '^E[1-5]$'
           OR NOT workflow.workspace_analysis_dynamic_identity_valid(binding_json-ARRAY['search_receipt_id','search_receipt_hash','search_evidence_ref'])
           OR output_json->>'evidence_ref'<>binding_json->>'evidence_ref'
           OR output_json->>'content_hash'<>binding_json->>'content_hash'
           OR NOT workflow.workspace_analysis_dynamic_read_bound(NEW.workspace_id,NEW.workflow_run_id,operation_row.analysis_run_id,binding_json) THEN
            RAISE EXCEPTION 'dynamic Read Search binding is invalid' USING ERRCODE='23514';
        END IF;
    ELSIF NEW.tool_name='ValidateCitation' THEN
        IF NOT workflow.workspace_analysis_receipt_exact_object_keys(output_json,ARRAY['results'])
           OR jsonb_typeof(output_json->'results')<>'array'
           OR jsonb_array_length(output_json->'results') NOT BETWEEN 1 AND 8
           OR EXISTS (
             SELECT 1 FROM jsonb_array_elements(output_json->'results') result
             WHERE NOT workflow.workspace_analysis_receipt_exact_object_keys(result,ARRAY['evidence_ref','valid','reason_code'])
                OR jsonb_typeof(result->'evidence_ref')<>'string'
                OR jsonb_typeof(result->'valid')<>'boolean'
                OR jsonb_typeof(result->'reason_code')<>'string'
                OR result->>'evidence_ref' !~ '^E([1-9]|[12][0-9]|3[0-2])$'
                OR result->>'reason_code' NOT IN ('OK','CITATION_UNRESOLVABLE','EVIDENCE_INELIGIBLE','BINDING_MISMATCH')
                OR (result->>'valid')::boolean<>(result->>'reason_code'='OK')
           ) THEN
            RAISE EXCEPTION 'dynamic Citation output is invalid' USING ERRCODE='23514';
        END IF;
        IF binding_json IS NULL OR NOT workflow.workspace_analysis_receipt_exact_object_keys(binding_json,ARRAY['candidate_id','candidate_hash','results'])
           OR jsonb_typeof(binding_json->'results')<>'array'
           OR jsonb_array_length(binding_json->'results')<>jsonb_array_length(output_json->'results') THEN
            RAISE EXCEPTION 'dynamic Citation private binding is invalid' USING ERRCODE='23514';
        END IF;
        IF operation_row.node_key='decide_next' THEN
            IF binding_json->'candidate_id'<>'null'::jsonb OR binding_json->'candidate_hash'<>'null'::jsonb THEN
                RAISE EXCEPTION 'loop Citation cannot bind a candidate' USING ERRCODE='23514';
            END IF;
        ELSIF operation_row.node_key='validate_citations' AND operation_row.ordinal=1 THEN
            IF jsonb_typeof(binding_json->'candidate_id')<>'string'
               OR jsonb_typeof(binding_json->'candidate_hash')<>'string'
               OR NOT workflow.workspace_analysis_receipt_canonical_uuid(binding_json->>'candidate_id')
               OR NOT workflow.workspace_analysis_receipt_hash(binding_json->>'candidate_hash')
               OR NOT EXISTS (
                 SELECT 1 FROM agent.workspace_analysis_candidate candidate
                 WHERE candidate.id=(binding_json->>'candidate_id')::uuid
                   AND candidate.workspace_id=NEW.workspace_id AND candidate.analysis_run_id=operation_row.analysis_run_id
                   AND candidate.schema_version=2 AND candidate.document_hash=binding_json->>'candidate_hash'
                   AND convert_from(candidate.document,'UTF8')::jsonb->'payload'->'citation_refs'=(
                     SELECT jsonb_agg(result->'evidence_ref' ORDER BY ordinal)
                     FROM jsonb_array_elements(output_json->'results') WITH ORDINALITY AS item(result,ordinal))
               ) THEN
                RAISE EXCEPTION 'final Citation candidate binding is invalid' USING ERRCODE='23514';
            END IF;
        ELSE
            RAISE EXCEPTION 'dynamic Citation operation purpose is invalid' USING ERRCODE='23514';
        END IF;
        IF EXISTS (
            SELECT 1 FROM jsonb_array_elements(binding_json->'results') WITH ORDINALITY binding(item,ordinal)
            JOIN jsonb_array_elements(output_json->'results') WITH ORDINALITY output(item,ordinal) USING (ordinal)
            WHERE NOT workflow.workspace_analysis_receipt_exact_object_keys(binding.item,ARRAY[
               'search_receipt_id','search_receipt_hash','search_evidence_ref','read_receipt_id','read_receipt_hash','evidence_ref','citation_id','index_version_id','chunk_id','source_version_id','source_span_id','content_hash'])
              OR jsonb_typeof(binding.item->'search_receipt_id')<>'string'
              OR jsonb_typeof(binding.item->'search_receipt_hash')<>'string'
              OR jsonb_typeof(binding.item->'search_evidence_ref')<>'string'
              OR jsonb_typeof(binding.item->'read_receipt_id')<>'string'
              OR jsonb_typeof(binding.item->'read_receipt_hash')<>'string'
              OR NOT workflow.workspace_analysis_receipt_canonical_uuid(binding.item->>'search_receipt_id')
              OR NOT workflow.workspace_analysis_receipt_canonical_uuid(binding.item->>'read_receipt_id')
              OR binding.item->>'read_receipt_id'=binding.item->>'search_receipt_id'
              OR NOT workflow.workspace_analysis_receipt_hash(binding.item->>'search_receipt_hash')
              OR NOT workflow.workspace_analysis_receipt_hash(binding.item->>'read_receipt_hash')
              OR binding.item->>'search_evidence_ref' !~ '^E[1-5]$'
              OR NOT workflow.workspace_analysis_dynamic_identity_valid(binding.item-ARRAY['search_receipt_id','search_receipt_hash','search_evidence_ref','read_receipt_id','read_receipt_hash'])
              OR binding.item->>'evidence_ref'<>output.item->>'evidence_ref'
              OR NOT workflow.workspace_analysis_dynamic_read_bound(NEW.workspace_id,NEW.workflow_run_id,operation_row.analysis_run_id,binding.item)
              OR NOT EXISTS (
                SELECT 1 FROM workflow.tool_result_receipt source_read
                JOIN agent.workspace_analysis_operation read_operation ON read_operation.tool_call_id=source_read.tool_call_id
                  AND read_operation.workspace_id=source_read.workspace_id AND read_operation.workflow_run_id=source_read.workflow_run_id
                  AND read_operation.analysis_run_id=operation_row.analysis_run_id
                  AND read_operation.node_key='decide_next' AND read_operation.operation_kind='SOURCE_READ'
                  AND read_operation.status='SUCCEEDED' AND read_operation.result_id=source_read.id
                  AND read_operation.result_hash=source_read.output_hash AND read_operation.result_kind='TOOL_RESULT_RECEIPT'
                WHERE source_read.id=(binding.item->>'read_receipt_id')::uuid
                  AND source_read.workspace_id=NEW.workspace_id AND source_read.workflow_run_id=NEW.workflow_run_id
                  AND source_read.tool_name='ReadSource' AND source_read.tool_version=4
                  AND source_read.output_hash=binding.item->>'read_receipt_hash'
                  AND convert_from(source_read.server_binding_document,'UTF8')::jsonb=binding.item-ARRAY['read_receipt_id','read_receipt_hash']
              )
        ) OR (SELECT count(DISTINCT item->>'evidence_ref') FROM jsonb_array_elements(output_json->'results') item)<>jsonb_array_length(output_json->'results') THEN
            RAISE EXCEPTION 'dynamic Citation exact Read/Search chain is invalid' USING ERRCODE='23514';
        END IF;
    ELSE
        RAISE EXCEPTION 'unsupported dynamic Tool receipt' USING ERRCODE='23514';
    END IF;
    RETURN NEW;
END;
$$;

-- Exact tuple routing leaves the historical v1 trigger function unchanged.
DROP TRIGGER tool_result_receipt_guard_insert ON workflow.tool_result_receipt;
CREATE TRIGGER tool_result_receipt_guard_insert BEFORE INSERT ON workflow.tool_result_receipt
FOR EACH ROW WHEN ((NEW.tool_name,NEW.tool_version) IN (
  ('ReadGitStatus',2),('SearchKnowledge',2),('ReadSource',3),('ValidateCitation',3)))
EXECUTE FUNCTION workflow.guard_tool_result_receipt_insert();
CREATE TRIGGER tool_result_receipt_v2_guard_insert BEFORE INSERT ON workflow.tool_result_receipt
FOR EACH ROW WHEN ((NEW.tool_name,NEW.tool_version) IN (
  ('ReadGitStatus',3),('SearchKnowledge',3),('ReadSource',4),('ValidateCitation',4)))
EXECUTE FUNCTION workflow.guard_tool_result_receipt_insert_v2();

CREATE FUNCTION agent.guard_workspace_analysis_evidence_receipt()
RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE
    search workflow.tool_result_receipt%ROWTYPE;
    operation agent.workspace_analysis_operation%ROWTYPE;
    identity jsonb;
BEGIN
    SELECT * INTO search FROM workflow.tool_result_receipt
      WHERE id=NEW.search_receipt_id AND workspace_id=NEW.workspace_id;
    SELECT * INTO operation FROM agent.workspace_analysis_operation
      WHERE id=NEW.search_operation_id AND workspace_id=NEW.workspace_id AND analysis_run_id=NEW.analysis_run_id;
    identity:=jsonb_build_object('evidence_ref',NEW.local_evidence_ref,'citation_id',NEW.citation_id,
      'index_version_id',NEW.index_version_id::text,'chunk_id',NEW.chunk_id::text,
      'source_version_id',NEW.source_version_id::text,'source_span_id',NEW.source_span_id::text,'content_hash',NEW.content_hash);
    IF search.id IS NULL OR search.tool_name<>'SearchKnowledge' OR search.tool_version<>3
       OR search.output_hash<>NEW.search_receipt_hash OR operation.id IS NULL
       OR operation.node_key<>'decide_next' OR operation.operation_kind<>'KNOWLEDGE_SEARCH'
       OR operation.status<>'SUCCEEDED' OR operation.result_id IS DISTINCT FROM search.id
       OR operation.result_hash IS DISTINCT FROM search.output_hash OR operation.tool_call_id IS DISTINCT FROM search.tool_call_id
       OR operation.workflow_run_id<>search.workflow_run_id
       OR NOT workflow.workspace_analysis_receipt_identity_valid(identity,5)
       OR NOT workflow.workspace_analysis_dynamic_source_bound(NEW.workspace_id,identity)
       OR NOT EXISTS (SELECT 1 FROM jsonb_array_elements(convert_from(search.server_binding_document,'UTF8')::jsonb->'items') item WHERE item=identity) THEN
        RAISE EXCEPTION 'dynamic evidence alias does not match exact Search receipt' USING ERRCODE='23514';
    END IF;
    RETURN NEW;
END;
$$;
CREATE TRIGGER workspace_analysis_evidence_receipt_guard BEFORE INSERT ON agent.workspace_analysis_evidence
FOR EACH ROW EXECUTE FUNCTION agent.guard_workspace_analysis_evidence_receipt();

-- A successful Search cannot commit with missing, partial or reordered aliases.
CREATE FUNCTION agent.guard_workspace_analysis_search_evidence_closure()
RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE
    search workflow.tool_result_receipt%ROWTYPE;
    analysis_id_value uuid;
    items jsonb;
    alias_count bigint;
    lower_ref integer;
    upper_ref integer;
BEGIN
    IF TG_TABLE_SCHEMA='workflow' THEN
        SELECT * INTO search FROM workflow.tool_result_receipt WHERE id=NEW.id;
        IF search.tool_name<>'SearchKnowledge' OR search.tool_version<>3 THEN RETURN NULL; END IF;
    ELSE
        SELECT * INTO search FROM workflow.tool_result_receipt WHERE id=NEW.search_receipt_id;
    END IF;
    SELECT analysis_run_id INTO analysis_id_value FROM agent.workspace_analysis_operation
      WHERE tool_call_id=search.tool_call_id AND workspace_id=search.workspace_id AND status='SUCCEEDED'
        AND result_id=search.id AND result_hash=search.output_hash;
    IF analysis_id_value IS NULL THEN
        RAISE EXCEPTION 'dynamic Search operation is not complete' USING ERRCODE='23514';
    END IF;
    items:=convert_from(search.server_binding_document,'UTF8')::jsonb->'items';
    SELECT count(*),min(reference_no),max(reference_no) INTO alias_count,lower_ref,upper_ref
      FROM agent.workspace_analysis_evidence WHERE analysis_run_id=analysis_id_value AND search_receipt_id=search.id;
    IF alias_count<>jsonb_array_length(items)
       OR (alias_count>0 AND upper_ref-lower_ref+1<>alias_count)
       OR EXISTS (
         SELECT 1 FROM jsonb_array_elements(items) WITH ORDINALITY item(identity,ordinal)
         WHERE NOT EXISTS (
           SELECT 1 FROM agent.workspace_analysis_evidence evidence
           WHERE evidence.analysis_run_id=analysis_id_value AND evidence.search_receipt_id=search.id
             AND evidence.reference_no=lower_ref+item.ordinal-1
             AND evidence.local_evidence_ref=item.identity->>'evidence_ref'
             AND evidence.search_receipt_hash=search.output_hash
         )
       ) THEN
        RAISE EXCEPTION 'dynamic Search evidence mapping is incomplete' USING ERRCODE='23514';
    END IF;
    RETURN NULL;
END;
$$;
CREATE CONSTRAINT TRIGGER tool_search_evidence_closure AFTER INSERT ON workflow.tool_result_receipt
DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION agent.guard_workspace_analysis_search_evidence_closure();
CREATE CONSTRAINT TRIGGER workspace_analysis_evidence_search_closure AFTER INSERT ON agent.workspace_analysis_evidence
DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION agent.guard_workspace_analysis_search_evidence_closure();
