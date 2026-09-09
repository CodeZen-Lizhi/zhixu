package application

import (
	agentdomain "github.com/CodeZen-Lizhi/zhixu/internal/agent/domain"
	"strings"
	"testing"
)

func TestWorkspaceAnalysisV2FinalizerCommandsBindVersionAndNullableGit(t *testing.T) {
	lookup := workspaceAnalysisFinalizerTestLookup()
	lookup.DefinitionVersion = 2
	command := FinalizeWorkspaceAnalysisSuccessCommand{
		WorkspaceAnalysisPublicationLookup: lookup, ExpectedAnswerVersion: 1,
		CandidateID: workspaceAnalysisFinalizerTestID(20), CandidateHash: strings.Repeat("a", 64),
		ValidationReceiptID: workspaceAnalysisFinalizerTestID(21), ValidationReceiptHash: strings.Repeat("b", 64),
		ReviewModelResultID: workspaceAnalysisFinalizerTestID(22), ReviewModelResultHash: strings.Repeat("c", 64),
	}
	if err := command.Validate(); err != nil {
		t.Fatalf("v2 without Git: %v", err)
	}
	for _, version := range []int64{-1, 0, 1, 3} {
		changed := command
		changed.DefinitionVersion = version
		if changed.Validate() == nil {
			t.Fatalf("version %d accepted absent Git", version)
		}
	}
	for _, change := range []func(*FinalizeWorkspaceAnalysisSuccessCommand){
		func(c *FinalizeWorkspaceAnalysisSuccessCommand) {
			c.GitReceiptID = workspaceAnalysisFinalizerTestID(23)
		},
		func(c *FinalizeWorkspaceAnalysisSuccessCommand) { c.GitReceiptHash = strings.Repeat("d", 64) },
		func(c *FinalizeWorkspaceAnalysisSuccessCommand) {
			c.GitReceiptID = c.CandidateID
			c.GitReceiptHash = strings.Repeat("d", 64)
		},
	} {
		changed := command
		change(&changed)
		if changed.Validate() == nil {
			t.Fatal("incomplete or reused Git proof accepted")
		}
	}
	operation := workspaceAnalysisFinalizerTestID(24)
	artifact := &WorkspaceAnalysisTerminationArtifact{Kind: WorkspaceAnalysisTerminationArtifactDecisionReceipt, ID: workspaceAnalysisFinalizerTestID(25), Hash: strings.Repeat("e", 64)}
	termination := workspaceAnalysisTerminationTestCommand(lookup, agentdomain.WorkspaceAnalysisRunEvidenceInsufficient, &operation, artifact)
	if err := termination.Validate(); err != nil {
		t.Fatalf("v2 finish refusal: %v", err)
	}
	termination.DefinitionVersion = 1
	if termination.Validate() == nil {
		t.Fatal("v1 accepted decision receipt")
	}
	termination.DefinitionVersion = 2
	artifact.Kind = WorkspaceAnalysisTerminationArtifactToolReceipt
	if termination.Validate() == nil {
		t.Fatal("v2 accepted static search receipt")
	}
}
