package approvaldispatchpostgres

import (
	"errors"
	"testing"
	"time"

	changedispatch "github.com/CodeZen-Lizhi/zhixu/internal/changecontrol/dispatch"
	"github.com/CodeZen-Lizhi/zhixu/internal/changecontrol/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
)

func TestValidateApprovedDispatchSafetyAcceptsCreateOnlyAbsenceToken(t *testing.T) {
	workspaceID := foundation.ID("workspace")
	targetPath := "notes/new.md"
	absenceToken, err := domain.ComputeAbsenceToken(workspaceID, targetPath)
	if err != nil {
		t.Fatal(err)
	}
	head := "abcdef0123456789abcdef0123456789abcdef01"
	command := changedispatch.Command{
		WorkspaceID: workspaceID,
		Approval: domain.Approval{
			ApprovedGitHead: &head,
			DecidedAt:       time.Now().UTC(),
		},
		ObservedBaseHash: absenceToken,
		ObservedGitHead:  head,
	}

	if err := validateApprovedDispatchSafety(command, targetPath, domain.TargetModeCreateOnly, absenceToken); err != nil {
		t.Fatalf("valid CREATE_ONLY safety facts rejected: %v", err)
	}
}

func TestValidateApprovedDispatchSafetyRejectsAbsenceTokenForDifferentTarget(t *testing.T) {
	workspaceID := foundation.ID("workspace")
	absenceToken, err := domain.ComputeAbsenceToken(workspaceID, "notes/other.md")
	if err != nil {
		t.Fatal(err)
	}
	head := "abcdef0123456789abcdef0123456789abcdef01"
	command := changedispatch.Command{
		WorkspaceID: workspaceID,
		Approval: domain.Approval{
			ApprovedGitHead: &head,
			DecidedAt:       time.Now().UTC(),
		},
		ObservedBaseHash: absenceToken,
		ObservedGitHead:  head,
	}

	err = validateApprovedDispatchSafety(command, "notes/new.md", domain.TargetModeCreateOnly, absenceToken)
	var classified *foundation.Error
	if !errors.As(err, &classified) || classified.Kind != foundation.ErrorVersionConflict || classified.Code != "APPROVAL_DISPATCH_SAFETY_CONFLICT" {
		t.Fatalf("error = %#v, want version conflict safety error", err)
	}
}
