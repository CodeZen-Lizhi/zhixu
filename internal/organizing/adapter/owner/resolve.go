package owner

import (
	"context"
	"reflect"
	"strings"

	authoringapp "github.com/CodeZen-Lizhi/zhixu/internal/authoring/application"
	authoringdomain "github.com/CodeZen-Lizhi/zhixu/internal/authoring/domain"
	captureapp "github.com/CodeZen-Lizhi/zhixu/internal/capture/application"
	capturedomain "github.com/CodeZen-Lizhi/zhixu/internal/capture/domain"
	collectionapp "github.com/CodeZen-Lizhi/zhixu/internal/collection/application"
	collectiondomain "github.com/CodeZen-Lizhi/zhixu/internal/collection/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	knowledgedomain "github.com/CodeZen-Lizhi/zhixu/internal/knowledge/domain"
	organizingapp "github.com/CodeZen-Lizhi/zhixu/internal/organizing/application"
	organizingdomain "github.com/CodeZen-Lizhi/zhixu/internal/organizing/domain"
	retrievalapp "github.com/CodeZen-Lizhi/zhixu/internal/retrieval/application"
	retrievaldomain "github.com/CodeZen-Lizhi/zhixu/internal/retrieval/domain"
)

const maxSerialOwnerLookups = 20

// Resolve 通过公开 owner API 批量补全只含身份的材料选择器。
func (adapter *Adapter) Resolve(
	ctx context.Context,
	workspaceID foundation.ID,
	selectors []organizingapp.MaterialSelector,
) ([]organizingapp.MaterialCandidate, error) {
	if adapter == nil || ctx == nil || !validID(workspaceID) || len(selectors) == 0 || len(selectors) > organizingdomain.MaxDraftMaterials {
		return nil, invalid("organizing material resolve request is invalid")
	}
	result := make([]organizingapp.MaterialCandidate, len(selectors))
	sourceIDs := make([]foundation.ID, 0, len(selectors))
	sourceIndexes := make(map[foundation.ID][]int)
	documentIdentities := make([]authoringapp.ArticleRevisionIdentity, 0, len(selectors))
	documentIndexes := make(map[authoringapp.ArticleRevisionIdentity][]int)
	claimIDs := make([]foundation.ID, 0, len(selectors))
	claimIndexes := make(map[foundation.ID][]int)
	collectionCount := 0
	for index, selector := range selectors {
		if !validSelector(selector) {
			return nil, invalid("organizing material selector is invalid")
		}
		switch selector.Kind {
		case organizingdomain.MaterialSourceVersion:
			if _, exists := sourceIndexes[selector.SourceVersionID]; !exists {
				sourceIDs = append(sourceIDs, selector.SourceVersionID)
			}
			sourceIndexes[selector.SourceVersionID] = append(sourceIndexes[selector.SourceVersionID], index)
		case organizingdomain.MaterialDocumentRevision:
			identity := authoringapp.ArticleRevisionIdentity{DocumentID: selector.DocumentID, RevisionID: selector.ArticleRevisionID}
			if _, exists := documentIndexes[identity]; !exists {
				documentIdentities = append(documentIdentities, identity)
			}
			documentIndexes[identity] = append(documentIndexes[identity], index)
		case organizingdomain.MaterialClaim:
			if _, exists := claimIndexes[selector.ClaimID]; !exists {
				claimIDs = append(claimIDs, selector.ClaimID)
			}
			claimIndexes[selector.ClaimID] = append(claimIndexes[selector.ClaimID], index)
		case organizingdomain.MaterialSmartCollection:
			collectionCount++
		}
	}
	if collectionCount > maxSerialOwnerLookups {
		return nil, capabilityUnavailable("owner API does not provide a bounded batch lookup for this material kind")
	}

	if len(sourceIDs) > 0 {
		sources, err := adapter.loadSources(ctx, workspaceID, sourceIDs)
		if err != nil {
			return nil, err
		}
		profiles, err := adapter.loadProfiles(ctx, workspaceID, sourceIDs)
		if err != nil {
			return nil, err
		}
		for _, sourceID := range sourceIDs {
			source := sources[sourceID]
			profileID, profileAvailability := profileBinding(profiles[sourceID])
			availability := sourceAvailability(source)
			if profileAvailability == organizingdomain.MaterialStale && availability == organizingdomain.MaterialAvailable {
				availability = organizingdomain.MaterialStale
			}
			candidate := organizingapp.MaterialCandidate{
				Reference: organizingdomain.MaterialRef{Kind: organizingdomain.MaterialSourceVersion,
					SourceVersionID: sourceID, ProfileRevisionID: profileID, ContentHash: source.ContentHash, Evidence: []organizingdomain.EvidenceRef{}},
				Title: boundedTitle(source.LogicalName), Reasons: []organizingdomain.SuggestionReasonCode{organizingdomain.ReasonUserAdded},
				Origin: organizingdomain.MaterialOriginUser, Availability: availability, Score: 1,
			}
			if candidate.Reference.Validate() != nil {
				return nil, inconsistent("source owner returned an invalid organizing material")
			}
			for _, index := range sourceIndexes[sourceID] {
				result[index] = candidate
			}
		}
	}
	if len(documentIdentities) > 0 {
		documents, err := adapter.loadArticleRevisions(ctx, workspaceID, documentIdentities)
		if err != nil {
			return nil, err
		}
		for _, identity := range documentIdentities {
			snapshot := documents[identity]
			candidate, err := documentCandidate(snapshot)
			if err != nil {
				return nil, err
			}
			for _, index := range documentIndexes[identity] {
				result[index] = candidate
			}
		}
	}
	if len(claimIDs) > 0 {
		claims, err := adapter.loadClaimCandidates(ctx, workspaceID, claimIDs, "")
		if err != nil {
			return nil, err
		}
		for _, claimID := range claimIDs {
			candidate, ok := claims[claimID]
			if !ok {
				return nil, inconsistent("knowledge omitted a requested formal claim")
			}
			for _, index := range claimIndexes[claimID] {
				result[index] = candidate
			}
		}
	}

	collections := make(map[foundation.ID]organizingapp.MaterialCandidate, collectionCount)
	for index, selector := range selectors {
		switch selector.Kind {
		case organizingdomain.MaterialSmartCollection:
			candidate, ok := collections[selector.CollectionID]
			if !ok {
				var err error
				candidate, err = adapter.resolveCollection(ctx, workspaceID, selector.CollectionID)
				if err != nil {
					return nil, err
				}
				collections[selector.CollectionID] = candidate
			}
			result[index] = candidate
		}
	}
	return result, nil
}

func documentCandidate(snapshot authoringapp.ArticleRevisionSnapshot) (organizingapp.MaterialCandidate, error) {
	document, revision := snapshot.Document, snapshot.Revision
	if document.Validate() != nil || revision.Validate() != nil || revision.DocumentID != document.ID || revision.WorkspaceID != document.WorkspaceID {
		return organizingapp.MaterialCandidate{}, inconsistent("authoring returned an invalid article revision snapshot")
	}
	availability := organizingdomain.MaterialAvailable
	if document.Lifecycle == authoringdomain.DocumentDeleted {
		availability = organizingdomain.MaterialUnavailable
	}
	reference := organizingdomain.MaterialRef{Kind: organizingdomain.MaterialDocumentRevision,
		DocumentID: document.ID, ArticleRevisionID: revision.ID,
		Version: int64(revision.RevisionNo), ContentHash: revision.ContentHash, Evidence: []organizingdomain.EvidenceRef{}}
	if reference.Validate() != nil {
		return organizingapp.MaterialCandidate{}, inconsistent("authoring revision cannot form an organizing material")
	}
	return organizingapp.MaterialCandidate{Reference: reference, Title: boundedTitle(document.Title),
		Reasons: []organizingdomain.SuggestionReasonCode{organizingdomain.ReasonUserAdded}, Origin: organizingdomain.MaterialOriginUser,
		Availability: availability, Score: 1}, nil
}

func (adapter *Adapter) loadArticleRevisions(
	ctx context.Context,
	workspaceID foundation.ID,
	identities []authoringapp.ArticleRevisionIdentity,
) (map[authoringapp.ArticleRevisionIdentity]authoringapp.ArticleRevisionSnapshot, error) {
	result := make(map[authoringapp.ArticleRevisionIdentity]authoringapp.ArticleRevisionSnapshot, len(identities))
	for start := 0; start < len(identities); start += authoringapp.MaxArticleRevisionBatchSize {
		end := start + authoringapp.MaxArticleRevisionBatchSize
		if end > len(identities) {
			end = len(identities)
		}
		batch := identities[start:end]
		items, err := adapter.dependencies.Authoring.GetArticleRevisions(ctx, authoringapp.ArticleRevisionBatchQuery{WorkspaceID: workspaceID, Items: batch})
		if err != nil {
			return nil, err
		}
		if len(items) != len(batch) {
			return nil, inconsistent("authoring returned an incomplete article revision batch")
		}
		for index, item := range items {
			identity := batch[index]
			if item.Document.Validate() != nil || item.Revision.Validate() != nil ||
				item.Document.WorkspaceID != workspaceID || item.Revision.WorkspaceID != workspaceID ||
				item.Document.ID != identity.DocumentID || item.Revision.DocumentID != identity.DocumentID || item.Revision.ID != identity.RevisionID {
				return nil, inconsistent("authoring returned an invalid article revision batch")
			}
			if _, duplicate := result[identity]; duplicate {
				return nil, inconsistent("authoring returned duplicate article revisions")
			}
			result[identity] = item
		}
	}
	return result, nil
}

func (adapter *Adapter) loadClaimCandidates(
	ctx context.Context,
	workspaceID foundation.ID,
	claimIDs []foundation.ID,
	originCollectionID foundation.ID,
) (map[foundation.ID]organizingapp.MaterialCandidate, error) {
	if len(claimIDs) == 0 {
		return map[foundation.ID]organizingapp.MaterialCandidate{}, nil
	}
	items, err := adapter.dependencies.Knowledge.GetClaims(ctx, knowledgedomain.BatchGetClaimsQuery{
		WorkspaceID: workspaceID, IDs: claimIDs,
		Statuses: []knowledgedomain.ClaimStatus{knowledgedomain.ClaimStatusConfirmed, knowledgedomain.ClaimStatusDisputed}, Limit: len(claimIDs),
	})
	if err != nil {
		return nil, err
	}
	if len(items) != len(claimIDs) {
		return nil, stale("one or more formal claims are no longer available")
	}
	requested := make(map[foundation.ID]struct{}, len(claimIDs))
	for _, claimID := range claimIDs {
		requested[claimID] = struct{}{}
	}
	provenanceQueries := make([]retrievaldomain.ProvenanceReferenceQuery, 0)
	seenProvenance := make(map[provenanceKey]struct{})
	for _, item := range items {
		if knowledgedomain.ValidateClaimAggregate(item.Claim, item.Sources) != nil || item.Claim.WorkspaceID != workspaceID || !formalClaim(item.Claim.Status) {
			return nil, inconsistent("knowledge returned an invalid formal claim")
		}
		if _, ok := requested[item.Claim.ID]; !ok {
			return nil, inconsistent("knowledge returned an out-of-scope formal claim")
		}
		for _, source := range item.Sources {
			key := provenanceKey{source.Provenance.SourceVersionID, source.Provenance.SourceSpanID}
			if _, duplicate := seenProvenance[key]; duplicate {
				continue
			}
			seenProvenance[key] = struct{}{}
			provenanceQueries = append(provenanceQueries, retrievaldomain.ProvenanceReferenceQuery{
				WorkspaceID: workspaceID, SourceVersionID: key.sourceVersionID, SourceSpanID: key.sourceSpanID,
			})
		}
	}
	if len(provenanceQueries) == 0 {
		return nil, stale("formal claims have no evidence provenance")
	}
	citations := make([]retrievaldomain.CitationReferenceQuery, 0, len(provenanceQueries))
	for start := 0; start < len(provenanceQueries); start += maxCitationBatchSize {
		end := start + maxCitationBatchSize
		if end > len(provenanceQueries) {
			end = len(provenanceQueries)
		}
		batch, resolveErr := adapter.dependencies.Evidence.ResolveProvenanceCitations(ctx, provenanceQueries[start:end])
		if resolveErr != nil {
			return nil, resolveErr
		}
		if len(batch) != end-start {
			return nil, inconsistent("retrieval returned an incomplete provenance citation batch")
		}
		citations = append(citations, batch...)
	}
	opened, err := adapter.openEvidence(ctx, workspaceID, citations)
	if err != nil {
		return nil, err
	}
	evidenceByProvenance := make(map[provenanceKey]organizingdomain.EvidenceRef, len(opened))
	for key, evidence := range opened {
		provenance := provenanceKey{key.sourceVersionID, key.sourceSpanID}
		if _, duplicate := evidenceByProvenance[provenance]; duplicate {
			return nil, inconsistent("retrieval returned multiple citations for one provenance")
		}
		evidenceByProvenance[provenance] = evidence
	}
	result := make(map[foundation.ID]organizingapp.MaterialCandidate, len(items))
	for _, item := range items {
		evidence := make([]organizingdomain.EvidenceRef, 0, len(item.Sources))
		for _, source := range item.Sources {
			key := provenanceKey{source.Provenance.SourceVersionID, source.Provenance.SourceSpanID}
			resolved, ok := evidenceByProvenance[key]
			if !ok {
				return nil, stale("formal claim provenance is not available in the active index")
			}
			evidence = appendUniqueEvidence(evidence, resolved)
		}
		if len(evidence) == 0 {
			return nil, stale("formal claim has no verifiable evidence")
		}
		reference := organizingdomain.MaterialRef{Kind: organizingdomain.MaterialClaim, ClaimID: item.Claim.ID,
			OriginCollectionID: originCollectionID, Version: item.Claim.Version, ContentHash: item.Claim.Fingerprint,
			Evidence: sortEvidence(evidence)}
		if reference.Validate() != nil {
			return nil, inconsistent("formal claim cannot form an organizing material")
		}
		if _, duplicate := result[item.Claim.ID]; duplicate {
			return nil, inconsistent("knowledge returned duplicate formal claims")
		}
		result[item.Claim.ID] = organizingapp.MaterialCandidate{Reference: reference, Title: boundedTitle(item.Claim.Statement),
			Reasons: []organizingdomain.SuggestionReasonCode{organizingdomain.ReasonFormalKnowledge}, Origin: organizingdomain.MaterialOriginUser,
			Availability: organizingdomain.MaterialAvailable, Score: 1}
	}
	return result, nil
}

func (adapter *Adapter) resolveCollection(
	ctx context.Context,
	workspaceID, collectionID foundation.ID,
) (organizingapp.MaterialCandidate, error) {
	collection, binding, err := adapter.loadCollection(ctx, workspaceID, collectionID)
	if err != nil {
		return organizingapp.MaterialCandidate{}, err
	}
	availability := organizingdomain.MaterialAvailable
	if collection.Status != collectionapp.CollectionStatusActive {
		availability = organizingdomain.MaterialUnavailable
	}
	reference := organizingdomain.MaterialRef{Kind: organizingdomain.MaterialSmartCollection, CollectionID: collection.ID,
		Version: binding.CollectionVersion, QueryHash: binding.QueryHash, ReadModelRevision: binding.ReadModelRevision,
		Evidence: []organizingdomain.EvidenceRef{}}
	if reference.Validate() != nil {
		return organizingapp.MaterialCandidate{}, inconsistent("collection binding cannot form an organizing material")
	}
	return organizingapp.MaterialCandidate{Reference: reference, Title: boundedTitle(collection.Name),
		Reasons: []organizingdomain.SuggestionReasonCode{organizingdomain.ReasonUserAdded}, Origin: organizingdomain.MaterialOriginUser,
		Availability: availability, Score: 1}, nil
}

func (adapter *Adapter) loadCollection(
	ctx context.Context,
	workspaceID, collectionID foundation.ID,
) (collectionapp.Collection, collectionapp.DurableScanBinding, error) {
	collection, err := adapter.dependencies.Collections.Get(ctx, workspaceID, collectionID)
	if err != nil {
		return collectionapp.Collection{}, collectionapp.DurableScanBinding{}, err
	}
	canonical, canonicalErr := collectiondomain.CanonicalizeQuery(collection.Query)
	if canonicalErr != nil || collection.ID != collectionID || collection.WorkspaceID != workspaceID ||
		strings.TrimSpace(collection.Name) == "" || collection.Version < 1 || collection.QueryVersion < 1 ||
		collection.QuerySchemaVersion != collectiondomain.QuerySchemaVersionV1 || collection.QueryHash != canonical.Hash ||
		!reflect.DeepEqual(collection.Query, canonical.Definition) ||
		(collection.Status != collectionapp.CollectionStatusActive && collection.Status != collectionapp.CollectionStatusArchived) {
		return collectionapp.Collection{}, collectionapp.DurableScanBinding{}, inconsistent("collection returned an invalid aggregate")
	}
	binding, err := adapter.dependencies.Collections.PlanDurableScan(ctx, workspaceID, collectionID)
	if err != nil {
		return collectionapp.Collection{}, collectionapp.DurableScanBinding{}, err
	}
	if binding.WorkspaceID != workspaceID || binding.CollectionID != collectionID || binding.CollectionVersion != collection.Version ||
		binding.CollectionVersion < 1 || binding.QueryHash != collection.QueryHash || !isHash(binding.ReadModelRevision) || binding.ExactCount < 0 {
		return collectionapp.Collection{}, collectionapp.DurableScanBinding{}, inconsistent("collection returned an inconsistent durable binding")
	}
	return collection, binding, nil
}

func (adapter *Adapter) loadSources(
	ctx context.Context,
	workspaceID foundation.ID,
	ids []foundation.ID,
) (map[foundation.ID]retrievaldomain.SourceVersionReference, error) {
	result := make(map[foundation.ID]retrievaldomain.SourceVersionReference, len(ids))
	for start := 0; start < len(ids); start += retrievalapp.MaxSourceVersionBatchSize {
		end := start + retrievalapp.MaxSourceVersionBatchSize
		if end > len(ids) {
			end = len(ids)
		}
		batch := ids[start:end]
		items, err := adapter.dependencies.Evidence.GetSourceVersions(ctx, workspaceID, batch)
		if err != nil {
			return nil, err
		}
		if len(items) != len(batch) {
			return nil, inconsistent("retrieval returned an incomplete source version batch")
		}
		for index, item := range items {
			if retrievaldomain.ValidateSourceVersionReference(item) != nil || item.WorkspaceID != workspaceID || item.SourceVersionID != batch[index] {
				return nil, inconsistent("retrieval returned an invalid source version batch")
			}
			if _, duplicate := result[item.SourceVersionID]; duplicate {
				return nil, inconsistent("retrieval returned duplicate source versions")
			}
			result[item.SourceVersionID] = item
		}
	}
	return result, nil
}

func (adapter *Adapter) loadProfiles(
	ctx context.Context,
	workspaceID foundation.ID,
	ids []foundation.ID,
) (map[foundation.ID]captureapp.ProfileView, error) {
	requested := make(map[foundation.ID]struct{}, len(ids))
	for _, id := range ids {
		requested[id] = struct{}{}
	}
	result := make(map[foundation.ID]captureapp.ProfileView, len(ids))
	for start := 0; start < len(ids); start += captureapp.MaxProfileBatchSize {
		end := start + captureapp.MaxProfileBatchSize
		if end > len(ids) {
			end = len(ids)
		}
		batch := ids[start:end]
		items, err := adapter.dependencies.Profiles.GetProfiles(ctx, captureapp.ProfileBatchQuery{WorkspaceID: workspaceID, SourceVersionIDs: batch})
		if err != nil {
			return nil, err
		}
		if len(items) > len(batch) {
			return nil, inconsistent("capture exceeded the requested profile batch")
		}
		for _, item := range items {
			if item.Profile.Validate() != nil || item.Profile.WorkspaceID != workspaceID {
				return nil, inconsistent("capture returned an invalid profile")
			}
			if _, ok := requested[item.Profile.SourceVersionID]; !ok {
				return nil, inconsistent("capture returned an out-of-scope profile")
			}
			if _, duplicate := result[item.Profile.SourceVersionID]; duplicate {
				return nil, inconsistent("capture returned duplicate profiles")
			}
			if item.Profile.CurrentRevisionID == "" {
				if item.Revision != nil || len(item.Evidence) > 0 {
					return nil, inconsistent("capture returned a revision without a current profile binding")
				}
			} else if item.Revision == nil || item.Revision.Validate() != nil || item.Revision.ID != item.Profile.CurrentRevisionID ||
				item.Revision.WorkspaceID != workspaceID || item.Revision.SourceVersionID != item.Profile.SourceVersionID {
				return nil, inconsistent("capture returned an invalid current profile revision")
			}
			result[item.Profile.SourceVersionID] = item
		}
	}
	return result, nil
}

func profileBinding(view captureapp.ProfileView) (foundation.ID, organizingdomain.MaterialAvailability) {
	if view.Revision == nil {
		return "", organizingdomain.MaterialAvailable
	}
	if view.Profile.Status == capturedomain.ProfileStatusStale {
		return view.Revision.ID, organizingdomain.MaterialStale
	}
	return view.Revision.ID, organizingdomain.MaterialAvailable
}

func sourceAvailability(source retrievaldomain.SourceVersionReference) organizingdomain.MaterialAvailability {
	if source.SecurityStatus != "passed" || source.IngestionStatus == "parse_failed" || source.IngestionStatus == "cancelled" {
		return organizingdomain.MaterialUnavailable
	}
	if source.IndexStatus == "excluded" {
		return organizingdomain.MaterialStale
	}
	if source.IndexStatus != "included" {
		return organizingdomain.MaterialUnavailable
	}
	return organizingdomain.MaterialAvailable
}

func validSelector(selector organizingapp.MaterialSelector) bool {
	source := validID(selector.SourceVersionID)
	document := validID(selector.DocumentID)
	revision := validID(selector.ArticleRevisionID)
	claim := validID(selector.ClaimID)
	collection := validID(selector.CollectionID)
	switch selector.Kind {
	case organizingdomain.MaterialSourceVersion:
		return source && selector.DocumentID == "" && selector.ArticleRevisionID == "" && selector.ClaimID == "" && selector.CollectionID == ""
	case organizingdomain.MaterialDocumentRevision:
		return document && revision && selector.SourceVersionID == "" && selector.ClaimID == "" && selector.CollectionID == ""
	case organizingdomain.MaterialClaim:
		return claim && selector.SourceVersionID == "" && selector.DocumentID == "" && selector.ArticleRevisionID == "" && selector.CollectionID == ""
	case organizingdomain.MaterialSmartCollection:
		return collection && selector.SourceVersionID == "" && selector.DocumentID == "" && selector.ArticleRevisionID == "" && selector.ClaimID == ""
	default:
		return false
	}
}

func formalClaim(status knowledgedomain.ClaimStatus) bool {
	return status == knowledgedomain.ClaimStatusConfirmed || status == knowledgedomain.ClaimStatusDisputed
}
