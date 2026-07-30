#!/usr/bin/env bash

set -Eeuo pipefail

readonly CONFIRMATION="DELETE_UNUSED_ZHIXU_SMOKE_IMAGES"
readonly PROJECT_RE='^zhixu-(auth|rag|search|tool)-smoke-[0-9a-f]{12}$'
readonly IMAGE_ID_RE='^sha256:[0-9a-f]{64}$'
readonly IMAGE_INSPECT_FORMAT='{{.Id}}|{{index .Config.Labels "com.docker.compose.project"}}|{{index .Config.Labels "com.docker.compose.service"}}'
readonly -a BUILD_SERVICES=(
  model-settings-key-init
  app-model-relay
  worker-model-relay
  migrate
  firewall
  proxy
  worker
  app
)

APPLY=0
CONFIRMED=""
STATE_DIR=""
TARGET_PROJECT=""
TARGET_SERVICE=""
INSPECTED_IMAGE_ID=""
INSPECTED_PROJECT=""
INSPECTED_SERVICE=""

log() {
  printf '[cleanup-smoke-images] %s\n' "$1"
}

fail() {
  printf '[cleanup-smoke-images] failed: %s\n' "$1" >&2
  exit 1
}

usage() {
  cat <<EOF
Usage: deploy/cleanup-smoke-images.sh [--apply --confirm ${CONFIRMATION}]

Without --apply, inventory the exact zhixu Compose smoke namespace and print
the unreferenced image tags that would be removed. Applying removes only those
tags and never removes containers, volumes, or global build cache.
EOF
}

cleanup() {
  [[ -z "${STATE_DIR}" ]] || rm -rf -- "${STATE_DIR}"
}

parse_arguments() {
  while [[ $# -gt 0 ]]; do
    case "$1" in
      --apply)
        APPLY=1
        shift
        ;;
      --confirm)
        [[ $# -ge 2 ]] || fail "--confirm requires a value"
        CONFIRMED=$2
        shift 2
        ;;
      help|-h|--help)
        usage
        exit 0
        ;;
      *) fail "unknown argument: $1" ;;
    esac
  done
  if [[ "${APPLY}" -eq 1 && "${CONFIRMED}" != "${CONFIRMATION}" ]]; then
    fail "--apply requires --confirm ${CONFIRMATION}"
  fi
  if [[ "${APPLY}" -eq 0 && -n "${CONFIRMED}" ]]; then
    fail "--confirm is only valid with --apply"
  fi
}

parse_target_repository() {
  local repository=$1
  local service_name suffix project_name
  TARGET_PROJECT=""
  TARGET_SERVICE=""
  for service_name in "${BUILD_SERVICES[@]}"; do
    suffix="-${service_name}"
    [[ "${repository}" == *"${suffix}" ]] || continue
    project_name="${repository%"${suffix}"}"
    [[ "${project_name}" =~ ${PROJECT_RE} ]] || continue
    TARGET_PROJECT="${project_name}"
    TARGET_SERVICE="${service_name}"
    return 0
  done
  return 1
}

inspect_image_metadata() {
  local image_ref=$1
  local output extra
  INSPECTED_IMAGE_ID=""
  INSPECTED_PROJECT=""
  INSPECTED_SERVICE=""
  output="$(docker image inspect --format "${IMAGE_INSPECT_FORMAT}" "${image_ref}")" || return 1
  [[ "${output}" != *$'\n'* ]] || return 1
  IFS='|' read -r INSPECTED_IMAGE_ID INSPECTED_PROJECT INSPECTED_SERVICE extra <<<"${output}"
  [[ -z "${extra:-}" && "${INSPECTED_IMAGE_ID}" =~ ${IMAGE_ID_RE} ]] || return 1
}

inventory_target_images() {
  local output_file=$1
  local raw_file="${output_file}.raw"
  local repository tag image_id extra image_ref

  docker image ls --no-trunc --format '{{.Repository}}\t{{.Tag}}\t{{.ID}}' >"${raw_file}" \
    || fail "could not inventory local images"
  : >"${output_file}"
  while IFS=$'\t' read -r repository tag image_id extra; do
    [[ -n "${repository}" ]] || continue
    parse_target_repository "${repository}" || continue
    [[ -z "${extra:-}" ]] || fail "target image inventory contained unexpected fields"
    [[ "${tag}" =~ ^[A-Za-z0-9_][A-Za-z0-9_.-]*$ ]] || fail "target image tag is not canonical"
    [[ "${image_id}" =~ ${IMAGE_ID_RE} ]] || fail "target image id is not canonical"
    image_ref="${repository}:${tag}"
    if ! inspect_image_metadata "${image_ref}"; then
      log "skipping ${image_ref}; image metadata could not prove Compose ownership"
      continue
    fi
    if [[ "${INSPECTED_IMAGE_ID}" != "${image_id}" \
      || "${INSPECTED_PROJECT}" != "${TARGET_PROJECT}" \
      || "${INSPECTED_SERVICE}" != "${TARGET_SERVICE}" ]]; then
      log "skipping ${image_ref}; name and Compose ownership labels do not agree"
      continue
    fi
    printf '%s\t%s\t%s\t%s\n' \
      "${image_ref}" "${image_id}" "${TARGET_PROJECT}" "${TARGET_SERVICE}" >>"${output_file}"
  done <"${raw_file}"
  LC_ALL=C sort -u "${output_file}" -o "${output_file}"
}

container_references() {
  local image_id=$1
  docker ps --all --quiet --filter "ancestor=${image_id}"
}

print_or_remove_unreferenced() {
  local inventory_file=$1
  local image_id references image_ref row_image_id row_project row_service extra
  local removal_failed=0

  cut -f2 "${inventory_file}" | LC_ALL=C sort -u >"${STATE_DIR}/image-ids"
  while IFS= read -r image_id; do
    [[ -n "${image_id}" ]] || continue
    references="$(container_references "${image_id}")" || fail "could not inspect container references for ${image_id}"
    if [[ -n "${references}" ]]; then
      log "skipping referenced image ${image_id}"
      continue
    fi
    while IFS=$'\t' read -r image_ref row_image_id row_project row_service extra; do
      [[ "${row_image_id}" == "${image_id}" ]] || continue
      [[ -z "${extra:-}" ]] || fail "verified image inventory contained unexpected fields"
      if [[ "${APPLY}" -eq 0 ]]; then
        log "would remove ${image_ref} (${image_id})"
        continue
      fi
      if ! inspect_image_metadata "${image_ref}"; then
        log "could not revalidate ${image_ref}; refusing removal"
        removal_failed=1
        continue
      fi
      if [[ "${INSPECTED_IMAGE_ID}" != "${image_id}" ]]; then
        log "${image_ref} changed image id after inventory; refusing removal"
        removal_failed=1
        continue
      fi
      if [[ "${INSPECTED_PROJECT}" != "${row_project}" || "${INSPECTED_SERVICE}" != "${row_service}" ]]; then
        log "${image_ref} changed Compose ownership after inventory; refusing removal"
        removal_failed=1
        continue
      fi
      if ! references="$(container_references "${image_id}")"; then
        log "could not recheck container references for ${image_ref}"
        removal_failed=1
        continue
      fi
      if [[ -n "${references}" ]]; then
        log "${image_ref} gained a container reference; refusing removal"
        continue
      fi
      if ! docker image rm "${image_ref}" >/dev/null; then
        log "could not remove ${image_ref}; it may have gained a reference"
        removal_failed=1
      else
        log "removed ${image_ref}"
      fi
    done <"${inventory_file}"
  done <"${STATE_DIR}/image-ids"
  return "${removal_failed}"
}

verify_no_unreferenced_targets() {
  local inventory_file=$1
  local image_id references
  local remaining_unreferenced=0

  cut -f2 "${inventory_file}" | LC_ALL=C sort -u >"${STATE_DIR}/remaining-image-ids"
  while IFS= read -r image_id; do
    [[ -n "${image_id}" ]] || continue
    references="$(container_references "${image_id}")" || fail "could not recheck container references for ${image_id}"
    if [[ -z "${references}" ]]; then
      log "unreferenced target image remains: ${image_id}"
      remaining_unreferenced=1
    fi
  done <"${STATE_DIR}/remaining-image-ids"
  return "${remaining_unreferenced}"
}

main() {
  parse_arguments "$@"
  command -v docker >/dev/null 2>&1 || fail "docker is unavailable"
  docker version >/dev/null 2>&1 || fail "Docker daemon is unavailable"
  STATE_DIR="$(mktemp -d "${TMPDIR:-/tmp}/zhixu-smoke-image-cleanup.XXXXXX")" \
    || fail "could not allocate inventory state"
  trap cleanup EXIT
  trap 'exit 129' HUP
  trap 'exit 130' INT
  trap 'exit 143' TERM

  inventory_target_images "${STATE_DIR}/before"
  local target_count
  target_count="$(wc -l <"${STATE_DIR}/before" | tr -d ' ')"
  log "found ${target_count} exact target image tag(s)"
  [[ "${target_count}" -gt 0 ]] || return 0

  print_or_remove_unreferenced "${STATE_DIR}/before" \
    || fail "one or more exact target image tags could not be removed"
  if [[ "${APPLY}" -eq 0 ]]; then
    log "dry run only; no image tag was removed"
    return 0
  fi

  inventory_target_images "${STATE_DIR}/after"
  verify_no_unreferenced_targets "${STATE_DIR}/after" \
    || fail "unreferenced target images remain after cleanup"
  log "cleanup complete; referenced target images, volumes, containers, and build cache were preserved"
}

main "$@"
