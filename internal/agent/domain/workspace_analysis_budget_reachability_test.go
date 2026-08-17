package domain

import "testing"

func TestWorkspaceAnalysisV1CanonicalOperationPrefixesAlwaysFitFrozenBudget(t *testing.T) {
	contracts := WorkspaceAnalysisV1OperationContracts()
	if len(contracts) != WorkspaceAnalysisV1MaxModelCalls+WorkspaceAnalysisV1MaxToolCalls {
		t.Fatalf("frozen operation catalog has %d slots, want %d", len(contracts), WorkspaceAnalysisV1MaxModelCalls+WorkspaceAnalysisV1MaxToolCalls)
	}

	for synthesisOutputCap := int64(1); synthesisOutputCap <= WorkspaceAnalysisV1SynthesisMaxOutputTokens; synthesisOutputCap++ {
		maximum := workspaceAnalysisV1FrozenBudget(synthesisOutputCap)
		limits := WorkspaceAnalysisBudgetLimits{
			Nodes:           WorkspaceAnalysisV1MaxNodes,
			ToolConcurrency: WorkspaceAnalysisV1MaxToolConcurrency,
			Amount:          maximum,
		}
		if !validWorkspaceAnalysisLimits(limits) {
			t.Fatalf("synthesis output cap %d produced invalid frozen limits: %#v", synthesisOutputCap, limits)
		}

		oneReadPath := workspaceAnalysisV1SuccessfulBudgetPath(t, contracts, 1, synthesisOutputCap)
		clarificationPath := workspaceAnalysisBudgetPrefixThrough(t, oneReadPath, WorkspaceAnalysisOperationRetrievalPlan)
		clarificationUsed := workspaceAnalysisAssertReachableBudgetPrefixes(t, "clarification", clarificationPath, limits)
		workspaceAnalysisAssertBudgetAmount(t, "clarification", clarificationUsed, WorkspaceAnalysisBudgetAmount{
			ModelCalls:   1,
			ToolCalls:    1,
			InputTokens:  WorkspaceAnalysisV1MaxInputTokensPerModelCall,
			OutputTokens: WorkspaceAnalysisV1PlanMaxOutputTokens,
		})

		zeroHitPath := workspaceAnalysisBudgetPrefixThrough(t, oneReadPath, WorkspaceAnalysisOperationKnowledgeSearch)
		zeroHitUsed := workspaceAnalysisAssertReachableBudgetPrefixes(t, "zero-hit", zeroHitPath, limits)
		workspaceAnalysisAssertBudgetAmount(t, "zero-hit", zeroHitUsed, WorkspaceAnalysisBudgetAmount{
			ModelCalls:   1,
			ToolCalls:    2,
			InputTokens:  WorkspaceAnalysisV1MaxInputTokensPerModelCall,
			OutputTokens: WorkspaceAnalysisV1PlanMaxOutputTokens,
		})

		for sourceReadCount := 1; sourceReadCount <= WorkspaceAnalysisV1MaxSourceReads; sourceReadCount++ {
			path := workspaceAnalysisV1SuccessfulBudgetPath(t, contracts, sourceReadCount, synthesisOutputCap)
			used := workspaceAnalysisAssertReachableBudgetPrefixes(t, "success", path, limits)
			want := WorkspaceAnalysisBudgetAmount{
				ModelCalls:   WorkspaceAnalysisV1MaxModelCalls,
				ToolCalls:    sourceReadCount + 3,
				SourceReads:  sourceReadCount,
				InputTokens:  WorkspaceAnalysisV1MaxRunInputTokens,
				OutputTokens: maximum.OutputTokens,
			}
			workspaceAnalysisAssertBudgetAmount(t, "success", used, want)
			if used.ModelCalls != maximum.ModelCalls || used.InputTokens != maximum.InputTokens || used.OutputTokens != maximum.OutputTokens {
				t.Fatalf("success cap=%d reads=%d did not exactly exhaust model/input/output budget: used=%#v maximum=%#v", synthesisOutputCap, sourceReadCount, used, maximum)
			}
			if used.ToolCalls != used.SourceReads+3 || used.ToolCalls > maximum.ToolCalls || used.SourceReads > maximum.SourceReads {
				t.Fatalf("success cap=%d reads=%d used invalid actual tool/source budget: used=%#v maximum=%#v", synthesisOutputCap, sourceReadCount, used, maximum)
			}
		}
	}
}

type workspaceAnalysisBudgetReachabilityStep struct {
	contract    WorkspaceAnalysisOperationContract
	reservation WorkspaceAnalysisBudgetAmount
}

func workspaceAnalysisV1FrozenBudget(synthesisOutputCap int64) WorkspaceAnalysisBudgetAmount {
	return WorkspaceAnalysisBudgetAmount{
		ModelCalls:   WorkspaceAnalysisV1MaxModelCalls,
		ToolCalls:    WorkspaceAnalysisV1MaxToolCalls,
		SourceReads:  WorkspaceAnalysisV1MaxSourceReads,
		InputTokens:  WorkspaceAnalysisV1MaxRunInputTokens,
		OutputTokens: WorkspaceAnalysisV1PlanMaxOutputTokens + synthesisOutputCap + WorkspaceAnalysisV1ReviewMaxOutputTokens,
	}
}

func workspaceAnalysisV1SuccessfulBudgetPath(
	t *testing.T,
	contracts []WorkspaceAnalysisOperationContract,
	sourceReadCount int,
	synthesisOutputCap int64,
) []workspaceAnalysisBudgetReachabilityStep {
	t.Helper()
	path := make([]workspaceAnalysisBudgetReachabilityStep, 0, len(contracts))
	for _, contract := range contracts {
		if contract.Kind == WorkspaceAnalysisOperationSourceRead && contract.Ordinal > sourceReadCount {
			continue
		}
		reservation, ok := workspaceAnalysisV1CanonicalReservation(contract.Kind, synthesisOutputCap)
		if !ok {
			t.Fatalf("frozen operation %s/%d has no canonical budget reservation", contract.Kind, contract.Ordinal)
		}
		path = append(path, workspaceAnalysisBudgetReachabilityStep{contract: contract, reservation: reservation})
	}
	return path
}

func workspaceAnalysisV1CanonicalReservation(kind WorkspaceAnalysisOperationKind, synthesisOutputCap int64) (WorkspaceAnalysisBudgetAmount, bool) {
	switch kind {
	case WorkspaceAnalysisOperationGitStatus, WorkspaceAnalysisOperationKnowledgeSearch, WorkspaceAnalysisOperationCitationValidation:
		return WorkspaceAnalysisBudgetAmount{ToolCalls: 1}, true
	case WorkspaceAnalysisOperationSourceRead:
		return WorkspaceAnalysisBudgetAmount{ToolCalls: 1, SourceReads: 1}, true
	case WorkspaceAnalysisOperationRetrievalPlan:
		return WorkspaceAnalysisBudgetAmount{
			ModelCalls: 1, InputTokens: WorkspaceAnalysisV1MaxInputTokensPerModelCall,
			OutputTokens: WorkspaceAnalysisV1PlanMaxOutputTokens,
		}, true
	case WorkspaceAnalysisOperationAnswerSynthesis:
		return WorkspaceAnalysisBudgetAmount{
			ModelCalls: 1, InputTokens: WorkspaceAnalysisV1MaxInputTokensPerModelCall,
			OutputTokens: synthesisOutputCap,
		}, true
	case WorkspaceAnalysisOperationFaithfulnessReview:
		return WorkspaceAnalysisBudgetAmount{
			ModelCalls: 1, InputTokens: WorkspaceAnalysisV1MaxInputTokensPerModelCall,
			OutputTokens: WorkspaceAnalysisV1ReviewMaxOutputTokens,
		}, true
	default:
		return WorkspaceAnalysisBudgetAmount{}, false
	}
}

func workspaceAnalysisBudgetPrefixThrough(
	t *testing.T,
	path []workspaceAnalysisBudgetReachabilityStep,
	kind WorkspaceAnalysisOperationKind,
) []workspaceAnalysisBudgetReachabilityStep {
	t.Helper()
	for index, step := range path {
		if step.contract.Kind == kind {
			return path[:index+1]
		}
	}
	t.Fatalf("successful path is missing terminal prefix operation %s", kind)
	return nil
}

func workspaceAnalysisAssertReachableBudgetPrefixes(
	t *testing.T,
	pathName string,
	path []workspaceAnalysisBudgetReachabilityStep,
	limits WorkspaceAnalysisBudgetLimits,
) WorkspaceAnalysisBudgetAmount {
	t.Helper()
	// 以前序 reservation 全额结算作为最坏情形；真实成功用量只会小于或等于该前缀。
	used := WorkspaceAnalysisBudgetAmount{}
	run := WorkspaceAnalysisRun{Limits: limits}
	for index, step := range path {
		if !validWorkspaceAnalysisReservationShape(step.reservation, step.contract.CallKind) {
			t.Fatalf("%s prefix=%d operation=%s/%d has invalid reservation shape: %#v", pathName, index, step.contract.Kind, step.contract.Ordinal, step.reservation)
		}
		reservation := WorkspaceAnalysisBudgetReservation{CallKind: step.contract.CallKind, Reserved: step.reservation}
		if !workspaceAnalysisReservationMatchesOperationPolicy(run, reservation, step.contract.Kind) {
			t.Fatalf("%s prefix=%d operation=%s/%d does not match frozen operation policy: %#v", pathName, index, step.contract.Kind, step.contract.Ordinal, step.reservation)
		}
		if !workspaceAnalysisAmountWithin(step.reservation, used, limits.Amount) {
			t.Fatalf("%s prefix=%d operation=%s/%d cannot reserve its canonical next budget: used=%#v next=%#v maximum=%#v", pathName, index, step.contract.Kind, step.contract.Ordinal, used, step.reservation, limits.Amount)
		}
		used = workspaceAnalysisAddReachableBudget(t, pathName, index, used, step.reservation, limits.Amount)
	}
	return used
}

func workspaceAnalysisAddReachableBudget(
	t *testing.T,
	pathName string,
	prefix int,
	used WorkspaceAnalysisBudgetAmount,
	next WorkspaceAnalysisBudgetAmount,
	maximum WorkspaceAnalysisBudgetAmount,
) WorkspaceAnalysisBudgetAmount {
	t.Helper()
	if used.CostMicrounits != nil || next.CostMicrounits != nil || maximum.CostMicrounits != nil {
		t.Fatalf("%s prefix=%d introduced a v1 cost budget: used=%#v next=%#v maximum=%#v", pathName, prefix, used, next, maximum)
	}
	combined := WorkspaceAnalysisBudgetAmount{
		ModelCalls:   used.ModelCalls + next.ModelCalls,
		ToolCalls:    used.ToolCalls + next.ToolCalls,
		SourceReads:  used.SourceReads + next.SourceReads,
		InputTokens:  used.InputTokens + next.InputTokens,
		OutputTokens: used.OutputTokens + next.OutputTokens,
	}
	if combined.ModelCalls > maximum.ModelCalls || combined.ToolCalls > maximum.ToolCalls ||
		combined.SourceReads > maximum.SourceReads || combined.InputTokens > maximum.InputTokens ||
		combined.OutputTokens > maximum.OutputTokens {
		t.Fatalf("%s prefix=%d exceeds a frozen budget dimension: used=%#v next=%#v combined=%#v maximum=%#v", pathName, prefix, used, next, combined, maximum)
	}
	return combined
}

func workspaceAnalysisAssertBudgetAmount(t *testing.T, pathName string, got, want WorkspaceAnalysisBudgetAmount) {
	t.Helper()
	if got != want {
		t.Fatalf("%s budget=%#v, want %#v", pathName, got, want)
	}
}
