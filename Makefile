SHELL := /bin/sh
DOCKER_COMPOSE ?= docker compose
ATLAS_IMAGE ?= arigaio/atlas@sha256:dce85fd3f83c9c28f343c73236c8917f802d526f1fe921e150b720077a83c5ae
POSTGRES_CLIENT_IMAGE ?= pgvector/pgvector@sha256:1d533553fefe4f12e5d80c7b80622ba0c382abb5758856f52983d8789179f0fb

.PHONY: test migrate go-test go-vet testcontainers-integration atlas-schema-inspect atlas-schema-drift atlas-migrate-hash atlas-migrate-hash-check atlas-migrate-lint atlas-migrate-lint-pro atlas-migrate-validate web-install web-lint web-typecheck web-test web-build eino-test eino-vet eino-live-smoke eino-live-smoke-env eino-live-smoke-contract eino-live-chat-smoke eino-live-query-plan-smoke eino-live-rag-metadata-smoke eino-live-openai-embedding-smoke eino-live-ollama-embedding-smoke eino-stable-observation-test eino-stable-observation-preflight eino-stable-observation-start eino-stable-observation-day eino-stable-observation-attest eino-stable-observation-verify agent-eval semantic-link-eval openapi-check trellis-script-test task-context-check auth-integration tool-integration rag-integration workspace-analysis-integration workspace-analysis-terminal-matrix workspace-analysis-fault-smoke graph-integration graph-smoke graph-benchmark benchmark-capacity semantic-link-integration semantic-link-fault-smoke semantic-link-browser-smoke semantic-link-smoke collection-health-integration collection-health-fault-smoke collection-health-benchmark collection-health-browser-smoke collection-health-secret-scan collection-health-smoke artifact-browser-smoke m8-learning-browser-smoke export-browser-smoke timeline-impact-integration timeline-impact-fault-smoke timeline-impact-worker-smoke compose-auth-check compose-runtime-check compose-bootstrap-check compose-runtime-contract compose-netns-check compose-netns-contract compose-workspace-check compose-workspace-contract compose-static-models-check compose-smoke-cleanup-contract smoke-image-cleanup-contract compose-rag-real-provider-contract compose-workspace-analysis-compat-contract compose-workspace-analysis-worker-restart-contract compose-workspace-analysis-otlp-contract compose-auth-smoke compose-check launcher-contract model-secrets-init-contract architecture-quality-baseline docker-build compose-up compose-down compose-reset compose-search-smoke compose-tool-smoke compose-rag-smoke compose-rag-browser-smoke compose-workspace-analysis-smoke compose-workspace-analysis-otlp-smoke compose-workspace-analysis-compat-smoke compose-workspace-analysis-worker-restart-smoke compose-rag-real-provider-preflight compose-rag-real-provider-smoke compose-model-runtime-hot-activation-smoke
.PHONY: openapi-install openapi-lint openapi-project-check openapi-route-check openapi-tags-check openapi-generated-typecheck openapi-generate openapi-generate-check openapi-breaking-check
.PHONY: persistence-check

test: persistence-check trellis-script-test task-context-check go-test go-vet web-lint web-typecheck web-test web-build eino-test eino-vet eino-live-smoke-contract eino-stable-observation-test agent-eval openapi-check compose-check

persistence-check:
	go run ./cmd/persistencecheck

migrate:
	go run ./cmd/migrate

testcontainers-integration:
	go test -count=1 -timeout=5m -tags='integration testcontainers' ./internal/platform/testdb

atlas-schema-inspect:
	@test -n "$${ZHIXU_DATABASE_URL:-}" || (echo "ZHIXU_DATABASE_URL is required" >&2; exit 1)
	docker run --rm --network host -e ATLAS_NO_UPDATE_NOTIFIER=1 -e ZHIXU_DATABASE_URL \
		-v "$$(pwd):/workspace" -w /workspace \
		$(ATLAS_IMAGE) schema inspect --env local

atlas-schema-drift:
	ATLAS_IMAGE="$(ATLAS_IMAGE)" POSTGRES_CLIENT_IMAGE="$(POSTGRES_CLIENT_IMAGE)" \
		deploy/atlas-schema-drift.sh

atlas-migrate-hash:
	docker run --rm -e ATLAS_NO_UPDATE_NOTIFIER=1 -v "$$(pwd):/workspace" -w /workspace \
		$(ATLAS_IMAGE) migrate hash --dir file://atlas/migrations

atlas-migrate-hash-check:
	@set -e; \
	before=$$(git hash-object atlas/migrations/atlas.sum); \
	$(MAKE) --no-print-directory atlas-migrate-hash >/dev/null; \
	after=$$(git hash-object atlas/migrations/atlas.sum); \
	if test "$$before" != "$$after"; then \
		echo "atlas.sum is out of date; run make atlas-migrate-hash and review the result" >&2; \
		exit 1; \
	fi

atlas-migrate-lint:
	deploy/atlas-migration-lint.sh

atlas-migrate-lint-pro:
	@test -n "$${ATLAS_TOKEN:-}" || (echo "ATLAS_TOKEN is required for Atlas Pro migration lint" >&2; exit 1)
	@test -n "$${ZHIXU_ATLAS_LINT_DEV_URL:-}" || (echo "ZHIXU_ATLAS_LINT_DEV_URL is required for Atlas Pro migration lint" >&2; exit 1)
	docker run --rm --network host -e ATLAS_NO_UPDATE_NOTIFIER=1 -e ATLAS_TOKEN \
		-v "$$(pwd):/workspace" -w /workspace \
		$(ATLAS_IMAGE) migrate lint --dir file://atlas/migrations --dev-url "$${ZHIXU_ATLAS_LINT_DEV_URL}"

atlas-migrate-validate:
	docker run --rm -e ATLAS_NO_UPDATE_NOTIFIER=1 -v "$$(pwd):/workspace" -w /workspace \
		$(ATLAS_IMAGE) migrate validate --dir file://atlas/migrations

go-test:
	go test ./cmd/... ./internal/...

go-vet:
	go vet ./cmd/... ./internal/...

web-install:
	npm ci --prefix web

web-lint:
	npm run lint --prefix web

web-typecheck:
	npm run typecheck --prefix web

web-test:
	npm run test --prefix web
	npm run test:controller --prefix web

web-build:
	npm run build --prefix web

eino-test:
	cd poc/eino && go test -race ./...

eino-vet:
	cd poc/eino && go vet ./...

eino-live-smoke:
	$(MAKE) --no-print-directory eino-live-smoke-env
	$(MAKE) --no-print-directory eino-live-chat-smoke
	$(MAKE) --no-print-directory eino-live-openai-embedding-smoke
	$(MAKE) --no-print-directory eino-live-ollama-embedding-smoke
	$(MAKE) --no-print-directory eino-live-query-plan-smoke
	$(MAKE) --no-print-directory eino-live-rag-metadata-smoke
	$(MAKE) --no-print-directory eino-live-faithfulness-smoke

eino-live-smoke-env:
	@test "$${ZHIXU_EINO_LIVE_ENABLED:-}" = true || (echo "ZHIXU_EINO_LIVE_ENABLED=true is required" >&2; exit 1)
	@test -n "$${ZHIXU_EINO_LIVE_BASE_URL:-}" || (echo "ZHIXU_EINO_LIVE_BASE_URL is required" >&2; exit 1)
	@test -n "$${ZHIXU_EINO_LIVE_API_KEY:-}" || (echo "ZHIXU_EINO_LIVE_API_KEY is required" >&2; exit 1)
	@test -n "$${ZHIXU_EINO_LIVE_MODEL:-}" || (echo "ZHIXU_EINO_LIVE_MODEL is required" >&2; exit 1)
	@test "$${ZHIXU_EINO_LIVE_OPENAI_EMBEDDING_ENABLED:-}" = true || (echo "ZHIXU_EINO_LIVE_OPENAI_EMBEDDING_ENABLED=true is required" >&2; exit 1)
	@test -n "$${ZHIXU_EINO_LIVE_OPENAI_EMBEDDING_BASE_URL:-}" || (echo "ZHIXU_EINO_LIVE_OPENAI_EMBEDDING_BASE_URL is required" >&2; exit 1)
	@test -n "$${ZHIXU_EINO_LIVE_OPENAI_EMBEDDING_API_KEY:-}" || (echo "ZHIXU_EINO_LIVE_OPENAI_EMBEDDING_API_KEY is required" >&2; exit 1)
	@test -n "$${ZHIXU_EINO_LIVE_OPENAI_EMBEDDING_MODEL:-}" || (echo "ZHIXU_EINO_LIVE_OPENAI_EMBEDDING_MODEL is required" >&2; exit 1)
	@test -n "$${ZHIXU_EINO_LIVE_OPENAI_EMBEDDING_DIMENSIONS:-}" || (echo "ZHIXU_EINO_LIVE_OPENAI_EMBEDDING_DIMENSIONS is required" >&2; exit 1)
	@test "$${ZHIXU_EINO_LIVE_OLLAMA_EMBEDDING_ENABLED:-}" = true || (echo "ZHIXU_EINO_LIVE_OLLAMA_EMBEDDING_ENABLED=true is required" >&2; exit 1)
	@test -n "$${ZHIXU_EINO_LIVE_OLLAMA_EMBEDDING_BASE_URL:-}" || (echo "ZHIXU_EINO_LIVE_OLLAMA_EMBEDDING_BASE_URL is required" >&2; exit 1)
	@test -n "$${ZHIXU_EINO_LIVE_OLLAMA_EMBEDDING_MODEL:-}" || (echo "ZHIXU_EINO_LIVE_OLLAMA_EMBEDDING_MODEL is required" >&2; exit 1)
	@test -n "$${ZHIXU_EINO_LIVE_OLLAMA_EMBEDDING_DIMENSIONS:-}" || (echo "ZHIXU_EINO_LIVE_OLLAMA_EMBEDDING_DIMENSIONS is required" >&2; exit 1)
	@test "$${ZHIXU_EINO_LIVE_QUERY_PLAN_ENABLED:-}" = true || (echo "ZHIXU_EINO_LIVE_QUERY_PLAN_ENABLED=true is required" >&2; exit 1)
	@test "$${ZHIXU_EINO_LIVE_RAG_METADATA_ENABLED:-}" = true || (echo "ZHIXU_EINO_LIVE_RAG_METADATA_ENABLED=true is required" >&2; exit 1)
	@test "$${ZHIXU_EINO_LIVE_FAITHFULNESS_ENABLED:-}" = true || (echo "ZHIXU_EINO_LIVE_FAITHFULNESS_ENABLED=true is required" >&2; exit 1)

eino-live-smoke-contract:
	bash deploy/eino-live-smoke-contract.sh

eino-live-chat-smoke:
	@test "$${ZHIXU_EINO_LIVE_ENABLED:-}" = true || (echo "ZHIXU_EINO_LIVE_ENABLED=true is required" >&2; exit 1)
	@test -n "$${ZHIXU_EINO_LIVE_BASE_URL:-}" || (echo "ZHIXU_EINO_LIVE_BASE_URL is required" >&2; exit 1)
	@test -n "$${ZHIXU_EINO_LIVE_API_KEY:-}" || (echo "ZHIXU_EINO_LIVE_API_KEY is required" >&2; exit 1)
	@test -n "$${ZHIXU_EINO_LIVE_MODEL:-}" || (echo "ZHIXU_EINO_LIVE_MODEL is required" >&2; exit 1)
	go test -count=1 -run '^TestEinoOpenAIChatModelLiveSmoke$$' -v ./internal/platform/models

eino-live-query-plan-smoke:
	@test "$${ZHIXU_EINO_LIVE_QUERY_PLAN_ENABLED:-}" = true || (echo "ZHIXU_EINO_LIVE_QUERY_PLAN_ENABLED=true is required" >&2; exit 1)
	@test -n "$${ZHIXU_EINO_LIVE_BASE_URL:-}" || (echo "ZHIXU_EINO_LIVE_BASE_URL is required" >&2; exit 1)
	@test -n "$${ZHIXU_EINO_LIVE_API_KEY:-}" || (echo "ZHIXU_EINO_LIVE_API_KEY is required" >&2; exit 1)
	@test -n "$${ZHIXU_EINO_LIVE_MODEL:-}" || (echo "ZHIXU_EINO_LIVE_MODEL is required" >&2; exit 1)
	go test -count=1 -run '^TestEinoOpenAIQueryPlanLiveSmoke$$' -v ./internal/platform/models

eino-live-rag-metadata-smoke:
	@test "$${ZHIXU_EINO_LIVE_RAG_METADATA_ENABLED:-}" = true || (echo "ZHIXU_EINO_LIVE_RAG_METADATA_ENABLED=true is required" >&2; exit 1)
	@test -n "$${ZHIXU_EINO_LIVE_BASE_URL:-}" || (echo "ZHIXU_EINO_LIVE_BASE_URL is required" >&2; exit 1)
	@test -n "$${ZHIXU_EINO_LIVE_API_KEY:-}" || (echo "ZHIXU_EINO_LIVE_API_KEY is required" >&2; exit 1)
	@test -n "$${ZHIXU_EINO_LIVE_MODEL:-}" || (echo "ZHIXU_EINO_LIVE_MODEL is required" >&2; exit 1)
	go test -count=1 -run '^TestEinoOpenAIRAGMetadataLiveSmoke$$' -v ./internal/platform/models

eino-live-faithfulness-smoke:
	@test "$${ZHIXU_EINO_LIVE_FAITHFULNESS_ENABLED:-}" = true || (echo "ZHIXU_EINO_LIVE_FAITHFULNESS_ENABLED=true is required" >&2; exit 1)
	@test -n "$${ZHIXU_EINO_LIVE_BASE_URL:-}" || (echo "ZHIXU_EINO_LIVE_BASE_URL is required" >&2; exit 1)
	@test -n "$${ZHIXU_EINO_LIVE_API_KEY:-}" || (echo "ZHIXU_EINO_LIVE_API_KEY is required" >&2; exit 1)
	@test -n "$${ZHIXU_EINO_LIVE_MODEL:-}" || (echo "ZHIXU_EINO_LIVE_MODEL is required" >&2; exit 1)
	go test -count=1 -run '^TestEinoOpenAIFaithfulnessReviewLiveSmoke$$' -v ./internal/platform/models

eino-live-openai-embedding-smoke:
	@test "$${ZHIXU_EINO_LIVE_OPENAI_EMBEDDING_ENABLED:-}" = true || (echo "ZHIXU_EINO_LIVE_OPENAI_EMBEDDING_ENABLED=true is required" >&2; exit 1)
	@test -n "$${ZHIXU_EINO_LIVE_OPENAI_EMBEDDING_BASE_URL:-}" || (echo "ZHIXU_EINO_LIVE_OPENAI_EMBEDDING_BASE_URL is required" >&2; exit 1)
	@test -n "$${ZHIXU_EINO_LIVE_OPENAI_EMBEDDING_API_KEY:-}" || (echo "ZHIXU_EINO_LIVE_OPENAI_EMBEDDING_API_KEY is required" >&2; exit 1)
	@test -n "$${ZHIXU_EINO_LIVE_OPENAI_EMBEDDING_MODEL:-}" || (echo "ZHIXU_EINO_LIVE_OPENAI_EMBEDDING_MODEL is required" >&2; exit 1)
	@test -n "$${ZHIXU_EINO_LIVE_OPENAI_EMBEDDING_DIMENSIONS:-}" || (echo "ZHIXU_EINO_LIVE_OPENAI_EMBEDDING_DIMENSIONS is required" >&2; exit 1)
	go test -count=1 -run '^TestEinoOpenAIEmbeddingLiveSmoke$$' -v ./internal/platform/models

eino-live-ollama-embedding-smoke:
	@test "$${ZHIXU_EINO_LIVE_OLLAMA_EMBEDDING_ENABLED:-}" = true || (echo "ZHIXU_EINO_LIVE_OLLAMA_EMBEDDING_ENABLED=true is required" >&2; exit 1)
	@test -n "$${ZHIXU_EINO_LIVE_OLLAMA_EMBEDDING_BASE_URL:-}" || (echo "ZHIXU_EINO_LIVE_OLLAMA_EMBEDDING_BASE_URL is required" >&2; exit 1)
	@test -n "$${ZHIXU_EINO_LIVE_OLLAMA_EMBEDDING_MODEL:-}" || (echo "ZHIXU_EINO_LIVE_OLLAMA_EMBEDDING_MODEL is required" >&2; exit 1)
	@test -n "$${ZHIXU_EINO_LIVE_OLLAMA_EMBEDDING_DIMENSIONS:-}" || (echo "ZHIXU_EINO_LIVE_OLLAMA_EMBEDDING_DIMENSIONS is required" >&2; exit 1)
	go test -count=1 -run '^TestEinoOllamaEmbeddingLiveSmoke$$' -v ./internal/platform/models

eino-stable-observation-test:
	python3 -m unittest discover -s deploy -p 'eino_stable_observation_*_test.py'

eino-stable-observation-preflight:
	@test -n "$${ZHIXU_EINO_OBSERVATION_ARCHIVE_DIR:-}" || (echo "ZHIXU_EINO_OBSERVATION_ARCHIVE_DIR is required" >&2; exit 1)
	@test -n "$${ZHIXU_EINO_OBSERVATION_PROMETHEUS_URL_FILE:-}" || (echo "ZHIXU_EINO_OBSERVATION_PROMETHEUS_URL_FILE is required" >&2; exit 1)
	@test -n "$${ZHIXU_EINO_OBSERVATION_PROMETHEUS_BEARER_TOKEN_FILE:-}" || (echo "ZHIXU_EINO_OBSERVATION_PROMETHEUS_BEARER_TOKEN_FILE is required" >&2; exit 1)
	@test -n "$${ZHIXU_EINO_OBSERVATION_TEMPO_URL_FILE:-}" || (echo "ZHIXU_EINO_OBSERVATION_TEMPO_URL_FILE is required" >&2; exit 1)
	@test -n "$${ZHIXU_EINO_OBSERVATION_TEMPO_BEARER_TOKEN_FILE:-}" || (echo "ZHIXU_EINO_OBSERVATION_TEMPO_BEARER_TOKEN_FILE is required" >&2; exit 1)
	@test -n "$${ZHIXU_EINO_OBSERVATION_TIMEZONE:-}" || (echo "ZHIXU_EINO_OBSERVATION_TIMEZONE is required" >&2; exit 1)
	@test -n "$${ZHIXU_EINO_OBSERVATION_IMAGE_DIGEST_SHA256:-}" || (echo "ZHIXU_EINO_OBSERVATION_IMAGE_DIGEST_SHA256 is required" >&2; exit 1)
	@test -n "$${ZHIXU_EINO_OBSERVATION_CONFIG_SHA256:-}" || (echo "ZHIXU_EINO_OBSERVATION_CONFIG_SHA256 is required" >&2; exit 1)
	@test -n "$${ZHIXU_EINO_OBSERVATION_EMBEDDING_GATE_SHA256:-}" || (echo "ZHIXU_EINO_OBSERVATION_EMBEDDING_GATE_SHA256 is required" >&2; exit 1)
	@test -n "$${ZHIXU_EINO_OBSERVATION_ATTESTATION_KEY_FILE:-}" || (echo "ZHIXU_EINO_OBSERVATION_ATTESTATION_KEY_FILE is required" >&2; exit 1)
	python3 deploy/eino_stable_observation_collect.py preflight \
		--archive-dir "$${ZHIXU_EINO_OBSERVATION_ARCHIVE_DIR}" \
		--timezone "$${ZHIXU_EINO_OBSERVATION_TIMEZONE}" \
		--image-digest-sha256 "$${ZHIXU_EINO_OBSERVATION_IMAGE_DIGEST_SHA256}" \
		--config-sha256 "$${ZHIXU_EINO_OBSERVATION_CONFIG_SHA256}" \
		--embedding-gate-sha256 "$${ZHIXU_EINO_OBSERVATION_EMBEDDING_GATE_SHA256}" \
		--attestation-key-file "$${ZHIXU_EINO_OBSERVATION_ATTESTATION_KEY_FILE}" \
		--prometheus-url-file "$${ZHIXU_EINO_OBSERVATION_PROMETHEUS_URL_FILE}" \
		--prometheus-bearer-token-file "$${ZHIXU_EINO_OBSERVATION_PROMETHEUS_BEARER_TOKEN_FILE}" \
		--tempo-url-file "$${ZHIXU_EINO_OBSERVATION_TEMPO_URL_FILE}" \
		--tempo-bearer-token-file "$${ZHIXU_EINO_OBSERVATION_TEMPO_BEARER_TOKEN_FILE}"

eino-stable-observation-start: eino-stable-observation-preflight
	python3 deploy/eino_stable_observation_collect.py start \
		--archive-dir "$${ZHIXU_EINO_OBSERVATION_ARCHIVE_DIR}" \
		--timezone "$${ZHIXU_EINO_OBSERVATION_TIMEZONE}" \
		--image-digest-sha256 "$${ZHIXU_EINO_OBSERVATION_IMAGE_DIGEST_SHA256}" \
		--config-sha256 "$${ZHIXU_EINO_OBSERVATION_CONFIG_SHA256}" \
		--embedding-gate-sha256 "$${ZHIXU_EINO_OBSERVATION_EMBEDDING_GATE_SHA256}" \
		--attestation-key-file "$${ZHIXU_EINO_OBSERVATION_ATTESTATION_KEY_FILE}" \
		--prometheus-url-file "$${ZHIXU_EINO_OBSERVATION_PROMETHEUS_URL_FILE}" \
		--prometheus-bearer-token-file "$${ZHIXU_EINO_OBSERVATION_PROMETHEUS_BEARER_TOKEN_FILE}" \
		--tempo-url-file "$${ZHIXU_EINO_OBSERVATION_TEMPO_URL_FILE}" \
		--tempo-bearer-token-file "$${ZHIXU_EINO_OBSERVATION_TEMPO_BEARER_TOKEN_FILE}"

eino-stable-observation-day:
	@test -n "$${ZHIXU_EINO_OBSERVATION_ARCHIVE_DIR:-}" || (echo "ZHIXU_EINO_OBSERVATION_ARCHIVE_DIR is required" >&2; exit 1)
	@test -n "$${ZHIXU_EINO_OBSERVATION_PROMETHEUS_URL_FILE:-}" || (echo "ZHIXU_EINO_OBSERVATION_PROMETHEUS_URL_FILE is required" >&2; exit 1)
	@test -n "$${ZHIXU_EINO_OBSERVATION_PROMETHEUS_BEARER_TOKEN_FILE:-}" || (echo "ZHIXU_EINO_OBSERVATION_PROMETHEUS_BEARER_TOKEN_FILE is required" >&2; exit 1)
	@test -n "$${ZHIXU_EINO_OBSERVATION_TEMPO_URL_FILE:-}" || (echo "ZHIXU_EINO_OBSERVATION_TEMPO_URL_FILE is required" >&2; exit 1)
	@test -n "$${ZHIXU_EINO_OBSERVATION_TEMPO_BEARER_TOKEN_FILE:-}" || (echo "ZHIXU_EINO_OBSERVATION_TEMPO_BEARER_TOKEN_FILE is required" >&2; exit 1)
	@test -n "$${ZHIXU_EINO_OBSERVATION_IMAGE_DIGEST_SHA256:-}" || (echo "ZHIXU_EINO_OBSERVATION_IMAGE_DIGEST_SHA256 is required" >&2; exit 1)
	@test -n "$${ZHIXU_EINO_OBSERVATION_CONFIG_SHA256:-}" || (echo "ZHIXU_EINO_OBSERVATION_CONFIG_SHA256 is required" >&2; exit 1)
	@test -n "$${ZHIXU_EINO_OBSERVATION_INCIDENT_EVIDENCE:-}" || (echo "ZHIXU_EINO_OBSERVATION_INCIDENT_EVIDENCE is required" >&2; exit 1)
	@test -n "$${ZHIXU_EINO_OBSERVATION_ATTESTATION_KEY_FILE:-}" || (echo "ZHIXU_EINO_OBSERVATION_ATTESTATION_KEY_FILE is required" >&2; exit 1)
	python3 deploy/eino_stable_observation_collect.py day \
		--archive-dir "$${ZHIXU_EINO_OBSERVATION_ARCHIVE_DIR}" \
		--image-digest-sha256 "$${ZHIXU_EINO_OBSERVATION_IMAGE_DIGEST_SHA256}" \
		--config-sha256 "$${ZHIXU_EINO_OBSERVATION_CONFIG_SHA256}" \
		--incident-evidence "$${ZHIXU_EINO_OBSERVATION_INCIDENT_EVIDENCE}" \
		--attestation-key-file "$${ZHIXU_EINO_OBSERVATION_ATTESTATION_KEY_FILE}" \
		--prometheus-url-file "$${ZHIXU_EINO_OBSERVATION_PROMETHEUS_URL_FILE}" \
		--prometheus-bearer-token-file "$${ZHIXU_EINO_OBSERVATION_PROMETHEUS_BEARER_TOKEN_FILE}" \
		--tempo-url-file "$${ZHIXU_EINO_OBSERVATION_TEMPO_URL_FILE}" \
		--tempo-bearer-token-file "$${ZHIXU_EINO_OBSERVATION_TEMPO_BEARER_TOKEN_FILE}"

eino-stable-observation-attest:
	@test -n "$${ZHIXU_EINO_OBSERVATION_ARCHIVE_DIR:-}" || (echo "ZHIXU_EINO_OBSERVATION_ARCHIVE_DIR is required" >&2; exit 1)
	@test -n "$${ZHIXU_EINO_OBSERVATION_ATTESTATION_KEY_FILE:-}" || (echo "ZHIXU_EINO_OBSERVATION_ATTESTATION_KEY_FILE is required" >&2; exit 1)
	python3 deploy/eino_stable_observation_collect.py attest \
		--archive-dir "$${ZHIXU_EINO_OBSERVATION_ARCHIVE_DIR}" \
		--attestation-key-file "$${ZHIXU_EINO_OBSERVATION_ATTESTATION_KEY_FILE}"

eino-stable-observation-verify:
	@test -n "$${ZHIXU_EINO_OBSERVATION_START_MANIFEST:-}" || (echo "ZHIXU_EINO_OBSERVATION_START_MANIFEST is required" >&2; exit 1)
	@test -n "$${ZHIXU_EINO_OBSERVATION_DAILY_DIR:-}" || (echo "ZHIXU_EINO_OBSERVATION_DAILY_DIR is required" >&2; exit 1)
	@test -n "$${ZHIXU_EINO_OBSERVATION_ATTESTATION:-}" || (echo "ZHIXU_EINO_OBSERVATION_ATTESTATION is required" >&2; exit 1)
	@test -n "$${ZHIXU_EINO_OBSERVATION_ATTESTATION_KEY_FILE:-}" || (echo "ZHIXU_EINO_OBSERVATION_ATTESTATION_KEY_FILE is required" >&2; exit 1)
	@test -n "$${ZHIXU_EINO_OBSERVATION_EVIDENCE_DIR:-}" || (echo "ZHIXU_EINO_OBSERVATION_EVIDENCE_DIR is required" >&2; exit 1)
	python3 deploy/eino_stable_observation_verify.py \
		--start "$${ZHIXU_EINO_OBSERVATION_START_MANIFEST}" \
		--daily-dir "$${ZHIXU_EINO_OBSERVATION_DAILY_DIR}" \
		--attestation "$${ZHIXU_EINO_OBSERVATION_ATTESTATION}" \
		--attestation-key-file "$${ZHIXU_EINO_OBSERVATION_ATTESTATION_KEY_FILE}" \
		--evidence-dir "$${ZHIXU_EINO_OBSERVATION_EVIDENCE_DIR}"

agent-eval:
	go run ./eval/agent/cmd

semantic-link-eval:
	go test ./eval/semanticlink
	go run ./eval/semanticlink/cmd

openapi-install:
	SCARF_ANALYTICS=false npm ci --include=dev --prefix api/openapi

openapi-lint:
	SCARF_ANALYTICS=false npm run lint --prefix api/openapi

openapi-project-check:
	node api/openapi/check.mjs

openapi-route-check:
	GIN_MODE=test go test -count=1 -timeout 60s ./internal/app -run '^TestRouter(RoutesExactlyMatchOpenAPI|MetricsIsTheOnlyOptionalRuntimeRoute)$$'

openapi-tags-check:
	node api/openapi/tag-manifest.mjs

openapi-generated-typecheck:
	npm run typecheck:generated --prefix web

openapi-check: openapi-lint openapi-project-check openapi-route-check openapi-tags-check

openapi-generate: openapi-check
	npm run generate --prefix api/openapi

openapi-generate-check: openapi-check
	npm run generate:check --prefix api/openapi
	$(MAKE) --no-print-directory openapi-generated-typecheck

openapi-breaking-check:
	api/openapi/breaking-check.sh

trellis-script-test:
	python3 -m unittest discover -s .trellis/scripts -p 'test_*.py'

task-context-check:
	@set -eu; \
	for task_json in .trellis/tasks/*/task.json; do \
		[ -f "$$task_json" ] || continue; \
		task_dir=$${task_json%/task.json}; \
		python3 .trellis/scripts/task.py validate "$$task_dir"; \
	done

auth-integration:
	@test -n "$$ZHIXU_TEST_DATABASE_URL" || (echo "ZHIXU_TEST_DATABASE_URL is required" >&2; exit 1)
	ZHIXU_DATABASE_URL="$$ZHIXU_TEST_DATABASE_URL" ZHIXU_MIGRATION_INTEGRATION=1 go test -tags=integration -count=1 ./cmd/migrate
	go test -race -tags=integration -count=1 -p 1 ./internal/auth/adapter/postgres
	go test -race -tags=integration -count=1 -p 1 -run '^TestLearningOpsAuthMigration(UpRepeatDownPreservesSchemas|GuardsNonEmptyDown)$$' ./internal/platform/migration

tool-integration:
	@test -n "$$ZHIXU_TEST_DATABASE_URL" || (echo "ZHIXU_TEST_DATABASE_URL is required" >&2; exit 1)
	go test -race -tags=integration -count=1 -p 1 -run 'TestPersistedWorkflowRiverToolRequestExecutesRefusesAndReplays|TestWorkerToolCompositionSeparatesContractsExecutorsAndTrustedAudit' ./cmd/worker
	go test -race -tags=integration -count=1 -p 1 -run 'TestWritebackSagaRealFaultSmoke|TestSafeWritebackWorkflowNodePostgreSQLGitFilesystemSmoke' ./internal/changecontrol/application

rag-integration:
	@test -n "$$ZHIXU_TEST_DATABASE_URL" || (echo "ZHIXU_TEST_DATABASE_URL is required" >&2; exit 1)
	go test -race -tags=integration -count=1 -p 1 -run '^TestPublicConversationRunsThroughRiverRAGAndFeedback$$' ./cmd/worker

workspace-analysis-integration:
	@test -n "$$ZHIXU_TEST_DATABASE_URL" || (echo "ZHIXU_TEST_DATABASE_URL is required" >&2; exit 1)
	go test -race -tags=integration -count=1 -p 1 -timeout 5m -run '^TestWorkspaceAnalysis' ./internal/platform/migration
	go test -race -tags=integration -count=1 -p 1 -timeout 5m -run '^TestWorkspaceAnalysis' ./internal/conversation/adapter/postgres ./internal/agent/adapter/postgres ./internal/tools/adapter/postgres
	go test -race -tags=integration -count=1 -p 1 -timeout 5m -run '^TestWorkspaceAnalysis' ./cmd/worker

workspace-analysis-terminal-matrix:
	bash deploy/workspace-analysis-terminal-matrix-go-gate.sh

workspace-analysis-fault-smoke: workspace-analysis-terminal-matrix
	bash deploy/compose-workspace-analysis-worker-restart-smoke.sh

graph-integration:
	@test -n "$$ZHIXU_TEST_DATABASE_URL" || (echo "ZHIXU_TEST_DATABASE_URL is required" >&2; exit 1)
	go test -race -tags=integration -count=1 -p 1 -run '^TestGraphPublicHTTPIntegration$$' ./cmd/api

graph-smoke:
	@test -n "$$ZHIXU_TEST_DATABASE_URL" || (echo "ZHIXU_TEST_DATABASE_URL is required" >&2; exit 1)
	bash deploy/graph-smoke.sh

graph-benchmark:
	@test -n "$$ZHIXU_TEST_DATABASE_URL" || (echo "ZHIXU_TEST_DATABASE_URL is required" >&2; exit 1)
	ZHIXU_GRAPH_BENCHMARK_ARTIFACT_DIR="$${ZHIXU_GRAPH_BENCHMARK_ARTIFACT_DIR:-$(CURDIR)/tmp/graph-benchmark}" \
		go test -tags=integration -count=1 -p 1 -run '^TestGraphCapacityBenchmark$$' ./internal/graph/adapter/postgres

benchmark-capacity:
	bash deploy/capacity-benchmark.sh

semantic-link-integration:
	@test -n "$$ZHIXU_TEST_DATABASE_URL" || (echo "ZHIXU_TEST_DATABASE_URL is required" >&2; exit 1)
	go test -race -tags=integration -count=1 -p 1 -run 'TestSemanticLinkTopicScanRunsThroughRealRiverToCandidate|TestSemanticLinkTopicScanPageIncludesBoundedCrossPagePairs|TestSemanticLinkScanRepositoryStartReplayConflictAdvanceAndFinish|TestCandidateConfirmCreatesIndependentTypedProposalsAndExactlyReplays|TestApprovedCandidateAppliesOneConfirmedRelationAndExactlyReplays|TestCandidateApprovalAndRelationApplyCommitInOneTransaction|TestCandidateApprovalAndRelationApplyReusesSuggestedRelationAndEvidence|TestCandidateApprovalAndRelationApplyDoesNotOverwriteConfirmedRelation|TestCandidateApprovalAndRelationApplyRejectsNonSuggestedRelationStates|TestCandidateApprovalAndRelationApplyPreservesHistoricalEvidenceAcrossLifecycleReplay|TestCandidateApprovalAndRelationApplyLocksCandidateBeforeProposal|TestCandidateApprovalAndRelationApplyReusesConcurrentSuggestedWinner' ./internal/graph/adapter/postgres ./internal/changecontrol/... ./internal/knowledge/...

semantic-link-fault-smoke:
	@test -n "$$ZHIXU_TEST_DATABASE_URL" || (echo "ZHIXU_TEST_DATABASE_URL is required" >&2; exit 1)
	go test -race -tags=integration -count=1 -p 1 -run 'TestSemanticLinkTopicScan(ReplaysAfterWorkflowCompletionResponseLoss|CancellationConvergesWithWorkflow)|TestSemanticLinkTopicScanRiver(RecoversFromRetryablePageFault|PersistsFaultAfterRetryBudget)|TestApprovedCandidateApply(MarksNeedsRevisionOnEndpointOrProvenanceDrift|RollsBackRelationAndProposalOnEvidenceFailure|RecoversCommitResponseLoss)|TestCandidateApprovalAndRelationApply(RollBackTogetherOnEvidenceFailure|RollBackTogetherOnEventFailure|RollsBackNeedsRevisionEventFailure|CommitNeedsRevisionOnBaselineDrift|RecoverCommitResponseLoss)' ./internal/graph/adapter/postgres ./internal/changecontrol/... ./internal/knowledge/...

semantic-link-browser-smoke:
	@test -n "$$ZHIXU_TEST_DATABASE_URL" || (echo "ZHIXU_TEST_DATABASE_URL is required" >&2; exit 1)
	bash deploy/semantic-link-browser-smoke.sh

semantic-link-smoke: openapi-check semantic-link-eval semantic-link-browser-smoke
	ZHIXU_TEST_DATABASE_URL= go test -race ./internal/graph/... ./internal/changecontrol/... ./internal/knowledge/... ./internal/workflow/...
	npm run test --prefix web -- --run src/api/semantic-links.test.ts src/features/graph/semantic-link-queries.test.tsx src/features/graph/SemanticLinkCandidatePanel.test.tsx

collection-health-integration:
	bash deploy/collection-health-go-gate.sh integration

collection-health-fault-smoke:
	bash deploy/collection-health-go-gate.sh fault

collection-health-benchmark:
	bash deploy/collection-health-go-gate.sh benchmark

collection-health-browser-smoke:
	@test -n "$$ZHIXU_TEST_DATABASE_URL" || (echo "ZHIXU_TEST_DATABASE_URL is required" >&2; exit 1)
	bash deploy/collection-health-browser-smoke.sh

collection-health-secret-scan:
	bash deploy/collection-health-secret-scan.sh --source

collection-health-smoke: collection-health-browser-smoke

artifact-browser-smoke:
	@test -n "$$ZHIXU_TEST_DATABASE_URL" || (echo "ZHIXU_TEST_DATABASE_URL is required" >&2; exit 1)
	bash deploy/artifact-browser-smoke.sh

m8-learning-browser-smoke:
	@test -n "$$ZHIXU_TEST_DATABASE_URL" || (echo "ZHIXU_TEST_DATABASE_URL is required" >&2; exit 1)
	bash deploy/m8-learning-browser-smoke.sh

export-browser-smoke:
	@test -n "$$ZHIXU_TEST_DATABASE_URL" || (echo "ZHIXU_TEST_DATABASE_URL is required" >&2; exit 1)
	bash deploy/export-browser-smoke.sh

timeline-impact-integration:
	@test -n "$$ZHIXU_TEST_DATABASE_URL" || (echo "ZHIXU_TEST_DATABASE_URL is required" >&2; exit 1)
	go test -race -tags=integration -count=1 -p 1 -timeout 60s -run '^TestTimelineImpact' ./cmd/api
	go test -race -tags=integration -count=1 -p 1 -timeout 60s -run '^(TestTimelineRepositoryAppendPageAndImpactReplay|TestImpactReportV2SupersedesV1AndDerivesSuccessor|TestImpactReportV2RequiresMatchingV1Predecessor|TestTimelineRepositoryListImpactObjectsRejectsMoreThan500Candidates|TestImpactReportV2ConcurrentCreateReplaysOneWinner|TestImpactReportV2RejectsIncompleteSelectorMarkerWithoutWrites|TestTimelineRepositoryListsOwnerBackedImpactFromExactProvenanceAndRejectsStaleSnapshot|TestImpactReportAndTimelineOutboxRollBackWhenTransactionalAuditFails|TestTimelineRepositoryListImpactObjectsKeepsActionsReadOnly|TestTimelineProjectionOutboxConnectsProposalConflictAndImpact|TestTimelineProjectionV2PersistsOwnerBindingAndReplays|TestTimelineProjectionTwoDispatchersSkipLockedAndPersistExactlyOneEvent)$$' ./internal/knowledge/adapter/postgres
	go test -race -tags=integration -count=1 -p 1 -timeout 60s -run '^(TestRepositoryDownstreamUpdateProposalRoundTripReplayAndApprovalOnly|TestRepositoryBuildDownstreamReviewCardProposalGuardsBindings)$$' ./internal/changecontrol/adapter/postgres
	go test -race -tags=integration -count=1 -p 1 -timeout 60s -run '^(TestArtifactRevisionWritesCitationSelectorsInOwnerTransaction|TestArtifactCitationBackfillPersistsFailureAndResumesExactValidation)$$' ./internal/artifact/adapter/postgres
	go test -race -tags=integration -count=1 -p 1 -timeout 60s -run '^TestTimelineImpact(V2)?Migration' ./internal/platform/migration

timeline-impact-fault-smoke:
	@test -n "$$ZHIXU_TEST_DATABASE_URL" || (echo "ZHIXU_TEST_DATABASE_URL is required" >&2; exit 1)
	go test -race -tags=integration -count=1 -p 1 -timeout 60s -run '^TestTimelineProjection(V2PoisonsMalformedOwnerAndOperator|PersistsPoisonWithoutWritingKnowledgeEvent)$$' ./internal/knowledge/adapter/postgres

timeline-impact-worker-smoke:
	@test -n "$$ZHIXU_TEST_DATABASE_URL" || (echo "ZHIXU_TEST_DATABASE_URL is required" >&2; exit 1)
	go test -race -tags=integration -count=1 -p 1 -timeout 5m -run '^TestTimelineProjectionWorkerStartupAndRestart$$' ./cmd/worker

compose-auth-check:
	@python3 deploy/compose_auth_check.py -- \
		$(DOCKER_COMPOSE) --profile workspace-runtime -f deploy/compose.yml --env-file .env.example config --format json

compose-auth-smoke:
	bash deploy/compose-auth-smoke.sh

compose-runtime-check:
	@python3 deploy/compose_runtime_check.py -- \
		$(DOCKER_COMPOSE) --profile workspace-runtime -f deploy/compose.yml --env-file .env.example config --format json

compose-bootstrap-check:
	@python3 deploy/compose_auth_check.py --bootstrap -- \
		$(DOCKER_COMPOSE) --profile workspace-runtime --profile modelctl -f deploy/compose.yml -f deploy/compose.bootstrap.yml --env-file .env.example config --format json
	@python3 deploy/compose_runtime_check.py --bootstrap -- \
		$(DOCKER_COMPOSE) --profile workspace-runtime --profile modelctl -f deploy/compose.yml -f deploy/compose.bootstrap.yml --env-file .env.example config --format json
	@python3 deploy/compose_workspace_check.py --base -- \
		$(DOCKER_COMPOSE) --profile workspace-runtime --profile modelctl -f deploy/compose.yml -f deploy/compose.bootstrap.yml --env-file .env.example config --format json

compose-static-models-check:
	@python3 deploy/compose_runtime_check.py --static-models -- \
		$(DOCKER_COMPOSE) --profile workspace-runtime -f deploy/compose.yml -f deploy/compose.static-models.yml --env-file .env.example config --format json

compose-runtime-contract:
	python3 deploy/compose_runtime_contract.py

compose-netns-check:
	@python3 deploy/compose_netns_check.py -- \
		$(DOCKER_COMPOSE) --project-name zhixu-netns -f deploy/compose.netns.yml --env-file .env.example config --format json

compose-netns-contract:
	python3 deploy/compose_netns_contract.py

compose-workspace-check:
	@python3 deploy/compose_workspace_check.py --base -- \
		$(DOCKER_COMPOSE) --profile workspace-runtime -f deploy/compose.yml --env-file .env.example config --format json

compose-workspace-contract:
	python3 deploy/compose_workspace_contract.py

compose-check: compose-auth-check compose-runtime-check compose-bootstrap-check compose-static-models-check compose-runtime-contract compose-netns-check compose-netns-contract compose-workspace-check compose-workspace-contract compose-smoke-cleanup-contract smoke-image-cleanup-contract compose-rag-real-provider-contract compose-workspace-analysis-compat-contract compose-workspace-analysis-worker-restart-contract compose-workspace-analysis-otlp-contract launcher-contract model-secrets-init-contract
	$(DOCKER_COMPOSE) -f deploy/compose.yml --env-file .env.example config --quiet
	$(DOCKER_COMPOSE) --profile workspace-runtime --profile modelctl -f deploy/compose.yml -f deploy/compose.bootstrap.yml --env-file .env.example config --quiet
	$(DOCKER_COMPOSE) --project-name zhixu-netns -f deploy/compose.netns.yml --env-file .env.example config --quiet

compose-smoke-cleanup-contract:
	bash deploy/compose-smoke-cleanup-contract.sh

smoke-image-cleanup-contract:
	bash deploy/smoke-image-cleanup-contract.sh

compose-rag-real-provider-contract:
	bash deploy/compose-rag-real-provider-contract.sh

compose-workspace-analysis-compat-contract:
	bash deploy/compose-workspace-analysis-compat-smoke-contract.sh

compose-workspace-analysis-worker-restart-contract:
	bash deploy/compose-workspace-analysis-worker-restart-smoke-contract.sh

compose-workspace-analysis-otlp-contract:
	bash deploy/compose-workspace-analysis-otlp-smoke-contract.sh

launcher-contract:
	bash deploy/launcher-contract.sh

model-secrets-init-contract:
	bash deploy/model-secrets-init-contract.sh

architecture-quality-baseline:
	python3 deploy/architecture_quality_baseline.py --format text

docker-build:
	docker build -f deploy/Dockerfile -t zhixu:local .

compose-up:
	./zhixu up

compose-down:
	./zhixu down

compose-reset:
	./zhixu reset

compose-search-smoke:
	bash deploy/compose-search-smoke.sh

compose-tool-smoke:
	bash deploy/compose-tool-smoke.sh

compose-rag-smoke:
	bash deploy/compose-rag-smoke.sh

compose-rag-browser-smoke:
	ZHIXU_COMPOSE_RAG_BROWSER=1 bash deploy/compose-rag-smoke.sh

compose-workspace-analysis-smoke:
	bash deploy/compose-workspace-analysis-smoke.sh

compose-workspace-analysis-otlp-smoke:
	bash deploy/compose-workspace-analysis-otlp-smoke.sh

compose-workspace-analysis-compat-smoke:
	bash deploy/compose-workspace-analysis-compat-smoke.sh

compose-workspace-analysis-worker-restart-smoke:
	bash deploy/compose-workspace-analysis-worker-restart-smoke.sh

compose-model-runtime-hot-activation-smoke:
	bash deploy/model-runtime-hot-activation-smoke.sh

compose-rag-real-provider-smoke:
	ZHIXU_COMPOSE_RAG_REAL_PROVIDER=1 ZHIXU_COMPOSE_RAG_REAL_PROVIDER_PREFLIGHT_ONLY=0 bash deploy/compose-rag-real-provider-smoke.sh

compose-rag-real-provider-preflight:
	ZHIXU_COMPOSE_RAG_REAL_PROVIDER=1 ZHIXU_COMPOSE_RAG_REAL_PROVIDER_PREFLIGHT_ONLY=1 bash deploy/compose-rag-real-provider-smoke.sh
