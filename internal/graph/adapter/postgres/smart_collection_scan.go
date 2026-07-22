package postgres

import (
	"context"
	"errors"
	"sort"
	"strings"

	collectionapp "github.com/CodeZen-Lizhi/zhixu/internal/collection/application"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	graphapp "github.com/CodeZen-Lizhi/zhixu/internal/graph/application"
	graphdomain "github.com/CodeZen-Lizhi/zhixu/internal/graph/domain"
	knowledge "github.com/CodeZen-Lizhi/zhixu/internal/knowledge/domain"
)

// SmartCollectionScanPlanner 从 Collection durable read seam 冻结启动绑定。
type SmartCollectionScanPlanner struct {
	reader graphapp.SmartCollectionScanReader
}

var _ graphapp.SemanticLinkSmartCollectionScanPlanner = (*SmartCollectionScanPlanner)(nil)

// NewSmartCollectionScanPlanner 创建 Smart Collection scan 规划 Adapter。
func NewSmartCollectionScanPlanner(reader graphapp.SmartCollectionScanReader) (*SmartCollectionScanPlanner, error) {
	if reader == nil {
		return nil, scanRepositoryUnavailable(errors.New("smart collection scan reader is missing"))
	}
	return &SmartCollectionScanPlanner{reader: reader}, nil
}

// PlanSmartCollectionScan 在 Collection 所拥有的同一快照中冻结 scan binding。
func (planner *SmartCollectionScanPlanner) PlanSmartCollectionScan(ctx context.Context, workspaceID, collectionID foundation.ID) (graphapp.SemanticLinkSmartCollectionScanPlan, error) {
	if planner == nil || planner.reader == nil {
		return graphapp.SemanticLinkSmartCollectionScanPlan{}, scanRepositoryUnavailable(errors.New("smart collection scan planner is unavailable"))
	}
	if !validID(workspaceID) || !validID(collectionID) {
		return graphapp.SemanticLinkSmartCollectionScanPlan{}, scanRepositoryInvalid(errors.New("smart collection scan identity is invalid"))
	}
	binding, err := planner.reader.PlanDurableScan(ctx, workspaceID, collectionID)
	if err != nil {
		return graphapp.SemanticLinkSmartCollectionScanPlan{}, err
	}
	if binding.WorkspaceID != workspaceID || binding.CollectionID != collectionID || binding.CollectionVersion < 1 || binding.ExactCount < 0 || !canonicalScanHash(binding.QueryHash) || !canonicalScanHash(binding.ReadModelRevision) {
		return graphapp.SemanticLinkSmartCollectionScanPlan{}, scanRepositoryConsistency(errors.New("smart collection durable scan plan is invalid"))
	}
	return graphapp.SemanticLinkSmartCollectionScanPlan{
		WorkspaceID: workspaceID, CollectionID: collectionID, CollectionVersion: binding.CollectionVersion,
		QueryHash: binding.QueryHash, ReadModelRevision: binding.ReadModelRevision, TotalNodes: binding.ExactCount,
	}, nil
}

// SmartCollectionScanPageRepository 将 durable Collection page 转成有界 discovery pair。
type SmartCollectionScanPageRepository struct {
	reader graphapp.SmartCollectionScanReader
	db     DB
}

var _ graphapp.SemanticLinkTopicScanPageSource = (*SmartCollectionScanPageRepository)(nil)

// NewSmartCollectionScanPageRepository 创建不依赖进程私有 cursor key 的 SMART_COLLECTION 页源。
func NewSmartCollectionScanPageRepository(reader graphapp.SmartCollectionScanReader, db DB) (*SmartCollectionScanPageRepository, error) {
	if reader == nil || isNilScanDependency(db) {
		return nil, scanRepositoryUnavailable(errors.New("smart collection scan page dependencies are missing"))
	}
	return &SmartCollectionScanPageRepository{reader: reader, db: db}, nil
}

// LoadPage 使用 Checkpoint.LastNode 恢复固定 binding；任何 Collection/read-model 漂移均 fail closed。
func (repository *SmartCollectionScanPageRepository) LoadPage(ctx context.Context, request graphapp.SemanticLinkTopicScanPageRequest) (graphapp.SemanticLinkTopicScanPage, error) {
	if repository == nil || repository.reader == nil || isNilScanDependency(repository.db) {
		return graphapp.SemanticLinkTopicScanPage{}, scanRepositoryUnavailable(errors.New("smart collection scan page repository is unavailable"))
	}
	if request.Scope.Type != graphdomain.SemanticLinkScanScopeSmartCollection || request.Scope.SchemaVersion != graphapp.SemanticLinkSmartCollectionScanScopeSchemaVersion || !validID(request.WorkspaceID) || !validID(request.ScanID) || request.Cursor != "" || request.TotalNodes < 0 || request.Limit < 1 || request.Limit > graphapp.MaxSemanticLinkScanPageNodes {
		return graphapp.SemanticLinkTopicScanPage{}, scanRepositoryInvalid(errors.New("smart collection scan page request is invalid"))
	}
	collectionID, err := foundation.ParseID(request.Scope.Ref)
	if err != nil || collectionID != foundation.ID(request.Scope.Ref) {
		return graphapp.SemanticLinkTopicScanPage{}, scanRepositoryInvalid(errors.New("smart collection scan collection identity is invalid"))
	}
	var after *collectionapp.DurableScanKey
	if request.LastNode != nil {
		value := collectionapp.DurableScanKey{ObjectType: string(request.LastNode.Type), ID: request.LastNode.ID}
		after = &value
	}
	page, err := repository.reader.ReadDurableScanPage(ctx, collectionapp.DurableScanPageRequest{
		Binding: collectionapp.DurableScanBinding{
			WorkspaceID: request.WorkspaceID, CollectionID: collectionID, CollectionVersion: request.Scope.Version,
			QueryHash: request.Scope.QueryHash, ReadModelRevision: request.Scope.ReadModelRevision, ExactCount: request.TotalNodes,
		},
		After: after, Limit: request.Limit, PairTargetLimit: graphapp.MaxSemanticLinkScanPagePairsPerNode,
	})
	if err != nil {
		return graphapp.SemanticLinkTopicScanPage{}, err
	}
	nodes, err := smartCollectionDiscoveryNodes(request.WorkspaceID, page.Nodes)
	if err != nil {
		return graphapp.SemanticLinkTopicScanPage{}, err
	}
	pairs := make([]graphdomain.SemanticLinkDiscoveryPair, 0, len(page.Pairs))
	for _, pair := range page.Pairs {
		source, sourceOK := nodes[pair.Source]
		target, targetOK := nodes[pair.Target]
		if !sourceOK || !targetOK {
			return graphapp.SemanticLinkTopicScanPage{}, scanRepositoryConsistency(errors.New("smart collection durable pair node is missing"))
		}
		if !source.eligible || !target.eligible {
			continue
		}
		pairs = append(pairs, graphdomain.SemanticLinkDiscoveryPair{Source: source.node, Target: target.node})
	}
	exclusions, err := loadSmartCollectionPairExclusions(ctx, repository.db, request.WorkspaceID, pairs)
	if err != nil {
		return graphapp.SemanticLinkTopicScanPage{}, err
	}
	result := graphapp.SemanticLinkTopicScanPage{
		WorkspaceID: request.WorkspaceID, ScanID: request.ScanID, ScopeVersion: request.Scope.Version,
		ScopeHash: request.Scope.QueryHash, ReadModelRevision: request.Scope.ReadModelRevision,
		Complete: page.Complete, ProcessedNodes: int64(len(page.Items)), Pairs: pairs, Exclusions: exclusions,
	}
	if len(page.Items) > 0 {
		last := page.Items[len(page.Items)-1]
		ref := knowledge.NodeRef{Type: knowledge.NodeType(last.ObjectType), ID: last.ID}
		result.LastNode = &ref
	}
	return result, nil
}

func durableScanPairKeys(pairs []collectionapp.DurableScanPair) []collectionapp.DurableScanKey {
	set := make(map[collectionapp.DurableScanKey]struct{}, len(pairs)*2)
	for _, pair := range pairs {
		set[pair.Source] = struct{}{}
		set[pair.Target] = struct{}{}
	}
	keys := make([]collectionapp.DurableScanKey, 0, len(set))
	for key := range set {
		keys = append(keys, key)
	}
	sort.Slice(keys, func(left, right int) bool {
		return keys[left].ObjectType < keys[right].ObjectType || (keys[left].ObjectType == keys[right].ObjectType && keys[left].ID < keys[right].ID)
	})
	return keys
}

type smartCollectionDiscoveryNode struct {
	node     graphdomain.SemanticLinkDiscoveryNode
	eligible bool
}

func smartCollectionDiscoveryNodes(workspaceID foundation.ID, values []collectionapp.DurableScanNode) (map[collectionapp.DurableScanKey]smartCollectionDiscoveryNode, error) {
	result := make(map[collectionapp.DurableScanKey]smartCollectionDiscoveryNode, len(values))
	for _, value := range values {
		if _, duplicate := result[value.Key]; duplicate {
			return nil, scanRepositoryConsistency(errors.New("smart collection durable node is duplicated"))
		}
		lifecycle, eligible := smartCollectionNodeLifecycle(value.Key.ObjectType, value.Status)
		entry := smartCollectionDiscoveryNode{eligible: eligible}
		if eligible {
			labels := make([]string, 0, 1+len(value.Aliases))
			labels = append(labels, boundedScanText(value.Title, 512))
			for _, alias := range value.Aliases {
				labels = append(labels, boundedScanText(alias, 512))
			}
			node := graphdomain.SemanticLinkDiscoveryNode{
				WorkspaceID: workspaceID,
				Endpoint: graphdomain.SemanticLinkCandidateEndpoint{
					Ref: knowledge.NodeRef{Type: knowledge.NodeType(value.Key.ObjectType), ID: value.Key.ID}, Version: value.Version,
					Summary: boundedScanText(value.Summary, 4096), Excerpt: boundedScanText(value.Summary, 4096),
				},
				Lifecycle: lifecycle, Labels: labels, Terms: scanTerms(value.Title + " " + value.Summary + " " + strings.Join(value.Aliases, " ")),
			}
			if node.Endpoint.Summary == "" {
				node.Endpoint.Summary = boundedScanText(value.Title, 4096)
				node.Endpoint.Excerpt = node.Endpoint.Summary
			}
			for _, topicID := range value.TopicIDs {
				node.TopicRefs = append(node.TopicRefs, knowledge.NodeRef{Type: knowledge.NodeTypeTopic, ID: topicID})
			}
			node.SourceVersionIDs = append(node.SourceVersionIDs, value.SourceVersionIDs...)
			if err := graphdomain.ValidateSemanticLinkDiscoveryNode(workspaceID, node); err != nil {
				return nil, err
			}
			entry.node = node
		}
		result[value.Key] = entry
	}
	return result, nil
}

func smartCollectionNodeLifecycle(objectType, status string) (knowledge.NodeLifecycle, bool) {
	if objectType == string(knowledge.NodeTypeTopic) {
		return knowledge.NodeLifecycleActive, status == string(knowledge.TopicStatusActive)
	}
	switch status {
	case string(knowledge.ClaimStatusSuggested):
		return knowledge.NodeLifecycleSuggested, true
	case string(knowledge.ClaimStatusConfirmed):
		return knowledge.NodeLifecycleConfirmed, true
	case string(knowledge.ClaimStatusDisputed):
		return knowledge.NodeLifecycleDisputed, true
	default:
		return "", false
	}
}

func loadSmartCollectionPairExclusions(ctx context.Context, db DB, workspaceID foundation.ID, pairs []graphdomain.SemanticLinkDiscoveryPair) ([]graphdomain.SemanticLinkDiscoveryExclusion, error) {
	if len(pairs) == 0 {
		return nil, nil
	}
	leftTypes, leftIDs := make([]string, len(pairs)), make([]string, len(pairs))
	rightTypes, rightIDs := make([]string, len(pairs)), make([]string, len(pairs))
	for index, pair := range pairs {
		leftTypes[index], leftIDs[index] = string(pair.Source.Endpoint.Ref.Type), string(pair.Source.Endpoint.Ref.ID)
		rightTypes[index], rightIDs[index] = string(pair.Target.Endpoint.Ref.Type), string(pair.Target.Endpoint.Ref.ID)
	}
	rows, err := db.Query(ctx, `
		WITH requested AS (
			SELECT input.left_type,input.left_id,input.right_type,input.right_id,input.ordinality
			FROM unnest($2::text[],$3::uuid[],$4::text[],$5::uuid[]) WITH ORDINALITY
				AS input(left_type,left_id,right_type,right_id,ordinality)
		)
		SELECT requested.left_type,requested.left_id::text,requested.right_type,requested.right_id::text,
			CASE
			WHEN EXISTS (
				SELECT 1 FROM core.relation relation
				WHERE relation.workspace_id=$1 AND relation.status IN ('CONFIRMED','STALE')
				  AND ((relation.source_node_type=requested.left_type AND relation.source_node_id=requested.left_id
				        AND relation.target_node_type=requested.right_type AND relation.target_node_id=requested.right_id)
				    OR (relation.source_node_type=requested.right_type AND relation.source_node_id=requested.right_id
				        AND relation.target_node_type=requested.left_type AND relation.target_node_id=requested.left_id))
			) THEN 'FORMAL_RELATION'
			WHEN EXISTS (
				SELECT 1
				FROM change_control.proposal proposal
				JOIN change_control.proposal_revision revision ON revision.proposal_id=proposal.id
				WHERE proposal.workspace_id=$1 AND proposal.proposal_type='knowledge_change'
				  AND proposal.status NOT IN ('rejected','needs_revision','completed','cancelled','rolled_back')
				  AND ((COALESCE(revision.change_set->'source'->>'type',revision.change_set->'source'->>'Type')=requested.left_type
				        AND COALESCE(revision.change_set->'source'->>'id',revision.change_set->'source'->>'ID')=requested.left_id::text
				        AND COALESCE(revision.change_set->'target'->>'type',revision.change_set->'target'->>'Type')=requested.right_type
				        AND COALESCE(revision.change_set->'target'->>'id',revision.change_set->'target'->>'ID')=requested.right_id::text)
				    OR (COALESCE(revision.change_set->'source'->>'type',revision.change_set->'source'->>'Type')=requested.right_type
				        AND COALESCE(revision.change_set->'source'->>'id',revision.change_set->'source'->>'ID')=requested.right_id::text
				        AND COALESCE(revision.change_set->'target'->>'type',revision.change_set->'target'->>'Type')=requested.left_type
				        AND COALESCE(revision.change_set->'target'->>'id',revision.change_set->'target'->>'ID')=requested.left_id::text))
			) THEN 'ACTIVE_PROPOSAL'
			ELSE '' END AS reason
		FROM requested
		ORDER BY requested.ordinality`, string(workspaceID), leftTypes, leftIDs, rightTypes, rightIDs)
	if err != nil {
		return nil, scanRepositoryClassify(err, "GRAPH_SEMANTIC_LINK_SCAN_EXCLUSION_QUERY_FAILED")
	}
	defer rows.Close()
	result := make([]graphdomain.SemanticLinkDiscoveryExclusion, 0)
	for rows.Next() {
		var leftType, leftID, rightType, rightID, reason string
		if err := rows.Scan(&leftType, &leftID, &rightType, &rightID, &reason); err != nil {
			return nil, scanRepositoryClassify(err, "GRAPH_SEMANTIC_LINK_SCAN_EXCLUSION_QUERY_FAILED")
		}
		if reason != "" {
			result = append(result, graphdomain.SemanticLinkDiscoveryExclusion{
				Source: knowledge.NodeRef{Type: knowledge.NodeType(leftType), ID: foundation.ID(leftID)},
				Target: knowledge.NodeRef{Type: knowledge.NodeType(rightType), ID: foundation.ID(rightID)}, Reason: reason,
			})
		}
	}
	if err := rows.Err(); err != nil {
		return nil, scanRepositoryClassify(err, "GRAPH_SEMANTIC_LINK_SCAN_EXCLUSION_QUERY_FAILED")
	}
	return result, nil
}
