#!/bin/sh

set -eu

readonly secret_dir="${ZHIXU_MODEL_SETTINGS_KEY_DIR:-/var/lib/zhixu/model-secrets}"
readonly key_file="${secret_dir}/model-settings.key"
readonly owner="10001:10001"

temporary_key=""
decoded_key=""

cleanup() {
  [ -z "${temporary_key}" ] || rm -f -- "${temporary_key}"
  [ -z "${decoded_key}" ] || rm -f -- "${decoded_key}"
}

fail() {
  printf 'model settings key initialization failed: %s\n' "$1" >&2
  exit 1
}

validate_key() {
  candidate_file=$1
  [ ! -L "${candidate_file}" ] || fail "key path must not be a symbolic link"
  [ -f "${candidate_file}" ] || fail "key path is not a regular file"
  [ "$(stat -c '%u:%g' "${candidate_file}")" = "${owner}" ] || fail "key owner must be 10001:10001"
  [ "$(stat -c '%a' "${candidate_file}")" = "400" ] || fail "key mode must be 0400"
  [ "$(wc -c <"${candidate_file}" | tr -d ' ')" = "45" ] || fail "key encoding must be canonical"
  [ "$(wc -l <"${candidate_file}" | tr -d ' ')" = "1" ] || fail "key encoding must be one line"

  decoded_key="$(mktemp /tmp/zhixu-model-settings-key.XXXXXX)" || fail "could not allocate validation buffer"
  chmod 0600 "${decoded_key}"
  base64 -d "${candidate_file}" >"${decoded_key}" 2>/dev/null || fail "key is not valid base64"
  [ "$(wc -c <"${decoded_key}" | tr -d ' ')" = "32" ] || fail "key must decode to exactly 32 bytes"
  rm -f -- "${decoded_key}"
  decoded_key=""
}

trap cleanup EXIT
trap 'exit 1' HUP INT TERM

[ ! -L "${secret_dir}" ] || fail "secret directory must not be a symbolic link"
mkdir -p -- "${secret_dir}"
[ -d "${secret_dir}" ] || fail "secret directory is not a directory"
chown "${owner}" "${secret_dir}"
chmod 0700 "${secret_dir}"

if [ -e "${key_file}" ] || [ -L "${key_file}" ]; then
  validate_key "${key_file}"
  exit 0
fi

umask 077
temporary_key="$(mktemp "${secret_dir}/.model-settings.key.tmp.XXXXXX")" || fail "could not allocate temporary key"
dd if=/dev/urandom bs=32 count=1 2>/dev/null | base64 >"${temporary_key}"
chown "${owner}" "${temporary_key}"
chmod 0400 "${temporary_key}"
validate_key "${temporary_key}"

# Publishing with a hard link is atomic and refuses to replace a concurrently
# created key. The temporary link is removed after the durable name exists.
if ln "${temporary_key}" "${key_file}" 2>/dev/null; then
  rm -f -- "${temporary_key}"
  temporary_key=""
else
  rm -f -- "${temporary_key}"
  temporary_key=""
  validate_key "${key_file}"
fi
