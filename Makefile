SHELL := /bin/sh
DOCKER_COMPOSE ?= docker compose

.PHONY: test migrate go-test go-vet web-install web-lint web-typecheck web-test web-build eino-test eino-vet eino-live-smoke agent-eval semantic-link-eval openapi-check auth-integration tool-integration rag-integration graph-integration graph-smoke graph-benchmark semantic-link-integration semantic-link-fault-smoke semantic-link-browser-smoke semantic-link-smoke collection-health-integration collection-health-fault-smoke collection-health-benchmark collection-health-browser-smoke collection-health-secret-scan collection-health-smoke artifact-browser-smoke export-browser-smoke timeline-impact-integration timeline-impact-fault-smoke timeline-impact-worker-smoke compose-auth-check compose-auth-smoke compose-check docker-build compose-up compose-down compose-search-smoke compose-tool-smoke compose-rag-smoke

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

semantic-link-eval:
	go test ./eval/semanticlink
	go run ./eval/semanticlink/cmd

openapi-check:
	node api/openapi/check.mjs

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

export-browser-smoke:
	@test -n "$$ZHIXU_TEST_DATABASE_URL" || (echo "ZHIXU_TEST_DATABASE_URL is required" >&2; exit 1)
	bash deploy/export-browser-smoke.sh

timeline-impact-integration:
	@test -n "$$ZHIXU_TEST_DATABASE_URL" || (echo "ZHIXU_TEST_DATABASE_URL is required" >&2; exit 1)
	go test -race -tags=integration -count=1 -p 1 -run '^(TestTimelineImpactPublicHTTPIntegration|TestTimelineImpactAuthenticatedAPITokenAuditIntegration|TestTimelineImpactAuthenticatedSessionAuditIntegration)$$' ./cmd/api
	go test -race -tags=integration -count=1 -p 1 -run '^(TestTimelineRepositoryAppendPageAndImpactReplay|TestImpactReportAndTimelineOutboxRollBackWhenTransactionalAuditFails|TestTimelineRepositoryListImpactObjectsKeepsActionsReadOnly|TestTimelineProjectionOutboxConnectsProposalConflictAndImpact|TestTimelineProjectionTwoDispatchersSkipLockedAndPersistExactlyOneEvent)$$' ./internal/knowledge/adapter/postgres
	go test -race -tags=integration -count=1 -p 1 -run '^TestTimelineImpactMigration' ./internal/platform/migration

timeline-impact-fault-smoke:
	@test -n "$$ZHIXU_TEST_DATABASE_URL" || (echo "ZHIXU_TEST_DATABASE_URL is required" >&2; exit 1)
	go test -race -tags=integration -count=1 -p 1 -run '^TestTimelineProjectionPersistsPoisonWithoutWritingKnowledgeEvent$$' ./internal/knowledge/adapter/postgres

timeline-impact-worker-smoke:
	@test -n "$$ZHIXU_TEST_DATABASE_URL" || (echo "ZHIXU_TEST_DATABASE_URL is required" >&2; exit 1)
	go test -race -tags=integration -count=1 -p 1 -timeout 5m -run '^TestTimelineProjectionWorkerStartupAndRestart$$' ./cmd/worker

compose-auth-check:
	@python3 deploy/compose_auth_check.py -- \
		$(DOCKER_COMPOSE) -f deploy/compose.yml --env-file .env.example config --format json

compose-auth-smoke:
	bash deploy/compose-auth-smoke.sh

compose-check: compose-auth-check
	$(DOCKER_COMPOSE) -f deploy/compose.yml --env-file .env.example config --quiet

docker-build:
	docker build -f deploy/Dockerfile -t zhixu:local .

compose-up: compose-auth-check
	$(DOCKER_COMPOSE) -f deploy/compose.yml --env-file .env.example up -d --build --wait

compose-down:
	$(DOCKER_COMPOSE) -f deploy/compose.yml --env-file .env.example down -v

compose-search-smoke:
	bash deploy/compose-search-smoke.sh

compose-tool-smoke:
	bash deploy/compose-tool-smoke.sh

compose-rag-smoke:
	bash deploy/compose-rag-smoke.sh
