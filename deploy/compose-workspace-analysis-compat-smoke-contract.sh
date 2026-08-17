#!/usr/bin/env bash

set -Eeuo pipefail

readonly SCRIPT_DIR="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
readonly REPOSITORY_ROOT="$(cd -- "${SCRIPT_DIR}/.." && pwd)"
readonly SMOKE_SCRIPT="${SCRIPT_DIR}/compose-workspace-analysis-compat-smoke.sh"

fail() { printf '[compose-workspace-analysis-compat-smoke-contract] failed: %s\n' "$1" >&2; exit 1; }

[[ -x "${SMOKE_SCRIPT}" || -f "${SMOKE_SCRIPT}" ]] || fail 'compatibility smoke script is missing'
bash -n "${SMOKE_SCRIPT}"

grep -Fq 'git archive --format=tar "${resolved_legacy_ref}"' "${SMOKE_SCRIPT}" || fail 'legacy binaries are not built from a resolved frozen commit'
legacy_ref="$(sed -n 's/^readonly DEFAULT_LEGACY_REF=\([0-9a-f]\{40\}\)$/\1/p' "${SMOKE_SCRIPT}")"
[[ "${legacy_ref}" =~ ^[0-9a-f]{40}$ ]] || fail 'default legacy commit is not frozen canonically'
git -C "${REPOSITORY_ROOT}" cat-file -e "${legacy_ref}^{commit}" || fail 'default legacy commit is unavailable'
if git -C "${REPOSITORY_ROOT}" cat-file -e "${legacy_ref}:migrations/00085_workspace_analysis_persistence.sql" 2>/dev/null; then
  fail 'default legacy commit already contains Workspace Analysis persistence'
fi
grep -Fq 'ZHIXU_WORKSPACE_ANALYSIS_COMPAT_PAIR:-all' "${SMOKE_SCRIPT}" || fail 'bounded single-pair diagnostics are missing'
grep -Fq 'safe retrieval projection=' "${SMOKE_SCRIPT}" || fail 'safe fixed RAG retrieval diagnosis is missing'
grep -Fq '.error_code // .code // ""' "${SMOKE_SCRIPT}" || fail 'safe retrieval diagnosis does not support the public degradation shape'
projection="$(jq -nr '{items:[{}],effective_mode:"keyword",degradations:[{error_code:"VECTOR_UNAVAILABLE"}]} | [(.items | length),(.effective_mode // ""),((.degradations // []) | map(.error_code // .code // "") | sort | join(","))] | join("|")')"
[[ "${projection}" == '1|keyword|VECTOR_UNAVAILABLE' ]] || fail 'safe retrieval projection is not executable'
grep -Fq 'retrieval_projection_snapshot()' "${SMOKE_SCRIPT}" || fail 'frozen retrieval projection snapshot is missing'
grep -Fq 'valid_retrieval_projection_snapshot()' "${SMOKE_SCRIPT}" || fail 'seeded retrieval projection validity check is missing'
grep -Fq 'seeded retrieval projection does not contain the fixed RAG evidence' "${SMOKE_SCRIPT}" || fail 'fixed RAG evidence is not required before binary switching'
grep -Fq 'EVIDENCE_TOKEN="durable-rag-${run_id}"' "${SMOKE_SCRIPT}" || fail 'compatibility gate does not reuse the deterministic RAG evidence token contract'
grep -Fq "go test -tags=integration -count=1 -run '^TestComposeRAGKnowledgeSeedExternalFixture$' ./cmd/worker" "${SMOKE_SCRIPT}" || fail 'retrievable evidence is not qualified as formal Knowledge before fixed RAG runs'
grep -Fq 'ZHIXU_RAG_FIXTURE_WORKSPACE_ID="${WORKSPACE_ID}"' "${SMOKE_SCRIPT}" || fail 'Knowledge qualification is not bound to the active Workspace'
grep -Fq 'ZHIXU_RAG_FIXTURE_SOURCE_VERSION_ID="${SOURCE_VERSION_ID}"' "${SMOKE_SCRIPT}" || fail 'Knowledge qualification is not bound to the seeded Source Version'
grep -Fq 'ZHIXU_RAG_FIXTURE_SOURCE_SPAN_ID="${SOURCE_SPAN_ID}"' "${SMOKE_SCRIPT}" || fail 'Knowledge qualification is not bound to the seeded Source Span'
grep -Fq '[[ "${retrieval_snapshot}" == "${RETRIEVAL_BASELINE}" ]]' "${SMOKE_SCRIPT}" || fail 'binary switches do not preserve the seeded retrieval projection'
grep -Fq "projection.search_vector @@ websearch_to_tsquery('simple','approved recovery')" "${SMOKE_SCRIPT}" || fail 'retrieval snapshot does not prove the fixed rewrite remains searchable'
grep -Fq 'image: "${PROJECT_NAME}-rag-model-fixture"' "${SMOKE_SCRIPT}" || fail 'materialized runtime model needs a fixture image reference'
grep -Fq 'bootstrap_compose run --rm --no-deps -T migrate' "${SMOKE_SCRIPT}" || fail 'current migration gate is missing'
grep -Fq '# Only the current tree' "${SMOKE_SCRIPT}" || fail 'current-only migration ownership must be documented'
grep -Fq 'compose build --quiet rag-model-fixture' "${SMOKE_SCRIPT}" || fail 'fixture image is not built locally before startup'
grep -Fq 'run_selected_pair legacy_api_legacy_worker legacy legacy' "${SMOKE_SCRIPT}" || fail 'legacy/legacy pair is missing'
grep -Fq 'run_selected_pair legacy_api_current_worker legacy current' "${SMOKE_SCRIPT}" || fail 'legacy/current pair is missing'
grep -Fq 'run_selected_pair current_api_legacy_worker current legacy' "${SMOKE_SCRIPT}" || fail 'current/legacy pair is missing'
grep -Fq 'run_selected_pair current_api_current_worker current current' "${SMOKE_SCRIPT}" || fail 'current/current pair is missing'
grep -Fq 'case "${PAIR_FILTER}" in' "${SMOKE_SCRIPT}" || fail 'filtered compatibility runs can falsely report the full matrix'
grep -Fq 'all)' "${SMOKE_SCRIPT}" || fail 'the full-matrix success branch is missing'
current_legacy_line="$(grep -nF 'run_selected_pair current_api_legacy_worker current legacy' "${SMOKE_SCRIPT}" | cut -d: -f1)"
legacy_current_line="$(grep -nF 'run_selected_pair legacy_api_current_worker legacy current' "${SMOKE_SCRIPT}" | cut -d: -f1)"
[[ "${current_legacy_line}" -lt "${legacy_current_line}" ]] || fail 'current API plus legacy Worker runs after a current Worker advertisement can leak into the matrix'
grep -Fq 'ZHIXU_WORKSPACE_ANALYSIS_WORKER_ENABLED: "${worker_enabled}"' "${SMOKE_SCRIPT}" || fail 'worker feature gate must remain explicit per pair'
grep -Fq 'WORKSPACE_ANALYSIS_CAPABILITY_UNAVAILABLE' "${SMOKE_SCRIPT}" || fail 'current API capability fail-closed assertion is missing'
grep -Fq '.error_code == "INVALID_JSON"' "${SMOKE_SCRIPT}" || fail 'legacy API strict unknown-field assertion is missing'
grep -Fq 'SELECT count(*) FROM agent.workspace_analysis_run' "${SMOKE_SCRIPT}" || fail 'zero Workspace Analysis facts assertion is missing'
grep -Fq 'run_post_fact_rollback "${current_root}" "${legacy_root}"' "${SMOKE_SCRIPT}" || fail 'post-fact Worker-only rollback phase is missing'
grep -Fq 'post_fact_current_api_legacy_worker' "${SMOKE_SCRIPT}" || fail 'post-fact rollback diagnostic selector is missing'
grep -Fq 'question.mode=' "${SMOKE_SCRIPT}" || fail 'post-fact route marker does not bind the persisted Question mode'
grep -Fq 'analysis.answer_id=answer.id' "${SMOKE_SCRIPT}" || fail 'post-fact route marker does not bind the Analysis Run to its Answer'
grep -Fq "[[ \"\${fact_counts}\" == '1|1' ]]" "${SMOKE_SCRIPT}" || fail 'post-fact fixture does not require exactly one Workspace Analysis Question and Run'
grep -Fq '[[ "${marker}" == '\''1|1|0|0'\'' ]]' "${SMOKE_SCRIPT}" || fail 'post-fact route marker is not fail closed on split facts'
grep -Fq 'api_image_after' "${SMOKE_SCRIPT}" || fail 'post-fact rollback does not verify the compatible API image'
grep -Fq 'docker inspect --format '\''{{.Image}}'\''' "${SMOKE_SCRIPT}" || fail 'post-fact rollback does not inspect the image used by each running container'
grep -Fq '[[ "${api_image_after}" == "${api_image_before}" ]]' "${SMOKE_SCRIPT}" || fail 'post-fact rollback may replace the compatible API image'
grep -Fq '[[ "${worker_image_after}" != "${worker_image_before}"' "${SMOKE_SCRIPT}" || fail 'post-fact rollback does not prove the Worker image changed'
grep -Fq '[[ "${netns_after}" == "${netns_before}" ]]' "${SMOKE_SCRIPT}" || fail 'post-fact rollback does not preserve the API ingress namespace'
grep -Fq '/turns?workspace_id=${WORKSPACE_ID}&latest=true' "${SMOKE_SCRIPT}" || fail 'post-fact rollback does not reread the historical turn'
grep -Fq '/analysis-timeline?workspace_id=${WORKSPACE_ID}' "${SMOKE_SCRIPT}" || fail 'post-fact rollback does not reread the historical timeline'
grep -Fq 'historical Workspace Analysis SSE replay request failed' "${SMOKE_SCRIPT}" || fail 'post-fact rollback does not replay historical SSE'
grep -Fq 'workspace_analysis:${ROLLBACK_ANALYSIS_RUN_ID}' "${SMOKE_SCRIPT}" || fail 'historical SSE is not bound to the rollback Analysis Run'
grep -Fq '.payload_summary.answer_id==$answer' "${SMOKE_SCRIPT}" || fail 'historical SSE is not bound to the rollback Answer'
grep -Fq '((.id|tonumber)>$watermark)' "${SMOKE_SCRIPT}" || fail 'historical SSE does not prove replay after the captured watermark'
grep -Fq 'assert_workspace_analysis_post_fact_rejected "${pair}"' "${SMOKE_SCRIPT}" || fail 'post-fact rollback does not reject new Workspace Analysis admission'
grep -Fq '.retryable==false' "${SMOKE_SCRIPT}" || fail 'post-fact capability rejection must remain non-retryable'
grep -Fq 'rejected post-fact admission persisted new Workspace Analysis facts' "${SMOKE_SCRIPT}" || fail 'post-fact admission rejection does not compare Workspace Analysis fact counts'
grep -Fq 'fixed RAG after Worker rollback mutated Workspace Analysis facts' "${SMOKE_SCRIPT}" || fail 'post-fact rollback does not prove fixed RAG continuity and fact immutability'
grep -Fq 'fixed RAG after Worker rollback persisted Workspace Analysis facts' "${SMOKE_SCRIPT}" || fail 'post-fact fixed RAG does not compare Workspace Analysis fact counts'
grep -Fq 'post-fact current-API/legacy-Worker rollback retained the canonical Workspace Analysis facts' "${SMOKE_SCRIPT}" || fail 'filtered post-fact success must not claim zero Workspace Analysis facts'
grep -Fq 'cleanup_compose_smoke_project_images' "${SMOKE_SCRIPT}" || fail 'compose cleanup is missing'
grep -Fq 'run_rag "${pair}"' "${SMOKE_SCRIPT}" || fail 'every pair must prove fixed RAG continuity'

printf '[compose-workspace-analysis-compat-smoke-contract] passed\n'
