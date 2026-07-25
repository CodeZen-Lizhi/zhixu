#!/usr/bin/env bash

set -Eeuo pipefail

readonly SCRIPT_DIR="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
readonly REPOSITORY_ROOT="$(cd -- "${SCRIPT_DIR}/.." && pwd)"
readonly COMPOSE_FILE="${SCRIPT_DIR}/compose.yml"
readonly ENV_FILE="${REPOSITORY_ROOT}/.env.example"
readonly GO_IMAGE="${ZHIXU_COMPOSE_TOOL_SMOKE_GO_IMAGE:-golang:1.25.4-bookworm}"

STATE_DIR=""
PROJECT_NAME=""

log() {
  printf '[compose-tool-smoke] %s\n' "$1"
}

fail() {
  printf '[compose-tool-smoke] failed: %s\n' "$1" >&2
  exit 1
}

require_command() {
  command -v "$1" >/dev/null 2>&1 || fail "required command is unavailable: $1"
}

random_hex() {
  python3 - "$1" <<'PY'
import secrets
import sys

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

compose() {
  docker compose --project-name "${PROJECT_NAME}" -f "${COMPOSE_FILE}" --env-file "${ENV_FILE}" "$@"
}

cleanup() {
  local exit_code=$?
  trap - EXIT INT TERM
  if [[ -n "${PROJECT_NAME}" ]]; then
    compose down --volumes --remove-orphans >/dev/null 2>&1 || true
  fi
  if [[ -n "${STATE_DIR}" ]]; then
    chmod -R u+rwX "${STATE_DIR}" >/dev/null 2>&1 || true
    rm -rf -- "${STATE_DIR}" >/dev/null 2>&1 || true
  fi
  exit "${exit_code}"
}

main() {
  require_command bash
  require_command docker
  require_command python3
  docker compose version >/dev/null 2>&1 || fail "Docker Compose v2 is unavailable"

  STATE_DIR="$(mktemp -d "${TMPDIR:-/tmp}/zhixu-compose-tool-smoke.XXXXXX" 2>/dev/null)" || fail "could not allocate disposable state"
  trap cleanup EXIT INT TERM

  local run_id database_url
  run_id="$(random_hex 6)"
  PROJECT_NAME="zhixu-tool-smoke-${run_id}"
  mkdir -p "${STATE_DIR}/workspace/project/docs"
  printf '# Compose Tool Smoke\n' >"${STATE_DIR}/workspace/project/docs/tool-smoke.md"
  chmod -R a+rwX "${STATE_DIR}/workspace"

  export ZHIXU_HTTP_PORT="$(allocate_port)"
  export ZHIXU_POSTGRES_DB="zhixu_tool_smoke"
  export ZHIXU_POSTGRES_USER="zhixu_tool_smoke"
	export ZHIXU_POSTGRES_PASSWORD="smoke_${run_id}_$(random_hex 12)"
	export ZHIXU_AUTH_BOOTSTRAP_TOKEN="auth_${run_id}_$(random_hex 24)"
  export ZHIXU_WORKSPACE_ROOT="${STATE_DIR}/workspace"
  export ZHIXU_TOOL_RUNTIME_MODE="enabled"
  export ZHIXU_WEB_FETCH_MODE="disabled"
  export ZHIXU_CHAT_PROVIDER="disabled"
  export ZHIXU_WORKER_QUEUE="${ZHIXU_WORKER_QUEUE:-workflow}"
  export ZHIXU_EMBEDDING_PROVIDER="disabled"
  export ZHIXU_EMBEDDING_BASE_URL=""
  export ZHIXU_EMBEDDING_API_KEY=""
  export ZHIXU_EMBEDDING_MODEL=""
  export ZHIXU_EMBEDDING_DIMENSIONS="0"

  log "validating and building the disposable Compose stack"
  compose config --quiet
  compose build
  compose run --rm --no-deps --user root --entrypoint sh app -c \
    'chown -R 10001:10001 /workspace/project && chmod -R u+rwX /workspace/project'
  compose run --rm --no-deps --entrypoint sh app -c \
    'git -C /workspace/project init --initial-branch=main >/dev/null && git -C /workspace/project config user.name "ZHIXU Tool Smoke" && git -C /workspace/project config user.email "tool-smoke@example.invalid" && git -C /workspace/project add -- docs/tool-smoke.md && git -C /workspace/project commit -m "base" >/dev/null'
  log "starting API, Worker and PostgreSQL with Tool Runtime enabled"
  compose up --detach --wait
  compose exec -T app wget -q -O /dev/null http://127.0.0.1:8080/readyz
  compose exec -T worker wget -q -O /dev/null http://127.0.0.1:8081/readyz

  database_url="postgres://${ZHIXU_POSTGRES_USER}:${ZHIXU_POSTGRES_PASSWORD}@postgres:5432/${ZHIXU_POSTGRES_DB}?sslmode=disable"
  log "executing a real persisted Tool Workflow through River on the Compose network"
  docker run --rm \
    --network "${PROJECT_NAME}_default" \
    --volume "${REPOSITORY_ROOT}:/src:ro" \
    --volume "${STATE_DIR}/workspace:/workspace:ro" \
    --workdir /src \
    --env "ZHIXU_COMPOSE_TOOL_SMOKE=1" \
    --env "ZHIXU_TEST_DATABASE_URL=${database_url}" \
    --env "ZHIXU_TEST_WORKSPACE_ROOT=/workspace/project" \
    --env "ZHIXU_WORKER_QUEUE=${ZHIXU_WORKER_QUEUE}" \
    "${GO_IMAGE}" \
    sh -c 'go test -mod=vendor -race -tags=integration -count=1 -p 1 -run TestComposeWorkerConsumesPersistedReadGitStatusToolWorkflow ./cmd/worker'

  log "executing Safe Writeback fault recovery and Tool audit smoke"
  docker run --rm \
    --network "${PROJECT_NAME}_default" \
    --volume "${REPOSITORY_ROOT}:/src:ro" \
    --workdir /src \
    --env "ZHIXU_TEST_DATABASE_URL=${database_url}" \
    "${GO_IMAGE}" \
    sh -c 'go test -mod=vendor -race -tags=integration -count=1 -p 1 -run "TestWritebackSagaRealFaultSmoke|TestSafeWritebackWorkflowNodePostgreSQLGitFilesystemSmoke" ./internal/changecontrol/application'

  log "Tool Runtime, real River Tool Workflow, and Safe Writeback fault/audit smoke passed"
}

main "$@"
