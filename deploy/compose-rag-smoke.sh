#!/usr/bin/env bash

set -Eeuo pipefail

readonly SCRIPT_DIR="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
readonly REPOSITORY_ROOT="$(cd -- "${SCRIPT_DIR}/.." && pwd)"
readonly COMPOSE_FILE="${SCRIPT_DIR}/compose.yml"
readonly BOOTSTRAP_COMPOSE_FILE="${SCRIPT_DIR}/compose.bootstrap.yml"
readonly NETNS_COMPOSE_FILE="${SCRIPT_DIR}/compose.netns.yml"
readonly STATIC_MODELS_COMPOSE_FILE="${SCRIPT_DIR}/compose.static-models.yml"
readonly RAG_COMPOSE_FILE="${SCRIPT_DIR}/compose.rag-smoke.yml"
readonly RAG_HOST_RELAY_COMPOSE_FILE="${SCRIPT_DIR}/compose.rag-host-relay-smoke.yml"
readonly WORKSPACE_ANALYSIS_OTLP_COMPOSE_FILE="${SCRIPT_DIR}/compose.workspace-analysis-otlp-smoke.yml"
readonly ENV_FILE="${REPOSITORY_ROOT}/.env.example"
readonly REAL_PROVIDER_MODE="${ZHIXU_COMPOSE_RAG_REAL_PROVIDER:-0}"
readonly REAL_PROVIDER_PREFLIGHT_ONLY="${ZHIXU_COMPOSE_RAG_REAL_PROVIDER_PREFLIGHT_ONLY:-0}"
readonly RAG_BROWSER_MODE="${ZHIXU_COMPOSE_RAG_BROWSER:-0}"
readonly SYNTHESIS_MODE="${ZHIXU_COMPOSE_SYNTHESIS:-0}"
readonly WORKSPACE_ANALYSIS_MODE="${ZHIXU_COMPOSE_WORKSPACE_ANALYSIS:-0}"
readonly WORKSPACE_ANALYSIS_OTLP_MODE="${ZHIXU_COMPOSE_WORKSPACE_ANALYSIS_OTLP:-0}"
readonly WORKSPACE_ANALYSIS_WORKER_RESTART_MODE="${ZHIXU_COMPOSE_WORKSPACE_ANALYSIS_WORKER_RESTART:-0}"
readonly WORKSPACE_ANALYSIS_BARRIER_CONTAINER_RELEASE_FILE='/tmp/zhixu-workspace-analysis-candidate.release'
readonly WORKSPACE_ANALYSIS_BARRIER_CONTAINER_ENTERED_FILE='/tmp/zhixu-workspace-analysis-candidate.entered'
readonly WORKSPACE_ANALYSIS_BARRIER_CONTAINER_SETTLED_FILE='/tmp/zhixu-workspace-analysis-candidate.settled'
readonly WORKSPACE_ANALYSIS_BARRIER_CONTAINER_ARMED_FILE='/tmp/zhixu-workspace-analysis-candidate.armed'
readonly WORKSPACE_ANALYSIS_BARRIER_CONTAINER_LOCK_DIR='/tmp/zhixu-workspace-analysis-candidate.claim'
readonly TIMEOUT_SECONDS="${ZHIXU_COMPOSE_RAG_SMOKE_TIMEOUT_SECONDS:-300}"
readonly POLL_INTERVAL_SECONDS="${ZHIXU_COMPOSE_RAG_SMOKE_POLL_INTERVAL_SECONDS:-2}"
readonly REQUEST_TIMEOUT_SECONDS="${ZHIXU_COMPOSE_RAG_SMOKE_REQUEST_TIMEOUT_SECONDS:-15}"

source "${SCRIPT_DIR}/compose-smoke-cleanup.sh"
source "${SCRIPT_DIR}/rag-real-provider-config.sh"

STATE_DIR=""
PROJECT_NAME=""
NETNS_PROJECT_NAME=""
NETNS_NETWORK_NAME=""
APP_NETNS_CONTAINER=""
WORKER_NETNS_CONTAINER=""
MAIN_NETNS_OVERRIDE_FILE=""
NETNS_OVERRIDE_FILE=""
API_BASE_URL=""
LAST_RESPONSE_FILE=""
CANARY_COMPOSE_FILE=""
RUNTIME_COMPOSE_FILE=""
GRANT_COMPOSE_FILE=""
WORKSPACE_ROOT=""
WORKSPACE_ID=""
CURRENT_ANSWER_ID=""
AUTH_ORIGIN=""
CSRF_TOKEN=""
COOKIE_JAR=""
SESSION_TOKEN=""
VITE_BASE_URL=""
VITE_PID=""
PLAYWRIGHT_PID=""
PROVIDER_HOST_RELAY_PID=""
PROVIDER_HOST_RELAY_PORT=""
PROVIDER_HOST_RELAY_CONFIG_FILE=""
PROVIDER_HOST_RELAY_BINARY=""
RAG_REAL_PROVIDER_TRANSPORT_RESOLVED="${ZHIXU_RAG_REAL_PROVIDER_TRANSPORT:-direct}"
RAG_NETNS_OVERRIDE_FILE=""
WORKSPACE_DOCKER_LOG=""
WORKSPACE_ANALYSIS_BARRIER_DIR=""
WORKSPACE_ANALYSIS_BARRIER_READY_FILE=""
WORKSPACE_ANALYSIS_BARRIER_ENTERED_FILE=""
WORKSPACE_ANALYSIS_BARRIER_SETTLED_FILE=""
WORKSPACE_ANALYSIS_BARRIER_TOKEN=""
WORKSPACE_ANALYSIS_BROWSER_PROGRESS_FILE=""

log() { printf '[compose-rag-smoke] %s\n' "$1"; }

compose() {
  local -a compose_files=(-f "${COMPOSE_FILE}")
  if [[ "${REAL_PROVIDER_MODE}" == 1 ]]; then
    compose_files+=(-f "${STATIC_MODELS_COMPOSE_FILE}")
    if [[ "${RAG_REAL_PROVIDER_TRANSPORT_RESOLVED:-direct}" == host-relay ]]; then
      compose_files+=(-f "${RAG_HOST_RELAY_COMPOSE_FILE}")
    fi
  else
    compose_files+=(-f "${STATIC_MODELS_COMPOSE_FILE}" -f "${RAG_COMPOSE_FILE}")
  fi
  [[ "${WORKSPACE_ANALYSIS_OTLP_MODE}" != 1 ]] || compose_files+=(-f "${WORKSPACE_ANALYSIS_OTLP_COMPOSE_FILE}")
  [[ -z "${CANARY_COMPOSE_FILE}" ]] || compose_files+=(-f "${CANARY_COMPOSE_FILE}")
  [[ -z "${GRANT_COMPOSE_FILE}" || ! -f "${GRANT_COMPOSE_FILE}" ]] || compose_files+=(-f "${GRANT_COMPOSE_FILE}")
  [[ -z "${MAIN_NETNS_OVERRIDE_FILE}" || ! -f "${MAIN_NETNS_OVERRIDE_FILE}" ]] || compose_files+=(-f "${MAIN_NETNS_OVERRIDE_FILE}")
  [[ -z "${RAG_NETNS_OVERRIDE_FILE}" || ! -f "${RAG_NETNS_OVERRIDE_FILE}" ]] || compose_files+=(-f "${RAG_NETNS_OVERRIDE_FILE}")
  docker compose --project-name "${PROJECT_NAME}" "${compose_files[@]}" --env-file "${ENV_FILE}" "$@"
}

assert_workspace_analysis_otlp_metrics() {
  local metrics_file="${STATE_DIR}/workspace-analysis-worker-otlp.prom"
  compose stop --timeout 45 worker >/dev/null || fail 'could not gracefully stop the Workspace Analysis Worker for OTLP flush'
  compose exec -T --user 10001:10001 rag-model-fixture \
    wget -q -O - http://127.0.0.1:8889/metrics >"${metrics_file}" || \
    fail 'could not read the isolated OpenTelemetry Collector projection'
  chmod 0600 "${metrics_file}"
  python3 - "${metrics_file}" <<'PY' || fail 'Worker OTLP metric projection is incomplete or unsafe'
import re
import sys

path = sys.argv[1]
expected_names = {
    "workspace_analysis_outcome_total",
    "runtime_process_presence",
    "runtime_telemetry_required",
}
series = {}
with open(path, encoding="utf-8") as stream:
    for raw in stream:
        name = raw.split("{", 1)[0]
        if name not in expected_names:
            continue
        matched = re.fullmatch(r'([a-zA-Z_:][a-zA-Z0-9_:]*)\{([^}]*)\} ([0-9]+(?:\.[0-9]+)?)\n?', raw)
        if matched is None:
            raise SystemExit(1)
        pairs = re.findall(r'([a-zA-Z_][a-zA-Z0-9_]*)="([^"\\]*)"', matched.group(2))
        labels = dict(pairs)
        if len(labels) != len(pairs) or ",".join(f'{key}="{value}"' for key, value in pairs) != matched.group(2):
            raise SystemExit(1)
        series.setdefault(matched.group(1), []).append((labels, float(matched.group(3))))

resource = {
    "service_name": "zhixu-worker",
    "service_version": "dev",
    "deployment_environment": "development",
    "job": "zhixu-worker",
}
expected = {
    "workspace_analysis_outcome_total": (resource | {
        "mode": "workspace_analysis",
        "definition": "workspace-analysis-v2",
        "outcome": "completed",
        "termination_reason": "COMPLETED",
    }, 1.0),
    "runtime_process_presence": (resource, 1.0),
    "runtime_telemetry_required": (resource, 1.0),
}
def fail_projection():
    for metric_name in sorted(expected_names):
        observed = series.get(metric_name, [])
        print(
            "metric_projection"
            f"|name={metric_name}"
            f"|series={len(observed)}"
            f"|keys={';'.join(','.join(sorted(labels)) for labels, _ in observed)}"
            f"|values={','.join(str(value) for _, value in observed)}",
            file=sys.stderr,
        )
    raise SystemExit(1)

if set(series) != set(expected):
    fail_projection()
for name, (expected_labels, expected_value) in expected.items():
    if len(series[name]) != 1:
        fail_projection()
    labels, value = series[name][0]
    if labels != expected_labels or value != expected_value:
        fail_projection()
PY
}

wait_for_workspace_analysis_otlp_collector() {
  local started_at=${SECONDS}
  while (( SECONDS - started_at < 30 )); do
    if compose exec -T --user 10001:10001 rag-model-fixture \
      wget -q -O /dev/null http://127.0.0.1:13133/ >/dev/null 2>&1; then
      return 0
    fi
    sleep 1
  done
  fail 'OpenTelemetry Collector did not become healthy before Worker startup'
}

bootstrap_compose() {
  local -a compose_files=(-f "${COMPOSE_FILE}" -f "${BOOTSTRAP_COMPOSE_FILE}")
  [[ -z "${MAIN_NETNS_OVERRIDE_FILE}" || ! -f "${MAIN_NETNS_OVERRIDE_FILE}" ]] || compose_files+=(-f "${MAIN_NETNS_OVERRIDE_FILE}")
  docker compose --project-name "${PROJECT_NAME}" "${compose_files[@]}" --env-file "${ENV_FILE}" "$@"
}

netns_compose() {
  docker compose --project-name "${NETNS_PROJECT_NAME}" -f "${NETNS_COMPOSE_FILE}" -f "${NETNS_OVERRIDE_FILE}" --env-file "${ENV_FILE}" "$@"
}

diagnose() {
  [[ "${REAL_PROVIDER_PREFLIGHT_ONLY}" != 1 ]] || return 0
  [[ -n "${PROJECT_NAME}" && -n "${CANARY_COMPOSE_FILE}" ]] || return 0
  printf '[compose-rag-smoke] diagnostic: service state (response bodies, model messages, evidence and credentials omitted)\n' >&2
  compose ps --format 'table {{.Service}}\t{{.State}}\t{{.Health}}' >&2 2>/dev/null || true

  local fixture_rejections
  fixture_rejections="$(compose logs --no-color --tail 100 rag-model-fixture 2>/dev/null | \
    grep -oE 'rag model fixture rejected stage=[a-z_]+ reason=[a-z0-9_]+ status=[0-9]{3}' || true)"
  if [[ -n "${fixture_rejections}" ]]; then
    printf '[compose-rag-smoke] diagnostic: safe model fixture contract rejections\n%s\n' "${fixture_rejections}" >&2
  fi

  local fixture_stages
  fixture_stages="$(compose logs --no-color --tail 100 rag-model-fixture 2>/dev/null | \
    grep -oE 'rag model fixture request stage=[a-z_]+' | sort | uniq -c | head -20 || true)"
  if [[ -n "${fixture_stages}" ]]; then
    printf '[compose-rag-smoke] diagnostic: safe model fixture request stage counts\n%s\n' "${fixture_stages}" >&2
  fi

  if [[ -n "${STATE_DIR}" && -f "${STATE_DIR}/playwright.log" ]]; then
    local browser_failure_count browser_failure_locations browser_gate_codes browser_runtime_issues
    browser_failure_count="$(grep -Ec '(^[[:space:]]*[0-9]+\)|Error:|Timeout|waiting for|at .*(rag-real-provider|rag-fixture|workspace-analysis)\.smoke\.spec\.ts)' "${STATE_DIR}/playwright.log" 2>/dev/null || true)"
    if [[ "${browser_failure_count}" != 0 ]]; then
      printf '[compose-rag-smoke] diagnostic: Playwright failure details omitted; matched lines=%s\n' "${browser_failure_count}" >&2
    fi
    browser_failure_locations="$(grep -oE '(rag-real-provider|rag-fixture|workspace-analysis)\.smoke\.spec\.ts:[0-9]+:[0-9]+' \
      "${STATE_DIR}/playwright.log" 2>/dev/null | sort -u | head -20 || true)"
    if [[ -n "${browser_failure_locations}" ]]; then
      printf '[compose-rag-smoke] diagnostic: safe Playwright failure locations\n%s\n' "${browser_failure_locations}" >&2
    fi
    browser_gate_codes="$(grep -oE '(RAG_BROWSER|RAG_FIXTURE_BROWSER|WORKSPACE_ANALYSIS_BROWSER)_[A-Z0-9_]+' "${STATE_DIR}/playwright.log" 2>/dev/null | sort -u | head -20 || true)"
    if [[ -n "${browser_gate_codes}" ]]; then
      printf '[compose-rag-smoke] diagnostic: safe browser gate codes\n%s\n' "${browser_gate_codes}" >&2
    fi
    browser_runtime_issues="$(grep -oE '(desktop|mobile)\.(console\.(warning|error)(\.[a-z0-9_]+)?|pageerror|http\.[0-9]{3})' \
      "${STATE_DIR}/playwright.log" 2>/dev/null | sort -u | head -20 || true)"
    if [[ -n "${browser_runtime_issues}" ]]; then
      printf '[compose-rag-smoke] diagnostic: safe browser runtime issue classes\n%s\n' "${browser_runtime_issues}" >&2
    fi
  fi
  local workspace_analysis_browser_submitted=0
  if [[ -n "${WORKSPACE_ANALYSIS_BROWSER_PROGRESS_FILE}" && -f "${WORKSPACE_ANALYSIS_BROWSER_PROGRESS_FILE}" ]]; then
    local browser_progress
    browser_progress="$(grep -E '^(STARTED|CHAT_READY|TITLE_FILLED|CREATE_SUBMITTED|CREATE_ACCEPTED|CONVERSATION_ROUTED|CONVERSATION_CREATED|MODE_SELECTED|QUESTION_ACCEPTED|STOP_VISIBLE|STOP_REQUEST_SENT|STOP_RESPONSE_NONE|STOP_RESPONSE_VERSION_CONFLICT_ONLY|STOP_RESPONSE_INVALID|STOP_VERSION_REFRESHED|STOP_ACCEPTED|STOP_CLICKED|TERMINAL_VISIBLE|TIMELINE_VERIFIED|DESKTOP_VERIFIED|MOBILE_VERIFIED|COMPLETED)$' \
      "${WORKSPACE_ANALYSIS_BROWSER_PROGRESS_FILE}" | tail -1 || true)"
    [[ -z "${browser_progress}" ]] || printf '[compose-rag-smoke] diagnostic: Workspace Analysis browser progress=%s\n' "${browser_progress}" >&2
    case "${browser_progress}" in
      QUESTION_ACCEPTED|STOP_VISIBLE|STOP_REQUEST_SENT|STOP_RESPONSE_NONE|STOP_RESPONSE_VERSION_CONFLICT_ONLY|STOP_RESPONSE_INVALID|STOP_VERSION_REFRESHED|STOP_ACCEPTED|STOP_CLICKED|TERMINAL_VISIBLE|TIMELINE_VERIFIED|DESKTOP_VERIFIED|MOBILE_VERIFIED|COMPLETED)
        workspace_analysis_browser_submitted=1
        ;;
    esac
  fi

  if [[ -n "${WORKSPACE_ANALYSIS_BARRIER_TOKEN}" && -n "${PROJECT_NAME}" ]]; then
    compose exec -T --user 10001:10001 rag-model-fixture sh -ec '
      token="$6"
      current() { [ -f "$1" ] && [ "$(cat "$1")" = "${token}" ]; }
      armed=0; entered=0; settled=0; released=0; claimed=0
      current "$1" && armed=1
      current "$2" && entered=1
      current "$3" && settled=1
      current "$4" && released=1
      [ ! -d "$5" ] || claimed=1
      printf "barrier_state|armed_current=%s|entered_current=%s|settled_current=%s|release_current=%s|claim_locked=%s\n" \
        "${armed}" "${entered}" "${settled}" "${released}" "${claimed}"
    ' sh "${WORKSPACE_ANALYSIS_BARRIER_CONTAINER_ARMED_FILE}" \
      "${WORKSPACE_ANALYSIS_BARRIER_CONTAINER_ENTERED_FILE}" \
      "${WORKSPACE_ANALYSIS_BARRIER_CONTAINER_SETTLED_FILE}" \
      "${WORKSPACE_ANALYSIS_BARRIER_CONTAINER_RELEASE_FILE}" \
      "${WORKSPACE_ANALYSIS_BARRIER_CONTAINER_LOCK_DIR}" \
      "${WORKSPACE_ANALYSIS_BARRIER_TOKEN}" >&2 2>/dev/null || true
  fi

  if [[ -n "${WORKSPACE_DOCKER_LOG}" && -f "${WORKSPACE_DOCKER_LOG}" ]]; then
    python3 - "${WORKSPACE_DOCKER_LOG}" >&2 <<'PY' || true
import json
import re
import sys

seen = set()
with open(sys.argv[1], encoding="utf-8", errors="replace") as stream:
    for raw in stream:
        start = raw.find("{")
        if start < 0:
            text_match = re.search(r"\blevel=ERROR\b.*?\bmsg=(?:\"([^\"]{1,160})\"|([^\s]+))", raw)
            if text_match:
                message = text_match.group(1) or text_match.group(2) or ""
                stage = message if re.fullmatch(r"[a-z][a-z0-9_]{0,63}", message) else "unclassified_non_json"
                if stage not in seen:
                    seen.add(stage)
                    print("[compose-rag-smoke] diagnostic: runtime error stage: " + stage)
                if len(seen) >= 20:
                    break
            compose_categories = (
                (r"dependency failed to start", "dependency_failed_to_start"),
                (r" is unhealthy", "service_unhealthy"),
                (r"failed to start", "service_failed_to_start"),
                (r"Error response from daemon", "daemon_error"),
                (r"no such service", "service_missing"),
                (r"exited with code", "service_exited_nonzero"),
            )
            for pattern, category in compose_categories:
                if re.search(pattern, raw, re.IGNORECASE) and category not in seen:
                    seen.add(category)
                    print("[compose-rag-smoke] diagnostic: runtime compose: " + category)
                if len(seen) >= 20:
                    break
            if len(seen) >= 20:
                break
            continue
        try:
            entry = json.loads(raw[start:])
        except json.JSONDecodeError:
            continue
        if str(entry.get("level", "")).upper() != "ERROR":
            continue
        message = str(entry.get("msg", ""))
        code = str(entry.get("error_code", ""))
        stage = message if re.fullmatch(r"[a-z][a-z0-9_]{0,63}", message) else "unclassified_json"
        if code and not re.fullmatch(r"[A-Z][A-Z0-9_]{0,127}", code):
            code = ""
        summary = stage + ((" (" + code + ")") if code else "")
        if summary in seen:
            continue
        seen.add(summary)
        print("[compose-rag-smoke] diagnostic: runtime error stage: " + summary)
        if len(seen) >= 20:
            break
PY
  fi

  if [[ -n "${PROJECT_NAME}" ]]; then
    compose exec -T postgres psql -Atq --username "${ZHIXU_POSTGRES_USER}" --dbname "${ZHIXU_POSTGRES_DB}" <<'SQL' >&2 2>/dev/null || true
SELECT 'workspace_switch|'||phase||'|'||COALESCE(result,'')||'|'||COALESCE(error_code,'')||'|'||version::text
FROM ops.workspace_switch
ORDER BY created_at DESC,id DESC
LIMIT 1;
SELECT 'workspace_control|'||COALESCE(operation_phase,'')||'|'||COALESCE(last_error_code,'')||'|'||state_version::text
FROM ops.workspace_control_state
WHERE singleton=true;
SELECT 'workspace_runtime|'||role||'|'||phase||'|'||version::text
FROM ops.workspace_runtime
ORDER BY role;
SQL
  fi

  local diagnostic_answer_id="${CURRENT_ANSWER_ID}"
  if [[ "${workspace_analysis_browser_submitted}" == 1 && -n "${WORKSPACE_ID}" ]]; then
    diagnostic_answer_id="$(compose exec -T postgres psql -Atq --username "${ZHIXU_POSTGRES_USER}" --dbname "${ZHIXU_POSTGRES_DB}" \
      --set workspace_id="${WORKSPACE_ID}" \
      -c "SELECT answer.id::text FROM agent.answer answer JOIN agent.workspace_analysis_run analysis ON analysis.answer_id=answer.id AND analysis.workspace_id=answer.workspace_id WHERE answer.workspace_id=:'workspace_id' ORDER BY answer.created_at DESC,answer.id DESC LIMIT 1" \
      2>/dev/null || true)"
  elif [[ -z "${diagnostic_answer_id}" && -n "${WORKSPACE_ID}" ]]; then
    diagnostic_answer_id="$(compose exec -T postgres psql -Atq --username "${ZHIXU_POSTGRES_USER}" --dbname "${ZHIXU_POSTGRES_DB}" \
      --set workspace_id="${WORKSPACE_ID}" \
      -c "SELECT id::text FROM agent.answer WHERE workspace_id=:'workspace_id' ORDER BY created_at DESC,id DESC LIMIT 1" \
      2>/dev/null || true)"
  fi
  if [[ -n "${diagnostic_answer_id}" ]]; then
    compose exec -T postgres psql -Atq --username "${ZHIXU_POSTGRES_USER}" --dbname "${ZHIXU_POSTGRES_DB}" \
      --set answer_id="${diagnostic_answer_id}" >&2 2>/dev/null <<'SQL' || true
SELECT 'answer|'||a.publication_status||'|'||a.version||'|'||COALESCE(a.result_type,'')||'|'||r.status||'|'||r.version
FROM agent.answer a JOIN workflow.run r ON r.id=a.workflow_run_id WHERE a.id=:'answer_id';
SELECT 'workflow_binding|'||a.workflow_run_id::text||'|'||d.key||'@'||d.version::text||'|'||d.id::text
FROM agent.answer a
JOIN workflow.run r ON r.id=a.workflow_run_id
JOIN workflow.definition d ON d.id=r.definition_id
WHERE a.id=:'answer_id';
SELECT 'definition_tools|'||COALESCE(string_agg(t.name||'@'||t.version,',' ORDER BY t.name,t.version),'')
FROM (
  SELECT tool->>'name' AS name,tool->>'version' AS version
  FROM agent.answer a
  JOIN workflow.run r ON r.id=a.workflow_run_id
  JOIN workflow.definition d ON d.id=r.definition_id
  CROSS JOIN LATERAL jsonb_array_elements(d.graph->'nodes') node
  CROSS JOIN LATERAL jsonb_array_elements(COALESCE(node->'allowed_tools','[]'::jsonb)) tool
  WHERE a.id=:'answer_id' AND node->>'key'='rag-answer'
  ORDER BY tool->>'name',tool->>'version'
  LIMIT 20
) t;
SELECT 'node|'||n.status||'|'||n.version||'|'||COALESCE(n.error_code,'')
FROM workflow.node_run n JOIN agent.answer a ON a.workflow_run_id=n.run_id
WHERE a.id=:'answer_id' ORDER BY n.created_at DESC,n.id DESC LIMIT 20;
SELECT 'model_run|'||m.status||'|'||m.version||'|'||COALESCE(m.error_code,'')
FROM agent.model_run m JOIN agent.answer a ON a.workflow_run_id=m.workflow_run_id
WHERE a.id=:'answer_id' ORDER BY m.started_at DESC,m.id DESC LIMIT 20;
SELECT 'model_call|'||c.phase||'|'||c.status||'|'||COALESCE(c.error_code,'')
FROM agent.model_call c JOIN agent.model_run m ON m.id=c.model_run_id JOIN agent.answer a ON a.workflow_run_id=m.workflow_run_id
WHERE a.id=:'answer_id' ORDER BY c.started_at DESC,c.id DESC LIMIT 20;
SELECT 'tool_call|'||c.requested_tool_name||'@'||COALESCE(c.tool_version::text,'')||'|'||c.status||'|'||COALESCE(c.error_code,'')||'|'||c.node_run_id::text||'|'||c.node_attempt_id::text||'|'||c.call_no::text
FROM workflow.tool_call c JOIN agent.answer a ON a.workflow_run_id=c.workflow_run_id
WHERE a.id=:'answer_id' ORDER BY c.started_at DESC,c.id DESC LIMIT 20;
SELECT 'search_receipt|'||jsonb_array_length(document->'items')::text||'|'||
       COALESCE(document->>'effective_mode','')||'|'||jsonb_array_length(document->'degradations')::text
FROM agent.answer a
JOIN agent.workspace_analysis_run analysis ON analysis.answer_id=a.id AND analysis.workspace_id=a.workspace_id
JOIN agent.workspace_analysis_operation operation
  ON operation.analysis_run_id=analysis.id
 AND operation.operation_kind='KNOWLEDGE_SEARCH'
 AND operation.ordinal=1
JOIN workflow.tool_result_receipt receipt
  ON receipt.id=operation.result_id
CROSS JOIN LATERAL (SELECT convert_from(receipt.output_document,'UTF8')::jsonb AS document) decoded
WHERE a.id=:'answer_id';
SELECT 'draft|'||d.generation::text||'|'||d.status
FROM agent.answer_draft_session d WHERE d.answer_id=:'answer_id' ORDER BY d.updated_at DESC,d.id DESC LIMIT 20;
SQL
  elif [[ -n "${WORKSPACE_ID}" ]]; then
    compose exec -T postgres psql -Atq --username "${ZHIXU_POSTGRES_USER}" --dbname "${ZHIXU_POSTGRES_DB}" \
      --set workspace_id="${WORKSPACE_ID}" >&2 2>/dev/null <<'SQL' || true
SELECT 'latest_run|'||r.status||'|'||r.version||'|'||r.id::text
FROM workflow.run r WHERE r.workspace_id=:'workspace_id' ORDER BY r.created_at DESC,r.id DESC LIMIT 1;
SELECT 'latest_node|'||n.status||'|'||n.version||'|'||COALESCE(n.error_code,'')||'|'||n.id::text
FROM workflow.node_run n JOIN workflow.run r ON r.id=n.run_id
WHERE r.workspace_id=:'workspace_id' ORDER BY n.created_at DESC,n.id DESC LIMIT 1;
SELECT 'latest_model_call|'||c.phase||'|'||c.status||'|'||COALESCE(c.error_code,'')
FROM agent.model_call c JOIN agent.model_run m ON m.id=c.model_run_id
WHERE m.workspace_id=:'workspace_id' ORDER BY c.started_at DESC,c.id DESC LIMIT 1;
SELECT 'model_call_history|'||a.attempt_no::text||'|'||c.call_no::text||'|'||c.phase||'|'||c.status||'|'||COALESCE(c.error_code,'')
FROM agent.model_call c
JOIN agent.model_run m ON m.id=c.model_run_id
JOIN workflow.node_attempt a ON a.id=m.node_attempt_id AND a.node_run_id=m.node_run_id
WHERE m.workspace_id=:'workspace_id'
ORDER BY c.started_at DESC,c.id DESC
LIMIT 20;
SELECT 'recent_draft_history|'||count(*)::text||'|'||COALESCE(string_agg(d.status,',' ORDER BY d.updated_at DESC,d.id DESC),'')
FROM (
  SELECT id,status,updated_at
  FROM agent.answer_draft_session
  WHERE workspace_id=:'workspace_id'
  ORDER BY updated_at DESC,id DESC
  LIMIT 20
) d;
SQL
  fi
}

fail() {
  printf '[compose-rag-smoke] failed: %s\n' "$1" >&2
  diagnose
  exit 1
}

stop_vite() {
  [[ -n "${VITE_PID}" ]] || return 0
  if kill -0 "${VITE_PID}" >/dev/null 2>&1; then
    kill -TERM -- "-${VITE_PID}" >/dev/null 2>&1 || kill -TERM "${VITE_PID}" >/dev/null 2>&1 || true
  fi
  wait "${VITE_PID}" >/dev/null 2>&1 || true
  VITE_PID=""
}

stop_playwright() {
  [[ -n "${PLAYWRIGHT_PID}" ]] || return 0
  if kill -0 "${PLAYWRIGHT_PID}" >/dev/null 2>&1; then
    kill -TERM -- "-${PLAYWRIGHT_PID}" >/dev/null 2>&1 || kill -TERM "${PLAYWRIGHT_PID}" >/dev/null 2>&1 || true
  fi
  wait "${PLAYWRIGHT_PID}" >/dev/null 2>&1 || true
  PLAYWRIGHT_PID=""
}

stop_provider_host_relay() {
  [[ -n "${PROVIDER_HOST_RELAY_PID}" ]] || return 0
  if kill -0 "${PROVIDER_HOST_RELAY_PID}" >/dev/null 2>&1; then
    kill -TERM -- "-${PROVIDER_HOST_RELAY_PID}" >/dev/null 2>&1 || kill -TERM "${PROVIDER_HOST_RELAY_PID}" >/dev/null 2>&1 || true
  fi
  wait "${PROVIDER_HOST_RELAY_PID}" >/dev/null 2>&1 || true
  PROVIDER_HOST_RELAY_PID=""
}

cleanup() {
  local exit_code=$?
  local cleanup_exit=0
  trap - EXIT HUP INT TERM
  stop_playwright
  stop_vite
  if [[ -n "${PROJECT_NAME}" && "${REAL_PROVIDER_PREFLIGHT_ONLY}" != 1 ]]; then
    if [[ -n "${GRANT_COMPOSE_FILE}" && -f "${GRANT_COMPOSE_FILE}" ]]; then
      compose stop proxy app-model-relay worker-model-relay app worker >/dev/null 2>&1 || \
        log "consumer pre-stop returned nonzero; full project cleanup will verify removal"
      compose rm --force --stop proxy firewall app-model-relay worker-model-relay app worker >/dev/null 2>&1 || \
        log "consumer pre-remove returned nonzero; full project cleanup will verify removal"
    fi
    cleanup_compose_smoke_project_images "${PROJECT_NAME}" "${NETNS_PROJECT_NAME}" || cleanup_exit=1
  fi
  stop_provider_host_relay
  if [[ -n "${STATE_DIR}" ]]; then
    chmod -R u+rwX "${STATE_DIR}" >/dev/null 2>&1 || true
    if ! rm -rf -- "${STATE_DIR}" >/dev/null 2>&1; then
      log "could not remove disposable state"
      cleanup_exit=1
    fi
  fi
  if [[ "${exit_code}" -ne 0 ]]; then
    exit "${exit_code}"
  fi
  exit "${cleanup_exit}"
}

require_command() { command -v "$1" >/dev/null 2>&1 || fail "required command is unavailable: $1"; }

random_hex() {
  python3 - "$1" <<'PY'
import secrets, sys
print(secrets.token_hex(int(sys.argv[1])))
PY
}

allocate_port() {
  python3 - <<'PY'
import socket
with socket.socket(socket.AF_INET, socket.SOCK_STREAM) as sock:
    sock.bind(("127.0.0.1", 0))
    print(sock.getsockname()[1])
PY
}

wait_for_provider_host_relay() {
  local started_at=${SECONDS}
  while (( SECONDS - started_at < 30 )); do
    if curl --fail --silent --show-error --max-time 2 "http://127.0.0.1:${PROVIDER_HOST_RELAY_PORT}/healthz" >/dev/null 2>&1; then
      return 0
    fi
    if [[ -n "${PROVIDER_HOST_RELAY_PID}" ]] && ! kill -0 "${PROVIDER_HOST_RELAY_PID}" >/dev/null 2>&1; then
      fail 'Provider host relay exited before becoming ready'
    fi
    sleep 1
  done
  fail 'Provider host relay did not become ready'
}

start_provider_host_relay() {
  [[ "${RAG_REAL_PROVIDER_TRANSPORT_RESOLVED:-direct}" == host-relay ]] || return 0
  PROVIDER_HOST_RELAY_CONFIG_FILE="${STATE_DIR}/provider-host-relay.json"
  PROVIDER_HOST_RELAY_BINARY="${STATE_DIR}/zhixu-rag-provider-host-relay"
  umask 077
  jq -cn --arg chat_base_url "${RAG_REAL_PROVIDER_CHAT_BASE_URL}" '{chat_base_url:$chat_base_url}' >"${PROVIDER_HOST_RELAY_CONFIG_FILE}"
  chmod 600 "${PROVIDER_HOST_RELAY_CONFIG_FILE}"
  (
    cd "${REPOSITORY_ROOT}"
    go build -mod=vendor -trimpath -o "${PROVIDER_HOST_RELAY_BINARY}" ./cmd/rag-provider-host-relay
  ) >/dev/null || fail 'Provider host relay could not be built'
  (
    cd "${REPOSITORY_ROOT}"
    export ZHIXU_RAG_PROVIDER_HOST_RELAY_LISTEN_ADDR="127.0.0.1:${PROVIDER_HOST_RELAY_PORT}"
    export ZHIXU_RAG_PROVIDER_HOST_RELAY_CONFIG_PATH="${PROVIDER_HOST_RELAY_CONFIG_FILE}"
    exec python3 -c 'import os, sys; os.setsid(); os.execv(sys.argv[1], [sys.argv[1]])' "${PROVIDER_HOST_RELAY_BINARY}"
  ) >"${STATE_DIR}/provider-host-relay.log" 2>&1 &
  PROVIDER_HOST_RELAY_PID=$!
  wait_for_provider_host_relay
}

provider_host_relay_request_count() {
  curl --fail --silent --show-error --max-time 2 "http://127.0.0.1:${PROVIDER_HOST_RELAY_PORT}/healthz" | \
    jq -er '.forwarded_count | select(type == "number" and . >= 0)'
}

request_json() {
  local method=$1 path=$2 expected_status=$3 body=${4-} label=$5 idempotency_key=${6-}
  local response_file http_status
  response_file="$(mktemp "${STATE_DIR}/response.XXXXXX")" || fail "could not allocate a private response file"
  local -a args=(--silent --show-error --connect-timeout 5 --max-time "${REQUEST_TIMEOUT_SECONDS}"
    --output "${response_file}" --write-out '%{http_code}' --request "${method}")
  if [[ -n "${COOKIE_JAR}" ]]; then
    args+=(--cookie "${COOKIE_JAR}")
    if [[ "${method}" != GET && "${method}" != HEAD && "${method}" != OPTIONS ]]; then
      args+=(--header "Origin: ${AUTH_ORIGIN}" --header "X-CSRF-Token: ${CSRF_TOKEN}")
    fi
  fi
  [[ -z "${WORKSPACE_ID}" ]] || args+=(--header "X-Workspace-ID: ${WORKSPACE_ID}")
  [[ -z "${idempotency_key}" ]] || args+=(--header "Idempotency-Key: ${idempotency_key}")
  if [[ -n "${body}" ]]; then
    args+=(--header 'Content-Type: application/json' --data-binary "${body}")
  fi
  http_status="$(curl "${args[@]}" "${API_BASE_URL}${path}")" || fail "${label} could not reach the API"
  LAST_RESPONSE_FILE="${response_file}"
  if [[ "${http_status}" != "${expected_status}" ]]; then
    local problem_code
    problem_code="$(jq -r 'if type=="object" then (.error_code // "unknown") else "non-json" end' "${response_file}" 2>/dev/null || printf unknown)"
    fail "${label} returned HTTP ${http_status} (${problem_code})"
  fi
  jq -e 'type == "object"' "${response_file}" >/dev/null || fail "${label} returned invalid JSON"
}

authenticate() {
  local response_file http_status
  response_file="$(mktemp "${STATE_DIR}/auth-response.XXXXXX")" || fail 'could not allocate an auth response file'
  COOKIE_JAR="${STATE_DIR}/cookies.txt"
  http_status="$(curl --silent --show-error --connect-timeout 5 --max-time "${REQUEST_TIMEOUT_SECONDS}" \
    --output "${response_file}" --write-out '%{http_code}' --request POST \
    --cookie-jar "${COOKIE_JAR}" --header "Origin: ${AUTH_ORIGIN}" \
    --header "Authorization: Bearer ${ZHIXU_AUTH_BOOTSTRAP_TOKEN}" \
    "${API_BASE_URL}/api/v1/auth/sessions")" || fail 'authentication request could not reach the API'
  [[ "${http_status}" == 201 ]] || fail "authentication returned HTTP ${http_status}"
  CSRF_TOKEN="$(jq -er '.csrf_token | select(type == "string" and length > 20)' "${response_file}")" || fail 'authentication response omitted csrf_token'
  SESSION_TOKEN="$(awk '$6 == "zhixu_session" { value=$7 } END { print value }' "${COOKIE_JAR}")"
  [[ "${#SESSION_TOKEN}" -eq 43 ]] || fail 'authentication response omitted the session credential'
}

wait_for_vite() {
  local started_at=${SECONDS}
  while (( SECONDS - started_at < 60 )); do
    if curl --fail --silent --show-error --max-time 2 "${VITE_BASE_URL}/" >/dev/null 2>&1; then
      return 0
    fi
    if [[ -n "${VITE_PID}" ]] && ! kill -0 "${VITE_PID}" >/dev/null 2>&1; then
      fail 'Vite exited before becoming ready'
    fi
    sleep 1
  done
  fail 'Vite did not become ready'
}

start_vite() {
  local vite_port=$1
  VITE_BASE_URL="http://127.0.0.1:${vite_port}"
  (
    cd "${REPOSITORY_ROOT}"
    unset VITE_API_BASE_URL
    export VITE_API_PROXY_TARGET="${API_BASE_URL}"
    exec python3 -c 'import os, sys; os.setsid(); os.execvp("npm", ["npm", "run", "dev", "--prefix", "web", "--", "--host", "127.0.0.1", "--port", sys.argv[1]])' "${vite_port}"
  ) >"${STATE_DIR}/vite.log" 2>&1 &
  VITE_PID=$!
  wait_for_vite
}

run_real_provider_browser() {
  local question=$1 evidence_token=$2 source_version_id=$3 source_span_id=$4 result_file="${STATE_DIR}/rag-real-provider-result.json" browser_evidence
  (
    cd "${REPOSITORY_ROOT}"
    ZHIXU_PLAYWRIGHT_BASE_URL="${VITE_BASE_URL}" \
    ZHIXU_RAG_REAL_PROVIDER_SESSION_TOKEN="${SESSION_TOKEN}" \
    ZHIXU_RAG_REAL_PROVIDER_CSRF_TOKEN="${CSRF_TOKEN}" \
    ZHIXU_RAG_REAL_PROVIDER_WORKSPACE_ID="${WORKSPACE_ID}" \
    ZHIXU_RAG_REAL_PROVIDER_SOURCE_VERSION_ID="${source_version_id}" \
    ZHIXU_RAG_REAL_PROVIDER_SOURCE_SPAN_ID="${source_span_id}" \
    ZHIXU_RAG_REAL_PROVIDER_QUESTION="${question}" \
    ZHIXU_RAG_REAL_PROVIDER_EVIDENCE_TOKEN="${evidence_token}" \
    ZHIXU_RAG_REAL_PROVIDER_OUTPUT_FILE="${result_file}" \
    ZHIXU_PLAYWRIGHT_OUTPUT_DIR="${STATE_DIR}/playwright" \
      npm run test:e2e --prefix web -- rag-real-provider.smoke.spec.ts
  ) >"${STATE_DIR}/playwright.log" 2>&1 || fail 'Playwright real Provider RAG smoke failed'
  [[ -f "${result_file}" ]] || fail 'Playwright real Provider RAG smoke omitted its result receipt'
  CURRENT_ANSWER_ID="$(jq -er '.answer_id | select(type == "string")' "${result_file}")" || fail 'Playwright result omitted answer_id'
  [[ "${CURRENT_ANSWER_ID}" =~ ^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$ ]] || fail 'Playwright result returned an invalid answer_id'
  jq -e '
    ((keys | sort) == ([
      "answer_id",
      "citation_opened",
      "completion_latency_ms",
      "desktop_draft_chunk_events",
      "desktop_first_draft_observed_latency_ms",
      "draft_observed",
      "mobile_draft_chunk_events",
      "mobile_first_draft_observed_latency_ms",
      "mobile_verified"
    ] | sort))
    and .draft_observed == true
    and .citation_opened == true
    and .mobile_verified == true
    and (.desktop_draft_chunk_events as $desktop | ($desktop | type) == "number" and $desktop >= 2 and ($desktop | floor) == $desktop)
    and (.mobile_draft_chunk_events as $mobile | ($mobile | type) == "number" and $mobile >= 2 and ($mobile | floor) == $mobile)
    and (.desktop_first_draft_observed_latency_ms as $desktop_first | ($desktop_first | type) == "number" and $desktop_first >= 0 and ($desktop_first | floor) == $desktop_first)
    and (.mobile_first_draft_observed_latency_ms as $mobile_first | ($mobile_first | type) == "number" and $mobile_first >= 0 and ($mobile_first | floor) == $mobile_first)
    and (.desktop_first_draft_observed_latency_ms as $desktop_first
      | .mobile_first_draft_observed_latency_ms as $mobile_first
      | .completion_latency_ms as $complete
      | ($complete | type) == "number" and $complete >= $desktop_first and $complete >= $mobile_first and ($complete | floor) == $complete)
  ' "${result_file}" >/dev/null || fail 'Playwright result omitted bounded desktop/mobile multi-frame or latency evidence'
  browser_evidence="$(jq -er '
    "desktop_chunk_events=\(.desktop_draft_chunk_events) mobile_chunk_events=\(.mobile_draft_chunk_events) desktop_first_draft_ms=\(.desktop_first_draft_observed_latency_ms) mobile_first_draft_ms=\(.mobile_first_draft_observed_latency_ms) completion_ms=\(.completion_latency_ms)"
  ' "${result_file}")" || fail 'Playwright result could not produce a bounded browser evidence summary'
  log "browser evidence: ${browser_evidence}"
}

run_fixed_rag_browser() {
  local question=$1
  (
    cd "${REPOSITORY_ROOT}"
    ZHIXU_PLAYWRIGHT_BASE_URL="${VITE_BASE_URL}" \
    ZHIXU_RAG_FIXTURE_SMOKE_BASE_URL="${VITE_BASE_URL}" \
    ZHIXU_RAG_FIXTURE_SMOKE_SESSION_TOKEN="${SESSION_TOKEN}" \
    ZHIXU_RAG_FIXTURE_SMOKE_CSRF_TOKEN="${CSRF_TOKEN}" \
    ZHIXU_RAG_FIXTURE_SMOKE_WORKSPACE_ID="${WORKSPACE_ID}" \
    ZHIXU_RAG_FIXTURE_SMOKE_QUESTION="${question}" \
    ZHIXU_PLAYWRIGHT_OUTPUT_DIR="${STATE_DIR}/playwright-rag-fixture" \
      npm run test:e2e --prefix web -- rag-fixture.smoke.spec.ts
  ) >"${STATE_DIR}/playwright.log" 2>&1 || fail 'Playwright fixed RAG fixture smoke failed'
}

persist_fixed_rag_browser_artifacts() {
  local destination=${ZHIXU_RAG_FIXTURE_SMOKE_ARTIFACT_DIR:-} source_file copied=0
  [[ -n "${destination}" ]] || return 0
  [[ "${destination}" == /* && "${destination}" != / && ! -e "${destination}" && ! -L "${destination}" ]] || \
    fail 'fixed RAG browser artifact directory must be a new absolute path'
  mkdir -m 0700 -- "${destination}" || fail 'could not create the fixed RAG browser artifact directory'
  while IFS= read -r source_file; do
    cp -- "${source_file}" "${destination}/$(basename -- "${source_file}")" || \
      fail 'could not persist a fixed RAG browser screenshot'
    chmod 0600 "${destination}/$(basename -- "${source_file}")"
    copied=$((copied + 1))
  done < <(find "${STATE_DIR}/playwright-rag-fixture" -type f \
    \( -name 'rag-fixture-desktop.png' -o -name 'rag-fixture-mobile.png' \) -print)
  [[ "${copied}" == 2 ]] || fail 'fixed RAG browser smoke did not produce both bounded screenshots'
  log "fixed RAG browser artifacts: ${destination}"
}

run_workspace_analysis_browser() {
  local question=$1 private_marker=$2 expected_terminal=${3:-completed} started_at=${SECONDS} browser_ready=0 fixture_entered=0
  local fixture_entry_required=1
  [[ "${expected_terminal}" == completed || "${expected_terminal}" == refused || "${expected_terminal}" == cancelled ]] || \
    fail 'Workspace Analysis browser terminal expectation is invalid'
  [[ "${expected_terminal}" == completed ]] || fixture_entry_required=0
  [[ -n "${WORKSPACE_ANALYSIS_BARRIER_READY_FILE}" ]] || fail 'Workspace Analysis browser barrier is unavailable'
  [[ -n "${WORKSPACE_ANALYSIS_BARRIER_ENTERED_FILE}" ]] || fail 'Workspace Analysis fixture barrier is unavailable'
  [[ -n "${WORKSPACE_ANALYSIS_BARRIER_TOKEN}" ]] || fail 'Workspace Analysis fixture barrier generation is unavailable'
  rm -f -- "${WORKSPACE_ANALYSIS_BARRIER_READY_FILE}"
  (
    cd "${REPOSITORY_ROOT}"
    export ZHIXU_PLAYWRIGHT_BASE_URL="${VITE_BASE_URL}" \
    ZHIXU_WORKSPACE_ANALYSIS_SMOKE_BASE_URL="${VITE_BASE_URL}" \
    ZHIXU_WORKSPACE_ANALYSIS_SMOKE_SESSION_TOKEN="${SESSION_TOKEN}" \
    ZHIXU_WORKSPACE_ANALYSIS_SMOKE_CSRF_TOKEN="${CSRF_TOKEN}" \
    ZHIXU_WORKSPACE_ANALYSIS_SMOKE_WORKSPACE_ID="${WORKSPACE_ID}" \
    ZHIXU_WORKSPACE_ANALYSIS_SMOKE_QUESTION="${question}" \
    ZHIXU_WORKSPACE_ANALYSIS_SMOKE_PRIVATE_MARKER="${private_marker}" \
    ZHIXU_WORKSPACE_ANALYSIS_SMOKE_BARRIER_READY_FILE="${WORKSPACE_ANALYSIS_BARRIER_READY_FILE}" \
    ZHIXU_WORKSPACE_ANALYSIS_SMOKE_PROGRESS_FILE="${WORKSPACE_ANALYSIS_BROWSER_PROGRESS_FILE}" \
    ZHIXU_WORKSPACE_ANALYSIS_SMOKE_EXPECTED_TERMINAL="${expected_terminal}" \
    ZHIXU_PLAYWRIGHT_OUTPUT_DIR="${STATE_DIR}/playwright-workspace-analysis-${expected_terminal}"
    exec python3 -c 'import os; os.setsid(); os.execvp("npm", ["npm", "run", "test:e2e", "--prefix", "web", "--", "workspace-analysis.smoke.spec.ts"])'
  ) >"${STATE_DIR}/playwright.log" 2>&1 &
  PLAYWRIGHT_PID=$!

  while (( SECONDS - started_at < 60 )); do
    if [[ -f "${WORKSPACE_ANALYSIS_BARRIER_READY_FILE}" ]]; then
      browser_ready=1
    fi
    if workspace_analysis_fixture_barrier_entered; then
      fixture_entered=1
    fi
    if [[ "${browser_ready}" == 1 && ( "${fixture_entry_required}" == 0 || "${fixture_entered}" == 1 ) ]]; then
      break
    fi
    if ! kill -0 "${PLAYWRIGHT_PID}" >/dev/null 2>&1; then
      wait "${PLAYWRIGHT_PID}" >/dev/null 2>&1 || true
      PLAYWRIGHT_PID=""
      fail 'Playwright Workspace Analysis smoke exited before observing the pending state'
    fi
    sleep 1
  done
  if [[ "${browser_ready}" != 1 || ( "${fixture_entry_required}" == 1 && "${fixture_entered}" != 1 ) ]]; then
    stop_playwright
    fail 'Workspace Analysis browser did not observe the required pending-state barrier'
  fi
  release_workspace_analysis_fixture_barrier
  if ! wait "${PLAYWRIGHT_PID}"; then
    PLAYWRIGHT_PID=""
    fail 'Playwright Workspace Analysis smoke failed'
  fi
  PLAYWRIGHT_PID=""
  [[ "${expected_terminal}" != completed ]] || persist_workspace_analysis_browser_artifacts
}

persist_workspace_analysis_browser_artifacts() {
  local destination=${ZHIXU_WORKSPACE_ANALYSIS_SMOKE_ARTIFACT_DIR:-} source_file copied=0
  [[ -n "${destination}" ]] || return 0
  [[ "${destination}" == /* && "${destination}" != / && ! -e "${destination}" && ! -L "${destination}" ]] || \
    fail 'Workspace Analysis browser artifact directory must be a new absolute path'
  mkdir -m 0700 -- "${destination}" || fail 'could not create the Workspace Analysis browser artifact directory'
  while IFS= read -r source_file; do
    cp -- "${source_file}" "${destination}/$(basename -- "${source_file}")" || \
      fail 'could not persist a Workspace Analysis browser screenshot'
    chmod 0600 "${destination}/$(basename -- "${source_file}")"
    copied=$((copied + 1))
  done < <(find "${STATE_DIR}/playwright-workspace-analysis-completed" -type f \
    \( -name 'workspace-analysis-desktop.png' -o -name 'workspace-analysis-mobile.png' \) -print)
  [[ "${copied}" == 2 ]] || fail 'Workspace Analysis browser smoke did not produce both bounded screenshots'
  log "browser artifacts: ${destination}"
}

release_workspace_analysis_fixture_barrier() {
  compose exec -T --user 10001:10001 rag-model-fixture sh -ec '
    token="$(cat "$1")"
    [ "${token}" = "$3" ]
    temporary="$2.$$.tmp"
    umask 077
    printf "%s\\n" "${token}" >"${temporary}"
    mv -f -- "${temporary}" "$2"
    [ -f "$2" ] && [ "$(cat "$2")" = "${token}" ]
  ' sh "${WORKSPACE_ANALYSIS_BARRIER_CONTAINER_ARMED_FILE}" \
    "${WORKSPACE_ANALYSIS_BARRIER_CONTAINER_RELEASE_FILE}" "${WORKSPACE_ANALYSIS_BARRIER_TOKEN}" >/dev/null || \
    fail 'could not release the Workspace Analysis model fixture barrier'
}

arm_workspace_analysis_fixture_barrier() {
  local token
  if [[ -n "${WORKSPACE_ANALYSIS_BARRIER_TOKEN}" ]] && workspace_analysis_fixture_barrier_entered; then
    wait_for_workspace_analysis_fixture_barrier_settled "${WORKSPACE_ANALYSIS_BARRIER_TOKEN}"
  fi
  token="$(random_hex 16)" || fail 'could not generate the Workspace Analysis fixture barrier generation'
  [[ "${token}" =~ ^[0-9a-f]{32}$ ]] || fail 'Workspace Analysis fixture barrier generation is invalid'
  WORKSPACE_ANALYSIS_BARRIER_TOKEN=""
  compose exec -T --user 10001:10001 rag-model-fixture sh -ec '
    actual_stage="$(tr "\\000" "\\n" </proc/1/environ | sed -n "s/^ZHIXU_RAG_FIXTURE_BARRIER_STAGE=//p")"
    actual_release="$(tr "\\000" "\\n" </proc/1/environ | sed -n "s/^ZHIXU_RAG_FIXTURE_BARRIER_RELEASE_FILE=//p")"
    actual_entered="$(tr "\\000" "\\n" </proc/1/environ | sed -n "s/^ZHIXU_RAG_FIXTURE_BARRIER_ENTERED_FILE=//p")"
    actual_settled="$(tr "\\000" "\\n" </proc/1/environ | sed -n "s/^ZHIXU_RAG_FIXTURE_BARRIER_SETTLED_FILE=//p")"
    actual_armed="$(tr "\\000" "\\n" </proc/1/environ | sed -n "s/^ZHIXU_RAG_FIXTURE_BARRIER_ARMED_FILE=//p")"
    actual_lock="$(tr "\\000" "\\n" </proc/1/environ | sed -n "s/^ZHIXU_RAG_FIXTURE_BARRIER_LOCK_DIR=//p")"
    [ "${actual_stage}" = "$2" ] && [ "${actual_release}" = "$3" ] && [ "${actual_entered}" = "$4" ] && \
      [ "${actual_settled}" = "$5" ] && [ "${actual_armed}" = "$6" ] && [ "${actual_lock}" = "$7" ]
    attempt=0
    umask 077
    while ! mkdir "$7" 2>/dev/null; do
      [ -d "$7" ] || exit 1
      attempt=$((attempt + 1))
      [ "${attempt}" -lt "$8" ] || exit 1
      sleep 1
    done
    trap "rmdir -- \"\$7\"" 0
    if [ -f "$4" ]; then
      previous_token="$(cat "$6")"
      [ "$(cat "$4")" = "${previous_token}" ] && [ -f "$5" ] && [ "$(cat "$5")" = "${previous_token}" ]
    fi
    rm -f -- "$3" "$4" "$5" "$6"
    temporary="$6.$$.tmp"
    printf "%s\\n" "$1" >"${temporary}"
    mv -f -- "${temporary}" "$6"
    [ -f "$6" ] && [ "$(cat "$6")" = "$1" ] && [ ! -e "$3" ] && [ ! -e "$4" ] && [ ! -e "$5" ]
  ' sh "${token}" workspace_analysis_candidate_stream \
    "${WORKSPACE_ANALYSIS_BARRIER_CONTAINER_RELEASE_FILE}" \
    "${WORKSPACE_ANALYSIS_BARRIER_CONTAINER_ENTERED_FILE}" \
    "${WORKSPACE_ANALYSIS_BARRIER_CONTAINER_SETTLED_FILE}" \
    "${WORKSPACE_ANALYSIS_BARRIER_CONTAINER_ARMED_FILE}" \
    "${WORKSPACE_ANALYSIS_BARRIER_CONTAINER_LOCK_DIR}" "${TIMEOUT_SECONDS}" >/dev/null || \
    fail 'could not arm the Workspace Analysis model fixture barrier'
  WORKSPACE_ANALYSIS_BARRIER_TOKEN="${token}"
}

workspace_analysis_fixture_barrier_entered() {
  compose exec -T --user 10001:10001 rag-model-fixture sh -ec '
    [ -f "$1" ] && [ -f "$2" ] && [ "$(cat "$1")" = "$3" ] && [ "$(cat "$2")" = "$3" ]
  ' sh "${WORKSPACE_ANALYSIS_BARRIER_CONTAINER_ENTERED_FILE}" \
    "${WORKSPACE_ANALYSIS_BARRIER_CONTAINER_ARMED_FILE}" "${WORKSPACE_ANALYSIS_BARRIER_TOKEN}" >/dev/null 2>&1
}

workspace_analysis_fixture_barrier_settled() {
  local token=${1:-${WORKSPACE_ANALYSIS_BARRIER_TOKEN}}
  compose exec -T --user 10001:10001 rag-model-fixture sh -ec '
    [ -f "$1" ] && [ -f "$2" ] && [ "$(cat "$1")" = "$3" ] && [ "$(cat "$2")" = "$3" ]
  ' sh "${WORKSPACE_ANALYSIS_BARRIER_CONTAINER_SETTLED_FILE}" \
    "${WORKSPACE_ANALYSIS_BARRIER_CONTAINER_ARMED_FILE}" "${token}" >/dev/null 2>&1
}

wait_for_workspace_analysis_fixture_barrier_settled() {
  local token=$1 started_at=${SECONDS}
  while (( SECONDS - started_at < 30 )); do
    if workspace_analysis_fixture_barrier_settled "${token}"; then
      return 0
    fi
    sleep 1
  done
  fail 'Workspace Analysis fixture barrier did not settle the current generation'
}

wait_for_workspace_analysis_capability() {
  local started_at=${SECONDS} ready
  while (( SECONDS - started_at < TIMEOUT_SECONDS )); do
    ready="$(compose exec -T postgres psql -Atq --username "${ZHIXU_POSTGRES_USER}" --dbname "${ZHIXU_POSTGRES_DB}" -c \
      "SELECT count(*) FROM agent.workspace_analysis_worker_capability WHERE released_at IS NULL AND lease_until>clock_timestamp() AND config_revision=1 AND definition_version=2 AND policy_version=2")" || \
      fail 'could not read Workspace Analysis capability'
    [[ "${ready}" =~ ^[1-9][0-9]*$ ]] && return 0
    sleep "${POLL_INTERVAL_SECONDS}"
  done
  fail 'Workspace Analysis worker did not advertise a fresh capability'
}

wait_for_workspace_analysis_fixture_barrier_entered() {
  local started_at=${SECONDS}
  while (( SECONDS - started_at < TIMEOUT_SECONDS )); do
    if workspace_analysis_fixture_barrier_entered; then
      return 0
    fi
    sleep 1
  done
  fail 'Workspace Analysis restart smoke did not enter the candidate barrier'
}

workspace_analysis_restart_inflight_projection() {
  local answer_id=$1
  compose exec -T postgres psql -Atq --username "${ZHIXU_POSTGRES_USER}" --dbname "${ZHIXU_POSTGRES_DB}" \
    --set answer_id="${answer_id}" <<'SQL'
SELECT concat_ws('|',operation.status,model_call.status,reservation.status,attempt.status,job.state,
       attempt.river_job_attempt,job.attempt)
FROM agent.workspace_analysis_run analysis
JOIN agent.workspace_analysis_operation operation
  ON operation.analysis_run_id=analysis.id AND operation.node_key='synthesize_answer'
 AND operation.operation_kind='ANSWER_SYNTHESIS' AND operation.ordinal=1
JOIN agent.model_call model_call ON model_call.id=operation.model_call_id
JOIN agent.workspace_analysis_budget_reservation reservation ON reservation.operation_id=operation.id
JOIN workflow.node_attempt attempt ON attempt.id=operation.latest_node_attempt_id
JOIN workflow.river_job job ON job.id=attempt.river_job_id
WHERE analysis.answer_id=:'answer_id';
SQL
}

wait_for_workspace_analysis_restart_lease_expiry() {
  local answer_id=$1 started_at=${SECONDS} expired
  while (( SECONDS - started_at < 30 )); do
    expired="$(compose exec -T postgres psql -Atq --username "${ZHIXU_POSTGRES_USER}" --dbname "${ZHIXU_POSTGRES_DB}" \
      --set answer_id="${answer_id}" <<'SQL'
SELECT count(*)
FROM agent.workspace_analysis_run analysis
JOIN agent.workspace_analysis_operation operation
  ON operation.analysis_run_id=analysis.id AND operation.node_key='synthesize_answer'
JOIN workflow.node_attempt attempt ON attempt.id=operation.latest_node_attempt_id
WHERE analysis.answer_id=:'answer_id'
  AND attempt.status='running'
  AND attempt.lease_until<=clock_timestamp();
SQL
)" || fail 'could not read the killed Workspace Analysis lease'
    [[ "${expired}" == 1 ]] && return 0
    sleep 1
  done
  fail 'killed Workspace Analysis Attempt lease did not expire'
}

age_workspace_analysis_river_job_for_restart_smoke() {
  local answer_id=$1 rescued_job_id
  # This only accelerates the disposable clock horizon. River still owns the
  # running -> retryable rescue transition and the replacement delivery.
  rescued_job_id="$(compose exec -T postgres psql -Atq --username "${ZHIXU_POSTGRES_USER}" --dbname "${ZHIXU_POSTGRES_DB}" \
    --set answer_id="${answer_id}" <<'SQL'
WITH target AS MATERIALIZED (
  SELECT attempt.river_job_id
  FROM agent.workspace_analysis_run analysis
  JOIN agent.workspace_analysis_operation operation
    ON operation.analysis_run_id=analysis.id AND operation.node_key='synthesize_answer'
   AND operation.operation_kind='ANSWER_SYNTHESIS' AND operation.ordinal=1
  JOIN workflow.node_attempt attempt ON attempt.id=operation.latest_node_attempt_id
  WHERE analysis.answer_id=:'answer_id'
    AND attempt.status='running'
    AND attempt.lease_until<=clock_timestamp()
    AND attempt.river_job_id IS NOT NULL
)
UPDATE workflow.river_job job
SET attempted_at=clock_timestamp()-interval '31 minutes'
FROM target
WHERE job.id=target.river_job_id AND job.state='running'
RETURNING job.id;
SQL
)" || fail 'could not age the exact killed River Job for rescue'
  [[ "${rescued_job_id}" =~ ^[1-9][0-9]*$ ]] || fail 'restart fault injection did not bind exactly one running River Job'
}

kill_workspace_analysis_worker() {
  local worker_container_id worker_pid worker_state started_at=${SECONDS}
  worker_container_id="$(compose ps -q worker)"
  [[ "${worker_container_id}" =~ ^[0-9a-f]{64}$ ]] || fail 'Workspace Analysis Worker container identity is unavailable'
  worker_pid="$(docker inspect --format '{{.State.Pid}}' "${worker_container_id}")" || fail 'could not read the Workspace Analysis Worker process identity'
  [[ "${worker_pid}" =~ ^[1-9][0-9]*$ ]] || fail 'Workspace Analysis Worker process is not running'
  compose kill --signal SIGKILL worker >/dev/null || fail 'could not SIGKILL the Workspace Analysis Worker'
  while (( SECONDS - started_at < 15 )); do
    worker_state="$(docker inspect --format '{{.State.Status}}|{{.RestartCount}}' "${worker_container_id}" 2>/dev/null || true)"
    case "${worker_state}" in
      exited\|0) printf '%s|%s\n' "${worker_container_id}" "${worker_pid}"; return 0 ;;
      *\|[1-9]*) fail 'Workspace Analysis Worker restarted automatically after SIGKILL' ;;
    esac
    sleep 1
  done
  fail 'Workspace Analysis Worker did not remain stopped after SIGKILL'
}

start_workspace_analysis_replacement_worker() {
  local killed_container_id=${1%%|*} killed_pid=${1##*|} replacement_container_id replacement_pid
  compose up --detach --no-deps --wait worker >/dev/null || fail 'could not start the replacement Workspace Analysis Worker'
  replacement_container_id="$(compose ps -q worker)"
  replacement_pid="$(docker inspect --format '{{.State.Pid}}' "${replacement_container_id}")" || fail 'could not read the replacement Worker process identity'
  [[ "${replacement_container_id}" =~ ^[0-9a-f]{64}$ && "${replacement_pid}" =~ ^[1-9][0-9]*$ ]] || \
    fail 'replacement Workspace Analysis Worker is not running'
  [[ "${replacement_container_id}" != "${killed_container_id}" || "${replacement_pid}" != "${killed_pid}" ]] || \
    fail 'Workspace Analysis Worker process identity did not change after manual restart'
}

wait_for_workspace_analysis_restart_terminal() {
  local answer_id=$1 started_at=${SECONDS} publication workflow
  while (( SECONDS - started_at < TIMEOUT_SECONDS )); do
    if workspace_analysis_fixture_barrier_entered; then
      fail 'replacement Workspace Analysis Attempt duplicated the interrupted model call'
    fi
    request_json GET "/api/v2/answers/${answer_id}?workspace_id=${WORKSPACE_ID}" 200 '' 'Workspace Analysis restart answer status'
    publication="$(jq -r '.publication_status' "${LAST_RESPONSE_FILE}")"
    workflow="$(jq -r '.workflow.status' "${LAST_RESPONSE_FILE}")"
    if [[ "${publication}:${workflow}" == 'failed:failed' ]]; then
      cp "${LAST_RESPONSE_FILE}" "${STATE_DIR}/restart-answer.json"
      return 0
    fi
    case "${publication}" in
      completed|refused|clarification_required|cancelled) fail "Workspace Analysis restart reached unexpected terminal state ${publication}/${workflow}" ;;
    esac
    sleep "${POLL_INTERVAL_SECONDS}"
  done
  fail 'Workspace Analysis Worker restart recovery timed out'
}

workspace_analysis_restart_database_projection() {
  local answer_id=$1
  compose exec -T postgres psql -Atq --username "${ZHIXU_POSTGRES_USER}" --dbname "${ZHIXU_POSTGRES_DB}" \
    --set answer_id="${answer_id}" <<'SQL'
WITH target AS (
  SELECT analysis.*,answer.publication_status,answer.result_type,answer.result,workflow_run.status AS workflow_status
  FROM agent.workspace_analysis_run analysis
  JOIN agent.answer answer ON answer.id=analysis.answer_id AND answer.workspace_id=analysis.workspace_id
  JOIN workflow.run workflow_run ON workflow_run.id=analysis.workflow_run_id
  WHERE analysis.answer_id=:'answer_id'
)
SELECT concat_ws('|',
  target.status,target.termination_reason,target.publication_status,target.result_type,target.workflow_status,
  target.result->'payload'->>'termination_reason',
  (SELECT count(*) FROM agent.workspace_analysis_operation operation WHERE operation.analysis_run_id=target.id),
  (SELECT count(DISTINCT (operation.node_key,operation.operation_kind,operation.ordinal)) FROM agent.workspace_analysis_operation operation WHERE operation.analysis_run_id=target.id),
  (SELECT count(*) FROM agent.workspace_analysis_operation operation WHERE operation.analysis_run_id=target.id AND operation.call_kind='MODEL'),
  (SELECT count(*) FROM agent.workspace_analysis_operation operation JOIN agent.model_call model_call ON model_call.id=operation.model_call_id WHERE operation.analysis_run_id=target.id),
  (SELECT count(DISTINCT model_call.model_run_id) FROM agent.workspace_analysis_operation operation JOIN agent.model_call model_call ON model_call.id=operation.model_call_id WHERE operation.analysis_run_id=target.id),
  (SELECT count(*) FROM agent.workspace_analysis_operation operation WHERE operation.analysis_run_id=target.id AND operation.call_kind='TOOL'),
  (SELECT count(*) FROM agent.workspace_analysis_operation operation JOIN workflow.tool_call tool_call ON tool_call.id=operation.tool_call_id WHERE operation.analysis_run_id=target.id),
  (SELECT count(*) FROM agent.workspace_analysis_operation operation JOIN workflow.tool_result_receipt receipt ON receipt.id=operation.result_id WHERE operation.analysis_run_id=target.id AND operation.result_kind='TOOL_RESULT_RECEIPT'),
  (SELECT count(*) FROM agent.workspace_analysis_budget_reservation reservation WHERE reservation.analysis_run_id=target.id),
  (SELECT count(*) FROM agent.workspace_analysis_budget_reservation reservation WHERE reservation.analysis_run_id=target.id AND reservation.status='RESERVED'),
  (SELECT count(*) FROM agent.workspace_analysis_operation operation WHERE operation.analysis_run_id=target.id AND operation.status='UNKNOWN'),
  (SELECT count(*) FROM agent.workspace_analysis_operation operation JOIN agent.model_call model_call ON model_call.id=operation.model_call_id WHERE operation.analysis_run_id=target.id AND model_call.status='UNKNOWN'),
  (SELECT count(*) FROM agent.workspace_analysis_operation operation JOIN agent.model_call model_call ON model_call.id=operation.model_call_id JOIN agent.model_run model_run ON model_run.id=model_call.model_run_id WHERE operation.analysis_run_id=target.id AND model_run.status='UNKNOWN'),
  (SELECT count(*) FROM agent.workspace_analysis_budget_reservation reservation
    WHERE reservation.analysis_run_id=target.id AND reservation.status='UNKNOWN_CHARGED'
      AND reservation.settled_model_calls=reservation.reserved_model_calls
      AND reservation.settled_tool_calls=reservation.reserved_tool_calls
      AND reservation.settled_source_reads=reservation.reserved_source_reads
      AND reservation.settled_input_tokens=reservation.reserved_input_tokens
      AND reservation.settled_output_tokens=reservation.reserved_output_tokens
      AND reservation.settled_cost_microunits IS NOT DISTINCT FROM reservation.reserved_cost_microunits),
  (SELECT count(*) FROM agent.workspace_analysis_candidate candidate WHERE candidate.analysis_run_id=target.id),
  (SELECT count(*) FROM agent.workspace_analysis_publication_proof proof WHERE proof.analysis_run_id=target.id),
  (SELECT count(*) FROM agent.workspace_analysis_termination_proof proof WHERE proof.analysis_run_id=target.id),
  (SELECT count(*) FROM agent.workspace_analysis_termination_proof proof
    JOIN agent.workspace_analysis_operation operation
      ON operation.id=proof.operation_id AND operation.analysis_run_id=proof.analysis_run_id
    WHERE proof.analysis_run_id=target.id AND proof.reason='WORKSPACE_ANALYSIS_RESULT_UNKNOWN'
      AND operation.node_key='synthesize_answer'
      AND proof.terminal_node_attempt_id=operation.latest_node_attempt_id),
  (SELECT count(*) FROM agent.workspace_analysis_operation operation WHERE operation.analysis_run_id=target.id AND operation.operation_kind='SOURCE_READ'),
  (SELECT count(*) FROM workflow.node_attempt attempt JOIN workflow.node_run node ON node.id=attempt.node_run_id WHERE node.run_id=target.workflow_run_id AND node.node_key='synthesize_answer'),
  (SELECT count(*) FROM workflow.node_attempt attempt JOIN workflow.node_run node ON node.id=attempt.node_run_id WHERE node.run_id=target.workflow_run_id AND node.node_key='synthesize_answer' AND attempt.status='lease_lost'),
  (SELECT count(*) FROM workflow.node_attempt attempt JOIN workflow.node_run node ON node.id=attempt.node_run_id WHERE node.run_id=target.workflow_run_id AND node.node_key='synthesize_answer' AND attempt.status='manual_recovery'),
  (SELECT count(*) FROM workflow.node_attempt attempt JOIN workflow.node_run node ON node.id=attempt.node_run_id WHERE node.run_id=target.workflow_run_id AND node.node_key='synthesize_answer' AND attempt.river_job_attempt>=2),
  (SELECT count(DISTINCT attempt.river_job_id) FROM workflow.node_attempt attempt JOIN workflow.node_run node ON node.id=attempt.node_run_id WHERE node.run_id=target.workflow_run_id AND node.node_key='synthesize_answer'),
  (SELECT count(*) FROM agent.workspace_analysis_operation operation WHERE operation.analysis_run_id=target.id AND operation.node_key='synthesize_answer' AND operation.first_node_attempt_id<>operation.latest_node_attempt_id),
  target.reserved_model_calls,target.reserved_tool_calls,target.reserved_source_reads,target.reserved_input_tokens,target.reserved_output_tokens,
  target.settled_model_calls,target.settled_tool_calls,target.settled_source_reads,
  CASE WHEN (
    SELECT ROW(COALESCE(sum(reservation.settled_model_calls),0)::bigint,COALESCE(sum(reservation.settled_tool_calls),0)::bigint,
               COALESCE(sum(reservation.settled_source_reads),0)::bigint,COALESCE(sum(reservation.settled_input_tokens),0)::bigint,
               COALESCE(sum(reservation.settled_output_tokens),0)::bigint)
    FROM agent.workspace_analysis_budget_reservation reservation WHERE reservation.analysis_run_id=target.id
  )=ROW(target.settled_model_calls::bigint,target.settled_tool_calls::bigint,target.settled_source_reads::bigint,target.settled_input_tokens::bigint,target.settled_output_tokens::bigint) THEN 1 ELSE 0 END,
  (SELECT COALESCE(max((job.metadata->>'river:rescue_count')::integer),0) FROM workflow.node_attempt attempt JOIN workflow.node_run node ON node.id=attempt.node_run_id JOIN workflow.river_job job ON job.id=attempt.river_job_id WHERE node.run_id=target.workflow_run_id AND node.node_key='synthesize_answer'),
  (SELECT COALESCE(max(job.attempt),0) FROM workflow.node_attempt attempt JOIN workflow.node_run node ON node.id=attempt.node_run_id JOIN workflow.river_job job ON job.id=attempt.river_job_id WHERE node.run_id=target.workflow_run_id AND node.node_key='synthesize_answer')
)
FROM target;
SQL
}

workspace_analysis_candidate_fixture_request_count() {
  compose logs --no-color rag-model-fixture 2>/dev/null | \
    awk 'index($0,"rag model fixture request stage=workspace_analysis_candidate_stream") { count++ } END { print count+0 }'
}

run_workspace_analysis_worker_restart_smoke() {
  local analysis_question=$1 run_id=$2 conversation_id answer_id payload inflight killed_identity old_barrier_token projection projection_delimiters
  local candidate_requests_before candidate_requests_after
  local run_status termination publication result_type workflow_status answer_reason operation_count logical_count
  local model_operations model_calls model_runs tool_operations tool_calls receipts reservations open_reservations
  local unknown_operations unknown_model_calls unknown_model_runs unknown_charged candidates publication_proofs termination_proofs result_unknown_termination_proofs source_reads attempts lease_lost manual_recovery
  local rescued_attempts river_jobs attempt_rebound reserved_models reserved_tools reserved_sources reserved_input reserved_output
  local settled_models settled_tools settled_sources budget_matches river_rescues river_attempt

  candidate_requests_before="$(workspace_analysis_candidate_fixture_request_count)" || fail 'could not capture the candidate fixture request baseline'
  [[ "${candidate_requests_before}" =~ ^[0-9]+$ ]] || fail 'candidate fixture request baseline is invalid'
  arm_workspace_analysis_fixture_barrier
  request_json POST /api/v1/conversations 201 "$(jq -cn --arg workspace "${WORKSPACE_ID}" '{workspace_id:$workspace,title:"Compose Workspace Analysis Worker restart smoke"}')" \
    'Workspace Analysis restart conversation creation' "workspace-analysis-restart-conversation-${run_id}"
  conversation_id="$(jq -er '.id' "${LAST_RESPONSE_FILE}")"
  payload="$(jq -cn --arg workspace "${WORKSPACE_ID}" --arg question "${analysis_question}" '{workspace_id:$workspace,mode:"workspace_analysis",question:$question,scope:{retrieval_mode:"keyword"},answer_depth:"standard",output_format:"markdown"}')"
  request_json POST "/api/v2/conversations/${conversation_id}/questions" 202 "${payload}" \
    'Workspace Analysis restart question submission' "workspace-analysis-restart-question-${run_id}"
  answer_id="$(jq -er '.answer.id' "${LAST_RESPONSE_FILE}")"
  CURRENT_ANSWER_ID="${answer_id}"
  wait_for_workspace_analysis_fixture_barrier_entered
  inflight="$(workspace_analysis_restart_inflight_projection "${answer_id}")" || fail 'could not read the interrupted Workspace Analysis model operation'
  [[ "${inflight}" == 'STARTED|STARTED|RESERVED|running|running|1|1' ]] || \
    fail "Workspace Analysis candidate call was not durably in flight (${inflight})"

  old_barrier_token="${WORKSPACE_ANALYSIS_BARRIER_TOKEN}"
  killed_identity="$(kill_workspace_analysis_worker)"
  wait_for_workspace_analysis_fixture_barrier_settled "${old_barrier_token}"
  wait_for_workspace_analysis_restart_lease_expiry "${answer_id}"
  age_workspace_analysis_river_job_for_restart_smoke "${answer_id}"
  arm_workspace_analysis_fixture_barrier
  start_workspace_analysis_replacement_worker "${killed_identity}"
  wait_for_workspace_analysis_restart_terminal "${answer_id}"
  jq -e '
    .publication_status=="failed" and .result_type=="workspace_analysis_termination" and .workflow.status=="failed"
    and .result.payload.termination_reason=="WORKSPACE_ANALYSIS_RESULT_UNKNOWN"
  ' "${STATE_DIR}/restart-answer.json" >/dev/null || fail 'Workspace Analysis restart Answer omitted the RESULT_UNKNOWN terminal fact'

  projection="$(workspace_analysis_restart_database_projection "${answer_id}")" || fail 'could not read the Workspace Analysis restart database projection'
  projection_delimiters="${projection//[^|]/}"
  [[ "${#projection_delimiters}" == 41 ]] || fail 'Workspace Analysis restart database projection did not return exactly 42 fields'
  IFS='|' read -r run_status termination publication result_type workflow_status answer_reason operation_count logical_count \
    model_operations model_calls model_runs tool_operations tool_calls receipts reservations open_reservations \
    unknown_operations unknown_model_calls unknown_model_runs unknown_charged candidates publication_proofs termination_proofs result_unknown_termination_proofs source_reads attempts lease_lost manual_recovery \
    rescued_attempts river_jobs attempt_rebound reserved_models reserved_tools reserved_sources reserved_input reserved_output \
    settled_models settled_tools settled_sources budget_matches river_rescues river_attempt <<<"${projection}"
  [[ "${run_status}|${termination}|${publication}|${result_type}|${workflow_status}|${answer_reason}" == \
    'failed|WORKSPACE_ANALYSIS_RESULT_UNKNOWN|failed|workspace_analysis_termination|failed|WORKSPACE_ANALYSIS_RESULT_UNKNOWN' ]] || \
    fail 'Workspace Analysis restart did not converge to the authoritative RESULT_UNKNOWN terminal state'
  [[ "${source_reads}" == 1 && "${model_operations}" == 6 && "${tool_operations}" == 4 && \
    "${operation_count}" -eq $((model_operations + tool_operations)) && "${logical_count}" == "${operation_count}" ]] || \
    fail 'Workspace Analysis restart logical operation projection is inconsistent'
  [[ "${model_calls}" == "${model_operations}" && "${model_runs}" == 2 && \
    "${tool_calls}" == "${tool_operations}" && "${receipts}" == "${tool_operations}" && "${reservations}" == "${operation_count}" ]] || \
    fail 'Workspace Analysis restart duplicated a Call, receipt, Model Run, or reservation'
  [[ "${open_reservations}" == 0 && "${unknown_operations}" == 1 && "${unknown_model_calls}" == 1 && \
    "${unknown_model_runs}" == 1 && "${unknown_charged}" == 1 && "${candidates}" == 0 && \
    "${publication_proofs}" == 0 && "${termination_proofs}" == 1 && "${result_unknown_termination_proofs}" == 1 ]] || \
    fail 'Workspace Analysis restart did not close the interrupted model operation exactly once'
  [[ "${attempts}" == 2 && "${lease_lost}" == 1 && "${manual_recovery}" == 1 && "${rescued_attempts}" == 1 && \
    "${river_jobs}" == 1 && "${attempt_rebound}" == 1 && "${river_rescues}" -ge 1 && "${river_attempt}" -ge 2 ]] || \
    fail 'Workspace Analysis restart omitted River rescue, lease_lost, or replacement Attempt evidence'
  [[ "${reserved_models}|${reserved_tools}|${reserved_sources}|${reserved_input}|${reserved_output}" == '0|0|0|0|0' && \
    "${settled_models}" == 6 && "${settled_tools}" == "${tool_operations}" && "${settled_sources}" == "${source_reads}" && "${budget_matches}" == 1 ]] || \
    fail 'Workspace Analysis restart duplicated or stranded budget accounting'
  candidate_requests_after="$(workspace_analysis_candidate_fixture_request_count)" || fail 'could not read the candidate fixture request count after restart'
  [[ "${candidate_requests_after}" -eq $((candidate_requests_before + 1)) ]] || \
    fail 'Workspace Analysis restart invoked the candidate Provider more than once'
  if workspace_analysis_fixture_barrier_entered; then
    fail 'replacement Workspace Analysis generation entered the candidate Provider barrier'
  fi
  release_workspace_analysis_fixture_barrier
  log 'passed: real Worker SIGKILL, generation settlement, River rescue, lease_lost replacement, RESULT_UNKNOWN, and exact-once durable facts'
}

wait_for_proposal() {
  local proposal_id=$1 workflow_path=$2 started_at=${SECONDS} proposal_status workflow_status
  while (( SECONDS - started_at < TIMEOUT_SECONDS )); do
    request_json GET "/api/v1/proposals/${proposal_id}" 200 '' 'proposal status'
    proposal_status="$(jq -r '.status' "${LAST_RESPONSE_FILE}")"
    request_json GET "${workflow_path}" 200 '' 'reindex workflow status'
    workflow_status="$(jq -r '.status' "${LAST_RESPONSE_FILE}")"
    [[ "${proposal_status}:${workflow_status}" == 'completed:succeeded' ]] && return 0
    case "${proposal_status}:${workflow_status}" in
      verify_failed:*|rolled_back:*|*:failed|*:cancelled) fail 'approved writeback/reindex reached terminal failure' ;;
    esac
    sleep "${POLL_INTERVAL_SECONDS}"
  done
  fail 'approved writeback/reindex timed out'
}

wait_for_answer() {
  local answer_id=$1 started_at=${SECONDS} publication workflow
  while (( SECONDS - started_at < TIMEOUT_SECONDS )); do
    request_json GET "/api/v2/answers/${answer_id}?workspace_id=${WORKSPACE_ID}" 200 '' 'answer status'
    publication="$(jq -r '.publication_status' "${LAST_RESPONSE_FILE}")"
    workflow="$(jq -r '.workflow.status' "${LAST_RESPONSE_FILE}")"
    if [[ "${publication}:${workflow}" == 'completed:succeeded' ]]; then
      cp "${LAST_RESPONSE_FILE}" "${STATE_DIR}/completed-answer.json"
      return 0
    fi
    case "${publication}:${workflow}" in
      refused:*|clarification_required:*|*:failed|*:cancelled) fail "RAG answer reached terminal state ${publication}/${workflow}" ;;
    esac
    sleep "${POLL_INTERVAL_SECONDS}"
  done
  fail 'RAG answer timed out'
}

model_call_projection() {
  compose exec -T postgres psql -Atq --username "${ZHIXU_POSTGRES_USER}" --dbname "${ZHIXU_POSTGRES_DB}" \
    --set answer_id="${CURRENT_ANSWER_ID}" <<'SQL'
SELECT count(*)::text||'|'||COALESCE(string_agg(c.phase||':'||c.status,',' ORDER BY c.call_no),'')
FROM agent.model_call c
JOIN agent.model_run m ON m.id=c.model_run_id
JOIN agent.answer a ON a.workflow_run_id=m.workflow_run_id
WHERE a.id=:'answer_id';
SQL
}

tool_call_projection() {
  compose exec -T postgres psql -Atq --username "${ZHIXU_POSTGRES_USER}" --dbname "${ZHIXU_POSTGRES_DB}" \
    --set answer_id="${CURRENT_ANSWER_ID}" <<'SQL'
SELECT count(*)::text||'|'||COALESCE(string_agg(c.requested_tool_name||'@'||c.tool_version::text||':'||c.status,',' ORDER BY c.call_no),'')
FROM workflow.tool_call c
JOIN agent.answer a ON a.workflow_run_id=c.workflow_run_id
WHERE a.id=:'answer_id';
SQL
}

unexpected_tool_call_count() {
  compose exec -T postgres psql -Atq --username "${ZHIXU_POSTGRES_USER}" --dbname "${ZHIXU_POSTGRES_DB}" \
    --set answer_id="${CURRENT_ANSWER_ID}" <<'SQL'
SELECT count(*)
FROM workflow.tool_call c
JOIN agent.answer a ON a.workflow_run_id=c.workflow_run_id
WHERE a.id=:'answer_id'
  AND (
    c.status<>'SUCCEEDED'
    OR c.tool_version<>2
    OR c.requested_tool_name NOT IN ('ReadSource','ValidateCitation')
    OR c.side_effect_level<>'NONE'
    OR c.invocation_policy<>'MODEL_REQUESTABLE'
  );
SQL
}

draft_session_projection() {
  compose exec -T postgres psql -Atq --username "${ZHIXU_POSTGRES_USER}" --dbname "${ZHIXU_POSTGRES_DB}" \
    --set answer_id="${CURRENT_ANSWER_ID}" <<'SQL'
SELECT count(*)::text||'|'||COALESCE(string_agg(d.status,',' ORDER BY d.generation),'')
FROM agent.answer_draft_session d
WHERE d.answer_id=:'answer_id';
SQL
}

embedding_cache_projection() {
  compose exec -T postgres psql -Atq --username "${ZHIXU_POSTGRES_USER}" --dbname "${ZHIXU_POSTGRES_DB}" \
    --set workspace_id="${WORKSPACE_ID}" <<'SQL'
SELECT count(*)::text||'|'||COALESCE(min(vector_dims(embedding)),0)::text||'|'||COALESCE(max(vector_dims(embedding)),0)::text
FROM retrieval.embedding_cache
WHERE workspace_id=:'workspace_id';
SQL
}

model_version_projection() {
  compose exec -T postgres psql -Atq --username "${ZHIXU_POSTGRES_USER}" --dbname "${ZHIXU_POSTGRES_DB}" \
    --set answer_id="${CURRENT_ANSWER_ID}" <<'SQL'
SELECT count(DISTINCT c.model_version)::text||'|'||COALESCE(min(c.model_version),'')
FROM agent.model_call c
JOIN agent.model_run m ON m.id=c.model_run_id
JOIN agent.answer a ON a.workflow_run_id=m.workflow_run_id
WHERE a.id=:'answer_id';
SQL
}

seed_knowledge_eligibility() {
  ZHIXU_RAG_FIXTURE_DATABASE_URL="postgres://${ZHIXU_POSTGRES_USER}:${ZHIXU_POSTGRES_PASSWORD}@127.0.0.1:${POSTGRES_PORT}/${ZHIXU_POSTGRES_DB}?sslmode=disable" \
  ZHIXU_RAG_FIXTURE_WORKSPACE_ID="${WORKSPACE_ID}" \
  ZHIXU_RAG_FIXTURE_SOURCE_VERSION_ID="${SOURCE_VERSION_ID}" \
  ZHIXU_RAG_FIXTURE_SOURCE_SPAN_ID="${SOURCE_SPAN_ID}" \
    go test -tags=integration -count=1 -run '^TestComposeRAGKnowledgeSeedExternalFixture$' ./cmd/worker >/dev/null || \
    fail 'formal Knowledge eligibility fixture failed'
}

activate_workspace_grant() {
  local database_url workspace_switch_log workspace_switch_code workspace_control_binary workspace_control_env workspace_control_result workspace_docker_wrapper control_instance_id granted_workspace_id
  database_url="postgres://${ZHIXU_POSTGRES_USER}:${ZHIXU_POSTGRES_PASSWORD}@127.0.0.1:${POSTGRES_PORT}/${ZHIXU_POSTGRES_DB}?sslmode=disable"
  workspace_switch_log="${STATE_DIR}/workspace-switch.log"
  workspace_control_binary="${STATE_DIR}/zhixu-workspacectl"
  workspace_control_env="${STATE_DIR}/workspacectl.env"
  workspace_control_result="${STATE_DIR}/workspace-switch.json"
  workspace_docker_wrapper="${STATE_DIR}/workspace-docker"
  WORKSPACE_DOCKER_LOG="${STATE_DIR}/workspace-docker.log"
  cp "${ENV_FILE}" "${workspace_control_env}"
  chmod 600 "${workspace_control_env}"
  : >"${WORKSPACE_DOCKER_LOG}"
  chmod 600 "${WORKSPACE_DOCKER_LOG}"
  cat >"${workspace_docker_wrapper}" <<'SH'
#!/usr/bin/env bash
set -Eeuo pipefail

arguments=("$@")
command_log="$(mktemp "${ZHIXU_WORKSPACE_DOCKER_LOG}.XXXXXX")"
trap 'rm -f -- "${command_log}"' EXIT
set +e
docker "${arguments[@]}" 2>"${command_log}"
status=$?
set -e
cat "${command_log}" >>"${ZHIXU_WORKSPACE_DOCKER_LOG}"
if [[ ${status} -ne 0 ]]; then
  for ((index = 0; index < ${#arguments[@]}; index++)); do
    if [[ "${arguments[index]}" == up ]]; then
      docker "${arguments[@]:0:index}" logs --no-color --tail 200 app worker >>"${ZHIXU_WORKSPACE_DOCKER_LOG}" 2>&1 || true
      break
    fi
  done
fi
exit "${status}"
SH
  chmod 700 "${workspace_docker_wrapper}"
  go build -mod=vendor -o "${workspace_control_binary}" ./cmd/workspacectl >/dev/null || \
    fail 'could not build the one-shot Workspace control binary'
  chmod 700 "${workspace_control_binary}"
  control_instance_id="$(python3 -c 'import uuid; print(uuid.uuid4())')" || fail 'could not create the one-shot Workspace controller identity'
  exec 8<<<"${database_url}"
  if ! ZHIXU_WORKSPACE_DOCKER_LOG="${WORKSPACE_DOCKER_LOG}" "${workspace_control_binary}" switch \
    --workspace-root "${WORKSPACE_ROOT}" --workspace-name 'Compose RAG Smoke' \
    --idempotency-key "compose-rag-workspace-${PROJECT_NAME}" --control-instance-id "${control_instance_id}" \
    --compose-file "${RUNTIME_COMPOSE_FILE}" --env-file "${workspace_control_env}" \
    --grant-override "${GRANT_COMPOSE_FILE}" --compose-project "${PROJECT_NAME}" --docker-executable "${workspace_docker_wrapper}" \
    --database-url-fd 8 --timeout 2m \
      >"${workspace_control_result}" 2>"${workspace_switch_log}"; then
    exec 8<&-
    workspace_switch_code="$(python3 - "${workspace_switch_log}" <<'PY'
import re
import sys

with open(sys.argv[1], encoding="utf-8") as stream:
    matches = re.findall(r'"error_code"\s*:\s*"([A-Z0-9_]+)"', stream.read())
print(matches[-1] if matches else "WORKSPACE_SWITCH_FAILED")
PY
)"
    fail "one-shot Workspace grant activation failed (${workspace_switch_code})"
  fi
  exec 8<&-
  granted_workspace_id="$(jq -er --arg root "${WORKSPACE_ROOT}" '
    select(.schema == "workspace-control-result/v1" and .action == "switch" and (.status == "switched" or .status == "reconciled") and .canonical_root == $root)
    | .workspace_id | select(type == "string")
  ' "${workspace_control_result}")" || fail 'one-shot Workspace control returned an invalid grant receipt'
  WORKSPACE_ID="$(compose exec -T postgres psql -Atq --username "${ZHIXU_POSTGRES_USER}" --dbname "${ZHIXU_POSTGRES_DB}" \
    -c 'SELECT active_workspace_id::text FROM ops.workspace_control_state WHERE singleton=true')"
  [[ "${WORKSPACE_ID}" =~ ^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$ && "${WORKSPACE_ID}" == "${granted_workspace_id}" ]] || \
    fail 'one-shot Workspace control did not publish an Active Workspace identity'
}

main() {
  for command in bash docker jq python3; do require_command "${command}"; done
  docker compose version >/dev/null 2>&1 || fail 'Docker Compose v2 is unavailable'
  [[ "${REAL_PROVIDER_MODE}" =~ ^[01]$ ]] || fail 'real Provider mode must be 0 or 1'
  [[ "${REAL_PROVIDER_PREFLIGHT_ONLY}" =~ ^[01]$ ]] || fail 'real Provider preflight-only mode must be 0 or 1'
  [[ "${RAG_BROWSER_MODE}" =~ ^[01]$ ]] || fail 'fixed RAG browser mode must be 0 or 1'
  [[ "${SYNTHESIS_MODE}" =~ ^[01]$ ]] || fail 'Synthesis mode must be 0 or 1'
  [[ "${WORKSPACE_ANALYSIS_MODE}" =~ ^[01]$ ]] || fail 'Workspace Analysis mode must be 0 or 1'
  [[ "${WORKSPACE_ANALYSIS_OTLP_MODE}" =~ ^[01]$ ]] || fail 'Workspace Analysis OTLP mode must be 0 or 1'
  [[ "${WORKSPACE_ANALYSIS_WORKER_RESTART_MODE}" =~ ^[01]$ ]] || fail 'Workspace Analysis Worker restart mode must be 0 or 1'
  if [[ "${REAL_PROVIDER_PREFLIGHT_ONLY}" == 1 && "${REAL_PROVIDER_MODE}" != 1 ]]; then
    fail 'real Provider preflight-only mode requires real Provider mode'
  fi
  if [[ "${RAG_BROWSER_MODE}" == 1 && ( "${REAL_PROVIDER_MODE}" == 1 || "${WORKSPACE_ANALYSIS_MODE}" == 1 ) ]]; then
    fail 'fixed RAG browser smoke cannot be combined with real Provider or Workspace Analysis mode'
  fi
  if [[ "${WORKSPACE_ANALYSIS_MODE}" == 1 && "${REAL_PROVIDER_MODE}" == 1 ]]; then
    fail 'Workspace Analysis deterministic smoke cannot be combined with real Provider mode'
  fi
  if [[ "${SYNTHESIS_MODE}" == 1 && ( "${WORKSPACE_ANALYSIS_MODE}" == 1 || "${REAL_PROVIDER_MODE}" == 1 || "${RAG_BROWSER_MODE}" == 1 ) ]]; then
    fail 'Synthesis smoke cannot be combined with Workspace Analysis, real Provider, or fixed RAG browser mode'
  fi
  if [[ "${WORKSPACE_ANALYSIS_WORKER_RESTART_MODE}" == 1 && "${WORKSPACE_ANALYSIS_MODE}" != 1 ]]; then
    fail 'Workspace Analysis Worker restart smoke requires Workspace Analysis mode'
  fi
  if [[ "${WORKSPACE_ANALYSIS_OTLP_MODE}" == 1 && "${WORKSPACE_ANALYSIS_MODE}" != 1 ]]; then
    fail 'Workspace Analysis OTLP smoke requires Workspace Analysis mode'
  fi
  if [[ "${WORKSPACE_ANALYSIS_OTLP_MODE}" == 1 && "${WORKSPACE_ANALYSIS_WORKER_RESTART_MODE}" == 1 ]]; then
    fail 'Workspace Analysis OTLP smoke cannot be combined with Worker restart mode'
  fi
  [[ "${TIMEOUT_SECONDS}" =~ ^[1-9][0-9]*$ ]] || fail 'timeout must be a positive integer'
  [[ "${POLL_INTERVAL_SECONDS}" =~ ^[1-9][0-9]*$ ]] || fail 'poll interval must be a positive integer'
  [[ "${REQUEST_TIMEOUT_SECONDS}" =~ ^[1-9][0-9]*$ ]] || fail 'request timeout must be a positive integer'
  if [[ "${REAL_PROVIDER_PREFLIGHT_ONLY}" != 1 ]]; then
    for command in curl git go; do require_command "${command}"; done
    if [[ "${REAL_PROVIDER_MODE}" == 1 || "${RAG_BROWSER_MODE}" == 1 || "${WORKSPACE_ANALYSIS_MODE}" == 1 || "${SYNTHESIS_MODE}" == 1 ]]; then
      require_command node
      require_command npm
    fi
  fi

  STATE_DIR="$(mktemp -d "${TMPDIR:-/tmp}/zhixu-compose-rag-smoke.XXXXXX")" || fail 'could not allocate disposable state'
  trap cleanup EXIT
  trap 'exit 129' HUP
  trap 'exit 130' INT
  trap 'exit 143' TERM
  STATE_DIR="$(cd -- "${STATE_DIR}" && pwd -P)"
  WORKSPACE_ANALYSIS_BARRIER_DIR="${STATE_DIR}/workspace-analysis-barrier"
  WORKSPACE_ANALYSIS_BARRIER_READY_FILE="${WORKSPACE_ANALYSIS_BARRIER_DIR}/browser-ready"
  WORKSPACE_ANALYSIS_BARRIER_ENTERED_FILE="${WORKSPACE_ANALYSIS_BARRIER_CONTAINER_ENTERED_FILE}"
  WORKSPACE_ANALYSIS_BARRIER_SETTLED_FILE="${WORKSPACE_ANALYSIS_BARRIER_CONTAINER_SETTLED_FILE}"
  WORKSPACE_ANALYSIS_BROWSER_PROGRESS_FILE="${WORKSPACE_ANALYSIS_BARRIER_DIR}/browser-progress"
  mkdir -m 0755 -- "${WORKSPACE_ANALYSIS_BARRIER_DIR}"
  local run_id http_port vite_port='' target_path evidence_token chat_canary canary_compose_file workspace_analysis_barrier_stage=''
  local fixture_answer_stream_frame_delay_ms=''
  local chat_runtime_base_url='' embedding_runtime_base_url='' ollama_tags_url=''
  run_id="$(random_hex 6)"; PROJECT_NAME="zhixu-rag-smoke-${run_id}"; http_port="$(allocate_port)"; POSTGRES_PORT="$(allocate_port)"
  [[ "${REAL_PROVIDER_MODE}" != 1 && "${RAG_BROWSER_MODE}" != 1 && "${WORKSPACE_ANALYSIS_MODE}" != 1 && "${SYNTHESIS_MODE}" != 1 ]] || vite_port="$(allocate_port)"
  API_BASE_URL="http://127.0.0.1:${http_port}"; AUTH_ORIGIN="${API_BASE_URL}"; WORKSPACE_ROOT="${STATE_DIR}/project"; target_path='docs/rag-smoke.md'
  [[ "${REAL_PROVIDER_MODE}" != 1 && "${RAG_BROWSER_MODE}" != 1 && "${WORKSPACE_ANALYSIS_MODE}" != 1 && "${SYNTHESIS_MODE}" != 1 ]] || VITE_BASE_URL="http://127.0.0.1:${vite_port}"
  evidence_token="durable-rag-${run_id}"; chat_canary="chat_${run_id}_$(random_hex 12)"
  [[ "${WORKSPACE_ANALYSIS_MODE}" != 1 ]] || workspace_analysis_barrier_stage='workspace_analysis_candidate_stream'
  [[ "${RAG_BROWSER_MODE}" != 1 ]] || fixture_answer_stream_frame_delay_ms='750'
  if [[ "${REAL_PROVIDER_MODE}" == 1 ]]; then
    rag_real_provider_configure "${chat_canary}" || fail 'real Provider configuration is invalid'
    case "${RAG_REAL_PROVIDER_TRANSPORT_RESOLVED}" in
      direct)
        ;;
      host-relay)
        [[ "${RAG_REAL_PROVIDER_KIND_RESOLVED}" == openai-compatible ]] || \
          fail 'Provider host relay requires openai-compatible Chat'
        [[ "${RAG_REAL_PROVIDER_EMBEDDING_KIND_RESOLVED}" == ollama ]] || \
          fail 'Provider host relay requires Ollama Embedding'
        ;;
      *)
        fail 'ZHIXU_RAG_REAL_PROVIDER_TRANSPORT must be direct or host-relay'
        ;;
    esac
    chat_runtime_base_url="${RAG_REAL_PROVIDER_CHAT_BASE_URL}"
    embedding_runtime_base_url="${RAG_REAL_PROVIDER_EMBEDDING_BASE_URL}"
    if [[ "${RAG_REAL_PROVIDER_TRANSPORT_RESOLVED}" == host-relay ]]; then
      PROVIDER_HOST_RELAY_PORT="$(allocate_port)"
      export ZHIXU_RAG_PROVIDER_HOST_RELAY_PORT="${PROVIDER_HOST_RELAY_PORT}"
      chat_runtime_base_url='http://127.0.0.1:11435'
      embedding_runtime_base_url='http://127.0.0.1:11434'
    fi
    if [[ "${REAL_PROVIDER_PREFLIGHT_ONLY}" != 1 && "${RAG_REAL_PROVIDER_EMBEDDING_KIND_RESOLVED}" == ollama ]]; then
      ollama_tags_url="$(python3 - "${RAG_REAL_PROVIDER_EMBEDDING_BASE_URL}" <<'PY'
from urllib.parse import urlsplit, urlunsplit
import sys

parsed = urlsplit(sys.argv[1])
print(urlunsplit((parsed.scheme, parsed.netloc, parsed.path.rstrip('/') + '/api/tags', '', '')))
PY
)" || fail 'could not derive the Ollama model-list endpoint'
      if [[ "${RAG_REAL_PROVIDER_KIND_RESOLVED}" == ollama ]]; then
        curl --fail --silent --show-error --max-time 10 "${ollama_tags_url}" | \
          jq -e --arg chat "${RAG_REAL_PROVIDER_CHAT_MODEL}" --arg embedding "${RAG_REAL_PROVIDER_EMBEDDING_MODEL}" \
            '([.models[].name] | index($chat)) != null and ([.models[].name] | index($embedding)) != null' >/dev/null || \
          fail 'the required real Ollama Chat and Embedding models are unavailable'
      else
        curl --fail --silent --show-error --max-time 10 "${ollama_tags_url}" | \
          jq -e --arg embedding "${RAG_REAL_PROVIDER_EMBEDDING_MODEL}" \
            '([.models[].name] | index($embedding)) != null' >/dev/null || \
          fail 'the required real Ollama Embedding model is unavailable'
      fi
    fi
  fi
  canary_compose_file="${STATE_DIR}/compose.canary.yml"
  if [[ "${REAL_PROVIDER_MODE}" == 1 ]]; then
  cat >"${canary_compose_file}" <<YAML
services:
  postgres:
    ports:
      - "127.0.0.1:${POSTGRES_PORT}:5432"
YAML
  else
  cat >"${canary_compose_file}" <<YAML
services:
  postgres:
    ports:
      - "127.0.0.1:${POSTGRES_PORT}:5432"
  rag-model-fixture:
    environment:
      ZHIXU_RAG_FIXTURE_API_KEY: ${chat_canary}
      ZHIXU_RAG_FIXTURE_BARRIER_STAGE: ${workspace_analysis_barrier_stage}
      ZHIXU_RAG_FIXTURE_BARRIER_RELEASE_FILE: ${WORKSPACE_ANALYSIS_BARRIER_CONTAINER_RELEASE_FILE}
      ZHIXU_RAG_FIXTURE_BARRIER_ENTERED_FILE: ${WORKSPACE_ANALYSIS_BARRIER_CONTAINER_ENTERED_FILE}
      ZHIXU_RAG_FIXTURE_BARRIER_SETTLED_FILE: ${WORKSPACE_ANALYSIS_BARRIER_CONTAINER_SETTLED_FILE}
      ZHIXU_RAG_FIXTURE_BARRIER_ARMED_FILE: ${WORKSPACE_ANALYSIS_BARRIER_CONTAINER_ARMED_FILE}
      ZHIXU_RAG_FIXTURE_BARRIER_LOCK_DIR: ${WORKSPACE_ANALYSIS_BARRIER_CONTAINER_LOCK_DIR}
      ZHIXU_RAG_FIXTURE_ANSWER_STREAM_FRAME_DELAY_MS: ${fixture_answer_stream_frame_delay_ms}
  app:
    environment:
      ZHIXU_CHAT_API_KEY: ${chat_canary}
  worker:
    environment:
      ZHIXU_CHAT_API_KEY: ${chat_canary}
YAML
  fi
  CANARY_COMPOSE_FILE="${canary_compose_file}"
  RUNTIME_COMPOSE_FILE="${STATE_DIR}/compose.runtime.yml"
  GRANT_COMPOSE_FILE="${STATE_DIR}/compose.grant.yml"

  export ZHIXU_HTTP_PORT="${http_port}" ZHIXU_POSTGRES_DB='zhixu_rag_smoke' ZHIXU_POSTGRES_USER='zhixu_rag_smoke'
  export ZHIXU_POSTGRES_PASSWORD="pg_${run_id}_$(random_hex 12)"
  export ZHIXU_AUTH_MODE='required' ZHIXU_AUTH_BOOTSTRAP_TOKEN="auth_${run_id}_$(random_hex 24)"
  export ZHIXU_WORKSPACE_ANALYSIS_CONFIG_REVISION=1
  if [[ "${WORKSPACE_ANALYSIS_MODE}" == 1 ]]; then
    export ZHIXU_WORKSPACE_ANALYSIS_API_ENABLED=true ZHIXU_WORKSPACE_ANALYSIS_WORKER_ENABLED=true
  else
    export ZHIXU_WORKSPACE_ANALYSIS_API_ENABLED=false ZHIXU_WORKSPACE_ANALYSIS_WORKER_ENABLED=false
  fi
  if [[ "${WORKSPACE_ANALYSIS_WORKER_RESTART_MODE}" == 1 ]]; then
    export ZHIXU_WORKER_RESTART_POLICY='no'
    export ZHIXU_WORKER_JOB_TIMEOUT='15m' ZHIXU_WORKER_RESCUE_STUCK_AFTER='30m'
    export ZHIXU_WORKFLOW_LEASE='8s' ZHIXU_WORKFLOW_HEARTBEAT='1s'
  fi
  if [[ "${REAL_PROVIDER_MODE}" == 1 ]]; then
    export ZHIXU_AUTH_ALLOWED_ORIGINS="${AUTH_ORIGIN},${VITE_BASE_URL}" ZHIXU_AUTH_SECURE_COOKIE='false'
    # The real slow-model gate may legitimately consume all ten bounded RAG
    # model calls. Keep River's outer job deadline above that budget so the
    # application ledger, rather than River, remains the first fail-closed cap.
    export ZHIXU_WORKER_JOB_TIMEOUT="${ZHIXU_WORKER_JOB_TIMEOUT:-60m}"
    export ZHIXU_WORKER_RESCUE_STUCK_AFTER="${ZHIXU_WORKER_RESCUE_STUCK_AFTER:-2h}"
    export ZHIXU_CHAT_PROVIDER='openai-compatible'
    export ZHIXU_CHAT_BASE_URL="${chat_runtime_base_url}" ZHIXU_CHAT_API_KEY="${RAG_REAL_PROVIDER_CHAT_API_KEY}"
    export ZHIXU_CHAT_MODEL="${RAG_REAL_PROVIDER_CHAT_MODEL}" ZHIXU_CHAT_MODEL_VERSION="${RAG_REAL_PROVIDER_CHAT_MODEL_VERSION}"
    export ZHIXU_CHAT_ADAPTER_VERSION="${RAG_REAL_PROVIDER_CHAT_ADAPTER_VERSION}" ZHIXU_CHAT_TIMEOUT="${RAG_REAL_PROVIDER_CHAT_TIMEOUT}"
    export ZHIXU_EMBEDDING_PROVIDER="${RAG_REAL_PROVIDER_EMBEDDING_PROVIDER}"
    export ZHIXU_EMBEDDING_BASE_URL="${embedding_runtime_base_url}" ZHIXU_EMBEDDING_API_KEY="${RAG_REAL_PROVIDER_EMBEDDING_API_KEY}"
    export ZHIXU_EMBEDDING_MODEL="${RAG_REAL_PROVIDER_EMBEDDING_MODEL}" ZHIXU_EMBEDDING_DIMENSIONS="${RAG_REAL_PROVIDER_EMBEDDING_DIMENSIONS}"
    export ZHIXU_EMBEDDING_NORMALIZATION='l2' ZHIXU_EMBEDDING_DISTANCE_METRIC='cosine' ZHIXU_EMBEDDING_TIMEOUT="${RAG_REAL_PROVIDER_EMBEDDING_TIMEOUT}"
  elif [[ "${RAG_BROWSER_MODE}" == 1 || "${WORKSPACE_ANALYSIS_MODE}" == 1 || "${SYNTHESIS_MODE}" == 1 ]]; then
    export ZHIXU_AUTH_ALLOWED_ORIGINS="${AUTH_ORIGIN},${VITE_BASE_URL}" ZHIXU_AUTH_SECURE_COOKIE='false'
    export ZHIXU_EMBEDDING_PROVIDER='disabled' ZHIXU_EMBEDDING_BASE_URL='' ZHIXU_EMBEDDING_API_KEY='' ZHIXU_EMBEDDING_MODEL='' ZHIXU_EMBEDDING_DIMENSIONS='0'
  else
    export ZHIXU_AUTH_ALLOWED_ORIGINS="${AUTH_ORIGIN}" ZHIXU_AUTH_SECURE_COOKIE='false'
    export ZHIXU_EMBEDDING_PROVIDER='disabled' ZHIXU_EMBEDDING_BASE_URL='' ZHIXU_EMBEDDING_API_KEY='' ZHIXU_EMBEDDING_MODEL='' ZHIXU_EMBEDDING_DIMENSIONS='0'
  fi
  export ZHIXU_REINDEX_DISPATCH_POLL_INTERVAL='250ms' ZHIXU_REINDEX_DISPATCH_ERROR_BACKOFF='500ms'

  if [[ "${REAL_PROVIDER_PREFLIGHT_ONLY}" == 1 ]]; then
    log 'validating real Provider environment and rendered Eino Compose model'
  else
    log 'validating and building disposable RAG Compose stack'
  fi
  compose config --quiet
  if [[ "${WORKSPACE_ANALYSIS_WORKER_RESTART_MODE}" == 1 ]]; then
    compose --profile workspace-runtime config --format json | jq -e '
      .services.worker.restart == "no"
      and .services.worker.environment.ZHIXU_WORKER_JOB_TIMEOUT == "15m"
      and .services.worker.environment.ZHIXU_WORKER_RESCUE_STUCK_AFTER == "30m"
      and .services.worker.environment.ZHIXU_WORKFLOW_LEASE == "8s"
      and .services.worker.environment.ZHIXU_WORKFLOW_HEARTBEAT == "1s"
    ' >/dev/null || fail 'Workspace Analysis Worker restart runtime is not fail-closed'
  fi
  if [[ "${REAL_PROVIDER_MODE}" == 1 ]]; then
    compose --profile workspace-runtime config --format json | jq -e \
      --arg job_timeout "${ZHIXU_WORKER_JOB_TIMEOUT}" \
      --arg rescue_timeout "${ZHIXU_WORKER_RESCUE_STUCK_AFTER}" \
      --arg transport "${RAG_REAL_PROVIDER_TRANSPORT_RESOLVED}" \
      --arg host_relay_port "${PROVIDER_HOST_RELAY_PORT}" \
      --arg embedding_provider "${RAG_REAL_PROVIDER_EMBEDDING_PROVIDER}" \
      --arg chat_model "${RAG_REAL_PROVIDER_CHAT_MODEL}" \
      --arg chat_model_version "${RAG_REAL_PROVIDER_CHAT_MODEL_VERSION}" \
      --arg chat_adapter_version "${RAG_REAL_PROVIDER_CHAT_ADAPTER_VERSION}" \
      --arg chat_timeout "${RAG_REAL_PROVIDER_CHAT_TIMEOUT}" \
      --arg embedding_model "${RAG_REAL_PROVIDER_EMBEDDING_MODEL}" \
      --arg embedding_dimensions "${RAG_REAL_PROVIDER_EMBEDDING_DIMENSIONS}" \
      --arg embedding_timeout "${RAG_REAL_PROVIDER_EMBEDDING_TIMEOUT}" '
      (.services | has("rag-model-fixture") | not)
      and .services.app.environment.ZHIXU_MODEL_SETTINGS_MODE == "static"
      and .services.worker.environment.ZHIXU_MODEL_SETTINGS_MODE == "static"
      and (.services.app.environment | has("ZHIXU_CHAT_IMPLEMENTATION") | not)
      and (.services.worker.environment | has("ZHIXU_CHAT_IMPLEMENTATION") | not)
      and .services.app.environment.ZHIXU_CHAT_PROVIDER == "openai-compatible"
      and .services.worker.environment.ZHIXU_CHAT_PROVIDER == "openai-compatible"
      and .services.app.environment.ZHIXU_CHAT_MODEL == $chat_model
      and .services.worker.environment.ZHIXU_CHAT_MODEL == $chat_model
      and .services.app.environment.ZHIXU_CHAT_MODEL_VERSION == $chat_model_version
      and .services.worker.environment.ZHIXU_CHAT_MODEL_VERSION == $chat_model_version
      and .services.app.environment.ZHIXU_CHAT_ADAPTER_VERSION == $chat_adapter_version
      and .services.worker.environment.ZHIXU_CHAT_ADAPTER_VERSION == $chat_adapter_version
      and .services.app.environment.ZHIXU_CHAT_TIMEOUT == $chat_timeout
      and .services.worker.environment.ZHIXU_CHAT_TIMEOUT == $chat_timeout
      and .services.app.environment.ZHIXU_CHAT_BASE_URL == env.ZHIXU_CHAT_BASE_URL
      and .services.worker.environment.ZHIXU_CHAT_BASE_URL == env.ZHIXU_CHAT_BASE_URL
      and (env.ZHIXU_CHAT_BASE_URL | length) > 0
      and .services.app.environment.ZHIXU_CHAT_API_KEY == env.ZHIXU_CHAT_API_KEY
      and .services.worker.environment.ZHIXU_CHAT_API_KEY == env.ZHIXU_CHAT_API_KEY
      and (env.ZHIXU_CHAT_API_KEY | length) > 0
      and (.services.worker.environment | has("ZHIXU_EMBEDDING_IMPLEMENTATION") | not)
      and (.services.app.environment | has("ZHIXU_EMBEDDING_IMPLEMENTATION") | not)
      and .services.app.environment.ZHIXU_EMBEDDING_PROVIDER == $embedding_provider
      and .services.worker.environment.ZHIXU_EMBEDDING_PROVIDER == $embedding_provider
      and .services.app.environment.ZHIXU_EMBEDDING_MODEL == $embedding_model
      and .services.worker.environment.ZHIXU_EMBEDDING_MODEL == $embedding_model
      and .services.app.environment.ZHIXU_EMBEDDING_DIMENSIONS == $embedding_dimensions
      and .services.worker.environment.ZHIXU_EMBEDDING_DIMENSIONS == $embedding_dimensions
      and .services.app.environment.ZHIXU_EMBEDDING_TIMEOUT == $embedding_timeout
      and .services.worker.environment.ZHIXU_EMBEDDING_TIMEOUT == $embedding_timeout
      and .services.app.environment.ZHIXU_EMBEDDING_BASE_URL == env.ZHIXU_EMBEDDING_BASE_URL
      and .services.worker.environment.ZHIXU_EMBEDDING_BASE_URL == env.ZHIXU_EMBEDDING_BASE_URL
      and (env.ZHIXU_EMBEDDING_BASE_URL | length) > 0
      and .services.app.environment.ZHIXU_EMBEDDING_API_KEY == env.ZHIXU_EMBEDDING_API_KEY
      and .services.worker.environment.ZHIXU_EMBEDDING_API_KEY == env.ZHIXU_EMBEDDING_API_KEY
      and (if $embedding_provider == "openai-compatible" then (env.ZHIXU_EMBEDDING_API_KEY | length) > 0 else env.ZHIXU_EMBEDDING_API_KEY == "" end)
      and .services.app.environment.ZHIXU_EMBEDDING_NORMALIZATION == "l2"
      and .services.worker.environment.ZHIXU_EMBEDDING_NORMALIZATION == "l2"
      and .services.app.environment.ZHIXU_EMBEDDING_DISTANCE_METRIC == "cosine"
      and .services.worker.environment.ZHIXU_EMBEDDING_DISTANCE_METRIC == "cosine"
      and .services.app.environment.ZHIXU_TOOL_RUNTIME_MODE == "enabled"
      and .services.worker.environment.ZHIXU_TOOL_RUNTIME_MODE == "enabled"
      and (.services.worker.environment | (
        (has("ZHIXU_STRUCTURED_SCHEDULER_RAG") | not)
        and (has("ZHIXU_STRUCTURED_SCHEDULER_RELATION") | not)
        and (has("ZHIXU_STRUCTURED_SCHEDULER_ARTIFACT") | not)
        and (has("ZHIXU_STRUCTURED_SCHEDULER_CAPTURE") | not)
        and (has("ZHIXU_STRUCTURED_SCHEDULER_ORGANIZING") | not)
      ))
      and .services.worker.environment.ZHIXU_WORKER_JOB_TIMEOUT == $job_timeout
      and .services.worker.environment.ZHIXU_WORKER_RESCUE_STUCK_AFTER == $rescue_timeout
      and (if $transport == "host-relay" then
        (.services["app-model-relay"].entrypoint | join(" ") | contains("TCP-LISTEN:11435,bind=127.0.0.1"))
        and (.services["worker-model-relay"].entrypoint | join(" ") | contains("TCP-LISTEN:11435,bind=127.0.0.1"))
        and .services["app-model-relay"].environment.ZHIXU_RAG_PROVIDER_HOST_RELAY_PORT == $host_relay_port
        and .services["worker-model-relay"].environment.ZHIXU_RAG_PROVIDER_HOST_RELAY_PORT == $host_relay_port
      else true end)
    ' >/dev/null || fail 'real Provider Compose model did not preserve the production Eino runtime contract'
    if [[ "${REAL_PROVIDER_PREFLIGHT_ONLY}" == 1 ]]; then
      log "passed: ${RAG_REAL_PROVIDER_DISPLAY_NAME} environment and Eino Compose runtime preflight"
      return 0
    fi
  fi

  prepare_compose_smoke_netns
  if [[ "${REAL_PROVIDER_MODE}" != 1 ]]; then
    RAG_NETNS_OVERRIDE_FILE="${STATE_DIR}/compose.rag-netns.yml"
    cat >"${RAG_NETNS_OVERRIDE_FILE}" <<YAML
services:
  rag-model-fixture:
    network_mode: "container:${WORKER_NETNS_CONTAINER}"
YAML
  fi
  mkdir -p "${WORKSPACE_ROOT}/docs"
  printf '# RAG Compose Smoke\n\nBase content awaiting approved recovery guidance.\n' >"${WORKSPACE_ROOT}/${target_path}"
  git -C "${WORKSPACE_ROOT}" init --initial-branch=main >/dev/null
  git -C "${WORKSPACE_ROOT}" config user.name 'ZHIXU RAG Smoke'
  git -C "${WORKSPACE_ROOT}" config user.email 'rag-smoke@example.invalid'
  git -C "${WORKSPACE_ROOT}" add -- "${target_path}"
  git -C "${WORKSPACE_ROOT}" commit -m base >/dev/null
  chmod -R a+rwX "${WORKSPACE_ROOT}"

  compose --profile workspace-runtime config >"${RUNTIME_COMPOSE_FILE}"
  chmod 600 "${RUNTIME_COMPOSE_FILE}"
  bootstrap_compose config --quiet
  netns_compose config --quiet
  compose --profile workspace-runtime build --quiet
  bootstrap_compose build --quiet model-settings-key-init migrate
  netns_compose build --quiet
  log 'starting isolated namespace anchors'
  netns_compose up --detach --wait >/dev/null
  compose up --detach --wait postgres >/dev/null
  bootstrap_compose run --rm --no-deps -T model-settings-key-init >/dev/null
  bootstrap_compose run --rm --no-deps -T migrate >/dev/null
  if [[ "${REAL_PROVIDER_MODE}" != 1 ]]; then
    compose up --detach --no-deps --wait rag-model-fixture >/dev/null
    if [[ "${WORKSPACE_ANALYSIS_MODE}" == 1 ]]; then
      arm_workspace_analysis_fixture_barrier
      release_workspace_analysis_fixture_barrier
    fi
    if [[ "${WORKSPACE_ANALYSIS_OTLP_MODE}" == 1 ]]; then
      compose up --detach --no-deps otel-collector >/dev/null
      wait_for_workspace_analysis_otlp_collector
    fi
  fi
  start_provider_host_relay
  log 'activating an exact Workspace grant through one-shot workspacectl'
  activate_workspace_grant
  authenticate

  if [[ "${SYNTHESIS_MODE}" == 1 ]]; then
    source "${SCRIPT_DIR}/compose-synthesis-smoke-functions.sh"
    run_synthesis_smoke "${run_id}" "${vite_port}"
    return 0
  fi

  local payload base_hash proposal_id revision_id change_hash workflow_path search_payload citation_href
  request_json POST "/api/v1/workspaces/${WORKSPACE_ID}/scan" 200 '{}' 'workspace scan'
  SOURCE_VERSION_ID="$(jq -er --arg path "${target_path}" '.files[] | select(.relative_path==$path) | .source_version_id' "${LAST_RESPONSE_FILE}")"
  base_hash="$(jq -er --arg path "${target_path}" '.files[] | select(.relative_path==$path) | .content_hash' "${LAST_RESPONSE_FILE}")"
  request_json POST "/api/v1/source-versions/${SOURCE_VERSION_ID}/ingestion-attempts" 201 '{"attempt_number":1}' 'source ingestion' "ingest-${run_id}"
  jq -e '.status=="chunked" and .security_status=="passed" and .chunk_count>0' "${LAST_RESPONSE_FILE}" >/dev/null || fail 'source ingestion did not produce approved chunks'

  payload="$(jq -cn --arg path "${target_path}" --arg base "${base_hash}" --arg token "${evidence_token}" '{target_path:$path,base_hash:$base,content:("# RAG Compose Smoke\n\nApproved recovery requires durable replay without duplicate provider work. Evidence marker "+$token+"\n"),evidence_summary:"compose rag smoke",risk_level:"LOW",risk:"low",rollback_plan:"revert generated commit"}')"
  request_json POST "/api/v1/workspaces/${WORKSPACE_ID}/proposals" 201 "${payload}" 'proposal creation' "proposal-${run_id}"
  proposal_id="$(jq -er '.id' "${LAST_RESPONSE_FILE}")"; revision_id="$(jq -er '.revision.id' "${LAST_RESPONSE_FILE}")"; change_hash="$(jq -er '.revision.change_hash' "${LAST_RESPONSE_FILE}")"
  payload="$(jq -cn --arg revision "${revision_id}" --arg hash "${change_hash}" '{revision_id:$revision,change_hash:$hash,decision:"approved"}')"
  request_json POST "/api/v1/proposals/${proposal_id}/approvals" 201 "${payload}" 'proposal approval'
  workflow_path="$(jq -er '.workflow_status_url' "${LAST_RESPONSE_FILE}")"
  wait_for_proposal "${proposal_id}" "${workflow_path}"

  local retrieval_mode='keyword'
  [[ "${REAL_PROVIDER_MODE}" != 1 ]] || retrieval_mode='hybrid'
  search_payload="$(jq -cn --arg workspace "${WORKSPACE_ID}" --arg query "${evidence_token}" --arg mode "${retrieval_mode}" '{workspace_id:$workspace,query:$query,retrieval_mode:$mode,limit:5}')"
  request_json POST /api/v1/search 200 "${search_payload}" 'approved evidence search'
  citation_href="$(jq -er --arg token "${evidence_token}" '.items[] | select(.snippet|contains($token)) | .provenances[0].source_span_href' "${LAST_RESPONSE_FILE}")"
  SOURCE_VERSION_ID="$(jq -er --arg token "${evidence_token}" '.items[] | select(.snippet|contains($token)) | .provenances[0].source_version_href | split("/")[-1]' "${LAST_RESPONSE_FILE}")"
  SOURCE_SPAN_ID="${citation_href##*/}"
  seed_knowledge_eligibility

  if [[ "${WORKSPACE_ANALYSIS_MODE}" == 1 ]]; then
    source "${SCRIPT_DIR}/compose-workspace-analysis-v2-smoke-functions.sh"
    local analysis_question analysis_conversation_id analysis_answer_id analysis_replay_id
    local analysis_projection replay_projection proposal_count_before proposal_count_after git_head_before git_head_after git_status_before git_status_after
    local analysis_watermark analysis_sse_file analysis_curl_status
    local question_payload analysis_provider_before analysis_provider_after

    wait_for_workspace_analysis_capability
    proposal_count_before="$(compose exec -T postgres psql -Atq --username "${ZHIXU_POSTGRES_USER}" --dbname "${ZHIXU_POSTGRES_DB}" -c \
      "SELECT count(*) FROM change_control.proposal WHERE workspace_id='${WORKSPACE_ID}'")" || fail 'could not capture the Proposal baseline'
    git_head_before="$(git -C "${WORKSPACE_ROOT}" rev-parse HEAD)" || fail 'could not capture the Git HEAD baseline'
    git_status_before="$(git -C "${WORKSPACE_ROOT}" status --porcelain=v2 --untracked-files=all)" || fail 'could not capture the Git status baseline'

    request_json POST /api/v1/conversations 201 "$(jq -cn --arg workspace "${WORKSPACE_ID}" '{workspace_id:$workspace,title:"Compose Workspace Analysis smoke"}')" 'Workspace Analysis conversation creation' "workspace-analysis-conversation-${run_id}"
    analysis_conversation_id="$(jq -er '.id' "${LAST_RESPONSE_FILE}")"
    analysis_watermark="$(compose exec -T postgres psql -Atq --username "${ZHIXU_POSTGRES_USER}" --dbname "${ZHIXU_POSTGRES_DB}" -c \
      "SELECT COALESCE(max(seq),0) FROM ops.server_event WHERE workspace_id='${WORKSPACE_ID}'")"
    [[ "${analysis_watermark}" =~ ^[1-9][0-9]*$ ]] || fail 'could not establish the Workspace Analysis SSE watermark'
    analysis_question="Analyze the approved recovery evidence token ${evidence_token} and recommend the bounded next step with citations."
    question_payload="$(jq -cn --arg workspace "${WORKSPACE_ID}" --arg question "${analysis_question}" '{workspace_id:$workspace,mode:"workspace_analysis",question:$question,scope:{retrieval_mode:"keyword"},answer_depth:"standard",output_format:"markdown"}')"
    request_json POST "/api/v2/conversations/${analysis_conversation_id}/questions" 202 "${question_payload}" 'Workspace Analysis question submission' "workspace-analysis-question-${run_id}"
    jq -e '.question.mode=="workspace_analysis" and .answer.publication_status=="pending"' "${LAST_RESPONSE_FILE}" >/dev/null || fail 'Workspace Analysis submission omitted canonical mode or pending Answer'
    analysis_answer_id="$(jq -er '.answer.id' "${LAST_RESPONSE_FILE}")"
    CURRENT_ANSWER_ID="${analysis_answer_id}"
    wait_for_answer "${analysis_answer_id}"
    assert_workspace_analysis_v2_completed "${analysis_answer_id}" default 5 5 1 true
    analysis_projection="$(cat "${STATE_DIR}/analysis-default-database.json")"
    analysis_provider_before="$(workspace_analysis_fixture_request_count workspace_analysis_decision)"

    request_json POST "/api/v2/conversations/${analysis_conversation_id}/questions" 200 "${question_payload}" 'Workspace Analysis exact replay' "workspace-analysis-question-${run_id}"
    analysis_replay_id="$(jq -er '.answer.id' "${LAST_RESPONSE_FILE}")"
    [[ "${analysis_replay_id}" == "${analysis_answer_id}" ]] || fail 'Workspace Analysis replay created another Answer'
    replay_projection="$(workspace_analysis_v2_database_projection "${analysis_answer_id}")" || fail 'could not reread Workspace Analysis database projection'
    [[ "${replay_projection}" == "${analysis_projection}" ]] || fail 'Workspace Analysis replay duplicated or mutated durable facts'
    analysis_provider_after="$(workspace_analysis_fixture_request_count workspace_analysis_decision)"
    [[ "${analysis_provider_before}" == "${analysis_provider_after}" ]] || fail 'Workspace Analysis replay repeated a Provider decision'
    assert_workspace_analysis_v2_timeline_replay "${analysis_answer_id}" default

    analysis_sse_file="${STATE_DIR}/workspace-analysis-events.sse"
    set +e
    curl --silent --connect-timeout 5 --max-time 2 --cookie "${COOKIE_JAR}" --header "Last-Event-ID: ${analysis_watermark}" --output "${analysis_sse_file}" "${API_BASE_URL}/api/v1/events?workspace_id=${WORKSPACE_ID}"
    analysis_curl_status=$?
    set -e
    [[ ${analysis_curl_status} -eq 0 || ${analysis_curl_status} -eq 28 ]] || fail 'Workspace Analysis SSE replay request failed'
    grep -Eq '^event: workspace_analysis\.started$' "${analysis_sse_file}" || fail 'Workspace Analysis SSE replay omitted the started invalidation'
    grep -Eq '^event: workspace_analysis\.terminated$' "${analysis_sse_file}" || fail 'Workspace Analysis SSE replay omitted the terminal invalidation'
    if grep -Fq -- "${evidence_token}" "${analysis_sse_file}" || grep -Fq -- "${WORKSPACE_ROOT}" "${analysis_sse_file}" || \
      grep -Fq -- "${ZHIXU_POSTGRES_PASSWORD}" "${analysis_sse_file}" || grep -Fq -- "${ZHIXU_AUTH_BOOTSTRAP_TOKEN}" "${analysis_sse_file}" || \
      grep -Fq -- "${chat_canary}" "${analysis_sse_file}" || grep -Fq -- "server_binding" "${analysis_sse_file}"; then
      fail 'Workspace Analysis SSE replay leaked evidence, credentials, paths, or private bindings'
    fi

    if [[ "${WORKSPACE_ANALYSIS_OTLP_MODE}" == 1 ]]; then
      assert_workspace_analysis_otlp_metrics
    else
      run_workspace_analysis_v2_additional_scenarios "${analysis_question}" "${run_id}"
      arm_workspace_analysis_fixture_barrier
      start_vite "${vite_port}"
      run_workspace_analysis_browser "${analysis_question}" "${WORKSPACE_ROOT}" cancelled
      arm_workspace_analysis_fixture_barrier
      run_workspace_analysis_browser "extended ${analysis_question}" "${WORKSPACE_ROOT}" completed
      if [[ "${WORKSPACE_ANALYSIS_WORKER_RESTART_MODE}" == 1 ]]; then
        run_workspace_analysis_worker_restart_smoke "${analysis_question}" "${run_id}"
      fi
    fi

    proposal_count_after="$(compose exec -T postgres psql -Atq --username "${ZHIXU_POSTGRES_USER}" --dbname "${ZHIXU_POSTGRES_DB}" -c \
      "SELECT count(*) FROM change_control.proposal WHERE workspace_id='${WORKSPACE_ID}'")" || fail 'could not verify the Proposal boundary'
    git_head_after="$(git -C "${WORKSPACE_ROOT}" rev-parse HEAD)" || fail 'could not verify Git HEAD'
    git_status_after="$(git -C "${WORKSPACE_ROOT}" status --porcelain=v2 --untracked-files=all)" || fail 'could not verify Git status'
    [[ "${proposal_count_after}" == "${proposal_count_before}" ]] || fail 'Workspace Analysis created or mutated a Proposal'
    [[ "${git_head_after}" == "${git_head_before}" && "${git_status_after}" == "${git_status_before}" ]] || fail 'Workspace Analysis mutated the Git repository'
    if [[ "${WORKSPACE_ANALYSIS_OTLP_MODE}" == 1 ]]; then
      log 'passed: deterministic Workspace Analysis Worker OTLP Metrics export, exact labels, replay uniqueness, and graceful flush'
    else
      log 'passed: dynamic Workspace Analysis default 5/7/5/1, extended 6/8/6/2, budget 12 decisions/12 Git with persistent denial and no 13th Provider call, replay, SSE, Stop, read-only boundary, and desktop/mobile browser'
    fi
    return 0
  fi

  if [[ "${REAL_PROVIDER_MODE}" == 1 ]]; then
    local embedding_projection real_question real_model_calls real_tool_calls real_tool_call_count real_unexpected_tool_calls real_draft_sessions real_model_version
    embedding_projection="$(embedding_cache_projection)" || fail 'could not read the real Embedding cache projection'
    [[ "${embedding_projection}" =~ ^[1-9][0-9]*\|${RAG_REAL_PROVIDER_EMBEDDING_DIMENSIONS}\|${RAG_REAL_PROVIDER_EMBEDDING_DIMENSIONS}$ ]] || \
      fail 'real Eino Embedding did not persist vectors with the configured dimensions'
    real_question="What exact durable recovery token does the approved evidence require? Include ${evidence_token} verbatim and cite the evidence."
    start_vite "${vite_port}"
    run_real_provider_browser "${real_question}" "${evidence_token}" "${SOURCE_VERSION_ID}" "${SOURCE_SPAN_ID}"
    real_model_calls="$(model_call_projection)" || fail 'could not read real Provider model calls'
    [[ "${real_model_calls}" == *'PLAN:SUCCEEDED'* && "${real_model_calls}" == *'AGENT:SUCCEEDED'* && \
       "${real_model_calls}" == *'ANSWER:SUCCEEDED'* && "${real_model_calls}" == *'REVIEW:SUCCEEDED'* ]] || \
      fail 'real Provider RAG omitted PLAN, AGENT, ANSWER, or REVIEW model calls'
    [[ "${real_model_calls}" != *':FAILED'* && "${real_model_calls}" != *':UNKNOWN'* && "${real_model_calls}" != *':STARTED'* ]] || \
      fail 'real Provider RAG retained an unsuccessful model call'
    [[ "${real_model_calls}" == *'INITIAL:SUCCEEDED'* || "${real_model_calls}" == *'REPAIR:SUCCEEDED'* || "${real_model_calls}" == *'REDUCED:SUCCEEDED'* ]] || \
      fail 'real Provider RAG omitted a successful metadata envelope call'
    real_tool_calls="$(tool_call_projection)" || fail 'could not read real Provider tool calls'
    real_tool_call_count="${real_tool_calls%%|*}"
    [[ "${real_tool_call_count}" =~ ^[1-9][0-9]*$ ]] || fail 'real Eino ToolsNode did not persist a read-only tool call'
    [[ "${real_tool_calls}" == *'ReadSource@2:SUCCEEDED'* || "${real_tool_calls}" == *'ValidateCitation@2:SUCCEEDED'* ]] || \
      fail 'real Eino ToolsNode did not persist an allowed read-only tool call'
    real_unexpected_tool_calls="$(unexpected_tool_call_count)" || fail 'could not verify the real Provider Tool allowlist'
    [[ "${real_unexpected_tool_calls}" == 0 ]] || fail 'real Eino ToolsNode persisted a call outside the exact read-only allowlist'
    real_draft_sessions="$(draft_session_projection)" || fail 'could not read real Provider draft sessions'
    [[ "${real_draft_sessions}" == '1|PUBLISHED' ]] || fail 'real Provider stream did not atomically publish one draft session'
    real_model_version="$(model_version_projection)" || fail 'could not read real Provider model version'
    [[ "${real_model_version}" == "1|${RAG_REAL_PROVIDER_CHAT_MODEL_VERSION}" ]] || fail 'persisted model calls did not use the configured real Provider model'
    if [[ "${RAG_REAL_PROVIDER_TRANSPORT_RESOLVED}" == host-relay ]]; then
      local relay_requests
      relay_requests="$(provider_host_relay_request_count)" || fail 'could not read the Provider host relay receipt'
      [[ "${relay_requests}" =~ ^[1-9][0-9]*$ ]] || fail 'the Provider host relay did not forward a real Chat request'
      log 'passed: host-relayed real OpenAI-Compatible Chat, real Ollama Embedding, Eino Graph/Agent/ToolsNode/Stream, PostgreSQL draft, API and desktop/mobile browser'
    else
      log "passed: real ${RAG_REAL_PROVIDER_DISPLAY_NAME} Chat/Embedding, Eino Graph/Agent/ToolsNode/Stream, PostgreSQL draft, API and desktop/mobile browser"
    fi
    return 0
  fi

  local conversation_id watermark answer_id citation_id question_payload replay_answer_id feedback_id sse_file curl_status model_calls_before model_calls_after tool_calls draft_sessions
  request_json POST /api/v1/conversations 201 "$(jq -cn --arg workspace "${WORKSPACE_ID}" '{workspace_id:$workspace,title:"Compose RAG smoke"}')" 'conversation creation' "conversation-${run_id}"
  conversation_id="$(jq -er '.id' "${LAST_RESPONSE_FILE}")"
  watermark="$(compose exec -T postgres psql -Atq --username "${ZHIXU_POSTGRES_USER}" --dbname "${ZHIXU_POSTGRES_DB}" -c "SELECT COALESCE(max(seq),0) FROM ops.server_event WHERE workspace_id='${WORKSPACE_ID}'")"
  [[ "${watermark}" =~ ^[1-9][0-9]*$ ]] || fail 'could not establish a positive SSE replay watermark'
  question_payload="$(jq -cn --arg workspace "${WORKSPACE_ID}" '{workspace_id:$workspace,question:"What does the approved recovery evidence require?",scope:{retrieval_mode:"keyword"},answer_depth:"standard",output_format:"markdown"}')"
  request_json POST "/api/v2/conversations/${conversation_id}/questions" 202 "${question_payload}" 'question submission' "question-${run_id}"
  answer_id="$(jq -er '.answer.id' "${LAST_RESPONSE_FILE}")"
  CURRENT_ANSWER_ID="${answer_id}"
  wait_for_answer "${answer_id}"
  jq -e --arg workspace "${WORKSPACE_ID}" '.publication_status=="completed" and .result_type=="rag_answer" and .workflow.status=="succeeded" and (.citations|length)>0 and (.result.payload.related_topics|length)>0 and (.result.payload.follow_up_questions|length)>0 and .retrieval_summary.selected_count>0 and ([.citations[].workspace_id]|all(.==$workspace))' "${STATE_DIR}/completed-answer.json" >/dev/null || fail 'completed Answer omitted validated RAG projections'
  citation_href="$(jq -er '.citations[0].href' "${STATE_DIR}/completed-answer.json")"; citation_id="$(jq -er '.citations[0].id' "${STATE_DIR}/completed-answer.json")"
  request_json GET "${citation_href}" 200 '' 'citation opening'

  model_calls_before="$(model_call_projection)" || fail 'could not read persisted model call projection'
  [[ "${model_calls_before}" == '5|PLAN:SUCCEEDED,AGENT:SUCCEEDED,ANSWER:SUCCEEDED,INITIAL:SUCCEEDED,REVIEW:SUCCEEDED' ]] || fail 'Compose RAG did not persist the required PLAN, direct-return Agent tool call, streamed Answer, metadata and Review model calls'
  tool_calls="$(tool_call_projection)" || fail 'could not read persisted tool call projection'
  [[ "${tool_calls}" == '1|ReadSource@2:SUCCEEDED' ]] || fail 'Compose RAG did not persist the required ReadSource@2 tool receipt'
  draft_sessions="$(draft_session_projection)" || fail 'could not read draft session projection'
  [[ "${draft_sessions}" == '1|PUBLISHED' ]] || fail 'Compose RAG did not atomically publish the final draft session'

  request_json POST "/api/v2/conversations/${conversation_id}/questions" 200 "${question_payload}" 'question exact replay' "question-${run_id}"
  replay_answer_id="$(jq -er '.answer.id' "${LAST_RESPONSE_FILE}")"; [[ "${replay_answer_id}" == "${answer_id}" ]] || fail 'question replay created another Answer'
  model_calls_after="$(model_call_projection)" || fail 'could not reread persisted model call projection'
  [[ "${model_calls_after}" == "${model_calls_before}" ]] || fail 'question replay repeated a Provider model call'

  sse_file="${STATE_DIR}/events.sse"
  set +e
  curl --silent --connect-timeout 5 --max-time 2 --cookie "${COOKIE_JAR}" --header "Last-Event-ID: ${watermark}" --output "${sse_file}" "${API_BASE_URL}/api/v1/events?workspace_id=${WORKSPACE_ID}"
  curl_status=$?
  set -e
  [[ ${curl_status} -eq 0 || ${curl_status} -eq 28 ]] || fail 'SSE replay request failed'
  grep -Eq '^id: [1-9][0-9]*$' "${sse_file}" || fail 'SSE replay omitted monotonic event ids'
  grep -Fq -- "${answer_id}" "${sse_file}" || fail 'SSE replay omitted the completed Answer resource'
  grep -Eq '^event: answer\.completed$' "${sse_file}" || fail 'SSE replay omitted the completed Answer event'
  if grep -Fq -- "${evidence_token}" "${sse_file}" || grep -Fq -- "${ZHIXU_POSTGRES_PASSWORD}" "${sse_file}" || grep -Fq -- "${ZHIXU_AUTH_BOOTSTRAP_TOKEN}" "${sse_file}" || grep -Fq -- "${chat_canary}" "${sse_file}"; then fail 'SSE replay leaked private content or credentials'; fi

  payload="$(jq -cn --arg workspace "${WORKSPACE_ID}" --arg citation "${citation_id}" '{workspace_id:$workspace,feedback_type:"irrelevant_citation",citation_id:$citation,comment:"compose evaluation signal"}')"
  request_json POST "/api/v1/answers/${answer_id}/feedback" 201 "${payload}" 'feedback creation' "feedback-${run_id}"
  feedback_id="$(jq -er '.id' "${LAST_RESPONSE_FILE}")"
  request_json POST "/api/v1/answers/${answer_id}/feedback" 200 "${payload}" 'feedback exact replay' "feedback-${run_id}"
  [[ "$(jq -er '.id' "${LAST_RESPONSE_FILE}")" == "${feedback_id}" ]] || fail 'feedback replay created another record'
  request_json GET "/api/v2/answers/${answer_id}?workspace_id=${WORKSPACE_ID}" 200 '' 'answer after feedback'
  jq -e --slurpfile before "${STATE_DIR}/completed-answer.json" '.publication_status==$before[0].publication_status and .result==$before[0].result and .citations==$before[0].citations' "${LAST_RESPONSE_FILE}" >/dev/null || fail 'feedback mutated the published Answer'

  if [[ "${RAG_BROWSER_MODE}" == 1 ]]; then
    start_vite "${vite_port}"
    run_fixed_rag_browser 'What does the approved recovery evidence require?'
    persist_fixed_rag_browser_artifacts
  fi

  if [[ "${RAG_BROWSER_MODE}" == 1 ]]; then
    log 'passed: deterministic fixed RAG API/River/Worker/PostgreSQL, keyword SSE draft/final Answer, Citation, desktop/mobile browser and replay'
  else
    log 'passed: public ingestion/approval/reindex, eligibility-only seed, Conversation/River/RAG, Citation, SSE and Feedback replay'
  fi
}

main "$@"
