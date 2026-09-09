#!/usr/bin/env bash
# Sourced only by the isolated Compose smoke; all writes use its disposable Root.

synthesis_database_projection() {
  compose exec -T postgres psql -Atq --username "${ZHIXU_POSTGRES_USER}" --dbname "${ZHIXU_POSTGRES_DB}" \
    --set workspace_id="${WORKSPACE_ID}" <<'SQL'
SELECT json_build_object(
  'notes',(SELECT count(*) FROM organizing.synthesis_note WHERE workspace_id=:'workspace_id'),
  'revisions',(SELECT count(*) FROM organizing.synthesis_revision WHERE workspace_id=:'workspace_id'),
  'receipts',(SELECT count(*) FROM organizing.synthesis_apply_receipt WHERE workspace_id=:'workspace_id'),
  'proposals',(SELECT count(*) FROM change_control.proposal WHERE workspace_id=:'workspace_id'),
  'commits',(SELECT count(*) FROM change_control.proposal_commit c JOIN change_control.proposal p ON p.id=c.proposal_id WHERE p.workspace_id=:'workspace_id'),
  'model_calls',(SELECT count(*) FROM agent.model_call c JOIN agent.model_run r ON r.id=c.model_run_id WHERE r.workspace_id=:'workspace_id'));
SQL
}

synthesis_capture_is_ready() {
  local capture_id=$1 source_version_id=$2
  jq -e --arg capture "${capture_id}" --arg source "${source_version_id}" --arg workspace "${WORKSPACE_ID}" '
    .id==$capture and .workspace_id==$workspace and .latest_source_version_id==$source
    and .ingestion_status=="READY" and .index_status=="READY" and .profile_status=="READY"
    and .retryable==false
    and (
      (.status=="READY" and (.failure_stage // "")=="" and (.error_code // "")=="")
      or (.status=="READY_DEGRADED" and .failure_stage=="INDEX" and .error_code=="CAPTURE_VECTOR_CAPABILITY_UNAVAILABLE")
    )
  ' "${LAST_RESPONSE_FILE}" >/dev/null
}

synthesis_capture_diagnostic() {
  local detail
  detail="$(jq -er '
    [.status,.ingestion_status,.index_status,.profile_status,(.failure_stage // "NONE"),(.error_code // "NONE")]
    | if all(.[]; type=="string" and test("^[A-Z][A-Z0-9_]{0,127}$")) then join("|") else "INVALID_STATUS" end
  ' "${LAST_RESPONSE_FILE}")" || detail=INVALID_STATUS
  log "synthesis Capture status/ingestion/index/profile/stage/code: ${detail}"
}

wait_for_synthesis_capture() {
  local capture_id=$1 source_version_id=$2 expected_status=$3 started_at=${SECONDS} status
  while (( SECONDS - started_at < TIMEOUT_SECONDS )); do
    request_json GET "/api/v1/workspaces/${WORKSPACE_ID}/captures/${capture_id}" 200 '' 'synthesis Capture status'
    status="$(jq -er '.status' "${LAST_RESPONSE_FILE}")"
    case "${status}" in
      FETCH_FAILED|PROCESSING_FAILED)
        synthesis_capture_diagnostic
        fail 'synthesis Capture did not finish its production pipeline'
        ;;
    esac
    if [[ "${status}" == READY || "${status}" == READY_DEGRADED ]]; then
      if ! synthesis_capture_is_ready "${capture_id}" "${source_version_id}"; then
        synthesis_capture_diagnostic
        fail 'synthesis Capture did not preserve a ready source, index, and profile'
      fi
      request_json GET "/api/v1/workspaces/${WORKSPACE_ID}/source-versions/${source_version_id}/knowledge-profile" 200 '' 'synthesis Capture profile'
      jq -e --arg capture "${capture_id}" --arg source "${source_version_id}" --arg workspace "${WORKSPACE_ID}" '
        .profile.status=="READY" and .revision!=null
        and .profile.workspace_id==$workspace and .revision.workspace_id==$workspace
        and .profile.capture_id==$capture and .profile.source_version_id==$source and .revision.source_version_id==$source
        and .profile.current_revision_id==.revision.id and .profile.id==.revision.profile_id
        and (.profile.error_code // "")=="" and .profile.retryable==false
      ' "${LAST_RESPONSE_FILE}" >/dev/null || fail 'synthesis Capture profile did not match its immutable source'
      request_json GET "/api/v1/workspaces/${WORKSPACE_ID}/synthesis/processing?limit=50" 200 '' 'synthesis processing status'
      status="$(jq -r --arg source "${source_version_id}" '[.items[] | select(.source_version_id==$source)][0].status // "PENDING"' "${LAST_RESPONSE_FILE}")"
      [[ "${status}" != "${expected_status}" ]] || return 0
      case "${status}" in
        FAILED|RECOVERY_REQUIRED|SKIPPED|NO_CHANGE|SUCCEEDED) fail "synthesis processing unexpectedly reached ${status}" ;;
      esac
    fi
    sleep "${POLL_INTERVAL_SECONDS}"
  done
  fail 'synthesis Capture or automatic processing timed out'
}

run_synthesis_browser() {
  local note_id=$1 artifact_dir=${ZHIXU_SYNTHESIS_SMOKE_ARTIFACT_DIR:-}
  local output_dir="${STATE_DIR}/playwright-synthesis"
  if [[ -n "${artifact_dir}" ]]; then
    [[ "${artifact_dir}" == /* && "${artifact_dir}" != / && ! -e "${artifact_dir}" && ! -L "${artifact_dir}" ]] || \
      fail 'synthesis browser artifact directory must be a new absolute path'
    mkdir -m 0700 -- "${artifact_dir}" || fail 'could not create the synthesis browser artifact directory'
    output_dir="${artifact_dir}/playwright"
  fi
  (
    cd "${REPOSITORY_ROOT}"
    export ZHIXU_PLAYWRIGHT_BASE_URL="${VITE_BASE_URL}" \
      ZHIXU_SYNTHESIS_SMOKE_BASE_URL="${VITE_BASE_URL}" \
      ZHIXU_SYNTHESIS_SMOKE_SESSION_TOKEN="${SESSION_TOKEN}" \
      ZHIXU_SYNTHESIS_SMOKE_CSRF_TOKEN="${CSRF_TOKEN}" \
      ZHIXU_SYNTHESIS_SMOKE_WORKSPACE_ID="${WORKSPACE_ID}" \
      ZHIXU_SYNTHESIS_SMOKE_NOTE_ID="${note_id}" \
      ZHIXU_PLAYWRIGHT_OUTPUT_DIR="${output_dir}"
    exec python3 -c 'import os; os.setsid(); os.execvp("npm", ["npm", "run", "test:e2e", "--prefix", "web", "--", "synthesis-notes.smoke.spec.ts"])'
  ) >"${STATE_DIR}/playwright.log" 2>&1 &
  PLAYWRIGHT_PID=$!
  if ! wait "${PLAYWRIGHT_PID}"; then
    PLAYWRIGHT_PID=""
    if [[ -n "${artifact_dir}" ]]; then
      cp -- "${STATE_DIR}/playwright.log" "${artifact_dir}/playwright.log"
      chmod 0600 "${artifact_dir}/playwright.log"
      log "synthesis browser diagnostics: ${artifact_dir}/playwright.log"
    fi
    fail 'Playwright synthesis notes smoke failed'
  fi
  PLAYWRIGHT_PID=""
  [[ -z "${artifact_dir}" ]] || log "synthesis browser artifacts: ${artifact_dir}"
}

run_synthesis_smoke() {
  local run_id=$1 vite_port=$2 first_text second_text payload first_capture second_capture third_capture
  local first_source second_source third_source note_id document_id first_revision proposal_id proposal_revision change_hash workflow_path
  local first_head published_head projection replay_projection target_path
  first_text='Cache source alpha. Cache entries expire after five minutes in the default configuration. The material does not explain expiration during an outage.'
  second_text='Cache source beta. Cache entries expire after five minutes in the default configuration. In the high-traffic configuration, cache entries expire after ten minutes. Refreshing invalidates the cache key. The material does not explain expiration during an outage.'
  first_head="$(git -C "${WORKSPACE_ROOT}" rev-parse HEAD)" || fail 'could not capture the synthesis Git baseline'

  log 'checking automatic Capture to generated note and approval boundary'
  payload="$(jq -cn --arg text "${first_text}" '{kind:"TEXT",display_name:"Cache expiration first",text:$text}')"
  request_json POST "/api/v1/workspaces/${WORKSPACE_ID}/captures" 201 "${payload}" 'first synthesis Capture' "synthesis-first-${run_id}"
  first_capture="$(jq -er '.capture.id' "${LAST_RESPONSE_FILE}")"
  first_source="$(jq -er '.capture.latest_source_version_id' "${LAST_RESPONSE_FILE}")"
  wait_for_synthesis_capture "${first_capture}" "${first_source}" SUCCEEDED
  request_json GET "/api/v1/workspaces/${WORKSPACE_ID}/synthesis/notes" 200 '' 'synthesis notes'
  jq -e '.items|length==1' "${LAST_RESPONSE_FILE}" >/dev/null || fail 'first Capture did not create exactly one note'
  note_id="$(jq -er '.items[0].note.id' "${LAST_RESPONSE_FILE}")"
  document_id="$(jq -er '.items[0].note.document_id' "${LAST_RESPONSE_FILE}")"
  first_revision="$(jq -er '.items[0].current_revision.id' "${LAST_RESPONSE_FILE}")"
  jq -e '.items[0].published_revision==null and .items[0].current_revision.revision_no==1' "${LAST_RESPONSE_FILE}" >/dev/null || \
    fail 'an unapproved note was published'
  request_json GET "/api/v1/workspaces/${WORKSPACE_ID}/documents/${document_id}" 200 '' 'generated Authoring publication'
  proposal_id="$(jq -er '.publication.proposal_id' "${LAST_RESPONSE_FILE}")"
  target_path="$(jq -er '.publication.target_path' "${LAST_RESPONSE_FILE}")"
  [[ ! -e "${WORKSPACE_ROOT}/${target_path}" && "$(git -C "${WORKSPACE_ROOT}" rev-parse HEAD)" == "${first_head}" ]] || \
    fail 'synthesis wrote a file or Git commit before approval'
  request_json GET "/api/v1/proposals/${proposal_id}" 200 '' 'synthesis Proposal'
  jq -e '.status=="ready_for_review"' "${LAST_RESPONSE_FILE}" >/dev/null || fail 'synthesis Proposal was not ready for review'
  proposal_revision="$(jq -er '.revision.id' "${LAST_RESPONSE_FILE}")"
  change_hash="$(jq -er '.revision.change_hash' "${LAST_RESPONSE_FILE}")"
  payload="$(jq -cn --arg revision "${proposal_revision}" --arg hash "${change_hash}" '{revision_id:$revision,change_hash:$hash,decision:"approved"}')"
  request_json POST "/api/v1/proposals/${proposal_id}/approvals" 201 "${payload}" 'synthesis Proposal approval'
  workflow_path="$(jq -er '.workflow_status_url' "${LAST_RESPONSE_FILE}")"
  wait_for_proposal "${proposal_id}" "${workflow_path}"
  published_head="$(git -C "${WORKSPACE_ROOT}" rev-parse HEAD)" || fail 'could not read the published synthesis Git HEAD'
  [[ -f "${WORKSPACE_ROOT}/${target_path}" && "${published_head}" != "${first_head}" ]] || fail 'approval did not write the generated note and Git commit'
  request_json GET "/api/v1/workspaces/${WORKSPACE_ID}/synthesis/notes/${note_id}" 200 '' 'published synthesis note'
  jq -e --arg revision "${first_revision}" '.published_revision.id==$revision and .publication.revision_id==$revision' "${LAST_RESPONSE_FILE}" >/dev/null || \
    fail 'published synthesis projection did not match the approved revision'

  log 'checking incremental synthesis, NO_CHANGE, and exact Capture replay'
  payload="$(jq -cn --arg text "${second_text}" '{kind:"TEXT",display_name:"Cache expiration update",text:$text}')"
  request_json POST "/api/v1/workspaces/${WORKSPACE_ID}/captures" 201 "${payload}" 'second synthesis Capture' "synthesis-second-${run_id}"
  second_capture="$(jq -er '.capture.id' "${LAST_RESPONSE_FILE}")"
  second_source="$(jq -er '.capture.latest_source_version_id' "${LAST_RESPONSE_FILE}")"
  wait_for_synthesis_capture "${second_capture}" "${second_source}" SUCCEEDED
  payload="$(jq -cn --arg text "${second_text}" '{kind:"TEXT",display_name:"Cache expiration repeated",text:$text}')"
  request_json POST "/api/v1/workspaces/${WORKSPACE_ID}/captures" 201 "${payload}" 'duplicate synthesis material' "synthesis-duplicate-${run_id}"
  third_capture="$(jq -er '.capture.id' "${LAST_RESPONSE_FILE}")"
  third_source="$(jq -er '.capture.latest_source_version_id' "${LAST_RESPONSE_FILE}")"
  wait_for_synthesis_capture "${third_capture}" "${third_source}" NO_CHANGE
  request_json GET "/api/v1/workspaces/${WORKSPACE_ID}/synthesis/notes/${note_id}" 200 '' 'incremental synthesis note'
  jq -e --arg first "${first_revision}" '
    .current_revision.revision_no==2 and .current_revision.parent_revision_id==$first
    and .published_revision.id==$first
    and ([.current_revision.items[].kind]|unique|sort)==["CONFLICT","FACT","GAP"]
  ' "${LAST_RESPONSE_FILE}" >/dev/null || fail 'incremental note lost its published history, facts, conflict, or gap'
  [[ "$(git -C "${WORKSPACE_ROOT}" rev-parse HEAD)" == "${published_head}" ]] || fail 'unapproved incremental candidate changed Git'
  projection="$(synthesis_database_projection)" || fail 'could not read the synthesis database projection'
  jq -e '.notes==1 and .revisions==2 and .proposals==2 and .commits==1' <<<"${projection}" >/dev/null || \
    fail 'synthesis produced duplicate revisions, proposals, or commits'
  request_json POST "/api/v1/workspaces/${WORKSPACE_ID}/captures" 200 "${payload}" 'duplicate Capture exact replay' "synthesis-duplicate-${run_id}"
  jq -e --arg id "${third_capture}" '.replayed==true and .capture.id==$id' "${LAST_RESPONSE_FILE}" >/dev/null || fail 'Capture replay changed identity'
  replay_projection="$(synthesis_database_projection)" || fail 'could not reread the synthesis database projection'
  [[ "${projection}" == "${replay_projection}" ]] || fail 'Capture replay duplicated model, proposal, or revision facts'

  start_vite "${vite_port}"
  log 'checking note history, original sources, and published-note interview in the browser'
  run_synthesis_browser "${note_id}"
  [[ "$(git -C "${WORKSPACE_ROOT}" rev-parse HEAD)" == "${published_head}" ]] || fail 'note reading or interview changed the published Git HEAD'
  log 'passed: continuous Capture synthesis, approval/Git, NO_CHANGE/replay, frozen sources/history, and desktop/mobile note interview'
}
