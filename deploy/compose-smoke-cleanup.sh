#!/usr/bin/env bash

# This file is sourced by the four project-owned Compose smoke scripts.

cleanup_compose_smoke_project_images() {
  local project_name=${1:-}
  local remaining_labeled_images=""
  local remaining_named_images=""
  local cleanup_failed=0

  if [[ ! "${project_name}" =~ ^zhixu-(auth|rag|search|tool)-smoke-[0-9a-f]{12}$ ]]; then
    log "refusing cleanup outside the known zhixu smoke project namespace"
    return 1
  fi
  if [[ "${PROJECT_NAME:-}" != "${project_name}" ]]; then
    log "refusing cleanup when the Compose project binding does not match"
    return 1
  fi

  if ! compose down --volumes --remove-orphans --rmi local >/dev/null 2>&1; then
    log "Compose project cleanup failed"
    cleanup_failed=1
  fi

  if ! remaining_labeled_images="$(docker image ls --quiet --filter "label=com.docker.compose.project=${project_name}" 2>/dev/null)"; then
    log "could not verify project image labels"
    cleanup_failed=1
  elif [[ -n "${remaining_labeled_images}" ]]; then
    log "project-labeled images remain after cleanup"
    cleanup_failed=1
  fi

  if ! remaining_named_images="$(docker image ls --quiet --filter "reference=${project_name}-*" 2>/dev/null)"; then
    log "could not verify project image names"
    cleanup_failed=1
  elif [[ -n "${remaining_named_images}" ]]; then
    log "project-named images remain after cleanup"
    cleanup_failed=1
  fi

  return "${cleanup_failed}"
}
