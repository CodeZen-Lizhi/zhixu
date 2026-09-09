package domain

import (
	"bytes"
	"fmt"
	"strings"
	"testing"
	"time"
)

func TestWorkspaceAnalysisV2DecisionStrictUnionAndPrivateCanonicalReceipt(t *testing.T) {
	valid := []string{
		`{"action":"git_status","query":null,"evidence_ref":null,"evidence_refs":null}`,
		`{"action":"finish","query":null,"evidence_ref":null,"evidence_refs":null}`,
		`{"action":"knowledge_search","query":"private query","evidence_ref":null,"evidence_refs":null}`,
		`{"action":"source_read","query":null,"evidence_ref":"E32","evidence_refs":null}`,
		`{"action":"citation_validation","query":null,"evidence_ref":null,"evidence_refs":["E9","E32"]}`,
	}
	for _, document := range valid {
		decision, err := DecodeWorkspaceAnalysisDecision([]byte(document))
		if err != nil {
			t.Fatalf("valid decision rejected: %v", err)
		}
		canonical, err := decision.Canonical()
		if err != nil || !bytes.Equal(canonical, []byte(document)) {
			t.Fatal("decision canonical bytes changed")
		}
		if strings.Contains(fmt.Sprintf("%v %#v", decision, decision), "private query") {
			t.Fatal("private decision leaked through formatting")
		}
	}
	invalid := []string{
		`{"action":"finish","query":null,"evidence_ref":null}`,
		`{"action":"finish","action":"git_status","query":null,"evidence_ref":null,"evidence_refs":null}`,
		`{"action":"finish","query":null,"evidence_ref":null,"evidence_refs":null,"permissions":"admin"}`,
		`{"action":"finish","query":null,"evidence_ref":null,"evidence_refs":[]}`,
		`{"action":"knowledge_search","query":" query ","evidence_ref":null,"evidence_refs":null}`,
		`{"action":"source_read","query":null,"evidence_ref":"E33","evidence_refs":null}`,
		`{"action":"source_read","query":null,"evidence_ref":"E01","evidence_refs":null}`,
		`{"action":"citation_validation","query":null,"evidence_ref":null,"evidence_refs":["E9","E9"]}`,
		`{"action":"write_file","query":null,"evidence_ref":null,"evidence_refs":null}`,
	}
	for _, document := range invalid {
		if _, err := DecodeWorkspaceAnalysisDecision([]byte(document)); err == nil {
			t.Fatal("invalid decision document accepted")
		}
	}
}

func TestWorkspaceAnalysisV2RunRejectsMixedVersionsAndKeepsPublicationAllowance(t *testing.T) {
	run := validWorkspaceAnalysisPersistenceRun()
	run.DefinitionVersion, run.PolicyVersion = 2, WorkspaceAnalysisPolicyVersionV2
	run.Limits = WorkspaceAnalysisBudgetLimits{Nodes: 4, ToolConcurrency: 1, Amount: WorkspaceAnalysisBudgetAmount{
		ModelCalls: 14, ToolCalls: 13, SourceReads: 8, InputTokens: 917504, OutputTokens: 11264}}
	deadlines, err := DeriveWorkspaceAnalysisV2Deadlines(run.Timeouts)
	if err != nil {
		t.Fatal(err)
	}
	run.DeadlineAt = run.CreatedAt.Add(deadlines.RunDeadline())
	if err := ValidateWorkspaceAnalysisRun(run); err != nil {
		t.Fatalf("v2 run: %v", err)
	}
	for _, pair := range [][2]int{{1, 2}, {2, 1}, {3, 3}} {
		invalid := run
		invalid.DefinitionVersion, invalid.PolicyVersion = int64(pair[0]), pair[1]
		if ValidateWorkspaceAnalysisRun(invalid) == nil {
			t.Fatal("mixed run versions accepted")
		}
	}
	invalid := run
	invalid.Limits.Amount.OutputTokens = 7168
	if ValidateWorkspaceAnalysisRun(invalid) == nil {
		t.Fatal("missing synthesis allowance accepted")
	}
	if deadlines.DecideNextDeadline() != 12*(run.Timeouts.PlanModelTimeout+45*time.Second+5*time.Second)+15*time.Second {
		t.Fatal("decision timeout no longer reserves all bounded calls")
	}
	invalid = run
	invalid.DeadlineAt = invalid.DeadlineAt.Add(time.Microsecond)
	if ValidateWorkspaceAnalysisRun(invalid) == nil {
		t.Fatal("unfrozen v2 deadline accepted")
	}
}
