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
  *" buildx version "*)
    printf 'github.com/docker/buildx v0.27.0\n'
    ;;
  *" buildx build "*)
    destination=""
    previous=""
    for argument in "$@"; do
      if [[ "${previous}" == "--output" ]]; then
        destination="${argument#type=local,dest=}"
        break
      fi
      previous="${argument}"
    done
    [[ -n "${destination}" ]] || exit 61
    mkdir -p "${destination}/web"
    cat >"${destination}/zhixu-host-controller" <<'EOF'
#!/usr/bin/env bash
trap 'exit 0' INT TERM
while :; do
  sleep 1
done
EOF
    printf '<!doctype html><title>ZHIXU</title>\n' >"${destination}/web/index.html"
    chmod 0755 "${destination}/zhixu-host-controller"
    ;;
  *" port postgres 5432 "*)
    printf '127.0.0.1:55432\n'
    ;;
  *"compose.static-models.yml"*" config --format json "*)
    cat "${ZHIXU_FAKE_STATIC_COMPOSE_MODEL}"
    ;;
  *" config --format json "*)
    cat "${ZHIXU_FAKE_MANAGED_COMPOSE_MODEL}"
    ;;
  *" config --quiet "*)
    ;;
  *" run --rm --no-deps -T model-settings-key-init "*)
    [[ "${ZHIXU_FAKE_KEY_INIT_EXIT:-0}" == "0" ]] || exit "${ZHIXU_FAKE_KEY_INIT_EXIT}"
    ;;
  *" run --rm --no-deps -T migrate "*)
    [[ "${ZHIXU_FAKE_MIGRATE_EXIT:-0}" == "0" ]] || exit "${ZHIXU_FAKE_MIGRATE_EXIT}"
    ;;
esac
