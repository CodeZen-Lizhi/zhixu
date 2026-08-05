#!/usr/bin/env bash

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPOSITORY_ROOT="$(cd "${SCRIPT_DIR}/../../../.." && pwd)"
QA_STATE_DIR="$(mktemp -d /tmp/zhixu-gitsync-closeout.XXXXXX)"
QA_CONTAINER="zhixu-gitsync-closeout-$(date +%s)-$$"
QA_KEY_FILE="${QA_STATE_DIR}/git-sync.key"
QA_API_PID=""
QA_WORKER_PID=""
QA_VITE_PID=""

cleanup_closeout() {
  local qa_status=$?
  set +e
  if (( qa_status != 0 )); then
    echo "QA_FAILURE status=${qa_status}" >&2
    for qa_log in migrate api worker vite seed-run; do
      if [[ -f "${QA_STATE_DIR}/${qa_log}.log" ]]; then
        echo "--- ${qa_log}.log ---" >&2
        tail -n 40 "${QA_STATE_DIR}/${qa_log}.log" >&2
      fi
    done
  fi
  for qa_pid in "${QA_VITE_PID}" "${QA_WORKER_PID}" "${QA_API_PID}"; do
    if [[ -n "${qa_pid}" ]]; then
      kill -TERM -- "-${qa_pid}" >/dev/null 2>&1 || true
    fi
  done
  sleep 1
  for qa_pid in "${QA_VITE_PID}" "${QA_WORKER_PID}" "${QA_API_PID}"; do
    if [[ -n "${qa_pid}" ]]; then
      kill -KILL -- "-${qa_pid}" >/dev/null 2>&1 || true
    fi
  done
  docker rm -f "${QA_CONTAINER}" >/dev/null 2>&1 || true
  case "${QA_STATE_DIR}" in
    /tmp/zhixu-gitsync-closeout.*) rm -rf -- "${QA_STATE_DIR}" ;;
  esac
}
trap cleanup_closeout EXIT INT TERM

allocate_closeout_port() {
  python3 -c 'import socket; s=socket.socket(); s.bind(("127.0.0.1",0)); print(s.getsockname()[1]); s.close()'
}

wait_ready_closeout() {
  local qa_url="$1"
  local qa_label="$2"
  local qa_attempt
  for qa_attempt in $(seq 1 120); do
    if curl --silent --fail --connect-timeout 1 --max-time 2 "${qa_url}" >/dev/null 2>&1; then
      return 0
    fi
    sleep 0.5
  done
  echo "${qa_label} failed readiness" >&2
  return 1
}

create_closeout_workspace() {
  local qa_name="$1"
  local qa_root="$2"
  jq -cn --arg name "${qa_name}" --arg root "${qa_root}" \
    '{name:$name,root_path:$root,initialize_git:false}' |
    curl --silent --show-error --fail --request POST \
      --header 'Accept: application/json' --header 'Content-Type: application/json' \
      --data-binary @- "${QA_API_BASE}/api/v1/workspaces"
}

save_closeout_config() {
  local qa_workspace="$1"
  local qa_remote="$2"
  local qa_key="$3"
  jq -cn --arg remote "${qa_remote}" \
    '{expected_revision:0,remote_url:$remote,branch:"main",auto_sync:false,token:{action:"clear"}}' |
    curl --silent --show-error --fail --request PUT \
      --header 'Accept: application/json' --header 'Content-Type: application/json' \
      --header "Idempotency-Key: ${qa_key}" --data-binary @- \
      "${QA_API_BASE}/api/v1/workspaces/${qa_workspace}/git-remote"
}

QA_PG_USER="zhixu_closeout"
QA_PG_PASSWORD="closeout-local-only"
QA_PG_DATABASE="zhixu_closeout"

python3 -c 'import base64,secrets,sys; sys.stdout.write(base64.b64encode(secrets.token_bytes(32)).decode("ascii"))' >"${QA_KEY_FILE}"
chmod 0600 "${QA_KEY_FILE}"

docker run -d --name "${QA_CONTAINER}" \
  -e POSTGRES_USER="${QA_PG_USER}" \
  -e POSTGRES_PASSWORD="${QA_PG_PASSWORD}" \
  -e POSTGRES_DB="${QA_PG_DATABASE}" \
  -p 127.0.0.1::5432 pgvector/pgvector:pg16 >"${QA_STATE_DIR}/container.id"
QA_PG_PORT="$(docker port "${QA_CONTAINER}" 5432/tcp | awk -F: 'NR==1 {print $NF}')"
QA_DATABASE_URL="postgres://${QA_PG_USER}:${QA_PG_PASSWORD}@127.0.0.1:${QA_PG_PORT}/${QA_PG_DATABASE}?sslmode=disable"

for qa_attempt in $(seq 1 60); do
  if docker exec "${QA_CONTAINER}" pg_isready -U "${QA_PG_USER}" -d "${QA_PG_DATABASE}" >/dev/null 2>&1; then
    break
  fi
  sleep 0.5
done
docker exec "${QA_CONTAINER}" pg_isready -U "${QA_PG_USER}" -d "${QA_PG_DATABASE}" >/dev/null

(
  cd "${REPOSITORY_ROOT}"
  ZHIXU_DATABASE_URL="${QA_DATABASE_URL}" ZHIXU_ENVIRONMENT=development \
    ZHIXU_MIGRATION_INTEGRATION=1 \
    go test -tags=integration -count=1 -run '^TestRunWithConfiguredDatabase$' ./cmd/migrate
) >"${QA_STATE_DIR}/migrate.log" 2>&1

QA_API_PORT="$(allocate_closeout_port)"
QA_WORKER_PORT="$(allocate_closeout_port)"
QA_VITE_PORT="$(allocate_closeout_port)"
while [[ "${QA_WORKER_PORT}" == "${QA_API_PORT}" ]]; do QA_WORKER_PORT="$(allocate_closeout_port)"; done
while [[ "${QA_VITE_PORT}" == "${QA_API_PORT}" || "${QA_VITE_PORT}" == "${QA_WORKER_PORT}" ]]; do QA_VITE_PORT="$(allocate_closeout_port)"; done
QA_API_BASE="http://127.0.0.1:${QA_API_PORT}"
QA_VITE_BASE="http://127.0.0.1:${QA_VITE_PORT}"
QA_QUEUE="git-sync-closeout-${QA_API_PORT}"

(
  cd "${REPOSITORY_ROOT}"
  export ZHIXU_ENVIRONMENT=development
  export ZHIXU_HTTP_ADDR="127.0.0.1:${QA_API_PORT}"
  export ZHIXU_DATABASE_URL="${QA_DATABASE_URL}"
  export ZHIXU_GIT_SYNC_KEY_FILE="${QA_KEY_FILE}"
  export ZHIXU_AUTH_MODE=disabled
  export ZHIXU_WEB_ASSETS_DIR="${REPOSITORY_ROOT}/web/dist"
  export ZHIXU_WORKER_QUEUE="${QA_QUEUE}"
  export ZHIXU_CHAT_PROVIDER=disabled
  export ZHIXU_EMBEDDING_PROVIDER=disabled
  export ZHIXU_TOOL_RUNTIME_MODE=disabled
  export ZHIXU_WEB_FETCH_MODE=disabled
  export ZHIXU_TELEMETRY_MODE=disabled
  exec python3 -c 'import os; os.setsid(); os.execvp("go", ["go", "run", "./cmd/api"])'
) >"${QA_STATE_DIR}/api.log" 2>&1 &
QA_API_PID=$!

(
  cd "${REPOSITORY_ROOT}"
  export ZHIXU_ENVIRONMENT=development
  export ZHIXU_DATABASE_URL="${QA_DATABASE_URL}"
  export ZHIXU_GIT_SYNC_KEY_FILE="${QA_KEY_FILE}"
  export ZHIXU_AUTH_MODE=disabled
  export ZHIXU_WORKER_QUEUE="${QA_QUEUE}"
  export ZHIXU_WORKER_HEALTH_ADDR="127.0.0.1:${QA_WORKER_PORT}"
  export ZHIXU_CHAT_PROVIDER=disabled
  export ZHIXU_EMBEDDING_PROVIDER=disabled
  export ZHIXU_TOOL_RUNTIME_MODE=disabled
  export ZHIXU_WEB_FETCH_MODE=disabled
  export ZHIXU_TELEMETRY_MODE=disabled
  exec python3 -c 'import os; os.setsid(); os.execvp("go", ["go", "run", "./cmd/worker"])'
) >"${QA_STATE_DIR}/worker.log" 2>&1 &
QA_WORKER_PID=$!

(
  cd "${REPOSITORY_ROOT}"
  export VITE_RUNTIME_MODE=direct
  unset VITE_API_BASE_URL
  export VITE_API_PROXY_TARGET="${QA_API_BASE}"
  exec python3 -c 'import os,sys; os.setsid(); os.execvp("npm", ["npm", "run", "dev", "--prefix", "web", "--", "--host", "127.0.0.1", "--port", sys.argv[1]])' "${QA_VITE_PORT}"
) >"${QA_STATE_DIR}/vite.log" 2>&1 &
QA_VITE_PID=$!

wait_ready_closeout "${QA_API_BASE}/readyz" api
wait_ready_closeout "http://127.0.0.1:${QA_WORKER_PORT}/readyz" worker
wait_ready_closeout "${QA_VITE_BASE}/" vite

mkdir -p "${QA_STATE_DIR}/workspace-a" "${QA_STATE_DIR}/workspace-b"
git -C "${QA_STATE_DIR}/workspace-a" init -q -b main
git -C "${QA_STATE_DIR}/workspace-a" -c user.name=QA -c user.email=qa@example.invalid \
  commit -q --allow-empty -m "qa init a"
git -C "${QA_STATE_DIR}/workspace-b" init -q -b main
git -C "${QA_STATE_DIR}/workspace-b" -c user.name=QA -c user.email=qa@example.invalid \
  commit -q --allow-empty -m "qa init b"

QA_WORKSPACE_A_JSON="$(create_closeout_workspace "Closeout Workspace A" "${QA_STATE_DIR}/workspace-a")"
QA_WORKSPACE_A="$(jq -r '.id' <<<"${QA_WORKSPACE_A_JSON}")"
docker exec "${QA_CONTAINER}" psql -v ON_ERROR_STOP=1 -U "${QA_PG_USER}" -d "${QA_PG_DATABASE}" \
  -c "UPDATE core.workspace SET status='inactive',version=version+1,updated_at=clock_timestamp() WHERE id='${QA_WORKSPACE_A}';" \
  >"${QA_STATE_DIR}/prepare-workspace-b.log" 2>&1
QA_WORKSPACE_B_JSON="$(create_closeout_workspace "Closeout Workspace B" "${QA_STATE_DIR}/workspace-b")"
QA_WORKSPACE_B="$(jq -r '.id' <<<"${QA_WORKSPACE_B_JSON}")"
docker exec "${QA_CONTAINER}" psql -v ON_ERROR_STOP=1 -U "${QA_PG_USER}" -d "${QA_PG_DATABASE}" -c "
BEGIN;
UPDATE core.workspace SET status='inactive',version=version+1,updated_at=clock_timestamp() WHERE id='${QA_WORKSPACE_B}';
UPDATE core.workspace SET status='active',version=version+1,updated_at=clock_timestamp() WHERE id='${QA_WORKSPACE_A}';
COMMIT;" >"${QA_STATE_DIR}/restore-workspace-a.log" 2>&1

QA_CONFIG_A_JSON="$(save_closeout_config "${QA_WORKSPACE_A}" "https://github.com/codexu/note-gen-a.git" "closeout-config-a")"
QA_CONFIG_B_JSON="$(save_closeout_config "${QA_WORKSPACE_B}" "https://github.com/codexu/note-gen-b.git" "closeout-config-b")"

QA_RUN_ID="a7050000-0000-4000-8000-000000000001"
docker exec "${QA_CONTAINER}" psql -v ON_ERROR_STOP=1 -U "${QA_PG_USER}" -d "${QA_PG_DATABASE}" -c "
INSERT INTO ops.git_sync_run(
  id,workspace_id,config_revision,remote_url,branch,trigger,retry_of_run_id,
  idempotency_key,request_hash,status,direction,failure_class,error_code,retryable,
  expected_head_oid,expected_remote_oid,verified_head_oid,verified_remote_oid,
  changed_files,index_status,index_error_code,index_retryable,index_version_id,
  attempt_count,version,created_at,updated_at,completed_at
) VALUES (
  '${QA_RUN_ID}','${QA_WORKSPACE_A}',1,'https://github.com/codexu/note-gen-a.git','main','MANUAL',NULL,
  'closeout-pending-run','aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa',
  'PENDING','UNKNOWN','NONE','',false,NULL,NULL,NULL,NULL,
  '[]'::jsonb,'NOT_REQUIRED','',false,NULL,0,1,clock_timestamp(),clock_timestamp(),NULL
);" >"${QA_STATE_DIR}/seed-run.log" 2>&1

curl --silent --show-error --fail \
  "${QA_API_BASE}/api/v1/workspaces/${QA_WORKSPACE_A}/git-sync" >"${QA_STATE_DIR}/status-a.json"
curl --silent --show-error --fail \
  "${QA_API_BASE}/api/v1/workspaces/${QA_WORKSPACE_B}/git-sync" >"${QA_STATE_DIR}/status-b.json"
jq -e --arg wa "${QA_WORKSPACE_A}" '.config.workspace_id==$wa and .current_run.direction=="UNKNOWN"' \
  "${QA_STATE_DIR}/status-a.json" >/dev/null
jq -e --arg wb "${QA_WORKSPACE_B}" '.config.workspace_id==$wb and .current_run==null' \
  "${QA_STATE_DIR}/status-b.json" >/dev/null

echo "QA_READY"
echo "VITE_BASE=${QA_VITE_BASE}"
echo "API_BASE=${QA_API_BASE}"
echo "WORKSPACE_A=${QA_WORKSPACE_A}"
echo "WORKSPACE_B=${QA_WORKSPACE_B}"
echo "RUN_ID=${QA_RUN_ID}"
echo "CONTAINER=${QA_CONTAINER}"
echo "PG_USER=${QA_PG_USER}"
echo "PG_DATABASE=${QA_PG_DATABASE}"
echo "STATE_DIR=${QA_STATE_DIR}"

while true; do sleep 5; done
