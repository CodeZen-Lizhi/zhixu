#!/usr/bin/env bash

set -Eeuo pipefail
umask 077

readonly SCRIPT_DIR="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
readonly REPOSITORY_ROOT="$(cd -- "${SCRIPT_DIR}/.." && pwd)"
readonly STARTUP_TIMEOUT_SECONDS=120

STATE_DIR=""
WORKSPACE_ROOT=""
SMOKE_DATABASE_URL=""
SMOKE_DATABASE_NAME=""
WORKSPACE_ID=""
API_PID=""
WORKER_PID=""
VITE_PID=""
MODEL_PID=""
API_BASE_URL=""
WORKER_HEALTH_BASE_URL=""
VITE_BASE_URL=""
MODEL_BASE_URL=""
WORKER_QUEUE=""
BROWSER_EXECUTABLE=""
SEEDER_PATH=""

readonly SMOKE_SOURCE_VERSION_ID="a1400000-0000-4000-8000-000000000004"
readonly SMOKE_SOURCE_SPAN_ID="a1400000-0000-4000-8000-000000000006"

log() { printf '[artifact-browser-smoke] %s\n' "$1"; }
fail() { printf '[artifact-browser-smoke] failed: %s\n' "$1" >&2; exit 1; }
require_command() { command -v "$1" >/dev/null 2>&1 || fail "required command is unavailable: $1"; }

allocate_port() {
  python3 - <<'PY'
import socket
with socket.socket(socket.AF_INET, socket.SOCK_STREAM) as sock:
    sock.bind(("127.0.0.1", 0))
    print(sock.getsockname()[1])
PY
}

resolve_browser() {
  if [[ -n "${ZHIXU_PLAYWRIGHT_EXECUTABLE_PATH:-}" ]]; then BROWSER_EXECUTABLE="${ZHIXU_PLAYWRIGHT_EXECUTABLE_PATH}"
  elif [[ -x "/Applications/Google Chrome.app/Contents/MacOS/Google Chrome" ]]; then BROWSER_EXECUTABLE="/Applications/Google Chrome.app/Contents/MacOS/Google Chrome"
  elif command -v google-chrome >/dev/null 2>&1; then BROWSER_EXECUTABLE="$(command -v google-chrome)"
  elif command -v google-chrome-stable >/dev/null 2>&1; then BROWSER_EXECUTABLE="$(command -v google-chrome-stable)"
  elif command -v chromium >/dev/null 2>&1; then BROWSER_EXECUTABLE="$(command -v chromium)"; fi
  [[ -n "${BROWSER_EXECUTABLE}" && -x "${BROWSER_EXECUTABLE}" ]] || fail "Chrome or Chromium is required; set ZHIXU_PLAYWRIGHT_EXECUTABLE_PATH"
}

stop_process() {
  local pid=$1 attempt
  [[ -n "${pid}" ]] || return 0
  kill -TERM -- "-${pid}" >/dev/null 2>&1 || kill "${pid}" >/dev/null 2>&1 || true
  for attempt in {1..80}; do
    kill -0 -- "-${pid}" >/dev/null 2>&1 || break
    sleep 0.1
  done
  kill -KILL -- "-${pid}" >/dev/null 2>&1 || true
  wait "${pid}" >/dev/null 2>&1 || true
}
stop_api() { stop_process "${API_PID}"; API_PID=""; }
stop_worker() { stop_process "${WORKER_PID}"; WORKER_PID=""; }
stop_vite() { stop_process "${VITE_PID}"; VITE_PID=""; }
stop_model() { stop_process "${MODEL_PID}"; MODEL_PID=""; }

write_dbtool() {
  cat >"${STATE_DIR}/dbtool.go" <<'GO'
package main

import (
  "context"
  "errors"
  "fmt"
  "net/url"
  "os"
  "regexp"
  "strings"
  "time"

  "github.com/jackc/pgx/v5"
)

var identifier = regexp.MustCompile(`^[a-z][a-z0-9_]{0,62}$`)

func main() { if err := run(); err != nil { fmt.Fprintln(os.Stderr, err); os.Exit(1) } }
func run() error {
  if len(os.Args) < 2 { return errors.New("operation is required") }
  ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second); defer cancel()
  switch os.Args[1] {
  case "create", "drop":
    if len(os.Args) != 4 || !identifier.MatchString(os.Args[3]) { return errors.New("invalid database operation") }
    maintenance, err := replaceDatabase(os.Args[2], "postgres"); if err != nil { return err }
    conn, err := pgx.Connect(ctx, maintenance); if err != nil { return errors.New("connect maintenance database failed") }; defer conn.Close(ctx)
    if os.Args[1] == "create" { if _, err = conn.Exec(ctx, "CREATE DATABASE "+quote(os.Args[3])); err != nil { return errors.New("create disposable database failed") }; smoke, err := replaceDatabase(os.Args[2], os.Args[3]); if err != nil { return err }; fmt.Print(smoke); return nil }
    _, _ = conn.Exec(ctx, `SELECT pg_terminate_backend(pid) FROM pg_stat_activity WHERE datname=$1 AND pid <> pg_backend_pid()`, os.Args[3]); _, _ = conn.Exec(ctx, "DROP DATABASE IF EXISTS "+quote(os.Args[3])); return nil
  case "workspace":
    if len(os.Args) != 5 { return errors.New("invalid workspace operation") }
    conn, err := pgx.Connect(ctx, os.Args[2]); if err != nil { return errors.New("connect disposable database failed") }; defer conn.Close(ctx)
    _, err = conn.Exec(ctx, `INSERT INTO core.workspace(id,name,root_path,git_repository_path,git_checked_at,status,version,created_at,updated_at) VALUES($1,'artifact-browser-smoke',$2,$2,clock_timestamp(),'active',1,clock_timestamp(),clock_timestamp())`, os.Args[3], os.Args[4])
    if err != nil { return errors.New("create smoke workspace failed") }; return nil
  default: return errors.New("unknown operation")
  }
}
func replaceDatabase(raw, database string) (string, error) { parsed, err := url.Parse(raw); if err != nil || parsed.Scheme == "" || parsed.Host == "" { return "", errors.New("invalid database URL") }; parsed.Path = "/"+database; return parsed.String(), nil }
func quote(value string) string { return `"`+strings.ReplaceAll(value, `"`, `""`)+`"` }
GO
}

create_database() {
  SMOKE_DATABASE_NAME="zhixu_artifact_smoke_${RANDOM}_${RANDOM}"
  SMOKE_DATABASE_URL="$(cd "${REPOSITORY_ROOT}" && go run "${STATE_DIR}/dbtool.go" create "${ZHIXU_TEST_DATABASE_URL}" "${SMOKE_DATABASE_NAME}")" || fail "could not create disposable database"
}
drop_database() {
  [[ -n "${SMOKE_DATABASE_NAME}" && -n "${STATE_DIR}" ]] || return 0
  (cd "${REPOSITORY_ROOT}" && go run "${STATE_DIR}/dbtool.go" drop "${ZHIXU_TEST_DATABASE_URL}" "${SMOKE_DATABASE_NAME}") >/dev/null 2>&1 || true
  SMOKE_DATABASE_NAME=""; SMOKE_DATABASE_URL=""
}

cleanup() {
  local exit_code=$?
  trap - EXIT INT TERM
  stop_vite; stop_worker; stop_api; stop_model; drop_database
  [[ -z "${SEEDER_PATH}" ]] || rm -f -- "${SEEDER_PATH}" >/dev/null 2>&1 || exit_code=1
  [[ -z "${WORKSPACE_ROOT}" ]] || rm -rf -- "${WORKSPACE_ROOT}" >/dev/null 2>&1 || exit_code=1
  if [[ -n "${STATE_DIR}" && ${exit_code} -eq 0 ]]; then rm -rf -- "${STATE_DIR}" >/dev/null 2>&1 || exit_code=1
  elif [[ -n "${STATE_DIR}" ]]; then printf '[artifact-browser-smoke] diagnostic state retained at %s\n' "${STATE_DIR}" >&2; fi
  exit "${exit_code}"
}

write_provenance_seeder() {
  SEEDER_PATH="$(mktemp "${REPOSITORY_ROOT}/artifact_browser_smoke_seed.XXXXXX.go")" || fail "could not allocate temporary provenance seeder"
  cat >"${SEEDER_PATH}" <<'GO'
package main

import (
  "context"
  "crypto/sha256"
  "encoding/hex"
  "fmt"
  "os"
  "path/filepath"
  "time"

  "github.com/CodeZen-Lizhi/zhixu/internal/foundation"
  retrievalpostgres "github.com/CodeZen-Lizhi/zhixu/internal/retrieval/adapter/postgres"
  retrievalapplication "github.com/CodeZen-Lizhi/zhixu/internal/retrieval/application"
  retrievaldomain "github.com/CodeZen-Lizhi/zhixu/internal/retrieval/domain"
  "github.com/jackc/pgx/v5/pgxpool"
)

const (
  sourceID = "a1400000-0000-4000-8000-000000000002"
  artifactID = "a1400000-0000-4000-8000-000000000003"
  sourceVersionID = "a1400000-0000-4000-8000-000000000004"
  projectionID = "a1400000-0000-4000-8000-000000000005"
  spanID = "a1400000-0000-4000-8000-000000000006"
  chunkID = "a1400000-0000-4000-8000-000000000007"
  indexID = "a1400000-0000-4000-8000-000000000008"
)

func main() {
  if len(os.Args) != 4 { fail("usage: seed <database-url> <workspace-id> <workspace-root>") }
  ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second); defer cancel()
  workspaceID, err := foundation.ParseID(os.Args[2]); if err != nil { fail("invalid workspace id") }
  pool, err := pgxpool.New(ctx, os.Args[1]); if err != nil { fail(err.Error()) }; defer pool.Close()
  content := []byte("Approved recovery replays durable facts without duplicating provider work.")
  digest := sha256.Sum256(content); contentHash := hex.EncodeToString(digest[:])
  location := filepath.Join(".knowledge", "sources", contentHash)
  target := filepath.Join(os.Args[3], location)
  if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil { fail(err.Error()) }
  if err := os.WriteFile(target, content, 0o600); err != nil { fail(err.Error()) }
  at := time.Date(2026, 7, 20, 0, 0, 0, 0, time.UTC)
  hash := func(value string) string { sum := sha256.Sum256([]byte("compose-rag-smoke:" + value)); return hex.EncodeToString(sum[:]) }
  statements := []struct { query string; args []any }{
    {`INSERT INTO core.content_artifact(id,workspace_id,content_hash,byte_size,managed_location,created_at) VALUES($1,$2,$3,$4,$5,$6)`, []any{artifactID, string(workspaceID), contentHash, int64(len(content)), location, at}},
    {`INSERT INTO core.source(id,workspace_id,type,logical_name,original_location,created_at) VALUES($1,$2,'text','Recovery','docs/recovery.txt',$3)`, []any{sourceID, string(workspaceID), at}},
    {`INSERT INTO core.source_version(id,source_id,workspace_id,content_artifact_id,content_hash,byte_size,mime_type,original_content_location,security_status,captured_at) VALUES($1,$2,$3,$4,$5,$6,'text/plain','docs/recovery.txt','passed',$7)`, []any{sourceVersionID, sourceID, string(workspaceID), artifactID, contentHash, int64(len(content)), at}},
    {`INSERT INTO ingestion.parse_projection(id,workspace_id,content_artifact_id,parser_id,parser_version,parser_config_hash,schema_version,normalized_content_hash,warnings,created_at) VALUES($1,$2,$3,'text','v1',$4,'v1',$5,'[]',$6)`, []any{projectionID, string(workspaceID), artifactID, hash("rag-parser"), hash("rag-normalized"), at}},
    {`INSERT INTO ingestion.source_version_projection(source_version_id,parse_projection_id,workspace_id,created_at) VALUES($1,$2,$3,$4)`, []any{sourceVersionID, projectionID, string(workspaceID), at}},
    {`INSERT INTO ingestion.attempt(id,workspace_id,source_version_id,parse_projection_id,status,security_status,failure_stage,error_code,retryable,parser_id,parser_version,parser_config_hash,chunk_strategy_version,schema_version,idempotency_key,attempt_number,started_at,completed_at,version) VALUES('a1400000-0000-4000-8000-000000000012',$1,$2,$3,'chunked','passed','','',false,'text','v1',$4,'structure-v1','v1','artifact-browser-smoke-ingestion',1,$5,$5,1)`, []any{string(workspaceID), sourceVersionID, projectionID, hash("rag-parser"), at}},
    {`INSERT INTO ingestion.source_span(id,workspace_id,content_artifact_id,parse_projection_id,span_type,start_line,end_line,start_byte,end_byte,selector,excerpt_hash,parser_version,schema_version,created_at) VALUES($1,$2,$3,$4,'paragraph',1,1,0,$5,'{"kind":"paragraph"}'::jsonb,$6,'v1','v1',$7)`, []any{spanID, string(workspaceID), artifactID, projectionID, int64(len(content)), contentHash, at}},
    {`INSERT INTO ingestion.canonical_chunk(id,workspace_id,parse_projection_id,sequence,heading_path,content,content_hash,source_span_id,byte_count,rune_count,parser_version,chunk_strategy_version,schema_version,atomic_oversized,status,created_at) VALUES($1,$2,$3,0,'["Recovery"]',$4,$5,$6,$7,$7,'v1','structure-v1','v1',false,'active',$8)`, []any{chunkID, string(workspaceID), projectionID, string(content), contentHash, spanID, int64(len(content)), at}},
    {`INSERT INTO retrieval.index_version(id,workspace_id,tokenizer_id,tokenizer_version,tokenizer_config_hash,fusion_config,source_snapshot_ref,manifest_hash,expected_chunk_count,idempotency_key,status,degraded_capabilities,version,created_at,updated_at,source_manifest_hash,expected_source_count,source_parser_id,source_parser_version,source_parser_config_hash,source_chunk_strategy_version,source_schema_version) VALUES($1,$2,'postgres-simple','v1',$3,'{}','artifact-browser-smoke',$4,1,'artifact-browser-smoke','building','["vector"]',1,$5,$5,$6,1,'text','v1',$3,'structure-v1','v1')`, []any{indexID, string(workspaceID), hash("rag-parser"), hash("rag-manifest"), at, hash("rag-source-manifest")}},
    {`INSERT INTO retrieval.index_manifest_chunk(index_version_id,chunk_id,workspace_id,content_hash,sequence,parser_version,chunk_strategy_version,schema_version,created_at) VALUES($1,$2,$3,$4,0,'v1','structure-v1','v1',$5)`, []any{indexID, chunkID, string(workspaceID), contentHash, at}},
    {`INSERT INTO retrieval.index_manifest_source(index_version_id,workspace_id,source_id,source_version_id,parse_projection_id,selection_status,created_at) VALUES($1,$2,$3,$4,$5,'included',$6)`, []any{indexID, string(workspaceID), sourceID, sourceVersionID, projectionID, at}},
  }
  for _, statement := range statements { if _, err := pool.Exec(ctx, statement.query, statement.args...); err != nil { fail(err.Error()) } }
  repository, err := retrievalpostgres.NewRepository(pool); if err != nil { fail(err.Error()) }
  if _, err := repository.BuildLexical(ctx, retrievaldomain.LexicalBuildCommand{WorkspaceID: workspaceID, IndexVersionID: foundation.ID(indexID), ExpectedIndexVersion: 1, At: at.Add(time.Second)}); err != nil { fail(err.Error()) }
  service, err := retrievalapplication.NewService(retrievalapplication.Dependencies{Store: repository, IDs: foundation.NewUUIDGenerator(nil), Clock: foundation.FixedClock{Value: at.Add(2*time.Second)}}); if err != nil { fail(err.Error()) }
  ready, err := service.Ready(ctx, retrievalapplication.TransitionRequest{WorkspaceID: workspaceID, IndexVersionID: foundation.ID(indexID), ExpectedVersion: 1}); if err != nil { fail(err.Error()) }
  if _, err := service.Activate(ctx, retrievalapplication.ActivateRequest{WorkspaceID: workspaceID, TargetIndexVersionID: foundation.ID(indexID), ExpectedTargetVersion: ready.Version, IdempotencyKey: "artifact-browser-smoke:activate", ReasonCode: "SMOKE"}); err != nil { fail(err.Error()) }
}

func fail(message string) { fmt.Fprintln(os.Stderr, message); os.Exit(1) }
GO
}

write_fake_model() {
  cat >"${STATE_DIR}/fake-model.py" <<'PY'
import json
import sys
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer

CONTENT = "Approved recovery replays durable facts without duplicating provider work."

class Handler(BaseHTTPRequestHandler):
    def log_message(self, _format, *_args):
        pass

    def do_GET(self):
        if self.path != "/healthz":
            self.send_error(404)
            return
        self.send_response(200)
        self.send_header("Content-Type", "application/json")
        self.end_headers()
        self.wfile.write(b'{"status":"ready"}')

    def do_POST(self):
        if self.path != "/v1/chat/completions":
            self.send_error(404)
            return
        length = int(self.headers.get("Content-Length", "0"))
        try:
            request = json.loads(self.rfile.read(length))
            if request.get("model") != "artifact-browser-smoke" or not isinstance(request.get("messages"), list):
                raise ValueError("unexpected request")
        except (ValueError, json.JSONDecodeError):
            self.send_error(400)
            return
        output = json.dumps({
            "result_type": "artifact_section",
            "schema_id": "artifact.section-generation",
            "schema_version": "v1",
            "payload": {"coverage_status": "COVERED", "content": CONTENT, "citation_labels": ["citation-001"], "gaps": []},
        }, separators=(",", ":"))
        response = {"id": "artifact-browser-smoke", "object": "chat.completion", "created": 0,
                    "model": "artifact-browser-smoke-v1", "choices": [{"index": 0, "message": {"role": "assistant", "content": output}, "finish_reason": "stop"}],
                    "usage": {"prompt_tokens": 1, "completion_tokens": 1, "total_tokens": 2}}
        encoded = json.dumps(response, separators=(",", ":")).encode()
        self.send_response(200)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(encoded)))
        self.end_headers()
        self.wfile.write(encoded)

ThreadingHTTPServer(("127.0.0.1", int(sys.argv[1])), Handler).serve_forever()
PY
}

assert_log_safe() {
  local file=$1 label=$2 forbidden
  [[ -f "${file}" ]] || return 0
  if grep -Fq -- "${ZHIXU_TEST_DATABASE_URL}" "${file}" || grep -Eq 'postgres(ql)?://' "${file}"; then fail "${label} exposed a database URL"; fi
  if grep -Fq -- "${REPOSITORY_ROOT}" "${file}" || grep -Fq -- "${WORKSPACE_ROOT}" "${file}"; then fail "${label} exposed an absolute repository or workspace path"; fi
  for forbidden in "${artifactTitle:-Artifact Browser Smoke Isolated Draft}" "验证隔离 Artifact" "已批准知识不足"; do
    grep -Fq -- "${forbidden}" "${file}" && fail "${label} exposed Artifact body content"
  done
  return 0
}

wait_for_ready() {
  local pid=$1 base_url=$2 label=$3 output_file=$4 started_at=${SECONDS} status
  while (( SECONDS - started_at < STARTUP_TIMEOUT_SECONDS )); do
    kill -0 "${pid}" >/dev/null 2>&1 || fail "${label} process exited before readiness"
    status="$(curl --silent --show-error --connect-timeout 1 --max-time 2 --output "${output_file}" --write-out '%{http_code}' "${base_url}/readyz" 2>/dev/null || true)"
    [[ "${status}" == "200" ]] && jq -e '.status == "ready"' "${output_file}" >/dev/null 2>&1 && return 0
    sleep 0.2
  done
  fail "${label} readiness timed out"
}

wait_for_vite() {
  local started_at=${SECONDS} status
  while (( SECONDS - started_at < STARTUP_TIMEOUT_SECONDS )); do
    kill -0 "${VITE_PID}" >/dev/null 2>&1 || fail "Vite process exited before readiness"
    status="$(curl --silent --show-error --connect-timeout 1 --max-time 2 --output "${STATE_DIR}/vite-ready.html" --write-out '%{http_code}' "${VITE_BASE_URL}/" 2>/dev/null || true)"
    [[ "${status}" == "200" ]] && return 0
    sleep 0.2
  done
  fail "Vite readiness timed out"
}

migrate_database() {
  (cd "${REPOSITORY_ROOT}" && ZHIXU_DATABASE_URL="${SMOKE_DATABASE_URL}" go run ./cmd/migrate) >"${STATE_DIR}/migrate.log" 2>&1 || fail "fresh database migration failed"
}

create_workspace() {
  local temporary_root
  temporary_root="$(mktemp -d "${TMPDIR:-/tmp}/zhixu-artifact-browser-workspace.XXXXXX")" || fail "could not allocate temporary Git workspace"
  WORKSPACE_ROOT="$(cd -P -- "${temporary_root}" && pwd)"
  chmod 0700 "${WORKSPACE_ROOT}"
  git -C "${WORKSPACE_ROOT}" init -q || fail "could not initialize temporary Git workspace"
  WORKSPACE_ID="$(uuidgen | tr '[:upper:]' '[:lower:]')"
  (cd "${REPOSITORY_ROOT}" && go run "${STATE_DIR}/dbtool.go" workspace "${SMOKE_DATABASE_URL}" "${WORKSPACE_ID}" "${WORKSPACE_ROOT}") >"${STATE_DIR}/workspace.log" 2>&1 || fail "could not create smoke workspace"
  WORKER_QUEUE="artifact-browser-${WORKSPACE_ID:0:8}"
}

seed_provenance() {
  write_provenance_seeder
  (cd "${REPOSITORY_ROOT}" && go run "${SEEDER_PATH}" "${SMOKE_DATABASE_URL}" "${WORKSPACE_ID}" "${WORKSPACE_ROOT}") >"${STATE_DIR}/provenance.log" 2>&1 || fail "could not seed Artifact retrieval provenance"
}

seed_eligible_knowledge() {
  (cd "${REPOSITORY_ROOT}" && ZHIXU_RAG_FIXTURE_DATABASE_URL="${SMOKE_DATABASE_URL}" ZHIXU_RAG_FIXTURE_WORKSPACE_ID="${WORKSPACE_ID}" ZHIXU_RAG_FIXTURE_SOURCE_VERSION_ID="${SMOKE_SOURCE_VERSION_ID}" ZHIXU_RAG_FIXTURE_SOURCE_SPAN_ID="${SMOKE_SOURCE_SPAN_ID}" go test -tags=integration -count=1 -run '^TestComposeRAGKnowledgeSeedExternalFixture$' ./cmd/worker) >"${STATE_DIR}/eligibility-fixture.log" 2>&1 || fail "existing Evidence eligibility fixture failed"
}

wait_for_model() {
  local started_at=${SECONDS} status
  while (( SECONDS - started_at < STARTUP_TIMEOUT_SECONDS )); do
    kill -0 "${MODEL_PID}" >/dev/null 2>&1 || fail "fake model process exited before readiness"
    status="$(curl --silent --show-error --connect-timeout 1 --max-time 2 --output "${STATE_DIR}/model-ready.json" --write-out '%{http_code}' "${MODEL_BASE_URL}/healthz" 2>/dev/null || true)"
    [[ "${status}" == "200" ]] && jq -e '.status == "ready"' "${STATE_DIR}/model-ready.json" >/dev/null 2>&1 && return 0
    sleep 0.2
  done
  fail "fake model readiness timed out"
}

start_model() {
  local port; port="$(allocate_port)"; MODEL_BASE_URL="http://127.0.0.1:${port}"
  write_fake_model
  (cd "${REPOSITORY_ROOT}" && exec python3 -c 'import os, sys; os.setsid(); os.execvp("python3", ["python3", sys.argv[1], sys.argv[2]])' "${STATE_DIR}/fake-model.py" "${port}") >"${STATE_DIR}/model.log" 2>&1 & MODEL_PID=$!
}

start_api() {
  local port; port="$(allocate_port)"; API_BASE_URL="http://127.0.0.1:${port}"
  (cd "${REPOSITORY_ROOT}" && export ZHIXU_HTTP_ADDR="127.0.0.1:${port}" ZHIXU_DATABASE_URL="${SMOKE_DATABASE_URL}" ZHIXU_WEB_ASSETS_DIR="${REPOSITORY_ROOT}/web/dist" ZHIXU_WORKER_QUEUE="${WORKER_QUEUE}" ZHIXU_CHAT_PROVIDER=openai-compatible ZHIXU_CHAT_BASE_URL="${MODEL_BASE_URL}/v1" ZHIXU_CHAT_API_KEY=artifact-browser-smoke ZHIXU_CHAT_MODEL=artifact-browser-smoke ZHIXU_CHAT_MODEL_VERSION=artifact-browser-smoke-v1 ZHIXU_CHAT_ADAPTER_VERSION=smoke-v1 ZHIXU_EMBEDDING_PROVIDER=disabled ZHIXU_TOOL_RUNTIME_MODE=disabled ZHIXU_WEB_FETCH_MODE=disabled; exec python3 -c 'import os; os.setsid(); os.execvp("go", ["go", "run", "./cmd/api"])') >"${STATE_DIR}/api.log" 2>&1 & API_PID=$!
}

start_worker() {
  local port; port="$(allocate_port)"; WORKER_HEALTH_BASE_URL="http://127.0.0.1:${port}"
  (cd "${REPOSITORY_ROOT}" && export ZHIXU_DATABASE_URL="${SMOKE_DATABASE_URL}" ZHIXU_WORKER_HEALTH_ADDR="127.0.0.1:${port}" ZHIXU_WORKER_QUEUE="${WORKER_QUEUE}" ZHIXU_WORKER_MAX_WORKERS=2 ZHIXU_CHAT_PROVIDER=openai-compatible ZHIXU_CHAT_BASE_URL="${MODEL_BASE_URL}/v1" ZHIXU_CHAT_API_KEY=artifact-browser-smoke ZHIXU_CHAT_MODEL=artifact-browser-smoke ZHIXU_CHAT_MODEL_VERSION=artifact-browser-smoke-v1 ZHIXU_CHAT_ADAPTER_VERSION=smoke-v1 ZHIXU_EMBEDDING_PROVIDER=disabled ZHIXU_TOOL_RUNTIME_MODE=disabled ZHIXU_WEB_FETCH_MODE=disabled; exec python3 -c 'import os; os.setsid(); os.execvp("go", ["go", "run", "./cmd/worker"])') >"${STATE_DIR}/worker.log" 2>&1 & WORKER_PID=$!
}

start_vite() {
  local port; port="$(allocate_port)"; VITE_BASE_URL="http://127.0.0.1:${port}"
  (cd "${REPOSITORY_ROOT}" && unset VITE_API_BASE_URL; export VITE_API_PROXY_TARGET="${API_BASE_URL}"; exec python3 -c 'import os, sys; os.setsid(); os.execvp("npm", ["npm", "run", "dev", "--prefix", "web", "--", "--host", "127.0.0.1", "--port", sys.argv[1]])' "${port}") >"${STATE_DIR}/vite.log" 2>&1 & VITE_PID=$!
}

run_playwright() {
  (cd "${REPOSITORY_ROOT}" && ZHIXU_PLAYWRIGHT_BASE_URL="${VITE_BASE_URL}" ZHIXU_ARTIFACT_SMOKE_BASE_URL="${VITE_BASE_URL}" ZHIXU_ARTIFACT_SMOKE_WORKSPACE_ID="${WORKSPACE_ID}" ZHIXU_ARTIFACT_SMOKE_SOURCE_VERSION_ID="${SMOKE_SOURCE_VERSION_ID}" ZHIXU_ARTIFACT_SMOKE_SOURCE_SPAN_ID="${SMOKE_SOURCE_SPAN_ID}" ZHIXU_PLAYWRIGHT_EXECUTABLE_PATH="${BROWSER_EXECUTABLE}" ZHIXU_PLAYWRIGHT_OUTPUT_DIR="${STATE_DIR}/playwright" npm run test:e2e --prefix web -- artifact.smoke.spec.ts) >"${STATE_DIR}/playwright.log" 2>&1 || fail "Playwright Artifact browser smoke failed"
}

main() {
  [[ -n "${ZHIXU_TEST_DATABASE_URL:-}" ]] || fail "ZHIXU_TEST_DATABASE_URL is required"
  require_command curl; require_command git; require_command go; require_command jq; require_command node; require_command npm; require_command python3; require_command uuidgen
  resolve_browser
  STATE_DIR="$(mktemp -d "${TMPDIR:-/tmp}/zhixu-artifact-browser-smoke.XXXXXX")" || fail "could not allocate disposable state"
  chmod 0700 "${STATE_DIR}"; trap cleanup EXIT INT TERM
  write_dbtool; create_database; migrate_database; create_workspace; seed_provenance; seed_eligible_knowledge; start_model; wait_for_model; start_api; start_worker
  wait_for_ready "${API_PID}" "${API_BASE_URL}" "API" "${STATE_DIR}/api-ready.json"
  wait_for_ready "${WORKER_PID}" "${WORKER_HEALTH_BASE_URL}" "Worker" "${STATE_DIR}/worker-ready.json"
  start_vite; wait_for_vite; run_playwright
  stop_vite; stop_worker; stop_api; stop_model
  assert_log_safe "${STATE_DIR}/migrate.log" "migration log"; assert_log_safe "${STATE_DIR}/workspace.log" "workspace setup log"; assert_log_safe "${STATE_DIR}/provenance.log" "provenance setup log"; assert_log_safe "${STATE_DIR}/eligibility-fixture.log" "eligibility fixture log"; assert_log_safe "${STATE_DIR}/model.log" "fake model log"; assert_log_safe "${STATE_DIR}/api.log" "API log"; assert_log_safe "${STATE_DIR}/worker.log" "Worker log"; assert_log_safe "${STATE_DIR}/vite.log" "Vite log"; assert_log_safe "${STATE_DIR}/playwright.log" "Playwright log"
  log "passed: disposable database and Git workspace, fixture-qualified Evidence, loopback fake model, real API/Worker/Vite, isolated Artifact lifecycle, desktop/mobile checks"
}

main "$@"
