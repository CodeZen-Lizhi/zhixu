package owner

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"testing"
	"time"

	authoringapp "github.com/CodeZen-Lizhi/zhixu/internal/authoring/application"
	authoringdomain "github.com/CodeZen-Lizhi/zhixu/internal/authoring/domain"
	captureapp "github.com/CodeZen-Lizhi/zhixu/internal/capture/application"
	capturedomain "github.com/CodeZen-Lizhi/zhixu/internal/capture/domain"
	collectionapp "github.com/CodeZen-Lizhi/zhixu/internal/collection/application"
	collectiondomain "github.com/CodeZen-Lizhi/zhixu/internal/collection/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	graphdomain "github.com/CodeZen-Lizhi/zhixu/internal/graph/domain"
	knowledgeapp "github.com/CodeZen-Lizhi/zhixu/internal/knowledge/application"
	knowledgedomain "github.com/CodeZen-Lizhi/zhixu/internal/knowledge/domain"
	organizingapp "github.com/CodeZen-Lizhi/zhixu/internal/organizing/application"
	organizingdomain "github.com/CodeZen-Lizhi/zhixu/internal/organizing/domain"
	retrievalapp "github.com/CodeZen-Lizhi/zhixu/internal/retrieval/application"
	retrievaldomain "github.com/CodeZen-Lizhi/zhixu/internal/retrieval/domain"
)

var ownerTestNow = time.Date(2026, 8, 3, 8, 0, 0, 0, time.UTC)

func TestNewRequiresEveryOwnerBoundary(t *testing.T) {
	t.Parallel()
	fake := newOwnerFake()
	dependencies := Dependencies{Search: fake, Evidence: fake, Profiles: fake, Knowledge: fake, Collections: fake, Authoring: fake, Claims: fake}
	if _, err := New(dependencies); err != nil {
		t.Fatal(err)
	}
	dependencies.Knowledge = nil
	assertOwnerError(t, mustOwnerError(New(dependencies)), foundation.ErrorDependencyUnavailable, ErrorCodeOwnerDependencyUnavailable)
	var typedNil *ownerFake
	dependencies.Knowledge = typedNil
	assertOwnerError(t, mustOwnerError(New(dependencies)), foundation.ErrorDependencyUnavailable, ErrorCodeOwnerDependencyUnavailable)
}

func TestVerifyFrozenRequiresCallerOwnedPostgreSQLTransaction(t *testing.T) {
	t.Parallel()
	adapter := mustOwnerAdapter(t, newOwnerFake())
	err := adapter.VerifyFrozen(context.Background(), struct{}{}, ownerID(1), []organizingdomain.MaterialRef{{
		Kind:            organizingdomain.MaterialSourceVersion,
		SourceVersionID: ownerID(10),
		ContentHash:     ownerHash(10),
		Evidence:        []organizingdomain.EvidenceRef{},
	}})
	assertOwnerError(t, err, foundation.ErrorInvalidInput, organizingapp.ErrorCodeRequestInvalid)
}

func TestSuggestHydratesProfileAndFormalClaimWithExactEvidence(t *testing.T) {
	t.Parallel()
	workspaceID, sourceVersionID := ownerID(1), ownerID(10)
	indexVersionID, chunkID, spanID := ownerID(20), ownerID(21), ownerID(22)
	fake := newOwnerFake()
	fake.sources[sourceVersionID] = sourceReference(workspaceID, sourceVersionID, 10)
	fake.search = searchResult(workspaceID, indexVersionID, chunkID, spanID, sourceVersionID)
	fake.profiles[sourceVersionID] = profileView(t, workspaceID, sourceVersionID, indexVersionID, spanID, 30)
	fake.claims = []knowledgedomain.ClaimWithSources{claimWithSource(t, workspaceID, sourceVersionID, spanID, 40)}
	adapter := mustOwnerAdapter(t, fake)

	result, err := adapter.Suggest(context.Background(), organizingapp.SuggestionQuery{
		WorkspaceID: workspaceID, Intent: "Golang concurrency", Limit: 5,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(result) != 2 || result[0].Reference.Kind != organizingdomain.MaterialClaim ||
		result[1].Reference.Kind != organizingdomain.MaterialSourceVersion {
		t.Fatalf("suggestions=%#v", result)
	}
	claim, source := result[0], result[1]
	if claim.Reasons[0] != organizingdomain.ReasonFormalKnowledge || claim.Reference.ContentHash != fake.claims[0].Claim.Fingerprint ||
		claim.Reference.Version != fake.claims[0].Claim.Version || len(claim.Reference.Evidence) != 1 {
		t.Fatalf("claim=%#v", claim)
	}
	profileRevisionID := fake.profiles[sourceVersionID].Revision.ID
	if source.Reference.ProfileRevisionID != profileRevisionID || len(source.Reference.Evidence) != 1 ||
		source.Reference.Evidence[0].ContentHash != fake.sources[sourceVersionID].ContentHash ||
		source.Reference.Evidence[0].ExcerptHash != ownerHash(22) ||
		!sameReasons(source.Reasons, organizingdomain.ReasonHybridMatch, organizingdomain.ReasonProfileMatch, organizingdomain.ReasonAliasMatch) {
		t.Fatalf("source=%#v", source)
	}
	if fake.searchCalls != 1 || len(fake.sourceBatches) != 1 || len(fake.profileBatches) != 1 ||
		fake.knowledgeCalls != 1 || len(fake.citationBatches) != 1 || fake.searchRequest.WorkspaceID != workspaceID {
		t.Fatalf("calls search=%d source=%#v profile=%#v knowledge=%d citation=%#v",
			fake.searchCalls, fake.sourceBatches, fake.profileBatches, fake.knowledgeCalls, fake.citationBatches)
	}
}

func TestResolveUsesPublicSourceDocumentAndCollectionOwners(t *testing.T) {
	t.Parallel()
	workspaceID := ownerID(1)
	sourceVersionID, documentID, revisionID, collectionID := ownerID(10), ownerID(20), ownerID(21), ownerID(30)
	fake := newOwnerFake()
	fake.sources[sourceVersionID] = sourceReference(workspaceID, sourceVersionID, 10)
	fake.documents[documentID] = documentDetail(workspaceID, documentID, revisionID, 20)
	fake.collections[collectionID], fake.collectionBindings[collectionID] = collectionFixture(t, workspaceID, collectionID, 30)
	adapter := mustOwnerAdapter(t, fake)

	result, err := adapter.Resolve(context.Background(), workspaceID, []organizingapp.MaterialSelector{
		{Kind: organizingdomain.MaterialSourceVersion, SourceVersionID: sourceVersionID},
		{Kind: organizingdomain.MaterialDocumentRevision, DocumentID: documentID, ArticleRevisionID: revisionID},
		{Kind: organizingdomain.MaterialSmartCollection, CollectionID: collectionID},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(result) != 3 || result[0].Reference.SourceVersionID != sourceVersionID ||
		result[1].Reference.ArticleRevisionID != revisionID || result[1].Reference.Version != 1 ||
		result[2].Reference.CollectionID != collectionID || result[2].Reference.ReadModelRevision != fake.collectionBindings[collectionID].ReadModelRevision {
		t.Fatalf("resolved=%#v", result)
	}
	if fake.documentCalls != 1 || fake.collectionGetCalls != 1 || fake.collectionPlanCalls != 1 {
		t.Fatalf("document=%d collection get/plan=%d/%d", fake.documentCalls, fake.collectionGetCalls, fake.collectionPlanCalls)
	}
}

func TestSearchMaterialsUsesBoundedWorkspaceOwnerIdentities(t *testing.T) {
	t.Parallel()
	workspaceID := ownerID(1)
	t.Run("source version", func(t *testing.T) {
		fake := newOwnerFake()
		sourceVersionID := ownerID(10)
		fake.search = searchResult(workspaceID, ownerID(11), ownerID(12), ownerID(13), sourceVersionID)
		fake.sources[sourceVersionID] = sourceReference(workspaceID, sourceVersionID, 10)
		hits, err := mustOwnerAdapter(t, fake).SearchMaterials(context.Background(), organizingapp.MaterialSearchQuery{
			WorkspaceID: workspaceID, Query: "Go", Kind: organizingdomain.MaterialSourceVersion, Limit: 5,
		})
		if err != nil || len(hits) != 1 || hits[0].Selector.SourceVersionID != sourceVersionID || hits[0].Selector.Kind != organizingdomain.MaterialSourceVersion {
			t.Fatalf("source hits=%#v err=%v", hits, err)
		}
		if fake.searchRequest.WorkspaceID != workspaceID || fake.searchRequest.Limit != 20 || len(fake.sourceBatches) != 1 {
			t.Fatalf("source owner calls request=%#v batches=%#v", fake.searchRequest, fake.sourceBatches)
		}
	})

	t.Run("document revision", func(t *testing.T) {
		fake := newOwnerFake()
		documentID, revisionID := ownerID(20), ownerID(21)
		detail := documentDetail(workspaceID, documentID, revisionID, 20)
		fake.documentSearch = []authoringapp.ArticleRevisionSearchHit{{
			Document: detail.Document, RevisionID: revisionID, RevisionNo: detail.CurrentRevision.RevisionNo, ContentHash: detail.CurrentRevision.ContentHash,
		}}
		hits, err := mustOwnerAdapter(t, fake).SearchMaterials(context.Background(), organizingapp.MaterialSearchQuery{
			WorkspaceID: workspaceID, Query: "Article", Kind: organizingdomain.MaterialDocumentRevision, Limit: 5,
		})
		if err != nil || len(hits) != 1 || hits[0].Selector.DocumentID != documentID || hits[0].Selector.ArticleRevisionID != revisionID {
			t.Fatalf("document hits=%#v err=%v", hits, err)
		}
	})

	t.Run("claim", func(t *testing.T) {
		fake := newOwnerFake()
		claim := claimWithSource(t, workspaceID, ownerID(30), ownerID(31), 32).Claim
		fake.claimSearch = graphdomain.NodeSearchResult{WorkspaceID: workspaceID, Matches: []graphdomain.NodeSearchMatch{{
			Kind: graphdomain.NodeSearchExact, Node: graphdomain.GraphNode{Claim: &graphdomain.ClaimNode{
				Ref: knowledgedomain.NodeRef{Type: knowledgedomain.NodeTypeClaim, ID: claim.ID}, WorkspaceID: workspaceID,
				Statement: claim.Statement, Status: claim.Status, Confidence: claim.ConfidenceScore,
				Applicability: claim.Applicability, Version: claim.Version, UpdatedAt: claim.UpdatedAt,
			}},
		}}}
		hits, err := mustOwnerAdapter(t, fake).SearchMaterials(context.Background(), organizingapp.MaterialSearchQuery{
			WorkspaceID: workspaceID, Query: "Claim", Kind: organizingdomain.MaterialClaim, Limit: 5,
		})
		if err != nil || len(hits) != 1 || hits[0].Selector.ClaimID != claim.ID {
			t.Fatalf("claim hits=%#v err=%v", hits, err)
		}
	})

	t.Run("smart collection", func(t *testing.T) {
		fake := newOwnerFake()
		collectionID := ownerID(40)
		item, _ := collectionFixture(t, workspaceID, collectionID, 40)
		fake.collectionSearch = []collectionapp.Collection{item}
		hits, err := mustOwnerAdapter(t, fake).SearchMaterials(context.Background(), organizingapp.MaterialSearchQuery{
			WorkspaceID: workspaceID, Query: "Formal", Kind: organizingdomain.MaterialSmartCollection, Limit: 5,
		})
		if err != nil || len(hits) != 1 || hits[0].Selector.CollectionID != collectionID {
			t.Fatalf("collection hits=%#v err=%v", hits, err)
		}
	})
}

func TestSearchMaterialsRejectsCrossWorkspaceOwnerResult(t *testing.T) {
	t.Parallel()
	workspaceID, otherWorkspaceID := ownerID(1), ownerID(2)
	fake := newOwnerFake()
	detail := documentDetail(otherWorkspaceID, ownerID(20), ownerID(21), 20)
	fake.documentSearch = []authoringapp.ArticleRevisionSearchHit{{Document: detail.Document, RevisionID: detail.CurrentRevision.ID,
		RevisionNo: detail.CurrentRevision.RevisionNo, ContentHash: detail.CurrentRevision.ContentHash}}
	_, err := mustOwnerAdapter(t, fake).SearchMaterials(context.Background(), organizingapp.MaterialSearchQuery{
		WorkspaceID: workspaceID, Query: "Article", Kind: organizingdomain.MaterialDocumentRevision, Limit: 5,
	})
	assertOwnerError(t, err, foundation.ErrorConsistencyViolation, ErrorCodeOwnerResultInvalid)
}

func TestResolveRejectsUnprovableClaimAndSupportsHistoricalDocumentRevision(t *testing.T) {
	t.Parallel()
	workspaceID, claimID := ownerID(1), ownerID(10)
	fake := newOwnerFake()
	adapter := mustOwnerAdapter(t, fake)

	_, err := adapter.Resolve(context.Background(), workspaceID, []organizingapp.MaterialSelector{{Kind: organizingdomain.MaterialClaim, ClaimID: claimID}})
	assertOwnerError(t, err, foundation.ErrorVersionConflict, organizingapp.ErrorCodeMaterialStale)
	if fake.knowledgeCalls != 1 {
		t.Fatalf("manual claim owner calls=%d", fake.knowledgeCalls)
	}

	documentID, currentRevisionID, historicalRevisionID := ownerID(20), ownerID(21), ownerID(22)
	fake.documents[documentID] = documentDetail(workspaceID, documentID, currentRevisionID, 20)
	historical := documentDetail(workspaceID, documentID, historicalRevisionID, 22)
	fake.articleRevisions[authoringapp.ArticleRevisionIdentity{DocumentID: documentID, RevisionID: historicalRevisionID}] = authoringapp.ArticleRevisionSnapshot{
		Document: fake.documents[documentID].Document, Revision: *historical.CurrentRevision,
	}
	resolved, err := adapter.Resolve(context.Background(), workspaceID, []organizingapp.MaterialSelector{{
		Kind: organizingdomain.MaterialDocumentRevision, DocumentID: documentID, ArticleRevisionID: historicalRevisionID,
	}})
	if err != nil || len(resolved) != 1 || resolved[0].Reference.ArticleRevisionID != historicalRevisionID {
		t.Fatalf("historical revision resolve=%#v err=%v", resolved, err)
	}
	_, err = adapter.Resolve(context.Background(), workspaceID, []organizingapp.MaterialSelector{{
		Kind: organizingdomain.MaterialDocumentRevision, DocumentID: documentID, ArticleRevisionID: ownerID(23),
	}})
	assertOwnerError(t, err, foundation.ErrorDependencyUnavailable, ErrorCodeOwnerCapabilityUnavailable)
}

func TestDocumentContentReaderOpensExactHistoricalRevisionAndRejectsHashDrift(t *testing.T) {
	t.Parallel()
	workspaceID, documentID, revisionID := ownerID(1), ownerID(30), ownerID(31)
	detail := documentDetail(workspaceID, documentID, revisionID, 30)
	fake := newOwnerFake()
	fake.articleRevisions[authoringapp.ArticleRevisionIdentity{DocumentID: documentID, RevisionID: revisionID}] = authoringapp.ArticleRevisionSnapshot{
		Document: detail.Document, Revision: *detail.CurrentRevision,
	}
	reader, err := NewDocumentContentReader(fake)
	if err != nil {
		t.Fatal(err)
	}
	reference := organizingdomain.MaterialRef{
		Kind: organizingdomain.MaterialDocumentRevision, DocumentID: documentID, ArticleRevisionID: revisionID,
		Version: int64(detail.CurrentRevision.RevisionNo), ContentHash: detail.CurrentRevision.ContentHash, Evidence: []organizingdomain.EvidenceRef{},
	}
	opened, err := reader.OpenFrozenDocumentContents(context.Background(), workspaceID, []organizingdomain.MaterialRef{reference})
	if err != nil {
		t.Fatal(err)
	}
	if len(opened) != 1 || opened[0].DocumentID != documentID || opened[0].ArticleRevisionID != revisionID ||
		opened[0].Content != detail.CurrentRevision.Content || opened[0].ContentHash != reference.ContentHash {
		t.Fatalf("opened=%+v", opened)
	}

	drifted := reference
	drifted.ContentHash = ownerHash(999)
	_, err = reader.OpenFrozenDocumentContents(context.Background(), workspaceID, []organizingdomain.MaterialRef{drifted})
	assertOwnerError(t, err, foundation.ErrorConsistencyViolation, ErrorCodeOwnerResultInvalid)
}

func TestSuggestRejectsCrossWorkspaceOwnerResult(t *testing.T) {
	t.Parallel()
	workspaceID, otherWorkspaceID := ownerID(1), ownerID(2)
	fake := newOwnerFake()
	fake.search = searchResult(otherWorkspaceID, ownerID(20), ownerID(21), ownerID(22), ownerID(10))
	adapter := mustOwnerAdapter(t, fake)

	_, err := adapter.Suggest(context.Background(), organizingapp.SuggestionQuery{WorkspaceID: workspaceID, Intent: "isolated", Limit: 5})
	assertOwnerError(t, err, foundation.ErrorConsistencyViolation, ErrorCodeOwnerResultInvalid)
	if len(fake.sourceBatches) != 0 || len(fake.citationBatches) != 0 {
		t.Fatalf("cross-workspace search triggered hydration: sources=%d citations=%d", len(fake.sourceBatches), len(fake.citationBatches))
	}
}

func TestFreezeRevalidatesSourceProfileClaimAndCitation(t *testing.T) {
	t.Parallel()
	workspaceID, sourceVersionID := ownerID(1), ownerID(10)
	indexVersionID, chunkID, spanID := ownerID(20), ownerID(21), ownerID(22)
	fake := newOwnerFake()
	fake.sources[sourceVersionID] = sourceReference(workspaceID, sourceVersionID, 10)
	fake.search = searchResult(workspaceID, indexVersionID, chunkID, spanID, sourceVersionID)
	fake.profiles[sourceVersionID] = profileView(t, workspaceID, sourceVersionID, indexVersionID, spanID, 30)
	fake.claims = []knowledgedomain.ClaimWithSources{claimWithSource(t, workspaceID, sourceVersionID, spanID, 40)}
	adapter := mustOwnerAdapter(t, fake)
	suggestions, err := adapter.Suggest(context.Background(), organizingapp.SuggestionQuery{WorkspaceID: workspaceID, Intent: "Golang", Limit: 5})
	if err != nil {
		t.Fatal(err)
	}
	references := []organizingdomain.MaterialRef{suggestions[1].Reference, suggestions[0].Reference}

	frozen, err := adapter.Freeze(context.Background(), workspaceID, references)
	if err != nil {
		t.Fatal(err)
	}
	if len(frozen) != 2 || frozen[0].SourceVersionID != sourceVersionID || frozen[1].ClaimID != fake.claims[0].Claim.ID {
		t.Fatalf("frozen=%#v", frozen)
	}

	source := fake.sources[sourceVersionID]
	source.ContentHash = ownerHash(999)
	fake.sources[sourceVersionID] = source
	_, err = adapter.Freeze(context.Background(), workspaceID, references)
	assertOwnerError(t, err, foundation.ErrorVersionConflict, organizingapp.ErrorCodeMaterialStale)
}

func TestFreezeRejectsClaimEvidenceNoLongerOwned(t *testing.T) {
	t.Parallel()
	workspaceID, sourceVersionID := ownerID(1), ownerID(10)
	indexVersionID, chunkID, spanID := ownerID(20), ownerID(21), ownerID(22)
	fake := newOwnerFake()
	fake.sources[sourceVersionID] = sourceReference(workspaceID, sourceVersionID, 10)
	fake.search = searchResult(workspaceID, indexVersionID, chunkID, spanID, sourceVersionID)
	fake.claims = []knowledgedomain.ClaimWithSources{claimWithSource(t, workspaceID, sourceVersionID, spanID, 40)}
	adapter := mustOwnerAdapter(t, fake)
	suggestions, err := adapter.Suggest(context.Background(), organizingapp.SuggestionQuery{WorkspaceID: workspaceID, Intent: "claim", Limit: 5})
	if err != nil {
		t.Fatal(err)
	}
	claimRef := suggestions[0].Reference

	changed := claimWithSource(t, workspaceID, ownerID(11), ownerID(23), 40)
	changed.Claim = fake.claims[0].Claim
	changed.Sources[0].ClaimID = changed.Claim.ID
	changed.Sources[0].WorkspaceID = workspaceID
	changed.Sources[0].Provenance.WorkspaceID = workspaceID
	changed.Sources[0].EvidenceHash = knowledgedomain.ComputeClaimSourceEvidenceHash(changed.Sources[0], changed.Claim.Applicability)
	fake.claims = []knowledgedomain.ClaimWithSources{changed}
	_, err = adapter.Freeze(context.Background(), workspaceID, []organizingdomain.MaterialRef{claimRef})
	assertOwnerError(t, err, foundation.ErrorVersionConflict, organizingapp.ErrorCodeMaterialStale)
}

func TestFreezeRejectsProfileRevisionDrift(t *testing.T) {
	t.Parallel()
	workspaceID, sourceVersionID := ownerID(1), ownerID(10)
	indexVersionID, chunkID, spanID := ownerID(20), ownerID(21), ownerID(22)
	fake := newOwnerFake()
	fake.sources[sourceVersionID] = sourceReference(workspaceID, sourceVersionID, 10)
	fake.search = searchResult(workspaceID, indexVersionID, chunkID, spanID, sourceVersionID)
	fake.profiles[sourceVersionID] = profileView(t, workspaceID, sourceVersionID, indexVersionID, spanID, 30)
	adapter := mustOwnerAdapter(t, fake)
	suggestions, err := adapter.Suggest(context.Background(), organizingapp.SuggestionQuery{WorkspaceID: workspaceID, Intent: "Golang", Limit: 5})
	if err != nil {
		t.Fatal(err)
	}
	sourceRef := suggestions[0].Reference
	fake.profiles[sourceVersionID] = profileView(t, workspaceID, sourceVersionID, indexVersionID, spanID, 31)

	_, err = adapter.Freeze(context.Background(), workspaceID, []organizingdomain.MaterialRef{sourceRef})
	assertOwnerError(t, err, foundation.ErrorVersionConflict, organizingapp.ErrorCodeMaterialStale)
}

func TestFreezeChunksSourceAndProfileHydrationAtOwnerLimit(t *testing.T) {
	t.Parallel()
	workspaceID := ownerID(1)
	fake := newOwnerFake()
	references := make([]organizingdomain.MaterialRef, 101)
	for index := range references {
		sourceVersionID := ownerID(1000 + index)
		fake.sources[sourceVersionID] = sourceReference(workspaceID, sourceVersionID, 1000+index)
		references[index] = organizingdomain.MaterialRef{Kind: organizingdomain.MaterialSourceVersion,
			SourceVersionID: sourceVersionID, ContentHash: fake.sources[sourceVersionID].ContentHash, Evidence: []organizingdomain.EvidenceRef{}}
	}
	adapter := mustOwnerAdapter(t, fake)
	result, err := adapter.Freeze(context.Background(), workspaceID, references)
	if err != nil {
		t.Fatal(err)
	}
	if len(result) != len(references) || len(fake.sourceBatches) != 2 || len(fake.sourceBatches[0]) != 100 || len(fake.sourceBatches[1]) != 1 ||
		len(fake.profileBatches) != 2 || len(fake.profileBatches[0]) != 100 || len(fake.profileBatches[1]) != 1 {
		t.Fatalf("result=%d source batches=%v profile batches=%v", len(result), batchLengths(fake.sourceBatches), batchLengths(fake.profileBatches))
	}
}

func TestFreezeRejectsStaleCollectionBinding(t *testing.T) {
	t.Parallel()
	workspaceID, collectionID := ownerID(1), ownerID(30)
	fake := newOwnerFake()
	fake.collections[collectionID], fake.collectionBindings[collectionID] = collectionFixture(t, workspaceID, collectionID, 30)
	adapter := mustOwnerAdapter(t, fake)
	resolved, err := adapter.Resolve(context.Background(), workspaceID, []organizingapp.MaterialSelector{{Kind: organizingdomain.MaterialSmartCollection, CollectionID: collectionID}})
	if err != nil {
		t.Fatal(err)
	}
	binding := fake.collectionBindings[collectionID]
	binding.ReadModelRevision = ownerHash(999)
	fake.collectionBindings[collectionID] = binding
	_, err = adapter.Freeze(context.Background(), workspaceID, []organizingdomain.MaterialRef{resolved[0].Reference})
	assertOwnerError(t, err, foundation.ErrorVersionConflict, organizingapp.ErrorCodeMaterialStale)
}

func TestFreezeExpandsClaimCollectionIntoEvidenceBoundSnapshotMaterials(t *testing.T) {
	t.Parallel()
	workspaceID, collectionID := ownerID(1), ownerID(30)
	sourceVersionID, spanID := ownerID(40), ownerID(41)
	fake := newOwnerFake()
	fake.sources[sourceVersionID] = sourceReference(workspaceID, sourceVersionID, 40)
	claim := claimWithSource(t, workspaceID, sourceVersionID, spanID, 50)
	fake.claims = []knowledgedomain.ClaimWithSources{claim}
	fake.collections[collectionID], fake.collectionBindings[collectionID] = collectionFixture(t, workspaceID, collectionID, 30)
	binding := fake.collectionBindings[collectionID]
	binding.ExactCount = 1
	fake.collectionBindings[collectionID] = binding
	fake.collectionItems[collectionID] = []collectionapp.CollectionItem{{
		ObjectType: "CLAIM", ID: claim.Claim.ID, Title: claim.Claim.Statement, Summary: claim.Claim.Statement,
		Status: string(claim.Claim.Status), CreatedAt: claim.Claim.CreatedAt, UpdatedAt: claim.Claim.UpdatedAt,
	}}
	adapter := mustOwnerAdapter(t, fake)
	resolved, err := adapter.Resolve(context.Background(), workspaceID, []organizingapp.MaterialSelector{{Kind: organizingdomain.MaterialSmartCollection, CollectionID: collectionID}})
	if err != nil {
		t.Fatal(err)
	}
	frozen, err := adapter.Freeze(context.Background(), workspaceID, []organizingdomain.MaterialRef{resolved[0].Reference})
	if err != nil {
		t.Fatal(err)
	}
	if len(frozen) != 2 || frozen[0].Kind != organizingdomain.MaterialSmartCollection || frozen[1].ClaimID != claim.Claim.ID ||
		frozen[1].OriginCollectionID != collectionID || len(frozen[1].Evidence) != 1 || frozen[1].Evidence[0].SourceSpanID != spanID {
		t.Fatalf("expanded collection=%#v", frozen)
	}
}

type ownerFake struct {
	search             retrievaldomain.SearchResult
	sources            map[foundation.ID]retrievaldomain.SourceVersionReference
	profiles           map[foundation.ID]captureapp.ProfileView
	claims             []knowledgedomain.ClaimWithSources
	documents          map[foundation.ID]authoringapp.DocumentDetail
	articleRevisions   map[authoringapp.ArticleRevisionIdentity]authoringapp.ArticleRevisionSnapshot
	collections        map[foundation.ID]collectionapp.Collection
	collectionBindings map[foundation.ID]collectionapp.DurableScanBinding
	collectionItems    map[foundation.ID][]collectionapp.CollectionItem
	documentSearch     []authoringapp.ArticleRevisionSearchHit
	collectionSearch   []collectionapp.Collection
	claimSearch        graphdomain.NodeSearchResult

	searchRequest       retrievaldomain.SearchRequest
	searchCalls         int
	sourceBatches       [][]foundation.ID
	profileBatches      [][]foundation.ID
	citationBatches     [][]retrievaldomain.CitationReferenceQuery
	knowledgeQueries    []knowledgeapp.GetClaimsQuery
	knowledgeCalls      int
	documentCalls       int
	collectionGetCalls  int
	collectionPlanCalls int
}

func newOwnerFake() *ownerFake {
	return &ownerFake{sources: map[foundation.ID]retrievaldomain.SourceVersionReference{}, profiles: map[foundation.ID]captureapp.ProfileView{},
		documents: map[foundation.ID]authoringapp.DocumentDetail{}, articleRevisions: map[authoringapp.ArticleRevisionIdentity]authoringapp.ArticleRevisionSnapshot{}, collections: map[foundation.ID]collectionapp.Collection{},
		collectionBindings: map[foundation.ID]collectionapp.DurableScanBinding{}, collectionItems: map[foundation.ID][]collectionapp.CollectionItem{}}
}

func (fake *ownerFake) Search(_ context.Context, request retrievaldomain.SearchRequest) (retrievaldomain.SearchResult, error) {
	fake.searchCalls++
	fake.searchRequest = request
	return fake.search, nil
}

func (fake *ownerFake) GetSourceVersions(_ context.Context, workspaceID foundation.ID, ids []foundation.ID) ([]retrievaldomain.SourceVersionReference, error) {
	fake.sourceBatches = append(fake.sourceBatches, append([]foundation.ID(nil), ids...))
	result := make([]retrievaldomain.SourceVersionReference, len(ids))
	for index, id := range ids {
		item, ok := fake.sources[id]
		if !ok || item.WorkspaceID != workspaceID {
			return nil, errors.New("source not found")
		}
		result[index] = item
	}
	return result, nil
}

func (fake *ownerFake) OpenCitationEvidenceBatch(_ context.Context, queries []retrievaldomain.CitationReferenceQuery) ([]retrievalapp.OpenedCitationEvidence, error) {
	fake.citationBatches = append(fake.citationBatches, append([]retrievaldomain.CitationReferenceQuery(nil), queries...))
	result := make([]retrievalapp.OpenedCitationEvidence, len(queries))
	for index, query := range queries {
		source, ok := fake.sources[query.SourceVersionID]
		if !ok || source.WorkspaceID != query.WorkspaceID {
			return nil, errors.New("citation source not found")
		}
		result[index] = retrievalapp.OpenedCitationEvidence{Query: query, View: retrievalapp.SourceSpanView{Reference: retrievaldomain.SourceSpanReference{
			SourceVersion: source, ParseProjectionID: ownerID(8000 + index),
			Span:     retrievaldomain.EvidenceSpan{ID: query.SourceSpanID, StartLine: 1, EndLine: 1, StartByte: 0, EndByte: 10},
			SpanType: "section", Selector: json.RawMessage(`{"heading":["Owner"]}`), ExcerptHash: hashForID(query.SourceSpanID),
			ParserVersion: "goldmark-v1", SchemaVersion: "parse-v1",
		}}}
	}
	sort.Slice(result, func(i, j int) bool {
		return citationKeyFor(result[i].Query).chunkID > citationKeyFor(result[j].Query).chunkID
	})
	return result, nil
}

func (fake *ownerFake) ResolveProvenanceCitations(_ context.Context, queries []retrievaldomain.ProvenanceReferenceQuery) ([]retrievaldomain.CitationReferenceQuery, error) {
	result := make([]retrievaldomain.CitationReferenceQuery, len(queries))
	for index, query := range queries {
		if _, ok := fake.sources[query.SourceVersionID]; !ok {
			return nil, errors.New("provenance source not found")
		}
		result[index] = retrievaldomain.CitationReferenceQuery{
			WorkspaceID: query.WorkspaceID, IndexVersionID: ownerID(12000 + index), ChunkID: ownerID(13000 + index),
			SourceVersionID: query.SourceVersionID, SourceSpanID: query.SourceSpanID,
		}
	}
	return result, nil
}

func (fake *ownerFake) GetProfiles(_ context.Context, query captureapp.ProfileBatchQuery) ([]captureapp.ProfileView, error) {
	fake.profileBatches = append(fake.profileBatches, append([]foundation.ID(nil), query.SourceVersionIDs...))
	result := make([]captureapp.ProfileView, 0, len(query.SourceVersionIDs))
	for _, id := range query.SourceVersionIDs {
		if item, ok := fake.profiles[id]; ok {
			result = append(result, item)
		}
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Profile.SourceVersionID < result[j].Profile.SourceVersionID })
	return result, nil
}

func (fake *ownerFake) GetClaims(_ context.Context, query knowledgeapp.GetClaimsQuery) ([]knowledgedomain.ClaimWithSources, error) {
	fake.knowledgeCalls++
	fake.knowledgeQueries = append(fake.knowledgeQueries, query)
	ids := make(map[foundation.ID]struct{}, len(query.IDs))
	for _, id := range query.IDs {
		ids[id] = struct{}{}
	}
	statuses := make(map[knowledgedomain.ClaimStatus]struct{}, len(query.Statuses))
	for _, status := range query.Statuses {
		statuses[status] = struct{}{}
	}
	result := make([]knowledgedomain.ClaimWithSources, 0, len(fake.claims))
	for _, item := range fake.claims {
		if item.Claim.WorkspaceID != query.WorkspaceID {
			continue
		}
		if len(ids) > 0 {
			if _, ok := ids[item.Claim.ID]; !ok {
				continue
			}
		}
		if len(statuses) > 0 {
			if _, ok := statuses[item.Claim.Status]; !ok {
				continue
			}
		}
		result = append(result, item)
	}
	if len(result) > query.Limit {
		result = result[:query.Limit]
	}
	return result, nil
}

func (fake *ownerFake) GetArticleRevisions(_ context.Context, query authoringapp.ArticleRevisionBatchQuery) ([]authoringapp.ArticleRevisionSnapshot, error) {
	fake.documentCalls++
	result := make([]authoringapp.ArticleRevisionSnapshot, len(query.Items))
	for index, identity := range query.Items {
		if item, ok := fake.articleRevisions[identity]; ok {
			if item.Document.WorkspaceID != query.WorkspaceID {
				return nil, errors.New("document not found")
			}
			result[index] = item
			continue
		}
		detail, ok := fake.documents[identity.DocumentID]
		if !ok || detail.Document.WorkspaceID != query.WorkspaceID || detail.CurrentRevision == nil || detail.CurrentRevision.ID != identity.RevisionID {
			return nil, capabilityUnavailable("article revision not found")
		}
		result[index] = authoringapp.ArticleRevisionSnapshot{Document: detail.Document, Revision: *detail.CurrentRevision}
	}
	return result, nil
}

func (fake *ownerFake) SearchArticleRevisions(_ context.Context, _ authoringapp.ArticleRevisionSearchQuery) ([]authoringapp.ArticleRevisionSearchHit, error) {
	return append([]authoringapp.ArticleRevisionSearchHit(nil), fake.documentSearch...), nil
}

func (fake *ownerFake) Get(_ context.Context, workspaceID, collectionID foundation.ID) (collectionapp.Collection, error) {
	fake.collectionGetCalls++
	item, ok := fake.collections[collectionID]
	if !ok || item.WorkspaceID != workspaceID {
		return collectionapp.Collection{}, errors.New("collection not found")
	}
	return item, nil
}

func (fake *ownerFake) PlanDurableScan(_ context.Context, workspaceID, collectionID foundation.ID) (collectionapp.DurableScanBinding, error) {
	fake.collectionPlanCalls++
	item, ok := fake.collectionBindings[collectionID]
	if !ok || item.WorkspaceID != workspaceID {
		return collectionapp.DurableScanBinding{}, errors.New("collection binding not found")
	}
	return item, nil
}

func (fake *ownerFake) ReadDurableScanPage(_ context.Context, request collectionapp.DurableScanPageRequest) (collectionapp.DurableScanPage, error) {
	binding, ok := fake.collectionBindings[request.Binding.CollectionID]
	if !ok || binding != request.Binding {
		return collectionapp.DurableScanPage{}, errors.New("collection scan binding not found")
	}
	items := fake.collectionItems[binding.CollectionID]
	start := 0
	if request.After != nil {
		start = len(items)
		for index, item := range items {
			if item.ObjectType == request.After.ObjectType && item.ID == request.After.ID {
				start = index + 1
				break
			}
		}
	}
	end := start + request.Limit
	if end > len(items) {
		end = len(items)
	}
	page := collectionapp.DurableScanPage{Binding: binding, Items: append([]collectionapp.CollectionItem(nil), items[start:end]...), Complete: end == len(items)}
	if !page.Complete && len(page.Items) > 0 {
		last := page.Items[len(page.Items)-1]
		page.Next = &collectionapp.DurableScanKey{ObjectType: last.ObjectType, ID: last.ID}
	}
	return page, nil
}

func (fake *ownerFake) SearchCollections(_ context.Context, _ collectionapp.CollectionSearchQuery) ([]collectionapp.Collection, error) {
	return append([]collectionapp.Collection(nil), fake.collectionSearch...), nil
}

func (fake *ownerFake) SearchNodes(_ context.Context, request graphdomain.NodeSearchRequest) (graphdomain.NodeSearchResult, error) {
	if fake.claimSearch.WorkspaceID == "" {
		return graphdomain.NodeSearchResult{WorkspaceID: request.WorkspaceID, Matches: []graphdomain.NodeSearchMatch{}}, nil
	}
	return fake.claimSearch, nil
}

func mustOwnerAdapter(t *testing.T, fake *ownerFake) *Adapter {
	t.Helper()
	adapter, err := New(Dependencies{Search: fake, Evidence: fake, Profiles: fake, Knowledge: fake, Collections: fake, Authoring: fake, Claims: fake})
	if err != nil {
		t.Fatal(err)
	}
	return adapter
}

func searchResult(workspaceID, indexVersionID, chunkID, spanID, sourceVersionID foundation.ID) retrievaldomain.SearchResult {
	fts, trigram := 0.5, 0.25
	return retrievaldomain.SearchResult{WorkspaceID: workspaceID, IndexVersionID: indexVersionID,
		RequestedMode: retrievaldomain.SearchModeHybrid, EffectiveMode: retrievaldomain.SearchModeKeyword,
		Degradations: []retrievaldomain.SearchDegradation{{Capability: retrievaldomain.SearchDegradationVector, Code: "RETRIEVAL_VECTOR_UNAVAILABLE"}},
		Items: []retrievaldomain.EvidenceV1{{WorkspaceID: workspaceID, IndexVersionID: indexVersionID,
			ChunkID: chunkID, ParseProjectionID: ownerID(23), Sequence: 0, ContentHash: ownerHash(21),
			HeadingPath: []string{"Concurrency"}, Span: retrievaldomain.EvidenceSpan{ID: spanID, StartLine: 1, EndLine: 1, StartByte: 0, EndByte: 10},
			Snippet: "Golang concurrency uses evidence.", Provenances: []retrievaldomain.EvidenceProvenance{{
				SourceID: ownerID(11), SourceVersionID: sourceVersionID, RelativePath: "docs/go.md", CapturedAt: ownerTestNow,
			}}, Lexical: &retrievaldomain.CandidateStageScore{Rank: 1, Score: 1}, LexicalFTSScore: &fts,
			LexicalTrigramScore: &trigram, Fusion: retrievaldomain.CandidateStageScore{Rank: 1, Score: 1}}}}
}

func sourceReference(workspaceID, sourceVersionID foundation.ID, seed int) retrievaldomain.SourceVersionReference {
	return retrievaldomain.SourceVersionReference{WorkspaceID: workspaceID, SourceID: ownerID(10000 + seed), SourceVersionID: sourceVersionID,
		ContentArtifactID: ownerID(11000 + seed), SourceType: "local_file", LogicalName: fmt.Sprintf("source-%d.md", seed),
		RelativePath: fmt.Sprintf("docs/source-%d.md", seed), ContentHash: ownerHash(seed), ByteSize: 128,
		MediaType: "text/markdown", SecurityStatus: "passed", IngestionStatus: "parsed", WorkflowStatus: "succeeded",
		IndexStatus: "included", CapturedAt: ownerTestNow}
}

func profileView(t *testing.T, workspaceID, sourceVersionID, indexVersionID, spanID foundation.ID, seed int) captureapp.ProfileView {
	t.Helper()
	content := capturedomain.ProfileContent{Summary: "Go concurrency profile", Topics: []capturedomain.ProfileCandidate{{
		Label: "Go", Aliases: []string{"Golang"}, SourceSpanIDs: []foundation.ID{spanID},
	}}, KnowledgePoints: []capturedomain.ProfilePoint{{Text: "Use evidence-backed concurrency.", SourceSpanIDs: []foundation.ID{spanID}}}}
	digest, err := capturedomain.ComputeProfileDigest(content)
	if err != nil {
		t.Fatal(err)
	}
	revisionID := ownerID(12000 + seed)
	revision := capturedomain.ProfileRevision{ID: revisionID, ProfileID: ownerID(12100 + seed), WorkspaceID: workspaceID,
		SourceVersionID: sourceVersionID, ParseProjectionID: ownerID(12200 + seed), IndexVersionID: indexVersionID,
		ModelRunID: ownerID(12300 + seed), PromptVersion: "profile-v1", SchemaVersion: capturedomain.ProfileSchemaVersion,
		Content: content, ContentDigest: digest, CreatedAt: ownerTestNow}
	profile := capturedomain.Profile{ID: revision.ProfileID, WorkspaceID: workspaceID, CaptureID: ownerID(12400 + seed),
		SourceVersionID: sourceVersionID, CurrentRevisionID: revision.ID, Status: capturedomain.ProfileStatusReady,
		Version: 1, CreatedAt: ownerTestNow, UpdatedAt: ownerTestNow}
	return captureapp.ProfileView{Profile: profile, Revision: &revision, Evidence: []capturedomain.ProfileEvidence{{
		RevisionID: revision.ID, WorkspaceID: workspaceID, SourceVersionID: sourceVersionID, SourceSpanID: spanID, CreatedAt: ownerTestNow,
	}}}
}

func claimWithSource(t *testing.T, workspaceID, sourceVersionID, spanID foundation.ID, seed int) knowledgedomain.ClaimWithSources {
	t.Helper()
	applicability, err := knowledgedomain.ParseApplicability(json.RawMessage(`{"region":"global"}`))
	if err != nil {
		t.Fatal(err)
	}
	statement, normalized, err := knowledgedomain.NormalizeStatement("Golang concurrency must remain evidence-backed.")
	if err != nil {
		t.Fatal(err)
	}
	claim := knowledgedomain.Claim{ID: ownerID(13000 + seed), WorkspaceID: workspaceID, Statement: statement,
		NormalizedStatement: normalized, Applicability: applicability, Status: knowledgedomain.ClaimStatusConfirmed,
		ConfidenceFactors: json.RawMessage(`{}`), Version: 3, CreatedAt: ownerTestNow, UpdatedAt: ownerTestNow}
	claim.Fingerprint = knowledgedomain.ComputeClaimFingerprint(workspaceID, normalized, applicability)
	source := knowledgedomain.ClaimSource{ID: ownerID(13100 + seed), WorkspaceID: workspaceID, ClaimID: claim.ID,
		Provenance:  knowledgedomain.ProvenanceRef{WorkspaceID: workspaceID, SourceVersionID: sourceVersionID, SourceSpanID: spanID},
		SupportType: knowledgedomain.ClaimSupportSupports, Reason: "The source directly supports the claim.", CreatedAt: ownerTestNow}
	source.EvidenceHash = knowledgedomain.ComputeClaimSourceEvidenceHash(source, applicability)
	return knowledgedomain.ClaimWithSources{Claim: claim, Sources: []knowledgedomain.ClaimSource{source}}
}

func documentDetail(workspaceID, documentID, revisionID foundation.ID, seed int) authoringapp.DocumentDetail {
	content := "# Article\n\nEvidence-backed content.\n"
	document := authoringdomain.Document{ID: documentID, WorkspaceID: workspaceID, CanonicalPath: fmt.Sprintf("docs/article-%d.md", seed),
		Title: "Evidence-backed article", Lifecycle: authoringdomain.DocumentDraft, Version: 1, CreatedAt: ownerTestNow, UpdatedAt: ownerTestNow}
	revision := authoringdomain.ArticleRevision{ID: revisionID, WorkspaceID: workspaceID, DocumentID: documentID, RevisionNo: 1,
		Content: content, ContentHash: authoringdomain.ComputeContentHash(content), Status: authoringdomain.RevisionDraft,
		OptimizationMode: "NONE", CreatedByType: "USER", CreatedAt: ownerTestNow}
	return authoringapp.DocumentDetail{Document: document, CurrentRevision: &revision}
}

func collectionFixture(t *testing.T, workspaceID, collectionID foundation.ID, seed int) (collectionapp.Collection, collectionapp.DurableScanBinding) {
	t.Helper()
	query := collectiondomain.Query{SchemaVersion: collectiondomain.QuerySchemaVersionV1, Root: collectiondomain.Clause{
		Kind: collectiondomain.ClauseKindGroup, Operator: string(collectiondomain.GroupOperatorAND), Clauses: []collectiondomain.Clause{{
			Kind: collectiondomain.ClauseKindPredicate, Field: "object_type", Operator: string(collectiondomain.OperatorEQ), Value: json.RawMessage(`"CLAIM"`),
		}},
	}}
	canonical, err := collectiondomain.CanonicalizeQuery(query)
	if err != nil {
		t.Fatal(err)
	}
	collection := collectionapp.Collection{ID: collectionID, WorkspaceID: workspaceID, Name: "Formal claims", NormalizedName: "formal claims",
		QuerySchemaVersion: collectiondomain.QuerySchemaVersionV1, QueryVersion: 1, Query: canonical.Definition, QueryHash: canonical.Hash,
		Status: collectionapp.CollectionStatusActive, Version: 2, CreatedAt: ownerTestNow, UpdatedAt: ownerTestNow}
	binding := collectionapp.DurableScanBinding{WorkspaceID: workspaceID, CollectionID: collectionID, CollectionVersion: collection.Version,
		QueryHash: canonical.Hash, ReadModelRevision: ownerHash(seed), ExactCount: 2}
	return collection, binding
}

func sameReasons(actual []organizingdomain.SuggestionReasonCode, expected ...organizingdomain.SuggestionReasonCode) bool {
	return fmt.Sprint(actual) == fmt.Sprint(expected)
}

func batchLengths(batches [][]foundation.ID) []int {
	result := make([]int, len(batches))
	for index := range batches {
		result[index] = len(batches[index])
	}
	return result
}

func hashForID(id foundation.ID) string {
	text := string(id)
	marker := text[len(text)-12:]
	marker = strings.TrimLeft(marker, "0")
	if marker == "" {
		return ownerHash(0)
	}
	var value int
	_, _ = fmt.Sscanf(marker, "%d", &value)
	return ownerHash(value)
}

func ownerHash(seed int) string { return fmt.Sprintf("%064x", seed) }

func ownerID(seed int) foundation.ID {
	return foundation.ID(fmt.Sprintf("91000000-0000-4000-8000-%012d", seed))
}

func assertOwnerError(t *testing.T, err error, kind foundation.ErrorKind, code string) {
	t.Helper()
	var classified *foundation.Error
	if !errors.As(err, &classified) || classified.Kind != kind || classified.Code != code {
		t.Fatalf("error=%#v want kind=%s code=%s", err, kind, code)
	}
}

func mustOwnerError(_ *Adapter, err error) error { return err }
