#!/usr/bin/env bash

# This file is sourced by compose-rag-smoke.sh. It validates and resolves the
# real Provider environment without printing endpoints or credentials.

RAG_REAL_PROVIDER_KIND_RESOLVED=""
RAG_REAL_PROVIDER_EMBEDDING_KIND_RESOLVED=""
RAG_REAL_PROVIDER_DISPLAY_NAME=""
RAG_REAL_PROVIDER_CHAT_BASE_URL=""
RAG_REAL_PROVIDER_CHAT_API_KEY=""
RAG_REAL_PROVIDER_CHAT_MODEL=""
RAG_REAL_PROVIDER_CHAT_MODEL_VERSION=""
RAG_REAL_PROVIDER_CHAT_ADAPTER_VERSION=""
RAG_REAL_PROVIDER_CHAT_TIMEOUT=""
RAG_REAL_PROVIDER_EMBEDDING_PROVIDER=""
RAG_REAL_PROVIDER_EMBEDDING_BASE_URL=""
RAG_REAL_PROVIDER_EMBEDDING_API_KEY=""
RAG_REAL_PROVIDER_EMBEDDING_MODEL=""
RAG_REAL_PROVIDER_EMBEDDING_DIMENSIONS=""
RAG_REAL_PROVIDER_EMBEDDING_TIMEOUT=""

rag_real_provider_config_error() {
  printf '[rag-real-provider-config] failed: %s\n' "$1" >&2
  return 1
}

rag_real_provider_require_env() {
  local name=$1 value=${!1:-}
  [[ -n "${value}" ]] || rag_real_provider_config_error "${name} is required"
}

rag_real_provider_validate_setting() {
  local name=$1 value=$2 maximum=$3
  python3 - "${value}" "${maximum}" <<'PY' >/dev/null 2>&1 || {
import sys

value = sys.argv[1]
maximum = int(sys.argv[2])
valid = (
    value
    and value == value.strip()
    and len(value.encode("utf-8")) <= maximum
    and all(ord(character) >= 0x21 and ord(character) != 0x7f for character in value)
)
raise SystemExit(0 if valid else 1)
PY
    rag_real_provider_config_error "${name} must be canonical"
  }
}

rag_real_provider_validate_embedding_model() {
  local value=$1
  python3 - "${value}" <<'PY' >/dev/null 2>&1 || {
import sys

value = sys.argv[1]
raise SystemExit(0 if value and value == value.strip() else 1)
PY
    rag_real_provider_config_error 'real Embedding model must be non-empty and canonical'
  }
}

rag_real_provider_validate_timeout() {
  local name=$1 value=$2
  python3 - "${value}" <<'PY' >/dev/null 2>&1 || {
from decimal import Decimal, InvalidOperation
import re
import sys

value = sys.argv[1]
if value.startswith("+"):
    value = value[1:]
token = re.compile(r"(?:\d+(?:\.\d*)?|\.\d+)(?:ns|us|\N{MICRO SIGN}s|\N{GREEK SMALL LETTER MU}s|ms|s|m|h)")
units = {
    "ns": Decimal(1),
    "us": Decimal(1_000),
    "\N{MICRO SIGN}s": Decimal(1_000),
    "\N{GREEK SMALL LETTER MU}s": Decimal(1_000),
    "ms": Decimal(1_000_000),
    "s": Decimal(1_000_000_000),
    "m": Decimal(60_000_000_000),
    "h": Decimal(3_600_000_000_000),
}
position = 0
nanoseconds = 0
for match in token.finditer(value):
    if match.start() != position:
        raise SystemExit(1)
    part = match.group(0)
    unit = next(candidate for candidate in units if part.endswith(candidate))
    try:
        nanoseconds += int(Decimal(part[:-len(unit)]) * units[unit])
    except InvalidOperation:
        raise SystemExit(1)
    position = match.end()
valid = position == len(value) and position > 0 and 0 < nanoseconds <= 300_000_000_000
raise SystemExit(0 if valid else 1)
PY
    rag_real_provider_config_error "${name} must be a positive duration no greater than 5m"
  }
}

rag_real_provider_validate_chat_secret() {
  local name=$1 value=$2
  printf '%s' "${value}" | python3 -c '
import sys

value = sys.stdin.read()
valid = (
    bool(value)
    and value == value.strip()
    and len(value.encode("utf-8")) <= 8192
    and all(ord(character) >= 0x20 and ord(character) != 0x7f for character in value)
)
raise SystemExit(0 if valid else 1)
' >/dev/null 2>&1 || {
    rag_real_provider_config_error "${name} must be non-empty and canonical"
  }
}

rag_real_provider_validate_embedding_secret() {
  local name=$1 value=$2
  printf '%s' "${value}" | python3 -c '
import sys

value = sys.stdin.read()
raise SystemExit(0 if value and value == value.strip() else 1)
' >/dev/null 2>&1 || {
    rag_real_provider_config_error "${name} must be non-empty and canonical"
  }
}

rag_real_provider_validate_https_url() {
  local name=$1 value=$2
  printf '%s' "${value}" | python3 -c '
from urllib.parse import quote_from_bytes, unquote_to_bytes, urlsplit
import sys

value = sys.stdin.read()
try:
    parsed = urlsplit(value)
    canonical_path = quote_from_bytes(unquote_to_bytes(parsed.path), safe="/$&+,/:;=@")
    valid = (
        bool(value)
        and value == value.strip()
        and len(value.encode("utf-8")) <= 2048
        and not any(ord(character) < 0x20 or ord(character) == 0x7f for character in value)
        and value.startswith("https://")
        and parsed.scheme == "https"
        and bool(parsed.hostname)
        and parsed.username is None
        and parsed.password is None
        and parsed.path == canonical_path
        and "?" not in value
        and not parsed.query
        and not parsed.fragment
    )
except (UnicodeError, ValueError):
    valid = False
raise SystemExit(0 if valid else 1)
' >/dev/null 2>&1 || {
    rag_real_provider_config_error "${name} must be a canonical HTTPS URL"
  }
}

rag_real_provider_validate_loopback_http_url() {
  local name=$1 value=$2
  printf '%s' "${value}" | python3 -c '
from ipaddress import ip_address
from urllib.parse import quote_from_bytes, unquote_to_bytes, urlsplit
import sys

value = sys.stdin.read()
try:
    parsed = urlsplit(value)
    hostname = parsed.hostname
    canonical_path = quote_from_bytes(unquote_to_bytes(parsed.path), safe="/$&+,/:;=@")
    try:
        port = parsed.port
    except ValueError:
        port = -1
    loopback = hostname is not None and (
        hostname.lower() == "localhost" or ip_address(hostname).is_loopback
    )
    valid = (
        bool(value)
        and value == value.strip()
        and len(value.encode("utf-8")) <= 2048
        and not any(ord(character) < 0x20 or ord(character) == 0x7f for character in value)
        and value.startswith("http://")
        and parsed.scheme == "http"
        and bool(parsed.netloc)
        and parsed.username is None
        and parsed.password is None
        and parsed.path == canonical_path
        and not parsed.query
        and not parsed.fragment
        and port != -1
        and (port is None or 1 <= port <= 65535)
        and loopback
    )
except (UnicodeError, ValueError):
    valid = False
raise SystemExit(0 if valid else 1)
' >/dev/null 2>&1 || {
    rag_real_provider_config_error "${name} must be a canonical loopback HTTP URL"
  }
}

rag_real_provider_set_display_name() {
  case "${RAG_REAL_PROVIDER_KIND_RESOLVED}/${RAG_REAL_PROVIDER_EMBEDDING_KIND_RESOLVED}" in
    ollama/ollama)
      RAG_REAL_PROVIDER_DISPLAY_NAME='Ollama'
      ;;
    openai-compatible/openai-compatible)
      RAG_REAL_PROVIDER_DISPLAY_NAME='OpenAI-Compatible'
      ;;
    ollama/openai-compatible)
      RAG_REAL_PROVIDER_DISPLAY_NAME='Ollama Chat + OpenAI-Compatible Embedding'
      ;;
    openai-compatible/ollama)
      RAG_REAL_PROVIDER_DISPLAY_NAME='OpenAI-Compatible Chat + Ollama Embedding'
      ;;
    *)
      rag_real_provider_config_error 'real Provider configuration has an unsupported Chat/Embedding combination'
      return 1
      ;;
  esac
}

rag_real_provider_configure() {
  local ollama_chat_api_key=$1 normalized_dimensions
  RAG_REAL_PROVIDER_KIND_RESOLVED="${ZHIXU_RAG_REAL_PROVIDER_KIND:-ollama}"
  RAG_REAL_PROVIDER_EMBEDDING_KIND_RESOLVED="${ZHIXU_RAG_REAL_PROVIDER_EMBEDDING_KIND:-${RAG_REAL_PROVIDER_KIND_RESOLVED}}"

  case "${RAG_REAL_PROVIDER_KIND_RESOLVED}" in
    ollama)
      RAG_REAL_PROVIDER_CHAT_BASE_URL='http://127.0.0.1:11434'
      RAG_REAL_PROVIDER_CHAT_API_KEY="${ollama_chat_api_key}"
      RAG_REAL_PROVIDER_CHAT_MODEL="${ZHIXU_RAG_REAL_PROVIDER_CHAT_MODEL:-qwen2.5:3b}"
      RAG_REAL_PROVIDER_CHAT_MODEL_VERSION="${RAG_REAL_PROVIDER_CHAT_MODEL}"
      RAG_REAL_PROVIDER_CHAT_ADAPTER_VERSION='ollama-openai-v0.9.6'
      RAG_REAL_PROVIDER_CHAT_TIMEOUT="${ZHIXU_RAG_REAL_PROVIDER_CHAT_TIMEOUT:-300s}"
      ;;
    openai-compatible)
      [[ "${ZHIXU_EINO_LIVE_ENABLED:-}" == true ]] ||
        { rag_real_provider_config_error 'ZHIXU_EINO_LIVE_ENABLED=true is required'; return 1; }
      rag_real_provider_require_env ZHIXU_EINO_LIVE_BASE_URL || return 1
      rag_real_provider_require_env ZHIXU_EINO_LIVE_API_KEY || return 1
      rag_real_provider_require_env ZHIXU_EINO_LIVE_MODEL || return 1

      RAG_REAL_PROVIDER_CHAT_BASE_URL="${ZHIXU_EINO_LIVE_BASE_URL}"
      RAG_REAL_PROVIDER_CHAT_API_KEY="${ZHIXU_EINO_LIVE_API_KEY}"
      RAG_REAL_PROVIDER_CHAT_MODEL="${ZHIXU_EINO_LIVE_MODEL}"
      RAG_REAL_PROVIDER_CHAT_MODEL_VERSION="${ZHIXU_EINO_LIVE_MODEL_VERSION:-${RAG_REAL_PROVIDER_CHAT_MODEL}}"
      RAG_REAL_PROVIDER_CHAT_ADAPTER_VERSION="${ZHIXU_RAG_REAL_PROVIDER_CHAT_ADAPTER_VERSION:-openai-compatible-v1}"
      RAG_REAL_PROVIDER_CHAT_TIMEOUT="${ZHIXU_RAG_REAL_PROVIDER_CHAT_TIMEOUT:-${ZHIXU_EINO_LIVE_TIMEOUT:-300s}}"
      rag_real_provider_validate_https_url ZHIXU_EINO_LIVE_BASE_URL "${RAG_REAL_PROVIDER_CHAT_BASE_URL}" || return 1
      rag_real_provider_validate_chat_secret ZHIXU_EINO_LIVE_API_KEY "${RAG_REAL_PROVIDER_CHAT_API_KEY}" || return 1
      ;;
    *)
      rag_real_provider_config_error 'ZHIXU_RAG_REAL_PROVIDER_KIND must be ollama or openai-compatible'
      return 1
      ;;
  esac

  case "${RAG_REAL_PROVIDER_EMBEDDING_KIND_RESOLVED}" in
    ollama)
      RAG_REAL_PROVIDER_EMBEDDING_PROVIDER='ollama'
      RAG_REAL_PROVIDER_EMBEDDING_API_KEY=''
      if [[ "${RAG_REAL_PROVIDER_KIND_RESOLVED}" == ollama ]]; then
        RAG_REAL_PROVIDER_EMBEDDING_BASE_URL='http://127.0.0.1:11434'
        RAG_REAL_PROVIDER_EMBEDDING_MODEL="${ZHIXU_RAG_REAL_PROVIDER_EMBEDDING_MODEL:-all-minilm:latest}"
        RAG_REAL_PROVIDER_EMBEDDING_DIMENSIONS="${ZHIXU_RAG_REAL_PROVIDER_EMBEDDING_DIMENSIONS:-384}"
        RAG_REAL_PROVIDER_EMBEDDING_TIMEOUT="${ZHIXU_RAG_REAL_PROVIDER_EMBEDDING_TIMEOUT:-120s}"
      else
        [[ "${ZHIXU_EINO_LIVE_OLLAMA_EMBEDDING_ENABLED:-}" == true ]] ||
          { rag_real_provider_config_error 'ZHIXU_EINO_LIVE_OLLAMA_EMBEDDING_ENABLED=true is required'; return 1; }
        rag_real_provider_require_env ZHIXU_EINO_LIVE_OLLAMA_EMBEDDING_BASE_URL || return 1
        rag_real_provider_require_env ZHIXU_EINO_LIVE_OLLAMA_EMBEDDING_MODEL || return 1
        rag_real_provider_require_env ZHIXU_EINO_LIVE_OLLAMA_EMBEDDING_DIMENSIONS || return 1
        RAG_REAL_PROVIDER_EMBEDDING_BASE_URL="${ZHIXU_EINO_LIVE_OLLAMA_EMBEDDING_BASE_URL}"
        RAG_REAL_PROVIDER_EMBEDDING_MODEL="${ZHIXU_EINO_LIVE_OLLAMA_EMBEDDING_MODEL}"
        RAG_REAL_PROVIDER_EMBEDDING_DIMENSIONS="${ZHIXU_EINO_LIVE_OLLAMA_EMBEDDING_DIMENSIONS}"
        RAG_REAL_PROVIDER_EMBEDDING_TIMEOUT="${ZHIXU_RAG_REAL_PROVIDER_EMBEDDING_TIMEOUT:-${ZHIXU_EINO_LIVE_OLLAMA_EMBEDDING_TIMEOUT:-120s}}"
        rag_real_provider_validate_loopback_http_url ZHIXU_EINO_LIVE_OLLAMA_EMBEDDING_BASE_URL "${RAG_REAL_PROVIDER_EMBEDDING_BASE_URL}" || return 1
      fi
      ;;
    openai-compatible)
      [[ "${ZHIXU_EINO_LIVE_OPENAI_EMBEDDING_ENABLED:-}" == true ]] ||
        { rag_real_provider_config_error 'ZHIXU_EINO_LIVE_OPENAI_EMBEDDING_ENABLED=true is required'; return 1; }
      rag_real_provider_require_env ZHIXU_EINO_LIVE_OPENAI_EMBEDDING_BASE_URL || return 1
      rag_real_provider_require_env ZHIXU_EINO_LIVE_OPENAI_EMBEDDING_API_KEY || return 1
      rag_real_provider_require_env ZHIXU_EINO_LIVE_OPENAI_EMBEDDING_MODEL || return 1
      rag_real_provider_require_env ZHIXU_EINO_LIVE_OPENAI_EMBEDDING_DIMENSIONS || return 1
      RAG_REAL_PROVIDER_EMBEDDING_PROVIDER='openai-compatible'
      RAG_REAL_PROVIDER_EMBEDDING_BASE_URL="${ZHIXU_EINO_LIVE_OPENAI_EMBEDDING_BASE_URL}"
      RAG_REAL_PROVIDER_EMBEDDING_API_KEY="${ZHIXU_EINO_LIVE_OPENAI_EMBEDDING_API_KEY}"
      RAG_REAL_PROVIDER_EMBEDDING_MODEL="${ZHIXU_EINO_LIVE_OPENAI_EMBEDDING_MODEL}"
      RAG_REAL_PROVIDER_EMBEDDING_DIMENSIONS="${ZHIXU_EINO_LIVE_OPENAI_EMBEDDING_DIMENSIONS}"
      RAG_REAL_PROVIDER_EMBEDDING_TIMEOUT="${ZHIXU_RAG_REAL_PROVIDER_EMBEDDING_TIMEOUT:-${ZHIXU_EINO_LIVE_OPENAI_EMBEDDING_TIMEOUT:-120s}}"
      rag_real_provider_validate_https_url ZHIXU_EINO_LIVE_OPENAI_EMBEDDING_BASE_URL "${RAG_REAL_PROVIDER_EMBEDDING_BASE_URL}" || return 1
      rag_real_provider_validate_embedding_secret ZHIXU_EINO_LIVE_OPENAI_EMBEDDING_API_KEY "${RAG_REAL_PROVIDER_EMBEDDING_API_KEY}" || return 1
      ;;
    *)
      rag_real_provider_config_error 'ZHIXU_RAG_REAL_PROVIDER_EMBEDDING_KIND must be ollama or openai-compatible'
      return 1
      ;;
  esac

  rag_real_provider_set_display_name || return 1
  rag_real_provider_validate_setting 'real Chat model' "${RAG_REAL_PROVIDER_CHAT_MODEL}" 128 || return 1
  rag_real_provider_validate_setting 'real Chat model version' "${RAG_REAL_PROVIDER_CHAT_MODEL_VERSION}" 64 || return 1
  rag_real_provider_validate_setting 'real Chat adapter version' "${RAG_REAL_PROVIDER_CHAT_ADAPTER_VERSION}" 64 || return 1
  rag_real_provider_validate_embedding_model "${RAG_REAL_PROVIDER_EMBEDDING_MODEL}" || return 1
  rag_real_provider_validate_timeout 'real Chat timeout' "${RAG_REAL_PROVIDER_CHAT_TIMEOUT}" || return 1
  rag_real_provider_validate_timeout 'real Embedding timeout' "${RAG_REAL_PROVIDER_EMBEDDING_TIMEOUT}" || return 1
  normalized_dimensions="$(python3 - "${RAG_REAL_PROVIDER_EMBEDDING_DIMENSIONS}" <<'PY' 2>/dev/null
import re
import sys

value = sys.argv[1]
if re.fullmatch(r"\+?[0-9]+", value) is None:
    raise SystemExit(1)
dimensions = int(value, 10)
if dimensions < 1 or dimensions > 16000:
    raise SystemExit(1)
print(dimensions)
PY
)" || {
    rag_real_provider_config_error 'real Embedding dimensions must be between 1 and 16000'
    return 1
  }
  RAG_REAL_PROVIDER_EMBEDDING_DIMENSIONS="${normalized_dimensions}"
}
