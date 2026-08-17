package domain

import (
	"testing"
	"time"
)

func TestWorkspaceAnalysisV1DeadlinesAreDerivedFromFrozenTimeoutSnapshot(t *testing.T) {
	timeouts := workspaceAnalysisDomainTestTimeouts()
	deadlines, err := DeriveWorkspaceAnalysisV1Deadlines(timeouts)
	if err != nil {
		t.Fatal(err)
	}
	if deadlines.InspectWorkspaceDeadline() != 35*time.Second ||
		deadlines.RetrieveEvidenceDeadline() != 3*time.Minute ||
		deadlines.ReadEvidenceDeadline() != 70*time.Second ||
		deadlines.SynthesizeAnswerDeadline() != 5*time.Minute+15*time.Second ||
		deadlines.ValidateCitationsDeadline() != 35*time.Second ||
		deadlines.ReviewPublishDeadline() != 3*time.Minute+15*time.Second ||
		deadlines.RunDeadline() != 13*time.Minute+50*time.Second ||
		deadlines.MinimumRiverJobTimeout() != 5*time.Minute+45*time.Second {
		t.Fatalf("derived deadlines drifted: %+v", deadlines)
	}
}

func TestWorkspaceAnalysisV1DeadlinesRejectInvalidSnapshotAndDerivedDrift(t *testing.T) {
	timeouts := workspaceAnalysisDomainTestTimeouts()
	timeouts.PlanModelTimeout = 0
	if _, err := DeriveWorkspaceAnalysisV1Deadlines(timeouts); errorCode(err) != ErrorCodeWorkspaceAnalysisPolicyInvalid {
		t.Fatalf("invalid timeout err=%v", err)
	}

	deadlines, err := DeriveWorkspaceAnalysisV1Deadlines(workspaceAnalysisDomainTestTimeouts())
	if err != nil {
		t.Fatal(err)
	}
	deadlines.nodes[0]++
	limits := WorkspaceAnalysisRuntimeLimits{
		RiverJobTimeout: 6 * time.Minute, LeaseDuration: 35 * time.Second, HeartbeatInterval: 10 * time.Second,
	}
	if err := deadlines.ValidateRuntimeReadiness(limits); errorCode(err) != ErrorCodeWorkspaceAnalysisPolicyInvalid {
		t.Fatalf("drifted deadline accepted: %v", err)
	}
}

func workspaceAnalysisDomainTestTimeouts() WorkspaceAnalysisV1Timeouts {
	return WorkspaceAnalysisV1Timeouts{
		PlanModelTimeout: 2 * time.Minute, SynthesisModelTimeout: 5 * time.Minute, ReviewModelTimeout: 3 * time.Minute,
		GitToolTimeout: 30 * time.Second, SearchToolTimeout: 45 * time.Second,
		SourceReadToolTimeout: 20 * time.Second, ValidateCitationToolTimeout: 25 * time.Second,
	}
}
