package postgres

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"sort"
	"time"

	graphdomain "github.com/CodeZen-Lizhi/zhixu/internal/graph/domain"
	"github.com/lib/pq"
	"gorm.io/gorm"
)

// gormInsertCandidate binds validated JSONB documents as single driver values.
func gormInsertCandidate(ctx context.Context, database *gorm.DB, candidate graphdomain.SemanticLinkCandidate) (candidateBase, bool, error) {
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
	row := gormQueryRow(ctx, database, `
		INSERT INTO graph.semantic_link_candidate(
			id,workspace_id,source_node_type,source_node_id,source_node_version,
			target_node_type,target_node_id,target_node_version,relation_type,fingerprint_schema_version,fingerprint,
			status,reopened_reason,reopened_from_candidate_id,current_proposal_id,confidence_score,reason,
			source_summary,source_excerpt,target_summary,target_excerpt,
			discovery_methods,evidence_semantic_hashes,generation,deferred_until,version,created_at,updated_at
		) VALUES((@p1),(@p2),(@p3),(@p4),(@p5),(@p6),(@p7),(@p8),(@p9),(@p10),(@p11),(@p12),(@p13),(@p14),(@p15),(@p16),(@p17),(@p18),(@p19),(@p20),(@p21),
		         (@p22)::jsonb,(@p23)::jsonb,(@p24)::jsonb,(@p25),(@p26),(@p27),(@p28))
		ON CONFLICT (workspace_id,fingerprint) DO NOTHING
		RETURNING id::text`,
		sql.Named("p1", string(candidate.ID)), sql.Named("p2", string(candidate.WorkspaceID)), sql.Named("p3", string(candidate.Source.Ref.Type)), sql.Named("p4", string(candidate.Source.Ref.ID)), sql.Named("p5", candidate.Source.Version),
		sql.Named("p6", string(candidate.Target.Ref.Type)), sql.Named("p7", string(candidate.Target.Ref.ID)), sql.Named("p8", candidate.Target.Version), sql.Named("p9", string(candidate.SuggestedRelationType)),
		sql.Named("p10", semanticLinkCandidateFingerprintSchemaVersion), sql.Named("p11", candidate.Fingerprint), sql.Named("p12", string(candidate.Status)), sql.Named("p13", reopenedReason), sql.Named("p14", reopenedFrom), sql.Named("p15", proposalID),
		sql.Named("p16", candidate.Confidence), sql.Named("p17", candidate.Reason), sql.Named("p18", candidate.Source.Summary), sql.Named("p19", candidate.Source.Excerpt), sql.Named("p20", candidate.Target.Summary), sql.Named("p21", candidate.Target.Excerpt),
		sql.Named("p22", graphJSONB(discoveryRaw)), sql.Named("p23", graphJSONB(evidenceRaw)), sql.Named("p24", graphJSONB(generationRaw)),
		sql.Named("p25", candidate.ResumeAfter), sql.Named("p26", candidate.Version), sql.Named("p27", candidate.CreatedAt.UTC()), sql.Named("p28", candidate.UpdatedAt.UTC()))
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
func insertGORMCandidateEvidence(ctx context.Context, database *gorm.DB, candidate graphdomain.SemanticLinkCandidate) error {
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
	_, err := gormGraphExec(ctx, database, `
		INSERT INTO graph.semantic_link_candidate_evidence(
			id,workspace_id,candidate_id,source_version_id,source_span_id,evidence_no,semantic_hash,summary,excerpt,created_at
		)
		SELECT value_id::uuid,(@p1),candidate_id::uuid,source_version_id::uuid,source_span_id::uuid,evidence_no,semantic_hash,summary,excerpt,created_at
		FROM unnest((@p2)::text[],(@p3)::text[],(@p4)::text[],(@p5)::text[],(@p6)::int4[],(@p7)::text[],(@p8)::text[],(@p9)::text[],(@p10)::timestamptz[])
		AS rows(value_id,candidate_id,source_version_id,source_span_id,evidence_no,semantic_hash,summary,excerpt,created_at)`,
		sql.Named("p1", string(candidate.WorkspaceID)), sql.Named("p2", pq.Array(idsValue)), sql.Named("p3", pq.Array(candidateIDs)), sql.Named("p4", pq.Array(sourceVersions)), sql.Named("p5", pq.Array(sourceSpans)),
		sql.Named("p6", pq.Array(numbers)), sql.Named("p7", pq.Array(hashes)), sql.Named("p8", pq.Array(reasons)), sql.Named("p9", pq.Array(excerpts)), sql.Named("p10", pq.Array(times)))
	return classifyGORM(ctx, err)
}
