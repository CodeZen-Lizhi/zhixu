#!/usr/bin/env bash

# This file is sourced by the four project-owned Compose smoke scripts.

prepare_compose_smoke_netns() {
  [[ -n "${STATE_DIR:-}" && "${PROJECT_NAME:-}" =~ ^zhixu-(auth|rag|search|tool)-smoke-[0-9a-f]{12}$ ]] \
    || { log "could not prepare an isolated namespace without a valid smoke project"; return 1; }

  NETNS_PROJECT_NAME="${PROJECT_NAME}-netns"
  NETNS_NETWORK_NAME="${NETNS_PROJECT_NAME}"
  APP_NETNS_CONTAINER="${PROJECT_NAME}-app-netns"
  WORKER_NETNS_CONTAINER="${PROJECT_NAME}-worker-netns"
  MAIN_NETNS_OVERRIDE_FILE="${STATE_DIR}/compose.main-netns.yml"
  NETNS_OVERRIDE_FILE="${STATE_DIR}/compose.netns-override.yml"

  cat >"${MAIN_NETNS_OVERRIDE_FILE}" <<YAML
services:
  app:
    network_mode: "container:${APP_NETNS_CONTAINER}"
  app-model-relay:
    network_mode: "container:${APP_NETNS_CONTAINER}"
  worker:
    network_mode: "container:${WORKER_NETNS_CONTAINER}"
  worker-model-relay:
    network_mode: "container:${WORKER_NETNS_CONTAINER}"
networks:
  default:
    name: "${NETNS_NETWORK_NAME}"
YAML
  cat >"${NETNS_OVERRIDE_FILE}" <<YAML
services:
  app-netns:
    container_name: "${APP_NETNS_CONTAINER}"
  worker-netns:
    container_name: "${WORKER_NETNS_CONTAINER}"
networks:
  default:
    name: "${NETNS_NETWORK_NAME}"
YAML
}

cleanup_compose_smoke_project_images() {
  local project_name=${1:-}
  local netns_project_name=${2:-}
  local project_image_reference=""
  local project_image_references=""
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
  if [[ ! "${netns_project_name}" =~ ^zhixu-(auth|rag|search|tool)-smoke-[0-9a-f]{12}-netns$ ]]; then
    log "refusing cleanup outside the known zhixu smoke helper namespace"
    return 1
  fi
  if [[ "${NETNS_PROJECT_NAME:-}" != "${netns_project_name}" ]]; then
    log "refusing cleanup when the helper Compose project binding does not match"
    return 1
  fi

  # Consumer containers and their disposable volumes must leave before anchors.
  if ! compose --profile workspace-runtime down --volumes --remove-orphans --rmi local >/dev/null 2>&1; then
    log "Compose project cleanup failed"
    cleanup_failed=1
  fi
  if ! netns_compose down --remove-orphans --rmi local >/dev/null 2>&1; then
    log "Compose helper cleanup failed"
    cleanup_failed=1
  fi

  # Compose v5 may retain its generated project tags after --rmi local. Remove
  # only references under the two validated, randomly generated smoke names.
  local cleanup_name
  for cleanup_name in "${project_name}" "${netns_project_name}"; do
    if ! project_image_references="$(docker image ls --format '{{.Repository}}:{{.Tag}}' \
      --filter "reference=${cleanup_name}-*" 2>/dev/null)"; then
      log "could not enumerate disposable project image references"
      cleanup_failed=1
      continue
    fi
    while IFS= read -r project_image_reference; do
      [[ -n "${project_image_reference}" ]] || continue
      if ! docker image rm -- "${project_image_reference}" >/dev/null 2>&1; then
        log "could not remove disposable project image reference"
        cleanup_failed=1
      fi
    done <<<"${project_image_references}"
  done

  for cleanup_name in "${project_name}" "${netns_project_name}"; do
    if ! remaining_labeled_images="$(docker image ls --quiet --filter "label=com.docker.compose.project=${cleanup_name}" 2>/dev/null)"; then
      log "could not verify project image labels"
      cleanup_failed=1
    elif [[ -n "${remaining_labeled_images}" ]]; then
      log "project-labeled images remain after cleanup"
      cleanup_failed=1
    fi

    if ! remaining_named_images="$(docker image ls --quiet --filter "reference=${cleanup_name}-*" 2>/dev/null)"; then
      log "could not verify project image names"
      cleanup_failed=1
    elif [[ -n "${remaining_named_images}" ]]; then
      log "project-named images remain after cleanup"
      cleanup_failed=1
    fi
  done

  return "${cleanup_failed}"
}
