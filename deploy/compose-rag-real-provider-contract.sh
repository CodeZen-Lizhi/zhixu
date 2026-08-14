#!/usr/bin/env bash

set -Eeuo pipefail

readonly SCRIPT_DIR="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
readonly SMOKE_SCRIPT="${SCRIPT_DIR}/compose-rag-real-provider-smoke.sh"
readonly CHAT_SECRET='chat-contract-secret'
readonly EMBEDDING_SECRET='embedding-contract-secret'
readonly CHAT_BASE_URL='https://chat.example.invalid/v1'
readonly EMBEDDING_BASE_URL='https://embedding.example.invalid/v1'
readonly OLLAMA_EMBEDDING_BASE_URL='http://127.0.0.1:11434/gateway'
readonly OLLAMA_EMBEDDING_MODEL='ollama-embedding-contract'
readonly LONG_EMBEDDING_MODEL="$(printf 'm%.0s' {1..300})"

PREFLIGHT_OUTPUT=""
PREFLIGHT_EXIT=0
CONTRACT_STATE_DIR=""
REAL_DOCKER=""

fail() {
  printf '[compose-rag-real-provider-contract] failed: %s\n' "$1" >&2
  exit 1
}

cleanup() {
  [[ -z "${CONTRACT_STATE_DIR}" ]] || rm -rf -- "${CONTRACT_STATE_DIR}"
}

run_preflight() {
  set +e
  PREFLIGHT_OUTPUT="$({
    unset \
      ZHIXU_EINO_LIVE_ENABLED \
      ZHIXU_EINO_LIVE_BASE_URL \
      ZHIXU_EINO_LIVE_API_KEY \
      ZHIXU_EINO_LIVE_MODEL \
      ZHIXU_EINO_LIVE_MODEL_VERSION \
      ZHIXU_EINO_LIVE_TIMEOUT \
      ZHIXU_EINO_LIVE_OPENAI_EMBEDDING_ENABLED \
      ZHIXU_EINO_LIVE_OPENAI_EMBEDDING_BASE_URL \
      ZHIXU_EINO_LIVE_OPENAI_EMBEDDING_API_KEY \
      ZHIXU_EINO_LIVE_OPENAI_EMBEDDING_MODEL \
      ZHIXU_EINO_LIVE_OPENAI_EMBEDDING_DIMENSIONS \
      ZHIXU_EINO_LIVE_OPENAI_EMBEDDING_TIMEOUT \
      ZHIXU_EINO_LIVE_OLLAMA_EMBEDDING_ENABLED \
      ZHIXU_EINO_LIVE_OLLAMA_EMBEDDING_BASE_URL \
      ZHIXU_EINO_LIVE_OLLAMA_EMBEDDING_MODEL \
      ZHIXU_EINO_LIVE_OLLAMA_EMBEDDING_DIMENSIONS \
      ZHIXU_EINO_LIVE_OLLAMA_EMBEDDING_TIMEOUT \
      ZHIXU_RAG_REAL_PROVIDER_KIND \
      ZHIXU_RAG_REAL_PROVIDER_EMBEDDING_KIND \
      ZHIXU_RAG_REAL_PROVIDER_TRANSPORT \
      ZHIXU_RAG_REAL_PROVIDER_CHAT_MODEL \
      ZHIXU_RAG_REAL_PROVIDER_CHAT_ADAPTER_VERSION \
      ZHIXU_RAG_REAL_PROVIDER_CHAT_TIMEOUT \
      ZHIXU_RAG_REAL_PROVIDER_EMBEDDING_MODEL \
      ZHIXU_RAG_REAL_PROVIDER_EMBEDDING_DIMENSIONS \
      ZHIXU_RAG_REAL_PROVIDER_EMBEDDING_TIMEOUT \
      ZHIXU_TOOL_RUNTIME_MODE \
      ZHIXU_COMPOSE_RAG_SMOKE_TIMEOUT_SECONDS \
      ZHIXU_COMPOSE_RAG_SMOKE_POLL_INTERVAL_SECONDS \
      ZHIXU_COMPOSE_RAG_SMOKE_REQUEST_TIMEOUT_SECONDS
    ZHIXU_COMPOSE_RAG_REAL_PROVIDER=1 \
    ZHIXU_COMPOSE_RAG_REAL_PROVIDER_PREFLIGHT_ONLY=1 \
    PATH="${CONTRACT_STATE_DIR}/bin:${PATH}" \
    ZHIXU_CONTRACT_REAL_DOCKER="${REAL_DOCKER}" \
      "$@" bash "${SMOKE_SCRIPT}"
  } 2>&1)"
  PREFLIGHT_EXIT=$?
  set -e
}

assert_output_is_redacted() {
  [[ "${PREFLIGHT_OUTPUT}" != *'unexpected preflight command'* &&
     "${PREFLIGHT_OUTPUT}" != *'unexpected stateful Docker Compose command'* ]] ||
    fail 'preflight invoked a prohibited command'
  for value in \
    "${CHAT_SECRET}" \
    "${EMBEDDING_SECRET}" \
    "${CHAT_BASE_URL}" \
    "${EMBEDDING_BASE_URL}" \
    "${OLLAMA_EMBEDDING_BASE_URL}" \
    'chat.example.invalid' \
    'embedding.example.invalid' \
    'provider.example.invalid'; do
    [[ "${PREFLIGHT_OUTPUT}" != *"${value}"* ]] || fail 'preflight output leaked Provider configuration'
  done
}

run_external_chat_preflight() {
  run_preflight env \
    ZHIXU_RAG_REAL_PROVIDER_KIND=openai-compatible \
    ZHIXU_EINO_LIVE_ENABLED=true \
    ZHIXU_EINO_LIVE_BASE_URL="${CHAT_BASE_URL}" \
    ZHIXU_EINO_LIVE_API_KEY="${CHAT_SECRET}" \
    ZHIXU_EINO_LIVE_MODEL=chat-contract \
    ZHIXU_EINO_LIVE_MODEL_VERSION=chat-contract-v1 \
    "$@"
}

run_external_preflight() {
  run_external_chat_preflight \
    ZHIXU_EINO_LIVE_OPENAI_EMBEDDING_ENABLED=true \
    ZHIXU_EINO_LIVE_OPENAI_EMBEDDING_BASE_URL="${EMBEDDING_BASE_URL}" \
    ZHIXU_EINO_LIVE_OPENAI_EMBEDDING_API_KEY="${EMBEDDING_SECRET}" \
    ZHIXU_EINO_LIVE_OPENAI_EMBEDDING_MODEL="${LONG_EMBEDDING_MODEL}" \
    ZHIXU_EINO_LIVE_OPENAI_EMBEDDING_DIMENSIONS=8 \
    "$@"
}

run_external_ollama_embedding_preflight() {
  run_external_chat_preflight \
    ZHIXU_RAG_REAL_PROVIDER_EMBEDDING_KIND=ollama \
    ZHIXU_EINO_LIVE_OLLAMA_EMBEDDING_ENABLED=true \
    ZHIXU_EINO_LIVE_OLLAMA_EMBEDDING_BASE_URL="${OLLAMA_EMBEDDING_BASE_URL}" \
    ZHIXU_EINO_LIVE_OLLAMA_EMBEDDING_MODEL="${OLLAMA_EMBEDDING_MODEL}" \
    ZHIXU_EINO_LIVE_OLLAMA_EMBEDDING_DIMENSIONS=8 \
    "$@"
}

main() {
  command -v docker >/dev/null 2>&1 || fail 'docker is unavailable'
  command -v jq >/dev/null 2>&1 || fail 'jq is unavailable'
  REAL_DOCKER="$(command -v docker)"
  docker compose version >/dev/null 2>&1 || fail 'Docker Compose v2 is unavailable'

  CONTRACT_STATE_DIR="$(mktemp -d "${TMPDIR:-/tmp}/zhixu-rag-provider-contract.XXXXXX")" ||
    fail 'could not allocate contract state'
  trap cleanup EXIT
  mkdir -p "${CONTRACT_STATE_DIR}/bin"
  cat >"${CONTRACT_STATE_DIR}/bin/docker" <<'SH'
#!/usr/bin/env bash
set -Eeuo pipefail
[[ "${1:-}" == compose ]] || {
  printf '[compose-rag-real-provider-contract] unexpected Docker command\n' >&2
  exit 97
}
allowed=0
for argument in "$@"; do
  case "${argument}" in
    build|create|down|exec|kill|pause|pull|push|restart|rm|run|start|stop|unpause|up)
      printf '[compose-rag-real-provider-contract] unexpected stateful Docker Compose command\n' >&2
      exit 97
      ;;
  esac
  if [[ "${argument}" == config || "${argument}" == version ]]; then
    allowed=1
  fi
done
[[ "${allowed}" == 1 ]] || {
  printf '[compose-rag-real-provider-contract] unexpected stateful Docker Compose command\n' >&2
  exit 97
}
exec "${ZHIXU_CONTRACT_REAL_DOCKER}" "$@"
SH
  chmod 700 "${CONTRACT_STATE_DIR}/bin/docker"
  cat >"${CONTRACT_STATE_DIR}/bin/prohibited" <<'SH'
#!/usr/bin/env bash
printf '[compose-rag-real-provider-contract] unexpected preflight command\n' >&2
exit 97
SH
  chmod 700 "${CONTRACT_STATE_DIR}/bin/prohibited"
  for command in curl git go node npm; do
    ln -s prohibited "${CONTRACT_STATE_DIR}/bin/${command}"
  done

  run_preflight env ZHIXU_RAG_REAL_PROVIDER_KIND=ollama
  [[ "${PREFLIGHT_EXIT}" -eq 0 ]] || fail 'Ollama preflight attempted a Provider request or failed Compose validation'
  grep -Fq 'passed: Ollama environment and Eino Compose runtime preflight' <<<"${PREFLIGHT_OUTPUT}" ||
    fail 'Ollama preflight omitted its receipt'
  assert_output_is_redacted

  run_preflight env ZHIXU_RAG_REAL_PROVIDER_KIND=unsupported
  [[ "${PREFLIGHT_EXIT}" -ne 0 ]] || fail 'unknown Provider kind passed preflight'
  grep -Fq 'ZHIXU_RAG_REAL_PROVIDER_KIND must be ollama or openai-compatible' <<<"${PREFLIGHT_OUTPUT}" ||
    fail 'unknown Provider kind did not return the stable preflight error'

  run_preflight env \
    ZHIXU_RAG_REAL_PROVIDER_KIND=ollama \
    ZHIXU_RAG_REAL_PROVIDER_EMBEDDING_KIND=unsupported
  [[ "${PREFLIGHT_EXIT}" -ne 0 ]] || fail 'unknown Embedding Provider kind passed preflight'
  grep -Fq 'ZHIXU_RAG_REAL_PROVIDER_EMBEDDING_KIND must be ollama or openai-compatible' <<<"${PREFLIGHT_OUTPUT}" ||
    fail 'unknown Embedding Provider kind did not return the stable preflight error'
  assert_output_is_redacted

  run_preflight env ZHIXU_RAG_REAL_PROVIDER_KIND=openai-compatible
  [[ "${PREFLIGHT_EXIT}" -ne 0 ]] || fail 'incomplete external Provider environment passed preflight'
  grep -Fq 'ZHIXU_EINO_LIVE_ENABLED=true is required' <<<"${PREFLIGHT_OUTPUT}" ||
    fail 'incomplete external Provider environment did not fail before Compose'

  run_preflight env \
    ZHIXU_RAG_REAL_PROVIDER_KIND=openai-compatible \
    ZHIXU_EINO_LIVE_ENABLED=false \
    ZHIXU_EINO_LIVE_BASE_URL="${CHAT_BASE_URL}" \
    ZHIXU_EINO_LIVE_API_KEY="${CHAT_SECRET}" \
    ZHIXU_EINO_LIVE_MODEL=chat-contract \
    ZHIXU_EINO_LIVE_OPENAI_EMBEDDING_ENABLED=true \
    ZHIXU_EINO_LIVE_OPENAI_EMBEDDING_BASE_URL="${EMBEDDING_BASE_URL}" \
    ZHIXU_EINO_LIVE_OPENAI_EMBEDDING_API_KEY="${EMBEDDING_SECRET}" \
    ZHIXU_EINO_LIVE_OPENAI_EMBEDDING_MODEL=embedding-contract \
    ZHIXU_EINO_LIVE_OPENAI_EMBEDDING_DIMENSIONS=8
  [[ "${PREFLIGHT_EXIT}" -ne 0 ]] || fail 'disabled Chat live gate passed external Provider preflight'
  grep -Fq 'ZHIXU_EINO_LIVE_ENABLED=true is required' <<<"${PREFLIGHT_OUTPUT}" ||
    fail 'disabled Chat live gate did not return the stable preflight error'
  assert_output_is_redacted

  run_external_preflight ZHIXU_EINO_LIVE_BASE_URL=https://chat.example.invalid/v1%2flegacy
  [[ "${PREFLIGHT_EXIT}" -ne 0 ]] || fail 'non-canonical external Provider path passed preflight'
  grep -Fq 'ZHIXU_EINO_LIVE_BASE_URL must be a canonical HTTPS URL' <<<"${PREFLIGHT_OUTPUT}" ||
    fail 'non-canonical external Provider path did not return the stable preflight error'
  assert_output_is_redacted

  run_external_preflight ZHIXU_EINO_LIVE_BASE_URL=HTTPS://chat.example.invalid/v1
  [[ "${PREFLIGHT_EXIT}" -ne 0 ]] || fail 'non-canonical external Provider scheme passed preflight'
  grep -Fq 'ZHIXU_EINO_LIVE_BASE_URL must be a canonical HTTPS URL' <<<"${PREFLIGHT_OUTPUT}" ||
    fail 'non-canonical external Provider scheme did not return the stable preflight error'
  assert_output_is_redacted

  run_preflight env \
    ZHIXU_RAG_REAL_PROVIDER_KIND=openai-compatible \
    ZHIXU_EINO_LIVE_ENABLED=true \
    ZHIXU_EINO_LIVE_BASE_URL=http://provider.example.invalid/v1 \
    ZHIXU_EINO_LIVE_API_KEY="${CHAT_SECRET}" \
    ZHIXU_EINO_LIVE_MODEL=chat-contract \
    ZHIXU_EINO_LIVE_OPENAI_EMBEDDING_ENABLED=true \
    ZHIXU_EINO_LIVE_OPENAI_EMBEDDING_BASE_URL="${EMBEDDING_BASE_URL}" \
    ZHIXU_EINO_LIVE_OPENAI_EMBEDDING_API_KEY="${EMBEDDING_SECRET}" \
    ZHIXU_EINO_LIVE_OPENAI_EMBEDDING_MODEL=embedding-contract \
    ZHIXU_EINO_LIVE_OPENAI_EMBEDDING_DIMENSIONS=8
  [[ "${PREFLIGHT_EXIT}" -ne 0 ]] || fail 'external HTTP Provider passed preflight'
  grep -Fq 'ZHIXU_EINO_LIVE_BASE_URL must be a canonical HTTPS URL' <<<"${PREFLIGHT_OUTPUT}" ||
    fail 'external HTTP Provider did not return the stable preflight error'
  assert_output_is_redacted

  run_preflight env \
    ZHIXU_RAG_REAL_PROVIDER_KIND=openai-compatible \
    ZHIXU_EINO_LIVE_ENABLED=true \
    ZHIXU_EINO_LIVE_BASE_URL="${CHAT_BASE_URL}" \
    ZHIXU_EINO_LIVE_API_KEY="${CHAT_SECRET}" \
    ZHIXU_EINO_LIVE_MODEL=chat-contract \
    ZHIXU_RAG_REAL_PROVIDER_CHAT_TIMEOUT=301s \
    ZHIXU_EINO_LIVE_OPENAI_EMBEDDING_ENABLED=true \
    ZHIXU_EINO_LIVE_OPENAI_EMBEDDING_BASE_URL="${EMBEDDING_BASE_URL}" \
    ZHIXU_EINO_LIVE_OPENAI_EMBEDDING_API_KEY="${EMBEDDING_SECRET}" \
    ZHIXU_EINO_LIVE_OPENAI_EMBEDDING_MODEL=embedding-contract \
    ZHIXU_EINO_LIVE_OPENAI_EMBEDDING_DIMENSIONS=8
  [[ "${PREFLIGHT_EXIT}" -ne 0 ]] || fail 'out-of-range Provider timeout passed preflight'
  grep -Fq 'real Chat timeout must be a positive duration no greater than 5m' <<<"${PREFLIGHT_OUTPUT}" ||
    fail 'out-of-range Provider timeout did not return the stable preflight error'
  assert_output_is_redacted

  run_external_preflight ZHIXU_EINO_LIVE_OPENAI_EMBEDDING_ENABLED=false
  [[ "${PREFLIGHT_EXIT}" -ne 0 ]] || fail 'disabled external Embedding gate passed preflight'
  grep -Fq 'ZHIXU_EINO_LIVE_OPENAI_EMBEDDING_ENABLED=true is required' <<<"${PREFLIGHT_OUTPUT}" ||
    fail 'disabled external Embedding gate did not return the stable preflight error'
  assert_output_is_redacted

  run_external_chat_preflight ZHIXU_RAG_REAL_PROVIDER_EMBEDDING_KIND=ollama
  [[ "${PREFLIGHT_EXIT}" -ne 0 ]] || fail 'missing live Ollama Embedding gate passed external Chat preflight'
  grep -Fq 'ZHIXU_EINO_LIVE_OLLAMA_EMBEDDING_ENABLED=true is required' <<<"${PREFLIGHT_OUTPUT}" ||
    fail 'missing live Ollama Embedding gate did not return the stable preflight error'
  assert_output_is_redacted

  run_external_ollama_embedding_preflight ZHIXU_EINO_LIVE_OLLAMA_EMBEDDING_ENABLED=false
  [[ "${PREFLIGHT_EXIT}" -ne 0 ]] || fail 'disabled live Ollama Embedding gate passed external Chat preflight'
  grep -Fq 'ZHIXU_EINO_LIVE_OLLAMA_EMBEDDING_ENABLED=true is required' <<<"${PREFLIGHT_OUTPUT}" ||
    fail 'disabled live Ollama Embedding gate did not return the stable preflight error'
  assert_output_is_redacted

  run_external_ollama_embedding_preflight ZHIXU_EINO_LIVE_OLLAMA_EMBEDDING_BASE_URL=http://embedding.example.invalid/v1
  [[ "${PREFLIGHT_EXIT}" -ne 0 ]] || fail 'non-loopback live Ollama Embedding URL passed external Chat preflight'
  grep -Fq 'ZHIXU_EINO_LIVE_OLLAMA_EMBEDDING_BASE_URL must be a canonical loopback HTTP URL' <<<"${PREFLIGHT_OUTPUT}" ||
    fail 'non-loopback live Ollama Embedding URL did not return the stable preflight error'
  assert_output_is_redacted

  run_external_preflight ZHIXU_EINO_LIVE_OPENAI_EMBEDDING_BASE_URL=http://embedding.example.invalid/v1
  [[ "${PREFLIGHT_EXIT}" -ne 0 ]] || fail 'external HTTP Embedding Provider passed preflight'
  grep -Fq 'ZHIXU_EINO_LIVE_OPENAI_EMBEDDING_BASE_URL must be a canonical HTTPS URL' <<<"${PREFLIGHT_OUTPUT}" ||
    fail 'external HTTP Embedding Provider did not return the stable preflight error'
  assert_output_is_redacted

  run_external_preflight ZHIXU_EINO_LIVE_OPENAI_EMBEDDING_DIMENSIONS=16001
  [[ "${PREFLIGHT_EXIT}" -ne 0 ]] || fail 'out-of-range Embedding dimensions passed preflight'
  grep -Fq 'real Embedding dimensions must be between 1 and 16000' <<<"${PREFLIGHT_OUTPUT}" ||
    fail 'out-of-range Embedding dimensions did not return the stable preflight error'
  assert_output_is_redacted

  run_external_preflight ZHIXU_EINO_LIVE_OPENAI_EMBEDDING_DIMENSIONS=999999999999999999999999999999999999
  [[ "${PREFLIGHT_EXIT}" -ne 0 ]] || fail 'overflowing Embedding dimensions passed preflight'
  grep -Fq 'real Embedding dimensions must be between 1 and 16000' <<<"${PREFLIGHT_OUTPUT}" ||
    fail 'overflowing Embedding dimensions did not return the stable preflight error'
  assert_output_is_redacted

  run_external_preflight ZHIXU_RAG_REAL_PROVIDER_EMBEDDING_TIMEOUT=301s
  [[ "${PREFLIGHT_EXIT}" -ne 0 ]] || fail 'out-of-range Embedding timeout passed preflight'
  grep -Fq 'real Embedding timeout must be a positive duration no greater than 5m' <<<"${PREFLIGHT_OUTPUT}" ||
    fail 'out-of-range Embedding timeout did not return the stable preflight error'
  assert_output_is_redacted

  run_external_preflight ZHIXU_EINO_LIVE_OPENAI_EMBEDDING_MODEL=' embedding-contract '
  [[ "${PREFLIGHT_EXIT}" -ne 0 ]] || fail 'non-canonical Embedding model passed preflight'
  grep -Fq 'real Embedding model must be non-empty and canonical' <<<"${PREFLIGHT_OUTPUT}" ||
    fail 'non-canonical Embedding model did not return the stable preflight error'
  assert_output_is_redacted

  run_external_preflight ZHIXU_EINO_LIVE_MODEL=' chat-contract '
  [[ "${PREFLIGHT_EXIT}" -ne 0 ]] || fail 'non-canonical Chat model passed preflight'
  grep -Fq 'real Chat model must be canonical' <<<"${PREFLIGHT_OUTPUT}" ||
    fail 'non-canonical Chat model did not return the stable preflight error'
  assert_output_is_redacted

  run_external_preflight "ZHIXU_EINO_LIVE_API_KEY=${CHAT_SECRET} "
  [[ "${PREFLIGHT_EXIT}" -ne 0 ]] || fail 'non-canonical Chat Credential passed preflight'
  grep -Fq 'ZHIXU_EINO_LIVE_API_KEY must be non-empty and canonical' <<<"${PREFLIGHT_OUTPUT}" ||
    fail 'non-canonical Chat Credential did not return the stable preflight error'
  assert_output_is_redacted

  run_external_preflight "ZHIXU_EINO_LIVE_OPENAI_EMBEDDING_API_KEY=${EMBEDDING_SECRET} "
  [[ "${PREFLIGHT_EXIT}" -ne 0 ]] || fail 'non-canonical Embedding Credential passed preflight'
  grep -Fq 'ZHIXU_EINO_LIVE_OPENAI_EMBEDDING_API_KEY must be non-empty and canonical' <<<"${PREFLIGHT_OUTPUT}" ||
    fail 'non-canonical Embedding Credential did not return the stable preflight error'
  assert_output_is_redacted

  run_external_preflight ZHIXU_RAG_REAL_PROVIDER_CHAT_TIMEOUT=0s
  [[ "${PREFLIGHT_EXIT}" -ne 0 ]] || fail 'zero Chat timeout passed preflight'
  grep -Fq 'real Chat timeout must be a positive duration no greater than 5m' <<<"${PREFLIGHT_OUTPUT}" ||
    fail 'zero Chat timeout did not return the stable preflight error'
  assert_output_is_redacted

  run_external_preflight ZHIXU_TOOL_RUNTIME_MODE=disabled
  [[ "${PREFLIGHT_EXIT}" -ne 0 ]] || fail 'disabled Tool Runtime passed Eino Compose preflight'
  grep -Fq 'real Provider Compose model did not preserve the production Eino runtime contract' <<<"${PREFLIGHT_OUTPUT}" ||
    fail 'disabled Tool Runtime did not return the stable Compose preflight error'
  [[ "${PREFLIGHT_OUTPUT}" != *'unexpected stateful Docker Compose command'* ]] ||
    fail 'failed preflight executed a diagnostic Compose command'
  [[ "${PREFLIGHT_OUTPUT}" != *'unexpected preflight command'* ]] ||
    fail 'failed preflight invoked a prohibited command'
  assert_output_is_redacted

  run_external_preflight ZHIXU_EINO_LIVE_OPENAI_EMBEDDING_DIMENSIONS=08
  [[ "${PREFLIGHT_EXIT}" -eq 0 ]] || fail 'Go-compatible leading-zero Embedding dimensions failed preflight'
  grep -Fq 'passed: OpenAI-Compatible environment and Eino Compose runtime preflight' <<<"${PREFLIGHT_OUTPUT}" ||
    fail 'normalized Embedding dimensions preflight omitted its receipt'
  assert_output_is_redacted

  run_external_preflight
  [[ "${PREFLIGHT_EXIT}" -eq 0 ]] || fail 'complete external Provider environment failed Compose preflight'
  grep -Fq 'passed: OpenAI-Compatible environment and Eino Compose runtime preflight' <<<"${PREFLIGHT_OUTPUT}" ||
    fail 'successful external Provider preflight omitted its receipt'
  assert_output_is_redacted

  run_external_ollama_embedding_preflight
  [[ "${PREFLIGHT_EXIT}" -eq 0 ]] || fail 'external Chat plus live Ollama Embedding failed Compose preflight'
  grep -Fq 'passed: OpenAI-Compatible Chat + Ollama Embedding environment and Eino Compose runtime preflight' <<<"${PREFLIGHT_OUTPUT}" ||
    fail 'external Chat plus live Ollama Embedding preflight omitted its receipt'
  assert_output_is_redacted

  run_preflight env \
    ZHIXU_RAG_REAL_PROVIDER_KIND=ollama \
    ZHIXU_RAG_REAL_PROVIDER_EMBEDDING_KIND=openai-compatible \
    ZHIXU_EINO_LIVE_OPENAI_EMBEDDING_ENABLED=true \
    ZHIXU_EINO_LIVE_OPENAI_EMBEDDING_BASE_URL="${EMBEDDING_BASE_URL}" \
    ZHIXU_EINO_LIVE_OPENAI_EMBEDDING_API_KEY="${EMBEDDING_SECRET}" \
    ZHIXU_EINO_LIVE_OPENAI_EMBEDDING_MODEL=embedding-contract \
    ZHIXU_EINO_LIVE_OPENAI_EMBEDDING_DIMENSIONS=8
  [[ "${PREFLIGHT_EXIT}" -eq 0 ]] || fail 'Ollama Chat plus external Embedding failed Compose preflight'
  grep -Fq 'passed: Ollama Chat + OpenAI-Compatible Embedding environment and Eino Compose runtime preflight' <<<"${PREFLIGHT_OUTPUT}" ||
    fail 'Ollama Chat plus external Embedding preflight omitted its receipt'
  assert_output_is_redacted

  printf '[compose-rag-real-provider-contract] passed\n'
}

main "$@"
