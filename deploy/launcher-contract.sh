#!/usr/bin/env bash

set -Eeuo pipefail

readonly SCRIPT_DIR="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
readonly REPOSITORY_ROOT="$(cd -- "${SCRIPT_DIR}/.." && pwd)"
STATE_DIR=""

cleanup() {
  [[ -z "${STATE_DIR}" ]] || rm -rf -- "${STATE_DIR}"
}

fail() {
  printf '[launcher-contract] failed: %s\n' "$1" >&2
  exit 1
}

assert_log_contains() {
  grep -F -- "$1" "${ZHIXU_FAKE_DOCKER_LOG}" >/dev/null || fail "missing Docker invocation: $1"
}

assert_log_order() {
  local previous=0
  local pattern line
  for pattern in "$@"; do
    line="$(awk -v needle="${pattern}" -v previous="${previous}" 'NR > previous && index($0, needle) { print NR; exit }' "${ZHIXU_FAKE_DOCKER_LOG}")"
    [[ -n "${line}" ]] || fail "missing or out-of-order Docker invocation: ${pattern}"
    previous=${line}
  done
}

assert_no_proxy_start_before_commit() {
  local candidate_line commit_line
  candidate_line="$(awk 'index($0, "ZHIXU_MODEL_SETTINGS_PREPARED=true ZHIXU_WORKER_RESTART_POLICY=no compose") { print NR; exit }' "${ZHIXU_FAKE_DOCKER_LOG}")"
  commit_line="$(awk -v candidate="${candidate_line}" 'NR > candidate && index($0, "modelctl commit --rollout-id") { print NR; exit }' "${ZHIXU_FAKE_DOCKER_LOG}")"
  [[ -n "${candidate_line}" && -n "${commit_line}" ]] || fail "candidate or commit boundary is missing"
  if awk -v candidate="${candidate_line}" -v commit="${commit_line}" '
    NR > candidate && NR < commit && index($0, " up ") && index($0, "proxy") { found = 1 }
    END { exit found ? 0 : 1 }
  ' "${ZHIXU_FAKE_DOCKER_LOG}"; then
    fail "prepared candidates exposed proxy ingress before commit"
  fi
}

reset_log() {
  : >"${ZHIXU_FAKE_DOCKER_LOG}"
}

assert_mode_0600() {
  python3 - "$1" <<'PY'
import os
import stat
import sys

mode = stat.S_IMODE(os.stat(sys.argv[1]).st_mode)
if mode != 0o600:
    raise SystemExit(f"local .env mode is {mode:o}, want 600")
PY
}

main() {
  command -v docker >/dev/null 2>&1 || fail "docker is unavailable"
  command -v python3 >/dev/null 2>&1 || fail "python3 is unavailable"
  docker compose version >/dev/null 2>&1 || fail "Docker Compose v2 is unavailable"

  STATE_DIR="$(mktemp -d "${TMPDIR:-/tmp}/zhixu-launcher-contract.XXXXXX")" || fail "could not allocate state directory"
  STATE_DIR="$(cd -- "${STATE_DIR}" && pwd -P)" || fail "could not resolve state directory"
  trap cleanup EXIT INT TERM
  mkdir -p "${STATE_DIR}/fixture/deploy" "${STATE_DIR}/bin"
  cp "${REPOSITORY_ROOT}/zhixu" "${STATE_DIR}/fixture/zhixu"
  cp "${REPOSITORY_ROOT}/.env.example" "${STATE_DIR}/fixture/.env.example"
  cp "${REPOSITORY_ROOT}/deploy/compose.yml" "${STATE_DIR}/fixture/deploy/compose.yml"
  cp "${REPOSITORY_ROOT}/deploy/compose.static-models.yml" "${STATE_DIR}/fixture/deploy/compose.static-models.yml"
  cp "${REPOSITORY_ROOT}/deploy/compose_auth_check.py" "${STATE_DIR}/fixture/deploy/compose_auth_check.py"
  cp "${REPOSITORY_ROOT}/deploy/compose_runtime_check.py" "${STATE_DIR}/fixture/deploy/compose_runtime_check.py"

  docker compose --profile modelctl --project-name deploy -f "${REPOSITORY_ROOT}/deploy/compose.yml" \
    --env-file "${REPOSITORY_ROOT}/.env.example" config --format json >"${STATE_DIR}/compose-managed-model.json"
  docker compose --project-name deploy -f "${REPOSITORY_ROOT}/deploy/compose.yml" \
    -f "${REPOSITORY_ROOT}/deploy/compose.static-models.yml" \
    --env-file "${REPOSITORY_ROOT}/.env.example" config --format json >"${STATE_DIR}/compose-static-model.json"
  ZHIXU_CHAT_PROVIDER=openai-compatible docker compose --project-name deploy \
    -f "${REPOSITORY_ROOT}/deploy/compose.yml" -f "${REPOSITORY_ROOT}/deploy/compose.static-models.yml" \
    --env-file "${REPOSITORY_ROOT}/.env.example" config --format json >"${STATE_DIR}/compose-unsafe-static-model.json"

  cp "${SCRIPT_DIR}/testdata/launcher-fake-docker.sh" "${STATE_DIR}/bin/docker"
  cp "${SCRIPT_DIR}/testdata/launcher-fake-curl.sh" "${STATE_DIR}/bin/curl"
  chmod 0755 "${STATE_DIR}/bin/docker" "${STATE_DIR}/bin/curl"

  export PATH="${STATE_DIR}/bin:${PATH}"
  export ZHIXU_FAKE_MANAGED_COMPOSE_MODEL="${STATE_DIR}/compose-managed-model.json"
  export ZHIXU_FAKE_STATIC_COMPOSE_MODEL="${STATE_DIR}/compose-static-model.json"
  export ZHIXU_FAKE_DOCKER_LOG="${STATE_DIR}/docker.log"
  reset_log

  (
    cd "${STATE_DIR}/fixture"
    ./zhixu up >/dev/null
  )
  [[ -d "${STATE_DIR}/fixture/workspace" ]] || fail "up did not create workspace"
  [[ ! -e "${STATE_DIR}/fixture/.zhixu-launcher.lock" ]] || fail "successful up left its mutation lock behind"
  assert_mode_0600 "${STATE_DIR}/fixture/.env"
  assert_log_contains "--project-name deploy"
  assert_log_contains "--profile modelctl build"
  assert_log_contains "up --detach --wait postgres"
  assert_log_contains "run --rm --no-deps -T model-settings-key-init"
  assert_log_contains "run --rm --no-deps -T migrate"
  assert_log_contains "run --rm --no-deps -T modelctl recover --stale"
  assert_log_contains "up --detach --no-deps --wait app worker"
  assert_log_contains "up --detach --no-deps --wait app-model-relay worker-model-relay"
  assert_log_contains "up --detach --no-deps --wait proxy"

  reset_log
  export ZHIXU_FAKE_KEY_INIT_EXIT=42
  local key_init_exit=0
  set +e
  (
    cd "${STATE_DIR}/fixture"
    ./zhixu up >/dev/null 2>&1
  )
  key_init_exit=$?
  set -e
  unset ZHIXU_FAKE_KEY_INIT_EXIT
  [[ "${key_init_exit}" -eq 42 ]] || fail "model settings key initialization failure returned ${key_init_exit}, want 42"
  assert_log_contains "run --rm --no-deps -T model-settings-key-init"
  if grep -F -- "run --rm --no-deps -T migrate" "${ZHIXU_FAKE_DOCKER_LOG}" >/dev/null; then
    fail "model settings key initialization failure invoked migrations"
  fi
  if grep -F -- "run --rm --no-deps -T modelctl" "${ZHIXU_FAKE_DOCKER_LOG}" >/dev/null; then
    fail "model settings key initialization failure invoked modelctl"
  fi
  if grep -F -- "up --detach --no-deps --wait app worker" "${ZHIXU_FAKE_DOCKER_LOG}" >/dev/null; then
    fail "model settings key initialization failure started the application runtime"
  fi

  reset_log
  export ZHIXU_FAKE_MIGRATE_EXIT=41
  local migrate_exit=0
  set +e
  (
    cd "${STATE_DIR}/fixture"
    ./zhixu up >/dev/null 2>&1
  )
  migrate_exit=$?
  set -e
  unset ZHIXU_FAKE_MIGRATE_EXIT
  [[ "${migrate_exit}" -eq 41 ]] || fail "migration failure returned ${migrate_exit}, want 41"
  assert_log_contains "run --rm --no-deps -T migrate"
  if grep -F -- "run --rm --no-deps -T modelctl" "${ZHIXU_FAKE_DOCKER_LOG}" >/dev/null; then
    fail "migration failure invoked modelctl"
  fi
  if grep -F -- "up --detach --no-deps --wait app worker" "${ZHIXU_FAKE_DOCKER_LOG}" >/dev/null; then
    fail "migration failure started the application runtime"
  fi

  printf '\nZHIXU_CONTRACT_MARKER=preserved\n' >>"${STATE_DIR}/fixture/.env"
  chmod 0644 "${STATE_DIR}/fixture/.env"
  : >"${STATE_DIR}/fixture/workspace/preserved"
  reset_log
  (
    cd "${STATE_DIR}/fixture"
    ./zhixu up >/dev/null
  )
  grep -F -- "ZHIXU_CONTRACT_MARKER=preserved" "${STATE_DIR}/fixture/.env" >/dev/null || fail "repeated up replaced local .env"
  [[ -f "${STATE_DIR}/fixture/workspace/preserved" ]] || fail "repeated up replaced workspace content"
  assert_mode_0600 "${STATE_DIR}/fixture/.env"
  [[ ! -e "${STATE_DIR}/fixture/.zhixu-launcher.lock" ]] || fail "repeated up left its mutation lock behind"

  reset_log
  export ZHIXU_FAKE_MISSING_RELAY=worker-model-relay
  local relay_exit=0
  set +e
  (
    cd "${STATE_DIR}/fixture"
    ./zhixu up >/dev/null 2>&1
  )
  relay_exit=$?
  set -e
  unset ZHIXU_FAKE_MISSING_RELAY
  [[ "${relay_exit}" -ne 0 ]] || fail "up accepted a missing Worker model relay"
  [[ ! -e "${STATE_DIR}/fixture/.zhixu-launcher.lock" ]] || fail "relay readiness failure left its mutation lock behind"
  assert_log_order \
    "up --detach --no-deps --wait app-model-relay worker-model-relay" \
    "ps --status running --services app-model-relay worker-model-relay" \
    "rm --force --stop proxy firewall app-model-relay worker-model-relay"

  cp "${STATE_DIR}/fixture/.env" "${STATE_DIR}/fixture/.env.before-legacy-contract"
  printf '\nZHIXU_CHAT_PROVIDER=openai-compatible\n' >>"${STATE_DIR}/fixture/.env"
  reset_log
  if (
    cd "${STATE_DIR}/fixture"
    ZHIXU_FAKE_STATIC_COMPOSE_MODEL="${STATE_DIR}/compose-unsafe-static-model.json" \
      ./zhixu up >"${STATE_DIR}/legacy-rejection.out" 2>&1
  ); then
    fail "managed startup accepted legacy model environment values"
  fi
  mv "${STATE_DIR}/fixture/.env.before-legacy-contract" "${STATE_DIR}/fixture/.env"
  grep -F -- "legacy ZHIXU_CHAT_PROVIDER override is not accepted by managed Compose" \
    "${STATE_DIR}/legacy-rejection.out" >/dev/null \
    || fail "legacy model environment rejection did not explain the Settings migration"
  assert_log_contains "compose.static-models.yml --env-file ${STATE_DIR}/fixture/.env config --format json"
  if grep -F -- "--profile modelctl build" "${ZHIXU_FAKE_DOCKER_LOG}" >/dev/null \
    || grep -F -- " up --" "${ZHIXU_FAKE_DOCKER_LOG}" >/dev/null; then
    fail "legacy model environment rejection happened after a build or startup"
  fi

  reset_log
  if (
    cd "${STATE_DIR}/fixture"
    ZHIXU_COMPOSE_PROJECT_NAME=another-project ./zhixu down >"${STATE_DIR}/project-rejection.out" 2>&1
  ); then
    fail "down accepted a Compose project override"
  fi
  grep -F -- "cannot override the fixed Compose project deploy" "${STATE_DIR}/project-rejection.out" >/dev/null \
    || fail "project override rejection did not explain the fixed project"
  [[ ! -s "${ZHIXU_FAKE_DOCKER_LOG}" ]] || fail "project override rejection invoked Docker"
  if (
    cd "${STATE_DIR}/fixture"
    ZHIXU_COMPOSE_PROJECT_NAME=another-project ./zhixu reset --confirm DELETE >/dev/null 2>&1
  ); then
    fail "reset accepted a Compose project override"
  fi
  [[ ! -s "${ZHIXU_FAKE_DOCKER_LOG}" ]] || fail "reset project override rejection invoked Docker"

  local lock_dir="${STATE_DIR}/fixture/.zhixu-launcher.lock"
  mkdir "${lock_dir}"
  printf 'pid=%s\nstarted_at_epoch=%s\ncommand=up\n' "$$" "$(date +%s)" >"${lock_dir}/owner"
  (
    cd "${STATE_DIR}/fixture"
    ./zhixu status >/dev/null
    ./zhixu logs app >/dev/null
  ) || fail "read-only commands were blocked by the mutation lock"
  reset_log
  if (
    cd "${STATE_DIR}/fixture"
    ./zhixu down >/dev/null 2>&1
  ); then
    fail "down ignored an active mutation lock"
  fi
  [[ ! -s "${ZHIXU_FAKE_DOCKER_LOG}" ]] || fail "active-lock rejection invoked Docker"
  [[ -d "${lock_dir}" ]] || fail "active mutation lock was removed"
  rm -f "${lock_dir}/owner"
  rmdir "${lock_dir}"

  mkdir "${lock_dir}"
  printf 'invalid\n' >"${lock_dir}/owner"
  if (
    cd "${STATE_DIR}/fixture"
    ./zhixu down >/dev/null 2>&1
  ); then
    fail "down accepted corrupt mutation lock metadata"
  fi
  [[ -d "${lock_dir}" ]] || fail "corrupt mutation lock was removed"
  rm -f "${lock_dir}/owner"
  rmdir "${lock_dir}"

  local stale_pid
  ( : ) &
  stale_pid=$!
  wait "${stale_pid}"
  mkdir "${lock_dir}"
  printf 'pid=%s\nstarted_at_epoch=%s\ncommand=restart\n' "${stale_pid}" "$(date +%s)" >"${lock_dir}/owner"
  reset_log
  (
    cd "${STATE_DIR}/fixture"
    ./zhixu down >/dev/null
  )
  [[ ! -e "${lock_dir}" ]] || fail "stale mutation lock was not released after recovery"
  assert_log_contains "down --remove-orphans"
  if grep -F -- "--volumes" "${ZHIXU_FAKE_DOCKER_LOG}" >/dev/null; then
    fail "normal down requested volume deletion"
  fi

  reset_log
  if (
    cd "${STATE_DIR}/fixture"
    ./zhixu reset </dev/null >/dev/null 2>&1
  ); then
    fail "non-interactive reset succeeded without explicit confirmation"
  fi
  [[ ! -e "${lock_dir}" ]] || fail "rejected reset left its mutation lock behind"
  [[ ! -s "${ZHIXU_FAKE_DOCKER_LOG}" ]] || fail "unconfirmed reset invoked Docker"
  (
    cd "${STATE_DIR}/fixture"
    ./zhixu reset --confirm DELETE >/dev/null
  )
  [[ ! -e "${lock_dir}" ]] || fail "confirmed reset left its mutation lock behind"
  assert_log_contains "down --volumes --remove-orphans"

  reset_log
  (
    cd "${STATE_DIR}/fixture"
    ./zhixu restart >/dev/null
  )
  [[ ! -e "${lock_dir}" ]] || fail "successful restart left its mutation lock behind"
  assert_log_order \
    "modelctl begin" \
    "exec -T app /app/zhixu-modelctl preflight --role api --rollout-id rollout-contract-1" \
    "exec -T worker /app/zhixu-modelctl preflight --role worker --rollout-id rollout-contract-1" \
    "modelctl drain --rollout-id rollout-contract-1" \
    "modelctl wait-quiesced --rollout-id rollout-contract-1" \
    "stop proxy app-model-relay worker-model-relay app worker" \
    "ZHIXU_MODEL_SETTINGS_PREPARED=true ZHIXU_WORKER_RESTART_POLICY=no compose" \
    "modelctl wait-prepared --rollout-id rollout-contract-1" \
    "modelctl commit --rollout-id rollout-contract-1" \
    "up --detach --no-deps --force-recreate --wait app worker" \
    "up --detach --no-deps --wait app-model-relay worker-model-relay" \
    "run --rm --no-deps -T firewall" \
    "up --detach --no-deps --wait proxy"
  assert_no_proxy_start_before_commit
  if grep -F -- "ZHIXU_MODEL_SETTINGS_PREPARED=true" "${ZHIXU_FAKE_DOCKER_LOG}" | grep -F -- "--wait app worker" >/dev/null; then
    fail "prepared candidates used the active-runtime readiness gate before commit"
  fi
  grep -F -- "ZHIXU_MODEL_SETTINGS_PREPARED=false ZHIXU_WORKER_RESTART_POLICY=on-failure" \
    "${ZHIXU_FAKE_DOCKER_LOG}" | grep -F -- "up --detach --no-deps --force-recreate --wait app worker" >/dev/null \
    || fail "committed runtime was not recreated with the steady Worker restart policy"

  reset_log
  local signal_ready="${STATE_DIR}/signal-ready"
  local signal_output="${STATE_DIR}/signal-restart.out"
  rm -f "${signal_ready}"
  export ZHIXU_FAKE_BLOCK_MODELCTL=wait-prepared
  export ZHIXU_FAKE_BLOCK_READY_FILE="${signal_ready}"
  (
    cd "${STATE_DIR}/fixture"
    exec ./zhixu restart >"${signal_output}" 2>&1
  ) &
  local launcher_pid=$!
  local signal_ready_seen=0
  local attempt
  for ((attempt = 0; attempt < 100; attempt++)); do
    if [[ -f "${signal_ready}" ]]; then
      signal_ready_seen=1
      break
    fi
    sleep 0.05
  done
  if [[ "${signal_ready_seen}" -ne 1 ]]; then
    kill -TERM "${launcher_pid}" 2>/dev/null || true
    wait "${launcher_pid}" 2>/dev/null || true
    fail "signal fixture did not reach candidate preparation"
  fi
  kill -TERM "${launcher_pid}"
  local signal_exit=0
  set +e
  wait "${launcher_pid}"
  signal_exit=$?
  set -e
  unset ZHIXU_FAKE_BLOCK_MODELCTL ZHIXU_FAKE_BLOCK_READY_FILE
  [[ "${signal_exit}" -eq 143 ]] || fail "TERM restart exit=${signal_exit}, want 143"
  [[ ! -e "${lock_dir}" ]] || fail "TERM restart left its mutation lock behind"
  assert_log_order \
    "modelctl wait-prepared --rollout-id rollout-contract-1" \
    "modelctl abort --rollout-id rollout-contract-1" \
    "up --detach --no-deps --force-recreate --wait app worker"

  reset_log
  export ZHIXU_FAKE_FAIL_MODELCTL="wait-prepared"
  if (
    cd "${STATE_DIR}/fixture"
    ./zhixu restart >/dev/null 2>&1
  ); then
    fail "restart unexpectedly succeeded after candidate preparation failure"
  fi
  unset ZHIXU_FAKE_FAIL_MODELCTL
  [[ ! -e "${lock_dir}" ]] || fail "failed restart left its mutation lock behind"
  assert_log_order \
    "modelctl abort --rollout-id rollout-contract-1" \
    "up --detach --no-deps --force-recreate --wait app worker"
  grep -F -- "ZHIXU_WORKER_RESTART_POLICY=on-failure" "${ZHIXU_FAKE_DOCKER_LOG}" \
    | grep -F -- "up --detach --no-deps --force-recreate --wait app worker" >/dev/null \
    || fail "pre-commit recovery did not restore the steady Worker restart policy"

  reset_log
  export ZHIXU_FAKE_FAIL_MODELCTL="commit-after-persist"
  (
    cd "${STATE_DIR}/fixture"
    ./zhixu restart >/dev/null
  )
  unset ZHIXU_FAKE_FAIL_MODELCTL
  [[ ! -e "${lock_dir}" ]] || fail "committed queue recovery left its mutation lock behind"
  assert_log_order \
    "modelctl commit --rollout-id rollout-contract-1" \
    "modelctl recover --stale" \
    "up --detach --no-deps --force-recreate --wait app worker"
  if grep -F -- "modelctl abort --rollout-id rollout-contract-1" "${ZHIXU_FAKE_DOCKER_LOG}" >/dev/null; then
    fail "durable commit queue recovery was misclassified as pre-commit abort"
  fi

  reset_log
  export ZHIXU_FAKE_FAIL_MODELCTL="commit-before-persist"
  if (
    cd "${STATE_DIR}/fixture"
    ./zhixu restart >/dev/null 2>&1
  ); then
    fail "restart unexpectedly succeeded after pre-commit failure"
  fi
  unset ZHIXU_FAKE_FAIL_MODELCTL
  [[ ! -e "${lock_dir}" ]] || fail "pre-commit failure left its mutation lock behind"
  assert_log_order \
    "modelctl commit --rollout-id rollout-contract-1" \
    "modelctl abort --rollout-id rollout-contract-1" \
    "up --detach --no-deps --force-recreate --wait app worker"
  local commit_line
  commit_line="$(awk 'index($0, "modelctl commit --rollout-id rollout-contract-1") { print NR; exit }' "${ZHIXU_FAKE_DOCKER_LOG}")"
  if awk -v commit="${commit_line}" 'NR > commit && index($0, "modelctl recover --stale") { found = 1 } END { exit found ? 0 : 1 }' \
    "${ZHIXU_FAKE_DOCKER_LOG}"; then
    fail "pre-commit failure was misclassified as durable commit recovery"
  fi

  reset_log
  export ZHIXU_FAKE_FAIL_STEADY_ONCE_FILE="${STATE_DIR}/steady-recreate-failed"
  rm -f "${ZHIXU_FAKE_FAIL_STEADY_ONCE_FILE}"
  if (
    cd "${STATE_DIR}/fixture"
    ./zhixu restart >/dev/null 2>&1
  ); then
    fail "restart hid a post-commit steady-runtime failure"
  fi
  unset ZHIXU_FAKE_FAIL_STEADY_ONCE_FILE
  [[ -f "${STATE_DIR}/steady-recreate-failed" ]] || fail "post-commit failure fixture did not trigger"
  assert_log_order \
    "modelctl commit --rollout-id rollout-contract-1" \
    "up --detach --no-deps --force-recreate --wait app worker" \
    "stop proxy app-model-relay worker-model-relay app worker" \
    "up --detach --no-deps --force-recreate --wait app worker" \
    "up --detach --no-deps --wait app-model-relay worker-model-relay"

  printf '[launcher-contract] passed\n'
}

main "$@"
