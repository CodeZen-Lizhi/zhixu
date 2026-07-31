#!/usr/bin/env bash

set -Eeuo pipefail

readonly SCRIPT_DIR="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
readonly REPOSITORY_ROOT="$(cd -- "${SCRIPT_DIR}/.." && pwd)"
STATE_DIR=""

fail() {
  printf '[launcher-contract] failed: %s\n' "$1" >&2
  exit 1
}

cleanup() {
  if [[ -n "${STATE_DIR}" ]]; then
    local pid_file="${STATE_DIR}/fixture/.zhixu/controller.pid" pid=""
    if [[ -f "${pid_file}" ]]; then
      pid="$(tr -d '\r\n' <"${pid_file}")"
      [[ "${pid}" =~ ^[1-9][0-9]*$ ]] && kill -TERM "${pid}" 2>/dev/null || true
    fi
    rm -rf -- "${STATE_DIR}"
  fi
}

assert_log_contains() {
  grep -F -- "$1" "${ZHIXU_FAKE_DOCKER_LOG}" >/dev/null || fail "missing Docker invocation: $1"
}

assert_mode() {
  python3 - "$1" "$2" <<'PY'
import os
import stat
import sys

mode = stat.S_IMODE(os.stat(sys.argv[1]).st_mode)
expected = int(sys.argv[2], 8)
if mode != expected:
    raise SystemExit(f"{sys.argv[1]} mode={mode:o}, want {expected:o}")
PY
}

reset_log() {
  : >"${ZHIXU_FAKE_DOCKER_LOG}"
}

main() {
  command -v docker >/dev/null 2>&1 || fail "docker is unavailable"
  command -v python3 >/dev/null 2>&1 || fail "python3 is unavailable"
  docker compose version >/dev/null 2>&1 || fail "Docker Compose v2 is unavailable"

  STATE_DIR="$(mktemp -d "${TMPDIR:-/tmp}/zhixu-launcher-contract.XXXXXX")" || fail "could not allocate fixture"
  STATE_DIR="$(cd -- "${STATE_DIR}" && pwd -P)"
  trap cleanup EXIT INT TERM
  mkdir -p "${STATE_DIR}/fixture/deploy" "${STATE_DIR}/fixture/deploy/testdata" "${STATE_DIR}/bin"
  cp "${REPOSITORY_ROOT}/zhixu" "${STATE_DIR}/fixture/zhixu"
  cp "${REPOSITORY_ROOT}/.env.example" "${STATE_DIR}/fixture/.env.example"
  cp "${REPOSITORY_ROOT}/deploy/compose.yml" "${STATE_DIR}/fixture/deploy/compose.yml"
  cp "${REPOSITORY_ROOT}/deploy/compose.static-models.yml" "${STATE_DIR}/fixture/deploy/compose.static-models.yml"
  cp "${REPOSITORY_ROOT}/deploy/Dockerfile" "${STATE_DIR}/fixture/deploy/Dockerfile"
  cp "${REPOSITORY_ROOT}/deploy/compose_auth_check.py" "${STATE_DIR}/fixture/deploy/compose_auth_check.py"
  cp "${REPOSITORY_ROOT}/deploy/compose_runtime_check.py" "${STATE_DIR}/fixture/deploy/compose_runtime_check.py"
  cp "${REPOSITORY_ROOT}/deploy/compose_workspace_check.py" "${STATE_DIR}/fixture/deploy/compose_workspace_check.py"

  docker compose --profile workspace-runtime --profile modelctl --project-name deploy \
    -f "${REPOSITORY_ROOT}/deploy/compose.yml" --env-file "${REPOSITORY_ROOT}/.env.example" \
    config --format json >"${STATE_DIR}/compose-managed.json"
  docker compose --profile workspace-runtime --project-name deploy \
    -f "${REPOSITORY_ROOT}/deploy/compose.yml" -f "${REPOSITORY_ROOT}/deploy/compose.static-models.yml" \
    --env-file "${REPOSITORY_ROOT}/.env.example" config --format json >"${STATE_DIR}/compose-static.json"

  cp "${SCRIPT_DIR}/testdata/launcher-fake-docker.sh" "${STATE_DIR}/bin/docker"
  cp "${SCRIPT_DIR}/testdata/launcher-fake-curl.sh" "${STATE_DIR}/bin/curl"
  chmod 0755 "${STATE_DIR}/bin/docker" "${STATE_DIR}/bin/curl"
  export PATH="${STATE_DIR}/bin:${PATH}"
  export ZHIXU_FAKE_MANAGED_COMPOSE_MODEL="${STATE_DIR}/compose-managed.json"
  export ZHIXU_FAKE_STATIC_COMPOSE_MODEL="${STATE_DIR}/compose-static.json"
  export ZHIXU_FAKE_DOCKER_LOG="${STATE_DIR}/docker.log"
  reset_log

  local up_output token first_pid
  up_output="$(cd "${STATE_DIR}/fixture" && ./zhixu up)"
  grep -E 'ready: http://127\.0\.0\.1:8080/#control=[A-Za-z0-9_-]{32,128}' <<<"${up_output}" >/dev/null \
    || fail "up did not print a one-time fragment link"
  token="$(sed -n 's/.*#control=\([A-Za-z0-9_-]*\).*/\1/p' <<<"${up_output}")"
  [[ -n "${token}" ]] || fail "could not extract one-time token"
  [[ ! -e "${STATE_DIR}/fixture/workspace" ]] || fail "up created a fallback workspace directory"
  [[ -d "${STATE_DIR}/fixture/.zhixu" ]] || fail "up did not create Controller state"
  [[ -x "${STATE_DIR}/fixture/.zhixu/bundle/zhixu-host-controller" ]] || fail "native Controller was not exported"
  [[ -f "${STATE_DIR}/fixture/.zhixu/bundle/web/index.html" ]] || fail "web bundle was not exported"
  assert_mode "${STATE_DIR}/fixture/.zhixu" 0700
  assert_mode "${STATE_DIR}/fixture/.env" 0600
  assert_mode "${STATE_DIR}/fixture/.zhixu/controller.pid" 0600
  assert_mode "${STATE_DIR}/fixture/.zhixu/controller.log" 0600
  first_pid="$(tr -d '\r\n' <"${STATE_DIR}/fixture/.zhixu/controller.pid")"
  kill -0 "${first_pid}" 2>/dev/null || fail "Controller process is not running"
	assert_log_contains "buildx build --target host-controller-bundle"
	assert_log_contains "build model-settings-key-init migrate modelctl app worker app-model-relay worker-model-relay firewall proxy"
	assert_log_contains "up --detach --wait postgres"
  assert_log_contains "run --rm --no-deps -T model-settings-key-init"
  assert_log_contains "run --rm --no-deps -T migrate"
  assert_log_contains "modelctl recover --stale"
  if grep -E 'up .*app|up .*worker' "${ZHIXU_FAKE_DOCKER_LOG}" >/dev/null; then
    fail "zero-grant up started a Workspace runtime"
  fi
  if grep -F -- "${token}" "${ZHIXU_FAKE_DOCKER_LOG}" "${STATE_DIR}/fixture/.zhixu/controller.log" >/dev/null; then
    fail "one-time token leaked to Docker argv or Controller logs"
  fi
  if grep -F -- "zhixu_dev_only_change_me" "${ZHIXU_FAKE_DOCKER_LOG}" "${STATE_DIR}/fixture/.zhixu/controller.log" >/dev/null; then
    fail "database password leaked to Docker argv or Controller logs"
  fi

  reset_log
  local second_output second_pid
  second_output="$(cd "${STATE_DIR}/fixture" && ./zhixu up)"
  second_pid="$(tr -d '\r\n' <"${STATE_DIR}/fixture/.zhixu/controller.pid")"
  [[ "${second_pid}" != "${first_pid}" ]] || fail "repeated up reused the old Controller process"
  kill -0 "${first_pid}" 2>/dev/null && fail "repeated up left the old Controller running"
  kill -0 "${second_pid}" 2>/dev/null || fail "repeated up did not start a new Controller"
  [[ "${second_output}" != *"#control=${token}"* ]] || fail "repeated up reused the bootstrap token"
  assert_log_contains "stop proxy app-model-relay worker-model-relay app worker"
  assert_log_contains "rm --force --stop proxy firewall app-model-relay worker-model-relay app worker"

  printf '\nZHIXU_WORKSPACE_ROOT=/legacy/ignored\n' >>"${STATE_DIR}/fixture/.env"
  reset_log
  local legacy_output
  legacy_output="$(cd "${STATE_DIR}/fixture" && ./zhixu up)"
  grep -F -- "ZHIXU_WORKSPACE_ROOT is obsolete" <<<"${legacy_output}" >/dev/null || fail "legacy setting did not produce a migration warning"
  if grep -F -- "/legacy/ignored" "${ZHIXU_FAKE_DOCKER_LOG}" >/dev/null; then
    fail "legacy Workspace root entered Docker argv"
  fi

  reset_log
  export ZHIXU_FAKE_KEY_INIT_EXIT=42
  local key_exit=0
  set +e
  (cd "${STATE_DIR}/fixture" && ./zhixu up >/dev/null 2>&1)
  key_exit=$?
  set -e
  unset ZHIXU_FAKE_KEY_INIT_EXIT
  [[ "${key_exit}" -eq 42 ]] || fail "key initialization failure returned ${key_exit}, want 42"
  if grep -F -- "buildx build" "${ZHIXU_FAKE_DOCKER_LOG}" >/dev/null; then
    fail "base failure still built or started the Controller"
  fi

  (cd "${STATE_DIR}/fixture" && ./zhixu up >/dev/null)

  reset_log
  (cd "${STATE_DIR}/fixture" && ./zhixu status >/dev/null)
  assert_log_contains "ps"

  chmod 0644 "${STATE_DIR}/fixture/.zhixu/controller.pid"
  if (cd "${STATE_DIR}/fixture" && ./zhixu status >/dev/null 2>&1); then
    fail "status accepted a permissive Controller PID file"
  fi
  chmod 0600 "${STATE_DIR}/fixture/.zhixu/controller.pid"

  printf 'services: {}\n' >"${STATE_DIR}/fixture/.zhixu/workspace-grant.yml"
  chmod 0600 "${STATE_DIR}/fixture/.zhixu/workspace-grant.yml"
  reset_log
  (cd "${STATE_DIR}/fixture" && ./zhixu down >/dev/null)
  [[ ! -e "${STATE_DIR}/fixture/.zhixu/controller.pid" ]] || fail "down left the Controller PID file"
  [[ ! -e "${STATE_DIR}/fixture/.zhixu/workspace-grant.yml" ]] || fail "down retained a stale active grant"
  assert_log_contains "down --remove-orphans"
  if grep -F -- "--volumes" "${ZHIXU_FAKE_DOCKER_LOG}" >/dev/null; then
    fail "normal down requested volume deletion"
  fi

  reset_log
  local reset_exit=0
  set +e
  (cd "${STATE_DIR}/fixture" && ./zhixu reset </dev/null >/dev/null 2>&1)
  reset_exit=$?
  set -e
  [[ "${reset_exit}" -ne 0 ]] || fail "non-interactive reset succeeded without confirmation"
  [[ ! -s "${ZHIXU_FAKE_DOCKER_LOG}" ]] || fail "unconfirmed reset invoked Docker"
  (cd "${STATE_DIR}/fixture" && ./zhixu reset --confirm DELETE >/dev/null)
  assert_log_contains "down --volumes --remove-orphans"

  reset_log
  if (cd "${STATE_DIR}/fixture" && ZHIXU_COMPOSE_PROJECT_NAME=other ./zhixu down >/dev/null 2>&1); then
    fail "down accepted a Compose project override"
  fi
  [[ ! -s "${ZHIXU_FAKE_DOCKER_LOG}" ]] || fail "project override rejection invoked Docker"

  reset_log
  local restart_output restart_pid
  restart_output="$(cd "${STATE_DIR}/fixture" && ./zhixu restart)"
  restart_pid="$(tr -d '\r\n' <"${STATE_DIR}/fixture/.zhixu/controller.pid")"
  kill -0 "${restart_pid}" 2>/dev/null || fail "restart did not start the Controller"
  grep -E 'ready: http://127\.0\.0\.1:8080/#control=[A-Za-z0-9_-]{32,128}' <<<"${restart_output}" >/dev/null \
    || fail "restart did not print a new one-time fragment link"
  assert_log_contains "port postgres 5432"
  assert_log_contains "buildx build --target host-controller-bundle"

  printf '[launcher-contract] passed\n'
}

main "$@"
