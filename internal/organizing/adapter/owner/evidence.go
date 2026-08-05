package owner

import (
	"context"
	"sort"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	organizingdomain "github.com/CodeZen-Lizhi/zhixu/internal/organizing/domain"
	retrievalapp "github.com/CodeZen-Lizhi/zhixu/internal/retrieval/application"
	retrievaldomain "github.com/CodeZen-Lizhi/zhixu/internal/retrieval/domain"
)

const maxCitationBatchSize = 500

type citationKey struct {
	indexVersionID  foundation.ID
	chunkID         foundation.ID
	sourceVersionID foundation.ID
	sourceSpanID    foundation.ID
}

type provenanceKey struct {
	sourceVersionID foundation.ID
	sourceSpanID    foundation.ID
}

func citationKeyFor(query retrievaldomain.CitationReferenceQuery) citationKey {
	return citationKey{query.IndexVersionID, query.ChunkID, query.SourceVersionID, query.SourceSpanID}
}

func citationQuery(workspaceID foundation.ID, evidence organizingdomain.EvidenceRef) retrievaldomain.CitationReferenceQuery {
	return retrievaldomain.CitationReferenceQuery{
		WorkspaceID: workspaceID, IndexVersionID: evidence.IndexVersionID, ChunkID: evidence.ChunkID,
		SourceVersionID: evidence.SourceVersionID, SourceSpanID: evidence.SourceSpanID,
	}
}

func evidenceFromOpened(opened retrievalapp.OpenedCitationEvidence) (organizingdomain.EvidenceRef, error) {
	query := opened.Query
	reference := opened.View.Reference
	if err := retrievaldomain.ValidateCitationReferenceQuery(query); err != nil ||
		retrievaldomain.ValidateSourceSpanReference(reference) != nil ||
		reference.SourceVersion.WorkspaceID != query.WorkspaceID ||
		reference.SourceVersion.SourceVersionID != query.SourceVersionID || reference.Span.ID != query.SourceSpanID {
		return organizingdomain.EvidenceRef{}, inconsistent("retrieval returned an invalid citation binding")
	}
	evidence := organizingdomain.EvidenceRef{
		IndexVersionID: query.IndexVersionID, ChunkID: query.ChunkID,
		SourceVersionID: query.SourceVersionID, SourceSpanID: query.SourceSpanID,
		ContentHash: reference.SourceVersion.ContentHash, ExcerptHash: reference.ExcerptHash,
	}
	if err := evidence.Validate(); err != nil {
		return organizingdomain.EvidenceRef{}, inconsistent("retrieval citation cannot form organizing evidence")
	}
	return evidence, nil
}

func (adapter *Adapter) openEvidence(
	ctx context.Context,
	workspaceID foundation.ID,
	queries []retrievaldomain.CitationReferenceQuery,
) (map[citationKey]organizingdomain.EvidenceRef, error) {
	if len(queries) == 0 {
		return map[citationKey]organizingdomain.EvidenceRef{}, nil
	}
	unique := make(map[citationKey]retrievaldomain.CitationReferenceQuery, len(queries))
	for _, query := range queries {
		if query.WorkspaceID != workspaceID || retrievaldomain.ValidateCitationReferenceQuery(query) != nil {
			return nil, invalid("organizing citation query is invalid")
		}
		unique[citationKeyFor(query)] = query
	}
	byIndex := make(map[foundation.ID][]retrievaldomain.CitationReferenceQuery)
	for _, query := range unique {
		byIndex[query.IndexVersionID] = append(byIndex[query.IndexVersionID], query)
	}
	indexIDs := make([]foundation.ID, 0, len(byIndex))
	for indexID := range byIndex {
		indexIDs = append(indexIDs, indexID)
	}
	sort.Slice(indexIDs, func(i, j int) bool { return indexIDs[i] < indexIDs[j] })

	result := make(map[citationKey]organizingdomain.EvidenceRef, len(unique))
	for _, indexID := range indexIDs {
		batch := byIndex[indexID]
		sort.Slice(batch, func(i, j int) bool {
			left, right := citationKeyFor(batch[i]), citationKeyFor(batch[j])
			if left.chunkID != right.chunkID {
				return left.chunkID < right.chunkID
			}
			if left.sourceVersionID != right.sourceVersionID {
				return left.sourceVersionID < right.sourceVersionID
			}
			return left.sourceSpanID < right.sourceSpanID
		})
		for start := 0; start < len(batch); start += maxCitationBatchSize {
			end := start + maxCitationBatchSize
			if end > len(batch) {
				end = len(batch)
			}
			requested := batch[start:end]
			opened, err := adapter.dependencies.Evidence.OpenCitationEvidenceBatch(ctx, requested)
			if err != nil {
				return nil, err
			}
			if len(opened) != len(requested) {
				return nil, inconsistent("retrieval returned an incomplete citation batch")
			}
			requestedSet := make(map[citationKey]struct{}, len(requested))
			for _, query := range requested {
				requestedSet[citationKeyFor(query)] = struct{}{}
			}
			for _, item := range opened {
				key := citationKeyFor(item.Query)
				if _, ok := requestedSet[key]; !ok {
					return nil, inconsistent("retrieval returned an out-of-scope citation")
				}
				if _, duplicate := result[key]; duplicate {
					return nil, inconsistent("retrieval returned a duplicate citation")
				}
				evidence, err := evidenceFromOpened(item)
				if err != nil {
					return nil, err
				}
				result[key] = evidence
			}
		}
	}
	if len(result) != len(unique) {
		return nil, inconsistent("retrieval omitted requested citation evidence")
	}
	return result, nil
}

func (adapter *Adapter) verifyEvidence(
	ctx context.Context,
	workspaceID foundation.ID,
	references []organizingdomain.MaterialRef,
) error {
	queries := make([]retrievaldomain.CitationReferenceQuery, 0)
	expected := make(map[citationKey]organizingdomain.EvidenceRef)
	for _, reference := range references {
		for _, evidence := range reference.Evidence {
			query := citationQuery(workspaceID, evidence)
			key := citationKeyFor(query)
			if prior, exists := expected[key]; exists && prior != evidence {
				return inconsistent("organizing materials disagree on one citation")
			}
			if _, exists := expected[key]; !exists {
				queries = append(queries, query)
				expected[key] = evidence
			}
		}
	}
	opened, err := adapter.openEvidence(ctx, workspaceID, queries)
	if err != nil {
		return err
	}
	for key, want := range expected {
		if actual, ok := opened[key]; !ok || actual != want {
			return stale("organizing citation changed before confirmation")
		}
	}
	return nil
}
