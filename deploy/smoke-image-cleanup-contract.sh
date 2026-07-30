#!/usr/bin/env bash

set -Eeuo pipefail

readonly SCRIPT_DIR="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
STATE_DIR=""

fail() {
  printf '[smoke-image-cleanup-contract] failed: %s\n' "$1" >&2
  exit 1
}

cleanup() {
  [[ -z "${STATE_DIR}" ]] || rm -rf -- "${STATE_DIR}"
}

run_cleanup() {
  : >"${ZHIXU_FAKE_DOCKER_LOG}"
  : >"${ZHIXU_FAKE_REMOVED_REFS}"
  set +e
  bash "${SCRIPT_DIR}/cleanup-smoke-images.sh" "$@" >"${STATE_DIR}/cleanup.out" 2>&1
  CLEANUP_EXIT=$?
  set -e
}

main() {
  STATE_DIR="$(mktemp -d "${TMPDIR:-/tmp}/zhixu-smoke-image-guard.XXXXXX")" \
    || fail "could not allocate contract state"
  trap cleanup EXIT
  mkdir -p "${STATE_DIR}/bin"
  cp "${SCRIPT_DIR}/testdata/smoke-image-cleanup-fake-docker.sh" "${STATE_DIR}/bin/docker"
  chmod 0755 "${STATE_DIR}/bin/docker"
  export PATH="${STATE_DIR}/bin:${PATH}"
  export ZHIXU_FAKE_DOCKER_LOG="${STATE_DIR}/docker.log"
  export ZHIXU_FAKE_REMOVED_REFS="${STATE_DIR}/removed"
  export ZHIXU_FAKE_IMAGE_INVENTORY="${STATE_DIR}/images"

  local auth_id="sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
  local rag_id="sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
  local referenced_id="sha256:cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc"
  local wrong_label_id="sha256:dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd"
  local missing_label_id="sha256:eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee"
  export ZHIXU_FAKE_REFERENCED_IMAGE_ID="${referenced_id}"
  printf '%b\n' \
    "zhixu-auth-smoke-a1b2c3d4e5f6-app\tlatest\t${auth_id}\tzhixu-auth-smoke-a1b2c3d4e5f6\tapp" \
    "zhixu-auth-smoke-a1b2c3d4e5f6-app\tarchive\t${auth_id}\tzhixu-auth-smoke-a1b2c3d4e5f6\tapp" \
    "zhixu-rag-smoke-d4e5f6071829-worker\tlatest\t${rag_id}\tzhixu-rag-smoke-d4e5f6071829\tworker" \
    "zhixu-search-smoke-112233445566-app\tlatest\t${referenced_id}\tzhixu-search-smoke-112233445566\tapp" \
    "zhixu-auth-smoke-010203040506-app\tlatest\t${wrong_label_id}\tzhixu-auth-smoke-010203040506\tworker" \
    "zhixu-tool-smoke-0708090a0b0c-worker\tlatest\t${missing_label_id}\t__missing__\t__missing__" \
    "zhixu-auth-smoke-a1b2c3-app\tlatest\t${wrong_label_id}\tzhixu-auth-smoke-a1b2c3\tapp" \
    "another-project-app\tlatest\t${missing_label_id}\tanother-project\tapp" \
    >"${ZHIXU_FAKE_IMAGE_INVENTORY}"

  run_cleanup
  [[ "${CLEANUP_EXIT}" -eq 0 ]] || fail "dry run returned ${CLEANUP_EXIT}"
  [[ ! -s "${ZHIXU_FAKE_REMOVED_REFS}" ]] || fail "dry run removed an image tag"
  grep -F -- "would remove zhixu-auth-smoke-a1b2c3d4e5f6-app:latest" "${STATE_DIR}/cleanup.out" >/dev/null \
    || fail "dry run omitted an exact target"
  grep -F -- "skipping referenced image ${referenced_id}" "${STATE_DIR}/cleanup.out" >/dev/null \
    || fail "dry run did not report a referenced target"

  run_cleanup --apply --confirm WRONG
  [[ "${CLEANUP_EXIT}" -eq 1 ]] || fail "wrong confirmation was accepted"
  [[ ! -s "${ZHIXU_FAKE_REMOVED_REFS}" ]] || fail "wrong confirmation removed an image tag"

  run_cleanup --apply --confirm DELETE_UNUSED_ZHIXU_SMOKE_IMAGES
  [[ "${CLEANUP_EXIT}" -eq 0 ]] || fail "guarded apply returned ${CLEANUP_EXIT}"
  grep -Fqx -- "zhixu-auth-smoke-a1b2c3d4e5f6-app:latest" "${ZHIXU_FAKE_REMOVED_REFS}" \
    || fail "guarded apply did not remove the auth target"
  grep -Fqx -- "zhixu-auth-smoke-a1b2c3d4e5f6-app:archive" "${ZHIXU_FAKE_REMOVED_REFS}" \
    || fail "guarded apply did not remove the second exact target tag"
  grep -Fqx -- "zhixu-rag-smoke-d4e5f6071829-worker:latest" "${ZHIXU_FAKE_REMOVED_REFS}" \
    || fail "guarded apply did not remove the rag target"
  if grep -Fq -- "zhixu-search-smoke-112233445566-app:latest" "${ZHIXU_FAKE_REMOVED_REFS}"; then
    fail "guarded apply removed a container-referenced target"
  fi
  if grep -Eq '010203040506|0708090a0b0c' "${ZHIXU_FAKE_REMOVED_REFS}"; then
    fail "guarded apply removed a name collision without matching Compose ownership labels"
  fi
  if grep -Eq 'nothex|another-project' "${ZHIXU_FAKE_REMOVED_REFS}"; then
    fail "guarded apply crossed the exact smoke namespace"
  fi
  if grep -Eq 'system prune|builder prune|volume (rm|prune)|container rm' "${ZHIXU_FAKE_DOCKER_LOG}"; then
    fail "cleanup invoked a global or non-image destructive operation"
  fi

  local drift_ref="zhixu-tool-smoke-aabbccddeeff-app:latest"
  local drift_inventory_id="sha256:dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd"
  export ZHIXU_FAKE_DRIFT_REF="${drift_ref}"
  export ZHIXU_FAKE_DRIFT_IMAGE_ID="sha256:ffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff"
  export ZHIXU_FAKE_DRIFT_COUNT_FILE="${STATE_DIR}/drift-inspect-count"
  rm -f "${ZHIXU_FAKE_DRIFT_COUNT_FILE}"
  printf '%b\n' "zhixu-tool-smoke-aabbccddeeff-app\tlatest\t${drift_inventory_id}\tzhixu-tool-smoke-aabbccddeeff\tapp" \
    >"${ZHIXU_FAKE_IMAGE_INVENTORY}"
  run_cleanup --apply --confirm DELETE_UNUSED_ZHIXU_SMOKE_IMAGES
  [[ "${CLEANUP_EXIT}" -eq 1 ]] || fail "image tag drift was not rejected"
  [[ ! -s "${ZHIXU_FAKE_REMOVED_REFS}" ]] || fail "drifted image tag reached removal"
  grep -F -- "${drift_ref} changed image id after inventory; refusing removal" \
    "${STATE_DIR}/cleanup.out" >/dev/null \
    || fail "image tag drift rejection was not reported"
  unset ZHIXU_FAKE_DRIFT_REF ZHIXU_FAKE_DRIFT_IMAGE_ID ZHIXU_FAKE_DRIFT_COUNT_FILE

  printf '%b\n' 'zhixu-tool-smoke-aabbccddeeff-app\tlatest\tnot-an-image-id\tzhixu-tool-smoke-aabbccddeeff\tapp' >"${ZHIXU_FAKE_IMAGE_INVENTORY}"
  run_cleanup --apply --confirm DELETE_UNUSED_ZHIXU_SMOKE_IMAGES
  [[ "${CLEANUP_EXIT}" -eq 1 ]] || fail "malformed target inventory was accepted"
  [[ ! -s "${ZHIXU_FAKE_REMOVED_REFS}" ]] || fail "malformed inventory reached image removal"

  printf '[smoke-image-cleanup-contract] passed\n'
}

main "$@"
