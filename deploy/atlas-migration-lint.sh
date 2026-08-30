#!/usr/bin/env bash
set -Eeuo pipefail

readonly SCRIPT_DIR="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd -P)"
readonly REPOSITORY_ROOT="$(cd -- "${SCRIPT_DIR}/.." && pwd -P)"
readonly MIGRATION_DIR="${REPOSITORY_ROOT}/atlas/migrations"

fail() {
  printf 'Atlas migration lint: %s\n' "$*" >&2
  exit 1
}

[[ -d "${MIGRATION_DIR}" ]] || fail 'atlas/migrations is missing'
[[ -f "${MIGRATION_DIR}/atlas.sum" ]] || fail 'atlas/migrations/atlas.sum is missing'
[[ ! -d "${REPOSITORY_ROOT}/migrations" ]] || fail 'legacy migrations directory must not exist'

state_dir="$(mktemp -d "${TMPDIR:-/tmp}/zhixu-atlas-migration-lint.XXXXXX")"
trap 'rm -rf "${state_dir}"' EXIT
versions_file="${state_dir}/versions"
file_count=0
previous_version=''
readonly MAX_INT64='9223372036854775807'

while IFS= read -r file; do
  file_count=$((file_count + 1))
  base="$(basename -- "${file}")"
  if [[ ! "${base}" =~ ^([0-9]+)_[a-z0-9][a-z0-9_]*\.sql$ ]]; then
    fail "invalid filename ${base}; expected <numeric-version>_<snake_case>.sql"
  fi
  version="${BASH_REMATCH[1]}"
  [[ "${version}" =~ [1-9] ]] || fail "version must be positive in ${base}"
  normalized_version="$(printf '%s' "${version}" | sed 's/^0*//')"
  if ((${#normalized_version} > ${#MAX_INT64})) ||
    { ((${#normalized_version} == ${#MAX_INT64})) && [[ "${normalized_version}" > "${MAX_INT64}" ]]; }; then
    fail "version exceeds signed 64-bit range in ${base}"
  fi
  if [[ -n "${previous_version}" ]] &&
    { ((${#normalized_version} < ${#previous_version})) ||
      { ((${#normalized_version} == ${#previous_version})) && [[ "${normalized_version}" < "${previous_version}" ]]; }; }; then
    fail "numeric versions do not increase in Atlas lexicographic file order at ${base}"
  fi
  previous_version="${normalized_version}"
  printf '%s\n' "${normalized_version}" >>"${versions_file}"

  if grep -Eiq '^[[:space:]]*--[[:space:]]*\+goose' "${file}"; then
    fail "Goose annotation found in ${base}"
  fi
  if grep -Eiq '^[[:space:]]*--[[:space:]]*atlas:down([[:space:]]|$)' "${file}"; then
    fail "Down directive found in ${base}; migrations are forward-only"
  fi
  if grep -Eiq 'CREATE[[:space:]]+(UNIQUE[[:space:]]+)?INDEX[[:space:]]+CONCURRENTLY' "${file}" &&
    ! grep -Eq '^[[:space:]]*--[[:space:]]*atlas:txmode[[:space:]]+none([[:space:]]|$)' "${file}"; then
    fail "${base} uses CREATE INDEX CONCURRENTLY without -- atlas:txmode none"
  fi
done < <(find "${MIGRATION_DIR}" -maxdepth 1 -type f -name '*.sql' -print | LC_ALL=C sort)

((file_count > 0)) || fail 'no migration SQL files found'
duplicate_versions="$(LC_ALL=C sort "${versions_file}" | uniq -d)"
if [[ -n "${duplicate_versions}" ]]; then
  fail "duplicate migration versions: $(printf '%s' "${duplicate_versions}" | tr '\n' ' ')"
fi

printf 'Atlas migration lint passed (%d files).\n' "${file_count}"
