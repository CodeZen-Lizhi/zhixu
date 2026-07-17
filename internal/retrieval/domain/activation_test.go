package domain

import (
	"testing"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
)

func TestValidateActivationCommandSupportsInitialAndReplacementActivation(t *testing.T) {
	t.Parallel()

	target := validIndexVersion(nil)
	target.Status = IndexStatusReady
	command := validActivationCommand(target)
	if err := ValidateActivationCommand(nil, target, command); err != nil {
		t.Fatalf("initial activation error = %v", err)
	}

	current := validIndexVersion(nil)
	current.ID = "50000000-0000-4000-8000-000000000010"
	current.Status = IndexStatusActive
	current.Version = 4
	currentID := current.ID
	currentVersion := current.Version
	command.ExpectedCurrentIndexVersionID = &currentID
	command.ExpectedCurrentVersion = &currentVersion
	if err := ValidateActivationCommand(&current, target, command); err != nil {
		t.Fatalf("replacement activation error = %v", err)
	}
}

func TestValidateActivationCommandRejectsStaleOrNonReadyTarget(t *testing.T) {
	t.Parallel()

	target := validIndexVersion(nil)
	target.Status = IndexStatusReady
	command := validActivationCommand(target)
	command.ExpectedTargetVersion++
	err := ValidateActivationCommand(nil, target, command)
	if err == nil {
		t.Fatal("stale activation accepted")
	}
	assertDomainError(t, err, foundation.ErrorVersionConflict, ErrorCodeActivationInvalid)

	command.ExpectedTargetVersion = target.Version
	target.Status = IndexStatusBuilding
	if err := ValidateActivationCommand(nil, target, command); err == nil {
		t.Fatal("building target activated")
	}
}

func TestValidateActivationCommandInputRequiresCompleteCurrentExpectation(t *testing.T) {
	t.Parallel()
	target := validIndexVersion(nil)
	command := validActivationCommand(target)
	currentID := foundation.ID("50000000-0000-4000-8000-000000000099")
	command.ExpectedCurrentIndexVersionID = &currentID
	if err := ValidateActivationCommandInput(command); err == nil {
		t.Fatal("partial current active expectation accepted")
	}
	currentVersion := int64(2)
	command.ExpectedCurrentVersion = &currentVersion
	if err := ValidateActivationCommandInput(command); err != nil {
		t.Fatalf("complete activation input error = %v", err)
	}
}

func TestValidateRollbackActivationCommandRequiresActiveAndRetiringPair(t *testing.T) {
	t.Parallel()

	current := validIndexVersion(nil)
	current.ID = "50000000-0000-4000-8000-000000000020"
	current.Status = IndexStatusActive
	target := validIndexVersion(nil)
	target.ID = "50000000-0000-4000-8000-000000000021"
	target.Status = IndexStatusRetiring
	command := RollbackActivationCommand{
		ActivationID:                  "50000000-0000-4000-8000-000000000022",
		WorkspaceID:                   target.WorkspaceID,
		TargetIndexVersionID:          target.ID,
		ExpectedTargetVersion:         target.Version,
		ExpectedCurrentIndexVersionID: current.ID,
		ExpectedCurrentVersion:        current.Version,
		IdempotencyKey:                "rollback-1",
		ReasonCode:                    "OPERATOR_ROLLBACK",
		At:                            target.UpdatedAt.Add(time.Minute),
	}
	if err := ValidateRollbackActivationCommand(current, target, command); err != nil {
		t.Fatalf("rollback error = %v", err)
	}
	target.Status = IndexStatusArchived
	if err := ValidateRollbackActivationCommand(current, target, command); err == nil {
		t.Fatal("archived target rolled back")
	}
}

func TestValidateRollbackActivationCommandInputRejectsSameTargetAndCurrent(t *testing.T) {
	t.Parallel()
	command := RollbackActivationCommand{
		ActivationID: "50000000-0000-4000-8000-000000000030", WorkspaceID: "50000000-0000-4000-8000-000000000031",
		TargetIndexVersionID: "50000000-0000-4000-8000-000000000032", ExpectedTargetVersion: 3,
		ExpectedCurrentIndexVersionID: "50000000-0000-4000-8000-000000000032", ExpectedCurrentVersion: 2,
		IdempotencyKey: "rollback-input", ReasonCode: "OPERATOR_ROLLBACK", At: time.Now().UTC(),
	}
	if err := ValidateRollbackActivationCommandInput(command); err == nil {
		t.Fatal("rollback accepted the target as current active")
	}
}

func TestValidateBuildReadyAcceptsFTSOnlyAndDegradedHybrid(t *testing.T) {
	t.Parallel()

	fts := validIndexVersion(nil)
	ftsStatus := completeBuildStatus(fts)
	if err := ValidateBuildReady(fts, ftsStatus); err != nil {
		t.Fatalf("FTS build status error = %v", err)
	}

	embeddingID := validEmbeddingVersion().ID
	hybrid := validIndexVersion(&embeddingID)
	hybrid.DegradedCapabilities = []DegradedCapability{DegradedVector}
	hybridStatus := completeBuildStatus(hybrid)
	hybridStatus.VectorReadyCount = 1
	hybridStatus.VectorSkippedOversizedCount = 1
	if err := ValidateBuildReady(hybrid, hybridStatus); err != nil {
		t.Fatalf("degraded hybrid build status error = %v", err)
	}
}

func TestValidateBuildReadyRejectsIncompleteOrFalseCapabilityClaims(t *testing.T) {
	t.Parallel()

	index := validIndexVersion(nil)
	status := completeBuildStatus(index)
	status.ProjectionCount--
	err := ValidateBuildReady(index, status)
	if err == nil {
		t.Fatal("missing projection accepted")
	}
	assertDomainError(t, err, foundation.ErrorConsistencyViolation, ErrorCodeBuildIncomplete)

	embeddingID := validEmbeddingVersion().ID
	hybrid := validIndexVersion(&embeddingID)
	hybrid.DegradedCapabilities = []DegradedCapability{DegradedVector}
	status = completeBuildStatus(hybrid)
	status.VectorReadyCount = hybrid.ExpectedChunkCount
	if err := ValidateBuildReady(hybrid, status); err == nil {
		t.Fatal("fully ready vector set falsely declared degraded")
	}
}

func validActivationCommand(target IndexVersion) ActivationCommand {
	return ActivationCommand{
		ActivationID:          "50000000-0000-4000-8000-000000000001",
		WorkspaceID:           target.WorkspaceID,
		TargetIndexVersionID:  target.ID,
		ExpectedTargetVersion: target.Version,
		IdempotencyKey:        "activate-1",
		ReasonCode:            "BUILD_VERIFIED",
		At:                    target.UpdatedAt.Add(time.Minute),
	}
}

func completeBuildStatus(index IndexVersion) BuildStatus {
	status := BuildStatus{
		WorkspaceID:          index.WorkspaceID,
		IndexVersionID:       index.ID,
		IndexStatus:          index.Status,
		IndexVersion:         index.Version,
		ExpectedChunkCount:   index.ExpectedChunkCount,
		ManifestChunkCount:   index.ExpectedChunkCount,
		ProjectionCount:      index.ExpectedChunkCount,
		LexicalReadyCount:    index.ExpectedChunkCount,
		DegradedCapabilities: append([]DegradedCapability(nil), index.DegradedCapabilities...),
	}
	if index.EmbeddingVersionID == nil {
		status.VectorDisabledCount = index.ExpectedChunkCount
	} else {
		status.VectorReadyCount = index.ExpectedChunkCount
	}
	return status
}
