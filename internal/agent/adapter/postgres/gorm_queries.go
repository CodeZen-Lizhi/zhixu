package postgres

const gormInsertModelRunSQL = `
	INSERT INTO agent.model_run(
		id,workspace_id,workflow_run_id,node_run_id,node_attempt_id,model_settings_revision,
		adapter_name,adapter_version,model_id,model_version,profile_id,profile_version,
		prompt_template_id,prompt_template_version,output_schema_id,output_schema_version,
		reduced_schema_id,reduced_schema_version,
		retrieval_index_version_id,embedding_version_id,rerank_model_version,
		memory_snapshot_id,memory_context_schema_version,memory_context_digest,memory_context_item_count,memory_context_bytes,
		status,final_result_type,error_code,version,started_at,updated_at,completed_at
	) VALUES(
		?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?
	) ON CONFLICT DO NOTHING`

const gormModelRunSelect = `
	SELECT id::text,workspace_id::text,workflow_run_id::text,node_run_id::text,node_attempt_id::text,
		model_settings_revision,
		adapter_name,adapter_version,model_id,model_version,profile_id,profile_version,
		prompt_template_id,prompt_template_version,output_schema_id,output_schema_version,
		reduced_schema_id,reduced_schema_version,
		retrieval_index_version_id::text,embedding_version_id::text,rerank_model_version,
		memory_snapshot_id::text,memory_context_schema_version,memory_context_digest,memory_context_item_count,memory_context_bytes,
		status,final_result_type,error_code,version,started_at,updated_at,completed_at
	FROM agent.model_run`

const gormModelCallSelect = `
	SELECT call.id::text,call.model_run_id::text,call.call_no,call.phase,
		call.adapter_name,call.adapter_version,call.model_id,call.model_version,
		call.profile_id,call.profile_version,call.prompt_template_id,call.prompt_template_version,
		call.output_schema_id,call.output_schema_version,call.max_output_tokens,
		call.request_hash,call.response_hash,
		call.request_bytes,call.response_bytes,call.input_tokens,call.output_tokens,call.latency_ms,
		call.status,call.error_code,call.version,call.started_at,call.completed_at
	FROM agent.model_call call `

const gormInsertModelCallSQL = `
	INSERT INTO agent.model_call(
		id,model_run_id,call_no,phase,
		adapter_name,adapter_version,model_id,model_version,profile_id,profile_version,
		prompt_template_id,prompt_template_version,output_schema_id,output_schema_version,max_output_tokens,
		request_hash,response_hash,request_bytes,response_bytes,
		input_tokens,output_tokens,latency_ms,status,error_code,version,started_at,completed_at
	)
	SELECT ?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,
		?,NULL,?,0,0,0,0,?,NULL,?,?,NULL
	FROM agent.model_run run
	WHERE run.id=? AND run.workspace_id=?
	ON CONFLICT DO NOTHING`
