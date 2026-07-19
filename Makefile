SHELL := /bin/sh

.PHONY: test migrate go-test go-vet web-install web-lint web-typecheck web-test web-build eino-test eino-vet eino-live-smoke agent-eval openapi-check tool-integration compose-check docker-build compose-up compose-down compose-search-smoke compose-tool-smoke

test: go-test go-vet web-lint web-typecheck web-test web-build eino-test eino-vet agent-eval openapi-check compose-check

migrate:
	go run ./cmd/migrate

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

web-build:
	npm run build --prefix web

eino-test:
	cd poc/eino && go test -race ./...

eino-vet:
	cd poc/eino && go vet ./...

eino-live-smoke:
	cd poc/eino && go test -run TestOpenAICompatibleChatSmoke -v ./live

agent-eval:
	go run ./eval/agent/cmd

openapi-check:
	node api/openapi/check.mjs

tool-integration:
	@test -n "$$ZHIXU_TEST_DATABASE_URL" || (echo "ZHIXU_TEST_DATABASE_URL is required" >&2; exit 1)
	go test -race -tags=integration -count=1 -p 1 -run 'TestPersistedWorkflowRiverToolRequestExecutesRefusesAndReplays|TestWorkerToolCompositionSeparatesContractsExecutorsAndTrustedAudit' ./cmd/worker
	go test -race -tags=integration -count=1 -p 1 -run 'TestWritebackSagaRealFaultSmoke|TestSafeWritebackWorkflowNodePostgreSQLGitFilesystemSmoke' ./internal/changecontrol/application

compose-check:
	docker compose -f deploy/compose.yml --env-file .env.example config --quiet

docker-build:
	docker build -f deploy/Dockerfile -t zhixu:local .

compose-up:
	docker compose -f deploy/compose.yml --env-file .env.example up -d --build --wait

compose-down:
	docker compose -f deploy/compose.yml --env-file .env.example down -v

compose-search-smoke:
	bash deploy/compose-search-smoke.sh

compose-tool-smoke:
	bash deploy/compose-tool-smoke.sh
