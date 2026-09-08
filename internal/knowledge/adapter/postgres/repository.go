// Package postgres persists Knowledge through the shared GORM transaction boundary.
package postgres

import (
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	"github.com/CodeZen-Lizhi/zhixu/internal/knowledge/domain"
)

const (
	commandCreateTopic        = domain.CommandCreateTopic
	commandSuggestClaim       = domain.CommandSuggestClaim
	commandConfirmClaim       = domain.CommandConfirmClaim
	commandTransitionClaim    = domain.CommandTransitionClaim
	commandSuggestRelation    = domain.CommandSuggestRelation
	commandConfirmRelation    = domain.CommandConfirmRelation
	commandTransitionRelation = domain.CommandTransitionRelation
	commandOpenConflict       = domain.CommandOpenConflict
	commandTransitionConflict = domain.CommandTransitionConflict
)

const (
	aggregateTopic    = domain.AggregateTopic
	aggregateClaim    = domain.AggregateClaim
	aggregateRelation = domain.AggregateRelation
	aggregateConflict = domain.AggregateConflict
)

type commandReceipt struct {
	RequestHash      string
	CommandType      domain.CommandType
	AggregateType    domain.AggregateType
	AggregateID      foundation.ID
	AggregateVersion int64
}

func equivalentSuggestedClaim(left, right domain.Claim) bool {
	return left.WorkspaceID == right.WorkspaceID && left.Statement == right.Statement &&
		left.NormalizedStatement == right.NormalizedStatement && left.Applicability.Hash == right.Applicability.Hash &&
		equalFloatPointers(left.ConfidenceScore, right.ConfidenceScore) &&
		string(left.ConfidenceFactors) == string(right.ConfidenceFactors)
}

func equalFloatPointers(left, right *float64) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return *left == *right
}

func idsAsStrings(ids []foundation.ID) []string {
	result := make([]string, len(ids))
	for index := range ids {
		result[index] = string(ids[index])
	}
	return result
}

func statusStrings[T ~string](statuses []T) []string {
	result := make([]string, len(statuses))
	for index := range statuses {
		result[index] = string(statuses[index])
	}
	return result
}

func timePointer(value *time.Time) any {
	if value == nil {
		return nil
	}
	return value.UTC()
}

func pointerString(value *string) any {
	if value == nil {
		return nil
	}
	return *value
}
