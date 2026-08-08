#!/usr/bin/env bash

set -Eeuo pipefail

readonly SCRIPT_DIR="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
readonly REPOSITORY_ROOT="$(cd -- "${SCRIPT_DIR}/.." && pwd)"
STATE_DIR=""
STATE_PARENT=""
PORT_HOLDER_PID=""
PORT_HOLDER_PORT=""

fail() {
  printf '[launcher-contract] failed: %s\n' "$1" >&2
  exit 1
}

cleanup() {
  if [[ -n "${PORT_HOLDER_PID}" ]]; then
    kill -TERM "${PORT_HOLDER_PID}" 2>/dev/null || true
    wait "${PORT_HOLDER_PID}" 2>/dev/null || true
  fi
  if [[ -n "${STATE_DIR}" && -n "${STATE_PARENT}" && -d "${STATE_DIR}" \
    && "${STATE_DIR}" == "${STATE_PARENT}"/zhixu-launcher-contract.* ]]; then
    rm -rf -- "${STATE_DIR}"
  fi
}

start_port_holder() {
  local port_file="${STATE_DIR}/held-port" attempt
  rm -f -- "${port_file}"
  python3 - "${port_file}" <<'PY' &
import signal
import socket
import sys

listener = socket.socket(socket.AF_INET, socket.SOCK_STREAM)
listener.bind(("127.0.0.1", 0))
listener.listen(1)
with open(sys.argv[1], "w", encoding="ascii") as target:
    target.write(str(listener.getsockname()[1]))
    target.flush()
while True:
    signal.pause()
PY
  PORT_HOLDER_PID=$!
  for ((attempt = 0; attempt < 100; attempt++)); do
    [[ -s "${port_file}" ]] && break
    sleep 0.02
  done
  [[ -s "${port_file}" ]] || fail "could not reserve a test HTTP port"
  PORT_HOLDER_PORT="$(cat "${port_file}")"
}

stop_port_holder() {
  [[ -n "${PORT_HOLDER_PID}" ]] || return
  kill -TERM "${PORT_HOLDER_PID}" 2>/dev/null || true
  wait "${PORT_HOLDER_PID}" 2>/dev/null || true
  PORT_HOLDER_PID=""
  PORT_HOLDER_PORT=""
}

assert_log_contains() {
  grep -F -- "$1" "${ZHIXU_FAKE_DOCKER_LOG}" >/dev/null || fail "missing Docker invocation: $1"
}

assert_control_log_contains() {
  grep -F -- "$1" "${ZHIXU_FAKE_WORKSPACECTL_LOG}" >/dev/null || fail "missing workspacectl invocation: $1"
}

assert_control_log_not_contains() {
  if grep -F -- "$1" "${ZHIXU_FAKE_WORKSPACECTL_LOG}" >/dev/null; then
    fail "unexpected workspacectl invocation: $1"
  fi
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

json_field() {
  python3 - "$1" "$2" <<'PY'
import json
import sys

with open(sys.argv[1], "r", encoding="utf-8") as source:
    value = json.load(source)
field = value[sys.argv[2]]
if not isinstance(field, str):
    raise SystemExit("field is not a string")
print(field)
PY
}

assert_selection_shape() {
  python3 - "$1" "$2" <<'PY'
import datetime
import json
import re
import sys
import uuid

with open(sys.argv[1], "r", encoding="utf-8") as source:
    value = json.load(source)
expected = {"schema_version", "canonical_root", "workspace_id", "root_fingerprint", "committed_at"}
if set(value) != expected or value["schema_version"] != 1:
    raise SystemExit("selection shape is invalid")
if value["canonical_root"] != sys.argv[2]:
    raise SystemExit("selection root is invalid")
if str(uuid.UUID(value["workspace_id"])) != value["workspace_id"]:
    raise SystemExit("selection Workspace ID is invalid")
if re.fullmatch(r"[0-9a-f]{64}", value["root_fingerprint"]) is None:
    raise SystemExit("selection fingerprint is invalid")
datetime.datetime.fromisoformat(value["committed_at"].removesuffix("Z") + "+00:00")
PY
}

assert_control_instance_shape() {
  python3 - "$1" <<'PY'
import sys
import uuid

with open(sys.argv[1], "r", encoding="ascii") as source:
    raw = source.read()
if len(raw) != 37 or not raw.endswith("\n"):
    raise SystemExit("control instance identity shape is invalid")
value = raw[:-1]
parsed = uuid.UUID(value)
if parsed.int == 0 or str(parsed) != value:
    raise SystemExit("control instance identity is not canonical")
PY
}

grant_root() {
  python3 - "$1" <<'PY'
import json
import sys

with open(sys.argv[1], "r", encoding="utf-8") as source:
    value = json.load(source)
app = value["services"]["app"]
worker = value["services"]["worker"]
root = app["environment"]["ZHIXU_WORKSPACE_GRANTED_ROOT"]
if worker["environment"]["ZHIXU_WORKSPACE_GRANTED_ROOT"] != root:
    raise SystemExit("grant roots differ")
print(root)
PY
}

reset_logs() {
  : >"${ZHIXU_FAKE_DOCKER_LOG}"
  : >"${ZHIXU_FAKE_WORKSPACECTL_LOG}"
}

expect_failure() {
  local expected=$1
  shift
  local exit_code=0
  set +e
  "$@" >/dev/null 2>&1
  exit_code=$?
  set -e
  [[ "${exit_code}" -eq "${expected}" ]] || fail "command returned ${exit_code}, want ${expected}"
}

main() {
  command -v docker >/dev/null 2>&1 || fail "docker is unavailable"
  command -v python3 >/dev/null 2>&1 || fail "python3 is unavailable"
  docker compose version >/dev/null 2>&1 || fail "Docker Compose v2 is unavailable"

  STATE_PARENT="$(cd -- "${TMPDIR:-/tmp}" && pwd -P)" || fail "could not resolve the temporary directory"
  STATE_DIR="$(mktemp -d "${STATE_PARENT}/zhixu-launcher-contract.XXXXXX")" || fail "could not allocate fixture"
  STATE_DIR="$(cd -- "${STATE_DIR}" && pwd -P)"
  trap cleanup EXIT INT TERM
  mkdir -p "${STATE_DIR}/fixture/deploy" "${STATE_DIR}/bin" "${STATE_DIR}/workspace A/知识" "${STATE_DIR}/workspace B"
  cp "${REPOSITORY_ROOT}/zhixu" "${STATE_DIR}/fixture/zhixu"
  cp "${REPOSITORY_ROOT}/.env.example" "${STATE_DIR}/fixture/.env.example"
  cp "${REPOSITORY_ROOT}/deploy/compose.yml" "${STATE_DIR}/fixture/deploy/compose.yml"
  cp "${REPOSITORY_ROOT}/deploy/compose.static-models.yml" "${STATE_DIR}/fixture/deploy/compose.static-models.yml"
  cp "${REPOSITORY_ROOT}/deploy/Dockerfile" "${STATE_DIR}/fixture/deploy/Dockerfile"
  cp "${REPOSITORY_ROOT}/deploy/compose_auth_check.py" "${STATE_DIR}/fixture/deploy/compose_auth_check.py"
  cp "${REPOSITORY_ROOT}/deploy/compose_runtime_check.py" "${STATE_DIR}/fixture/deploy/compose_runtime_check.py"
  cp "${REPOSITORY_ROOT}/deploy/compose_workspace_check.py" "${STATE_DIR}/fixture/deploy/compose_workspace_check.py"

  docker compose --profile workspace-runtime --profile modelctl --project-name zhixu \
    -f "${REPOSITORY_ROOT}/deploy/compose.yml" --env-file "${REPOSITORY_ROOT}/.env.example" \
    config --format json >"${STATE_DIR}/compose-managed.json"
  docker compose --profile workspace-runtime --project-name zhixu \
    -f "${REPOSITORY_ROOT}/deploy/compose.yml" -f "${REPOSITORY_ROOT}/deploy/compose.static-models.yml" \
    --env-file "${REPOSITORY_ROOT}/.env.example" config --format json >"${STATE_DIR}/compose-static.json"

  cp "${SCRIPT_DIR}/testdata/launcher-fake-docker.sh" "${STATE_DIR}/bin/docker"
  chmod 0755 "${STATE_DIR}/bin/docker"
  export PATH="${STATE_DIR}/bin:${PATH}"
  export ZHIXU_FAKE_MANAGED_COMPOSE_MODEL="${STATE_DIR}/compose-managed.json"
  export ZHIXU_FAKE_STATIC_COMPOSE_MODEL="${STATE_DIR}/compose-static.json"
  export ZHIXU_FAKE_DOCKER_LOG="${STATE_DIR}/docker.log"
  export ZHIXU_FAKE_WORKSPACECTL_LOG="${STATE_DIR}/workspacectl.log"
  reset_logs

  local workspace_a="${STATE_DIR}/workspace A/知识"
  local workspace_b="${STATE_DIR}/workspace B"
  local selection="${STATE_DIR}/fixture/.zhixu/workspace-selection"
  local grant="${STATE_DIR}/fixture/.zhixu/workspace-grant.yml"
  local control_instance="${STATE_DIR}/fixture/.zhixu/control-instance-id"

  expect_failure 1 bash -c "cd '${STATE_DIR}/fixture' && ./zhixu up"
  [[ ! -s "${ZHIXU_FAKE_DOCKER_LOG}" ]] || fail "up without selection invoked Docker"
  expect_failure 1 bash -c "cd '${STATE_DIR}/fixture' && ./zhixu up --workspace relative/path"
  [[ ! -s "${ZHIXU_FAKE_DOCKER_LOG}" ]] || fail "relative Workspace validation invoked Docker"
  expect_failure 1 bash -c "cd '${STATE_DIR}/fixture' && ./zhixu up --workspace /"
  [[ ! -s "${ZHIXU_FAKE_DOCKER_LOG}" ]] || fail "dangerous Workspace validation invoked Docker"

  local up_output workspace_a_id first_selection first_control_instance
  up_output="$(cd "${STATE_DIR}/fixture" && ./zhixu up --workspace "${workspace_a}")"
  grep -F -- "ready: http://127.0.0.1:8080/" <<<"${up_output}" >/dev/null || fail "first up did not print the fixed URL"
  [[ "${up_output}" != *"#"* ]] || fail "first up printed a URL fragment"
  [[ -d "${STATE_DIR}/fixture/.zhixu" ]] || fail "up did not create launcher state"
  [[ -f "${selection}" ]] || fail "up did not commit Workspace selection"
  [[ -f "${grant}" ]] || fail "up did not create the Workspace grant"
  [[ -f "${control_instance}" ]] || fail "up did not create the stable control instance identity"
  [[ -x "${STATE_DIR}/fixture/.zhixu/bundle/zhixu-workspacectl" ]] || fail "native workspacectl was not exported"
  [[ ! -e "${STATE_DIR}/fixture/.zhixu/bundle/web" ]] || fail "native bundle still contains Web assets"
  [[ -z "$(find "${STATE_DIR}/fixture/.zhixu" -maxdepth 1 -type f \( -name '*.pid' -o -name '*.log' \) -print -quit)" ]] \
    || fail "up created a resident-process state file"
  assert_mode "${STATE_DIR}/fixture/.zhixu" 0700
  assert_mode "${selection}" 0600
  assert_mode "${grant}" 0600
  assert_mode "${control_instance}" 0600
  assert_mode "${STATE_DIR}/fixture/.env" 0600
  assert_selection_shape "${selection}" "${workspace_a}"
  assert_control_instance_shape "${control_instance}"
  [[ "$(grant_root "${grant}")" == "${workspace_a}" ]] || fail "grant does not use the exact A root"
  workspace_a_id="$(json_field "${selection}" workspace_id)"
  first_selection="$(cat "${selection}")"
  first_control_instance="$(cat "${control_instance}")"
  assert_log_contains "buildx build --target workspace-control-bundle"
  assert_log_contains "build model-settings-key-init migrate modelctl app worker app-model-relay worker-model-relay firewall proxy"
  assert_log_contains "up --detach --wait postgres"
  assert_log_contains "run --rm --no-deps -T model-settings-key-init"
  assert_log_contains "run --rm --no-deps -T migrate"
  assert_log_contains "modelctl recover --stale"
  assert_log_contains "port postgres 5432"
  assert_log_contains "ps --format json app worker proxy"
  assert_log_contains "ZHIXU_APP_RESTART_POLICY=on-failure ZHIXU_WORKER_RESTART_POLICY=on-failure"
  assert_control_log_contains "switch --workspace-root ${workspace_a} --idempotency-key present"
  assert_control_log_contains "--control-instance-id present"
  assert_control_log_not_contains "--initialize-git"
  if grep -F -- "zhixu_dev_only_change_me" "${ZHIXU_FAKE_DOCKER_LOG}" "${ZHIXU_FAKE_WORKSPACECTL_LOG}" >/dev/null; then
    fail "database password leaked to argv or logs"
  fi
  if grep -E 'nohup|bootstrap-token|#' "${ZHIXU_FAKE_DOCKER_LOG}" "${ZHIXU_FAKE_WORKSPACECTL_LOG}" >/dev/null; then
    fail "resident delivery state appeared in launcher invocations"
  fi

  reset_logs
  (cd "${STATE_DIR}/fixture" && ./zhixu up >/dev/null)
  assert_control_log_contains "switch --workspace-root ${workspace_a} --idempotency-key present"
  assert_control_log_not_contains "--initialize-git"
  [[ "$(json_field "${selection}" workspace_id)" == "${workspace_a_id}" ]] \
    || fail "same-root desired operation changed the Workspace ID"
  [[ "$(cat "${control_instance}")" == "${first_control_instance}" ]] \
    || fail "same-root up changed the stable control instance identity"

  reset_logs
  start_port_holder
  export ZHIXU_HTTP_PORT="${PORT_HOLDER_PORT}"
  export ZHIXU_FAKE_APP_ENDPOINT=unavailable
  expect_failure 1 bash -c "cd '${STATE_DIR}/fixture' && ./zhixu up"
  unset ZHIXU_HTTP_PORT ZHIXU_FAKE_APP_ENDPOINT
  stop_port_holder
  [[ ! -s "${ZHIXU_FAKE_WORKSPACECTL_LOG}" ]] || fail "occupied Web port still invoked workspacectl"
  [[ "$(json_field "${selection}" workspace_id)" == "${workspace_a_id}" ]] \
    || fail "occupied Web port changed the saved selection"

  reset_logs
  export ZHIXU_FAKE_WORKSPACECTL_WORKSPACE_ID="550e8400-e29b-41d4-a716-446655440099"
  expect_failure 1 bash -c "cd '${STATE_DIR}/fixture' && ./zhixu up"
  unset ZHIXU_FAKE_WORKSPACECTL_WORKSPACE_ID
  [[ "$(json_field "${selection}" workspace_id)" == "${workspace_a_id}" ]] \
    || fail "mismatched desired-root result overwrote the saved selection"
  [[ ! -e "${grant}" ]] || fail "mismatched desired-root result left a grant active"
  assert_log_contains "stop proxy app-model-relay worker-model-relay firewall app worker"

  reset_logs
  export ZHIXU_FAKE_WORKSPACECTL_INVALID_RESULT=1
  expect_failure 1 bash -c "cd '${STATE_DIR}/fixture' && ./zhixu up"
  unset ZHIXU_FAKE_WORKSPACECTL_INVALID_RESULT
  [[ "$(json_field "${selection}" workspace_id)" == "${workspace_a_id}" ]] \
    || fail "invalid workspacectl result overwrote the saved selection"
  [[ ! -e "${grant}" ]] || fail "invalid workspacectl result left a grant active"

  reset_logs
  (cd "${STATE_DIR}/fixture" && ./zhixu restart >/dev/null)
  assert_control_log_contains "switch --workspace-root ${workspace_a} --idempotency-key present"
  assert_control_log_not_contains "--initialize-git"

  reset_logs
  (cd "${STATE_DIR}/fixture" && ./zhixu up --workspace "${workspace_a}" --initialize-git >/dev/null)
  assert_control_log_contains "switch --workspace-root ${workspace_a} --idempotency-key present"
  assert_control_log_not_contains "--initialize-git"

  reset_logs
  (cd "${STATE_DIR}/fixture" && ./zhixu workspace switch "${workspace_b}" --initialize-git >/dev/null)
  assert_control_log_contains "switch --workspace-root ${workspace_b} --idempotency-key present"
  assert_control_log_contains "--initialize-git --compose-file"
  assert_selection_shape "${selection}" "${workspace_b}"
  [[ "$(json_field "${selection}" workspace_id)" != "${workspace_a_id}" ]] || fail "A and B reused one Workspace ID"
  [[ "$(grant_root "${grant}")" == "${workspace_b}" ]] || fail "switch did not replace the exact grant"

  local before_failed_switch
  before_failed_switch="$(cat "${selection}")"
  reset_logs
  export ZHIXU_FAKE_WORKSPACECTL_FAIL_ROOT="${workspace_a}"
  expect_failure 44 bash -c "cd '${STATE_DIR}/fixture' && ./zhixu workspace switch '${workspace_a}'"
  unset ZHIXU_FAKE_WORKSPACECTL_FAIL_ROOT
  [[ "$(cat "${selection}")" == "${before_failed_switch}" ]] || fail "failed switch overwrote the saved selection"
  [[ "$(grant_root "${grant}")" == "${workspace_b}" ]] || fail "failed switch replaced the active grant"

  reset_logs
  export ZHIXU_FAKE_RUNTIME_READY=0
  expect_failure 1 bash -c "cd '${STATE_DIR}/fixture' && ./zhixu up"
  unset ZHIXU_FAKE_RUNTIME_READY
  [[ "$(cat "${selection}")" == "${before_failed_switch}" ]] || fail "unready runtime overwrote the saved selection"
  [[ ! -e "${grant}" ]] || fail "unready runtime left a grant active"

  printf '\nZHIXU_WORKSPACE_ROOT=/legacy/ignored\n' >>"${STATE_DIR}/fixture/.env"
  reset_logs
  local legacy_output
  legacy_output="$(cd "${STATE_DIR}/fixture" && ./zhixu up)"
  grep -F -- "ZHIXU_WORKSPACE_ROOT is obsolete" <<<"${legacy_output}" >/dev/null || fail "legacy root did not produce a migration warning"
  if grep -F -- "/legacy/ignored" "${ZHIXU_FAKE_DOCKER_LOG}" "${ZHIXU_FAKE_WORKSPACECTL_LOG}" >/dev/null; then
    fail "legacy Workspace root entered an invocation"
  fi

  reset_logs
  export ZHIXU_FAKE_POSTGRES_ENDPOINT="0.0.0.0:49123"
  expect_failure 1 bash -c "cd '${STATE_DIR}/fixture' && ./zhixu up"
  unset ZHIXU_FAKE_POSTGRES_ENDPOINT
  [[ ! -s "${ZHIXU_FAKE_WORKSPACECTL_LOG}" ]] || fail "unsafe PostgreSQL endpoint invoked workspacectl"

  reset_logs
  export ZHIXU_FAKE_KEY_INIT_EXIT=42
  expect_failure 42 bash -c "cd '${STATE_DIR}/fixture' && ./zhixu up"
  unset ZHIXU_FAKE_KEY_INIT_EXIT
  [[ ! -s "${ZHIXU_FAKE_WORKSPACECTL_LOG}" ]] || fail "base failure still invoked workspacectl"

  reset_logs
  (cd "${STATE_DIR}/fixture" && ./zhixu status >/dev/null)
  assert_log_contains "ps"
  reset_logs
  (cd "${STATE_DIR}/fixture" && ./zhixu logs postgres >/dev/null)
  assert_log_contains "logs --follow --tail 200 postgres"
  reset_logs
  expect_failure 1 bash -c "cd '${STATE_DIR}/fixture' && ./zhixu logs unknown-service"
  [[ ! -s "${ZHIXU_FAKE_DOCKER_LOG}" ]] || fail "unknown logs request invoked Docker"

  chmod 0644 "${selection}"
  reset_logs
  expect_failure 1 bash -c "cd '${STATE_DIR}/fixture' && ./zhixu up"
  [[ ! -s "${ZHIXU_FAKE_DOCKER_LOG}" ]] || fail "permissive selection invoked Docker"
  chmod 0600 "${selection}"
  mv "${selection}" "${selection}.real"
  ln -s "${selection}.real" "${selection}"
  reset_logs
  expect_failure 1 bash -c "cd '${STATE_DIR}/fixture' && ./zhixu up"
  [[ ! -s "${ZHIXU_FAKE_DOCKER_LOG}" ]] || fail "selection symlink invoked Docker"
  rm "${selection}"
  mv "${selection}.real" "${selection}"

  chmod 0644 "${control_instance}"
  reset_logs
  expect_failure 1 bash -c "cd '${STATE_DIR}/fixture' && ./zhixu up"
  [[ ! -s "${ZHIXU_FAKE_WORKSPACECTL_LOG}" ]] || fail "permissive control identity invoked workspacectl"
  chmod 0600 "${control_instance}"
  mv "${control_instance}" "${control_instance}.real"
  ln -s "${control_instance}.real" "${control_instance}"
  reset_logs
  expect_failure 1 bash -c "cd '${STATE_DIR}/fixture' && ./zhixu up"
  [[ ! -s "${ZHIXU_FAKE_WORKSPACECTL_LOG}" ]] || fail "control identity symlink invoked workspacectl"
  rm "${control_instance}"
  mv "${control_instance}.real" "${control_instance}"
  printf 'not-a-canonical-uuid\n' >"${control_instance}"
  reset_logs
  expect_failure 1 bash -c "cd '${STATE_DIR}/fixture' && ./zhixu up"
  [[ ! -s "${ZHIXU_FAKE_WORKSPACECTL_LOG}" ]] || fail "invalid control identity invoked workspacectl"
  printf '%s\n' "${first_control_instance}" >"${control_instance}"
  chmod 0600 "${control_instance}"

  mkdir -m 0700 "${STATE_DIR}/fixture/.zhixu/launcher.lock"
  printf 'pid=%s\ncommand=up\n' "$$" >"${STATE_DIR}/fixture/.zhixu/launcher.lock/owner"
  chmod 0600 "${STATE_DIR}/fixture/.zhixu/launcher.lock/owner"
  reset_logs
  expect_failure 1 bash -c "cd '${STATE_DIR}/fixture' && ./zhixu up"
  [[ ! -s "${ZHIXU_FAKE_DOCKER_LOG}" ]] || fail "live launcher lock invoked Docker"
  rm -f "${STATE_DIR}/fixture/.zhixu/launcher.lock/owner"
  rmdir "${STATE_DIR}/fixture/.zhixu/launcher.lock"

  printf '{invalid-derived-grant\n' >"${grant}"
  chmod 0600 "${grant}"
  reset_logs
  (cd "${STATE_DIR}/fixture" && ./zhixu down >/dev/null)
  [[ -f "${selection}" ]] || fail "down forgot the saved Workspace"
  [[ ! -e "${grant}" ]] || fail "down retained a corrupt derived grant"
  assert_log_contains "--profile workspace-runtime --profile modelctl down --remove-orphans"
  if grep -F -- "--volumes" "${ZHIXU_FAKE_DOCKER_LOG}" >/dev/null; then
    fail "normal down requested volume deletion"
  fi

  reset_logs
  (cd "${STATE_DIR}/fixture" && ./zhixu up >/dev/null)
  assert_control_log_contains "switch --workspace-root ${workspace_b} --idempotency-key present"
  assert_control_log_not_contains "--initialize-git"
  [[ -f "${grant}" ]] || fail "up after down did not restore the grant"

  reset_logs
  expect_failure 1 bash -c "cd '${STATE_DIR}/fixture' && ./zhixu reset </dev/null"
  [[ ! -s "${ZHIXU_FAKE_DOCKER_LOG}" ]] || fail "unconfirmed reset invoked Docker"
  [[ -f "${selection}" && -f "${grant}" ]] || fail "unconfirmed reset changed local state"
  (cd "${STATE_DIR}/fixture" && ./zhixu reset --confirm DELETE >/dev/null)
  assert_log_contains "down --volumes --remove-orphans"
  [[ ! -e "${selection}" && ! -e "${grant}" ]] || fail "confirmed reset retained selection or grant"
  [[ "$(cat "${control_instance}")" == "${first_control_instance}" ]] \
    || fail "reset changed the stable control instance identity"
  expect_failure 1 bash -c "cd '${STATE_DIR}/fixture' && ./zhixu restart"

  reset_logs
  expect_failure 1 bash -c "cd '${STATE_DIR}/fixture' && ZHIXU_COMPOSE_PROJECT_NAME=other ./zhixu down"
  [[ ! -s "${ZHIXU_FAKE_DOCKER_LOG}" ]] || fail "project override rejection invoked Docker"

  [[ "${first_selection}" == *"${workspace_a_id}"* ]] || fail "initial selection did not retain its identity"
  printf '[launcher-contract] passed\n'
}

main "$@"
