// Package application 定义 Graph 只读查询的应用服务与 Adapter 端口。
package application

import (
	"context"
	"errors"
	"reflect"
	"strings"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	graphdomain "github.com/CodeZen-Lizhi/zhixu/internal/graph/domain"
	knowledge "github.com/CodeZen-Lizhi/zhixu/internal/knowledge/domain"
)

// QueryPort 是 Graph 查询 Adapter 必须实现的最小只读端口。
//
// 每个方法只执行一种查询；实现必须尊重 context 取消，并返回绑定请求 Workspace 的投影。
// 三类 Window 查询必须以 limit+1 探测超限，最多返回 MaxResultWindowItems 项并显式标记 Truncated，不能生成 cursor。
type QueryPort interface {
	GlobalWindow(context.Context, graphdomain.GlobalRequest) (GlobalResultWindow, error)
	SearchNodes(context.Context, graphdomain.NodeSearchRequest) (graphdomain.NodeSearchResult, error)
	NeighborhoodWindow(context.Context, graphdomain.NeighborhoodRequest) (graphdomain.Neighborhood, error)
	FindPath(context.Context, graphdomain.PathRequest) (graphdomain.PathResult, error)
	NodeDetail(context.Context, foundation.ID, knowledge.NodeRef) (graphdomain.GraphNode, error)
	RelationDetail(context.Context, foundation.ID, foundation.ID) (graphdomain.RelationDetail, error)
	RelationEvidenceWindow(context.Context, foundation.ID, foundation.ID) (RelationEvidenceResultWindow, error)
}

// GlobalResultWindow 是 Adapter 查询 limit+1 后返回的最多 500 个 cluster 窗口。
type GlobalResultWindow struct {
	Items     []graphdomain.GlobalCluster
	Truncated bool
	Reason    string
}

// RelationEvidenceResultWindow 是 Adapter 查询 limit+1 后返回的最多 500 条 Evidence 窗口。
type RelationEvidenceResultWindow struct {
	Items     []graphdomain.RelationEvidenceItem
	Truncated bool
	Reason    string
}

// Service 在 Adapter 边界前后校验 Graph 查询合同。
type Service struct {
	port   QueryPort
	cursor *CursorCodec
}

// NewService 构造 fail-closed 的 Graph 查询服务。
func NewService(port QueryPort, cursor *CursorCodec) (*Service, error) {
	if nilInterface(port) || cursor == nil || !cursor.initialized {
		return nil, unavailable("graph query port or cursor codec is unavailable")
	}
	return &Service{port: port, cursor: cursor}, nil
}

// GlobalPageRequest 携带 HTTP opaque cursor；domain request 本身不拥有分页状态。
type GlobalPageRequest struct {
	Request graphdomain.GlobalRequest
	Cursor  string
}

// Global 查询有界且稳定排序的全局 Topic cluster。
func (service *Service) Global(ctx context.Context, request graphdomain.GlobalRequest) (graphdomain.GlobalPage, error) {
	return service.GlobalPage(ctx, GlobalPageRequest{Request: request})
}

// GlobalPage 从 Adapter 完整有界窗口生成 application-owned cursor 页面。
func (service *Service) GlobalPage(ctx context.Context, query GlobalPageRequest) (graphdomain.GlobalPage, error) {
	if err := service.ready(ctx); err != nil {
		return graphdomain.GlobalPage{}, err
	}
	request := query.Request
	if err := graphdomain.ValidateGlobalRequest(request); err != nil {
		return graphdomain.GlobalPage{}, err
	}
	window, err := service.port.GlobalWindow(ctx, request)
	if err != nil {
		return graphdomain.GlobalPage{}, err
	}
	if err := validateWindowState(len(window.Items), window.Truncated, window.Reason); err != nil {
		return graphdomain.GlobalPage{}, err
	}
	if err := graphdomain.ValidateGlobalWindow(request, window.Items, MaxResultWindowItems); err != nil {
		return graphdomain.GlobalPage{}, err
	}
	requestHash, err := CanonicalGlobalRequestHash(request)
	if err != nil {
		return graphdomain.GlobalPage{}, err
	}
	page, err := PaginateResultWindow(service.cursor, ResultWindowRequest{
		QueryKind: QueryKindGlobal, WorkspaceID: request.WorkspaceID,
		CanonicalRequestHash: requestHash, Limit: request.Limit, Cursor: query.Cursor,
	}, window.Items)
	if err != nil {
		return graphdomain.GlobalPage{}, err
	}
	result := graphdomain.GlobalPage{WorkspaceID: request.WorkspaceID, Clusters: page.Items, Meta: graphdomain.PageMeta{
		Fingerprint: page.Fingerprint, NextCursor: page.NextCursor, Complete: page.Complete && !window.Truncated, Truncated: window.Truncated, Reason: window.Reason,
	}}
	if err := graphdomain.ValidateGlobalPage(request, result); err != nil {
		return graphdomain.GlobalPage{}, err
	}
	return result, nil
}

// SearchNodes 查询 Workspace 内有界且稳定排序的 Topic/Claim 摘要。
func (service *Service) SearchNodes(ctx context.Context, request graphdomain.NodeSearchRequest) (graphdomain.NodeSearchResult, error) {
	if err := service.ready(ctx); err != nil {
		return graphdomain.NodeSearchResult{}, err
	}
	if err := graphdomain.ValidateNodeSearchRequest(request); err != nil {
		return graphdomain.NodeSearchResult{}, err
	}
	result, err := service.port.SearchNodes(ctx, request)
	if err != nil {
		return graphdomain.NodeSearchResult{}, err
	}
	if err := graphdomain.ValidateNodeSearchResult(request, result); err != nil {
		return graphdomain.NodeSearchResult{}, err
	}
	return result, nil
}

// Neighborhood 查询中心节点一至三跳的有界完整闭包。
func (service *Service) Neighborhood(ctx context.Context, request graphdomain.NeighborhoodRequest) (graphdomain.Neighborhood, error) {
	return service.NeighborhoodPage(ctx, NeighborhoodPageRequest{Request: request})
}

// NeighborhoodPageRequest 为 depth=1 局部图携带 application-owned opaque cursor。
type NeighborhoodPageRequest struct {
	Request graphdomain.NeighborhoodRequest
	Cursor  string
}

// NeighborhoodPage 对 depth=1 的完整 Adapter 窗口按 edge 切页并重建端点闭包。
func (service *Service) NeighborhoodPage(ctx context.Context, query NeighborhoodPageRequest) (graphdomain.Neighborhood, error) {
	if err := service.ready(ctx); err != nil {
		return graphdomain.Neighborhood{}, err
	}
	request := query.Request
	if err := graphdomain.ValidateNeighborhoodRequest(request); err != nil {
		return graphdomain.Neighborhood{}, err
	}
	result, err := service.port.NeighborhoodWindow(ctx, request)
	if err != nil {
		return graphdomain.Neighborhood{}, err
	}
	if result.Meta.NextCursor != "" {
		return graphdomain.Neighborhood{}, inconsistent("graph adapter must not produce opaque cursor")
	}
	if err := graphdomain.ValidateNeighborhood(request, result); err != nil {
		return graphdomain.Neighborhood{}, err
	}
	if request.Depth != 1 {
		if query.Cursor != "" {
			return graphdomain.Neighborhood{}, invalid("cursor is only supported for depth one neighborhood")
		}
		return result, nil
	}
	requestHash, err := CanonicalNeighborhoodDepth1RequestHash(request)
	if err != nil {
		return graphdomain.Neighborhood{}, err
	}
	fingerprintValue := struct {
		Nodes         []graphdomain.GraphNode `json:"nodes"`
		Edges         []graphdomain.GraphEdge `json:"edges"`
		BoundaryNodes []knowledge.NodeRef     `json:"boundary_nodes"`
	}{result.Nodes, result.Edges, result.BoundaryNodes}
	page, err := paginateResultWindow(service.cursor, ResultWindowRequest{
		QueryKind: QueryKindNeighborhoodDepth1, WorkspaceID: request.WorkspaceID,
		CanonicalRequestHash: requestHash, Limit: request.Limit, Cursor: query.Cursor,
	}, result.Edges, fingerprintValue)
	if err != nil {
		return graphdomain.Neighborhood{}, err
	}
	result.Edges = page.Items
	result.Nodes, result.BoundaryNodes = neighborhoodPageClosure(request.Center, result.Nodes, result.BoundaryNodes, page.Items)
	result.LayerCounts = []int{len(result.Nodes) - 1}
	result.CompletedDepth = 1
	result.Meta = graphdomain.PageMeta{Fingerprint: page.Fingerprint, NextCursor: page.NextCursor, Complete: page.Complete && !result.Meta.Truncated, Truncated: result.Meta.Truncated, Reason: result.Meta.Reason}
	if err := graphdomain.ValidateNeighborhood(request, result); err != nil {
		return graphdomain.Neighborhood{}, err
	}
	return result, nil
}

func neighborhoodPageClosure(center knowledge.NodeRef, nodes []graphdomain.GraphNode, boundaries []knowledge.NodeRef, edges []graphdomain.GraphEdge) ([]graphdomain.GraphNode, []knowledge.NodeRef) {
	wanted := map[knowledge.NodeRef]struct{}{center: {}}
	for _, edge := range edges {
		wanted[edge.Source], wanted[edge.Target] = struct{}{}, struct{}{}
	}
	pageNodes := make([]graphdomain.GraphNode, 0, len(wanted))
	hydrated := make(map[knowledge.NodeRef]struct{}, len(wanted))
	for _, node := range nodes {
		if _, ok := wanted[node.Ref()]; ok {
			pageNodes = append(pageNodes, node)
			hydrated[node.Ref()] = struct{}{}
		}
	}
	pageBoundaries := make([]knowledge.NodeRef, 0, len(boundaries))
	for _, ref := range boundaries {
		if _, ok := wanted[ref]; ok {
			if _, ok := hydrated[ref]; !ok {
				pageBoundaries = append(pageBoundaries, ref)
			}
		}
	}
	return pageNodes, pageBoundaries
}

// FindPath 查询两个 Topic/Claim 间已证明的最短路径或明确无路径结果。
func (service *Service) FindPath(ctx context.Context, request graphdomain.PathRequest) (graphdomain.PathResult, error) {
	if err := service.ready(ctx); err != nil {
		return graphdomain.PathResult{}, err
	}
	if err := graphdomain.ValidatePathRequest(request); err != nil {
		return graphdomain.PathResult{}, err
	}
	result, err := service.port.FindPath(ctx, request)
	if err != nil {
		return graphdomain.PathResult{}, err
	}
	if err := graphdomain.ValidatePathResult(request, result); err != nil {
		return graphdomain.PathResult{}, err
	}
	return result, nil
}

// NodeDetail 查询 Workspace 内指定 Topic/Claim 的只读详情。
func (service *Service) NodeDetail(ctx context.Context, workspaceID foundation.ID, ref knowledge.NodeRef) (graphdomain.GraphNode, error) {
	if err := service.ready(ctx); err != nil {
		return graphdomain.GraphNode{}, err
	}
	if !validID(workspaceID) || !validNodeRef(ref) {
		return graphdomain.GraphNode{}, invalid("node detail request is invalid")
	}
	result, err := service.port.NodeDetail(ctx, workspaceID, ref)
	if err != nil {
		return graphdomain.GraphNode{}, err
	}
	if graphdomain.ValidateGraphNode(result) != nil || result.WorkspaceID() != workspaceID || result.Ref() != ref {
		return graphdomain.GraphNode{}, inconsistent("node detail projection binding is inconsistent")
	}
	return result, nil
}

// RelationDetail 查询 Workspace 内指定 canonical Relation 的只读详情。
func (service *Service) RelationDetail(ctx context.Context, workspaceID, relationID foundation.ID) (graphdomain.RelationDetail, error) {
	if err := service.ready(ctx); err != nil {
		return graphdomain.RelationDetail{}, err
	}
	if !validID(workspaceID) || !validID(relationID) {
		return graphdomain.RelationDetail{}, invalid("relation detail request is invalid")
	}
	result, err := service.port.RelationDetail(ctx, workspaceID, relationID)
	if err != nil {
		return graphdomain.RelationDetail{}, err
	}
	if result.Edge.RelationID != relationID {
		return graphdomain.RelationDetail{}, inconsistent("relation detail projection binding is inconsistent")
	}
	if err := graphdomain.ValidateRelationDetail(workspaceID, result); err != nil {
		return graphdomain.RelationDetail{}, err
	}
	return result, nil
}

// RelationEvidence 查询指定 Relation 的独立有界 Evidence 页面。
func (service *Service) RelationEvidence(ctx context.Context, workspaceID, relationID foundation.ID, limit int) (graphdomain.RelationEvidencePage, error) {
	return service.RelationEvidencePage(ctx, RelationEvidencePageRequest{WorkspaceID: workspaceID, RelationID: relationID, Limit: limit})
}

// RelationEvidencePageRequest 携带 Evidence 页大小与 opaque cursor。
type RelationEvidencePageRequest struct {
	WorkspaceID foundation.ID
	RelationID  foundation.ID
	Limit       int
	Cursor      string
}

// RelationEvidencePage 从 owner 的完整有界 Evidence 窗口切页。
func (service *Service) RelationEvidencePage(ctx context.Context, request RelationEvidencePageRequest) (graphdomain.RelationEvidencePage, error) {
	if err := service.ready(ctx); err != nil {
		return graphdomain.RelationEvidencePage{}, err
	}
	workspaceID, relationID, limit := request.WorkspaceID, request.RelationID, request.Limit
	if !validID(workspaceID) || !validID(relationID) || limit < 1 || limit > graphdomain.MaxLimit {
		return graphdomain.RelationEvidencePage{}, invalid("relation evidence request is invalid")
	}
	window, err := service.port.RelationEvidenceWindow(ctx, workspaceID, relationID)
	if err != nil {
		return graphdomain.RelationEvidencePage{}, err
	}
	if err := validateWindowState(len(window.Items), window.Truncated, window.Reason); err != nil {
		return graphdomain.RelationEvidencePage{}, err
	}
	if err := graphdomain.ValidateRelationEvidenceWindow(workspaceID, relationID, window.Items, MaxResultWindowItems); err != nil {
		return graphdomain.RelationEvidencePage{}, err
	}
	requestHash, err := CanonicalRelationEvidenceRequestHash(workspaceID, relationID)
	if err != nil {
		return graphdomain.RelationEvidencePage{}, err
	}
	page, err := PaginateResultWindow(service.cursor, ResultWindowRequest{
		QueryKind: QueryKindRelationEvidence, WorkspaceID: workspaceID,
		CanonicalRequestHash: requestHash, Limit: limit, Cursor: request.Cursor,
	}, window.Items)
	if err != nil {
		return graphdomain.RelationEvidencePage{}, err
	}
	result := graphdomain.RelationEvidencePage{WorkspaceID: workspaceID, RelationID: relationID, Items: page.Items, Meta: graphdomain.PageMeta{
		Fingerprint: page.Fingerprint, NextCursor: page.NextCursor, Complete: page.Complete && !window.Truncated, Truncated: window.Truncated, Reason: window.Reason,
	}}
	if err := graphdomain.ValidateRelationEvidencePage(workspaceID, relationID, limit, result); err != nil {
		return graphdomain.RelationEvidencePage{}, err
	}
	return result, nil
}

func validateWindowState(itemCount int, truncated bool, reason string) error {
	if itemCount > MaxResultWindowItems || (truncated && strings.TrimSpace(reason) == "") || (!truncated && reason != "") {
		return inconsistent("graph adapter result window state is inconsistent")
	}
	return nil
}

func (service *Service) ready(ctx context.Context) error {
	if service == nil || nilInterface(service.port) || service.cursor == nil || !service.cursor.initialized {
		return unavailable("graph query service is unavailable")
	}
	if ctx == nil {
		return invalid("graph query context is nil")
	}
	return nil
}

func validID(id foundation.ID) bool {
	parsed, err := foundation.ParseID(string(id))
	return err == nil && parsed == id
}

func validNodeRef(ref knowledge.NodeRef) bool {
	return (ref.Type == knowledge.NodeTypeTopic || ref.Type == knowledge.NodeTypeClaim) && validID(ref.ID)
}

func nilInterface(value any) bool {
	if value == nil {
		return true
	}
	kind := reflect.ValueOf(value).Kind()
	return (kind == reflect.Chan || kind == reflect.Func || kind == reflect.Interface || kind == reflect.Map || kind == reflect.Pointer || kind == reflect.Slice) && reflect.ValueOf(value).IsNil()
}

func invalid(message string) error {
	return foundation.NewError(foundation.ErrorInvalidInput, graphdomain.ErrorCodeRequestInvalid, false, errors.New(message))
}

func inconsistent(message string) error {
	return foundation.NewError(foundation.ErrorConsistencyViolation, graphdomain.ErrorCodeProjectionInconsistent, false, errors.New(message))
}

func unavailable(message string) error {
	return foundation.NewError(foundation.ErrorDependencyUnavailable, graphdomain.ErrorCodeDependencyUnavailable, false, errors.New(message))
}
