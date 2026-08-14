// Package catalog 提供项目内置 Tool 的版本化契约目录，不注册任何 Executor。
package catalog

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/capability"
	"github.com/CodeZen-Lizhi/zhixu/internal/tools/application"
	"github.com/CodeZen-Lizhi/zhixu/internal/tools/domain"
)

const (
	toolVersionV1   int64 = 1
	toolVersionV2   int64 = 2
	schemaVersionV1 int64 = 1

	agentRAGWorkflowKey       = "agent-rag"
	agentRAGAnswerWorkflowKey = "agent-rag-answer"
	safeWritebackWorkflowKey  = "change-control.safe-writeback"
	indexMaintenanceWorkflow  = "retrieval.index-maintenance"
	regressionEvaluationFlow  = "evaluation.regression"
	builtinWorkflowVersionV1  = int64(1)
	agentRAGAnswerVersionV2   = int64(2)
	localReadTimeout          = 15 * time.Second
	trustedOperationTimeout   = 5 * time.Minute
	maxSmallDocumentBytes     = int64(64 * 1024)
	maxMediumDocumentBytes    = int64(256 * 1024)
	maxContentDocumentBytes   = int64(1024 * 1024)
)

type contractSeed struct {
	name            string
	version         int64
	description     string
	inputSchemaID   string
	inputSchema     string
	outputSchemaID  string
	outputSchema    string
	decodeInput     application.DocumentDecoder
	decodeOutput    application.DocumentDecoder
	capability      capability.Capability
	sideEffect      domain.SideEffectLevel
	invocation      domain.InvocationPolicy
	timeout         time.Duration
	retry           domain.RetryPolicy
	idempotency     domain.IdempotencyMode
	sensitiveFields []string
	workflow        string
	workflowVersion int64
	maxInputBytes   int64
	maxOutputBytes  int64
}

// Contracts 构造全部 13 个内置 Tool 的不可变契约；结果只描述能力，不表示已启用或可执行。
func Contracts() ([]application.Contract, error) {
	seeds := builtinSeeds()
	contracts := make([]application.Contract, 0, len(seeds))
	for _, seed := range seeds {
		definition, err := domain.CanonicalizeDefinition(domain.Definition{
			Ref:                  domain.ToolRef{Name: seed.name, Version: seed.version},
			Description:          seed.description,
			InputSchema:          domain.SchemaRef{ID: seed.inputSchemaID, Version: schemaVersionV1},
			InputSchemaDocument:  json.RawMessage(seed.inputSchema),
			OutputSchema:         domain.SchemaRef{ID: seed.outputSchemaID, Version: schemaVersionV1},
			OutputSchemaDocument: json.RawMessage(seed.outputSchema),
			RequiredCapability:   seed.capability,
			SideEffectLevel:      seed.sideEffect,
			InvocationPolicy:     seed.invocation,
			Timeout:              seed.timeout,
			RetryPolicy:          seed.retry,
			IdempotencyMode:      seed.idempotency,
			SensitiveFields:      seed.sensitiveFields,
			AllowedWorkflows: []domain.WorkflowBinding{{
				Key: seed.workflow, Version: seed.workflowVersion,
			}},
			MaxInputBytes:  seed.maxInputBytes,
			MaxOutputBytes: seed.maxOutputBytes,
		})
		if err != nil {
			return nil, fmt.Errorf("canonicalize built-in tool %s: %w", seed.name, err)
		}
		contracts = append(contracts, application.Contract{
			Definition: definition, DecodeInput: seed.decodeInput, DecodeOutput: seed.decodeOutput,
		})
	}
	return contracts, nil
}

// NewFrozenContractRegistry 构造 API、Worker 与 trusted audit 共用的全部 13 项冻结契约目录。
// 该 Registry 不包含 Executor，因此不会把 contract-only Tool 宣称为可执行。
func NewFrozenContractRegistry() (*application.Registry, error) {
	contracts, err := Contracts()
	if err != nil {
		return nil, err
	}
	registry := application.NewContractRegistry()
	for _, contract := range contracts {
		if err := registry.RegisterContract(contract); err != nil {
			return nil, err
		}
	}
	if err := registry.Freeze(); err != nil {
		return nil, err
	}
	return registry, nil
}

func builtinSeeds() []contractSeed {
	localRetry := domain.RetryPolicy{MaxAttempts: 3, BaseDelay: 250 * time.Millisecond, MaxDelay: 2 * time.Second}
	noRetry := domain.RetryPolicy{MaxAttempts: 1}
	return []contractSeed{
		{
			name: "SearchKnowledge", version: toolVersionV1, description: "Search approved knowledge in the current workspace through the frozen retrieval index.",
			inputSchemaID: "tool.search_knowledge.input", inputSchema: searchKnowledgeInputSchema,
			outputSchemaID: "tool.search_knowledge.output", outputSchema: searchKnowledgeOutputSchema,
			decodeInput: decodeSearchKnowledgeInput, decodeOutput: decodeSearchKnowledgeOutput,
			capability: capability.ReadLocal, sideEffect: domain.SideEffectNone, invocation: domain.InvocationModelRequestable,
			timeout: localReadTimeout, retry: localRetry, idempotency: domain.IdempotencyNone,
			sensitiveFields: []string{"/items", "/query"}, workflow: agentRAGWorkflowKey, workflowVersion: builtinWorkflowVersionV1,
			maxInputBytes: maxSmallDocumentBytes, maxOutputBytes: maxContentDocumentBytes,
		},
		{
			name: "ReadSource", version: toolVersionV1, description: "Open one immutable source span by stable identifiers in the current workspace.",
			inputSchemaID: "tool.read_source.input", inputSchema: readSourceInputSchema,
			outputSchemaID: "tool.read_source.output", outputSchema: readSourceOutputSchema,
			decodeInput: decodeReadSourceInput, decodeOutput: decodeReadSourceOutput,
			capability: capability.ReadLocal, sideEffect: domain.SideEffectNone, invocation: domain.InvocationModelRequestable,
			timeout: localReadTimeout, retry: localRetry, idempotency: domain.IdempotencyNone,
			sensitiveFields: []string{"/excerpt"}, workflow: agentRAGWorkflowKey, workflowVersion: builtinWorkflowVersionV1,
			maxInputBytes: maxSmallDocumentBytes, maxOutputBytes: maxMediumDocumentBytes,
		},
		{
			name: "ReadDocument", version: toolVersionV1, description: "Read one approved document projection by stable identifier in the current workspace.",
			inputSchemaID: "tool.read_document.input", inputSchema: readDocumentInputSchema,
			outputSchemaID: "tool.read_document.output", outputSchema: readDocumentOutputSchema,
			decodeInput: decodeReadDocumentInput, decodeOutput: decodeReadDocumentOutput,
			capability: capability.ReadLocal, sideEffect: domain.SideEffectNone, invocation: domain.InvocationModelRequestable,
			timeout: localReadTimeout, retry: localRetry, idempotency: domain.IdempotencyNone,
			sensitiveFields: []string{"/text"}, workflow: agentRAGWorkflowKey, workflowVersion: builtinWorkflowVersionV1,
			maxInputBytes: maxSmallDocumentBytes, maxOutputBytes: maxContentDocumentBytes,
		},
		{
			name: "FetchWebPage", version: toolVersionV1, description: "Fetch one explicitly allowed public HTTP or HTTPS page through the SSRF-safe adapter.",
			inputSchemaID: "tool.fetch_web_page.input", inputSchema: fetchWebPageInputSchema,
			outputSchemaID: "tool.fetch_web_page.output", outputSchema: fetchWebPageOutputSchema,
			decodeInput: decodeFetchWebPageInput, decodeOutput: decodeFetchWebPageOutput,
			capability: capability.ReadExternal, sideEffect: domain.SideEffectExternalRead, invocation: domain.InvocationModelRequestable,
			timeout: 30 * time.Second, retry: domain.RetryPolicy{MaxAttempts: 2, BaseDelay: time.Second, MaxDelay: 2 * time.Second}, idempotency: domain.IdempotencyNone,
			sensitiveFields: []string{"/final_url", "/text", "/url"}, workflow: agentRAGWorkflowKey, workflowVersion: builtinWorkflowVersionV1,
			maxInputBytes: maxSmallDocumentBytes, maxOutputBytes: maxContentDocumentBytes,
		},
		{
			name: "ValidateCitation", version: toolVersionV1, description: "Validate a bounded batch of complete citation tuples against immutable retrieval evidence.",
			inputSchemaID: "tool.validate_citation.input", inputSchema: validateCitationInputSchema,
			outputSchemaID: "tool.validate_citation.output", outputSchema: validateCitationOutputSchema,
			decodeInput: decodeValidateCitationInput, decodeOutput: decodeValidateCitationOutput,
			capability: capability.ReadLocal, sideEffect: domain.SideEffectNone, invocation: domain.InvocationModelRequestable,
			timeout: localReadTimeout, retry: localRetry, idempotency: domain.IdempotencyNone,
			workflow: agentRAGWorkflowKey, workflowVersion: builtinWorkflowVersionV1, maxInputBytes: maxMediumDocumentBytes, maxOutputBytes: maxMediumDocumentBytes,
		},
		{
			name: "CalculateDiff", version: toolVersionV1, description: "Calculate a bounded deterministic text diff without accessing workspace, network, or Git state.",
			inputSchemaID: "tool.calculate_diff.input", inputSchema: calculateDiffInputSchema,
			outputSchemaID: "tool.calculate_diff.output", outputSchema: calculateDiffOutputSchema,
			decodeInput: decodeCalculateDiffInput, decodeOutput: decodeCalculateDiffOutput,
			sideEffect: domain.SideEffectNone, invocation: domain.InvocationModelRequestable,
			timeout: 5 * time.Second, retry: noRetry, idempotency: domain.IdempotencyNone,
			sensitiveFields: []string{"/after", "/before", "/patch"}, workflow: agentRAGWorkflowKey, workflowVersion: builtinWorkflowVersionV1,
			maxInputBytes: maxContentDocumentBytes, maxOutputBytes: maxContentDocumentBytes,
		},
		{
			name: "ReadGitStatus", version: toolVersionV1, description: "Inspect the current workspace Git state through the fixed-argument Git inspector.",
			inputSchemaID: "tool.read_git_status.input", inputSchema: readGitStatusInputSchema,
			outputSchemaID: "tool.read_git_status.output", outputSchema: readGitStatusOutputSchema,
			decodeInput: decodeReadGitStatusInput, decodeOutput: decodeReadGitStatusOutput,
			capability: capability.ReadLocal, sideEffect: domain.SideEffectNone, invocation: domain.InvocationModelRequestable,
			timeout: 10 * time.Second, retry: noRetry, idempotency: domain.IdempotencyNone,
			workflow: agentRAGWorkflowKey, workflowVersion: builtinWorkflowVersionV1, maxInputBytes: maxSmallDocumentBytes, maxOutputBytes: maxSmallDocumentBytes,
		},
		{
			name: "ApplyApprovedPatch", version: toolVersionV1, description: "Associate the approved patch step with the existing atomic safe-writeback execution.",
			inputSchemaID: "tool.apply_approved_patch.input", inputSchema: applyApprovedPatchInputSchema,
			outputSchemaID: "tool.apply_approved_patch.output", outputSchema: applyApprovedPatchOutputSchema,
			decodeInput: decodeApplyApprovedPatchInput, decodeOutput: decodeApplyApprovedPatchOutput,
			capability: capability.WriteKnowledge, sideEffect: domain.SideEffectDomainWrite, invocation: domain.InvocationTrustedWorkflowOnly,
			timeout: trustedOperationTimeout, retry: noRetry, idempotency: domain.IdempotencyRequired,
			workflow: safeWritebackWorkflowKey, workflowVersion: builtinWorkflowVersionV1, maxInputBytes: maxSmallDocumentBytes, maxOutputBytes: maxSmallDocumentBytes,
		},
		{
			name: "CreateGitCommit", version: toolVersionV1, description: "Associate the approved commit step with the existing atomic safe-writeback execution.",
			inputSchemaID: "tool.create_git_commit.input", inputSchema: createGitCommitInputSchema,
			outputSchemaID: "tool.create_git_commit.output", outputSchema: createGitCommitOutputSchema,
			decodeInput: decodeCreateGitCommitInput, decodeOutput: decodeCreateGitCommitOutput,
			capability: capability.GitWrite, sideEffect: domain.SideEffectDomainWrite, invocation: domain.InvocationTrustedWorkflowOnly,
			timeout: trustedOperationTimeout, retry: noRetry, idempotency: domain.IdempotencyRequired,
			workflow: safeWritebackWorkflowKey, workflowVersion: builtinWorkflowVersionV1, maxInputBytes: maxSmallDocumentBytes, maxOutputBytes: maxSmallDocumentBytes,
		},
		{
			name: "RebuildIndex", version: toolVersionV1, description: "Run one existing versioned reindex delivery through the maintenance workflow.",
			inputSchemaID: "tool.rebuild_index.input", inputSchema: rebuildIndexInputSchema,
			outputSchemaID: "tool.rebuild_index.output", outputSchema: rebuildIndexOutputSchema,
			decodeInput: decodeRebuildIndexInput, decodeOutput: decodeRebuildIndexOutput,
			capability: capability.IndexMaintenance, sideEffect: domain.SideEffectDomainWrite, invocation: domain.InvocationTrustedWorkflowOnly,
			timeout: trustedOperationTimeout, retry: noRetry, idempotency: domain.IdempotencyRequired,
			workflow: indexMaintenanceWorkflow, workflowVersion: builtinWorkflowVersionV1, maxInputBytes: maxSmallDocumentBytes, maxOutputBytes: maxSmallDocumentBytes,
		},
		{
			name: "RunRegressionEvaluation", version: toolVersionV1, description: "Run a frozen regression dataset against one exact index version in the evaluation workflow.",
			inputSchemaID: "tool.run_regression_evaluation.input", inputSchema: runRegressionEvaluationInputSchema,
			outputSchemaID: "tool.run_regression_evaluation.output", outputSchema: runRegressionEvaluationOutputSchema,
			decodeInput: decodeRunRegressionEvaluationInput, decodeOutput: decodeRunRegressionEvaluationOutput,
			capability: capability.EvaluationRun, sideEffect: domain.SideEffectDomainWrite, invocation: domain.InvocationTrustedWorkflowOnly,
			timeout: trustedOperationTimeout, retry: noRetry, idempotency: domain.IdempotencyRequired,
			workflow: regressionEvaluationFlow, workflowVersion: builtinWorkflowVersionV1, maxInputBytes: maxSmallDocumentBytes, maxOutputBytes: maxSmallDocumentBytes,
		},
		{
			name: "ReadSource", version: toolVersionV2, description: "Open one immutable source span by stable identifiers in the current workspace.",
			inputSchemaID: "tool.read_source.input", inputSchema: readSourceInputSchema,
			outputSchemaID: "tool.read_source.output", outputSchema: readSourceOutputSchema,
			decodeInput: decodeReadSourceInput, decodeOutput: decodeReadSourceOutput,
			capability: capability.ReadLocal, sideEffect: domain.SideEffectNone, invocation: domain.InvocationModelRequestable,
			timeout: localReadTimeout, retry: localRetry, idempotency: domain.IdempotencyNone,
			sensitiveFields: []string{"/excerpt"}, workflow: agentRAGAnswerWorkflowKey, workflowVersion: agentRAGAnswerVersionV2,
			maxInputBytes: maxSmallDocumentBytes, maxOutputBytes: maxMediumDocumentBytes,
		},
		{
			name: "ValidateCitation", version: toolVersionV2, description: "Validate a bounded batch of complete citation tuples against immutable retrieval evidence.",
			inputSchemaID: "tool.validate_citation.input", inputSchema: validateCitationInputSchema,
			outputSchemaID: "tool.validate_citation.output", outputSchema: validateCitationOutputSchema,
			decodeInput: decodeValidateCitationInput, decodeOutput: decodeValidateCitationOutput,
			capability: capability.ReadLocal, sideEffect: domain.SideEffectNone, invocation: domain.InvocationModelRequestable,
			timeout: localReadTimeout, retry: localRetry, idempotency: domain.IdempotencyNone,
			workflow: agentRAGAnswerWorkflowKey, workflowVersion: agentRAGAnswerVersionV2,
			maxInputBytes: maxMediumDocumentBytes, maxOutputBytes: maxMediumDocumentBytes,
		},
	}
}
