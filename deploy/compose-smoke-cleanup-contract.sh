#!/usr/bin/env bash

set -Eeuo pipefail

readonly SCRIPT_DIR="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
STATE_DIR=""

source "${SCRIPT_DIR}/compose-smoke-cleanup.sh"

log() {
  printf '[compose-smoke-cleanup-contract] %s\n' "$1"
}

fail() {
  printf '[compose-smoke-cleanup-contract] failed: %s\n' "$1" >&2
  exit 1
}

compose() {
  docker compose --project-name "${PROJECT_NAME}" -f contract.yml "$@"
}

harness_cleanup() {
  local exit_code=$?
  local cleanup_exit=0
  local cleanup_name="${ZHIXU_CONTRACT_CLEANUP_NAME:-${PROJECT_NAME}}"
  trap - EXIT HUP INT TERM
  cleanup_compose_smoke_project_images "${cleanup_name}" || cleanup_exit=$?
  if [[ "${exit_code}" -ne 0 ]]; then
    exit "${exit_code}"
  fi
  exit "${cleanup_exit}"
}

run_harness() {
  readonly PROJECT_NAME="${ZHIXU_CONTRACT_PROJECT_NAME:-zhixu-auth-smoke-a1b2c3d4e5f6}"
  trap harness_cleanup EXIT
  trap 'exit 129' HUP
  trap 'exit 130' INT
  trap 'exit 143' TERM
  case "${1:-}" in
    success) return 0 ;;
    error) exit 23 ;;
    signal)
      case "${ZHIXU_CONTRACT_SIGNAL:-}" in
        HUP|INT|TERM) ;;
        *) exit 64 ;;
      esac
      kill -s "${ZHIXU_CONTRACT_SIGNAL}" "$$"
      sleep 1
      exit 70
      ;;
    *) exit 64 ;;
  esac
}

cleanup_contract_state() {
  [[ -z "${STATE_DIR}" ]] || rm -rf -- "${STATE_DIR}"
}

run_case() {
  local mode=$1
  shift
  : >"${ZHIXU_FAKE_DOCKER_LOG}"
  set +e
  env "$@" bash "$0" --harness "${mode}" >"${STATE_DIR}/case.out" 2>&1
  CASE_EXIT=$?
  set -e
}

assert_cleanup_invocation() {
  local project_name=$1
  grep -F -- "compose --project-name ${project_name} -f contract.yml down --volumes --remove-orphans --rmi local" \
    "${ZHIXU_FAKE_DOCKER_LOG}" >/dev/null || fail "project-scoped Compose cleanup was not invoked"
  grep -F -- "image ls --quiet --filter label=com.docker.compose.project=${project_name}" \
    "${ZHIXU_FAKE_DOCKER_LOG}" >/dev/null || fail "project image cleanup was not verified"
  grep -F -- "image ls --quiet --filter reference=${project_name}-*" \
    "${ZHIXU_FAKE_DOCKER_LOG}" >/dev/null || fail "project image names were not verified"
}

assert_docker_not_invoked() {
  [[ ! -s "${ZHIXU_FAKE_DOCKER_LOG}" ]] || fail "rejected cleanup invoked Docker"
}

assert_runtime_startup_contract() {
  local smoke_script=$1
  local script_path="${SCRIPT_DIR}/${smoke_script}"
  local previous_line=0
  local startup_step line
  local -a startup_steps=()
  if [[ "${smoke_script}" == compose-rag-smoke.sh ]]; then
    startup_steps=(
      'up --detach --wait postgres'
      'run --rm --no-deps -T model-settings-key-init'
      'run --rm --no-deps -T migrate'
      'up --detach --no-deps --wait rag-model-fixture'
      '  activate_workspace_grant'
    )
  else
    startup_steps=(
      'up --detach --wait postgres'
      'run --rm --no-deps -T model-settings-key-init'
      'run --rm --no-deps -T migrate'
      'up --detach --no-deps --wait app worker'
      'up --detach --no-deps --wait app-model-relay worker-model-relay'
      'run --rm --no-deps -T firewall'
      'up --detach --no-deps --wait proxy'
    )
  fi

  if grep -Eq 'up[[:space:]]+--detach[[:space:]]+--wait([[:space:]]*$|[[:space:]]*[>/])' "${script_path}"; then
    fail "${smoke_script} can still start every Compose service with --wait"
  fi
  if grep -Eq 'up[[:space:]].*(model-settings-key-init|migrate|firewall)' "${script_path}"; then
    fail "${smoke_script} starts a one-shot service through Compose up"
  fi
  for startup_step in "${startup_steps[@]}"; do
    line="$(grep -n -m 1 -F -- "${startup_step}" "${script_path}" | cut -d: -f1)"
    [[ -n "${line}" ]] || fail "${smoke_script} does not run startup step: ${startup_step}"
    (( line > previous_line )) || fail "${smoke_script} startup step is out of order: ${startup_step}"
    previous_line=${line}
  done
}

main() {
  command -v bash >/dev/null 2>&1 || fail "bash is unavailable"
  STATE_DIR="$(mktemp -d "${TMPDIR:-/tmp}/zhixu-compose-cleanup-contract.XXXXXX")" \
    || fail "could not allocate contract state"
  trap cleanup_contract_state EXIT
  mkdir -p "${STATE_DIR}/bin"
  cp "${SCRIPT_DIR}/testdata/compose-smoke-cleanup-fake-docker.sh" "${STATE_DIR}/bin/docker"
  chmod 0755 "${STATE_DIR}/bin/docker"
  export PATH="${STATE_DIR}/bin:${PATH}"
  export ZHIXU_FAKE_DOCKER_LOG="${STATE_DIR}/docker.log"

  local smoke_kind project_name
  for smoke_kind in auth rag search tool; do
    project_name="zhixu-${smoke_kind}-smoke-a1b2c3d4e5f6"
    run_case success ZHIXU_CONTRACT_PROJECT_NAME="${project_name}"
    [[ "${CASE_EXIT}" -eq 0 ]] || fail "${smoke_kind} namespace cleanup returned ${CASE_EXIT}"
    assert_cleanup_invocation "${project_name}"
  done

  run_case error
  [[ "${CASE_EXIT}" -eq 23 ]] || fail "smoke failure status was not preserved"
  assert_cleanup_invocation "zhixu-auth-smoke-a1b2c3d4e5f6"

  run_case success ZHIXU_FAKE_COMPOSE_DOWN_FAIL=1
  [[ "${CASE_EXIT}" -eq 1 ]] || fail "successful smoke ignored Compose cleanup failure"

  run_case error ZHIXU_FAKE_COMPOSE_DOWN_FAIL=1
  [[ "${CASE_EXIT}" -eq 23 ]] || fail "cleanup failure replaced the original smoke failure"

  run_case success ZHIXU_FAKE_LABELED_IMAGES_REMAIN=1
  [[ "${CASE_EXIT}" -eq 1 ]] || fail "remaining project-labeled images did not fail cleanup"

  run_case success ZHIXU_FAKE_NAMED_IMAGES_REMAIN=1
  [[ "${CASE_EXIT}" -eq 1 ]] || fail "remaining project-named images did not fail cleanup"

  run_case success ZHIXU_FAKE_LABEL_LS_FAIL=1
  [[ "${CASE_EXIT}" -eq 1 ]] || fail "image label verification failure did not fail cleanup"

  run_case success ZHIXU_FAKE_NAME_LS_FAIL=1
  [[ "${CASE_EXIT}" -eq 1 ]] || fail "image name verification failure did not fail cleanup"

  local invalid_project
  for invalid_project in deploy zhixu-auth-smoke-a zhixu-auth-smoke-a1b2c3d4e5f60 zhixu-auth-smoke-A1B2C3D4E5F6; do
    run_case success ZHIXU_CONTRACT_PROJECT_NAME="${invalid_project}"
    [[ "${CASE_EXIT}" -eq 1 ]] || fail "cleanup accepted broad project namespace ${invalid_project}"
    assert_docker_not_invoked
  done

  run_case success \
    ZHIXU_CONTRACT_PROJECT_NAME=zhixu-auth-smoke-a1b2c3d4e5f6 \
    ZHIXU_CONTRACT_CLEANUP_NAME=zhixu-rag-smoke-a1b2c3d4e5f6
  [[ "${CASE_EXIT}" -eq 1 ]] || fail "cleanup accepted a mismatched Compose project binding"
  assert_docker_not_invoked

  local signal_name expected_exit
  for signal_name in HUP INT TERM; do
    case "${signal_name}" in
      HUP) expected_exit=129 ;;
      INT) expected_exit=130 ;;
      TERM) expected_exit=143 ;;
    esac
    run_case signal ZHIXU_CONTRACT_SIGNAL="${signal_name}"
    [[ "${CASE_EXIT}" -eq "${expected_exit}" ]] || fail "${signal_name} cleanup returned ${CASE_EXIT}, want ${expected_exit}"
    assert_cleanup_invocation "zhixu-auth-smoke-a1b2c3d4e5f6"
  done

  run_case signal ZHIXU_CONTRACT_SIGNAL=TERM ZHIXU_FAKE_COMPOSE_DOWN_FAIL=1
  [[ "${CASE_EXIT}" -eq 143 ]] || fail "cleanup failure replaced TERM status"

  local smoke_script
  for smoke_script in compose-auth-smoke.sh compose-search-smoke.sh compose-tool-smoke.sh compose-rag-smoke.sh; do
    grep -F -- 'source "${SCRIPT_DIR}/compose-smoke-cleanup.sh"' "${SCRIPT_DIR}/${smoke_script}" >/dev/null \
      || fail "${smoke_script} does not use the shared cleanup contract"
    grep -F -- "cleanup_compose_smoke_project_images" "${SCRIPT_DIR}/${smoke_script}" >/dev/null \
      || fail "${smoke_script} does not remove project-local images"
    grep -F -- "trap cleanup EXIT" "${SCRIPT_DIR}/${smoke_script}" >/dev/null \
      || fail "${smoke_script} does not bind cleanup to every exit path"
    local trap_contract
    for trap_contract in "trap 'exit 129' HUP" "trap 'exit 130' INT" "trap 'exit 143' TERM"; do
      grep -F -- "${trap_contract}" "${SCRIPT_DIR}/${smoke_script}" >/dev/null \
        || fail "${smoke_script} does not preserve ${trap_contract} semantics"
    done
    assert_runtime_startup_contract "${smoke_script}"
  done

  printf '[compose-smoke-cleanup-contract] passed\n'
}

if [[ "${1:-}" == "--harness" ]]; then
  shift
  run_harness "$@"
else
  main "$@"
fi
