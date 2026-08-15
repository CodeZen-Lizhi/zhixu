package domain

import (
	"strings"
	"testing"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
)

func TestRawContentHashPreservesLineEndings(t *testing.T) {
	if RawContentHash("a\n") == RawContentHash("a\r\n") {
		t.Fatal("raw content hash must distinguish LF and CRLF")
	}
}

func TestValidateAppendProposalRevision(t *testing.T) {
	command := AppendProposalRevision{
		WorkspaceID: foundation.ID("workspace"), ProposalID: foundation.ID("proposal"),
		IdempotencyKey: "revision-1", ExpectedProposalVersion: 1,
		SourceRevisionID: foundation.ID("source"), SourceChangeHash: strings.Repeat("a", 64),
		NewRevisionID:       foundation.ID("new-revision"),
		ExpectedCurrentHash: RawContentHash("base"), CurrentContent: "base",
		PreviewFingerprint: strings.Repeat("b", 64), MergeAlgorithm: ProposalRevisionMergeAlgorithm,
		MergeAlgorithmVersion: ProposalRevisionMergeAlgorithmVersion, FinalContent: "merged",
		EvidenceSummary: "evidence", Risk: "risk", RollbackPlan: "rollback",
		CreatedAt: time.Unix(1, 0),
	}
	if err := ValidateAppendProposalRevision(command); err != nil {
		t.Fatalf("valid command rejected: %v", err)
	}
	command.CurrentContent = strings.Repeat("x", ProposalRevisionMaxBytes+1)
	if err := ValidateAppendProposalRevision(command); err != ErrProposalRevisionInputTooLarge {
		t.Fatalf("oversized base error = %v", err)
	}
}

func TestComputeRevisionRequestHashUsesCallerVisibleCurrentBinding(t *testing.T) {
	command := AppendProposalRevision{WorkspaceID: "w", ProposalID: "p", IdempotencyKey: "k", ExpectedProposalVersion: 1,
		SourceRevisionID: "r", SourceChangeHash: strings.Repeat("a", 64), ExpectedCurrentHash: RawContentHash("base"), CurrentContent: "base",
		PreviewFingerprint: "preview", MergeAlgorithm: "git-merge-file", MergeAlgorithmVersion: "v1", FinalContent: "x", EvidenceSummary: "e", Risk: "r", RollbackPlan: "b"}
	other := command
	other.CurrentContent = "base\r\n"
	if ComputeRevisionRequestHash(command) != ComputeRevisionRequestHash(other) {
		t.Fatal("request hash must not require a server-only current-content read")
	}
	other.ExpectedCurrentHash = RawContentHash(other.CurrentContent)
	if ComputeRevisionRequestHash(command) == ComputeRevisionRequestHash(other) {
		t.Fatal("request hash must bind the caller-visible expected current hash")
	}
	other = command
	other.FinalContent = "x\r\n"
	if ComputeRevisionRequestHash(command) == ComputeRevisionRequestHash(other) {
		t.Fatal("request hash must bind exact final-content bytes")
	}
	first := command
	first.EvidenceSummary = "x\nrisk:y"
	first.Risk = "z"
	second := command
	second.EvidenceSummary = "x"
	second.Risk = "y\nrisk:z"
	if ComputeRevisionRequestHash(first) == ComputeRevisionRequestHash(second) {
		t.Fatal("request hash fields must be framed against delimiter ambiguity")
	}
}

func TestValidateAppendProposalRevisionRejectsCompleteReservedMarkerLine(t *testing.T) {
	command := AppendProposalRevision{
		WorkspaceID: "workspace", ProposalID: "proposal", IdempotencyKey: "revision-1", ExpectedProposalVersion: 1,
		SourceRevisionID: "source", SourceChangeHash: strings.Repeat("a", 64), ExpectedCurrentHash: RawContentHash("base"),
		PreviewFingerprint: strings.Repeat("b", 64), MergeAlgorithm: ProposalRevisionMergeAlgorithm,
		MergeAlgorithmVersion: ProposalRevisionMergeAlgorithmVersion, FinalContent: "prefix <<<<<<<< marker-like text\n",
		EvidenceSummary: "evidence", Risk: "risk", RollbackPlan: "rollback",
	}
	if err := ValidateAppendProposalRevisionRequest(command); err != nil {
		t.Fatalf("marker-like content rejected: %v", err)
	}
	for _, marker := range []string{
		strings.Repeat("<", 32) + " CURRENT",
		strings.Repeat("|", 32) + " BASE",
		strings.Repeat("=", 32),
		strings.Repeat(">", 32) + " PROPOSED",
	} {
		command.FinalContent = "before\n" + marker + "\nafter"
		if err := ValidateAppendProposalRevisionRequest(command); err != ErrWritebackInvalidInput {
			t.Fatalf("reserved marker %q error = %v", marker, err)
		}
		command.FinalContent = "before\r\n" + marker + "\r\nafter"
		if err := ValidateAppendProposalRevisionRequest(command); err != ErrWritebackInvalidInput {
			t.Fatalf("reserved CRLF marker %q error = %v", marker, err)
		}
	}
}

func TestEvaluateProposalRevisionCapability(t *testing.T) {
	runID := foundation.ID("run")
	base := ProposalRevisionCapabilityFacts{
		ProposalType: ProposalTypeFilePatch, ProposalStatus: StatusNeedsRevision, TargetMode: TargetModeReplace,
		CurrentRevisionID: "revision", RevisionID: "revision", RevisionContentSize: 128,
	}
	tests := []struct {
		name   string
		mutate func(*ProposalRevisionCapabilityFacts)
		want   ProposalRevisionCapability
	}{
		{name: "no workflow is available", want: ProposalRevisionCapability{Editable: true, Reason: ProposalRevisionAvailable}},
		{name: "failed workflow is available", mutate: func(facts *ProposalRevisionCapabilityFacts) {
			facts.WorkflowRunID, facts.WorkflowRunStatus = &runID, "failed"
		}, want: ProposalRevisionCapability{Editable: true, Reason: ProposalRevisionAvailable}},
		{name: "cancelled workflow is available", mutate: func(facts *ProposalRevisionCapabilityFacts) {
			facts.WorkflowRunID, facts.WorkflowRunStatus = &runID, "cancelled"
		}, want: ProposalRevisionCapability{Editable: true, Reason: ProposalRevisionAvailable}},
		{name: "running workflow is active", mutate: func(facts *ProposalRevisionCapabilityFacts) {
			facts.WorkflowRunID, facts.WorkflowRunStatus = &runID, "running"
		}, want: ProposalRevisionCapability{Editable: false, Reason: ProposalRevisionWorkflowActive}},
		{name: "missing workflow status fails closed", mutate: func(facts *ProposalRevisionCapabilityFacts) {
			facts.WorkflowRunID = &runID
		}, want: ProposalRevisionCapability{Editable: false, Reason: ProposalRevisionWorkflowActive}},
		{name: "succeeded workflow crossed side effect boundary", mutate: func(facts *ProposalRevisionCapabilityFacts) {
			facts.WorkflowRunID, facts.WorkflowRunStatus = &runID, "succeeded"
		}, want: ProposalRevisionCapability{Editable: false, Reason: ProposalRevisionSideEffectStarted}},
		{name: "oversized revision is unavailable", mutate: func(facts *ProposalRevisionCapabilityFacts) {
			facts.ProposalStatus = StatusReady
			facts.RevisionContentSize = ProposalRevisionMaxBytes + 1
		}, want: ProposalRevisionCapability{Editable: false, Reason: ProposalRevisionCapabilityTooLarge}},
		{name: "stale pointer is unavailable", mutate: func(facts *ProposalRevisionCapabilityFacts) {
			facts.CurrentRevisionID = "other"
		}, want: ProposalRevisionCapability{Editable: false, Reason: ProposalRevisionCapabilityStale}},
		{name: "missing pointer is unavailable", mutate: func(facts *ProposalRevisionCapabilityFacts) {
			facts.CurrentRevisionID = ""
		}, want: ProposalRevisionCapability{Editable: false, Reason: ProposalRevisionCapabilityStale}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			facts := base
			if test.mutate != nil {
				test.mutate(&facts)
			}
			if got := EvaluateProposalRevisionCapability(facts); got != test.want {
				t.Fatalf("capability = %#v, want %#v", got, test.want)
			}
		})
	}
}

func TestGateProposalRevisionMergeEngine(t *testing.T) {
	available := ProposalRevisionCapability{Editable: true, Reason: ProposalRevisionAvailable}
	if got := GateProposalRevisionMergeEngine(available, false); got != (ProposalRevisionCapability{Editable: false, Reason: ProposalRevisionEngineUnavailable}) {
		t.Fatalf("capability = %#v", got)
	}
	locked := ProposalRevisionCapability{Editable: false, Reason: ProposalRevisionUnsupportedType}
	if got := GateProposalRevisionMergeEngine(locked, false); got != locked {
		t.Fatalf("specific reason was overwritten: %#v", got)
	}
}
