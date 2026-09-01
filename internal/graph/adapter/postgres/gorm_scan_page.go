package postgres

import (
	"context"
	"errors"
	"sort"

	collectionapp "github.com/CodeZen-Lizhi/zhixu/internal/collection/application"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	graphapp "github.com/CodeZen-Lizhi/zhixu/internal/graph/application"
	graphdomain "github.com/CodeZen-Lizhi/zhixu/internal/graph/domain"
	knowledge "github.com/CodeZen-Lizhi/zhixu/internal/knowledge/domain"
	platformpostgres "github.com/CodeZen-Lizhi/zhixu/internal/platform/postgres"
	"gorm.io/gorm"
)

const gormTopicPairExclusionsSQL = `
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
			  AND ((relation.source_node_type=requested.left_type AND relation.source_node_id=requested.left_id AND relation.target_node_type=requested.right_type AND relation.target_node_id=requested.right_id)
			    OR (relation.source_node_type=requested.right_type AND relation.source_node_id=requested.right_id AND relation.target_node_type=requested.left_type AND relation.target_node_id=requested.left_id))
		) THEN 'FORMAL_RELATION'
		WHEN EXISTS (
			SELECT 1
			FROM change_control.proposal proposal
			JOIN change_control.proposal_revision revision ON revision.proposal_id=proposal.id
			WHERE proposal.workspace_id=$1 AND proposal.proposal_type='knowledge_change'
			  AND proposal.status NOT IN ('rejected','needs_revision','completed','cancelled','rolled_back')
			  AND ((COALESCE(revision.change_set->'source'->>'Type',revision.change_set->'source'->>'type')=requested.left_type
			        AND COALESCE(revision.change_set->'source'->>'ID',revision.change_set->'source'->>'id')=requested.left_id::text
			        AND COALESCE(revision.change_set->'target'->>'Type',revision.change_set->'target'->>'type')=requested.right_type
			        AND COALESCE(revision.change_set->'target'->>'ID',revision.change_set->'target'->>'id')=requested.right_id::text)
			    OR (COALESCE(revision.change_set->'source'->>'Type',revision.change_set->'source'->>'type')=requested.right_type
			        AND COALESCE(revision.change_set->'source'->>'ID',revision.change_set->'source'->>'id')=requested.right_id::text
			        AND COALESCE(revision.change_set->'target'->>'Type',revision.change_set->'target'->>'type')=requested.left_type
			        AND COALESCE(revision.change_set->'target'->>'ID',revision.change_set->'target'->>'id')=requested.left_id::text))
		) THEN 'ACTIVE_PROPOSAL'
		ELSE '' END AS reason
	FROM requested
	ORDER BY requested.ordinality`

const gormSmartCollectionPairExclusionsSQL = `
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
	ORDER BY requested.ordinality`

type gormPairExclusionQuery uint8

const (
	gormPairExclusionQueryTopic gormPairExclusionQuery = iota + 1
	gormPairExclusionQuerySmartCollection
)

// GORMSemanticLinkTopicScanPlanner reads the Topic plan through the shared
// Graph GORM root. The plan is one statement, so it needs no owned snapshot.
type GORMSemanticLinkTopicScanPlanner struct {
	repository *GORMRepository
}

var _ graphapp.SemanticLinkTopicScanPlanner = (*GORMSemanticLinkTopicScanPlanner)(nil)

// NewGORMSemanticLinkTopicScanPlanner constructs the staged Topic planner.
func NewGORMSemanticLinkTopicScanPlanner(pool *platformpostgres.Pool) (*GORMSemanticLinkTopicScanPlanner, error) {
	if pool == nil {
		return nil, scanRepositoryUnavailable(errors.New("semantic link GORM scan planner pool is missing"))
	}
	repository, err := NewGORMRepository(pool)
	if err != nil {
		return nil, err
	}
	return &GORMSemanticLinkTopicScanPlanner{repository: repository}, nil
}

// PlanTopicScan preserves the legacy aggregate SQL and strict projection.
func (planner *GORMSemanticLinkTopicScanPlanner) PlanTopicScan(ctx context.Context, workspaceID, topicID foundation.ID) (graphapp.SemanticLinkTopicScanPlan, error) {
	if planner == nil || planner.repository == nil {
		return graphapp.SemanticLinkTopicScanPlan{}, scanRepositoryUnavailable(errors.New("semantic link GORM scan planner is unavailable"))
	}
	if ctx == nil {
		return graphapp.SemanticLinkTopicScanPlan{}, scanRepositoryInvalid(errors.New("semantic link GORM scan context is nil"))
	}
	if !validID(workspaceID) || !validID(topicID) {
		return graphapp.SemanticLinkTopicScanPlan{}, scanRepositoryInvalid(errors.New("semantic link topic scan identity is invalid"))
	}
	database, err := gormScanRootDB(ctx, planner.repository)
	if err != nil {
		return graphapp.SemanticLinkTopicScanPlan{}, err
	}
	row, err := gormRawRow(ctx, database, `
		SELECT topic.version,count(DISTINCT claim.id)
		FROM core.topic topic
		LEFT JOIN core.relation relation
		  ON relation.workspace_id=topic.workspace_id
		 AND relation.target_node_type='TOPIC' AND relation.target_node_id=topic.id
		 AND relation.source_node_type='CLAIM' AND relation.relation_type='BELONGS_TO'
		 AND relation.status='CONFIRMED'
		LEFT JOIN core.claim claim
		  ON claim.workspace_id=topic.workspace_id AND claim.id=relation.source_node_id
		 AND claim.status IN ('CONFIRMED','DISPUTED')
		WHERE topic.workspace_id=$1 AND topic.id=$2 AND topic.status='ACTIVE'
		GROUP BY topic.version`, string(workspaceID), string(topicID))
	if err != nil {
		return graphapp.SemanticLinkTopicScanPlan{}, classifyGORMScan(ctx, err, "GRAPH_SEMANTIC_LINK_SCAN_PLAN_FAILED")
	}
	var version, total int64
	if err := row.Scan(&version, &total); gormGraphNoRows(err) {
		return graphapp.SemanticLinkTopicScanPlan{}, notFound(graphdomain.ErrorCodeSemanticLinkScanNotFound, err)
	} else if err != nil {
		return graphapp.SemanticLinkTopicScanPlan{}, classifyGORMScan(ctx, err, "GRAPH_SEMANTIC_LINK_SCAN_PLAN_FAILED")
	}
	return graphapp.SemanticLinkTopicScanPlan{WorkspaceID: workspaceID, TopicID: topicID, TopicVersion: version, TotalNodes: total}, nil
}

// GORMSemanticLinkTopicScanPageRepository executes all Topic page statements
// in one repeatable-read, read-only snapshot.
type GORMSemanticLinkTopicScanPageRepository struct {
	repository *GORMRepository
}

var _ graphapp.SemanticLinkTopicScanPageSource = (*GORMSemanticLinkTopicScanPageRepository)(nil)

// NewGORMSemanticLinkTopicScanPageRepository constructs the staged Topic page source.
func NewGORMSemanticLinkTopicScanPageRepository(pool *platformpostgres.Pool) (*GORMSemanticLinkTopicScanPageRepository, error) {
	if pool == nil {
		return nil, scanRepositoryUnavailable(errors.New("semantic link GORM Topic page pool is missing"))
	}
	repository, err := NewGORMRepository(pool)
	if err != nil {
		return nil, err
	}
	return &GORMSemanticLinkTopicScanPageRepository{repository: repository}, nil
}

// LoadPage keeps Topic version, source page, LATERAL pairs, target hydration
// and batched exclusions inside one transaction-local 1.5 second timeout.
func (repository *GORMSemanticLinkTopicScanPageRepository) LoadPage(ctx context.Context, request graphapp.SemanticLinkTopicScanPageRequest) (graphapp.SemanticLinkTopicScanPage, error) {
	if repository == nil || repository.repository == nil {
		return graphapp.SemanticLinkTopicScanPage{}, scanRepositoryUnavailable(errors.New("semantic link GORM Topic page repository is unavailable"))
	}
	if ctx == nil {
		return graphapp.SemanticLinkTopicScanPage{}, scanRepositoryInvalid(errors.New("semantic link GORM Topic page context is nil"))
	}
	topicID, err := validateGORMTopicScanPageRequest(request)
	if err != nil {
		return graphapp.SemanticLinkTopicScanPage{}, err
	}
	var page graphapp.SemanticLinkTopicScanPage
	err = repository.repository.gormReadSnapshot(ctx, func(callbackCtx context.Context, database *gorm.DB) error {
		var pageErr error
		page, pageErr = gormLoadTopicScanPage(callbackCtx, database, request, topicID)
		return pageErr
	})
	if err != nil {
		return graphapp.SemanticLinkTopicScanPage{}, err
	}
	return page, nil
}

func validateGORMTopicScanPageRequest(request graphapp.SemanticLinkTopicScanPageRequest) (foundation.ID, error) {
	if request.Scope.Type != graphdomain.SemanticLinkScanScopeTopic || !validID(request.WorkspaceID) || !validID(request.ScanID) || request.Limit < 1 || request.Limit > graphapp.MaxSemanticLinkScanPageNodes {
		return "", scanRepositoryInvalid(errors.New("semantic link topic scan page request is invalid"))
	}
	topicID, err := foundation.ParseID(request.Scope.Ref)
	if err != nil || topicID != foundation.ID(request.Scope.Ref) {
		return "", scanRepositoryInvalid(errors.New("semantic link topic scan scope is invalid"))
	}
	if request.Cursor != "" {
		cursorID, parseErr := foundation.ParseID(request.Cursor)
		if parseErr != nil || cursorID != foundation.ID(request.Cursor) {
			return "", scanRepositoryInvalid(errors.New("semantic link topic scan cursor is invalid"))
		}
	}
	return topicID, nil
}

func gormLoadTopicScanPage(ctx context.Context, database *gorm.DB, request graphapp.SemanticLinkTopicScanPageRequest, topicID foundation.ID) (graphapp.SemanticLinkTopicScanPage, error) {
	row, err := gormRawRow(ctx, database, `SELECT version FROM core.topic WHERE workspace_id=$1 AND id=$2 AND status='ACTIVE'`, string(request.WorkspaceID), string(topicID))
	if err != nil {
		return graphapp.SemanticLinkTopicScanPage{}, classifyGORMScan(ctx, err, "GRAPH_SEMANTIC_LINK_SCAN_PAGE_SCOPE_FAILED")
	}
	var topicVersion int64
	if err := row.Scan(&topicVersion); gormGraphNoRows(err) {
		return graphapp.SemanticLinkTopicScanPage{}, notFound(graphdomain.ErrorCodeSemanticLinkScanNotFound, err)
	} else if err != nil {
		return graphapp.SemanticLinkTopicScanPage{}, classifyGORMScan(ctx, err, "GRAPH_SEMANTIC_LINK_SCAN_PAGE_SCOPE_FAILED")
	}
	if topicVersion != request.Scope.Version {
		return graphapp.SemanticLinkTopicScanPage{}, scanRepositoryVersionConflict("semantic link topic version changed")
	}
	nodes, hasMore, err := gormLoadTopicClaimNodes(ctx, database, request, topicID)
	if err != nil {
		return graphapp.SemanticLinkTopicScanPage{}, err
	}
	pairs, err := gormLoadTopicClaimPairs(ctx, database, request.WorkspaceID, topicID, nodes)
	if err != nil {
		return graphapp.SemanticLinkTopicScanPage{}, err
	}
	exclusions, err := gormLoadPairExclusions(ctx, database, request.WorkspaceID, pairs, gormPairExclusionQueryTopic)
	if err != nil {
		return graphapp.SemanticLinkTopicScanPage{}, err
	}
	page := graphapp.SemanticLinkTopicScanPage{
		WorkspaceID: request.WorkspaceID, ScanID: request.ScanID, ScopeVersion: request.Scope.Version,
		Cursor: request.Cursor, Complete: !hasMore, ProcessedNodes: int64(len(nodes)),
		Pairs: pairs, Exclusions: exclusions,
	}
	if len(nodes) > 0 {
		last := nodes[len(nodes)-1].Endpoint.Ref
		page.LastNode = &last
		if hasMore {
			page.NextCursor = string(last.ID)
		}
	}
	return page, nil
}

func gormLoadTopicClaimNodes(ctx context.Context, database *gorm.DB, request graphapp.SemanticLinkTopicScanPageRequest, topicID foundation.ID) ([]graphdomain.SemanticLinkDiscoveryNode, bool, error) {
	rows, err := gormRawRows(ctx, database, `
		SELECT claim.id::text,claim.version,claim.statement,claim.status,
			COALESCE(array_agg(DISTINCT membership.target_node_id::text ORDER BY membership.target_node_id::text) FILTER (WHERE membership.target_node_id IS NOT NULL),'{}'::text[]),
			COALESCE(array_agg(DISTINCT source.source_version_id::text ORDER BY source.source_version_id::text) FILTER (WHERE source.source_version_id IS NOT NULL),'{}'::text[])
		FROM core.claim claim
		JOIN core.relation scoped
		  ON scoped.workspace_id=claim.workspace_id
		 AND scoped.source_node_type='CLAIM' AND scoped.source_node_id=claim.id
		 AND scoped.target_node_type='TOPIC' AND scoped.target_node_id=$2
		 AND scoped.relation_type='BELONGS_TO' AND scoped.status='CONFIRMED'
		LEFT JOIN core.relation membership
		  ON membership.workspace_id=claim.workspace_id
		 AND membership.source_node_type='CLAIM' AND membership.source_node_id=claim.id
		 AND membership.target_node_type='TOPIC' AND membership.relation_type='BELONGS_TO'
		 AND membership.status='CONFIRMED'
		LEFT JOIN core.claim_source source
		  ON source.workspace_id=claim.workspace_id AND source.claim_id=claim.id
		WHERE claim.workspace_id=$1 AND claim.status IN ('CONFIRMED','DISPUTED')
		  AND ($3='' OR claim.id::text>$3)
		GROUP BY claim.id,claim.version,claim.statement,claim.status
		ORDER BY claim.id
		LIMIT $4`, string(request.WorkspaceID), string(topicID), request.Cursor, request.Limit+1)
	if err != nil {
		return nil, false, classifyGORMScan(ctx, err, "GRAPH_SEMANTIC_LINK_SCAN_PAGE_QUERY_FAILED")
	}
	defer func() { _ = rows.Close() }()
	nodes := make([]graphdomain.SemanticLinkDiscoveryNode, 0, request.Limit+1)
	for rows.Next() {
		node, err := gormScanTopicNode(ctx, rows, request.WorkspaceID, "GRAPH_SEMANTIC_LINK_SCAN_PAGE_QUERY_FAILED")
		if err != nil {
			return nil, false, err
		}
		nodes = append(nodes, node)
	}
	if err := gormFinishScanRows(ctx, rows, "GRAPH_SEMANTIC_LINK_SCAN_PAGE_QUERY_FAILED"); err != nil {
		return nil, false, err
	}
	hasMore := len(nodes) > request.Limit
	if hasMore {
		nodes = nodes[:request.Limit]
	}
	return nodes, hasMore, nil
}

func gormLoadTopicClaimPairs(ctx context.Context, database *gorm.DB, workspaceID, topicID foundation.ID, sources []graphdomain.SemanticLinkDiscoveryNode) ([]graphdomain.SemanticLinkDiscoveryPair, error) {
	if len(sources) == 0 {
		return nil, nil
	}
	sourceIDs := make([]string, len(sources))
	nodes := make(map[foundation.ID]graphdomain.SemanticLinkDiscoveryNode, len(sources))
	for index, source := range sources {
		sourceIDs[index] = string(source.Endpoint.Ref.ID)
		nodes[source.Endpoint.Ref.ID] = source
	}
	rows, err := gormRawRows(ctx, database, semanticLinkTopicClaimPairsSQL, string(workspaceID), string(topicID), sourceIDs, graphapp.MaxSemanticLinkScanPagePairsPerNode)
	if err != nil {
		return nil, classifyGORMScan(ctx, err, "GRAPH_SEMANTIC_LINK_SCAN_PAIR_QUERY_FAILED")
	}
	defer func() { _ = rows.Close() }()
	refs := make([]topicScanPairRef, 0, len(sources)*graphapp.MaxSemanticLinkScanPagePairsPerNode)
	missing := make(map[foundation.ID]struct{})
	for rows.Next() {
		var sourceID, targetID string
		if err := rows.Scan(&sourceID, &targetID); err != nil {
			return nil, classifyGORMScan(ctx, err, "GRAPH_SEMANTIC_LINK_SCAN_PAIR_QUERY_FAILED")
		}
		ref := topicScanPairRef{sourceID: foundation.ID(sourceID), targetID: foundation.ID(targetID)}
		refs = append(refs, ref)
		if _, found := nodes[ref.targetID]; !found {
			missing[ref.targetID] = struct{}{}
		}
	}
	if err := gormFinishScanRows(ctx, rows, "GRAPH_SEMANTIC_LINK_SCAN_PAIR_QUERY_FAILED"); err != nil {
		return nil, err
	}
	if len(missing) > 0 {
		ids := make([]string, 0, len(missing))
		for id := range missing {
			ids = append(ids, string(id))
		}
		sort.Strings(ids)
		targets, err := gormLoadTopicClaimNodesByID(ctx, database, workspaceID, topicID, ids)
		if err != nil {
			return nil, err
		}
		for _, target := range targets {
			nodes[target.Endpoint.Ref.ID] = target
		}
	}
	pairs := make([]graphdomain.SemanticLinkDiscoveryPair, 0, len(refs))
	for _, ref := range refs {
		source, sourceFound := nodes[ref.sourceID]
		target, targetFound := nodes[ref.targetID]
		if !sourceFound || !targetFound {
			return nil, scanRepositoryConsistency(errors.New("semantic link scan pair node is missing"))
		}
		pairs = append(pairs, graphdomain.SemanticLinkDiscoveryPair{Source: source, Target: target})
	}
	return pairs, nil
}

func gormLoadTopicClaimNodesByID(ctx context.Context, database *gorm.DB, workspaceID, topicID foundation.ID, claimIDs []string) ([]graphdomain.SemanticLinkDiscoveryNode, error) {
	rows, err := gormRawRows(ctx, database, `
		SELECT claim.id::text,claim.version,claim.statement,claim.status,
			COALESCE(array_agg(DISTINCT membership.target_node_id::text ORDER BY membership.target_node_id::text) FILTER (WHERE membership.target_node_id IS NOT NULL),'{}'::text[]),
			COALESCE(array_agg(DISTINCT source.source_version_id::text ORDER BY source.source_version_id::text) FILTER (WHERE source.source_version_id IS NOT NULL),'{}'::text[])
		FROM core.claim claim
		JOIN core.relation scoped
		  ON scoped.workspace_id=claim.workspace_id
		 AND scoped.source_node_type='CLAIM' AND scoped.source_node_id=claim.id
		 AND scoped.target_node_type='TOPIC' AND scoped.target_node_id=$2
		 AND scoped.relation_type='BELONGS_TO' AND scoped.status='CONFIRMED'
		LEFT JOIN core.relation membership
		  ON membership.workspace_id=claim.workspace_id
		 AND membership.source_node_type='CLAIM' AND membership.source_node_id=claim.id
		 AND membership.target_node_type='TOPIC' AND membership.relation_type='BELONGS_TO'
		 AND membership.status='CONFIRMED'
		LEFT JOIN core.claim_source source
		  ON source.workspace_id=claim.workspace_id AND source.claim_id=claim.id
		WHERE claim.workspace_id=$1 AND claim.status IN ('CONFIRMED','DISPUTED') AND claim.id=ANY($3::uuid[])
		GROUP BY claim.id,claim.version,claim.statement,claim.status
		ORDER BY claim.id`, string(workspaceID), string(topicID), claimIDs)
	if err != nil {
		return nil, classifyGORMScan(ctx, err, "GRAPH_SEMANTIC_LINK_SCAN_PAIR_NODE_QUERY_FAILED")
	}
	defer func() { _ = rows.Close() }()
	nodes := make([]graphdomain.SemanticLinkDiscoveryNode, 0, len(claimIDs))
	for rows.Next() {
		node, err := gormScanTopicNode(ctx, rows, workspaceID, "GRAPH_SEMANTIC_LINK_SCAN_PAIR_NODE_QUERY_FAILED")
		if err != nil {
			return nil, err
		}
		nodes = append(nodes, node)
	}
	if err := gormFinishScanRows(ctx, rows, "GRAPH_SEMANTIC_LINK_SCAN_PAIR_NODE_QUERY_FAILED"); err != nil {
		return nil, err
	}
	if len(nodes) != len(claimIDs) {
		return nil, scanRepositoryConsistency(errors.New("semantic link scan target node set changed"))
	}
	return nodes, nil
}

func gormScanTopicNode(ctx context.Context, row graphSQLScanner, workspaceID foundation.ID, code string) (graphdomain.SemanticLinkDiscoveryNode, error) {
	var id, statement, status string
	var version int64
	var topicIDs, sourceVersionIDs []string
	if err := row.Scan(&id, &version, &statement, &status, &topicIDs, &sourceVersionIDs); err != nil {
		return graphdomain.SemanticLinkDiscoveryNode{}, classifyGORMScan(ctx, err, code)
	}
	node, err := topicScanDiscoveryNode(workspaceID, foundation.ID(id), version, statement, status, topicIDs, sourceVersionIDs)
	if err != nil {
		return graphdomain.SemanticLinkDiscoveryNode{}, err
	}
	return node, nil
}

func gormFinishScanRows(ctx context.Context, rows *graphSQLRows, code string) error {
	if err := rows.Err(); err != nil {
		return classifyGORMScan(ctx, err, code)
	}
	if err := rows.Close(); err != nil {
		return classifyGORMScan(ctx, err, code)
	}
	return nil
}

// GORMSmartCollectionScanPlanner keeps Collection as the durable membership
// owner and only validates its Application projection.
type GORMSmartCollectionScanPlanner struct {
	reader graphapp.SmartCollectionScanReader
}

var _ graphapp.SemanticLinkSmartCollectionScanPlanner = (*GORMSmartCollectionScanPlanner)(nil)

// NewGORMSmartCollectionScanPlanner constructs the staged Smart planner.
func NewGORMSmartCollectionScanPlanner(reader graphapp.SmartCollectionScanReader) (*GORMSmartCollectionScanPlanner, error) {
	if isNilScanDependency(reader) {
		return nil, scanRepositoryUnavailable(errors.New("smart collection GORM scan reader is missing"))
	}
	return &GORMSmartCollectionScanPlanner{reader: reader}, nil
}

// PlanSmartCollectionScan delegates to the Collection-owned snapshot.
func (planner *GORMSmartCollectionScanPlanner) PlanSmartCollectionScan(ctx context.Context, workspaceID, collectionID foundation.ID) (graphapp.SemanticLinkSmartCollectionScanPlan, error) {
	if planner == nil || isNilScanDependency(planner.reader) {
		return graphapp.SemanticLinkSmartCollectionScanPlan{}, scanRepositoryUnavailable(errors.New("smart collection GORM scan planner is unavailable"))
	}
	if ctx == nil {
		return graphapp.SemanticLinkSmartCollectionScanPlan{}, scanRepositoryInvalid(errors.New("smart collection GORM scan context is nil"))
	}
	return (&SmartCollectionScanPlanner{reader: planner.reader}).PlanSmartCollectionScan(ctx, workspaceID, collectionID)
}

// GORMSmartCollectionScanPageRepository keeps the existing cross-owner
// boundary: Collection completes its durable page snapshot before Graph reads
// exclusions from its shared root.
type GORMSmartCollectionScanPageRepository struct {
	reader     graphapp.SmartCollectionScanReader
	repository *GORMRepository
}

var _ graphapp.SemanticLinkTopicScanPageSource = (*GORMSmartCollectionScanPageRepository)(nil)

// NewGORMSmartCollectionScanPageRepository constructs the staged Smart page source.
func NewGORMSmartCollectionScanPageRepository(pool *platformpostgres.Pool, reader graphapp.SmartCollectionScanReader) (*GORMSmartCollectionScanPageRepository, error) {
	if pool == nil || isNilScanDependency(reader) {
		return nil, scanRepositoryUnavailable(errors.New("smart collection GORM scan page dependencies are missing"))
	}
	repository, err := NewGORMRepository(pool)
	if err != nil {
		return nil, err
	}
	return &GORMSmartCollectionScanPageRepository{reader: reader, repository: repository}, nil
}

// LoadPage deliberately does not wrap the Collection reader and Graph
// exclusion query in a nested UoW; the current Application port cannot share
// one snapshot across those owners.
func (repository *GORMSmartCollectionScanPageRepository) LoadPage(ctx context.Context, request graphapp.SemanticLinkTopicScanPageRequest) (graphapp.SemanticLinkTopicScanPage, error) {
	if repository == nil || repository.repository == nil || isNilScanDependency(repository.reader) {
		return graphapp.SemanticLinkTopicScanPage{}, scanRepositoryUnavailable(errors.New("smart collection GORM scan page repository is unavailable"))
	}
	if ctx == nil {
		return graphapp.SemanticLinkTopicScanPage{}, scanRepositoryInvalid(errors.New("smart collection GORM scan context is nil"))
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
	database, err := gormScanRootDB(ctx, repository.repository)
	if err != nil {
		return graphapp.SemanticLinkTopicScanPage{}, err
	}
	exclusions, err := gormLoadPairExclusions(ctx, database, request.WorkspaceID, pairs, gormPairExclusionQuerySmartCollection)
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

func gormLoadPairExclusions(ctx context.Context, database *gorm.DB, workspaceID foundation.ID, pairs []graphdomain.SemanticLinkDiscoveryPair, queryKind gormPairExclusionQuery) ([]graphdomain.SemanticLinkDiscoveryExclusion, error) {
	if len(pairs) == 0 {
		return nil, nil
	}
	var query string
	switch queryKind {
	case gormPairExclusionQueryTopic:
		query = gormTopicPairExclusionsSQL
	case gormPairExclusionQuerySmartCollection:
		query = gormSmartCollectionPairExclusionsSQL
	default:
		return nil, scanRepositoryConsistency(errors.New("semantic link scan exclusion query is invalid"))
	}
	leftTypes, leftIDs := make([]string, len(pairs)), make([]string, len(pairs))
	rightTypes, rightIDs := make([]string, len(pairs)), make([]string, len(pairs))
	for index, pair := range pairs {
		leftTypes[index], leftIDs[index] = string(pair.Source.Endpoint.Ref.Type), string(pair.Source.Endpoint.Ref.ID)
		rightTypes[index], rightIDs[index] = string(pair.Target.Endpoint.Ref.Type), string(pair.Target.Endpoint.Ref.ID)
	}
	rows, err := gormRawRows(ctx, database, query, string(workspaceID), leftTypes, leftIDs, rightTypes, rightIDs)
	if err != nil {
		return nil, classifyGORMScan(ctx, err, "GRAPH_SEMANTIC_LINK_SCAN_EXCLUSION_QUERY_FAILED")
	}
	defer func() { _ = rows.Close() }()
	result := make([]graphdomain.SemanticLinkDiscoveryExclusion, 0)
	for rows.Next() {
		var leftType, leftID, rightType, rightID, reason string
		if err := rows.Scan(&leftType, &leftID, &rightType, &rightID, &reason); err != nil {
			return nil, classifyGORMScan(ctx, err, "GRAPH_SEMANTIC_LINK_SCAN_EXCLUSION_QUERY_FAILED")
		}
		if reason != "" {
			result = append(result, graphdomain.SemanticLinkDiscoveryExclusion{
				Source: knowledge.NodeRef{Type: knowledge.NodeType(leftType), ID: foundation.ID(leftID)},
				Target: knowledge.NodeRef{Type: knowledge.NodeType(rightType), ID: foundation.ID(rightID)}, Reason: reason,
			})
		}
	}
	if err := gormFinishScanRows(ctx, rows, "GRAPH_SEMANTIC_LINK_SCAN_EXCLUSION_QUERY_FAILED"); err != nil {
		return nil, err
	}
	return result, nil
}
