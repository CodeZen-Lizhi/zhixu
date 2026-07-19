package application

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"sort"
	"strconv"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	"github.com/CodeZen-Lizhi/zhixu/internal/knowledge/domain"
)

func hashRequest(kind string, values ...string) string {
	digest := sha256.New()
	writeHashPart(digest, kind)
	for _, value := range values {
		writeHashPart(digest, value)
	}
	return hex.EncodeToString(digest.Sum(nil))
}

type hashWriter interface{ Write([]byte) (int, error) }

func writeHashPart(dst hashWriter, value string) {
	var size [8]byte
	binary.BigEndian.PutUint64(size[:], uint64(len(value)))
	_, _ = dst.Write(size[:])
	_, _ = dst.Write([]byte(value))
}

func hashTopicRequest(topic domain.Topic) string {
	values := []string{string(topic.WorkspaceID), topic.Name, topic.NormalizedName, topic.Description}
	for _, alias := range topic.Aliases {
		values = append(values, alias.Name, alias.NormalizedName)
	}
	return hashRequest("create-topic/v1", values...)
}
func hashClaimRequest(claim domain.Claim) string {
	score := ""
	if claim.ConfidenceScore != nil {
		score = strconv.FormatFloat(*claim.ConfidenceScore, 'g', -1, 64)
	}
	return hashRequest("suggest-claim/v1", string(claim.WorkspaceID), claim.Statement, string(claim.Applicability.CanonicalJSON), score, string(claim.ConfidenceFactors))
}
func hashConfirmClaimRequest(record domain.ConfirmClaimRecord) string {
	source := record.Source
	return hashRequest("confirm-claim/v1", string(record.WorkspaceID), string(record.ClaimID), strconv.FormatInt(record.ExpectedVersion, 10), string(source.Provenance.SourceVersionID), string(source.Provenance.SourceSpanID), string(source.SupportType), source.Reason, optional(source.ModelRunRef))
}
func hashSuggestRelationRequest(relation domain.Relation, evidence []domain.RelationEvidence) string {
	score, from, to := "", "", ""
	if relation.ConfidenceScore != nil {
		score = strconv.FormatFloat(*relation.ConfidenceScore, 'g', -1, 64)
	}
	if relation.ValidFrom != nil {
		from = relation.ValidFrom.UTC().Format(time.RFC3339Nano)
	}
	if relation.ValidTo != nil {
		to = relation.ValidTo.UTC().Format(time.RFC3339Nano)
	}
	values := []string{string(relation.WorkspaceID), string(relation.Source.Type), string(relation.Source.ID), string(relation.Target.Type), string(relation.Target.ID), string(relation.Type), score, from, to}
	semantics := make([]string, len(evidence))
	for i, item := range evidence {
		semantics[i] = relationEvidenceSemanticHash(item)
	}
	sort.Strings(semantics)
	values = append(values, semantics...)
	return hashRequest("suggest-relation/v1", values...)
}

func relationEvidenceSemanticHash(item domain.RelationEvidence) string {
	return domain.ComputeRelationEvidenceSemanticHash(item)
}
func hashConfirmRelationRequest(record domain.ConfirmRelationRecord) string {
	item := record.Evidence
	return hashRequest("confirm-relation/v1", string(record.WorkspaceID), string(record.RelationID), strconv.FormatInt(record.ExpectedVersion, 10), string(item.Provenance.SourceVersionID), string(item.Provenance.SourceSpanID), item.Reason, string(item.Applicability.CanonicalJSON), optional(item.ModelRunRef), string(record.Confirmation.Method), record.Confirmation.Reference)
}
func hashOpenConflictRequest(conflict domain.Conflict, members []domain.ConflictMember) string {
	values := []string{string(conflict.WorkspaceID), optionalID(conflict.TopicID), string(conflict.Severity), conflict.Summary, string(conflict.ApplicabilityAssessment), conflict.ReviewedOverlapReason}
	for _, member := range members {
		values = append(values, string(member.ClaimID), string(member.Applicability.CanonicalJSON), member.PositionSummary)
	}
	return hashRequest("open-conflict/v1", values...)
}
func optionalID(value *foundation.ID) string {
	if value == nil {
		return ""
	}
	return string(*value)
}

func validateTopicResult(workspaceID foundation.ID, requested domain.Topic, result domain.TopicResult) error {
	if result.Topic.WorkspaceID != workspaceID || domain.ValidateTopic(result.Topic) != nil || !sameTopicPayload(requested, result.Topic) || result.Topic.Version < 1 {
		return consistencyError("repository returned an invalid topic result")
	}
	if !result.Replayed && (result.Topic.Status != domain.TopicStatusActive || result.Topic.Version != 1) {
		return consistencyError("repository returned an invalid new topic lifecycle")
	}
	if !result.Replayed && result.Topic.ID != requested.ID {
		return consistencyError("repository changed newly created topic identity")
	}
	return nil
}
func sameTopicPayload(left, right domain.Topic) bool {
	if left.WorkspaceID != right.WorkspaceID || left.Name != right.Name || left.NormalizedName != right.NormalizedName || left.Description != right.Description || len(left.Aliases) != len(right.Aliases) {
		return false
	}
	for i := range left.Aliases {
		if left.Aliases[i] != right.Aliases[i] {
			return false
		}
	}
	return true
}

func validateClaimResult(workspaceID foundation.ID, requested domain.Claim, result domain.ClaimResult, replayed bool) error {
	if result.Claim.WorkspaceID != workspaceID || domain.ValidateClaimAggregate(result.Claim, result.Sources) != nil || result.Claim.Statement != requested.Statement || result.Claim.Applicability.Hash != requested.Applicability.Hash || result.Claim.Fingerprint != requested.Fingerprint || result.Claim.Version < 1 || !sameFloat(result.Claim.ConfidenceScore, requested.ConfidenceScore) || string(result.Claim.ConfidenceFactors) != string(requested.ConfidenceFactors) {
		return consistencyError("repository returned an invalid claim result")
	}
	if !replayed && (result.Claim.Status != domain.ClaimStatusSuggested || result.Claim.Version != 1) {
		return consistencyError("repository returned an invalid new claim lifecycle")
	}
	if !replayed && len(result.Sources) != 0 {
		return consistencyError("new suggested claim unexpectedly contains sources")
	}
	if !replayed && result.Claim.ID != requested.ID {
		return consistencyError("repository changed newly suggested claim identity")
	}
	return nil
}
func validateChangedClaimResult(workspaceID, claimID foundation.ID, expectedVersion int64, status domain.ClaimStatus, result domain.ClaimResult) error {
	if result.Claim.ID != claimID || result.Claim.WorkspaceID != workspaceID || result.Claim.Version < expectedVersion+1 || domain.ValidateClaimAggregate(result.Claim, result.Sources) != nil || (!result.Replayed && (result.Claim.Version != expectedVersion+1 || result.Claim.Status != status)) {
		return consistencyError("repository returned an invalid changed claim aggregate")
	}
	return nil
}

func validateConfirmedClaimResult(workspaceID, claimID foundation.ID, expectedVersion int64, requested domain.ClaimSource, result domain.ClaimResult) error {
	if err := validateChangedClaimResult(workspaceID, claimID, expectedVersion, domain.ClaimStatusConfirmed, result); err != nil {
		return err
	}
	for _, source := range result.Sources {
		if source.SupportType == domain.ClaimSupportSupports && source.Provenance == requested.Provenance && source.Reason == requested.Reason && optional(source.ModelRunRef) == optional(requested.ModelRunRef) {
			return nil
		}
	}
	return consistencyError("confirmed claim replay lost its supporting source")
}

func validateRelationResult(workspaceID foundation.ID, requested domain.Relation, requestedEvidence []domain.RelationEvidence, result domain.RelationResult, replayed bool) error {
	if result.Relation.WorkspaceID != workspaceID || domain.ValidateRelationAggregate(result.Relation, result.Evidence) != nil || !sameRelationPayload(requested, result.Relation) || result.Relation.Version < 1 {
		return consistencyError("repository returned an invalid relation result")
	}
	if !replayed && (result.Relation.Status != domain.RelationStatusSuggested || result.Relation.Version != 1) {
		return consistencyError("repository returned an invalid new relation lifecycle")
	}
	if !replayed && result.Relation.EvidenceFingerprint != requested.EvidenceFingerprint {
		return consistencyError("new relation evidence differs from request")
	}
	if replayed && !containsRelationEvidenceSemantics(result.Evidence, requestedEvidence) {
		return consistencyError("replayed relation no longer contains requested evidence")
	}
	if !replayed && result.Relation.ID != requested.ID {
		return consistencyError("repository changed newly suggested relation identity")
	}
	return nil
}

func containsRelationEvidenceSemantics(current, requested []domain.RelationEvidence) bool {
	for _, want := range requested {
		matched := false
		for _, item := range current {
			if item.Provenance == want.Provenance && item.Reason == want.Reason && item.Applicability.Hash == want.Applicability.Hash && optional(item.ModelRunRef) == optional(want.ModelRunRef) {
				matched = true
				break
			}
		}
		if !matched {
			return false
		}
	}
	return true
}
func validateChangedRelationResult(command ConfirmRelationCommand, requested domain.RelationEvidence, confirmation domain.Confirmation, result domain.RelationResult) error {
	if result.Relation.ID != command.RelationID || result.Relation.WorkspaceID != command.WorkspaceID || result.Relation.Version < command.ExpectedVersion+1 || domain.ValidateRelationAggregate(result.Relation, result.Evidence) != nil || (!result.Replayed && (result.Relation.Version != command.ExpectedVersion+1 || result.Relation.Status != domain.RelationStatusConfirmed || result.Relation.Confirmation == nil || *result.Relation.Confirmation != confirmation)) {
		return consistencyError("repository returned an invalid confirmed relation aggregate")
	}
	matched := false
	for _, item := range result.Evidence {
		matched = matched || (item.Confirmation != nil && *item.Confirmation == confirmation && item.Provenance == requested.Provenance && item.Reason == requested.Reason && item.Applicability.Hash == requested.Applicability.Hash && optional(item.ModelRunRef) == optional(requested.ModelRunRef))
	}
	if !matched {
		return consistencyError("confirmed relation replay lost its confirmation evidence")
	}
	return nil
}
func validateTransitionedRelationResult(command TransitionRelationCommand, requestedEvidence *domain.RelationEvidence, result domain.RelationResult) error {
	if result.Relation.ID != command.RelationID || result.Relation.WorkspaceID != command.WorkspaceID || result.Relation.Version < command.ExpectedVersion+1 || domain.ValidateRelationAggregate(result.Relation, result.Evidence) != nil || (!result.Replayed && (result.Relation.Version != command.ExpectedVersion+1 || result.Relation.Status != command.Status)) {
		return consistencyError("repository returned an invalid transitioned relation aggregate")
	}
	if requestedEvidence != nil && !containsRelationEvidenceSemantics(result.Evidence, []domain.RelationEvidence{*requestedEvidence}) {
		return consistencyError("resuggested relation lost its new evidence")
	}
	return nil
}
func sameRelationPayload(left, right domain.Relation) bool {
	return left.WorkspaceID == right.WorkspaceID && left.Source == right.Source && left.Target == right.Target && left.Type == right.Type && left.Fingerprint == right.Fingerprint && sameFloat(left.ConfidenceScore, right.ConfidenceScore) && sameTime(left.ValidFrom, right.ValidFrom) && sameTime(left.ValidTo, right.ValidTo)
}

func validateConflictResult(workspaceID foundation.ID, requested domain.Conflict, requestedMembers []domain.ConflictMember, result domain.ConflictResult, replayed bool) error {
	if result.Conflict.WorkspaceID != workspaceID || domain.ValidateConflictAggregate(result.Conflict, result.Members) != nil || !sameConflictPayload(requested, result.Conflict) || result.Conflict.Version < 1 {
		return consistencyError("repository returned an invalid conflict result")
	}
	if !replayed && (result.Conflict.Status != domain.ConflictStatusOpen || result.Conflict.Version != 1) {
		return consistencyError("repository returned an invalid new conflict lifecycle")
	}
	if !containsConflictMemberSemantics(result.Members, requestedMembers) {
		return consistencyError("conflict members differ from command request")
	}
	if !replayed && result.Conflict.ID != requested.ID {
		return consistencyError("repository changed newly opened conflict identity")
	}
	return nil
}

func containsConflictMemberSemantics(current, requested []domain.ConflictMember) bool {
	for _, want := range requested {
		matched := false
		for _, item := range current {
			if item.ClaimID == want.ClaimID && item.ApplicabilityHash == want.ApplicabilityHash && item.PositionSummary == want.PositionSummary {
				matched = true
				break
			}
		}
		if !matched {
			return false
		}
	}
	return true
}
func validateTransitionedConflictResult(command TransitionConflictCommand, result domain.ConflictResult) error {
	if result.Conflict.ID != command.ConflictID || result.Conflict.WorkspaceID != command.WorkspaceID || result.Conflict.Version < command.ExpectedVersion+1 || domain.ValidateConflictAggregate(result.Conflict, result.Members) != nil || (!result.Replayed && (result.Conflict.Version != command.ExpectedVersion+1 || result.Conflict.Status != command.Status)) {
		return consistencyError("repository returned an invalid transitioned conflict aggregate")
	}
	return nil
}
func sameConflictPayload(left, right domain.Conflict) bool {
	return left.WorkspaceID == right.WorkspaceID && optionalID(left.TopicID) == optionalID(right.TopicID) && left.Severity == right.Severity && left.Summary == right.Summary && left.ApplicabilityAssessment == right.ApplicabilityAssessment && left.ReviewedOverlapReason == right.ReviewedOverlapReason && left.Fingerprint == right.Fingerprint
}

func sameFloat(left, right *float64) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return *left == *right
}
func sameTime(left, right *time.Time) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return left.Equal(*right)
}
