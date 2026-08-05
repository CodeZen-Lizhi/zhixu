package owner

import (
	"context"
	"sort"

	authoringapp "github.com/CodeZen-Lizhi/zhixu/internal/authoring/application"
	authoringdomain "github.com/CodeZen-Lizhi/zhixu/internal/authoring/domain"
	collectionapp "github.com/CodeZen-Lizhi/zhixu/internal/collection/application"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	knowledgedomain "github.com/CodeZen-Lizhi/zhixu/internal/knowledge/domain"
	organizingdomain "github.com/CodeZen-Lizhi/zhixu/internal/organizing/domain"
)

// Freeze 重新校验精确 owner 事实并保留用户选择顺序。
func (adapter *Adapter) Freeze(
	ctx context.Context,
	workspaceID foundation.ID,
	references []organizingdomain.MaterialRef,
) ([]organizingdomain.MaterialRef, error) {
	if adapter == nil || ctx == nil || !validID(workspaceID) || len(references) == 0 || len(references) > organizingdomain.MaxSnapshotMaterials {
		return nil, invalid("organizing material freeze request is invalid")
	}
	canonical := make([]organizingdomain.MaterialRef, len(references))
	for index, reference := range references {
		value, err := organizingdomain.CanonicalMaterialRef(reference)
		if err != nil {
			return nil, err
		}
		canonical[index] = value
	}
	_, collectionCount := countSerialLookups(canonical)
	if collectionCount > maxSerialOwnerLookups {
		return nil, capabilityUnavailable("owner API does not provide a bounded batch lookup for this material kind")
	}
	for _, reference := range canonical {
		switch reference.Kind {
		case organizingdomain.MaterialSourceVersion:
			for _, evidence := range reference.Evidence {
				if evidence.SourceVersionID != reference.SourceVersionID {
					return nil, capabilityUnavailable("source material evidence belongs to another source version")
				}
			}
		case organizingdomain.MaterialDocumentRevision:
			if len(reference.Evidence) > 0 {
				return nil, capabilityUnavailable("authoring has no public document-to-citation ownership boundary")
			}
		}
	}
	if err := adapter.verifyEvidence(ctx, workspaceID, canonical); err != nil {
		return nil, err
	}

	sourceIDs := uniqueSourceIDs(canonical)
	sources := map[foundation.ID]structSource{}
	profiles := map[foundation.ID]profileState{}
	if len(sourceIDs) > 0 {
		loaded, err := adapter.loadSources(ctx, workspaceID, sourceIDs)
		if err != nil {
			return nil, err
		}
		for id, item := range loaded {
			sources[id] = structSource{contentHash: item.ContentHash, availability: sourceAvailability(item)}
		}
		loadedProfiles, err := adapter.loadProfiles(ctx, workspaceID, sourceIDs)
		if err != nil {
			return nil, err
		}
		for _, id := range sourceIDs {
			profileID, availability := profileBinding(loadedProfiles[id])
			profiles[id] = profileState{revisionID: profileID, availability: availability}
		}
	}

	claims, err := adapter.loadClaimsForFreeze(ctx, workspaceID, canonical)
	if err != nil {
		return nil, err
	}
	documentIdentities := uniqueDocumentRevisionIdentities(canonical)
	documents, err := adapter.loadArticleRevisions(ctx, workspaceID, documentIdentities)
	if err != nil {
		return nil, err
	}
	frozen := make([]organizingdomain.MaterialRef, len(canonical))
	seenFrozen := make(map[string]struct{}, len(canonical))
	for _, reference := range canonical {
		identity, _ := reference.IdentityKey()
		seenFrozen[identity] = struct{}{}
	}
	expanded := make([]organizingdomain.MaterialRef, 0)
	collections := make(map[foundation.ID]collectionFreezeState, collectionCount)
	for index, reference := range canonical {
		switch reference.Kind {
		case organizingdomain.MaterialSourceVersion:
			state, ok := sources[reference.SourceVersionID]
			if !ok {
				return nil, inconsistent("retrieval omitted a selected source version")
			}
			profile := profiles[reference.SourceVersionID]
			if state.availability != organizingdomain.MaterialAvailable || profile.availability == organizingdomain.MaterialStale ||
				state.contentHash != reference.ContentHash || profile.revisionID != reference.ProfileRevisionID {
				return nil, stale("source material changed before confirmation")
			}
			frozen[index] = reference
		case organizingdomain.MaterialDocumentRevision:
			identity := authoringapp.ArticleRevisionIdentity{DocumentID: reference.DocumentID, RevisionID: reference.ArticleRevisionID}
			snapshot, ok := documents[identity]
			if !ok {
				return nil, inconsistent("authoring omitted a selected article revision")
			}
			if snapshot.Document.Lifecycle == authoringdomain.DocumentDeleted || int64(snapshot.Revision.RevisionNo) != reference.Version ||
				snapshot.Revision.ContentHash != reference.ContentHash {
				return nil, stale("document material changed before confirmation")
			}
			frozen[index] = reference
		case organizingdomain.MaterialClaim:
			item, ok := claims[reference.ClaimID]
			if !ok {
				return nil, stale("formal claim is no longer available")
			}
			if !formalClaim(item.claim.Status) || item.claim.Version != reference.Version || item.claim.Fingerprint != reference.ContentHash {
				return nil, stale("formal claim changed before confirmation")
			}
			for _, evidence := range reference.Evidence {
				if _, ok := item.provenance[provenanceKey{evidence.SourceVersionID, evidence.SourceSpanID}]; !ok {
					return nil, stale("claim evidence is no longer owned by the claim")
				}
			}
			frozen[index] = reference
		case organizingdomain.MaterialSmartCollection:
			state, ok := collections[reference.CollectionID]
			if !ok {
				collection, binding, err := adapter.loadCollection(ctx, workspaceID, reference.CollectionID)
				if err != nil {
					return nil, err
				}
				state = collectionFreezeState{status: collection.Status, binding: binding}
				collections[reference.CollectionID] = state
			}
			if state.status != collectionapp.CollectionStatusActive || state.binding.CollectionVersion != reference.Version ||
				state.binding.QueryHash != reference.QueryHash || state.binding.ReadModelRevision != reference.ReadModelRevision {
				return nil, stale("smart collection changed before confirmation")
			}
			frozen[index] = reference
			members, err := adapter.expandCollectionClaims(ctx, workspaceID, state.binding, reference.CollectionID)
			if err != nil {
				return nil, err
			}
			for _, member := range members {
				identity, _ := member.IdentityKey()
				if _, duplicate := seenFrozen[identity]; duplicate {
					continue
				}
				if len(frozen)+len(expanded) == organizingdomain.MaxSnapshotMaterials {
					return nil, invalid("expanded collection exceeds the snapshot material bound")
				}
				seenFrozen[identity] = struct{}{}
				expanded = append(expanded, member)
			}
		}
	}
	return append(frozen, expanded...), nil
}

func (adapter *Adapter) expandCollectionClaims(
	ctx context.Context,
	workspaceID foundation.ID,
	binding collectionapp.DurableScanBinding,
	originCollectionID foundation.ID,
) ([]organizingdomain.MaterialRef, error) {
	if binding.ExactCount < 1 {
		return nil, stale("selected smart collection has no materials")
	}
	if binding.ExactCount >= int64(organizingdomain.MaxSnapshotMaterials) {
		return nil, invalid("selected smart collection exceeds the snapshot material bound")
	}
	claimIDs := make([]foundation.ID, 0, int(binding.ExactCount))
	seen := make(map[foundation.ID]struct{}, int(binding.ExactCount))
	var after *collectionapp.DurableScanKey
	for {
		page, err := adapter.dependencies.Collections.ReadDurableScanPage(ctx, collectionapp.DurableScanPageRequest{
			Binding: binding, After: after, Limit: 100, PairTargetLimit: 0,
		})
		if err != nil {
			return nil, err
		}
		if page.Binding != binding || page.Complete != (page.Next == nil) || len(page.Items) == 0 && !page.Complete {
			return nil, inconsistent("collection returned an invalid durable member page")
		}
		for _, item := range page.Items {
			if item.ObjectType != "CLAIM" {
				return nil, capabilityUnavailable("smart collection contains Topic members that cannot form evidence-bound materials")
			}
			if !validID(item.ID) || (item.Status != string(knowledgedomain.ClaimStatusConfirmed) && item.Status != string(knowledgedomain.ClaimStatusDisputed)) {
				return nil, stale("smart collection contains a non-formal claim")
			}
			if _, duplicate := seen[item.ID]; duplicate {
				return nil, inconsistent("collection durable scan returned a duplicate member")
			}
			seen[item.ID] = struct{}{}
			claimIDs = append(claimIDs, item.ID)
		}
		if page.Complete {
			break
		}
		if page.Next == nil {
			return nil, inconsistent("collection durable scan omitted its next key")
		}
		next := *page.Next
		after = &next
		if len(claimIDs) > organizingdomain.MaxSnapshotMaterials {
			return nil, invalid("selected smart collection exceeds the snapshot material bound")
		}
	}
	if int64(len(claimIDs)) != binding.ExactCount {
		return nil, stale("smart collection member count changed before confirmation")
	}
	candidates, err := adapter.loadClaimCandidates(ctx, workspaceID, claimIDs, originCollectionID)
	if err != nil {
		return nil, err
	}
	result := make([]organizingdomain.MaterialRef, len(claimIDs))
	for index, claimID := range claimIDs {
		candidate, ok := candidates[claimID]
		if !ok {
			return nil, inconsistent("formal claim candidate is missing after collection expansion")
		}
		result[index] = candidate.Reference
	}
	return result, nil
}

func uniqueDocumentRevisionIdentities(references []organizingdomain.MaterialRef) []authoringapp.ArticleRevisionIdentity {
	seen := make(map[authoringapp.ArticleRevisionIdentity]struct{})
	result := make([]authoringapp.ArticleRevisionIdentity, 0)
	for _, reference := range references {
		if reference.Kind != organizingdomain.MaterialDocumentRevision {
			continue
		}
		identity := authoringapp.ArticleRevisionIdentity{DocumentID: reference.DocumentID, RevisionID: reference.ArticleRevisionID}
		if _, exists := seen[identity]; exists {
			continue
		}
		seen[identity] = struct{}{}
		result = append(result, identity)
	}
	return result
}

type structSource struct {
	contentHash  string
	availability organizingdomain.MaterialAvailability
}

type profileState struct {
	revisionID   foundation.ID
	availability organizingdomain.MaterialAvailability
}

type claimState struct {
	claim      knowledgedomain.Claim
	provenance map[provenanceKey]struct{}
}

type collectionFreezeState struct {
	status  collectionapp.CollectionStatus
	binding collectionapp.DurableScanBinding
}

func uniqueSourceIDs(references []organizingdomain.MaterialRef) []foundation.ID {
	seen := make(map[foundation.ID]struct{})
	result := make([]foundation.ID, 0)
	for _, reference := range references {
		if reference.Kind != organizingdomain.MaterialSourceVersion {
			continue
		}
		if _, exists := seen[reference.SourceVersionID]; !exists {
			seen[reference.SourceVersionID] = struct{}{}
			result = append(result, reference.SourceVersionID)
		}
	}
	return result
}

func countSerialLookups(references []organizingdomain.MaterialRef) (int, int) {
	documents, collections := map[foundation.ID]struct{}{}, map[foundation.ID]struct{}{}
	for _, reference := range references {
		if reference.Kind == organizingdomain.MaterialDocumentRevision {
			documents[reference.DocumentID] = struct{}{}
		}
		if reference.Kind == organizingdomain.MaterialSmartCollection {
			collections[reference.CollectionID] = struct{}{}
		}
	}
	return len(documents), len(collections)
}

func (adapter *Adapter) loadClaimsForFreeze(
	ctx context.Context,
	workspaceID foundation.ID,
	references []organizingdomain.MaterialRef,
) (map[foundation.ID]claimState, error) {
	ids := make([]foundation.ID, 0)
	seen := make(map[foundation.ID]struct{})
	for _, reference := range references {
		if reference.Kind == organizingdomain.MaterialClaim {
			if _, exists := seen[reference.ClaimID]; !exists {
				seen[reference.ClaimID] = struct{}{}
				ids = append(ids, reference.ClaimID)
			}
		}
	}
	if len(ids) == 0 {
		return map[foundation.ID]claimState{}, nil
	}
	items, err := adapter.dependencies.Knowledge.GetClaims(ctx, knowledgedomain.BatchGetClaimsQuery{WorkspaceID: workspaceID, IDs: ids, Limit: len(ids)})
	if err != nil {
		return nil, err
	}
	result := make(map[foundation.ID]claimState, len(items))
	for _, item := range items {
		if knowledgedomain.ValidateClaimAggregate(item.Claim, item.Sources) != nil || item.Claim.WorkspaceID != workspaceID {
			return nil, inconsistent("knowledge returned an invalid claim aggregate")
		}
		if _, requested := seen[item.Claim.ID]; !requested {
			return nil, inconsistent("knowledge returned an out-of-scope claim")
		}
		if _, duplicate := result[item.Claim.ID]; duplicate {
			return nil, inconsistent("knowledge returned duplicate claims")
		}
		provenance := make(map[provenanceKey]struct{}, len(item.Sources))
		for _, source := range item.Sources {
			provenance[provenanceKey{source.Provenance.SourceVersionID, source.Provenance.SourceSpanID}] = struct{}{}
		}
		result[item.Claim.ID] = claimState{claim: item.Claim, provenance: provenance}
	}
	return result, nil
}

func sortEvidence(values []organizingdomain.EvidenceRef) []organizingdomain.EvidenceRef {
	result := append([]organizingdomain.EvidenceRef(nil), values...)
	sort.Slice(result, func(i, j int) bool {
		left, right := result[i], result[j]
		if left.IndexVersionID != right.IndexVersionID {
			return left.IndexVersionID < right.IndexVersionID
		}
		if left.ChunkID != right.ChunkID {
			return left.ChunkID < right.ChunkID
		}
		if left.SourceVersionID != right.SourceVersionID {
			return left.SourceVersionID < right.SourceVersionID
		}
		return left.SourceSpanID < right.SourceSpanID
	})
	return result
}
