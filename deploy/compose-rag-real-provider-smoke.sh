#!/usr/bin/env bash

set -Eeuo pipefail

readonly SCRIPT_DIR="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"

[[ "${ZHIXU_COMPOSE_RAG_REAL_PROVIDER:-0}" == 1 ]] || {
  printf '[compose-rag-real-provider-smoke] failed: set ZHIXU_COMPOSE_RAG_REAL_PROVIDER=1 to run this real Provider gate\n' >&2
  exit 1
}

export ZHIXU_COMPOSE_RAG_SMOKE_TIMEOUT_SECONDS="${ZHIXU_COMPOSE_RAG_SMOKE_TIMEOUT_SECONDS:-1800}"
exec bash "${SCRIPT_DIR}/compose-rag-smoke.sh"
