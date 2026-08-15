#!/usr/bin/env bash

set -Eeuo pipefail
umask 077

readonly SCRIPT_DIR="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
readonly REPOSITORY_ROOT="$(cd -- "${SCRIPT_DIR}/.." && pwd)"
readonly COMPOSE_FILE="${SCRIPT_DIR}/compose.yml"
readonly BOOTSTRAP_COMPOSE_FILE="${SCRIPT_DIR}/compose.bootstrap.yml"
readonly NETNS_COMPOSE_FILE="${SCRIPT_DIR}/compose.netns.yml"
readonly ENV_FILE="${REPOSITORY_ROOT}/.env.example"
readonly TIMEOUT_SECONDS="${ZHIXU_MANAGED_OLLAMA_SMOKE_TIMEOUT_SECONDS:-900}"
readonly CHAT_MODEL="smollm2:135m"
readonly EMBEDDING_MODEL="all-minilm:latest"
readonly EMBEDDING_DIMENSIONS=384
readonly IDLE_MEMORY_LIMIT_BYTES=$((64 * 1024 * 1024))
# This public-shaped address exists only on each disposable smoke network
# namespace's loopback device. It lets production SSRF guards stay enabled.
readonly ONLINE_FIXTURE_IP="93.184.216.34"

source "${SCRIPT_DIR}/compose-smoke-cleanup.sh"

STATE_DIR=""
PROJECT_NAME=""
NETNS_PROJECT_NAME=""
NETNS_NETWORK_NAME=""
APP_NETNS_CONTAINER=""
WORKER_NETNS_CONTAINER=""
MAIN_NETNS_OVERRIDE_FILE=""
NETNS_OVERRIDE_FILE=""
ONLINE_NETNS_OVERRIDE_FILE=""
RUNTIME_OVERRIDE_FILE=""
API_BASE_URL=""
AUTH_ORIGIN=""
COOKIE_JAR=""
CSRF_TOKEN=""
LAST_RESPONSE_FILE=""
ONLINE_FIXTURE_PID=""
ONLINE_FIXTURE_PORT=""
ONLINE_FIXTURE_SECRET=""
ONLINE_BASE_URL=""
WORKSPACE_ROOT=""
RUNTIME_CONTAINER=""

log() { printf '[managed-ollama-compose-smoke] %s\n' "$1"; }

compose() {
  docker compose --project-name "${PROJECT_NAME}" -f "${COMPOSE_FILE}" -f "${RUNTIME_OVERRIDE_FILE}" \
    -f "${MAIN_NETNS_OVERRIDE_FILE}" --env-file "${ENV_FILE}" "$@"
}

bootstrap_compose() {
  docker compose --project-name "${PROJECT_NAME}" -f "${COMPOSE_FILE}" -f "${BOOTSTRAP_COMPOSE_FILE}" \
    -f "${MAIN_NETNS_OVERRIDE_FILE}" --env-file "${ENV_FILE}" "$@"
}

netns_compose() {
  docker compose --project-name "${NETNS_PROJECT_NAME}" -f "${NETNS_COMPOSE_FILE}" \
    -f "${NETNS_OVERRIDE_FILE}" -f "${ONLINE_NETNS_OVERRIDE_FILE}" --env-file "${ENV_FILE}" "$@"
}

diagnose() {
  [[ -n "${PROJECT_NAME}" && -n "${RUNTIME_OVERRIDE_FILE}" ]] || return 0
  if [[ -n "${LAST_RESPONSE_FILE}" && -f "${LAST_RESPONSE_FILE}" ]]; then
    jq '{rollout,participants:{api:.participants.api|{phase,last_error_code,retryable},worker:.participants.worker|{phase,last_error_code,retryable}},local_runtime,desired:{chat:.desired_settings.chat|{provider,model},embedding:.desired_settings.embedding|{provider,model}}}' \
      "${LAST_RESPONSE_FILE}" >&2 2>/dev/null || true
  fi
  compose ps --format 'table {{.Service}}\t{{.State}}\t{{.Health}}' >&2 2>/dev/null || true
  compose logs --no-color --tail 100 local-model-runtime app worker >&2 2>/dev/null || true
  if [[ -n "${RUNTIME_CONTAINER}" ]]; then
    docker top "${RUNTIME_CONTAINER}" -eo pid,ppid,pgid,rss,args >&2 2>/dev/null || true
  fi
  compose exec -T postgres psql -Atq --username "${ZHIXU_POSTGRES_USER}" --dbname "${ZHIXU_POSTGRES_DB}" <<'SQL' >&2 2>/dev/null || true
SELECT 'runtime|'||observed_phase||'|'||requirement_hash||'|'||ready_requirement_hash||'|'||COALESCE(last_error_code,'')
FROM ops.managed_ollama_runtime WHERE singleton=true;
SELECT 'operation|'||kind||'|'||phase||'|'||attempt_no||'|'||COALESCE(error_code,'')
FROM ops.managed_ollama_operations ORDER BY created_at DESC LIMIT 8;
SELECT 'settings|'||desired_revision||'|'||active_revision||'|'||phase||'|'||COALESCE(last_error_code,'')
FROM ops.model_settings_state WHERE singleton=true;
SQL
}

fail() {
  printf '[managed-ollama-compose-smoke] failed: %s\n' "$1" >&2
  diagnose
  exit 1
}

stop_online_fixture() {
  [[ -n "${ONLINE_FIXTURE_PID}" ]] || return 0
  kill "${ONLINE_FIXTURE_PID}" >/dev/null 2>&1 || true
  wait "${ONLINE_FIXTURE_PID}" >/dev/null 2>&1 || true
  ONLINE_FIXTURE_PID=""
}

cleanup() {
  local exit_code=$?
  local cleanup_exit=0
  trap - EXIT HUP INT TERM
  stop_online_fixture
  if [[ -n "${PROJECT_NAME}" && -f "${RUNTIME_OVERRIDE_FILE}" && -f "${MAIN_NETNS_OVERRIDE_FILE}" && -f "${NETNS_OVERRIDE_FILE}" && -f "${ONLINE_NETNS_OVERRIDE_FILE}" ]]; then
    cleanup_compose_smoke_project_images "${PROJECT_NAME}" "${NETNS_PROJECT_NAME}" || cleanup_exit=$?
  fi
  if [[ -n "${STATE_DIR}" && "${STATE_DIR}" == "${TMPDIR:-/tmp}"/zhixu-managed-ollama-smoke.* ]]; then
    chmod -R u+rwX "${STATE_DIR}" >/dev/null 2>&1 || true
    rm -rf -- "${STATE_DIR}" >/dev/null 2>&1 || cleanup_exit=1
  fi
  if [[ ${exit_code} -ne 0 ]]; then
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

start_online_fixture() {
  local certificate_dir="${STATE_DIR}/online-fixture-certs"
  mkdir -p "${certificate_dir}"
  openssl req -x509 -newkey rsa:2048 -nodes -days 1 \
    -subj '/CN=ZHIXU Managed Ollama Smoke CA' \
    -keyout "${certificate_dir}/ca.key" -out "${certificate_dir}/ca.pem" >/dev/null 2>&1
  openssl req -newkey rsa:2048 -nodes -subj '/CN=host.docker.internal' \
    -keyout "${certificate_dir}/server.key" -out "${certificate_dir}/server.csr" >/dev/null 2>&1
  printf 'subjectAltName=DNS:host.docker.internal,IP:127.0.0.1,IP:%s\nextendedKeyUsage=serverAuth\n' "${ONLINE_FIXTURE_IP}" \
    >"${certificate_dir}/server.ext"
  openssl x509 -req -days 1 -sha256 -in "${certificate_dir}/server.csr" \
    -CA "${certificate_dir}/ca.pem" -CAkey "${certificate_dir}/ca.key" -CAcreateserial \
    -extfile "${certificate_dir}/server.ext" -out "${certificate_dir}/server.pem" >/dev/null 2>&1
  chmod 0644 "${certificate_dir}/ca.pem"

  cat >"${STATE_DIR}/online_fixture.py" <<'PY'
import json
import os
import ssl
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer

SECRET = os.environ["SMOKE_ONLINE_SECRET"]

class Handler(BaseHTTPRequestHandler):
    def log_message(self, *_args):
        return

    def send_json(self, status, value):
        encoded = json.dumps(value, separators=(",", ":")).encode()
        self.send_response(status)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(encoded)))
        self.end_headers()
        self.wfile.write(encoded)

    def do_GET(self):
        if self.path == "/healthz":
            self.send_response(204)
            self.end_headers()
            return
        self.send_error(404)

    def do_POST(self):
        if self.headers.get("Authorization") != "Bearer " + SECRET:
            self.send_error(401)
            return
        try:
            length = int(self.headers.get("Content-Length", "0"))
            if length <= 0 or length > 1024 * 1024:
                raise ValueError()
            request = json.loads(self.rfile.read(length))
            model = request["model"]
        except Exception:
            self.send_error(400)
            return
        if self.path == "/v1/chat/completions":
            self.send_json(200, {
                "choices": [{"index": 0, "message": {"role": "assistant", "content": "ok"}, "finish_reason": "stop"}]
            })
            return
        if self.path == "/v1/embeddings":
            dimensions = int(request.get("dimensions", 384))
            vector = [0.0] * dimensions
            vector[0] = 1.0
            self.send_json(200, {"model": model, "data": [{"index": 0, "embedding": vector}]})
            return
        self.send_error(404)

address = ("0.0.0.0", int(os.environ["SMOKE_ONLINE_PORT"]))
server = ThreadingHTTPServer(address, Handler)
context = ssl.SSLContext(ssl.PROTOCOL_TLS_SERVER)
context.load_cert_chain(os.environ["SMOKE_ONLINE_CERT"], os.environ["SMOKE_ONLINE_KEY"])
server.socket = context.wrap_socket(server.socket, server_side=True)
server.serve_forever()
PY

  SMOKE_ONLINE_SECRET="${ONLINE_FIXTURE_SECRET}" \
    SMOKE_ONLINE_PORT="${ONLINE_FIXTURE_PORT}" \
    SMOKE_ONLINE_CERT="${certificate_dir}/server.pem" \
    SMOKE_ONLINE_KEY="${certificate_dir}/server.key" \
    python3 "${STATE_DIR}/online_fixture.py" >"${STATE_DIR}/online-fixture.log" 2>&1 &
  ONLINE_FIXTURE_PID=$!

  local started_at=${SECONDS}
  while (( SECONDS - started_at < 30 )); do
    if curl --silent --fail --max-time 2 --noproxy '*' --cacert "${certificate_dir}/ca.pem" \
      "https://127.0.0.1:${ONLINE_FIXTURE_PORT}/healthz" >/dev/null; then
      return 0
    fi
    kill -0 "${ONLINE_FIXTURE_PID}" >/dev/null 2>&1 || fail 'controlled online fixture exited before readiness'
    sleep 0.25
  done
  fail 'controlled online fixture readiness timed out'
}

request_json() {
  local method=$1 path=$2 expected_status=$3 body=${4-} label=$5
  local response_file http_status
  response_file="$(mktemp "${STATE_DIR}/response.XXXXXX")" || fail 'could not allocate response file'
  local -a args=(--silent --show-error --connect-timeout 5 --max-time 35
    --output "${response_file}" --write-out '%{http_code}' --request "${method}" --cookie "${COOKIE_JAR}")
  if [[ "${method}" != GET && "${method}" != HEAD && "${method}" != OPTIONS ]]; then
    args+=(--header "Origin: ${AUTH_ORIGIN}" --header "X-CSRF-Token: ${CSRF_TOKEN}")
  fi
  if [[ -n "${body}" ]]; then
    args+=(--header 'Content-Type: application/json' --data-binary "${body}")
  fi
  http_status="$(curl "${args[@]}" "${API_BASE_URL}${path}")" || fail "${label} request failed"
  LAST_RESPONSE_FILE="${response_file}"
  if [[ "${http_status}" != "${expected_status}" ]]; then
    local problem_code
    problem_code="$(jq -r '.error_code // "unknown"' "${response_file}" 2>/dev/null || printf unknown)"
    fail "${label} returned HTTP ${http_status} (${problem_code})"
  fi
  jq -e 'type == "object"' "${response_file}" >/dev/null || fail "${label} returned invalid JSON"
}

authenticate() {
  local response_file="${STATE_DIR}/auth-response.json"
  local http_status
  COOKIE_JAR="${STATE_DIR}/cookies.txt"
  http_status="$(curl --silent --show-error --connect-timeout 5 --max-time 15 \
    --output "${response_file}" --write-out '%{http_code}' --request POST \
    --cookie-jar "${COOKIE_JAR}" --header "Origin: ${AUTH_ORIGIN}" \
    --header "Authorization: Bearer ${ZHIXU_AUTH_BOOTSTRAP_TOKEN}" \
    "${API_BASE_URL}/api/v1/auth/sessions")" || fail 'authentication request failed'
  [[ "${http_status}" == 201 ]] || fail "authentication returned HTTP ${http_status}"
  CSRF_TOKEN="$(jq -er '.csrf_token | select(type == "string" and length > 20)' "${response_file}")" || \
    fail 'authentication omitted CSRF token'
}

process_table() {
  docker top "${RUNTIME_CONTAINER}" -eo pid,ppid,pgid,rss,args
}

serve_count() {
  process_table | awk 'NR>1 && $0 ~ /(^|[[:space:]])\/(usr\/)?bin\/ollama serve([[:space:]]|$)/ {count++} END {print count+0}'
}

heavy_process_count() {
  process_table | awk 'NR>1 && ($0 ~ /(^|[[:space:]])\/(usr\/)?bin\/ollama serve([[:space:]]|$)/ || $0 ~ /\/usr\/lib\/ollama\// || $0 ~ /llama-server/ || $0 ~ /ollama runner/) {count++} END {print count+0}'
}

assert_no_heavy_process() {
  [[ "$(heavy_process_count)" == 0 ]] || fail "$1 left ollama serve or a model runner alive"
}

assert_one_serve() {
  [[ "$(serve_count)" == 1 ]] || fail "$1 did not retain exactly one ollama serve process"
}

wait_one_serve() {
  local label=$1 started_at=${SECONDS}
  while (( SECONDS - started_at < 30 )); do
    [[ "$(serve_count)" == 1 ]] && return 0
    sleep 1
  done
  fail "$label did not retain exactly one ollama serve process"
}

settings_payload() {
  local mode=$1 expected_revision=$2 chat_local=false embedding_local=false
  case "${mode}" in
    online) ;;
    chat-local) chat_local=true ;;
    embedding-local) embedding_local=true ;;
    both-local) chat_local=true; embedding_local=true ;;
    *) fail "unsupported settings mode: ${mode}" ;;
  esac
  jq -cn \
    --argjson revision "${expected_revision}" \
    --argjson chat_local "${chat_local}" \
    --argjson embedding_local "${embedding_local}" \
    --arg online_url "${ONLINE_BASE_URL}" \
    --arg secret "${ONLINE_FIXTURE_SECRET}" \
    --arg chat_model "${CHAT_MODEL}" \
    --arg embedding_model "${EMBEDDING_MODEL}" \
    --argjson dimensions "${EMBEDDING_DIMENSIONS}" '
  {
    expected_revision: $revision,
    chat: (if $chat_local then {
      provider:"ollama",api_style:"chat_completions",base_url:"http://127.0.0.1:11434",
      model:$chat_model,model_version:$chat_model,adapter_version:"v1",api_key:{action:"clear"}
    } else {
      provider:"openai-compatible",api_style:"chat_completions",base_url:$online_url,
      model:"online-chat",model_version:"online-chat",adapter_version:"v1",api_key:{action:"replace",value:$secret}
    } end),
    embedding: (if $embedding_local then {
      provider:"ollama",base_url:"http://127.0.0.1:11434",model:$embedding_model,dimensions:$dimensions,
      normalization:"l2",distance_metric:"cosine",api_key:{action:"clear"}
    } else {
      provider:"openai-compatible",base_url:$online_url,model:"online-embedding",dimensions:$dimensions,
      normalization:"l2",distance_metric:"cosine",api_key:{action:"replace",value:$secret}
    } end)
  }'
}

save_mode() {
  local mode=$1 current_revision payload
  request_json GET /api/v1/settings/models 200 '' 'settings snapshot'
  current_revision="$(jq -er '.desired_revision' "${LAST_RESPONSE_FILE}")"
  payload="$(settings_payload "${mode}" "${current_revision}")"
  request_json PUT /api/v1/settings/models 200 "${payload}" "save ${mode}"
  jq -er '.desired_revision' "${LAST_RESPONSE_FILE}"
}

wait_converged() {
  local mode=$1 target_revision=$2 local_required=$3
  local chat_provider='openai-compatible' embedding_provider='openai-compatible'
  case "${mode}" in
    chat-local) chat_provider='ollama' ;;
    embedding-local) embedding_provider='ollama' ;;
    both-local) chat_provider='ollama'; embedding_provider='ollama' ;;
  esac
  local started_at=${SECONDS}
  while (( SECONDS - started_at < TIMEOUT_SECONDS )); do
    request_json GET /api/v1/settings/models 200 '' "wait ${mode} convergence"
    if jq -e \
      --argjson target "${target_revision}" \
      --arg chat "${chat_provider}" --arg embedding "${embedding_provider}" \
      --argjson local_required "${local_required}" '
      .desired_revision==$target and .active_revision==$target and
      .desired_settings.chat.provider==$chat and .active_settings.chat.provider==$chat and
      .desired_settings.embedding.provider==$embedding and .active_settings.embedding.provider==$embedding and
      .runtime.api.applied_revision==$target and .runtime.api.phase=="active" and .runtime.api.fresh and
      .runtime.worker.applied_revision==$target and .runtime.worker.phase=="active" and .runtime.worker.fresh and
      .rollout.phase=="idle" and (.apply_required|not) and (.restart_required|not) and
      (if $local_required then
        .local_runtime.mode=="managed" and .local_runtime.phase=="ready" and .local_runtime.fresh and
        .local_runtime.requirement_hash!="" and .local_runtime.ready_hash==.local_runtime.requirement_hash
      else
        .local_runtime.mode=="managed" and .local_runtime.phase=="stopped" and .local_runtime.fresh and
        .local_runtime.requirement_hash=="" and .local_runtime.ready_hash==""
      end)
    ' "${LAST_RESPONSE_FILE}" >/dev/null; then
      return 0
    fi
    if [[ "$(jq -r '.rollout.phase' "${LAST_RESPONSE_FILE}")" == failed ]]; then
      fail "${mode} activation failed ($(jq -r '.rollout.last_error_code // "unknown"' "${LAST_RESPONSE_FILE}"))"
    fi
    sleep 2
  done
  fail "${mode} activation did not converge"
}

activate_mode() {
  local mode=$1 target_revision=$2 local_required=$3 payload
  payload="$(jq -cn --argjson revision "${target_revision}" '{expected_revision:$revision}')"
  request_json POST /api/v1/settings/models/activations 202 "${payload}" "activate ${mode}"
  wait_converged "${mode}" "${target_revision}" "${local_required}"
}

runtime_requirement() {
  compose exec -T postgres psql -Atq --username "${ZHIXU_POSTGRES_USER}" --dbname "${ZHIXU_POSTGRES_DB}" \
    -c "SELECT required_models::text FROM ops.managed_ollama_runtime WHERE singleton=true" | tr -d '[:space:]'
}

wait_exact_runtime_requirement() {
  local expected=$1 label=$2 started_at=${SECONDS} current
  while (( SECONDS - started_at < TIMEOUT_SECONDS )); do
    current="$(runtime_requirement)"
    if [[ "${current}" == "${expected}" ]]; then
      return 0
    fi
    sleep 2
  done
  fail "${label} requirement did not converge (got ${current:-empty}, want ${expected})"
}

latest_activation_attempts() {
  compose exec -T postgres psql -Atq --username "${ZHIXU_POSTGRES_USER}" --dbname "${ZHIXU_POSTGRES_DB}" \
    -c "SELECT attempt_no FROM ops.managed_ollama_operations WHERE kind='activation' ORDER BY created_at DESC LIMIT 1" | tr -d '[:space:]'
}

operation_attempt_total() {
  compose exec -T postgres psql -Atq --username "${ZHIXU_POSTGRES_USER}" --dbname "${ZHIXU_POSTGRES_DB}" \
    -c "SELECT COALESCE(sum(attempt_no),0)||'|'||count(*) FROM ops.managed_ollama_operations" | tr -d '[:space:]'
}

model_identities() {
  docker exec -e OLLAMA_HOST=127.0.0.1:11435 "${RUNTIME_CONTAINER}" /bin/bash -ec \
    '/bin/ollama list | awk '\''NR>1 {print $1"|"$2}'\'' | sort'
}

wait_runtime_health() {
  local started_at=${SECONDS}
  while (( SECONDS - started_at < 120 )); do
    if [[ "$(docker inspect --format '{{if .State.Health}}{{.State.Health.Status}}{{end}}' "${RUNTIME_CONTAINER}" 2>/dev/null || true)" == healthy ]]; then
      return 0
    fi
    sleep 1
  done
  fail 'local model manager did not become healthy after restart'
}

sample_idle_memory() {
  local max_anon=0 max_process_rss=0 sample anon process_rss
  for sample in 1 2 3 4 5 6; do
    assert_no_heavy_process "idle memory sample ${sample}"
    anon="$(docker exec "${RUNTIME_CONTAINER}" /bin/bash -ec "awk '\$1==\"anon\" {print \$2}' /sys/fs/cgroup/memory.stat")"
    process_rss="$(process_table | awk 'NR>1 && $0 ~ /zhixu-local-model-runtime/ {sum+=$4} END {print (sum+0)*1024}')"
    [[ "${anon}" =~ ^[0-9]+$ && "${process_rss}" =~ ^[0-9]+$ ]] || fail 'idle memory sample was not numeric'
    (( anon > max_anon )) && max_anon=${anon}
    (( process_rss > max_process_rss )) && max_process_rss=${process_rss}
    if (( sample < 6 )); then sleep 12; fi
  done
  (( max_anon <= IDLE_MEMORY_LIMIT_BYTES )) || fail "idle cgroup anon exceeded 64 MiB (${max_anon} bytes)"
  (( max_process_rss <= IDLE_MEMORY_LIMIT_BYTES )) || fail "idle manager RSS exceeded 64 MiB (${max_process_rss} bytes)"
  awk -v anon="${max_anon}" -v rss="${max_process_rss}" 'BEGIN {
    printf "[managed-ollama-compose-smoke] idle memory: max anon %.2f MiB, max manager RSS %.2f MiB, reduction from 700 MiB %.1f%%\n", anon/1048576, rss/1048576, (1-(anon/1048576)/700)*100
  }'
}

main() {
  local command run_id http_port target models_before models_after attempts_before attempts_after child_epoch_before child_epoch_after
  for command in bash curl docker jq openssl python3; do require_command "${command}"; done
  docker compose version >/dev/null 2>&1 || fail 'Docker Compose v2 is unavailable'
  [[ "${TIMEOUT_SECONDS}" =~ ^[1-9][0-9]*$ ]] || fail 'timeout must be a positive integer'

  STATE_DIR="$(mktemp -d "${TMPDIR:-/tmp}/zhixu-managed-ollama-smoke.XXXXXX")" || fail 'could not allocate state directory'
  trap cleanup EXIT
  trap 'exit 129' HUP
  trap 'exit 130' INT
  trap 'exit 143' TERM

  run_id="$(random_hex 6)"
  PROJECT_NAME="zhixu-rag-smoke-${run_id}"
  prepare_compose_smoke_netns
  http_port="$(allocate_port)"
  ONLINE_FIXTURE_PORT="$(allocate_port)"
  ONLINE_FIXTURE_SECRET="fixture_${run_id}_$(random_hex 16)"
  ONLINE_BASE_URL="https://${ONLINE_FIXTURE_IP}:${ONLINE_FIXTURE_PORT}"
  API_BASE_URL="http://127.0.0.1:${http_port}"
  AUTH_ORIGIN="${API_BASE_URL}"
  WORKSPACE_ROOT="${STATE_DIR}/workspace"
  mkdir -p "${WORKSPACE_ROOT}/docs"
  printf '# Managed Ollama Compose Smoke\n' >"${WORKSPACE_ROOT}/docs/smoke.md"

  start_online_fixture
  ONLINE_NETNS_OVERRIDE_FILE="${STATE_DIR}/compose.online-netns.yml"
  cat >"${ONLINE_NETNS_OVERRIDE_FILE}" <<YAML
services:
  app-netns:
    entrypoint:
      - /bin/sh
      - -ec
      - |
        ip address add ${ONLINE_FIXTURE_IP}/32 dev lo
        setpriv --reuid 10001 --regid 10001 --clear-groups --nnp --inh-caps -all --ambient-caps -all -- \
          socat TCP-LISTEN:${ONLINE_FIXTURE_PORT},bind=${ONLINE_FIXTURE_IP},fork,reuseaddr TCP:host.docker.internal:${ONLINE_FIXTURE_PORT} &
        exec /app/netns-ingress.sh
  worker-netns:
    user: "0:0"
    cap_add:
      - NET_ADMIN
      - SETUID
      - SETGID
    entrypoint:
      - /bin/sh
      - -ec
      - |
        ip address add ${ONLINE_FIXTURE_IP}/32 dev lo
        setpriv --reuid 10001 --regid 10001 --clear-groups --nnp --inh-caps -all --ambient-caps -all -- \
          socat TCP-LISTEN:${ONLINE_FIXTURE_PORT},bind=${ONLINE_FIXTURE_IP},fork,reuseaddr TCP:host.docker.internal:${ONLINE_FIXTURE_PORT} &
        exec setpriv --reuid 10001 --regid 10001 --clear-groups --nnp --inh-caps -all --ambient-caps -all -- \
          /app/netns-ingress.sh --worker-sentinel
YAML
  RUNTIME_OVERRIDE_FILE="${STATE_DIR}/compose.runtime.yml"
  cat >"${RUNTIME_OVERRIDE_FILE}" <<YAML
services:
  app:
    environment:
      SSL_CERT_FILE: /run/zhixu-smoke-ca.pem
    volumes:
      - type: bind
        source: "${WORKSPACE_ROOT}"
        target: /workspace/project
      - type: bind
        source: "${STATE_DIR}/online-fixture-certs/ca.pem"
        target: /run/zhixu-smoke-ca.pem
        read_only: true
  worker:
    environment:
      SSL_CERT_FILE: /run/zhixu-smoke-ca.pem
    volumes:
      - type: bind
        source: "${WORKSPACE_ROOT}"
        target: /workspace/project
      - type: bind
        source: "${STATE_DIR}/online-fixture-certs/ca.pem"
        target: /run/zhixu-smoke-ca.pem
        read_only: true
YAML

  export ZHIXU_HTTP_PORT="${http_port}" ZHIXU_POSTGRES_DB='zhixu_managed_ollama_smoke'
  export ZHIXU_POSTGRES_USER='zhixu_managed_ollama_smoke'
  export ZHIXU_POSTGRES_PASSWORD="pg_${run_id}_$(random_hex 16)"
  export ZHIXU_AUTH_MODE='required' ZHIXU_AUTH_BOOTSTRAP_TOKEN="auth_${run_id}_$(random_hex 24)"
  export ZHIXU_AUTH_ALLOWED_ORIGINS="${AUTH_ORIGIN}" ZHIXU_AUTH_SECURE_COOKIE='false'
  export ZHIXU_MODEL_SETTINGS_MODE='managed' ZHIXU_APP_RESTART_POLICY='no' ZHIXU_WORKER_RESTART_POLICY='no'

  log 'building an isolated real Compose stack'
  compose config --quiet
  bootstrap_compose --profile workspace-runtime config --quiet
  netns_compose config --quiet
  compose build --quiet app worker app-model-relay worker-model-relay local-model-runtime
  bootstrap_compose --profile workspace-runtime build --quiet model-settings-key-init migrate \
    local-model-volume-init local-model-runtime-credential-init
  netns_compose build --quiet
  netns_compose up --detach --wait >/dev/null
  compose run --rm --no-deps --user root --entrypoint sh app -c \
    'chown -R 10001:10001 /workspace/project && chmod -R u+rwX /workspace/project' >/dev/null
  compose run --rm --no-deps --entrypoint sh app -c \
    'git -C /workspace/project init --initial-branch=main >/dev/null && git -C /workspace/project config user.name smoke && git -C /workspace/project config user.email smoke@example.invalid && git -C /workspace/project add -- docs/smoke.md && git -C /workspace/project commit -m base >/dev/null' >/dev/null
  compose up --detach --wait postgres >/dev/null
  bootstrap_compose run --rm --no-deps -T model-settings-key-init >/dev/null
  bootstrap_compose run --rm --no-deps -T migrate >/dev/null
  bootstrap_compose --profile workspace-runtime run --rm --no-deps -T local-model-runtime-credential-init >/dev/null
  bootstrap_compose --profile workspace-runtime run --rm --no-deps -T local-model-volume-init >/dev/null
  compose up --detach --no-deps --wait local-model-runtime >/dev/null
  compose up --detach --no-deps --wait app worker >/dev/null || fail 'API or Worker startup failed'
  compose up --detach --no-deps --wait app-model-relay worker-model-relay >/dev/null || fail 'model relay startup failed'
  RUNTIME_CONTAINER="$(compose ps -q local-model-runtime)"
  [[ -n "${RUNTIME_CONTAINER}" ]] || fail 'local model manager container is missing'
  authenticate

  log 'test 1/2: online, Chat local, Embedding local, both local, back online'
  target="$(save_mode online)"; activate_mode online "${target}" false
  assert_no_heavy_process 'both-online baseline'

  target="$(save_mode chat-local)"
  sleep 2
  assert_no_heavy_process 'save-only local draft'
  activate_mode chat-local "${target}" true
  assert_one_serve 'Chat-local mode'
  wait_exact_runtime_requirement '["smollm2:135m"]' 'Chat-local'

  target="$(save_mode embedding-local)"; activate_mode embedding-local "${target}" true
  assert_one_serve 'Embedding-local mode'
  wait_exact_runtime_requirement '["all-minilm:latest"]' 'Embedding-local'

  target="$(save_mode both-local)"; activate_mode both-local "${target}" true
  assert_one_serve 'both-local mode'
  wait_exact_runtime_requirement '["all-minilm:latest","smollm2:135m"]' 'both-local'
  models_before="$(model_identities)"
  grep -Fq "${CHAT_MODEL}|" <<<"${models_before}" || fail 'Chat model is missing from the managed volume'
  grep -Fq "${EMBEDDING_MODEL}|" <<<"${models_before}" || fail 'Embedding model is missing from the managed volume'

  target="$(save_mode online)"; activate_mode online "${target}" false
  assert_no_heavy_process 'switch back online'
  log 'test 1/2 passed'

  log 'test 2/2: one serve, exit, cached reuse, restart recovery, 60-second memory sample'
  target="$(save_mode both-local)"; activate_mode both-local "${target}" true
  assert_one_serve 'cached both-local mode'
  [[ "$(latest_activation_attempts)" == 0 ]] || fail 'cached models triggered a new pull attempt'
  models_after="$(model_identities)"
  [[ "${models_after}" == "${models_before}" ]] || fail 'cached model identities changed after reuse'
  attempts_before="$(operation_attempt_total)"
  child_epoch_before="$(compose exec -T postgres psql -Atq --username "${ZHIXU_POSTGRES_USER}" --dbname "${ZHIXU_POSTGRES_DB}" -c "SELECT child_epoch FROM ops.managed_ollama_runtime WHERE singleton=true" | tr -d '[:space:]')"

  compose restart local-model-runtime >/dev/null
  wait_runtime_health
  wait_converged both-local "${target}" true
  wait_one_serve 'manager restart recovery'
  attempts_after="$(operation_attempt_total)"
  [[ "${attempts_after}" == "${attempts_before}" ]] || fail 'manager restart consumed another pull attempt'
  models_after="$(model_identities)"
  [[ "${models_after}" == "${models_before}" ]] || fail 'manager restart changed cached model identities'
  child_epoch_after="$(compose exec -T postgres psql -Atq --username "${ZHIXU_POSTGRES_USER}" --dbname "${ZHIXU_POSTGRES_DB}" -c "SELECT child_epoch FROM ops.managed_ollama_runtime WHERE singleton=true" | tr -d '[:space:]')"
  (( child_epoch_after > child_epoch_before )) || fail 'manager restart did not create a fresh child generation'

  target="$(save_mode online)"; activate_mode online "${target}" false
  assert_no_heavy_process 'final online mode'
  sample_idle_memory
  log 'test 2/2 passed'
  log 'passed: all five modes converged, models were reused without pull, restart recovered one child, idle memory stayed within budget'
}

main "$@"
