#!/usr/bin/env bash

set -eu

printf 'ZHIXU_MODEL_SETTINGS_ROLLOUT_ID=%s ZHIXU_MODEL_SETTINGS_PREPARED=%s ZHIXU_WORKER_RESTART_POLICY=%s %s\n' \
  "${ZHIXU_MODEL_SETTINGS_ROLLOUT_ID:-}" "${ZHIXU_MODEL_SETTINGS_PREPARED:-}" \
  "${ZHIXU_WORKER_RESTART_POLICY:-}" "$*" >>"${ZHIXU_FAKE_DOCKER_LOG}"

arguments=" $* "
case "${arguments}" in
  *" compose version "*)
    printf 'Docker Compose version v5.1.2\n'
    ;;
  *"compose.static-models.yml"*" config --format json "*)
    cat "${ZHIXU_FAKE_STATIC_COMPOSE_MODEL}"
    ;;
  *" config --format json "*)
    cat "${ZHIXU_FAKE_MANAGED_COMPOSE_MODEL}"
    ;;
  *" config --quiet "*)
    ;;
  *" port app 8080 "*)
    printf '127.0.0.1:8080\n'
    ;;
  *" run --rm --no-deps -T model-settings-key-init "*)
    if [[ "${ZHIXU_FAKE_KEY_INIT_EXIT:-0}" != "0" ]]; then
      exit "${ZHIXU_FAKE_KEY_INIT_EXIT}"
    fi
    ;;
  *" run --rm --no-deps -T migrate "*)
    if [[ "${ZHIXU_FAKE_MIGRATE_EXIT:-0}" != "0" ]]; then
      exit "${ZHIXU_FAKE_MIGRATE_EXIT}"
    fi
    ;;
  *" run --rm --no-deps -T modelctl begin "*)
    printf 'rollout-contract-1\n'
    ;;
  *" run --rm --no-deps -T modelctl wait-prepared "*)
    if [[ "${ZHIXU_FAKE_BLOCK_MODELCTL:-}" == "wait-prepared" ]]; then
      : >"${ZHIXU_FAKE_BLOCK_READY_FILE:?missing fake block ready file}"
      while :; do
        sleep 1
      done
    fi
    if [[ "${ZHIXU_FAKE_FAIL_MODELCTL:-}" == "wait-prepared" ]]; then
      exit 17
    fi
    ;;
  *" run --rm --no-deps -T modelctl commit "*)
    if [[ "${ZHIXU_FAKE_FAIL_MODELCTL:-}" == "commit-after-persist" ]]; then
      exit 20
    fi
    if [[ "${ZHIXU_FAKE_FAIL_MODELCTL:-}" == "commit-before-persist" ]]; then
      exit 17
    fi
    ;;
  *" run --rm --no-deps -T modelctl status "*)
    printf 'active\n'
    ;;
  *" up --detach --no-deps --force-recreate --wait app worker "*)
    if [[ -n "${ZHIXU_FAKE_FAIL_STEADY_ONCE_FILE:-}" && ! -e "${ZHIXU_FAKE_FAIL_STEADY_ONCE_FILE}" ]]; then
      : >"${ZHIXU_FAKE_FAIL_STEADY_ONCE_FILE}"
      exit 18
    fi
    ;;
  *" ps --status running --services app-model-relay worker-model-relay "*)
    [[ "${ZHIXU_FAKE_MISSING_RELAY:-}" == "app-model-relay" ]] || printf 'app-model-relay\n'
    [[ "${ZHIXU_FAKE_MISSING_RELAY:-}" == "worker-model-relay" ]] || printf 'worker-model-relay\n'
    ;;
esac
