#!/usr/bin/env bash

set -Eeuo pipefail

readonly SCRIPT_DIR="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
readonly REPOSITORY_ROOT="$(cd -- "${SCRIPT_DIR}/.." && pwd)"
readonly STATE_DIR="$(mktemp -d "${TMPDIR:-/tmp}/zhixu-eino-live-contract.XXXXXX")"
trap 'rm -rf -- "${STATE_DIR}"' EXIT

fail() { printf '[eino-live-smoke-contract] failed: %s\n' "$1" >&2; exit 1; }

output="$({
  ZHIXU_EINO_LIVE_ENABLED=true \
  ZHIXU_EINO_LIVE_BASE_URL=https://provider.invalid/v1 \
  ZHIXU_EINO_LIVE_API_KEY=contract-only \
  ZHIXU_EINO_LIVE_MODEL=contract-model \
  ZHIXU_EINO_LIVE_OPENAI_EMBEDDING_ENABLED=true \
  ZHIXU_EINO_LIVE_OPENAI_EMBEDDING_BASE_URL=https://embedding.invalid \
  ZHIXU_EINO_LIVE_OPENAI_EMBEDDING_API_KEY=contract-only \
  ZHIXU_EINO_LIVE_OPENAI_EMBEDDING_MODEL=contract-embedding \
  ZHIXU_EINO_LIVE_OPENAI_EMBEDDING_DIMENSIONS=8 \
  ZHIXU_EINO_LIVE_OLLAMA_EMBEDDING_ENABLED=true \
  ZHIXU_EINO_LIVE_OLLAMA_EMBEDDING_BASE_URL=http://127.0.0.1:11434 \
  ZHIXU_EINO_LIVE_OLLAMA_EMBEDDING_MODEL=contract-ollama \
  ZHIXU_EINO_LIVE_OLLAMA_EMBEDDING_DIMENSIONS=8 \
  ZHIXU_EINO_LIVE_QUERY_PLAN_ENABLED=true \
  ZHIXU_EINO_LIVE_RAG_METADATA_ENABLED=true \
  ZHIXU_EINO_LIVE_FAITHFULNESS_ENABLED=true \
  make --no-print-directory -n -C "${REPOSITORY_ROOT}" eino-live-smoke
} 2>&1)" || fail 'could not expand the aggregate live smoke target'

for test_name in \
  TestEinoOpenAIChatModelLiveSmoke \
  TestEinoOpenAIEmbeddingLiveSmoke \
  TestEinoOllamaEmbeddingLiveSmoke \
  TestEinoOpenAIQueryPlanLiveSmoke \
  TestEinoOpenAIRAGMetadataLiveSmoke \
  TestEinoOpenAIFaithfulnessReviewLiveSmoke; do
  grep -Fq "${test_name}" <<<"${output}" || fail "aggregate target omitted ${test_name}"
done

mkdir -p -- "${STATE_DIR}/bin"
printf '%s\n' \
  '#!/usr/bin/env bash' \
  ': >"${EINO_LIVE_CONTRACT_GO_MARKER:?}"' \
  'exit 99' >"${STATE_DIR}/bin/go"
chmod 0700 "${STATE_DIR}/bin/go"

parallel_output="$({
  PATH="${STATE_DIR}/bin:/usr/bin:/bin" \
  EINO_LIVE_CONTRACT_GO_MARKER="${STATE_DIR}/go-invoked" \
  ZHIXU_EINO_LIVE_ENABLED=true \
  ZHIXU_EINO_LIVE_BASE_URL=https://provider.invalid/v1 \
  ZHIXU_EINO_LIVE_API_KEY=contract-only \
  ZHIXU_EINO_LIVE_MODEL=contract-model \
  ZHIXU_EINO_LIVE_OPENAI_EMBEDDING_ENABLED=true \
  ZHIXU_EINO_LIVE_OPENAI_EMBEDDING_BASE_URL=https://embedding.invalid \
  ZHIXU_EINO_LIVE_OPENAI_EMBEDDING_API_KEY=contract-only \
  ZHIXU_EINO_LIVE_OPENAI_EMBEDDING_MODEL=contract-embedding \
  ZHIXU_EINO_LIVE_OPENAI_EMBEDDING_DIMENSIONS=8 \
  ZHIXU_EINO_LIVE_OLLAMA_EMBEDDING_ENABLED=true \
  ZHIXU_EINO_LIVE_OLLAMA_EMBEDDING_BASE_URL=http://127.0.0.1:11434 \
  ZHIXU_EINO_LIVE_OLLAMA_EMBEDDING_MODEL=contract-ollama \
  ZHIXU_EINO_LIVE_OLLAMA_EMBEDDING_DIMENSIONS=8 \
  ZHIXU_EINO_LIVE_QUERY_PLAN_ENABLED=true \
  ZHIXU_EINO_LIVE_RAG_METADATA_ENABLED=true \
  make --no-print-directory -j8 -C "${REPOSITORY_ROOT}" eino-live-smoke
} 2>&1)" && fail 'aggregate target accepted an incomplete live environment'

grep -Fq 'ZHIXU_EINO_LIVE_FAITHFULNESS_ENABLED=true is required' <<<"${parallel_output}" || \
  fail 'aggregate target did not report the missing live environment value'
[[ ! -e "${STATE_DIR}/go-invoked" ]] || fail 'aggregate target started a Provider smoke before the full environment gate passed'

printf '[eino-live-smoke-contract] passed\n'
