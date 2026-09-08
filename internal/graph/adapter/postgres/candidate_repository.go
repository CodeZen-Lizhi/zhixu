package postgres

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"sort"
	"strings"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	graphapp "github.com/CodeZen-Lizhi/zhixu/internal/graph/application"
	graphdomain "github.com/CodeZen-Lizhi/zhixu/internal/graph/domain"
	knowledge "github.com/CodeZen-Lizhi/zhixu/internal/knowledge/domain"
	"github.com/lib/pq"
	"gorm.io/gorm"
)

const (
	semanticLinkCandidateFingerprintSchemaVersion = "semantic-link-candidate/v1"
	semanticLinkCandidateWindowReason             = "RESULT_WINDOW_LIMIT"
)

// SemanticLinkCandidateUpsertResult 描述一次候选评估快照的持久化结果。
type SemanticLinkCandidateUpsertResult struct {
	Candidate  graphdomain.SemanticLinkCandidate
	Created    bool
	Suppressed bool
	Reopened   bool
}

type candidateBase struct {
	candidate      graphdomain.SemanticLinkCandidate
	evidenceHashes []string
}

type candidateDecisionRow struct {
	id               foundation.ID
	workspaceID      foundation.ID
	candidateID      foundation.ID
	candidateVersion int64
	proposalID       *foundation.ID
	idempotencyKey   string
	requestHash      string
	action           graphdomain.SemanticLinkCandidateDecisionAction
	relationType     *knowledge.RelationType
	reason           *string
	deferUntil       *time.Time
	createdAt        time.Time
}

func loadCandidate(ctx context.Context, db *gorm.DB, workspaceID foundation.ID, value any, key string, forUpdate bool) (candidateBase, error) {
	if key != "id" && key != "fingerprint" {
		return candidateBase{}, candidateInvalid("candidate lookup key is invalid")
	}
	query := candidateSelectSQL + " WHERE c.workspace_id=(@p1) AND c." + key + "=(@p2)"
	if forUpdate {
		query += " FOR UPDATE"
	}
	var base candidateBase
	if err := scanCandidateBase(gormQueryRow(ctx, db, query, sql.Named("p1", string(workspaceID)), sql.Named("p2", value)), &base); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return candidateBase{}, notFound(graphdomain.ErrorCodeSemanticLinkCandidateNotFound, err)
		}
		return candidateBase{}, err
	}
	if err := hydrateCandidateEvidence(ctx, db, &base); err != nil {
		return candidateBase{}, err
	}
	if err := graphdomain.ValidateSemanticLinkCandidate(base.candidate); err != nil {
		return candidateBase{}, err
	}
	return base, nil
}

func queryCandidateWindow(ctx context.Context, db *gorm.DB, request graphdomain.SemanticLinkCandidateQuery, windowLimit int) ([]candidateBase, bool, error) {
	if windowLimit < request.Limit || windowLimit > graphapp.MaxSemanticLinkCandidateWindow {
		return nil, false, candidateInvalid("candidate result window limit is invalid")
	}
	query := strings.Builder{}
	query.WriteString(candidateSelectSQL)
	query.WriteString(" WHERE c.workspace_id=(@workspace_id)")
	bindings := map[string]any{"workspace_id": string(request.WorkspaceID), "limit": windowLimit + 1}
	if request.NodeRef != nil {
		bindings["node_id"] = string(request.NodeRef.ID)
		if request.NodeRef.Type == knowledge.NodeTypeTopic {
			// Topic scans include confirmed member Claim pairs in the Topic scope.
			query.WriteString(` AND (
                (c.source_node_type='TOPIC' AND c.source_node_id=(@node_id)) OR
                (c.target_node_type='TOPIC' AND c.target_node_id=(@node_id)) OR
                (c.source_node_type='CLAIM' AND c.target_node_type='CLAIM' AND
                 EXISTS (SELECT 1 FROM core.relation source_membership
                    WHERE source_membership.workspace_id=c.workspace_id
                      AND source_membership.source_node_type='CLAIM'
                      AND source_membership.source_node_id=c.source_node_id
                      AND source_membership.target_node_type='TOPIC'
                      AND source_membership.target_node_id=(@node_id)
                      AND source_membership.relation_type='BELONGS_TO'
                      AND source_membership.status='CONFIRMED')
                 AND EXISTS (SELECT 1 FROM core.relation target_membership
                    WHERE target_membership.workspace_id=c.workspace_id
                      AND target_membership.source_node_type='CLAIM'
                      AND target_membership.source_node_id=c.target_node_id
                      AND target_membership.target_node_type='TOPIC'
                      AND target_membership.target_node_id=(@node_id)
                      AND target_membership.relation_type='BELONGS_TO'
                      AND target_membership.status='CONFIRMED'))
            )`)
		} else {
			query.WriteString(" AND ((c.source_node_type=(@node_type) AND c.source_node_id=(@node_id)) OR (c.target_node_type=(@node_type) AND c.target_node_id=(@node_id)))")
			bindings["node_type"] = string(request.NodeRef.Type)
		}
	}
	if len(request.Statuses) > 0 {
		query.WriteString(" AND c.status=ANY((@statuses)::text[])")
		bindings["statuses"] = pq.Array(stringsOf(request.Statuses))
	}
	if len(request.RelationTypes) > 0 {
		query.WriteString(" AND c.relation_type=ANY((@relation_types)::text[])")
		bindings["relation_types"] = pq.Array(stringsOf(request.RelationTypes))
	}
	if len(request.ReopenedReasons) > 0 {
		query.WriteString(" AND c.reopened_reason=ANY((@reopened_reasons)::text[])")
		bindings["reopened_reasons"] = pq.Array(stringsOf(request.ReopenedReasons))
	}
	if request.MinConfidence != nil {
		query.WriteString(" AND c.confidence_score >= (@min_confidence)")
		bindings["min_confidence"] = *request.MinConfidence
	}
	query.WriteString(" ORDER BY CASE c.status WHEN 'ACTIVE' THEN 0 WHEN 'DEFERRED' THEN 1 WHEN 'IGNORED' THEN 2 WHEN 'FALSE_POSITIVE' THEN 3 WHEN 'PROPOSAL_CREATED' THEN 4 WHEN 'SUPERSEDED' THEN 5 ELSE 6 END,c.updated_at DESC,c.id")
	query.WriteString(" LIMIT (@limit)")
	rows, err := gormRawRows(ctx, db, query.String(), bindings)
	if err != nil {
		return nil, false, classify(err)
	}
	defer rows.Close()
	bases := make([]candidateBase, 0, windowLimit+1)
	for rows.Next() {
		var base candidateBase
		if err := scanCandidateBase(rows, &base); err != nil {
			return nil, false, err
		}
		bases = append(bases, base)
	}
	if err := rows.Err(); err != nil {
		return nil, false, classify(err)
	}
	truncated := len(bases) > windowLimit
	if truncated {
		bases = bases[:windowLimit]
	}
	return bases, truncated, nil
}

func scanCandidateBase(row rowScanner, target *candidateBase) error {
	var id, workspaceID, sourceType, sourceID, targetType, targetID, relationType, status, fingerprintSchema, fingerprint string
	var reopenedReason, reopenedFrom, proposalID *string
	var confidence *float64
	var discoveryRaw, evidenceRaw, generationRaw []byte
	var deferredUntil *time.Time
	var sourceVersion, targetVersion, version int64
	var createdAt, updatedAt time.Time
	if err := row.Scan(&id, &workspaceID, &sourceType, &sourceID, &sourceVersion, &targetType, &targetID, &targetVersion,
		&relationType, &status, &reopenedReason, &reopenedFrom, &proposalID, &confidence, &target.candidate.Reason,
		&target.candidate.Source.Summary, &target.candidate.Source.Excerpt, &target.candidate.Target.Summary, &target.candidate.Target.Excerpt,
		&discoveryRaw, &evidenceRaw, &generationRaw,
		&deferredUntil, &fingerprintSchema, &fingerprint, &version, &createdAt, &updatedAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return err
		}
		return classify(err)
	}
	var methods []graphdomain.SemanticLinkDiscoveryMethod
	if err := json.Unmarshal(discoveryRaw, &methods); err != nil {
		return inconsistent(err)
	}
	var generation graphdomain.SemanticLinkCandidateGeneration
	if err := json.Unmarshal(generationRaw, &generation); err != nil {
		return inconsistent(err)
	}
	var evidenceHashes []string
	if err := json.Unmarshal(evidenceRaw, &evidenceHashes); err != nil {
		return inconsistent(err)
	}
	target.candidate.ID = foundation.ID(id)
	target.candidate.WorkspaceID = foundation.ID(workspaceID)
	target.candidate.Source.Ref = knowledge.NodeRef{Type: knowledge.NodeType(sourceType), ID: foundation.ID(sourceID)}
	target.candidate.Source.Version = sourceVersion
	target.candidate.Target.Ref = knowledge.NodeRef{Type: knowledge.NodeType(targetType), ID: foundation.ID(targetID)}
	target.candidate.Target.Version = targetVersion
	target.candidate.SuggestedRelationType = knowledge.RelationType(relationType)
	target.candidate.Status = graphdomain.SemanticLinkCandidateStatus(status)
	target.candidate.Confidence = 0
	if confidence != nil {
		target.candidate.Confidence = *confidence
	}
	target.candidate.DiscoveryMethods = methods
	target.candidate.Generation = generation
	target.candidate.Fingerprint = fingerprint
	target.candidate.Version = version
	target.candidate.CreatedAt = createdAt
	target.candidate.UpdatedAt = updatedAt
	target.evidenceHashes = evidenceHashes
	if reopenedReason != nil {
		target.candidate.ReopenedReason = graphdomain.SemanticLinkCandidateReopenedReason(*reopenedReason)
	}
	if reopenedFrom != nil {
		value := foundation.ID(*reopenedFrom)
		target.candidate.ReopenedFromCandidateID = &value
	}
	if proposalID != nil {
		value := foundation.ID(*proposalID)
		target.candidate.ProposalID = &value
	}
	if deferredUntil != nil {
		target.candidate.ResumeAfter = deferredUntil
	}
	if fingerprintSchema != semanticLinkCandidateFingerprintSchemaVersion {
		return inconsistent(errors.New("candidate fingerprint schema version is unsupported"))
	}
	return nil
}

func hydrateCandidateEvidence(ctx context.Context, db *gorm.DB, base *candidateBase) error {
	result, err := hydrateEvidenceForIDs(ctx, db, base.candidate.WorkspaceID, []foundation.ID{base.candidate.ID})
	if err != nil {
		return err
	}
	base.candidate.Evidence = result[base.candidate.ID]
	return validateStoredEvidenceHashes(base.evidenceHashes, base.candidate.Evidence)
}

func hydrateCandidateBatchCandidates(ctx context.Context, db *gorm.DB, bases []candidateBase) ([]graphdomain.SemanticLinkCandidate, error) {
	if err := hydrateCandidateBatchEvidence(ctx, db, bases); err != nil {
		return nil, err
	}
	items := make([]graphdomain.SemanticLinkCandidate, len(bases))
	for index := range bases {
		items[index] = bases[index].candidate
	}
	return items, nil
}

func hydrateCandidateBatchEvidence(ctx context.Context, db *gorm.DB, bases []candidateBase) error {
	if len(bases) == 0 {
		return nil
	}
	ids := make([]foundation.ID, len(bases))
	for index := range bases {
		ids[index] = bases[index].candidate.ID
	}
	evidence, err := hydrateEvidenceForIDs(ctx, db, bases[0].candidate.WorkspaceID, ids)
	if err != nil {
		return err
	}
	for index := range bases {
		bases[index].candidate.Evidence = evidence[bases[index].candidate.ID]
		if err := validateStoredEvidenceHashes(bases[index].evidenceHashes, bases[index].candidate.Evidence); err != nil {
			return err
		}
		if err := graphdomain.ValidateSemanticLinkCandidate(bases[index].candidate); err != nil {
			return err
		}
	}
	return nil
}

func hydrateEvidenceForIDs(ctx context.Context, db *gorm.DB, workspaceID foundation.ID, candidateIDs []foundation.ID) (map[foundation.ID][]graphdomain.SemanticLinkCandidateEvidence, error) {
	result := make(map[foundation.ID][]graphdomain.SemanticLinkCandidateEvidence, len(candidateIDs))
	if len(candidateIDs) == 0 {
		return result, nil
	}
	rows, err := gormRawRows(ctx, db, `
		SELECT id::text,candidate_id::text,workspace_id::text,source_version_id::text,source_span_id::text,
		       evidence_no,semantic_hash,summary,excerpt,created_at
		FROM graph.semantic_link_candidate_evidence
		WHERE workspace_id=(@p1) AND candidate_id=ANY((@p2)::uuid[])
		ORDER BY candidate_id,semantic_hash,id`, sql.Named("p1", string(workspaceID)), sql.Named("p2", pq.Array(ids(candidateIDs))))
	if err != nil {
		return nil, classify(err)
	}
	defer rows.Close()
	for rows.Next() {
		var id, candidateID, evidenceWorkspace, sourceVersion, sourceSpan, hash, reason, excerpt string
		var evidenceNo int
		var createdAt time.Time
		if err := rows.Scan(&id, &candidateID, &evidenceWorkspace, &sourceVersion, &sourceSpan, &evidenceNo, &hash, &reason, &excerpt, &createdAt); err != nil {
			return nil, inconsistent(err)
		}
		if evidenceWorkspace != string(workspaceID) {
			return nil, inconsistent(errors.New("candidate evidence workspace binding is inconsistent"))
		}
		result[foundation.ID(candidateID)] = append(result[foundation.ID(candidateID)], graphdomain.SemanticLinkCandidateEvidence{
			ID:           foundation.ID(id),
			Provenance:   knowledge.ProvenanceRef{WorkspaceID: workspaceID, SourceVersionID: foundation.ID(sourceVersion), SourceSpanID: foundation.ID(sourceSpan)},
			SemanticHash: hash, Reason: reason, Excerpt: excerpt,
		})
	}
	if err := rows.Err(); err != nil {
		return nil, classify(err)
	}
	return result, nil
}

func validateStoredEvidenceHashes(stored []string, evidence []graphdomain.SemanticLinkCandidateEvidence) error {
	actual := make([]string, len(evidence))
	for index := range evidence {
		actual[index] = evidence[index].SemanticHash
	}
	sort.Strings(actual)
	want := append([]string(nil), stored...)
	sort.Strings(want)
	if len(actual) != len(want) {
		return inconsistent(errors.New("candidate evidence count is inconsistent"))
	}
	for index := range actual {
		if actual[index] != want[index] {
			return inconsistent(errors.New("candidate evidence semantic hashes are inconsistent"))
		}
	}
	return nil
}

func findReopenSource(ctx context.Context, db *gorm.DB, candidate graphdomain.SemanticLinkCandidate) (*foundation.ID, graphdomain.SemanticLinkCandidateReopenedReason, error) {
	row := gormQueryRow(ctx, db, `
		SELECT id::text
		FROM graph.semantic_link_candidate
		WHERE workspace_id=(@p1) AND status IN ('IGNORED','FALSE_POSITIVE')
		  AND source_node_type=(@p2) AND source_node_id=(@p3) AND target_node_type=(@p4) AND target_node_id=(@p5)
		ORDER BY updated_at DESC,id DESC LIMIT 1 FOR UPDATE`,
		sql.Named("p1", string(candidate.WorkspaceID)), sql.Named("p2", string(candidate.Source.Ref.Type)), sql.Named("p3", string(candidate.Source.Ref.ID)), sql.Named("p4", string(candidate.Target.Ref.Type)), sql.Named("p5", string(candidate.Target.Ref.ID)))
	var id string
	if err := row.Scan(&id); errors.Is(err, sql.ErrNoRows) {
		return nil, "", nil
	} else if err != nil {
		return nil, "", classify(err)
	}
	parsed := foundation.ID(id)
	reason := graphdomain.SemanticLinkCandidateReopenedReasonContentChanged
	return &parsed, reason, nil
}

func validateCandidateEndpoints(ctx context.Context, db *gorm.DB, candidate graphdomain.SemanticLinkCandidate) error {
	for _, endpoint := range []graphdomain.SemanticLinkCandidateEndpoint{candidate.Source, candidate.Target} {
		var currentVersion int64
		var status string
		var err error
		switch endpoint.Ref.Type {
		case knowledge.NodeTypeTopic:
			err = gormQueryRow(ctx, db, `SELECT version,status FROM core.topic WHERE workspace_id=(@p1) AND id=(@p2)`, sql.Named("p1", string(candidate.WorkspaceID)), sql.Named("p2", string(endpoint.Ref.ID))).Scan(&currentVersion, &status)
			if err == nil && status != string(knowledge.TopicStatusActive) {
				err = errors.New("candidate topic endpoint is not active")
			}
		case knowledge.NodeTypeClaim:
			err = gormQueryRow(ctx, db, `SELECT version,status FROM core.claim WHERE workspace_id=(@p1) AND id=(@p2)`, sql.Named("p1", string(candidate.WorkspaceID)), sql.Named("p2", string(endpoint.Ref.ID))).Scan(&currentVersion, &status)
			if err == nil && status != string(knowledge.ClaimStatusSuggested) && status != string(knowledge.ClaimStatusConfirmed) && status != string(knowledge.ClaimStatusDisputed) {
				err = errors.New("candidate claim endpoint is not eligible")
			}
		default:
			return candidateInvalid("candidate endpoint type is unsupported")
		}
		if errors.Is(err, sql.ErrNoRows) {
			return foundation.NewError(foundation.ErrorNotFound, "GRAPH_CANDIDATE_ENDPOINT_NOT_FOUND", false, err)
		}
		if err != nil {
			return candidateInvalid(err.Error())
		}
		// Candidate stores a snapshot version. It may be older than the current
		// node while awaiting review, but it cannot claim a future version.
		if endpoint.Version > currentVersion {
			return candidateVersionConflict("candidate endpoint version is ahead of the canonical node")
		}
	}
	return nil
}

func supersedePriorCandidates(ctx context.Context, db *gorm.DB, candidate graphdomain.SemanticLinkCandidate) error {
	_, err := gormGraphExec(ctx, db, `
		UPDATE graph.semantic_link_candidate
		SET status='SUPERSEDED',deferred_until=NULL,version=version+1,updated_at=(@p7)
		WHERE workspace_id=(@p1) AND fingerprint<>(@p6)
		  AND status<>'SUPERSEDED'
		  AND source_node_type=(@p2) AND source_node_id=(@p3) AND target_node_type=(@p4) AND target_node_id=(@p5)`,
		sql.Named("p1", string(candidate.WorkspaceID)), sql.Named("p2", string(candidate.Source.Ref.Type)), sql.Named("p3", string(candidate.Source.Ref.ID)), sql.Named("p4", string(candidate.Target.Ref.Type)), sql.Named("p5", string(candidate.Target.Ref.ID)), sql.Named("p6", candidate.Fingerprint), sql.Named("p7", candidate.UpdatedAt.UTC()))
	return classify(err)
}

func loadDecisionByIdempotency(ctx context.Context, db *gorm.DB, workspaceID foundation.ID, key string) (candidateDecisionRow, bool, error) {
	var row candidateDecisionRow
	var id, candidateID, storedWorkspace, action string
	var proposalID, relationType, reason *string
	err := gormQueryRow(ctx, db, `
		SELECT id::text,candidate_id::text,workspace_id::text,candidate_version,proposal_id::text,idempotency_key,request_hash,
		       action,relation_type,reason,defer_until,created_at
		FROM graph.semantic_link_candidate_decision
		WHERE workspace_id=(@p1) AND idempotency_key=(@p2)`, sql.Named("p1", string(workspaceID)), sql.Named("p2", key)).Scan(
		&id, &candidateID, &storedWorkspace, &row.candidateVersion, &proposalID, &row.idempotencyKey, &row.requestHash,
		&action, &relationType, &reason, &row.deferUntil, &row.createdAt)
	if errors.Is(err, sql.ErrNoRows) {
		return candidateDecisionRow{}, false, nil
	}
	if err != nil {
		return candidateDecisionRow{}, false, classify(err)
	}
	row.id, row.candidateID, row.workspaceID = foundation.ID(id), foundation.ID(candidateID), foundation.ID(storedWorkspace)
	row.action = graphdomain.SemanticLinkCandidateDecisionAction(action)
	if proposalID != nil {
		value := foundation.ID(*proposalID)
		row.proposalID = &value
	}
	if relationType != nil {
		value := knowledge.RelationType(*relationType)
		row.relationType = &value
	}
	if reason != nil {
		value := *reason
		row.reason = &value
	}
	return row, true, nil
}

func insertDecision(ctx context.Context, db *gorm.DB, decision candidateDecisionRow) (bool, error) {
	var insertedID string
	err := gormQueryRow(ctx, db, `
		INSERT INTO graph.semantic_link_candidate_decision(
			id,workspace_id,candidate_id,candidate_version,proposal_id,idempotency_key,request_hash,action,relation_type,reason,defer_until,created_at
		) VALUES((@p1),(@p2),(@p3),(@p4),(@p5),(@p6),(@p7),(@p8),(@p9),(@p10),(@p11),(@p12))
		ON CONFLICT (workspace_id,idempotency_key) DO NOTHING
		RETURNING id::text`, sql.Named("p1", decisionID(decision)), sql.Named("p2", string(decision.workspaceID)), sql.Named("p3", string(decision.candidateID)), sql.Named("p4", decision.candidateVersion),
		sql.Named("p5", optionalID(decision.proposalID)), sql.Named("p6", decision.idempotencyKey), sql.Named("p7", decision.requestHash), sql.Named("p8", decisionActionWire(decision.action)), sql.Named("p9", optionalRelationType(decision.relationType)), sql.Named("p10", optionalReason(decision.reason)), sql.Named("p11", decision.deferUntil), sql.Named("p12", decision.createdAt.UTC())).Scan(&insertedID)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, classify(err)
	}
	return insertedID != "", nil
}

func candidateAtDecision(base graphdomain.SemanticLinkCandidate, decision candidateDecisionRow) (graphdomain.SemanticLinkCandidate, error) {
	base.Version = decision.candidateVersion + 1
	base.UpdatedAt = decision.createdAt.UTC()
	base.ProposalID = nil
	base.ResumeAfter = nil
	switch decision.action {
	case graphdomain.SemanticLinkCandidateDecisionConfirm, graphdomain.SemanticLinkCandidateDecisionConfirmWithRelationType:
		base.Status = graphdomain.SemanticLinkCandidateStatusProposalCreated
		base.ProposalID = decision.proposalID
	case graphdomain.SemanticLinkCandidateDecisionIgnore:
		base.Status = graphdomain.SemanticLinkCandidateStatusIgnored
	case graphdomain.SemanticLinkCandidateDecisionFalsePositive:
		base.Status = graphdomain.SemanticLinkCandidateStatusFalsePositive
	case graphdomain.SemanticLinkCandidateDecisionDefer:
		base.Status = graphdomain.SemanticLinkCandidateStatusDeferred
		base.ResumeAfter = decision.deferUntil
	case graphdomain.SemanticLinkCandidateDecisionResume:
		base.Status = graphdomain.SemanticLinkCandidateStatusActive
	default:
		return graphdomain.SemanticLinkCandidate{}, candidateInvalid("candidate decision action is unsupported")
	}
	if err := graphdomain.ValidateSemanticLinkCandidate(base); err != nil {
		return graphdomain.SemanticLinkCandidate{}, err
	}
	return base, nil
}

func replayCandidateDecision(
	command graphapp.SemanticLinkCandidateDecisionCommand,
	requestHash string,
	proposalID *foundation.ID,
	receipt candidateDecisionRow,
	current graphdomain.SemanticLinkCandidate,
) (graphapp.SemanticLinkCandidateDecisionResult, error) {
	if receipt.requestHash != requestHash || receipt.candidateID != command.CandidateID || !sameOptionalID(receipt.proposalID, proposalID) {
		return graphapp.SemanticLinkCandidateDecisionResult{}, candidateVersionConflict("candidate decision idempotency binding differs")
	}
	resultCandidate, err := candidateAtDecision(current, receipt)
	if err != nil {
		return graphapp.SemanticLinkCandidateDecisionResult{}, err
	}
	result := graphapp.SemanticLinkCandidateDecisionResult{
		Candidate: resultCandidate, ProposalID: resultCandidate.ProposalID,
		DecisionID: receipt.id, DecidedAt: receipt.createdAt, Replayed: true,
	}
	if err := graphapp.ValidateSemanticLinkCandidateDecisionResult(command, result); err != nil {
		return graphapp.SemanticLinkCandidateDecisionResult{}, err
	}
	return result, nil
}

func sameOptionalID(left, right *foundation.ID) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return *left == *right
}

func validateDecisionProposal(command graphapp.SemanticLinkCandidateDecisionCommand, proposalID *foundation.ID) error {
	confirm := command.Decision.Action == graphdomain.SemanticLinkCandidateDecisionConfirm || command.Decision.Action == graphdomain.SemanticLinkCandidateDecisionConfirmWithRelationType
	if confirm != (proposalID != nil) {
		return candidateInvalid("proposal binding is required only for confirm decisions")
	}
	return nil
}

func validateKnowledgeProposalBinding(ctx context.Context, db *gorm.DB, workspaceID, proposalID foundation.ID) error {
	if !validID(proposalID) {
		return candidateInvalid("relation proposal id is invalid")
	}
	var storedWorkspace, proposalType string
	if err := gormQueryRow(ctx, db, `SELECT workspace_id::text,proposal_type FROM change_control.proposal WHERE id=(@p1) FOR SHARE`, sql.Named("p1", string(proposalID))).Scan(&storedWorkspace, &proposalType); errors.Is(err, sql.ErrNoRows) {
		return foundation.NewError(foundation.ErrorNotFound, "RELATION_PROPOSAL_NOT_FOUND", false, err)
	} else if err != nil {
		return classify(err)
	} else if storedWorkspace != string(workspaceID) || proposalType != "knowledge_change" {
		return foundation.NewError(foundation.ErrorConsistencyViolation, "RELATION_PROPOSAL_BINDING_INVALID", false, errors.New("proposal workspace or type does not match candidate"))
	}
	return nil
}

func validateCandidateWindow(request graphdomain.SemanticLinkCandidateQuery, window graphapp.SemanticLinkCandidateResultWindow) error {
	if len(window.Items) > graphapp.MaxSemanticLinkCandidateWindow ||
		(window.Truncated && window.Reason == "") || (!window.Truncated && window.Reason != "") {
		return inconsistent(errors.New("candidate result window state is inconsistent"))
	}
	return graphdomain.ValidateSemanticLinkCandidateWindow(request, window.Items, graphapp.MaxSemanticLinkCandidateWindow)
}

func candidateDecisionReason(value string) *string {
	if value == "" {
		return nil
	}
	return &value
}

func candidateDatabaseNow(ctx context.Context, db *gorm.DB) (time.Time, error) {
	var now time.Time
	if err := gormQueryRow(ctx, db, `SELECT CURRENT_TIMESTAMP`).Scan(&now); err != nil {
		return time.Time{}, classify(err)
	}
	return now.UTC(), nil
}

func validID(value foundation.ID) bool {
	parsed, err := foundation.ParseID(string(value))
	return err == nil && parsed == value
}

func decisionID(decision candidateDecisionRow) string {
	return string(decision.id)
}

func decisionActionWire(action graphdomain.SemanticLinkCandidateDecisionAction) string {
	return string(action)
}

func optionalID(value *foundation.ID) any {
	if value == nil {
		return nil
	}
	return string(*value)
}

func optionalRelationType(value *knowledge.RelationType) any {
	if value == nil {
		return nil
	}
	return string(*value)
}

func optionalReason(value *string) any {
	if value == nil || *value == "" {
		return nil
	}
	return *value
}

func candidatePageFingerprint(items []graphdomain.SemanticLinkCandidate) string {
	encoded, _ := json.Marshal(items)
	sum := sha256.Sum256(encoded)
	return hex.EncodeToString(sum[:])
}

func isSuppressed(candidate graphdomain.SemanticLinkCandidate) bool {
	return candidate.Status == graphdomain.SemanticLinkCandidateStatusIgnored || candidate.Status == graphdomain.SemanticLinkCandidateStatusFalsePositive
}

func candidateInvalid(message string) error {
	return foundation.NewError(foundation.ErrorInvalidInput, graphdomain.ErrorCodeSemanticLinkCandidateRequestInvalid, false, errors.New(message))
}

func candidateVersionConflict(message string) error {
	return foundation.NewError(foundation.ErrorVersionConflict, graphdomain.ErrorCodeSemanticLinkCandidateTransitionInvalid, false, errors.New(message))
}

const candidateSelectSQL = `
	SELECT c.id::text,c.workspace_id::text,c.source_node_type,c.source_node_id::text,c.source_node_version,
	       c.target_node_type,c.target_node_id::text,c.target_node_version,c.relation_type,c.status,c.reopened_reason,
	       c.reopened_from_candidate_id::text,c.current_proposal_id::text,c.confidence_score,c.reason,
	       c.source_summary,c.source_excerpt,c.target_summary,c.target_excerpt,
	       c.discovery_methods,c.evidence_semantic_hashes,c.generation,c.deferred_until,c.fingerprint_schema_version,c.fingerprint,
	       c.version,c.created_at,c.updated_at
	FROM graph.semantic_link_candidate c`

var _ graphapp.SemanticLinkCandidateDecisionResult
