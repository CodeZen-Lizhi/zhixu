package postgres

import (
	"context"
	"database/sql"
	"errors"
	"sort"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	graphapp "github.com/CodeZen-Lizhi/zhixu/internal/graph/application"
	graphdomain "github.com/CodeZen-Lizhi/zhixu/internal/graph/domain"
	knowledge "github.com/CodeZen-Lizhi/zhixu/internal/knowledge/domain"
	"github.com/lib/pq"
	"gorm.io/gorm"
)

// GORMSemanticLinkDiscoveryCandidateWriter persists bounded rule-discovery
// output through the same GORMRepository used for Candidate commands.
// It never owns a second database root or transaction boundary.
type GORMSemanticLinkDiscoveryCandidateWriter struct {
	repository *GORMRepository
	ids        foundation.IDGenerator
	clock      foundation.Clock
}

var _ graphapp.SemanticLinkDiscoveryCandidateWriter = (*GORMSemanticLinkDiscoveryCandidateWriter)(nil)

// NewGORMSemanticLinkDiscoveryCandidateWriter creates the rule-only
// Candidate writer from one already-constructed GORMRepository.
func NewGORMSemanticLinkDiscoveryCandidateWriter(repository *GORMRepository, ids foundation.IDGenerator, clock foundation.Clock) (*GORMSemanticLinkDiscoveryCandidateWriter, error) {
	if repository == nil || !validGraphGORMDatabase(repository.database) || nilGraphGORMDependency(repository.unitOfWork) || isNilScanDependency(ids) || isNilScanDependency(clock) {
		return nil, scanRepositoryUnavailable(errors.New("semantic link GORM candidate writer dependencies are missing"))
	}
	return &GORMSemanticLinkDiscoveryCandidateWriter{repository: repository, ids: ids, clock: clock}, nil
}

// PersistDiscoveryCandidates loads all Claim evidence once, then persists only
// the bounded eligible discovery hits through Candidate fingerprint idempotency.
func (writer *GORMSemanticLinkDiscoveryCandidateWriter) PersistDiscoveryCandidates(ctx context.Context, request graphapp.SemanticLinkDiscoveryCandidateWriteRequest) (graphapp.SemanticLinkDiscoveryCandidateWriteResult, error) {
	if writer == nil || writer.repository == nil || isNilScanDependency(writer.ids) || isNilScanDependency(writer.clock) {
		return graphapp.SemanticLinkDiscoveryCandidateWriteResult{}, scanRepositoryUnavailable(errors.New("semantic link GORM candidate writer is unavailable"))
	}
	if ctx == nil {
		return graphapp.SemanticLinkDiscoveryCandidateWriteResult{}, scanRepositoryInvalid(errors.New("semantic link GORM candidate writer context is nil"))
	}
	if err := writer.repository.ready(ctx); err != nil {
		return graphapp.SemanticLinkDiscoveryCandidateWriteResult{}, scanRepositoryUnavailable(err)
	}
	if request.WorkspaceID == "" || request.Discovery.WorkspaceID != request.WorkspaceID || !graphdomain.EqualSemanticLinkCandidateGeneration(request.Generation.Rule, semanticLinkRuleGeneration()) {
		return graphapp.SemanticLinkDiscoveryCandidateWriteResult{}, scanRepositoryInvalid(errors.New("semantic link candidate write request is invalid"))
	}
	nodes, err := discoveryPairNodeIndex(request.Pairs)
	if err != nil {
		return graphapp.SemanticLinkDiscoveryCandidateWriteResult{}, err
	}
	evidence, err := writer.loadClaimEvidence(ctx, request.WorkspaceID, request.Discovery.Items)
	if err != nil {
		return graphapp.SemanticLinkDiscoveryCandidateWriteResult{}, err
	}

	result := graphapp.SemanticLinkDiscoveryCandidateWriteResult{}
	for _, hit := range request.Discovery.Items {
		classification := graphdomain.ClassifySemanticLinkRuleDiscoveryHit(hit)
		if !classification.Eligible {
			continue
		}
		pair, ok := nodes[discoveryPairKey(hit.Source, hit.Target)]
		if !ok || pair.Source.Endpoint.Ref.Type != knowledge.NodeTypeClaim || pair.Target.Endpoint.Ref.Type != knowledge.NodeTypeClaim {
			result.Failed++
			continue
		}
		candidateEvidence := mergeCandidateEvidence(evidence[pair.Source.Endpoint.Ref.ID], evidence[pair.Target.Endpoint.Ref.ID])
		if len(candidateEvidence) == 0 {
			result.Failed++
			continue
		}

		candidateID, err := writer.ids.New()
		if err != nil {
			return graphapp.SemanticLinkDiscoveryCandidateWriteResult{}, err
		}
		for index := range candidateEvidence {
			candidateEvidence[index].ID, err = writer.ids.New()
			if err != nil {
				return graphapp.SemanticLinkDiscoveryCandidateWriteResult{}, err
			}
		}
		now := writer.clock.Now().UTC()
		if now.IsZero() {
			return graphapp.SemanticLinkDiscoveryCandidateWriteResult{}, scanRepositoryUnavailable(errors.New("semantic link candidate writer clock returned zero time"))
		}
		candidate := graphdomain.SemanticLinkCandidate{
			ID: candidateID, WorkspaceID: request.WorkspaceID,
			Source: pair.Source.Endpoint, Target: pair.Target.Endpoint,
			SuggestedRelationType: classification.RelationType, Status: graphdomain.SemanticLinkCandidateStatusActive,
			Reason: classification.Reason, Confidence: classification.Confidence,
			DiscoveryMethods: append([]graphdomain.SemanticLinkDiscoveryMethod(nil), hit.Methods...),
			Evidence:         candidateEvidence, Generation: request.Generation.Rule,
			Version: 1, CreatedAt: now, UpdatedAt: now,
		}
		candidate.Fingerprint, err = graphdomain.ComputeSemanticLinkCandidateFingerprint(candidate)
		if err != nil {
			return graphapp.SemanticLinkDiscoveryCandidateWriteResult{}, err
		}
		persisted, err := writer.repository.UpsertSemanticLinkCandidate(ctx, candidate)
		if err != nil {
			return graphapp.SemanticLinkDiscoveryCandidateWriteResult{}, err
		}
		switch {
		case persisted.Suppressed:
			result.Suppressed++
		case persisted.Reopened:
			result.Reopened++
		default:
			// Candidate fingerprint owns fact idempotency. The Scan checkpoint
			// remains the once-only counter boundary for exact active replay.
			result.Created++
		}
	}
	return result, nil
}

func (writer *GORMSemanticLinkDiscoveryCandidateWriter) loadClaimEvidence(ctx context.Context, workspaceID foundation.ID, hits []graphdomain.SemanticLinkDiscoveryHit) (map[foundation.ID][]graphdomain.SemanticLinkCandidateEvidence, error) {
	claimSet := make(map[foundation.ID]struct{})
	for _, hit := range hits {
		if hit.Source.Type == knowledge.NodeTypeClaim {
			claimSet[hit.Source.ID] = struct{}{}
		}
		if hit.Target.Type == knowledge.NodeTypeClaim {
			claimSet[hit.Target.ID] = struct{}{}
		}
	}
	claimIDs := make([]string, 0, len(claimSet))
	for id := range claimSet {
		claimIDs = append(claimIDs, string(id))
	}
	sort.Strings(claimIDs)
	result := make(map[foundation.ID][]graphdomain.SemanticLinkCandidateEvidence, len(claimIDs))
	if len(claimIDs) == 0 {
		return result, nil
	}

	err := writer.repository.gormReadSnapshot(ctx, func(callbackCtx context.Context, database *gorm.DB) error {
		rows, queryErr := gormRawRows(callbackCtx, database, `
			SELECT evidence.claim_id::text,evidence.id::text,evidence.source_version_id::text,evidence.source_span_id::text,
			       evidence.evidence_hash,evidence.reason,
			       COALESCE((
			           SELECT chunk.content
			           FROM ingestion.canonical_chunk chunk
			           WHERE chunk.workspace_id=(@p1) AND chunk.source_span_id=evidence.source_span_id
			           ORDER BY chunk.sequence,chunk.id
			           LIMIT 1
			       ),evidence.reason)
			FROM unnest((@p2)::uuid[]) requested(claim_id)
			CROSS JOIN LATERAL (
			    SELECT claim_source.claim_id,claim_source.id,claim_source.source_version_id,claim_source.source_span_id,
			           claim_source.evidence_hash,claim_source.reason
			    FROM core.claim_source claim_source
			    WHERE claim_source.workspace_id=(@p1) AND claim_source.claim_id=requested.claim_id
			    ORDER BY claim_source.evidence_hash,claim_source.id
			    LIMIT 2
			) evidence
			ORDER BY evidence.claim_id,evidence.evidence_hash,evidence.id`, sql.Named("p1", string(workspaceID)), sql.Named("p2", pq.Array(claimIDs)))
		if queryErr != nil {
			return queryErr
		}
		defer rows.Close()
		perClaim := make(map[foundation.ID]int)
		for rows.Next() {
			var claimID, evidenceID, sourceVersionID, sourceSpanID, semanticHash, reason, excerpt string
			if scanErr := rows.Scan(&claimID, &evidenceID, &sourceVersionID, &sourceSpanID, &semanticHash, &reason, &excerpt); scanErr != nil {
				return scanErr
			}
			owner := foundation.ID(claimID)
			if perClaim[owner] >= 2 {
				continue
			}
			result[owner] = append(result[owner], graphdomain.SemanticLinkCandidateEvidence{
				ID: foundation.ID(evidenceID),
				Provenance: knowledge.ProvenanceRef{
					WorkspaceID: workspaceID, SourceVersionID: foundation.ID(sourceVersionID), SourceSpanID: foundation.ID(sourceSpanID),
				},
				SemanticHash: semanticHash,
				Reason:       boundedScanText(reason, 4096),
				Excerpt:      boundedScanText(excerpt, 4096),
			})
			perClaim[owner]++
		}
		if rowsErr := rows.Err(); rowsErr != nil {
			return rowsErr
		}
		rows.Close()
		return rows.Err()
	})
	if err != nil {
		return nil, gormDiscoveryCandidateError(ctx, err, "GRAPH_SEMANTIC_LINK_SCAN_EVIDENCE_QUERY_FAILED")
	}
	return result, nil
}

func gormDiscoveryCandidateError(ctx context.Context, err error, code string) error {
	if err == nil {
		return nil
	}
	if cause := graphGORMContextCause(ctx, err); cause != nil {
		if errors.Is(cause, context.Canceled) {
			return foundation.NewError(foundation.ErrorNonRetryableFailure, code, false, cause)
		}
		return foundation.NewError(foundation.ErrorRetryableFailure, code, true, cause)
	}
	var classified *foundation.Error
	if errors.As(err, &classified) {
		return foundation.NewError(classified.Kind, code, classified.Retryable, err)
	}
	return scanRepositoryClassify(err, code)
}
