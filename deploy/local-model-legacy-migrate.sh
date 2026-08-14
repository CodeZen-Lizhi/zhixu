#!/usr/bin/env bash

set -Eeuo pipefail
umask 077

readonly SOURCE_ROOT=/migration/source
readonly DESTINATION_ROOT=/var/lib/zhixu/ollama/.ollama
readonly STORE_MARKER="${DESTINATION_ROOT}/.zhixu-managed-model-store-v1"
readonly MIGRATION_MARKER="${DESTINATION_ROOT}/.zhixu-legacy-migration-v1"
readonly STORE_SCHEMA=local-model-store/v1
readonly MIGRATION_SCHEMA=zhixu-legacy-model-migration/v1
readonly SOURCE_CONTAINER=zhixu-eino-live-ollama
readonly SOURCE_VOLUME=zhixu-eino-live-models
readonly SOURCE_VERSION=0.9.6
readonly CHAT_PROBE_MODEL=qwen2.5:0.5b
readonly EMBEDDING_PROBE_MODEL=all-minilm:latest

SNAPSHOT_FILE=""
OLLAMA_PID=""
VERIFY_WATCHDOG_PID=""

fail() {
  printf '[legacy-model-migrate] %s\n' "$1" >&2
  exit 1
}

cleanup() {
  if [[ -n "${VERIFY_WATCHDOG_PID}" ]]; then
    kill -TERM "${VERIFY_WATCHDOG_PID}" 2>/dev/null || true
    wait "${VERIFY_WATCHDOG_PID}" 2>/dev/null || true
  fi
  if [[ -n "${OLLAMA_PID}" ]]; then
    kill -TERM "${OLLAMA_PID}" 2>/dev/null || true
    for _ in {1..50}; do
      kill -0 "${OLLAMA_PID}" 2>/dev/null || break
      sleep 0.1
    done
    kill -KILL "${OLLAMA_PID}" 2>/dev/null || true
    wait "${OLLAMA_PID}" 2>/dev/null || true
  fi
  if [[ -n "${SNAPSHOT_FILE}" && -f "${SNAPSHOT_FILE}" ]]; then
    rm -f -- "${SNAPSHOT_FILE}"
  fi
}

trap cleanup EXIT
trap 'exit 130' INT
trap 'exit 143' TERM

require_sha256() {
  [[ "$1" =~ ^sha256:[0-9a-f]{64}$ ]] || fail "$2 is invalid"
}

require_snapshot_sha256() {
  require_sha256 "$1" "model snapshot digest"
}

decode_snapshot() {
  local expected_digest=$1 encoded=$2 actual_digest
  require_snapshot_sha256 "${expected_digest}"
  [[ "${encoded}" =~ ^[A-Za-z0-9+/]*={0,2}$ && ${#encoded} -le 1048576 ]] \
    || fail "model snapshot encoding is invalid"
  if [[ -n "${SNAPSHOT_FILE}" && -f "${SNAPSHOT_FILE}" ]]; then
    rm -f -- "${SNAPSHOT_FILE}"
  fi
  SNAPSHOT_FILE="$(mktemp /tmp/zhixu-legacy-model-snapshot.XXXXXX)"
  printf '%s' "${encoded}" | base64 --decode >"${SNAPSHOT_FILE}" \
    || fail "model snapshot encoding is invalid"
  actual_digest="sha256:$(sha256sum "${SNAPSHOT_FILE}" | awk '{print $1}')"
  [[ "${actual_digest}" == "${expected_digest}" ]] || fail "model snapshot digest does not match"
  [[ -s "${SNAPSHOT_FILE}" ]] || fail "model snapshot is empty"
  awk -F '\t' '
    BEGIN { count = 0 }
    NF != 3 { exit 1 }
    $1 !~ /^[A-Za-z0-9._\/-]+:[A-Za-z0-9._-]+$/ { exit 1 }
    $2 !~ /^sha256:[0-9a-f]{64}$/ { exit 1 }
    $3 !~ /^(0|[1-9][0-9]*)$/ { exit 1 }
    seen[$1]++ { exit 1 }
    { count++ }
    END { if (count == 0) exit 1 }
  ' "${SNAPSHOT_FILE}" || fail "model snapshot shape is invalid"
}

validate_source_tree() {
  [[ -d "${SOURCE_ROOT}" && ! -L "${SOURCE_ROOT}" ]] || fail "legacy source root is invalid"
  [[ -d "${SOURCE_ROOT}/models" && ! -L "${SOURCE_ROOT}/models" ]] \
    || fail "legacy model directory is invalid"
  find "${SOURCE_ROOT}/models" -mindepth 1 -print -quit | grep -q . \
    || fail "legacy model directory is empty"
  [[ ! -e "${SOURCE_ROOT}/.zhixu-managed-model-store-v1" \
    && ! -e "${SOURCE_ROOT}/.zhixu-legacy-migration-v1" ]] \
    || fail "legacy source contains a reserved marker"
  if find "${SOURCE_ROOT}" -xdev \( -type l -o -type b -o -type c -o -type p -o -type s \) -print -quit | grep -q .; then
    fail "legacy source contains an unsupported filesystem entry"
  fi
}

validate_store_marker() {
  [[ -f "${STORE_MARKER}" && ! -L "${STORE_MARKER}" ]] || fail "managed store marker is missing"
  [[ "$(stat -c '%h' "${STORE_MARKER}")" == 1 ]] || fail "managed store marker has an unsafe link count"
  [[ "$(cat "${STORE_MARKER}")" == "${STORE_SCHEMA}" ]] || fail "managed store marker is invalid"
}

marker_field() {
  local key=$1
  sed -n "s/^${key}=//p" "${MIGRATION_MARKER}"
}

validate_marker_shape() {
  local line_count schema stage source_container source_volume source_version source_fingerprint
  local target_image_id models_sha256 models_base64
  [[ -f "${MIGRATION_MARKER}" && ! -L "${MIGRATION_MARKER}" ]] || return 1
  [[ "$(stat -c '%h' "${MIGRATION_MARKER}")" == 1 ]] || fail "migration marker has an unsafe link count"
  line_count="$(wc -l <"${MIGRATION_MARKER}" | tr -d ' ')"
  [[ "${line_count}" == 9 ]] || fail "migration marker shape is invalid"
  schema="$(marker_field schema)"
  stage="$(marker_field stage)"
  source_container="$(marker_field source_container)"
  source_volume="$(marker_field source_volume)"
  source_version="$(marker_field source_version)"
  source_fingerprint="$(marker_field source_fingerprint)"
  target_image_id="$(marker_field target_image_id)"
  models_sha256="$(marker_field models_sha256)"
  models_base64="$(marker_field models_base64)"
  [[ "${schema}" == "${MIGRATION_SCHEMA}" \
    && ( "${stage}" == copied || "${stage}" == verified ) \
    && "${source_container}" == "${SOURCE_CONTAINER}" \
    && "${source_volume}" == "${SOURCE_VOLUME}" \
    && "${source_version}" == "${SOURCE_VERSION}" ]] \
    || fail "migration marker identity is invalid"
  require_sha256 "${source_fingerprint}" "migration marker source fingerprint"
  require_sha256 "${target_image_id}" "migration marker target image ID"
  require_sha256 "${models_sha256}" "migration marker model snapshot digest"
  [[ "${models_base64}" =~ ^[A-Za-z0-9+/]*={0,2}$ && ${#models_base64} -le 1048576 ]] \
    || fail "migration marker model snapshot encoding is invalid"
  printf '%s\t%s\t%s\t%s\n' "${stage}" "${source_fingerprint}" "${target_image_id}" "${models_sha256}"
}

marker_matches() {
  local expected_fingerprint=$1 expected_target_image_id=$2 expected_models_sha256=$3 expected_models_base64=$4 marker
  marker="$(validate_marker_shape)" || return 1
  IFS=$'\t' read -r stage source_fingerprint target_image_id models_sha256 <<<"${marker}"
  [[ "${source_fingerprint}" == "${expected_fingerprint}" \
    && "${target_image_id}" == "${expected_target_image_id}" \
    && "${models_sha256}" == "${expected_models_sha256}" \
    && "$(marker_field models_base64)" == "${expected_models_base64}" ]] \
    || fail "migration marker does not match this source snapshot"
  printf '%s\n' "${stage}"
}

validate_fresh_destination() {
  validate_store_marker
  [[ -d "${DESTINATION_ROOT}/models" && ! -L "${DESTINATION_ROOT}/models" ]] \
    || fail "managed model directory is invalid"
  if find "${DESTINATION_ROOT}/models" -mindepth 1 -print -quit | grep -q .; then
    fail "managed destination is non-empty without a matching migration marker"
  fi
  if find "${DESTINATION_ROOT}" -mindepth 1 -maxdepth 1 \
      ! -name '.zhixu-managed-model-store-v1' ! -name models -print -quit | grep -q .; then
    fail "managed destination is non-empty without a matching migration marker"
  fi
}

destination_state() {
  local fingerprint=$1 target_image_id=$2 snapshot_sha256=$3 snapshot_base64=$4
  require_sha256 "${fingerprint}" "source fingerprint"
  require_sha256 "${target_image_id}" "target image ID"
  decode_snapshot "${snapshot_sha256}" "${snapshot_base64}"
  validate_store_marker
  if [[ -e "${MIGRATION_MARKER}" || -L "${MIGRATION_MARKER}" ]]; then
    marker_matches "${fingerprint}" "${target_image_id}" "${snapshot_sha256}" "${snapshot_base64}"
    return
  fi
  validate_fresh_destination
  printf 'fresh\n'
}

source_preflight() {
  local source_kib available_kib margin_kib required_kib
  validate_source_tree
  [[ -d "${DESTINATION_ROOT}" && ! -L "${DESTINATION_ROOT}" ]] \
    || fail "managed destination is unavailable for free-space preflight"
  source_kib="$(du -skx "${SOURCE_ROOT}" | awk '{print $1}')"
  available_kib="$(df -Pk "${DESTINATION_ROOT}" | awk 'NR == 2 {print $4}')"
  [[ "${source_kib}" =~ ^[1-9][0-9]*$ && "${available_kib}" =~ ^[0-9]+$ ]] \
    || fail "could not determine legacy model storage size"
  margin_kib=$((source_kib / 10))
  ((margin_kib >= 65536)) || margin_kib=65536
  required_kib=$((source_kib + margin_kib))
  ((available_kib >= required_kib)) || fail "insufficient free space for a verified copy"
  printf 'source_kib=%s\navailable_kib=%s\nrequired_kib=%s\n' \
    "${source_kib}" "${available_kib}" "${required_kib}"
}

tree_fingerprint() {
  local root=$1 path relative size links mode content_digest digest
  if find "${root}" -xdev \( -type l -o -type b -o -type c -o -type p -o -type s \) -print -quit | grep -q .; then
    fail "model tree contains an unsupported filesystem entry"
  fi
  digest="$({
    while IFS= read -r -d '' path; do
      relative="${path#${root}/}"
      mode="$(stat -c '%a' "${path}")"
      printf 'directory\0%s\0%s\0' "${relative}" "${mode}"
    done < <(find "${root}" -mindepth 1 -xdev -type d -print0 | LC_ALL=C sort -z)
    while IFS= read -r -d '' path; do
      relative="${path#${root}/}"
      size="$(stat -c '%s' "${path}")"
      links="$(stat -c '%h' "${path}")"
      mode="$(stat -c '%a' "${path}")"
      content_digest="$(sha256sum "${path}" | awk '{print $1}')"
      printf 'file\0%s\0%s\0%s\0%s\0' "${relative}" "${size}" "${links}" "${mode}"
      printf 'content\0%s\0' "${content_digest}"
    done < <(find "${root}" -xdev -type f \
      ! -path "${root}/.zhixu-managed-model-store-v1" \
      ! -path "${root}/.zhixu-legacy-migration-v1" -print0 | LC_ALL=C sort -z)
  } | sha256sum | awk '{print $1}')"
  [[ "${digest}" =~ ^[0-9a-f]{64}$ ]] || fail "could not fingerprint the model tree"
  printf 'sha256:%s\n' "${digest}"
}

source_fingerprint() {
  validate_source_tree
  tree_fingerprint "${SOURCE_ROOT}"
}

destination_fingerprint() {
  validate_store_marker
  [[ -d "${DESTINATION_ROOT}/models" && ! -L "${DESTINATION_ROOT}/models" ]] \
    || fail "managed model directory is invalid"
  tree_fingerprint "${DESTINATION_ROOT}"
}

write_marker() {
  local stage=$1 fingerprint=$2 target_image_id=$3 snapshot_sha256=$4 snapshot_base64=$5 temporary
  temporary="${MIGRATION_MARKER}.tmp.$$"
  [[ ! -e "${temporary}" && ! -L "${temporary}" ]] || fail "migration marker temporary path exists"
  {
    printf 'schema=%s\n' "${MIGRATION_SCHEMA}"
    printf 'stage=%s\n' "${stage}"
    printf 'source_container=%s\n' "${SOURCE_CONTAINER}"
    printf 'source_volume=%s\n' "${SOURCE_VOLUME}"
    printf 'source_version=%s\n' "${SOURCE_VERSION}"
    printf 'source_fingerprint=%s\n' "${fingerprint}"
    printf 'target_image_id=%s\n' "${target_image_id}"
    printf 'models_sha256=%s\n' "${snapshot_sha256}"
    printf 'models_base64=%s\n' "${snapshot_base64}"
  } >"${temporary}"
  chmod 0600 "${temporary}"
  if [[ "$(id -u)" == 0 ]]; then
    chown 10001:10001 "${temporary}"
  else
    [[ "$(id -u)" == 10001 && "$(id -g)" == 10001 ]] || fail "migration marker owner is invalid"
  fi
  mv -f -- "${temporary}" "${MIGRATION_MARKER}"
}

copy_source() {
  local fingerprint=$1 target_image_id=$2 snapshot_sha256=$3 snapshot_base64=$4
  require_sha256 "${fingerprint}" "source fingerprint"
  require_sha256 "${target_image_id}" "target image ID"
  decode_snapshot "${snapshot_sha256}" "${snapshot_base64}"
  validate_source_tree
  validate_fresh_destination
  cp -a --no-preserve=ownership "${SOURCE_ROOT}/." "${DESTINATION_ROOT}/"
  chown -R 10001:10001 "${DESTINATION_ROOT}"
  chmod 0700 "${DESTINATION_ROOT}"
  validate_store_marker
  write_marker copied "${fingerprint}" "${target_image_id}" "${snapshot_sha256}" "${snapshot_base64}"
  printf 'copied\n'
}

http_json() {
  local method=$1 path=$2 payload=$3 destination=$4 port=${5:-11435} max_bytes=${6:-16777216}
  local status header content_type="" payload_length content_length="" transfer_encoding="" header_count=0
  local chunk_header chunk_size total_bytes=0 terminator trailer_count
  [[ "${port}" =~ ^[1-9][0-9]{0,4}$ && "${max_bytes}" =~ ^[1-9][0-9]*$ ]] || return 1
  ((port <= 65535 && max_bytes <= 16777216)) || return 1
  payload_length="${#payload}"
  exec 3<>"/dev/tcp/127.0.0.1/${port}" || return 1
  printf '%s %s HTTP/1.1\r\nHost: 127.0.0.1\r\nAccept: application/json\r\nConnection: close\r\n' \
    "${method}" "${path}" >&3
  if [[ "${method}" == POST ]]; then
    printf 'Content-Type: application/json\r\nContent-Length: %s\r\n' "${payload_length}" >&3
  fi
  printf '\r\n%s' "${payload}" >&3
  IFS= read -r status <&3 || return 1
  status="${status%$'\r'}"
  [[ "${status}" == 'HTTP/1.1 200 OK' ]] || return 1
  while IFS= read -r header <&3; do
    header_count=$((header_count + 1))
    ((header_count <= 100)) || return 1
    header="${header%$'\r'}"
    [[ -n "${header}" ]] || break
    case "${header,,}" in
      content-type:*)
        [[ -z "${content_type}" ]] || return 1
        content_type="${header#*:}"
        content_type="${content_type# }"
        ;;
      content-length:*)
        [[ -z "${content_length}" ]] || return 1
        content_length="${header#*:}"
        content_length="${content_length# }"
        ;;
      transfer-encoding:*)
        [[ -z "${transfer_encoding}" ]] || return 1
        transfer_encoding="${header#*:}"
        transfer_encoding="${transfer_encoding# }"
        ;;
    esac
  done
  [[ "${content_type,,}" == application/json* ]] || return 1
  [[ -z "${content_length}" || -z "${transfer_encoding}" ]] || return 1
  : >"${destination}"
  if [[ -n "${content_length}" ]]; then
    [[ "${content_length}" =~ ^(0|[1-9][0-9]*)$ ]] || return 1
    ((content_length > 0 && content_length <= max_bytes)) || return 1
    head -c "${content_length}" <&3 >"${destination}"
    [[ "$(wc -c <"${destination}")" -eq "${content_length}" ]] || return 1
  elif [[ -n "${transfer_encoding}" ]]; then
    [[ "${transfer_encoding,,}" == chunked ]] || return 1
    while true; do
      IFS= read -r chunk_header <&3 || return 1
      chunk_header="${chunk_header%$'\r'}"
      [[ "${chunk_header}" =~ ^[0-9A-Fa-f]{1,8}$ ]] || return 1
      chunk_size=$((16#${chunk_header}))
      if ((chunk_size == 0)); then
        trailer_count=0
        while IFS= read -r header <&3; do
          trailer_count=$((trailer_count + 1))
          ((trailer_count <= 20)) || return 1
          header="${header%$'\r'}"
          [[ -n "${header}" ]] || break
        done
        break
      fi
      total_bytes=$((total_bytes + chunk_size))
      ((total_bytes <= max_bytes)) || return 1
      head -c "${chunk_size}" <&3 >>"${destination}"
      [[ "$(wc -c <"${destination}")" -eq "${total_bytes}" ]] || return 1
      IFS= read -r terminator <&3 || return 1
      [[ "${terminator}" == $'\r' ]] || return 1
    done
  else
    head -c "$((max_bytes + 1))" <&3 >"${destination}"
  fi
  exec 3<&-
  exec 3>&-
  [[ -s "${destination}" && "$(wc -c <"${destination}")" -le "${max_bytes}" ]]
}

verify_copy() {
  local fingerprint=$1 target_image_id=$2 snapshot_sha256=$3 snapshot_base64=$4 stage attempt
  local version_file=/tmp/version.json tags_file=/tmp/tags.json chat_file=/tmp/chat.json embedding_file=/tmp/embedding.json
  stage="$(marker_matches "${fingerprint}" "${target_image_id}" "${snapshot_sha256}" "${snapshot_base64}")"
  [[ "${stage}" == copied || "${stage}" == verified ]] || fail "migration marker stage is invalid"
  decode_snapshot "${snapshot_sha256}" "${snapshot_base64}"
  awk -F '\t' -v name="${CHAT_PROBE_MODEL}" '$1 == name {found = 1} END {exit !found}' "${SNAPSHOT_FILE}" \
    || fail "legacy snapshot lacks the approved Chat probe model"
  awk -F '\t' -v name="${EMBEDDING_PROBE_MODEL}" '$1 == name {found = 1} END {exit !found}' "${SNAPSHOT_FILE}" \
    || fail "legacy snapshot lacks the approved Embedding probe model"

  (
    sleep 600
    kill -TERM "$$" 2>/dev/null || true
  ) &
  VERIFY_WATCHDOG_PID=$!

  env -i \
    HOME=/var/lib/zhixu/ollama \
    OLLAMA_HOST=127.0.0.1:11435 \
    OLLAMA_MODELS=/var/lib/zhixu/ollama/.ollama/models \
    OLLAMA_NO_CLOUD=true \
    OLLAMA_NOPRUNE=true \
    OLLAMA_VULKAN=0 \
    PATH=/usr/bin:/bin \
    /usr/bin/ollama serve >/tmp/ollama-verify.log 2>&1 &
  OLLAMA_PID=$!
  for ((attempt = 0; attempt < 120; attempt++)); do
    if http_json GET /api/version '' "${version_file}"; then
      break
    fi
    sleep 0.25
  done
  [[ -s "${version_file}" ]] || fail "target Ollama did not become ready"
  http_json GET /api/tags '' "${tags_file}" || fail "target model snapshot request failed"
  http_json POST /v1/chat/completions \
    '{"model":"qwen2.5:0.5b","messages":[{"role":"user","content":"test"}]}' \
    "${chat_file}" || fail "target Chat production probe failed"
  http_json POST /api/embed \
    '{"model":"all-minilm:latest","input":["test"],"truncate":false}' \
    "${embedding_file}" || fail "target Embedding production probe failed"
  printf 'version_base64=%s\n' "$(base64 <"${version_file}" | tr -d '\n')"
  printf 'tags_base64=%s\n' "$(base64 <"${tags_file}" | tr -d '\n')"
  printf 'chat_base64=%s\n' "$(base64 <"${chat_file}" | tr -d '\n')"
  printf 'embedding_base64=%s\n' "$(base64 <"${embedding_file}" | tr -d '\n')"
}

mark_verified() {
  local fingerprint=$1 target_image_id=$2 snapshot_sha256=$3 snapshot_base64=$4 stage
  stage="$(marker_matches "${fingerprint}" "${target_image_id}" "${snapshot_sha256}" "${snapshot_base64}")"
  [[ "${stage}" == copied || "${stage}" == verified ]] || fail "migration marker stage is invalid"
  decode_snapshot "${snapshot_sha256}" "${snapshot_base64}"
  write_marker verified "${fingerprint}" "${target_image_id}" "${snapshot_sha256}" "${snapshot_base64}"
  printf 'verified\n'
}

completed() {
  local expected_target_image_id=$1 marker stage target_image_id models_sha256 models_base64
  require_sha256 "${expected_target_image_id}" "target image ID"
  marker="$(validate_marker_shape)" || exit 1
  IFS=$'\t' read -r stage _ target_image_id models_sha256 <<<"${marker}"
  [[ "${stage}" == verified && "${target_image_id}" == "${expected_target_image_id}" ]] || exit 1
  models_base64="$(marker_field models_base64)"
  decode_snapshot "${models_sha256}" "${models_base64}"
  printf 'verified\n'
}

main() {
  local action=${1:-}
  [[ $# -gt 0 ]] && shift
  case "${action}" in
    source-preflight)
      [[ $# -eq 0 ]] || fail "source-preflight accepts no arguments"
      source_preflight
      ;;
    source-fingerprint)
      [[ $# -eq 0 ]] || fail "source-fingerprint accepts no arguments"
      source_fingerprint
      ;;
    destination-fingerprint)
      [[ $# -eq 0 ]] || fail "destination-fingerprint accepts no arguments"
      destination_fingerprint
      ;;
    destination-state)
      [[ $# -eq 4 ]] || fail "destination-state arguments are invalid"
      destination_state "$@"
      ;;
    copy)
      [[ $# -eq 4 ]] || fail "copy arguments are invalid"
      copy_source "$@"
      ;;
    verify)
      [[ $# -eq 4 ]] || fail "verify arguments are invalid"
      verify_copy "$@"
      ;;
    mark-verified)
      [[ $# -eq 4 ]] || fail "mark-verified arguments are invalid"
      mark_verified "$@"
      ;;
    completed)
      [[ $# -eq 1 ]] || fail "completed requires the target image ID"
      completed "$1"
      ;;
    *) fail "unknown migration helper action" ;;
  esac
}

main "$@"
