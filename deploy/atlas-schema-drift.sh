#!/usr/bin/env bash
set -Eeuo pipefail

readonly SCRIPT_DIR="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd -P)"
readonly REPOSITORY_ROOT="$(cd -- "${SCRIPT_DIR}/.." && pwd -P)"
readonly DEFAULT_ATLAS_IMAGE="arigaio/atlas@sha256:dce85fd3f83c9c28f343c73236c8917f802d526f1fe921e150b720077a83c5ae"
readonly DEFAULT_POSTGRES_CLIENT_IMAGE="pgvector/pgvector@sha256:1d533553fefe4f12e5d80c7b80622ba0c382abb5758856f52983d8789179f0fb"

: "${ZHIXU_DATABASE_URL:?ZHIXU_DATABASE_URL is required}"
: "${ZHIXU_ATLAS_DEV_URL:?ZHIXU_ATLAS_DEV_URL is required}"

ATLAS_IMAGE="${ATLAS_IMAGE:-${DEFAULT_ATLAS_IMAGE}}"
POSTGRES_CLIENT_IMAGE="${POSTGRES_CLIENT_IMAGE:-${DEFAULT_POSTGRES_CLIENT_IMAGE}}"
state_dir="$(mktemp -d "${TMPDIR:-/tmp}/zhixu-atlas-schema-drift.XXXXXX")"
trap 'rm -rf "${state_dir}"' EXIT

postgres_client() {
  local database_url="$1"
  shift
  ZHIXU_PSQL_URL="${database_url}" docker run --rm --network host \
	-e ZHIXU_PSQL_URL \
	-v "${REPOSITORY_ROOT}:/workspace:ro" -w /workspace \
	"${POSTGRES_CLIENT_IMAGE}" sh -c 'exec psql "$ZHIXU_PSQL_URL" "$@"' sh "$@"
}

target_identity="$(postgres_client "${ZHIXU_DATABASE_URL}" -X -Atq -v ON_ERROR_STOP=1 -c \
  "SELECT current_database() || '|' || COALESCE(inet_server_addr()::text, '') || '|' || inet_server_port()::text")"
dev_identity="$(postgres_client "${ZHIXU_ATLAS_DEV_URL}" -X -Atq -v ON_ERROR_STOP=1 -c \
  "SELECT current_database() || '|' || COALESCE(inet_server_addr()::text, '') || '|' || inet_server_port()::text")"
if [[ "${target_identity}" == "${dev_identity}" ]]; then
  printf '%s\n' 'ZHIXU_ATLAS_DEV_URL must point to a different disposable database' >&2
  exit 1
fi
target_server="${target_identity#*|}"
dev_server="${dev_identity#*|}"
if [[ "${target_server}" != "${dev_server}" ]]; then
  printf '%s\n' 'ZHIXU_ATLAS_DEV_URL must use a disposable database on the target PostgreSQL server' >&2
  exit 1
fi

postgres_client "${ZHIXU_ATLAS_DEV_URL}" -X -v ON_ERROR_STOP=1 \
  -f /workspace/deploy/atlas-schema-reset.sql >/dev/null
postgres_client "${ZHIXU_ATLAS_DEV_URL}" -X -v ON_ERROR_STOP=1 \
  -f /workspace/atlas/schema.sql >/dev/null

atlas_diff="$(docker run --rm --network host \
  -e ATLAS_NO_UPDATE_NOTIFIER=1 \
	-e ZHIXU_DATABASE_URL -e ZHIXU_ATLAS_DEV_URL \
  -v "${REPOSITORY_ROOT}:/workspace:ro" -w /workspace \
  "${ATLAS_IMAGE}" schema diff \
	--env local \
	--from env://url \
	--to env://dev \
	--format '{{ sql . }}')"
if [[ -n "$(printf '%s' "${atlas_diff}" | tr -d '[:space:]')" ]]; then
  printf '%s\n' 'Atlas schema diff detected drift:' >&2
  printf '%s\n' "${atlas_diff}" >&2
  exit 1
fi

postgres_client "${ZHIXU_DATABASE_URL}" -X -Atq -v ON_ERROR_STOP=1 \
  -f /workspace/deploy/atlas-schema-fingerprint.sql >"${state_dir}/target.fingerprint"
postgres_client "${ZHIXU_ATLAS_DEV_URL}" -X -Atq -v ON_ERROR_STOP=1 \
  -f /workspace/deploy/atlas-schema-fingerprint.sql >"${state_dir}/dev.fingerprint"
if ! diff -u "${state_dir}/target.fingerprint" "${state_dir}/dev.fingerprint"; then
  printf '%s\n' 'PostgreSQL catalog fingerprint detected drift' >&2
  exit 1
fi

printf '%s\n' 'Atlas schema drift check passed (Atlas diff plus PostgreSQL catalog fingerprint).'
