package owner

import (
	"context"
	"strings"

	authoringapp "github.com/CodeZen-Lizhi/zhixu/internal/authoring/application"
	authoringdomain "github.com/CodeZen-Lizhi/zhixu/internal/authoring/domain"
	collectionapp "github.com/CodeZen-Lizhi/zhixu/internal/collection/application"
	collectiondomain "github.com/CodeZen-Lizhi/zhixu/internal/collection/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	graphdomain "github.com/CodeZen-Lizhi/zhixu/internal/graph/domain"
	organizingapp "github.com/CodeZen-Lizhi/zhixu/internal/organizing/application"
	organizingdomain "github.com/CodeZen-Lizhi/zhixu/internal/organizing/domain"
	retrievaldomain "github.com/CodeZen-Lizhi/zhixu/internal/retrieval/domain"
)

// SearchMaterials searches one owner read model and returns identity-only results for a later AddMaterial command.
func (adapter *Adapter) SearchMaterials(
	ctx context.Context,
	query organizingapp.MaterialSearchQuery,
) ([]organizingapp.MaterialSearchHit, error) {
	if adapter == nil || ctx == nil || !validID(query.WorkspaceID) || strings.TrimSpace(query.Query) != query.Query ||
		len([]byte(query.Query)) < 2 || len([]byte(query.Query)) > organizingapp.MaxMaterialSearchQueryBytes ||
		query.Limit < 1 || query.Limit > organizingapp.MaxMaterialSearchLimit {
		return nil, invalid("organizing material search request is invalid")
	}
	switch query.Kind {
	case organizingdomain.MaterialSourceVersion:
		return adapter.searchSourceMaterials(ctx, query)
	case organizingdomain.MaterialDocumentRevision:
		return adapter.searchDocumentMaterials(ctx, query)
	case organizingdomain.MaterialClaim:
		return adapter.searchClaimMaterials(ctx, query)
	case organizingdomain.MaterialSmartCollection:
		return adapter.searchCollectionMaterials(ctx, query)
	default:
		return nil, invalid("organizing material search kind is invalid")
	}
}

func (adapter *Adapter) searchSourceMaterials(ctx context.Context, query organizingapp.MaterialSearchQuery) ([]organizingapp.MaterialSearchHit, error) {
	searchLimit := query.Limit * 4
	if searchLimit > int(retrievaldomain.MaxSearchLimit) {
		searchLimit = int(retrievaldomain.MaxSearchLimit)
	}
	request := retrievaldomain.SearchRequest{WorkspaceID: query.WorkspaceID, Query: query.Query,
		Mode: retrievaldomain.SearchModeHybrid, Limit: int32(searchLimit)}
	result, err := adapter.dependencies.Search.Search(ctx, request)
	if err != nil {
		return nil, err
	}
	if retrievaldomain.ValidateSearchResult(request, result) != nil {
		return nil, inconsistent("retrieval returned an invalid material search result")
	}
	ids := make([]foundation.ID, 0, query.Limit)
	seen := make(map[foundation.ID]struct{}, query.Limit)
	for _, item := range result.Items {
		for _, provenance := range item.Provenances {
			if _, duplicate := seen[provenance.SourceVersionID]; duplicate {
				continue
			}
			seen[provenance.SourceVersionID] = struct{}{}
			ids = append(ids, provenance.SourceVersionID)
			if len(ids) == query.Limit {
				break
			}
		}
		if len(ids) == query.Limit {
			break
		}
	}
	if len(ids) == 0 {
		return []organizingapp.MaterialSearchHit{}, nil
	}
	sources, err := adapter.loadSources(ctx, query.WorkspaceID, ids)
	if err != nil {
		return nil, err
	}
	hits := make([]organizingapp.MaterialSearchHit, len(ids))
	for index, id := range ids {
		source := sources[id]
		hits[index] = organizingapp.MaterialSearchHit{
			Selector: organizingapp.MaterialSelector{Kind: organizingdomain.MaterialSourceVersion, SourceVersionID: id},
			Title:    boundedTitle(source.LogicalName), Availability: sourceAvailability(source),
		}
	}
	return hits, nil
}

func (adapter *Adapter) searchDocumentMaterials(ctx context.Context, query organizingapp.MaterialSearchQuery) ([]organizingapp.MaterialSearchHit, error) {
	items, err := adapter.dependencies.Authoring.SearchArticleRevisions(ctx, authoringapp.ArticleRevisionSearchQuery{
		WorkspaceID: query.WorkspaceID, Query: query.Query, Limit: query.Limit,
	})
	if err != nil {
		return nil, err
	}
	if len(items) > query.Limit {
		return nil, inconsistent("authoring exceeded the material search bound")
	}
	hits := make([]organizingapp.MaterialSearchHit, len(items))
	for index, item := range items {
		if item.Document.WorkspaceID != query.WorkspaceID || item.Document.Lifecycle == authoringdomain.DocumentDeleted ||
			!validID(item.Document.ID) || !validID(item.RevisionID) || strings.TrimSpace(item.Document.Title) == "" {
			return nil, inconsistent("authoring returned an invalid material search hit")
		}
		hits[index] = organizingapp.MaterialSearchHit{
			Selector: organizingapp.MaterialSelector{Kind: organizingdomain.MaterialDocumentRevision,
				DocumentID: item.Document.ID, ArticleRevisionID: item.RevisionID},
			Title: boundedTitle(item.Document.Title), Availability: organizingdomain.MaterialAvailable,
		}
	}
	return hits, nil
}

func (adapter *Adapter) searchClaimMaterials(ctx context.Context, query organizingapp.MaterialSearchQuery) ([]organizingapp.MaterialSearchHit, error) {
	requestLimit := query.Limit * 2
	if requestLimit > graphdomain.MaxSearchLimit {
		requestLimit = graphdomain.MaxSearchLimit
	}
	request := graphdomain.NodeSearchRequest{WorkspaceID: query.WorkspaceID, Query: query.Query, Limit: requestLimit}
	result, err := adapter.dependencies.Claims.SearchNodes(ctx, request)
	if err != nil {
		return nil, err
	}
	if graphdomain.ValidateNodeSearchResult(request, result) != nil {
		return nil, inconsistent("graph returned an invalid Claim search result")
	}
	hits := make([]organizingapp.MaterialSearchHit, 0, query.Limit)
	for _, match := range result.Matches {
		if match.Node.Claim == nil {
			continue
		}
		claim := match.Node.Claim
		hits = append(hits, organizingapp.MaterialSearchHit{
			Selector: organizingapp.MaterialSelector{Kind: organizingdomain.MaterialClaim, ClaimID: claim.Ref.ID},
			Title:    boundedTitle(claim.Statement), Availability: organizingdomain.MaterialAvailable,
		})
		if len(hits) == query.Limit {
			break
		}
	}
	return hits, nil
}

func (adapter *Adapter) searchCollectionMaterials(ctx context.Context, query organizingapp.MaterialSearchQuery) ([]organizingapp.MaterialSearchHit, error) {
	items, err := adapter.dependencies.Collections.SearchCollections(ctx, collectionapp.CollectionSearchQuery{
		WorkspaceID: query.WorkspaceID, Query: query.Query, Limit: query.Limit,
	})
	if err != nil {
		return nil, err
	}
	if len(items) > query.Limit {
		return nil, inconsistent("collection exceeded the material search bound")
	}
	hits := make([]organizingapp.MaterialSearchHit, len(items))
	seen := make(map[foundation.ID]struct{}, len(items))
	for index, item := range items {
		canonical, canonicalErr := collectiondomain.CanonicalizeQuery(item.Query)
		if canonicalErr != nil || item.WorkspaceID != query.WorkspaceID || item.Status != collectionapp.CollectionStatusActive ||
			!validID(item.ID) || item.Version < 1 || item.QueryHash != canonical.Hash || strings.TrimSpace(item.Name) == "" {
			return nil, inconsistent("collection returned an invalid material search hit")
		}
		if _, duplicate := seen[item.ID]; duplicate {
			return nil, inconsistent("collection returned duplicate material search hits")
		}
		seen[item.ID] = struct{}{}
		hits[index] = organizingapp.MaterialSearchHit{
			Selector: organizingapp.MaterialSelector{Kind: organizingdomain.MaterialSmartCollection, CollectionID: item.ID},
			Title:    boundedTitle(item.Name), Availability: organizingdomain.MaterialAvailable,
		}
	}
	return hits, nil
}
