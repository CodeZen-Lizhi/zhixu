#!/usr/bin/env bash

# Shared by the isolated RAG and Workspace Analysis compatibility smokes.
# Projections retain identifiers and safe counts only, never model/source text.

workspace_analysis_fixture_request_count() {
  local stage=$1
  [[ "${stage}" == workspace_analysis_decision || "${stage}" == workspace_analysis_budget_decision ]] || \
    fail 'unsupported Workspace Analysis fixture count stage'
  compose logs --no-color rag-model-fixture 2>/dev/null | \
    awk -v stage="${stage}" '$NF == "stage=" stage && index($0,"rag model fixture request stage=") { count++ } END { print count+0 }'
}

workspace_analysis_v2_database_projection() {
  local answer_id=$1
  compose exec -T postgres psql -X -Atq --set ON_ERROR_STOP=1 \
    --username "${ZHIXU_POSTGRES_USER}" --dbname "${ZHIXU_POSTGRES_DB}" \
    --set workspace_id="${WORKSPACE_ID}" --set answer_id="${answer_id}" <<'SQL'
WITH target AS (
  SELECT * FROM agent.workspace_analysis_run WHERE workspace_id=:'workspace_id' AND answer_id=:'answer_id'
)
SELECT jsonb_build_object(
  'definition_version',run.definition_version,'policy_version',run.policy_version,
  'status',run.status,'termination_reason',run.termination_reason,
  'settled',jsonb_build_array(run.settled_model_calls,run.settled_tool_calls,run.settled_source_reads),
  'reserved',jsonb_build_array(run.reserved_model_calls,run.reserved_tool_calls,run.reserved_source_reads,
    run.reserved_input_tokens,run.reserved_output_tokens,COALESCE(run.reserved_cost_microunits,0)),
  'ledger_balanced',(
    SELECT ROW(COALESCE(sum(reservation.settled_model_calls),0),COALESCE(sum(reservation.settled_tool_calls),0),
      COALESCE(sum(reservation.settled_source_reads),0),COALESCE(sum(reservation.settled_input_tokens),0),
      COALESCE(sum(reservation.settled_output_tokens),0))=
      ROW(run.settled_model_calls,run.settled_tool_calls,run.settled_source_reads,run.settled_input_tokens,run.settled_output_tokens)
    FROM agent.workspace_analysis_budget_reservation reservation WHERE reservation.analysis_run_id=run.id),
  'operation_count',(SELECT count(*) FROM agent.workspace_analysis_operation WHERE analysis_run_id=run.id),
  'reservation_count',(SELECT count(*) FROM agent.workspace_analysis_budget_reservation WHERE analysis_run_id=run.id),
  'model_call_count',(SELECT count(*) FROM agent.model_call model_call JOIN agent.model_run model ON model.id=model_call.model_run_id WHERE model.workflow_run_id=run.workflow_run_id),
  'tool_call_count',(SELECT count(*) FROM workflow.tool_call WHERE workflow_run_id=run.workflow_run_id),
  'receipt_count',(SELECT count(*) FROM workflow.tool_result_receipt WHERE workflow_run_id=run.workflow_run_id),
  'candidate_count',(SELECT count(*) FROM agent.workspace_analysis_candidate WHERE analysis_run_id=run.id AND schema_version=2),
  'review_count',(SELECT count(*) FROM agent.workspace_analysis_model_result WHERE analysis_run_id=run.id AND operation_kind='FAITHFULNESS_REVIEW'),
  'decision_count',(SELECT count(*) FROM agent.workspace_analysis_decision WHERE analysis_run_id=run.id),
  'publication_count',(SELECT count(*) FROM agent.workspace_analysis_publication_proof WHERE analysis_run_id=run.id),
  'termination_count',(SELECT count(*) FROM agent.workspace_analysis_termination_proof WHERE analysis_run_id=run.id),
  'publication_has_git',(SELECT git_receipt_id IS NOT NULL AND git_receipt_hash IS NOT NULL FROM agent.workspace_analysis_publication_proof WHERE analysis_run_id=run.id),
  'loop_citation_unbound_count',(
    SELECT count(*) FROM agent.workspace_analysis_operation operation
    JOIN workflow.tool_result_receipt receipt ON receipt.id=operation.result_id
    WHERE operation.analysis_run_id=run.id AND operation.node_key='decide_next' AND operation.operation_kind='CITATION_VALIDATION'
      AND convert_from(receipt.server_binding_document,'UTF8')::jsonb->'candidate_id'='null'::jsonb
      AND convert_from(receipt.server_binding_document,'UTF8')::jsonb->'candidate_hash'='null'::jsonb),
  'final_citation_bound_count',(
    SELECT count(*) FROM agent.workspace_analysis_publication_proof proof
    JOIN agent.workspace_analysis_candidate candidate ON candidate.id=proof.candidate_id AND candidate.analysis_run_id=proof.analysis_run_id
    JOIN agent.workspace_analysis_operation operation ON operation.analysis_run_id=proof.analysis_run_id
      AND operation.node_key='validate_citations' AND operation.result_id=proof.validation_receipt_id
    JOIN workflow.tool_result_receipt receipt ON receipt.id=operation.result_id AND receipt.output_hash=proof.validation_receipt_hash
    WHERE proof.analysis_run_id=run.id AND operation.status='SUCCEEDED' AND receipt.tool_name='ValidateCitation' AND receipt.tool_version=4
      AND convert_from(receipt.server_binding_document,'UTF8')::jsonb->>'candidate_id'=candidate.id::text
      AND convert_from(receipt.server_binding_document,'UTF8')::jsonb->>'candidate_hash'=candidate.document_hash),
  'model_runs',(
    SELECT COALESCE(jsonb_agg(jsonb_build_object('id',model.id,'status',model.status,'schema',model.output_schema_id) ORDER BY model.started_at,model.id),'[]'::jsonb)
    FROM agent.model_run model WHERE model.workflow_run_id=run.workflow_run_id),
  'journal',(
    SELECT COALESCE(jsonb_agg(jsonb_build_object(
      'sequence',entry.sequence,'id',operation.id,'node_key',operation.node_key,'kind',operation.operation_kind,
      'ordinal',operation.ordinal,'status',operation.status,'result_id',operation.result_id,'result_kind',operation.result_kind,
      'model_call_id',model.id,'model_call_no',model.call_no,'model_status',model.status,'model_run_id',model.model_run_id,
      'tool_call_id',tool.id,'tool_name',tool.requested_tool_name,'tool_version',tool.tool_version,'tool_status',tool.status,
      'receipt_id',receipt.id,'receipt_tool_version',receipt.tool_version,
      'source_ref',CASE WHEN operation.operation_kind='SOURCE_READ' THEN convert_from(receipt.output_document,'UTF8')::jsonb->>'evidence_ref' END,
      'reservation_id',reservation.id,'reservation_status',reservation.status,
      'decision_id',decision.id,'decision_action',convert_from(decision.document,'UTF8')::jsonb->>'action',
      'cause_decision_id',entry.decision_id
    ) ORDER BY entry.sequence),'[]'::jsonb)
    FROM agent.workspace_analysis_journal entry
    JOIN agent.workspace_analysis_operation operation ON operation.id=entry.operation_id
    LEFT JOIN agent.model_call model ON model.id=operation.model_call_id
    LEFT JOIN workflow.tool_call tool ON tool.id=operation.tool_call_id
    LEFT JOIN workflow.tool_result_receipt receipt ON receipt.id=operation.result_id AND operation.result_kind='TOOL_RESULT_RECEIPT'
    LEFT JOIN agent.workspace_analysis_budget_reservation reservation ON reservation.operation_id=operation.id
    LEFT JOIN agent.workspace_analysis_decision decision ON decision.operation_id=operation.id
    WHERE entry.analysis_run_id=run.id),
  'termination',(
    SELECT jsonb_build_object('operation_id',operation_id,'reason',reason,'artifact_kind',artifact_kind,
      'artifact_id',artifact_id,'requested',jsonb_build_array(requested_model_calls,requested_tool_calls,requested_source_reads,
        requested_input_tokens,requested_output_tokens,requested_cost_microunits))
    FROM agent.workspace_analysis_termination_proof WHERE analysis_run_id=run.id)
)
FROM target run;
SQL
}

workspace_analysis_v2_expected_order() {
  local scenario=$1
  case "${scenario}" in
    default) printf '%s\n' '["decide_next","ReadGitStatus@3","decide_next","SearchKnowledge@3","decide_next","ReadSource@4","decide_next","ValidateCitation@4","decide_next","synthesize_answer","ValidateCitation@4","review_publish"]' ;;
    extended) printf '%s\n' '["decide_next","SearchKnowledge@3","decide_next","ReadSource@4","decide_next","SearchKnowledge@3","decide_next","ReadSource@4","decide_next","ValidateCitation@4","decide_next","synthesize_answer","ValidateCitation@4","review_publish"]' ;;
    *) fail 'unsupported Workspace Analysis success scenario' ;;
  esac
}

assert_workspace_analysis_v2_completed() {
  local answer_id=$1 scenario=$2 decisions=$3 tools=$4 reads=$5 has_git=$6 expected_order
  expected_order="$(workspace_analysis_v2_expected_order "${scenario}")"
  jq -e --arg workspace "${WORKSPACE_ID}" --argjson decisions "${decisions}" --argjson tools "${tools}" --argjson git "${has_git}" '
    .publication_status=="completed" and .result_type=="workspace_analysis" and .workflow.status=="succeeded"
    and .retrieval_summary==null and .current_stage==null and (.citations|length)>0
    and ([.citations[].workspace_id]|all(.==$workspace))
    and .result.schema_version=="v2" and .result.payload.termination_reason=="COMPLETED"
    and (.result.payload|has("git_status") and has("proposal_suggestion"))
    and ((.result.payload.git_status!=null)==$git) and .result.payload.proposal_suggestion==null
    and .result.payload.budget.model_calls==($decisions+2) and .result.payload.budget.tool_calls==$tools
  ' "${STATE_DIR}/completed-answer.json" >/dev/null || fail "Workspace Analysis ${scenario} Answer omitted its v2 terminal projection"
  cp "${STATE_DIR}/completed-answer.json" "${STATE_DIR}/analysis-${scenario}-answer.json"

  request_json GET "/api/v2/answers/${answer_id}/analysis-timeline?workspace_id=${WORKSPACE_ID}" 200 '' "Workspace Analysis ${scenario} timeline"
  jq -e --argjson decisions "${decisions}" --argjson tools "${tools}" --argjson reads "${reads}" --argjson order "${expected_order}" '
    .schema_id=="conversation.workspace_analysis_timeline" and .schema_version=="v2"
    and .run_status=="succeeded" and .termination_reason=="COMPLETED"
    and .budget.model_calls=={used:($decisions+2),max:14}
    and .budget.tool_calls=={used:$tools,max:13} and .budget.source_reads=={used:$reads,max:8}
    and ([.items[]|select(.kind=="node")]|length)==0
    and ([.items[]|select(.kind=="model" and .phase=="decide_next")]|length)==$decisions
    and ([.items[]|select(.kind=="model")]|length)==($decisions+2)
    and ([.items[]|select(.kind=="tool")]|length)==$tools
    and ([.items[].sequence]==[range(1;(.items|length)+1)])
    and (.items|all(.status=="succeeded"))
    and ([.items[]|if .kind=="tool" then (.tool_ref.name+"@"+(.tool_ref.version|tostring)) else .phase end]==$order)
  ' "${LAST_RESPONSE_FILE}" >/dev/null || fail "Workspace Analysis ${scenario} timeline lost dynamic order, versions, or exact usage"
  cp "${LAST_RESPONSE_FILE}" "${STATE_DIR}/analysis-${scenario}-timeline.json"

  workspace_analysis_v2_database_projection "${answer_id}" >"${STATE_DIR}/analysis-${scenario}-database.json" || \
    fail "could not read Workspace Analysis ${scenario} database projection"
  jq -e --argjson decisions "${decisions}" --argjson tools "${tools}" --argjson reads "${reads}" --argjson git "${has_git}" --argjson order "${expected_order}" '
    .definition_version==2 and .policy_version==2 and .status=="succeeded" and .termination_reason=="COMPLETED"
    and .settled==[($decisions+2),$tools,$reads] and .reserved==[0,0,0,0,0,0] and .ledger_balanced
    and .model_call_count==($decisions+2) and .tool_call_count==$tools and .receipt_count==$tools
    and .operation_count==($decisions+2+$tools) and .reservation_count==.operation_count
    and .decision_count==$decisions and .candidate_count==1 and .review_count==1
    and .publication_count==1 and .termination_count==0 and .publication_has_git==$git
    and .loop_citation_unbound_count==1 and .final_citation_bound_count==1
    and (.model_runs|length)==3 and (.model_runs|all(.status=="SUCCEEDED"))
    and ([.journal[].sequence]==[range(1;.operation_count+1)])
    and (.journal|unique_by(.id)|length)==.operation_count
    and (.journal|all(.status=="SUCCEEDED" and .reservation_id!=null and .reservation_status=="SETTLED" and .result_id!=null))
    and ([.journal[]|select(.kind=="DECISION")|.model_call_no]==[range(1;$decisions+1)])
    and ([.journal[]|select(.kind=="DECISION")]|all(.decision_id==.result_id and .result_kind=="DECISION_RECEIPT" and .model_status=="SUCCEEDED"))
    and ([.journal[]|select(.tool_call_id!=null)]|all(.tool_status=="SUCCEEDED" and .receipt_id==.result_id and .receipt_tool_version==.tool_version))
    and ([.journal[]|select(.kind=="DECISION")|.decision_action]|last)=="finish"
    and ([.journal[]|select(.kind=="SOURCE_READ")|.source_ref]|unique|length)==$reads
    and ([.journal[]|if .tool_call_id!=null then (.tool_name+"@"+(.tool_version|tostring)) else .node_key end]==$order)
  ' "${STATE_DIR}/analysis-${scenario}-database.json" >/dev/null || fail "Workspace Analysis ${scenario} database omitted the exact decisions, receipts, ledger, or publication proof"
}

assert_workspace_analysis_v2_timeline_replay() {
  local answer_id=$1 scenario=$2 before after
  before="$(jq -cS 'del(.latest_server_event_sequence)' "${STATE_DIR}/analysis-${scenario}-timeline.json")"
  request_json GET "/api/v2/answers/${answer_id}/analysis-timeline?workspace_id=${WORKSPACE_ID}" 200 '' 'Workspace Analysis timeline replay'
  after="$(jq -cS 'del(.latest_server_event_sequence)' "${LAST_RESPONSE_FILE}")"
  [[ "${before}" == "${after}" ]] || fail 'Workspace Analysis replay changed the durable timeline order or summary'
}

wait_for_workspace_analysis_budget_terminal() {
  local answer_id=$1 started_at=${SECONDS} publication workflow
  while (( SECONDS - started_at < TIMEOUT_SECONDS )); do
    request_json GET "/api/v2/answers/${answer_id}?workspace_id=${WORKSPACE_ID}" 200 '' 'Workspace Analysis budget answer'
    publication="$(jq -r '.publication_status' "${LAST_RESPONSE_FILE}")"
    workflow="$(jq -r '.workflow.status' "${LAST_RESPONSE_FILE}")"
    if [[ "${publication}:${workflow}" == 'failed:failed' ]]; then
      cp "${LAST_RESPONSE_FILE}" "${STATE_DIR}/analysis-budget-answer.json"
      return 0
    fi
    case "${publication}" in
      completed|refused|clarification_required|cancelled) fail 'Workspace Analysis budget loop reached an unexpected terminal result' ;;
    esac
    sleep "${POLL_INTERVAL_SECONDS}"
  done
  fail 'Workspace Analysis budget loop did not terminate'
}

assert_workspace_analysis_v2_budget() {
  local answer_id=$1
  jq -e '
    .publication_status=="failed" and .workflow.status=="failed" and .result_type=="workspace_analysis_termination"
    and .result.schema_version=="v1" and .result.payload.termination_reason=="WORKSPACE_ANALYSIS_BUDGET_EXHAUSTED"
    and .citations==[]
  ' "${STATE_DIR}/analysis-budget-answer.json" >/dev/null || fail 'Workspace Analysis budget Answer did not publish the precise denial'
  request_json GET "/api/v2/answers/${answer_id}/analysis-timeline?workspace_id=${WORKSPACE_ID}" 200 '' 'Workspace Analysis budget timeline'
  jq -e '
    .schema_id=="conversation.workspace_analysis_timeline" and .schema_version=="v2"
    and .run_status=="failed" and .termination_reason=="WORKSPACE_ANALYSIS_BUDGET_EXHAUSTED"
    and .budget.model_calls=={used:12,max:14} and .budget.tool_calls=={used:12,max:13} and .budget.source_reads=={used:0,max:8}
    and ([.items[]|select(.kind=="node")]|length)==0 and (.items|length)==25
    and ([.items[].sequence]==[range(1;26)])
    and ([.items[]|select(.kind=="model" and .status=="succeeded")]|length)==12
    and ([.items[]|select(.kind=="tool")]|all(.status=="succeeded" and .tool_ref=={name:"ReadGitStatus",version:3}))
    and ([.items[]|select(.kind=="model")]|last|.phase=="decide_next" and .status=="pending")
  ' "${LAST_RESPONSE_FILE}" >/dev/null || fail 'Workspace Analysis budget timeline lost the denied thirteenth decision'
  cp "${LAST_RESPONSE_FILE}" "${STATE_DIR}/analysis-budget-timeline.json"
  workspace_analysis_v2_database_projection "${answer_id}" >"${STATE_DIR}/analysis-budget-database.json" || fail 'could not read Workspace Analysis budget database projection'
  jq -e '
    .definition_version==2 and .policy_version==2 and .status=="failed" and .termination_reason=="WORKSPACE_ANALYSIS_BUDGET_EXHAUSTED"
    and .settled==[12,12,0] and .reserved==[0,0,0,0,0,0] and .ledger_balanced
    and .operation_count==25 and .decision_count==12 and .reservation_count==24
    and .model_call_count==12 and .tool_call_count==12 and .receipt_count==12
    and .candidate_count==0 and .review_count==0 and .publication_count==0 and .termination_count==1
    and (.model_runs|length)==1 and (.model_runs|all(.status=="SUCCEEDED" and .schema=="agent.workspace-analysis-decision"))
    and ([.journal[].sequence]==[range(1;26)]) and (.journal|unique_by(.id)|length)==25
    and (.journal[0:24]|all(.status=="SUCCEEDED" and .reservation_id!=null and .reservation_status=="SETTLED"))
    and ([.journal[0:24][]|if .kind=="DECISION" then "decide_next" else (.tool_name+"@"+(.tool_version|tostring)) end]
      ==([range(0;12)|["decide_next","ReadGitStatus@3"]]|flatten))
    and ([.journal[]|select(.kind=="DECISION" and .status=="SUCCEEDED")|.model_call_no]==[range(1;13)])
    and (.journal[-1]|.node_key=="decide_next" and .kind=="DECISION" and .ordinal==13 and .status=="PENDING"
      and .model_call_id==null and .tool_call_id==null and .reservation_id==null and .decision_id==null and .result_id==null)
    and .termination.operation_id==.journal[-1].id and .termination.reason=="WORKSPACE_ANALYSIS_BUDGET_EXHAUSTED"
    and .termination.artifact_kind==null and .termination.artifact_id==null and .termination.requested==[1,0,0,65536,512,null]
  ' "${STATE_DIR}/analysis-budget-database.json" >/dev/null || fail 'Workspace Analysis budget denial created a thirteenth call, stranded a ledger, or lacked durable proof'
}

run_workspace_analysis_v2_additional_scenarios() {
  local analysis_question=$1 run_id=$2 conversation_id answer_id payload replay_id before after
  arm_workspace_analysis_fixture_barrier
  release_workspace_analysis_fixture_barrier
  request_json POST /api/v1/conversations 201 "$(jq -cn --arg workspace "${WORKSPACE_ID}" '{workspace_id:$workspace,title:"Compose dynamic extended analysis"}')" \
    'extended Workspace Analysis conversation' "analysis-extended-conversation-${run_id}"
  conversation_id="$(jq -er '.id' "${LAST_RESPONSE_FILE}")"
  payload="$(jq -cn --arg workspace "${WORKSPACE_ID}" --arg question "extended ${analysis_question}" '{workspace_id:$workspace,mode:"workspace_analysis",question:$question,scope:{retrieval_mode:"keyword"},answer_depth:"standard",output_format:"markdown"}')"
  request_json POST "/api/v2/conversations/${conversation_id}/questions" 202 "${payload}" 'extended Workspace Analysis submission' "analysis-extended-question-${run_id}"
  answer_id="$(jq -er '.answer.id' "${LAST_RESPONSE_FILE}")"
  CURRENT_ANSWER_ID="${answer_id}"
  wait_for_answer "${answer_id}"
  assert_workspace_analysis_v2_completed "${answer_id}" extended 6 6 2 false
  before="$(cat "${STATE_DIR}/analysis-extended-database.json")"
  request_json POST "/api/v2/conversations/${conversation_id}/questions" 200 "${payload}" 'extended Workspace Analysis replay' "analysis-extended-question-${run_id}"
  replay_id="$(jq -er '.answer.id' "${LAST_RESPONSE_FILE}")"
  after="$(workspace_analysis_v2_database_projection "${answer_id}")"
  [[ "${replay_id}" == "${answer_id}" && "${before}" == "${after}" ]] || fail 'extended Workspace Analysis replay changed durable facts'
  assert_workspace_analysis_v2_timeline_replay "${answer_id}" extended
  log 'verified: dynamic extended Search/Read/Search/Read/citation/finish, 6 decisions/8 models/6 tools/2 reads, nullable Git, independent final Citation'

  before="$(workspace_analysis_fixture_request_count workspace_analysis_budget_decision)"
  [[ "${before}" =~ ^[0-9]+$ ]] || fail 'invalid budget fixture request baseline'
  request_json POST /api/v1/conversations 201 "$(jq -cn --arg workspace "${WORKSPACE_ID}" '{workspace_id:$workspace,title:"Compose dynamic budget loop"}')" \
    'budget Workspace Analysis conversation' "analysis-budget-conversation-${run_id}"
  conversation_id="$(jq -er '.id' "${LAST_RESPONSE_FILE}")"
  payload="$(jq -cn --arg workspace "${WORKSPACE_ID}" '{workspace_id:$workspace,mode:"workspace_analysis",question:"budget-loop: keep checking Git until the server denies the next decision.",scope:{retrieval_mode:"keyword"},answer_depth:"standard",output_format:"markdown"}')"
  request_json POST "/api/v2/conversations/${conversation_id}/questions" 202 "${payload}" 'budget Workspace Analysis submission' "analysis-budget-question-${run_id}"
  answer_id="$(jq -er '.answer.id' "${LAST_RESPONSE_FILE}")"
  CURRENT_ANSWER_ID="${answer_id}"
  wait_for_workspace_analysis_budget_terminal "${answer_id}"
  assert_workspace_analysis_v2_budget "${answer_id}"
  after="$(workspace_analysis_fixture_request_count workspace_analysis_budget_decision)"
  [[ "${after}" -eq $((before+12)) ]] || fail 'budget loop did not make exactly twelve Provider requests'
  request_json POST "/api/v2/conversations/${conversation_id}/questions" 200 "${payload}" 'budget Workspace Analysis replay' "analysis-budget-question-${run_id}"
  replay_id="$(jq -er '.answer.id' "${LAST_RESPONSE_FILE}")"
  [[ "${replay_id}" == "${answer_id}" ]] || fail 'budget replay created another Answer'
  workspace_analysis_v2_database_projection "${answer_id}" >"${STATE_DIR}/analysis-budget-replay-database.json" || fail 'could not read budget replay facts'
  cmp -s "${STATE_DIR}/analysis-budget-database.json" "${STATE_DIR}/analysis-budget-replay-database.json" || fail 'budget replay changed the persisted denial or billing'
  assert_workspace_analysis_v2_timeline_replay "${answer_id}" budget
  after="$(workspace_analysis_fixture_request_count workspace_analysis_budget_decision)"
  [[ "${after}" -eq $((before+12)) ]] || fail 'budget denial replay invoked a thirteenth Provider request'
  log 'verified: 12 durable decisions/12 Git receipts, thirteenth PENDING denial, zero thirteenth ModelCall/reservation/Provider request, exact replay'
}
