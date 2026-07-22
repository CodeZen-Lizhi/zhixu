package application

import (
	"testing"

	"github.com/CodeZen-Lizhi/zhixu/internal/health/domain"
)

func TestRepairOptionsRemainUnavailableWithoutApplyOwner(t *testing.T) {
	issueTypes := []domain.IssueType{
		domain.IssueTypeOrphan,
		domain.IssueTypeDuplicate,
		domain.IssueTypeConflict,
		domain.IssueTypeStale,
		domain.IssueTypeMissingSource,
		domain.IssueTypeLowConfidence,
		domain.IssueTypeBrokenReference,
		domain.IssueTypeIndexError,
		domain.IssueTypeSupersededUsage,
	}
	for _, issueType := range issueTypes {
		options := RepairOptionsForIssue(issueType)
		if len(options) != 1 || options[0].Available || options[0].UnavailableReason != repairOwnerUnavailableReason {
			t.Fatalf("issue type %s options=%#v", issueType, options)
		}
	}
	if options := RepairOptionsForIssue(domain.IssueTypeReviewInvalidated); options != nil {
		t.Fatalf("unavailable detector must not expose an issue repair option: %#v", options)
	}
}
