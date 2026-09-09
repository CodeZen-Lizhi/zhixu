#!/usr/bin/env bash
set -Eeuo pipefail

readonly SCRIPT_DIR="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
export ZHIXU_COMPOSE_SYNTHESIS=1
export ZHIXU_COMPOSE_WORKSPACE_ANALYSIS=0
export ZHIXU_COMPOSE_RAG_BROWSER=0
export ZHIXU_COMPOSE_RAG_REAL_PROVIDER=0
export ZHIXU_COMPOSE_RAG_REAL_PROVIDER_PREFLIGHT_ONLY=0
exec bash "${SCRIPT_DIR}/compose-rag-smoke.sh" "$@"
