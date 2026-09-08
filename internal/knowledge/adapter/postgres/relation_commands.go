package postgres

import (
	"errors"
	"sort"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	"github.com/CodeZen-Lizhi/zhixu/internal/knowledge/domain"
)

type nodeRefGroup struct {
	Type domain.NodeType
	IDs  []foundation.ID
}

func groupNodeRefs(refs []domain.NodeRef) ([]nodeRefGroup, error) {
	ordered := append([]domain.NodeRef(nil), refs...)
	sort.Slice(ordered, func(i, j int) bool {
		if ordered[i].Type != ordered[j].Type {
			return ordered[i].Type < ordered[j].Type
		}
		return ordered[i].ID < ordered[j].ID
	})
	groups := make([]nodeRefGroup, 0, 2)
	var previous domain.NodeRef
	hasPrevious := false
	for _, ref := range ordered {
		if ref.Type != domain.NodeTypeClaim && ref.Type != domain.NodeTypeTopic {
			return nil, consistency(domain.ErrorCodeRelationInvalid, errors.New("relation node type is unsupported"))
		}
		if hasPrevious && previous == ref {
			continue
		}
		if len(groups) == 0 || groups[len(groups)-1].Type != ref.Type {
			groups = append(groups, nodeRefGroup{Type: ref.Type})
		}
		last := &groups[len(groups)-1]
		last.IDs = append(last.IDs, ref.ID)
		previous = ref
		hasPrevious = true
	}
	return groups, nil
}

func activeNodeLifecycle(nodeType domain.NodeType, lifecycle string) bool {
	if nodeType == domain.NodeTypeTopic {
		return lifecycle == string(domain.TopicStatusActive)
	}
	return lifecycle == string(domain.ClaimStatusSuggested) || lifecycle == string(domain.ClaimStatusConfirmed) || lifecycle == string(domain.ClaimStatusDisputed)
}

func equivalentSuggestedRelation(left, right domain.Relation) bool {
	return left.WorkspaceID == right.WorkspaceID && left.Source == right.Source && left.Target == right.Target &&
		left.Type == right.Type &&
		equalFloatPointers(left.ConfidenceScore, right.ConfidenceScore) && pointerTimeEqual(left.ValidFrom, right.ValidFrom) &&
		pointerTimeEqual(left.ValidTo, right.ValidTo) && left.Fingerprint == right.Fingerprint
}

func validateDistinctEvidence(evidence []domain.RelationEvidence) error {
	seen := make(map[string]struct{}, len(evidence))
	for _, item := range evidence {
		if _, duplicate := seen[item.EvidenceHash]; duplicate {
			return consistency(domain.ErrorCodeRelationEvidenceInvalid, errors.New("relation evidence hashes must be distinct"))
		}
		seen[item.EvidenceHash] = struct{}{}
	}
	return nil
}

func matchesSuggestedEvidenceSet(existing, requested []domain.RelationEvidence) bool {
	unconfirmed := make([]domain.RelationEvidence, 0, len(existing))
	for _, item := range existing {
		if item.Confirmation == nil {
			unconfirmed = append(unconfirmed, item)
		}
	}
	if len(requested) != len(unconfirmed) {
		return false
	}
	hashes := make(map[string]int, len(unconfirmed))
	for _, item := range unconfirmed {
		hashes[domain.ComputeRelationEvidenceSemanticHash(item)]++
	}
	for _, item := range requested {
		hash := domain.ComputeRelationEvidenceSemanticHash(item)
		if hashes[hash] == 0 {
			return false
		}
		hashes[hash]--
	}
	return true
}

func sortedEvidence(evidence []domain.RelationEvidence) []domain.RelationEvidence {
	result := append([]domain.RelationEvidence(nil), evidence...)
	sort.Slice(result, func(i, j int) bool { return result[i].EvidenceHash < result[j].EvidenceHash })
	return result
}

func confirmationValues(value *domain.Confirmation) (any, any) {
	if value == nil {
		return nil, nil
	}
	return string(value.Method), value.Reference
}

func nullableEvidenceFingerprint(value string) any {
	if value == "" {
		return nil
	}
	return value
}

func pointerTimeEqual(left, right *time.Time) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return left.Equal(*right)
}
