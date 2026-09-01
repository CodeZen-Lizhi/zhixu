package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"sort"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	graphdomain "github.com/CodeZen-Lizhi/zhixu/internal/graph/domain"
	"github.com/lib/pq"
	"gorm.io/gorm"
)

func gormLoadCandidate(ctx context.Context, database *gorm.DB, workspaceID foundation.ID, value any, key string, forUpdate bool) (candidateBase, error) {
	store, err := newGORMDB(database)
	if err != nil {
		return candidateBase{}, classifyGORM(ctx, err)
	}
	return loadCandidate(ctx, store, workspaceID, value, key, forUpdate)
}

func gormLoadDecisionByIdempotency(ctx context.Context, database *gorm.DB, workspaceID foundation.ID, key string) (candidateDecisionRow, bool, error) {
	store, err := newGORMDB(database)
	if err != nil {
		return candidateDecisionRow{}, false, classifyGORM(ctx, err)
	}
	return loadDecisionByIdempotency(ctx, store, workspaceID, key)
}

func gormInsertDecision(ctx context.Context, database *gorm.DB, decision candidateDecisionRow) (bool, error) {
	store, err := newGORMDB(database)
	if err != nil {
		return false, classifyGORM(ctx, err)
	}
	return insertDecision(ctx, store, decision)
}

// gormInsertCandidate is kept GORM-specific because JSONB values must reach
// database/sql as validated string-valued carriers rather than bare []byte.
func gormInsertCandidate(ctx context.Context, database *gormDB, candidate graphdomain.SemanticLinkCandidate) (candidateBase, bool, error) {
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
	row := database.QueryRow(ctx, `
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
		graphJSONB(discoveryRaw), graphJSONB(evidenceRaw), graphJSONB(generationRaw),
		candidate.ResumeAfter, candidate.Version, candidate.CreatedAt.UTC(), candidate.UpdatedAt.UTC())
	var insertedID string
	if err := row.Scan(&insertedID); gormGraphNoRows(err) {
		return candidateBase{}, false, nil
	} else if err != nil {
		return candidateBase{}, false, classifyGORM(ctx, err)
	}
	return candidateBase{candidate: candidate, evidenceHashes: evidenceHashes}, true, nil
}

// insertGORMCandidateEvidence keeps Evidence insertion to one PostgreSQL
// statement. Every array is one driver.Valuer binding so GORM cannot expand a
// slice into a variable number of placeholders.
func insertGORMCandidateEvidence(ctx context.Context, database *gormDB, candidate graphdomain.SemanticLinkCandidate) error {
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
		idsValue[index] = string(evidence.ID)
		candidateIDs[index] = string(candidate.ID)
		sourceVersions[index] = string(evidence.Provenance.SourceVersionID)
		sourceSpans[index] = string(evidence.Provenance.SourceSpanID)
		numbers[index] = int32(index + 1)
		hashes[index] = evidence.SemanticHash
		reasons[index] = evidence.Reason
		excerpts[index] = evidence.Excerpt
		times[index] = now
	}
	_, err := database.Exec(ctx, `
		INSERT INTO graph.semantic_link_candidate_evidence(
			id,workspace_id,candidate_id,source_version_id,source_span_id,evidence_no,semantic_hash,summary,excerpt,created_at
		)
		SELECT value_id::uuid,$1,candidate_id::uuid,source_version_id::uuid,source_span_id::uuid,evidence_no,semantic_hash,summary,excerpt,created_at
		FROM unnest($2::text[],$3::text[],$4::text[],$5::text[],$6::int4[],$7::text[],$8::text[],$9::text[],$10::timestamptz[])
		AS rows(value_id,candidate_id,source_version_id,source_span_id,evidence_no,semantic_hash,summary,excerpt,created_at)`,
		string(candidate.WorkspaceID), pq.Array(idsValue), pq.Array(candidateIDs), pq.Array(sourceVersions), pq.Array(sourceSpans),
		pq.Array(numbers), pq.Array(hashes), pq.Array(reasons), pq.Array(excerpts), pq.Array(times))
	return classifyGORM(ctx, err)
}
