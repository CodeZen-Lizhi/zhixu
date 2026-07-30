#!/usr/bin/env bash

set -eu

printf '%s\n' "$*" >>"${ZHIXU_FAKE_DOCKER_LOG}"

arguments=" $* "
case "${arguments}" in
  *" version "*)
    ;;
  *" image ls --no-trunc --format "*)
    while IFS=$'\t' read -r repository tag image_id project_label service_label; do
      image_ref="${repository}:${tag}"
      if [[ -s "${ZHIXU_FAKE_REMOVED_REFS}" ]] && grep -Fqx -- "${image_ref}" "${ZHIXU_FAKE_REMOVED_REFS}"; then
        continue
      fi
      printf '%s\t%s\t%s\n' "${repository}" "${tag}" "${image_id}"
    done <"${ZHIXU_FAKE_IMAGE_INVENTORY}"
    ;;
  *" ps --all --quiet --filter ancestor="*)
    image_id=${arguments##*ancestor=}
    image_id=${image_id% }
    [[ "${image_id}" != "${ZHIXU_FAKE_REFERENCED_IMAGE_ID}" ]] || printf 'container-contract-1\n'
    ;;
  *" image inspect --format "*)
    image_ref=${!#}
    while IFS=$'\t' read -r repository tag image_id project_label service_label; do
      [[ "${repository}:${tag}" != "${image_ref}" ]] || {
        if [[ "${image_ref}" == "${ZHIXU_FAKE_DRIFT_REF:-}" ]]; then
          inspect_count=0
          if [[ -f "${ZHIXU_FAKE_DRIFT_COUNT_FILE}" ]]; then
            inspect_count="$(cat "${ZHIXU_FAKE_DRIFT_COUNT_FILE}")"
          fi
          inspect_count=$((inspect_count + 1))
          printf '%s\n' "${inspect_count}" >"${ZHIXU_FAKE_DRIFT_COUNT_FILE}"
          if [[ "${inspect_count}" -gt 1 ]]; then
            image_id="${ZHIXU_FAKE_DRIFT_IMAGE_ID}"
          fi
        fi
        [[ "${project_label}" != "__missing__" ]] || project_label=""
        [[ "${service_label}" != "__missing__" ]] || service_label=""
        printf '%s|%s|%s\n' "${image_id}" "${project_label}" "${service_label}"
        exit 0
      }
    done <"${ZHIXU_FAKE_IMAGE_INVENTORY}"
    exit 1
    ;;
  *" image rm "*)
    image_ref=${arguments##* image rm }
    image_ref=${image_ref% }
    printf '%s\n' "${image_ref}" >>"${ZHIXU_FAKE_REMOVED_REFS}"
    ;;
  *)
    printf 'unexpected fake Docker invocation: %s\n' "$*" >&2
    exit 64
    ;;
esac
