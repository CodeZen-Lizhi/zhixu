package postgres

import (
	"context"
	"errors"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	graphapp "github.com/CodeZen-Lizhi/zhixu/internal/graph/application"
	graphdomain "github.com/CodeZen-Lizhi/zhixu/internal/graph/domain"
	knowledge "github.com/CodeZen-Lizhi/zhixu/internal/knowledge/domain"
	"github.com/jackc/pgx/v5"
)

// SemanticLinkTopicScanPageRepository 按 Topic 成员页批量生成确定性 Claim pair。
type SemanticLinkTopicScanPageRepository struct{ db DB }

var _ graphapp.SemanticLinkTopicScanPageSource = (*SemanticLinkTopicScanPageRepository)(nil)

const semanticLinkTopicClaimPairsSQL = `
	WITH source_ids AS (
		SELECT unnest($3::uuid[]) AS source_id
	)
	SELECT source_ids.source_id::text,target.target_id::text
	FROM source_ids
	CROSS JOIN LATERAL (
		SELECT claim.id AS target_id
		FROM core.relation scoped
		JOIN core.claim claim
		  ON claim.workspace_id=scoped.workspace_id AND claim.id=scoped.source_node_id
		 AND claim.status IN ('CONFIRMED','DISPUTED')
		WHERE scoped.workspace_id=$1
		  AND scoped.source_node_type='CLAIM'
		  AND scoped.target_node_type='TOPIC' AND scoped.target_node_id=$2
		  AND scoped.relation_type='BELONGS_TO' AND scoped.status='CONFIRMED'
		  AND claim.id>source_ids.source_id
		ORDER BY claim.id
		LIMIT $4
	) target
	ORDER BY source_ids.source_id,target.target_id`

// NewSemanticLinkTopicScanPageRepository 创建只读 Topic scan 页源。
func NewSemanticLinkTopicScanPageRepository(db DB) (*SemanticLinkTopicScanPageRepository, error) {
	if isNilScanDependency(db) {
		return nil, scanRepositoryUnavailable(errors.New("semantic link topic scan page database is missing"))
	}
	return &SemanticLinkTopicScanPageRepository{db: db}, nil
}

// LoadPage 在固定 Topic version 下批量加载最多 100 个正式 Claim，并在内存生成有界 pair。
func (repository *SemanticLinkTopicScanPageRepository) LoadPage(ctx context.Context, request graphapp.SemanticLinkTopicScanPageRequest) (graphapp.SemanticLinkTopicScanPage, error) {
	if repository == nil || isNilScanDependency(repository.db) {
		return graphapp.SemanticLinkTopicScanPage{}, scanRepositoryUnavailable(errors.New("semantic link topic scan page repository is unavailable"))
	}
	if request.Scope.Type != graphdomain.SemanticLinkScanScopeTopic || !validID(request.WorkspaceID) || !validID(request.ScanID) || request.Limit < 1 || request.Limit > graphapp.MaxSemanticLinkScanPageNodes {
		return graphapp.SemanticLinkTopicScanPage{}, scanRepositoryInvalid(errors.New("semantic link topic scan page request is invalid"))
	}
	topicID, err := foundation.ParseID(request.Scope.Ref)
	if err != nil || topicID != foundation.ID(request.Scope.Ref) {
		return graphapp.SemanticLinkTopicScanPage{}, scanRepositoryInvalid(errors.New("semantic link topic scan scope is invalid"))
	}
	if request.Cursor != "" {
		cursorID, parseErr := foundation.ParseID(request.Cursor)
		if parseErr != nil || cursorID != foundation.ID(request.Cursor) {
			return graphapp.SemanticLinkTopicScanPage{}, scanRepositoryInvalid(errors.New("semantic link topic scan cursor is invalid"))
		}
	}
	var topicVersion int64
	if err := repository.db.QueryRow(ctx, `SELECT version FROM core.topic WHERE workspace_id=$1 AND id=$2 AND status='ACTIVE'`, string(request.WorkspaceID), string(topicID)).Scan(&topicVersion); errors.Is(err, pgx.ErrNoRows) {
		return graphapp.SemanticLinkTopicScanPage{}, notFound(graphdomain.ErrorCodeSemanticLinkScanNotFound, err)
	} else if err != nil {
		return graphapp.SemanticLinkTopicScanPage{}, scanRepositoryClassify(err, "GRAPH_SEMANTIC_LINK_SCAN_PAGE_SCOPE_FAILED")
	}
	if topicVersion != request.Scope.Version {
		return graphapp.SemanticLinkTopicScanPage{}, scanRepositoryVersionConflict("semantic link topic version changed")
	}
	nodes, hasMore, err := repository.loadTopicClaimNodes(ctx, request, topicID)
	if err != nil {
		return graphapp.SemanticLinkTopicScanPage{}, err
	}
	pairs, err := repository.loadTopicClaimPairs(ctx, request.WorkspaceID, topicID, nodes)
	if err != nil {
		return graphapp.SemanticLinkTopicScanPage{}, err
	}
	exclusions, err := repository.loadTopicPairExclusions(ctx, request.WorkspaceID, pairs)
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

type topicScanPairRef struct {
	sourceID foundation.ID
	targetID foundation.ID
}

func (repository *SemanticLinkTopicScanPageRepository) loadTopicClaimPairs(ctx context.Context, workspaceID, topicID foundation.ID, sources []graphdomain.SemanticLinkDiscoveryNode) ([]graphdomain.SemanticLinkDiscoveryPair, error) {
	if len(sources) == 0 {
		return nil, nil
	}
	sourceIDs := make([]string, len(sources))
	nodes := make(map[foundation.ID]graphdomain.SemanticLinkDiscoveryNode, len(sources))
	for index, source := range sources {
		sourceIDs[index] = string(source.Endpoint.Ref.ID)
		nodes[source.Endpoint.Ref.ID] = source
	}
	rows, err := repository.db.Query(ctx, semanticLinkTopicClaimPairsSQL, string(workspaceID), string(topicID), sourceIDs, graphapp.MaxSemanticLinkScanPagePairsPerNode)
	if err != nil {
		return nil, scanRepositoryClassify(err, "GRAPH_SEMANTIC_LINK_SCAN_PAIR_QUERY_FAILED")
	}
	defer rows.Close()
	refs := make([]topicScanPairRef, 0, len(sources)*graphapp.MaxSemanticLinkScanPagePairsPerNode)
	missing := make(map[foundation.ID]struct{})
	for rows.Next() {
		var sourceID, targetID string
		if err := rows.Scan(&sourceID, &targetID); err != nil {
			return nil, scanRepositoryClassify(err, "GRAPH_SEMANTIC_LINK_SCAN_PAIR_QUERY_FAILED")
		}
		ref := topicScanPairRef{sourceID: foundation.ID(sourceID), targetID: foundation.ID(targetID)}
		refs = append(refs, ref)
		if _, found := nodes[ref.targetID]; !found {
			missing[ref.targetID] = struct{}{}
		}
	}
	if err := rows.Err(); err != nil {
		return nil, scanRepositoryClassify(err, "GRAPH_SEMANTIC_LINK_SCAN_PAIR_QUERY_FAILED")
	}
	if len(missing) > 0 {
		ids := make([]string, 0, len(missing))
		for id := range missing {
			ids = append(ids, string(id))
		}
		sort.Strings(ids)
		targets, err := repository.loadTopicClaimNodesByID(ctx, workspaceID, topicID, ids)
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

func (repository *SemanticLinkTopicScanPageRepository) loadTopicClaimNodesByID(ctx context.Context, workspaceID, topicID foundation.ID, claimIDs []string) ([]graphdomain.SemanticLinkDiscoveryNode, error) {
	rows, err := repository.db.Query(ctx, `
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
		return nil, scanRepositoryClassify(err, "GRAPH_SEMANTIC_LINK_SCAN_PAIR_NODE_QUERY_FAILED")
	}
	defer rows.Close()
	nodes := make([]graphdomain.SemanticLinkDiscoveryNode, 0, len(claimIDs))
	for rows.Next() {
		var id, statement, status string
		var version int64
		var topicIDs, sourceVersionIDs []string
		if err := rows.Scan(&id, &version, &statement, &status, &topicIDs, &sourceVersionIDs); err != nil {
			return nil, scanRepositoryClassify(err, "GRAPH_SEMANTIC_LINK_SCAN_PAIR_NODE_QUERY_FAILED")
		}
		node, err := topicScanDiscoveryNode(workspaceID, foundation.ID(id), version, statement, status, topicIDs, sourceVersionIDs)
		if err != nil {
			return nil, err
		}
		nodes = append(nodes, node)
	}
	if err := rows.Err(); err != nil {
		return nil, scanRepositoryClassify(err, "GRAPH_SEMANTIC_LINK_SCAN_PAIR_NODE_QUERY_FAILED")
	}
	if len(nodes) != len(claimIDs) {
		return nil, scanRepositoryConsistency(errors.New("semantic link scan target node set changed"))
	}
	return nodes, nil
}

func (repository *SemanticLinkTopicScanPageRepository) loadTopicClaimNodes(ctx context.Context, request graphapp.SemanticLinkTopicScanPageRequest, topicID foundation.ID) ([]graphdomain.SemanticLinkDiscoveryNode, bool, error) {
	rows, err := repository.db.Query(ctx, `
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
		return nil, false, scanRepositoryClassify(err, "GRAPH_SEMANTIC_LINK_SCAN_PAGE_QUERY_FAILED")
	}
	defer rows.Close()
	nodes := make([]graphdomain.SemanticLinkDiscoveryNode, 0, request.Limit+1)
	for rows.Next() {
		var id, statement, status string
		var version int64
		var topicIDs, sourceVersionIDs []string
		if err := rows.Scan(&id, &version, &statement, &status, &topicIDs, &sourceVersionIDs); err != nil {
			return nil, false, scanRepositoryClassify(err, "GRAPH_SEMANTIC_LINK_SCAN_PAGE_QUERY_FAILED")
		}
		node, err := topicScanDiscoveryNode(request.WorkspaceID, foundation.ID(id), version, statement, status, topicIDs, sourceVersionIDs)
		if err != nil {
			return nil, false, err
		}
		nodes = append(nodes, node)
	}
	if err := rows.Err(); err != nil {
		return nil, false, scanRepositoryClassify(err, "GRAPH_SEMANTIC_LINK_SCAN_PAGE_QUERY_FAILED")
	}
	hasMore := len(nodes) > request.Limit
	if hasMore {
		nodes = nodes[:request.Limit]
	}
	return nodes, hasMore, nil
}

func (repository *SemanticLinkTopicScanPageRepository) loadTopicPairExclusions(ctx context.Context, workspaceID foundation.ID, pairs []graphdomain.SemanticLinkDiscoveryPair) ([]graphdomain.SemanticLinkDiscoveryExclusion, error) {
	return loadTopicPairExclusions(ctx, repository.db, workspaceID, pairs)
}

func loadTopicPairExclusions(ctx context.Context, db DB, workspaceID foundation.ID, pairs []graphdomain.SemanticLinkDiscoveryPair) ([]graphdomain.SemanticLinkDiscoveryExclusion, error) {
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

func topicScanDiscoveryNode(workspaceID, claimID foundation.ID, version int64, statement, status string, topicIDs, sourceVersionIDs []string) (graphdomain.SemanticLinkDiscoveryNode, error) {
	summary := boundedScanText(statement, 4096)
	if summary == "" {
		return graphdomain.SemanticLinkDiscoveryNode{}, scanRepositoryConsistency(errors.New("semantic link claim summary is invalid"))
	}
	lifecycle := knowledge.NodeLifecycleConfirmed
	if status == string(knowledge.ClaimStatusDisputed) {
		lifecycle = knowledge.NodeLifecycleDisputed
	}
	node := graphdomain.SemanticLinkDiscoveryNode{
		WorkspaceID: workspaceID,
		Endpoint: graphdomain.SemanticLinkCandidateEndpoint{
			Ref: knowledge.NodeRef{Type: knowledge.NodeTypeClaim, ID: claimID}, Version: version,
			Summary: summary, Excerpt: summary,
		},
		Lifecycle: lifecycle, Labels: []string{boundedScanText(statement, 512)}, Terms: scanTerms(statement),
	}
	for _, value := range topicIDs {
		node.TopicRefs = append(node.TopicRefs, knowledge.NodeRef{Type: knowledge.NodeTypeTopic, ID: foundation.ID(value)})
	}
	for _, value := range sourceVersionIDs {
		node.SourceVersionIDs = append(node.SourceVersionIDs, foundation.ID(value))
	}
	if err := graphdomain.ValidateSemanticLinkDiscoveryNode(workspaceID, node); err != nil {
		return graphdomain.SemanticLinkDiscoveryNode{}, err
	}
	return node, nil
}

func scanTerms(value string) []string {
	set := make(map[string]struct{})
	for _, field := range strings.Fields(strings.ToLower(value)) {
		term := strings.TrimFunc(field, func(r rune) bool { return unicode.IsPunct(r) || unicode.IsSymbol(r) })
		if term == "" || utf8.RuneCountInString(term) > 64 {
			continue
		}
		set[term] = struct{}{}
		if len(set) == graphdomain.MaxSemanticLinkDiscoveryTerms {
			break
		}
	}
	result := make([]string, 0, len(set))
	for term := range set {
		result = append(result, term)
	}
	sort.Strings(result)
	return result
}

func boundedScanText(value string, maxBytes int) string {
	value = strings.Join(strings.Fields(value), " ")
	if len(value) <= maxBytes {
		return value
	}
	for len(value) > maxBytes {
		_, size := utf8.DecodeLastRuneInString(value)
		value = value[:len(value)-size]
	}
	return strings.TrimSpace(value)
}
