#!/bin/sh
set -eu
umask 077

readonly model_root=/var/lib/zhixu/ollama/.ollama
readonly marker="${model_root}/.zhixu-managed-model-store-v1"
readonly schema=local-model-store/v1

test "$(id -u)" = 0
test -d "${model_root}"
test ! -L "${model_root}"

# Existing populated stores enter only through the separately verified migration
# path. A fresh store and the exact empty-models interrupted state are safe to
# converge without scanning a populated model tree.
if [ ! -f "${marker}" ]; then
  if find "${model_root}" -mindepth 1 -maxdepth 1 ! -name models -print -quit | grep -q .; then
    exit 1
  fi
  if [ -e "${model_root}/models" ] && {
    [ -L "${model_root}/models" ] ||
      [ ! -d "${model_root}/models" ] ||
      find "${model_root}/models" -mindepth 1 -maxdepth 1 -print -quit | grep -q .;
  }; then
    exit 1
  fi
  install -d -m 0700 -o 10001 -g 10001 "${model_root}/models"
  printf '%s\n' "${schema}" >"${marker}"
fi

test ! -L "${marker}"
test -f "${marker}"
test "$(stat -c '%h' "${marker}")" = 1
test "$(cat "${marker}")" = "${schema}"
test ! -L "${model_root}/models"
test -d "${model_root}/models"

# Recover an interrupted run without CAP_FOWNER: temporarily own the exact
# schema paths, set their modes, then transfer ownership as the final step.
chown 0:0 "${model_root}" "${model_root}/models" "${marker}"
chmod 0700 "${model_root}" "${model_root}/models"
chmod 0600 "${marker}"
chown 10001:10001 "${marker}" "${model_root}/models" "${model_root}"
