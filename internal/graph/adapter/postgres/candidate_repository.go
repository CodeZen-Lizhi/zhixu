package postgres

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	graphapp "github.com/CodeZen-Lizhi/zhixu/internal/graph/application"
	graphdomain "github.com/CodeZen-Lizhi/zhixu/internal/graph/domain"
	knowledge "github.com/CodeZen-Lizhi/zhixu/internal/knowledge/domain"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
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

// UpsertSemanticLinkCandidate 持久化候选评估快照；相同 fingerprint 精确重放。
func (repository *Repository) UpsertSemanticLinkCandidate(ctx context.Context, candidate graphdomain.SemanticLinkCandidate) (SemanticLinkCandidateUpsertResult, error) {
	if repository == nil || repository.db == nil {
		return SemanticLinkCandidateUpsertResult{}, unavailable(errors.New("graph candidate repository is unavailable"))
	}
	if err := graphdomain.ValidateSemanticLinkCandidate(candidate); err != nil {
		return SemanticLinkCandidateUpsertResult{}, err
	}
	if candidate.Status != graphdomain.SemanticLinkCandidateStatusActive && candidate.Status != graphdomain.SemanticLinkCandidateStatusDeferred {
		return SemanticLinkCandidateUpsertResult{}, candidateInvalid("candidate evaluation must start active or deferred")
	}
	return candidateWriteTx(ctx, repository, func(tx DB) (SemanticLinkCandidateUpsertResult, error) {
		existing, err := loadCandidate(ctx, tx, candidate.WorkspaceID, candidate.Fingerprint, "fingerprint", true)
		if err == nil {
			suppressed := existing.candidate.Status == graphdomain.SemanticLinkCandidateStatusIgnored || existing.candidate.Status == graphdomain.SemanticLinkCandidateStatusFalsePositive
			return SemanticLinkCandidateUpsertResult{Candidate: existing.candidate, Suppressed: suppressed}, nil
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return SemanticLinkCandidateUpsertResult{}, err
		}
		if err := validateCandidateEndpoints(ctx, tx, candidate); err != nil {
			return SemanticLinkCandidateUpsertResult{}, err
		}

		candidate.ReopenedFromCandidateID, candidate.ReopenedReason, err = findReopenSource(ctx, tx, candidate)
		if err != nil {
			return SemanticLinkCandidateUpsertResult{}, err
		}
		if candidate.ReopenedFromCandidateID == nil {
			candidate.ReopenedReason = ""
		}
		if err := graphdomain.ValidateSemanticLinkCandidate(candidate); err != nil {
			return SemanticLinkCandidateUpsertResult{}, err
		}
		inserted, insertedOK, err := insertCandidate(ctx, tx, candidate)
		if err != nil {
			return SemanticLinkCandidateUpsertResult{}, err
		}
		if !insertedOK {
			// A concurrent evaluator won the fingerprint race. The unique constraint
			// is the idempotency boundary; read the committed winner and return it.
			winner, winnerErr := loadCandidate(ctx, tx, candidate.WorkspaceID, candidate.Fingerprint, "fingerprint", true)
			if winnerErr != nil {
				return SemanticLinkCandidateUpsertResult{}, winnerErr
			}
			return SemanticLinkCandidateUpsertResult{Candidate: winner.candidate, Suppressed: isSuppressed(winner.candidate)}, nil
		}
		if err := insertCandidateEvidence(ctx, tx, candidate); err != nil {
			return SemanticLinkCandidateUpsertResult{}, err
		}
		if err := supersedePriorCandidates(ctx, tx, candidate); err != nil {
			return SemanticLinkCandidateUpsertResult{}, err
		}
		inserted.candidate.Evidence = append([]graphdomain.SemanticLinkCandidateEvidence(nil), candidate.Evidence...)
		return SemanticLinkCandidateUpsertResult{Candidate: inserted.candidate, Created: true, Reopened: candidate.ReopenedFromCandidateID != nil}, nil
	})
}

// ListSemanticLinkCandidates 返回 Workspace 内稳定排序的有界候选窗口。
func (repository *Repository) ListSemanticLinkCandidates(ctx context.Context, request graphdomain.SemanticLinkCandidateQuery) (graphdomain.SemanticLinkCandidatePage, error) {
	if repository == nil || repository.db == nil {
		return graphdomain.SemanticLinkCandidatePage{}, unavailable(errors.New("graph candidate repository is unavailable"))
	}
	if err := graphdomain.ValidateSemanticLinkCandidateQuery(request); err != nil {
		return graphdomain.SemanticLinkCandidatePage{}, err
	}
	return inReadSnapshot(ctx, repository, func(snapshot *Repository) (graphdomain.SemanticLinkCandidatePage, error) {
		bases, truncated, err := queryCandidateWindow(ctx, snapshot.db, request, request.Limit)
		if err != nil {
			return graphdomain.SemanticLinkCandidatePage{}, err
		}
		items, err := hydrateCandidateBatchCandidates(ctx, snapshot.db, bases)
		if err != nil {
			return graphdomain.SemanticLinkCandidatePage{}, err
		}
		page := graphdomain.SemanticLinkCandidatePage{
			WorkspaceID: request.WorkspaceID,
			Items:       items,
			Meta:        graphdomain.PageMeta{Fingerprint: candidatePageFingerprint(items), Complete: !truncated},
		}
		if truncated {
			page.Meta.Truncated = true
			page.Meta.Reason = semanticLinkCandidateWindowReason
		}
		if err := graphdomain.ValidateSemanticLinkCandidatePage(request, page); err != nil {
			return graphdomain.SemanticLinkCandidatePage{}, err
		}
		return page, nil
	})
}

// SemanticLinkCandidateWindow 返回候选的完整有界窗口；分页 cursor 由 Application 统一处理。
func (repository *Repository) SemanticLinkCandidateWindow(ctx context.Context, request graphdomain.SemanticLinkCandidateQuery) (graphapp.SemanticLinkCandidateResultWindow, error) {
	if repository == nil || repository.db == nil {
		return graphapp.SemanticLinkCandidateResultWindow{}, unavailable(errors.New("graph candidate repository is unavailable"))
	}
	if err := graphdomain.ValidateSemanticLinkCandidateQuery(request); err != nil {
		return graphapp.SemanticLinkCandidateResultWindow{}, err
	}
	return inReadSnapshot(ctx, repository, func(snapshot *Repository) (graphapp.SemanticLinkCandidateResultWindow, error) {
		bases, truncated, err := queryCandidateWindow(ctx, snapshot.db, request, graphapp.MaxSemanticLinkCandidateWindow)
		if err != nil {
			return graphapp.SemanticLinkCandidateResultWindow{}, err
		}
		items, err := hydrateCandidateBatchCandidates(ctx, snapshot.db, bases)
		if err != nil {
			return graphapp.SemanticLinkCandidateResultWindow{}, err
		}
		window := graphapp.SemanticLinkCandidateResultWindow{Items: items, Truncated: truncated}
		if truncated {
			window.Reason = semanticLinkCandidateWindowReason
		}
		if err := validateCandidateWindow(request, window); err != nil {
			return graphapp.SemanticLinkCandidateResultWindow{}, err
		}
		return window, nil
	})
}

// GetSemanticLinkCandidate 返回单个候选及其完整有界 Evidence 快照。
func (repository *Repository) GetSemanticLinkCandidate(ctx context.Context, workspaceID, candidateID foundation.ID) (graphdomain.SemanticLinkCandidate, error) {
	if repository == nil || repository.db == nil {
		return graphdomain.SemanticLinkCandidate{}, unavailable(errors.New("graph candidate repository is unavailable"))
	}
	if !validID(workspaceID) || !validID(candidateID) {
		return graphdomain.SemanticLinkCandidate{}, candidateInvalid("candidate lookup is invalid")
	}
	return inReadSnapshot(ctx, repository, func(snapshot *Repository) (graphdomain.SemanticLinkCandidate, error) {
		base, err := loadCandidate(ctx, snapshot.db, workspaceID, candidateID, "id", false)
		if err != nil {
			return graphdomain.SemanticLinkCandidate{}, err
		}
		return base.candidate, nil
	})
}

// DecideSemanticLinkCandidate 以 expected_version/CAS 和 Idempotency-Key 记录一次候选决策。
// Proposal 创建由上层 Knowledge Proposal UoW 完成；此方法只接受已创建的 Proposal 绑定。
func (repository *Repository) DecideSemanticLinkCandidate(ctx context.Context, command graphapp.SemanticLinkCandidateDecisionCommand, proposalID *foundation.ID) (graphapp.SemanticLinkCandidateDecisionResult, error) {
	canonical, err := graphapp.CanonicalizeSemanticLinkCandidateDecisionCommand(command)
	if err != nil {
		return graphapp.SemanticLinkCandidateDecisionResult{}, err
	}
	if err := validateDecisionProposal(canonical, proposalID); err != nil {
		return graphapp.SemanticLinkCandidateDecisionResult{}, err
	}
	requestHash, err := graphapp.ComputeSemanticLinkCandidateDecisionRequestHash(canonical)
	if err != nil {
		return graphapp.SemanticLinkCandidateDecisionResult{}, err
	}
	return candidateWriteTx(ctx, repository, func(tx DB) (graphapp.SemanticLinkCandidateDecisionResult, error) {
		if receipt, found, receiptErr := loadDecisionByIdempotency(ctx, tx, canonical.WorkspaceID, canonical.IdempotencyKey); receiptErr != nil {
			return graphapp.SemanticLinkCandidateDecisionResult{}, receiptErr
		} else if found {
			base, baseErr := loadCandidate(ctx, tx, canonical.WorkspaceID, canonical.CandidateID, "id", false)
			if baseErr != nil {
				return graphapp.SemanticLinkCandidateDecisionResult{}, baseErr
			}
			return replayCandidateDecision(canonical, requestHash, proposalID, receipt, base.candidate)
		}

		base, err := loadCandidate(ctx, tx, canonical.WorkspaceID, canonical.CandidateID, "id", true)
		if err != nil {
			return graphapp.SemanticLinkCandidateDecisionResult{}, err
		}
		if base.candidate.Version != canonical.ExpectedVersion {
			// The first receipt lookup may race a concurrent winner. Re-read after
			// acquiring the candidate row lock before classifying a stale version.
			if receipt, found, receiptErr := loadDecisionByIdempotency(ctx, tx, canonical.WorkspaceID, canonical.IdempotencyKey); receiptErr != nil {
				return graphapp.SemanticLinkCandidateDecisionResult{}, receiptErr
			} else if found {
				return replayCandidateDecision(canonical, requestHash, proposalID, receipt, base.candidate)
			}
			return graphapp.SemanticLinkCandidateDecisionResult{}, candidateVersionConflict("candidate decision expected version mismatch")
		}
		if err := graphdomain.ValidateSemanticLinkCandidateDecision(base.candidate, canonical.Decision); err != nil {
			return graphapp.SemanticLinkCandidateDecisionResult{}, err
		}
		if proposalID != nil {
			if err := validateKnowledgeProposalBinding(ctx, tx, canonical.WorkspaceID, *proposalID); err != nil {
				return graphapp.SemanticLinkCandidateDecisionResult{}, err
			}
		}

		now, err := candidateDatabaseNow(ctx, tx)
		if err != nil {
			return graphapp.SemanticLinkCandidateDecisionResult{}, err
		}
		// PostgreSQL CURRENT_TIMESTAMP is transaction-start time. A candidate may
		// have been inserted later in the same transaction, so preserve the
		// append-only updated_at monotonicity required by the database trigger.
		if now.Before(base.candidate.UpdatedAt) {
			now = base.candidate.UpdatedAt
		}
		decisionID, err := foundation.NewUUIDGenerator(nil).New()
		if err != nil {
			return graphapp.SemanticLinkCandidateDecisionResult{}, err
		}
		receipt := candidateDecisionRow{
			id: decisionID, workspaceID: canonical.WorkspaceID, candidateID: canonical.CandidateID,
			candidateVersion: canonical.ExpectedVersion, proposalID: proposalID,
			idempotencyKey: canonical.IdempotencyKey, requestHash: requestHash,
			action: canonical.Decision.Action, relationType: canonical.Decision.RelationType,
			deferUntil: canonical.Decision.ResumeAfter, reason: candidateDecisionReason(canonical.Decision.Reason), createdAt: now,
		}
		inserted, err := insertDecision(ctx, tx, receipt)
		if err != nil {
			return graphapp.SemanticLinkCandidateDecisionResult{}, err
		}
		if !inserted {
			winner, found, lookupErr := loadDecisionByIdempotency(ctx, tx, canonical.WorkspaceID, canonical.IdempotencyKey)
			if lookupErr != nil {
				return graphapp.SemanticLinkCandidateDecisionResult{}, lookupErr
			}
			if !found {
				return graphapp.SemanticLinkCandidateDecisionResult{}, candidateVersionConflict("candidate decision receipt was concurrently rebound")
			}
			return replayCandidateDecision(canonical, requestHash, proposalID, winner, base.candidate)
		}

		nextStatus, err := graphdomain.NextSemanticLinkCandidateStatus(base.candidate.Status, canonical.Decision)
		if err != nil {
			return graphapp.SemanticLinkCandidateDecisionResult{}, err
		}
		var nextProposal any
		if proposalID != nil {
			nextProposal = string(*proposalID)
		}
		var deferUntil any
		if canonical.Decision.ResumeAfter != nil {
			deferUntil = canonical.Decision.ResumeAfter.UTC()
		}
		commandTag, err := tx.Exec(ctx, `
			UPDATE graph.semantic_link_candidate
			SET status=$3,current_proposal_id=$4,deferred_until=$5,version=version+1,updated_at=$6
			WHERE workspace_id=$1 AND id=$2 AND version=$7`,
			string(canonical.WorkspaceID), string(canonical.CandidateID), string(nextStatus), nextProposal, deferUntil, now, canonical.ExpectedVersion)
		if err != nil {
			return graphapp.SemanticLinkCandidateDecisionResult{}, classify(err)
		}
		if commandTag.RowsAffected() != 1 {
			return graphapp.SemanticLinkCandidateDecisionResult{}, candidateVersionConflict("candidate decision CAS was lost")
		}
		resultCandidate, err := candidateAtDecision(base.candidate, receipt)
		if err != nil {
			return graphapp.SemanticLinkCandidateDecisionResult{}, err
		}
		resultCandidate.Status = nextStatus
		resultCandidate.Version = canonical.ExpectedVersion + 1
		resultCandidate.UpdatedAt = now
		if nextStatus == graphdomain.SemanticLinkCandidateStatusProposalCreated {
			resultCandidate.ProposalID = proposalID
		} else {
			resultCandidate.ProposalID = nil
		}
		if nextStatus == graphdomain.SemanticLinkCandidateStatusDeferred {
			resultCandidate.ResumeAfter = canonical.Decision.ResumeAfter
		} else {
			resultCandidate.ResumeAfter = nil
		}
		result := graphapp.SemanticLinkCandidateDecisionResult{
			Candidate: resultCandidate, ProposalID: resultCandidate.ProposalID,
			DecisionID: receipt.id, DecidedAt: receipt.createdAt,
		}
		if err := graphapp.ValidateSemanticLinkCandidateDecisionResult(canonical, result); err != nil {
			return graphapp.SemanticLinkCandidateDecisionResult{}, err
		}
		return result, nil
	})
}

func candidateWriteTx[T any](ctx context.Context, repository *Repository, fn func(DB) (T, error)) (T, error) {
	var zero T
	if repository == nil || repository.db == nil {
		return zero, unavailable(errors.New("graph candidate repository is unavailable"))
	}
	if repository.beginner == nil {
		return fn(repository.db)
	}
	tx, err := repository.beginner.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return zero, classify(err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	result, err := fn(tx)
	if err != nil {
		return zero, err
	}
	if err := tx.Commit(ctx); err != nil {
		return zero, classify(err)
	}
	return result, nil
}

func loadCandidate(ctx context.Context, db DB, workspaceID foundation.ID, value any, key string, forUpdate bool) (candidateBase, error) {
	if key != "id" && key != "fingerprint" {
		return candidateBase{}, candidateInvalid("candidate lookup key is invalid")
	}
	query := candidateSelectSQL + " WHERE c.workspace_id=$1 AND c." + key + "=$2"
	if forUpdate {
		query += " FOR UPDATE"
	}
	var base candidateBase
	if err := scanCandidateBase(db.QueryRow(ctx, query, string(workspaceID), value), &base); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
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

func queryCandidateWindow(ctx context.Context, db DB, request graphdomain.SemanticLinkCandidateQuery, windowLimit int) ([]candidateBase, bool, error) {
	if windowLimit < request.Limit || windowLimit > graphapp.MaxSemanticLinkCandidateWindow {
		return nil, false, candidateInvalid("candidate result window limit is invalid")
	}
	query := strings.Builder{}
	query.WriteString(candidateSelectSQL)
	query.WriteString(" WHERE c.workspace_id=$1")
	args := []any{string(request.WorkspaceID)}
	next := 2
	if request.NodeRef != nil {
		query.WriteString(fmt.Sprintf(" AND ((c.source_node_type=$%d AND c.source_node_id=$%d) OR (c.target_node_type=$%d AND c.target_node_id=$%d))", next, next+1, next, next+1))
		args = append(args, string(request.NodeRef.Type), string(request.NodeRef.ID))
		next += 2
	}
	if len(request.Statuses) > 0 {
		query.WriteString(fmt.Sprintf(" AND c.status=ANY($%d::text[])", next))
		args = append(args, stringsOf(request.Statuses))
		next++
	}
	if len(request.RelationTypes) > 0 {
		query.WriteString(fmt.Sprintf(" AND c.relation_type=ANY($%d::text[])", next))
		args = append(args, stringsOf(request.RelationTypes))
		next++
	}
	if len(request.ReopenedReasons) > 0 {
		query.WriteString(fmt.Sprintf(" AND c.reopened_reason=ANY($%d::text[])", next))
		args = append(args, stringsOf(request.ReopenedReasons))
		next++
	}
	if request.MinConfidence != nil {
		query.WriteString(fmt.Sprintf(" AND c.confidence_score >= $%d", next))
		args = append(args, *request.MinConfidence)
		next++
	}
	query.WriteString(" ORDER BY CASE c.status WHEN 'ACTIVE' THEN 0 WHEN 'DEFERRED' THEN 1 WHEN 'IGNORED' THEN 2 WHEN 'FALSE_POSITIVE' THEN 3 WHEN 'PROPOSAL_CREATED' THEN 4 WHEN 'SUPERSEDED' THEN 5 ELSE 6 END,c.updated_at DESC,c.id")
	query.WriteString(fmt.Sprintf(" LIMIT $%d", next))
	args = append(args, windowLimit+1)
	rows, err := db.Query(ctx, query.String(), args...)
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
		if errors.Is(err, pgx.ErrNoRows) {
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

func hydrateCandidateEvidence(ctx context.Context, db DB, base *candidateBase) error {
	result, err := hydrateEvidenceForIDs(ctx, db, base.candidate.WorkspaceID, []foundation.ID{base.candidate.ID})
	if err != nil {
		return err
	}
	base.candidate.Evidence = result[base.candidate.ID]
	return validateStoredEvidenceHashes(base.evidenceHashes, base.candidate.Evidence)
}

func hydrateCandidateBatchCandidates(ctx context.Context, db DB, bases []candidateBase) ([]graphdomain.SemanticLinkCandidate, error) {
	if err := hydrateCandidateBatchEvidence(ctx, db, bases); err != nil {
		return nil, err
	}
	items := make([]graphdomain.SemanticLinkCandidate, len(bases))
	for index := range bases {
		items[index] = bases[index].candidate
	}
	return items, nil
}

func hydrateCandidateBatchEvidence(ctx context.Context, db DB, bases []candidateBase) error {
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

func hydrateEvidenceForIDs(ctx context.Context, db DB, workspaceID foundation.ID, candidateIDs []foundation.ID) (map[foundation.ID][]graphdomain.SemanticLinkCandidateEvidence, error) {
	result := make(map[foundation.ID][]graphdomain.SemanticLinkCandidateEvidence, len(candidateIDs))
	if len(candidateIDs) == 0 {
		return result, nil
	}
	rows, err := db.Query(ctx, `
		SELECT id::text,candidate_id::text,workspace_id::text,source_version_id::text,source_span_id::text,
		       evidence_no,semantic_hash,summary,excerpt,created_at
		FROM graph.semantic_link_candidate_evidence
		WHERE workspace_id=$1 AND candidate_id=ANY($2::uuid[])
		ORDER BY candidate_id,semantic_hash,id`, string(workspaceID), ids(candidateIDs))
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

func insertCandidate(ctx context.Context, db DB, candidate graphdomain.SemanticLinkCandidate) (candidateBase, bool, error) {
	discoveryRaw, err := json.Marshal(candidate.DiscoveryMethods)
	if err != nil {
		return candidateBase{}, false, inconsistent(err)
	}
	evidenceHashes := make([]string, len(candidate.Evidence))
	for index := range candidate.Evidence {
		evidenceHashes[index] = candidate.Evidence[index].SemanticHash
	}
	sort.Strings(evidenceHashes)
	evidenceRaw, err := json.Marshal(evidenceHashes)
	if err != nil {
		return candidateBase{}, false, inconsistent(err)
	}
	generationRaw, err := json.Marshal(candidate.Generation)
	if err != nil {
		return candidateBase{}, false, inconsistent(err)
	}
	var reopenedFrom, proposalID, reopenedReason any
	if candidate.ReopenedFromCandidateID != nil {
		reopenedFrom = string(*candidate.ReopenedFromCandidateID)
	}
	if candidate.ProposalID != nil {
		proposalID = string(*candidate.ProposalID)
	}
	if candidate.ReopenedReason != "" {
		reopenedReason = string(candidate.ReopenedReason)
	}
	var insertedID string
	err = db.QueryRow(ctx, `
		INSERT INTO graph.semantic_link_candidate(
			id,workspace_id,source_node_type,source_node_id,source_node_version,
			target_node_type,target_node_id,target_node_version,relation_type,fingerprint_schema_version,fingerprint,
				status,reopened_reason,reopened_from_candidate_id,current_proposal_id,confidence_score,reason,
				source_summary,source_excerpt,target_summary,target_excerpt,
				discovery_methods,evidence_semantic_hashes,generation,deferred_until,version,created_at,updated_at
			) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,$19,$20,$21,
			         $22::jsonb,$23::jsonb,$24::jsonb,$25,$26,$27,$28)
			ON CONFLICT (workspace_id,fingerprint) DO NOTHING
			RETURNING id::text`,
		string(candidate.ID), string(candidate.WorkspaceID), string(candidate.Source.Ref.Type), string(candidate.Source.Ref.ID), candidate.Source.Version,
		string(candidate.Target.Ref.Type), string(candidate.Target.Ref.ID), candidate.Target.Version, string(candidate.SuggestedRelationType),
		semanticLinkCandidateFingerprintSchemaVersion, candidate.Fingerprint, string(candidate.Status), reopenedReason, reopenedFrom, proposalID,
		candidate.Confidence, candidate.Reason, candidate.Source.Summary, candidate.Source.Excerpt, candidate.Target.Summary, candidate.Target.Excerpt,
		discoveryRaw, evidenceRaw, generationRaw,
		candidate.ResumeAfter, candidate.Version, candidate.CreatedAt.UTC(), candidate.UpdatedAt.UTC()).Scan(&insertedID)
	if errors.Is(err, pgx.ErrNoRows) {
		return candidateBase{}, false, nil
	}
	if err != nil {
		return candidateBase{}, false, classify(err)
	}
	return candidateBase{candidate: candidate, evidenceHashes: evidenceHashes}, true, nil
}

func insertCandidateEvidence(ctx context.Context, db DB, candidate graphdomain.SemanticLinkCandidate) error {
	if len(candidate.Evidence) == 0 {
		return inconsistent(errors.New("candidate evidence is required"))
	}
	idsValue := make([]string, len(candidate.Evidence))
	candidateIDs := make([]string, len(candidate.Evidence))
	sourceVersions := make([]string, len(candidate.Evidence))
	sourceSpans := make([]string, len(candidate.Evidence))
	numbers := make([]int32, len(candidate.Evidence))
	hashes := make([]string, len(candidate.Evidence))
	reasons := make([]string, len(candidate.Evidence))
	excerpts := make([]string, len(candidate.Evidence))
	times := make([]time.Time, len(candidate.Evidence))
	now := candidate.UpdatedAt.UTC()
	for index, evidence := range candidate.Evidence {
		idsValue[index], candidateIDs[index] = string(evidence.ID), string(candidate.ID)
		sourceVersions[index], sourceSpans[index] = string(evidence.Provenance.SourceVersionID), string(evidence.Provenance.SourceSpanID)
		numbers[index], hashes[index], reasons[index], excerpts[index], times[index] = int32(index+1), evidence.SemanticHash, evidence.Reason, evidence.Excerpt, now
	}
	_, err := db.Exec(ctx, `
		INSERT INTO graph.semantic_link_candidate_evidence(
			id,workspace_id,candidate_id,source_version_id,source_span_id,evidence_no,semantic_hash,summary,excerpt,created_at
		)
		SELECT value_id::uuid,$1,candidate_id::uuid,source_version_id::uuid,source_span_id::uuid,evidence_no,semantic_hash,summary,excerpt,created_at
		FROM unnest($2::text[],$3::text[],$4::text[],$5::text[],$6::int4[],$7::text[],$8::text[],$9::text[],$10::timestamptz[])
		AS rows(value_id,candidate_id,source_version_id,source_span_id,evidence_no,semantic_hash,summary,excerpt,created_at)`,
		string(candidate.WorkspaceID), idsValue, candidateIDs, sourceVersions, sourceSpans, numbers, hashes, reasons, excerpts, times)
	return classify(err)
}

func findReopenSource(ctx context.Context, db DB, candidate graphdomain.SemanticLinkCandidate) (*foundation.ID, graphdomain.SemanticLinkCandidateReopenedReason, error) {
	row := db.QueryRow(ctx, `
		SELECT id::text
		FROM graph.semantic_link_candidate
		WHERE workspace_id=$1 AND status IN ('IGNORED','FALSE_POSITIVE')
		  AND source_node_type=$2 AND source_node_id=$3 AND target_node_type=$4 AND target_node_id=$5
		ORDER BY updated_at DESC,id DESC LIMIT 1 FOR UPDATE`,
		string(candidate.WorkspaceID), string(candidate.Source.Ref.Type), string(candidate.Source.Ref.ID), string(candidate.Target.Ref.Type), string(candidate.Target.Ref.ID))
	var id string
	if err := row.Scan(&id); errors.Is(err, pgx.ErrNoRows) {
		return nil, "", nil
	} else if err != nil {
		return nil, "", classify(err)
	}
	parsed := foundation.ID(id)
	reason := graphdomain.SemanticLinkCandidateReopenedReasonContentChanged
	return &parsed, reason, nil
}

func validateCandidateEndpoints(ctx context.Context, db DB, candidate graphdomain.SemanticLinkCandidate) error {
	for _, endpoint := range []graphdomain.SemanticLinkCandidateEndpoint{candidate.Source, candidate.Target} {
		var currentVersion int64
		var status string
		var err error
		switch endpoint.Ref.Type {
		case knowledge.NodeTypeTopic:
			err = db.QueryRow(ctx, `SELECT version,status FROM core.topic WHERE workspace_id=$1 AND id=$2`, string(candidate.WorkspaceID), string(endpoint.Ref.ID)).Scan(&currentVersion, &status)
			if err == nil && status != string(knowledge.TopicStatusActive) {
				err = errors.New("candidate topic endpoint is not active")
			}
		case knowledge.NodeTypeClaim:
			err = db.QueryRow(ctx, `SELECT version,status FROM core.claim WHERE workspace_id=$1 AND id=$2`, string(candidate.WorkspaceID), string(endpoint.Ref.ID)).Scan(&currentVersion, &status)
			if err == nil && status != string(knowledge.ClaimStatusSuggested) && status != string(knowledge.ClaimStatusConfirmed) && status != string(knowledge.ClaimStatusDisputed) {
				err = errors.New("candidate claim endpoint is not eligible")
			}
		default:
			return candidateInvalid("candidate endpoint type is unsupported")
		}
		if errors.Is(err, pgx.ErrNoRows) {
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

func supersedePriorCandidates(ctx context.Context, db DB, candidate graphdomain.SemanticLinkCandidate) error {
	_, err := db.Exec(ctx, `
		UPDATE graph.semantic_link_candidate
		SET status='SUPERSEDED',deferred_until=NULL,version=version+1,updated_at=$7
		WHERE workspace_id=$1 AND fingerprint<>$6
		  AND status<>'SUPERSEDED'
		  AND source_node_type=$2 AND source_node_id=$3 AND target_node_type=$4 AND target_node_id=$5`,
		string(candidate.WorkspaceID), string(candidate.Source.Ref.Type), string(candidate.Source.Ref.ID), string(candidate.Target.Ref.Type), string(candidate.Target.Ref.ID), candidate.Fingerprint, candidate.UpdatedAt.UTC())
	return classify(err)
}

func loadDecisionByIdempotency(ctx context.Context, db DB, workspaceID foundation.ID, key string) (candidateDecisionRow, bool, error) {
	var row candidateDecisionRow
	var id, candidateID, storedWorkspace, action string
	var proposalID, relationType, reason *string
	err := db.QueryRow(ctx, `
		SELECT id::text,candidate_id::text,workspace_id::text,candidate_version,proposal_id::text,idempotency_key,request_hash,
		       action,relation_type,reason,defer_until,created_at
		FROM graph.semantic_link_candidate_decision
		WHERE workspace_id=$1 AND idempotency_key=$2`, string(workspaceID), key).Scan(
		&id, &candidateID, &storedWorkspace, &row.candidateVersion, &proposalID, &row.idempotencyKey, &row.requestHash,
		&action, &relationType, &reason, &row.deferUntil, &row.createdAt)
	if errors.Is(err, pgx.ErrNoRows) {
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

func insertDecision(ctx context.Context, db DB, decision candidateDecisionRow) (bool, error) {
	var insertedID string
	err := db.QueryRow(ctx, `
		INSERT INTO graph.semantic_link_candidate_decision(
			id,workspace_id,candidate_id,candidate_version,proposal_id,idempotency_key,request_hash,action,relation_type,reason,defer_until,created_at
		) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12)
		ON CONFLICT (workspace_id,idempotency_key) DO NOTHING
		RETURNING id::text`, decisionID(decision), string(decision.workspaceID), string(decision.candidateID), decision.candidateVersion,
		optionalID(decision.proposalID), decision.idempotencyKey, decision.requestHash, decisionActionWire(decision.action), optionalRelationType(decision.relationType), optionalReason(decision.reason), decision.deferUntil, decision.createdAt.UTC()).Scan(&insertedID)
	if errors.Is(err, pgx.ErrNoRows) {
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

func validateKnowledgeProposalBinding(ctx context.Context, db DB, workspaceID, proposalID foundation.ID) error {
	if !validID(proposalID) {
		return candidateInvalid("relation proposal id is invalid")
	}
	var storedWorkspace, proposalType string
	if err := db.QueryRow(ctx, `SELECT workspace_id::text,proposal_type FROM change_control.proposal WHERE id=$1 FOR SHARE`, string(proposalID)).Scan(&storedWorkspace, &proposalType); errors.Is(err, pgx.ErrNoRows) {
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

func candidateDatabaseNow(ctx context.Context, db DB) (time.Time, error) {
	var now time.Time
	if err := db.QueryRow(ctx, `SELECT CURRENT_TIMESTAMP`).Scan(&now); err != nil {
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

// Keep pgconn in this file's dependency set through a compile-time assertion;
// classify maps PostgreSQL constraint errors to stable project errors.
var _ *pgconn.PgError
