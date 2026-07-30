#!/usr/bin/env bash

set -eu

printf '%s\n' "$*" >>"${ZHIXU_FAKE_DOCKER_LOG}"

arguments=" $* "
case "${arguments}" in
  *" compose "*" down --volumes --remove-orphans --rmi local "*)
    [[ "${ZHIXU_FAKE_COMPOSE_DOWN_FAIL:-0}" != "1" ]] || exit 19
    ;;
  *" image ls --quiet --filter label=com.docker.compose.project="*)
    [[ "${ZHIXU_FAKE_LABEL_LS_FAIL:-0}" != "1" ]] || exit 20
    [[ "${ZHIXU_FAKE_LABELED_IMAGES_REMAIN:-0}" != "1" ]] || printf 'sha256:contract-labeled-image\n'
    ;;
  *" image ls --quiet --filter reference="*)
    [[ "${ZHIXU_FAKE_NAME_LS_FAIL:-0}" != "1" ]] || exit 21
    [[ "${ZHIXU_FAKE_NAMED_IMAGES_REMAIN:-0}" != "1" ]] || printf 'sha256:contract-named-image\n'
    ;;
esac
