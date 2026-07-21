package postgres

import (
	"context"
	"errors"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	graphapp "github.com/CodeZen-Lizhi/zhixu/internal/graph/application"
	graphdomain "github.com/CodeZen-Lizhi/zhixu/internal/graph/domain"
	"github.com/jackc/pgx/v5"
)

// SemanticLinkTopicScanPlanner 从正式 Knowledge 表读取 Topic version 和有资格 Claim 数量。
type SemanticLinkTopicScanPlanner struct{ db DB }

var _ graphapp.SemanticLinkTopicScanPlanner = (*SemanticLinkTopicScanPlanner)(nil)

// NewSemanticLinkTopicScanPlanner 创建只读 Topic scan 规划 Adapter。
func NewSemanticLinkTopicScanPlanner(db DB) (*SemanticLinkTopicScanPlanner, error) {
	if isNilScanDependency(db) {
		return nil, scanRepositoryUnavailable(errors.New("semantic link scan planner database is missing"))
	}
	return &SemanticLinkTopicScanPlanner{db: db}, nil
}

// PlanTopicScan 只统计通过 CONFIRMED BELONGS_TO 绑定到 Active Topic 的 Confirmed/Disputed Claim。
func (planner *SemanticLinkTopicScanPlanner) PlanTopicScan(ctx context.Context, workspaceID, topicID foundation.ID) (graphapp.SemanticLinkTopicScanPlan, error) {
	if planner == nil || isNilScanDependency(planner.db) {
		return graphapp.SemanticLinkTopicScanPlan{}, scanRepositoryUnavailable(errors.New("semantic link scan planner is unavailable"))
	}
	if !validID(workspaceID) || !validID(topicID) {
		return graphapp.SemanticLinkTopicScanPlan{}, scanRepositoryInvalid(errors.New("semantic link topic scan identity is invalid"))
	}
	var version, total int64
	err := planner.db.QueryRow(ctx, `
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
		GROUP BY topic.version`, string(workspaceID), string(topicID)).Scan(&version, &total)
	if errors.Is(err, pgx.ErrNoRows) {
		return graphapp.SemanticLinkTopicScanPlan{}, notFound(graphdomain.ErrorCodeSemanticLinkScanNotFound, err)
	}
	if err != nil {
		return graphapp.SemanticLinkTopicScanPlan{}, scanRepositoryClassify(err, "GRAPH_SEMANTIC_LINK_SCAN_PLAN_FAILED")
	}
	return graphapp.SemanticLinkTopicScanPlan{WorkspaceID: workspaceID, TopicID: topicID, TopicVersion: version, TotalNodes: total}, nil
}
