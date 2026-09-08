package postgres

import (
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	"github.com/CodeZen-Lizhi/zhixu/internal/knowledge/domain"
)

func equivalentOpenConflict(existing domain.ConflictResult, requested domain.Conflict, members []domain.ConflictMember) bool {
	if existing.Conflict.WorkspaceID != requested.WorkspaceID || !pointerEqual(existing.Conflict.TopicID, requested.TopicID) ||
		existing.Conflict.Severity != requested.Severity || existing.Conflict.Summary != requested.Summary ||
		existing.Conflict.ApplicabilityAssessment != requested.ApplicabilityAssessment ||
		existing.Conflict.ReviewedOverlapReason != requested.ReviewedOverlapReason || existing.Conflict.Fingerprint != requested.Fingerprint ||
		len(existing.Members) != len(members) {
		return false
	}
	want := make(map[foundation.ID]string, len(members))
	for _, member := range members {
		want[member.ClaimID] = member.ApplicabilityHash + "\x00" + member.PositionSummary
	}
	for _, member := range existing.Members {
		if want[member.ClaimID] != member.ApplicabilityHash+"\x00"+member.PositionSummary {
			return false
		}
	}
	return true
}

func memberClaimIDs(members []domain.ConflictMember) []string {
	result := make([]string, len(members))
	for index := range members {
		result[index] = string(members[index].ClaimID)
	}
	return result
}

func optionalIDValue(value *foundation.ID) any {
	if value == nil {
		return nil
	}
	return string(*value)
}
