#!/usr/bin/env bash

set -Eeuo pipefail

readonly SCRIPT_DIR="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
readonly VOLUME_OWNER_LABEL="com.zhixu.model-secrets-init-contract.owner"
CONTRACT_OWNER=""
COLLISION_VOLUME=""
PRIMARY_VOLUME=""
RACE_VOLUME=""
CORRUPT_VOLUME=""
SYMLINK_VOLUME=""
CONTRACT_STATE_DIR=""
CREATED_VOLUMES=()
CREATED_VOLUME_OWNERS=()

volume_owner() {
  local volume_name=$1
  docker volume inspect \
    --format "{{ index .Labels \"${VOLUME_OWNER_LABEL}\" }}" \
    "${volume_name}"
}

register_created_volume() {
  local volume_name=$1
  local expected_owner=$2
  local index=${#CREATED_VOLUMES[@]}
  CREATED_VOLUMES[${index}]="${volume_name}"
  CREATED_VOLUME_OWNERS[${index}]="${expected_owner}"
}

remove_volume_if_owned() {
  local volume_name=$1
  local expected_owner=$2
  local actual_owner
  actual_owner="$(volume_owner "${volume_name}" 2>/dev/null)" || return 0
  [[ "${actual_owner}" == "${expected_owner}" ]] || return 0
  docker volume rm "${volume_name}" >/dev/null 2>&1 || true
}

cleanup() {
  local index volume_name expected_owner
  for index in "${!CREATED_VOLUMES[@]}"; do
    volume_name="${CREATED_VOLUMES[${index}]}"
    expected_owner="${CREATED_VOLUME_OWNERS[${index}]}"
    remove_volume_if_owned "${volume_name}" "${expected_owner}"
  done
  [[ -z "${CONTRACT_STATE_DIR}" ]] || rm -rf -- "${CONTRACT_STATE_DIR}"
}

fail() {
  printf '[model-secrets-init-contract] failed: %s\n' "$1" >&2
  exit 1
}

run_initializer() {
  local volume_name=$1
  docker run --rm --user 0:0 \
    --volume "${SCRIPT_DIR}/model-secrets-init.sh:/app/model-secrets-init.sh:ro" \
    --volume "${volume_name}:/var/lib/zhixu/model-secrets" \
    alpine:3.22 /app/model-secrets-init.sh
}

key_digest() {
  local volume_name=$1
  docker run --rm \
    --volume "${volume_name}:/var/lib/zhixu/model-secrets:ro" \
    alpine:3.22 sha256sum /var/lib/zhixu/model-secrets/model-settings.key | awk '{print $1}'
}

create_labeled_volume() {
  local volume_name=$1
  local expected_owner=$2
  local actual_owner
  if docker volume inspect "${volume_name}" >/dev/null 2>&1; then
    return 1
  fi
  docker volume create \
    --label "${VOLUME_OWNER_LABEL}=${expected_owner}" \
    "${volume_name}" >/dev/null || return 1
  actual_owner="$(volume_owner "${volume_name}" 2>/dev/null)" || return 1
  [[ "${actual_owner}" == "${expected_owner}" ]] || return 1
  register_created_volume "${volume_name}" "${expected_owner}"
}

create_volume() {
  create_labeled_volume "$1" "${CONTRACT_OWNER}" \
    || fail "could not allocate an exclusively owned test volume"
}

random_hex() {
  od -An -N12 -tx1 /dev/urandom | tr -d ' \n'
}

assert_valid_layout() {
  local volume_name=$1
  docker run --rm \
    --volume "${volume_name}:/var/lib/zhixu/model-secrets:ro" \
    alpine:3.22 sh -eu -c '
      test "$(stat -c "%u:%g" /var/lib/zhixu/model-secrets)" = "10001:10001"
      test "$(stat -c "%a" /var/lib/zhixu/model-secrets)" = "700"
      test "$(stat -c "%u:%g" /var/lib/zhixu/model-secrets/model-settings.key)" = "10001:10001"
      test "$(stat -c "%a" /var/lib/zhixu/model-secrets/model-settings.key)" = "400"
      test "$(base64 -d /var/lib/zhixu/model-secrets/model-settings.key | wc -c)" = "32"
      test "$(find /var/lib/zhixu/model-secrets -mindepth 1 -maxdepth 1 | wc -l)" = "1"
    '
}

main() {
  command -v docker >/dev/null 2>&1 || fail "docker is unavailable"
  docker version >/dev/null 2>&1 || fail "Docker daemon is unavailable"
  local run_suffix collision_owner tracked_volume_count
  run_suffix="$(random_hex)" || fail "could not generate a unique contract suffix"
  [[ -n "${run_suffix}" ]] || fail "generated contract suffix is empty"
  CONTRACT_OWNER="run-${run_suffix}"
  collision_owner="collision-${CONTRACT_OWNER}"
  COLLISION_VOLUME="zhixu-model-secret-collision-${run_suffix}"
  PRIMARY_VOLUME="zhixu-model-secret-contract-${run_suffix}"
  RACE_VOLUME="zhixu-model-secret-race-${run_suffix}"
  CORRUPT_VOLUME="zhixu-model-secret-corrupt-${run_suffix}"
  SYMLINK_VOLUME="zhixu-model-secret-symlink-${run_suffix}"
  CONTRACT_STATE_DIR="$(mktemp -d "${TMPDIR:-/tmp}/zhixu-model-secret-contract.XXXXXX")" \
    || fail "could not allocate contract state"
  trap cleanup EXIT
  trap 'exit 129' HUP
  trap 'exit 130' INT
  trap 'exit 143' TERM
  create_labeled_volume "${COLLISION_VOLUME}" "${collision_owner}" \
    || fail "could not allocate the collision fixture volume"
  tracked_volume_count=${#CREATED_VOLUMES[@]}
  if create_labeled_volume "${COLLISION_VOLUME}" "${CONTRACT_OWNER}"; then
    fail "a pre-existing volume collision was accepted"
  fi
  [[ "${#CREATED_VOLUMES[@]}" -eq "${tracked_volume_count}" ]] \
    || fail "a rejected volume collision changed cleanup ownership"
  [[ "$(volume_owner "${COLLISION_VOLUME}")" == "${collision_owner}" ]] \
    || fail "a rejected volume collision changed the existing owner label"
  remove_volume_if_owned "${COLLISION_VOLUME}" "${CONTRACT_OWNER}"
  docker volume inspect "${COLLISION_VOLUME}" >/dev/null 2>&1 \
    || fail "cleanup removed a volume with a different owner label"

  create_volume "${PRIMARY_VOLUME}"
  create_volume "${RACE_VOLUME}"
  create_volume "${CORRUPT_VOLUME}"
  create_volume "${SYMLINK_VOLUME}"

  run_initializer "${PRIMARY_VOLUME}"
  local first_digest second_digest
  first_digest="$(key_digest "${PRIMARY_VOLUME}")"
  assert_valid_layout "${PRIMARY_VOLUME}"
  docker run --rm --user 10001:10001 \
    --volume "${PRIMARY_VOLUME}:/var/lib/zhixu/model-secrets:ro" \
    alpine:3.22 sh -eu -c \
    'test "$(base64 -d /var/lib/zhixu/model-secrets/model-settings.key | wc -c)" = "32"' \
    || fail "runtime owner could not read the generated key"
  if docker run --rm \
    --volume "${PRIMARY_VOLUME}:/var/lib/zhixu/model-secrets:ro" \
    alpine:3.22 sh -c 'printf x >>/var/lib/zhixu/model-secrets/model-settings.key' >/dev/null 2>&1; then
    fail "a read-only consumer could modify the key volume"
  fi

  run_initializer "${PRIMARY_VOLUME}"
  second_digest="$(key_digest "${PRIMARY_VOLUME}")"
  [[ "${first_digest}" == "${second_digest}" ]] || fail "a valid existing key was replaced"

  docker run --rm --user 0:0 \
    --volume "${PRIMARY_VOLUME}:/var/lib/zhixu/model-secrets" \
    alpine:3.22 chmod 0600 /var/lib/zhixu/model-secrets/model-settings.key
  if run_initializer "${PRIMARY_VOLUME}" >/dev/null 2>&1; then
    fail "initializer accepted an existing key with unsafe permissions"
  fi
  [[ "$(key_digest "${PRIMARY_VOLUME}")" == "${first_digest}" ]] || fail "invalid existing key was overwritten"

  run_initializer "${CORRUPT_VOLUME}"
  docker run --rm --user 0:0 \
    --volume "${CORRUPT_VOLUME}:/var/lib/zhixu/model-secrets" \
    alpine:3.22 sh -eu -c '
      printf "%*s\n" 44 "" | tr " " "!" >/var/lib/zhixu/model-secrets/model-settings.key
      chown 10001:10001 /var/lib/zhixu/model-secrets/model-settings.key
      chmod 0400 /var/lib/zhixu/model-secrets/model-settings.key
    '
  local corrupt_digest
  corrupt_digest="$(key_digest "${CORRUPT_VOLUME}")"
  if run_initializer "${CORRUPT_VOLUME}" >/dev/null 2>&1; then
    fail "initializer accepted corrupted existing key material"
  fi
  [[ "$(key_digest "${CORRUPT_VOLUME}")" == "${corrupt_digest}" ]] || fail "corrupted key material was overwritten"

  docker run --rm --user 0:0 \
    --volume "${SYMLINK_VOLUME}:/var/lib/zhixu/model-secrets" \
    alpine:3.22 ln -s /etc/passwd /var/lib/zhixu/model-secrets/model-settings.key
  if run_initializer "${SYMLINK_VOLUME}" >/dev/null 2>&1; then
    fail "initializer followed an existing key symlink"
  fi
  docker run --rm \
    --volume "${SYMLINK_VOLUME}:/var/lib/zhixu/model-secrets:ro" \
    alpine:3.22 test -L /var/lib/zhixu/model-secrets/model-settings.key \
    || fail "initializer replaced the existing key symlink"

  set +e
  run_initializer "${RACE_VOLUME}" >"${CONTRACT_STATE_DIR}/race-1.log" 2>&1 &
  local race_pid_one=$!
  run_initializer "${RACE_VOLUME}" >"${CONTRACT_STATE_DIR}/race-2.log" 2>&1 &
  local race_pid_two=$!
  wait "${race_pid_one}"
  local race_exit_one=$?
  wait "${race_pid_two}"
  local race_exit_two=$?
  set -e
  [[ "${race_exit_one}" -eq 0 && "${race_exit_two}" -eq 0 ]] \
    || fail "concurrent initializers did not converge on one valid key"
  assert_valid_layout "${RACE_VOLUME}"

  printf '[model-secrets-init-contract] passed\n'
}

main "$@"
