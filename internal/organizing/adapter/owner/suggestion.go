package owner

import (
	"context"
	"sort"
	"strings"
	"unicode/utf8"

	captureapp "github.com/CodeZen-Lizhi/zhixu/internal/capture/application"
	capturedomain "github.com/CodeZen-Lizhi/zhixu/internal/capture/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	knowledgedomain "github.com/CodeZen-Lizhi/zhixu/internal/knowledge/domain"
	organizingapp "github.com/CodeZen-Lizhi/zhixu/internal/organizing/application"
	organizingdomain "github.com/CodeZen-Lizhi/zhixu/internal/organizing/domain"
	retrievaldomain "github.com/CodeZen-Lizhi/zhixu/internal/retrieval/domain"
)

type sourceSuggestion struct {
	id       foundation.ID
	order    int
	score    float64
	queries  []retrievaldomain.CitationReferenceQuery
	querySet map[citationKey]struct{}
}

type rankedCandidate struct {
	candidate organizingapp.MaterialCandidate
	formal    bool
	order     int
}

// Suggest 组合一次 Hybrid Search、有界 owner 补全与精确 Citation 打开。
func (adapter *Adapter) Suggest(ctx context.Context, query organizingapp.SuggestionQuery) ([]organizingapp.MaterialCandidate, error) {
	if adapter == nil || ctx == nil || !validID(query.WorkspaceID) || strings.TrimSpace(query.Intent) == "" ||
		query.Limit < 1 || query.Limit > organizingdomain.MaxDraftMaterials {
		return nil, invalid("organizing suggestion request is invalid")
	}
	searchRequest := retrievaldomain.SearchRequest{WorkspaceID: query.WorkspaceID, Query: query.Intent,
		Mode: retrievaldomain.SearchModeHybrid, Limit: int32(query.Limit)}
	search, err := adapter.dependencies.Search.Search(ctx, searchRequest)
	if err != nil {
		return nil, err
	}
	if retrievaldomain.ValidateSearchResult(searchRequest, search) != nil {
		return nil, inconsistent("retrieval returned an invalid search result")
	}

	aggregates, citationQueries := collectSourceSuggestions(query, search)
	if len(aggregates) == 0 {
		return []organizingapp.MaterialCandidate{}, nil
	}
	opened, err := adapter.openEvidence(ctx, query.WorkspaceID, citationQueries)
	if err != nil {
		return nil, err
	}
	sourceIDs := make([]foundation.ID, len(aggregates))
	for index, aggregate := range aggregates {
		sourceIDs[index] = aggregate.id
	}
	sources, err := adapter.loadSources(ctx, query.WorkspaceID, sourceIDs)
	if err != nil {
		return nil, err
	}
	profiles, err := adapter.loadProfiles(ctx, query.WorkspaceID, sourceIDs)
	if err != nil {
		return nil, err
	}

	ranked := make([]rankedCandidate, 0, query.Limit*2)
	evidenceByProvenance := make(map[provenanceKey][]organizingdomain.EvidenceRef)
	scoreByProvenance := make(map[provenanceKey]float64)
	for _, aggregate := range aggregates {
		source := sources[aggregate.id]
		evidence := make([]organizingdomain.EvidenceRef, 0, len(aggregate.queries))
		for _, citation := range aggregate.queries {
			item, ok := opened[citationKeyFor(citation)]
			if !ok || item.SourceVersionID != aggregate.id || item.ContentHash != source.ContentHash {
				return nil, inconsistent("retrieval source hydration disagrees with opened citation")
			}
			evidence = append(evidence, item)
			key := provenanceKey{item.SourceVersionID, item.SourceSpanID}
			evidenceByProvenance[key] = appendUniqueEvidence(evidenceByProvenance[key], item)
			if aggregate.score > scoreByProvenance[key] {
				scoreByProvenance[key] = aggregate.score
			}
		}
		evidence = sortEvidence(evidence)
		profileID, profileAvailability := profileBinding(profiles[aggregate.id])
		availability := sourceAvailability(source)
		if profileAvailability == organizingdomain.MaterialStale && availability == organizingdomain.MaterialAvailable {
			availability = organizingdomain.MaterialStale
		}
		reasons := []organizingdomain.SuggestionReasonCode{organizingdomain.ReasonHybridMatch}
		profileMatch, aliasMatch := profileMatchesIntent(profiles[aggregate.id], query.Intent)
		if profileMatch {
			reasons = append(reasons, organizingdomain.ReasonProfileMatch)
		}
		if aliasMatch {
			reasons = append(reasons, organizingdomain.ReasonAliasMatch)
		}
		reference := organizingdomain.MaterialRef{Kind: organizingdomain.MaterialSourceVersion, SourceVersionID: aggregate.id,
			ProfileRevisionID: profileID, ContentHash: source.ContentHash, Evidence: evidence}
		if reference.Validate() != nil {
			return nil, inconsistent("retrieval source suggestion cannot form an organizing material")
		}
		ranked = append(ranked, rankedCandidate{candidate: organizingapp.MaterialCandidate{Reference: reference,
			Title: boundedTitle(source.LogicalName), Reasons: reasons, Origin: organizingdomain.MaterialOriginSuggested,
			Availability: availability, Score: aggregate.score}, order: aggregate.order})
	}

	claimLimit := query.Limit * 4
	if claimLimit > knowledgedomain.MaxBatchLimit {
		claimLimit = knowledgedomain.MaxBatchLimit
	}
	claims, err := adapter.dependencies.Knowledge.GetClaims(ctx, knowledgedomain.BatchGetClaimsQuery{WorkspaceID: query.WorkspaceID,
		Statuses: []knowledgedomain.ClaimStatus{knowledgedomain.ClaimStatusConfirmed, knowledgedomain.ClaimStatusDisputed}, Limit: claimLimit})
	if err != nil {
		return nil, err
	}
	if len(claims) > claimLimit {
		return nil, inconsistent("knowledge exceeded the requested formal claim batch")
	}
	seenClaims := make(map[foundation.ID]struct{}, len(claims))
	for claimIndex, item := range claims {
		if knowledgedomain.ValidateClaimAggregate(item.Claim, item.Sources) != nil || item.Claim.WorkspaceID != query.WorkspaceID || !formalClaim(item.Claim.Status) {
			return nil, inconsistent("knowledge returned an invalid formal claim")
		}
		if _, duplicate := seenClaims[item.Claim.ID]; duplicate {
			return nil, inconsistent("knowledge returned duplicate formal claims")
		}
		seenClaims[item.Claim.ID] = struct{}{}
		claimEvidence := make([]organizingdomain.EvidenceRef, 0)
		score := 0.0
		for _, source := range item.Sources {
			key := provenanceKey{source.Provenance.SourceVersionID, source.Provenance.SourceSpanID}
			claimEvidence = appendUniqueEvidence(claimEvidence, evidenceByProvenance[key]...)
			if scoreByProvenance[key] > score {
				score = scoreByProvenance[key]
			}
		}
		if len(claimEvidence) == 0 {
			continue
		}
		claimEvidence = sortEvidence(claimEvidence)
		reference := organizingdomain.MaterialRef{Kind: organizingdomain.MaterialClaim, ClaimID: item.Claim.ID,
			Version: item.Claim.Version, ContentHash: item.Claim.Fingerprint, Evidence: claimEvidence}
		if reference.Validate() != nil {
			return nil, inconsistent("knowledge claim cannot form an organizing material")
		}
		ranked = append(ranked, rankedCandidate{candidate: organizingapp.MaterialCandidate{Reference: reference,
			Title: boundedTitle(item.Claim.Statement), Reasons: []organizingdomain.SuggestionReasonCode{organizingdomain.ReasonFormalKnowledge},
			Origin: organizingdomain.MaterialOriginSuggested, Availability: organizingdomain.MaterialAvailable, Score: score},
			formal: true, order: len(aggregates) + claimIndex})
	}

	sort.SliceStable(ranked, func(i, j int) bool {
		if ranked[i].candidate.Score != ranked[j].candidate.Score {
			return ranked[i].candidate.Score > ranked[j].candidate.Score
		}
		if ranked[i].formal != ranked[j].formal {
			return ranked[i].formal
		}
		return ranked[i].order < ranked[j].order
	})
	if len(ranked) > query.Limit {
		ranked = ranked[:query.Limit]
	}
	result := make([]organizingapp.MaterialCandidate, len(ranked))
	for index := range ranked {
		result[index] = ranked[index].candidate
	}
	return result, nil
}

func collectSourceSuggestions(
	query organizingapp.SuggestionQuery,
	search retrievaldomain.SearchResult,
) ([]*sourceSuggestion, []retrievaldomain.CitationReferenceQuery) {
	bySource := make(map[foundation.ID]*sourceSuggestion)
	result := make([]*sourceSuggestion, 0, query.Limit)
	queries := make([]retrievaldomain.CitationReferenceQuery, 0, query.Limit)
	for itemIndex, item := range search.Items {
		for _, provenance := range item.Provenances {
			aggregate := bySource[provenance.SourceVersionID]
			if aggregate == nil {
				if len(result) >= query.Limit || len(queries) >= maxCitationBatchSize {
					continue
				}
				aggregate = &sourceSuggestion{id: provenance.SourceVersionID, order: len(result),
					score: scoreForRank(itemIndex), querySet: make(map[citationKey]struct{})}
				bySource[provenance.SourceVersionID] = aggregate
				result = append(result, aggregate)
			}
			if len(queries) >= maxCitationBatchSize || len(aggregate.queries) >= organizingdomain.MaxEvidencePerMaterial {
				continue
			}
			citation := retrievaldomain.CitationReferenceQuery{WorkspaceID: query.WorkspaceID, IndexVersionID: item.IndexVersionID,
				ChunkID: item.ChunkID, SourceVersionID: provenance.SourceVersionID, SourceSpanID: item.Span.ID}
			key := citationKeyFor(citation)
			if _, duplicate := aggregate.querySet[key]; duplicate {
				continue
			}
			aggregate.querySet[key] = struct{}{}
			aggregate.queries = append(aggregate.queries, citation)
			queries = append(queries, citation)
		}
	}
	return result, queries
}

func scoreForRank(index int) float64 {
	return 1 / float64(index+1)
}

func appendUniqueEvidence(current []organizingdomain.EvidenceRef, values ...organizingdomain.EvidenceRef) []organizingdomain.EvidenceRef {
	seen := make(map[citationKey]struct{}, len(current)+len(values))
	for _, item := range current {
		seen[citationKey{item.IndexVersionID, item.ChunkID, item.SourceVersionID, item.SourceSpanID}] = struct{}{}
	}
	for _, item := range values {
		key := citationKey{item.IndexVersionID, item.ChunkID, item.SourceVersionID, item.SourceSpanID}
		if _, duplicate := seen[key]; duplicate || len(current) >= organizingdomain.MaxEvidencePerMaterial {
			continue
		}
		seen[key] = struct{}{}
		current = append(current, item)
	}
	return current
}

func profileMatchesIntent(view captureapp.ProfileView, intent string) (bool, bool) {
	if view.Revision == nil || (view.Profile.Status != capturedomain.ProfileStatusReady && view.Profile.Status != capturedomain.ProfileStatusStale) {
		return false, false
	}
	intent = strings.ToLower(strings.TrimSpace(intent))
	profileMatch, aliasMatch := false, false
	for _, candidate := range append(append([]capturedomain.ProfileCandidate(nil), view.Revision.Content.Topics...), view.Revision.Content.Terms...) {
		if containsProfileTerm(intent, candidate.Label) {
			profileMatch = true
		}
		for _, alias := range candidate.Aliases {
			if containsProfileTerm(intent, alias) {
				profileMatch, aliasMatch = true, true
			}
		}
	}
	return profileMatch, aliasMatch
}

func containsProfileTerm(intent, value string) bool {
	value = strings.ToLower(strings.TrimSpace(value))
	return value != "" && strings.Contains(intent, value)
}

func boundedTitle(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	if len(value) <= 512 {
		return value
	}
	value = value[:512]
	for !utf8.ValidString(value) {
		value = value[:len(value)-1]
	}
	return strings.TrimSpace(value)
}

func isHash(value string) bool {
	if len(value) != 64 || value != strings.ToLower(value) {
		return false
	}
	for _, character := range value {
		if (character < '0' || character > '9') && (character < 'a' || character > 'f') {
			return false
		}
	}
	return true
}
