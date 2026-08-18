#!/bin/sh
set -eu

OASDIFF_IMAGE='tufin/oasdiff@sha256:bdba99e5e56558002952aa9a8aa2b91ab5f8e850f5981b5bb9bec732544ff721'
OASDIFF_VERSION='oasdiff version v1.29.1'
ZERO_SHA='0000000000000000000000000000000000000000'

fail() {
  echo "openapi breaking check: $*" >&2
  exit 1
}

command -v git >/dev/null 2>&1 || fail "git is required"
command -v node >/dev/null 2>&1 || fail "node is required"
command -v docker >/dev/null 2>&1 || fail "docker is required"

script_dir=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
repo_root=$(git -C "$script_dir" rev-parse --show-toplevel 2>/dev/null) || fail "script is not inside a Git worktree"
candidate="$script_dir/openapi.json"
[ -f "$candidate" ] && [ -r "$candidate" ] && [ ! -L "$candidate" ] || fail "candidate must be a readable regular file"

base_revision=${OPENAPI_BASE_REVISION-}
case "$base_revision" in
  ''|*[!0-9a-f]*) fail "OPENAPI_BASE_REVISION must be a full lowercase 40-character commit SHA" ;;
esac
[ "${#base_revision}" -eq 40 ] || fail "OPENAPI_BASE_REVISION must be a full lowercase 40-character commit SHA"
[ "$base_revision" != "$ZERO_SHA" ] || fail "OPENAPI_BASE_REVISION must not be the all-zero SHA"

verified_revision=$(git -C "$repo_root" rev-parse --verify "${base_revision}^{commit}" 2>/dev/null) || fail "base revision is not an available commit"
[ "$verified_revision" = "$base_revision" ] || fail "base revision did not resolve to the exact requested commit"

temp_dir=$(mktemp -d) || fail "unable to create temporary directory"
[ -n "$temp_dir" ] && [ -d "$temp_dir" ] || fail "mktemp did not return a directory"
trap 'rm -rf -- "$temp_dir"' EXIT HUP INT TERM
raw_base="$temp_dir/base.raw.json"
normalized_base="$temp_dir/base.json"

git -C "$repo_root" show "${base_revision}:api/openapi/openapi.json" >"$raw_base" || fail "base commit does not contain api/openapi/openapi.json"
[ -s "$raw_base" ] && [ -f "$raw_base" ] || fail "base OpenAPI blob is empty or unreadable"
node "$script_dir/normalize-oasdiff-base.mjs" "$raw_base" "$normalized_base"
[ -s "$normalized_base" ] && [ -f "$normalized_base" ] || fail "normalized base OpenAPI is empty or unreadable"

version_output=$(docker run --rm --network none --read-only --cap-drop ALL --security-opt no-new-privileges \
  --user 65534:65534 "$OASDIFF_IMAGE" --version) || fail "unable to run the pinned oasdiff image"
[ "$version_output" = "$OASDIFF_VERSION" ] || fail "unexpected oasdiff version: $version_output"

echo "OpenAPI breaking base: $verified_revision"
echo "OpenAPI breaking tool: $version_output"
docker run --rm --network none --read-only --cap-drop ALL --security-opt no-new-privileges \
  --user 65534:65534 --env LANG=C --env LC_ALL=C \
  --mount "type=bind,src=$normalized_base,dst=/specs/base.json,readonly" \
  --mount "type=bind,src=$candidate,dst=/specs/candidate.json,readonly" \
  "$OASDIFF_IMAGE" breaking \
  --fail-on WARN \
  --allow-external-refs=false \
  --format text \
  --color never \
  --lang en \
  --include-path-params \
  --deprecation-days-stable=180 \
  --deprecation-days-beta=180 \
  /specs/base.json /specs/candidate.json
