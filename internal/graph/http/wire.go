package graphhttp

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	graphdomain "github.com/CodeZen-Lizhi/zhixu/internal/graph/domain"
	knowledge "github.com/CodeZen-Lizhi/zhixu/internal/knowledge/domain"
)

type optional[T any] struct {
	Value   T
	Present bool
	Null    bool
}

func (value *optional[T]) UnmarshalJSON(raw []byte) error {
	value.Present = true
	if bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		value.Null = true
		return nil
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	return decoder.Decode(&value.Value)
}

type nodeRefRequest struct {
	Type string `json:"type"`
	ID   string `json:"id"`
}

type filterRequest struct {
	NodeTypes             optional[[]string] `json:"node_types,omitempty"`
	RelationTypes         optional[[]string] `json:"relation_types,omitempty"`
	TopicIDs              optional[[]string] `json:"topic_ids,omitempty"`
	RelationStatuses      optional[[]string] `json:"relation_statuses,omitempty"`
	ClaimStatuses         optional[[]string] `json:"claim_statuses,omitempty"`
	ClaimMinConfidence    optional[float64]  `json:"claim_min_confidence,omitempty"`
	RelationMinConfidence optional[float64]  `json:"relation_min_confidence,omitempty"`
	UpdatedAfter          optional[string]   `json:"updated_after,omitempty"`
}

type globalRequest struct {
	WorkspaceID optional[string]        `json:"workspace_id"`
	Filter      optional[filterRequest] `json:"filter,omitempty"`
	Cursor      optional[string]        `json:"cursor,omitempty"`
	Limit       optional[int]           `json:"limit,omitempty"`
}

type neighborhoodRequest struct {
	WorkspaceID optional[string]         `json:"workspace_id"`
	Center      optional[nodeRefRequest] `json:"center"`
	Depth       optional[int]            `json:"depth,omitempty"`
	Limit       optional[int]            `json:"limit,omitempty"`
	Direction   optional[string]         `json:"direction,omitempty"`
	Filter      optional[filterRequest]  `json:"filter,omitempty"`
	MaxNodes    optional[int]            `json:"max_nodes,omitempty"`
	MaxEdges    optional[int]            `json:"max_edges,omitempty"`
	MaxFrontier optional[int]            `json:"max_frontier,omitempty"`
	Cursor      optional[string]         `json:"cursor,omitempty"`
}

type pathRequest struct {
	WorkspaceID   optional[string]         `json:"workspace_id"`
	From          optional[nodeRefRequest] `json:"from"`
	To            optional[nodeRefRequest] `json:"to"`
	Direction     optional[string]         `json:"direction,omitempty"`
	RelationTypes optional[[]string]       `json:"relation_types,omitempty"`
	MaxDepth      optional[int]            `json:"max_depth,omitempty"`
	MaxVisited    optional[int]            `json:"max_visited,omitempty"`
}

func (wire globalRequest) toDomain() (graphdomain.GlobalRequest, string, error) {
	workspaceID, err := requiredID(wire.WorkspaceID)
	if err != nil || invalidOptional(wire.Filter) || invalidOptional(wire.Cursor) || wire.Cursor.Present && wire.Cursor.Value == "" || invalidOptional(wire.Limit) {
		return graphdomain.GlobalRequest{}, "", invalidRequest()
	}
	filter, err := wireFilter(wire.Filter)
	if err != nil {
		return graphdomain.GlobalRequest{}, "", err
	}
	limit := graphdomain.DefaultLimit
	if wire.Limit.Present {
		limit = wire.Limit.Value
	}
	cursor := ""
	if wire.Cursor.Present {
		cursor = wire.Cursor.Value
	}
	request := graphdomain.GlobalRequest{WorkspaceID: workspaceID, Filter: filter, Limit: limit}
	if err := graphdomain.ValidateGlobalRequest(request); err != nil {
		return graphdomain.GlobalRequest{}, "", err
	}
	return request, cursor, nil
}

func (wire neighborhoodRequest) toDomain() (graphdomain.NeighborhoodRequest, string, error) {
	workspaceID, err := requiredID(wire.WorkspaceID)
	if err != nil || !wire.Center.Present || wire.Center.Null || invalidOptional(wire.Depth) || invalidOptional(wire.Limit) || invalidOptional(wire.Direction) || invalidOptional(wire.Filter) || invalidOptional(wire.MaxNodes) || invalidOptional(wire.MaxEdges) || invalidOptional(wire.MaxFrontier) || invalidOptional(wire.Cursor) || wire.Cursor.Present && wire.Cursor.Value == "" {
		return graphdomain.NeighborhoodRequest{}, "", invalidRequest()
	}
	center, err := parseNodeRef(wire.Center.Value)
	if err != nil {
		return graphdomain.NeighborhoodRequest{}, "", err
	}
	filter, err := wireFilter(wire.Filter)
	if err != nil {
		return graphdomain.NeighborhoodRequest{}, "", err
	}
	request := graphdomain.NeighborhoodRequest{
		WorkspaceID: workspaceID, Center: center, Depth: 1, Limit: graphdomain.DefaultLimit,
		Direction: graphdomain.TraversalBoth, Filter: filter, MaxNodes: graphdomain.MaxNodes,
		MaxEdges: graphdomain.MaxEdges, MaxFrontier: graphdomain.MaxNodes,
	}
	if wire.Depth.Present {
		request.Depth = wire.Depth.Value
	}
	if wire.Limit.Present {
		request.Limit = wire.Limit.Value
	}
	if wire.Direction.Present {
		request.Direction = graphdomain.TraversalDirection(wire.Direction.Value)
	}
	if wire.MaxNodes.Present {
		request.MaxNodes = wire.MaxNodes.Value
	}
	if wire.MaxEdges.Present {
		request.MaxEdges = wire.MaxEdges.Value
	}
	if wire.MaxFrontier.Present {
		request.MaxFrontier = wire.MaxFrontier.Value
	}
	cursor := ""
	if wire.Cursor.Present {
		cursor = wire.Cursor.Value
	}
	if err := graphdomain.ValidateNeighborhoodRequest(request); err != nil {
		return graphdomain.NeighborhoodRequest{}, "", err
	}
	if request.Depth != 1 && cursor != "" {
		return graphdomain.NeighborhoodRequest{}, "", invalidRequest()
	}
	return request, cursor, nil
}

func (wire pathRequest) toDomain() (graphdomain.PathRequest, error) {
	workspaceID, err := requiredID(wire.WorkspaceID)
	if err != nil || !wire.From.Present || wire.From.Null || !wire.To.Present || wire.To.Null || invalidOptional(wire.Direction) || invalidOptional(wire.RelationTypes) || invalidOptional(wire.MaxDepth) || invalidOptional(wire.MaxVisited) {
		return graphdomain.PathRequest{}, invalidRequest()
	}
	from, err := parseNodeRef(wire.From.Value)
	if err != nil {
		return graphdomain.PathRequest{}, err
	}
	to, err := parseNodeRef(wire.To.Value)
	if err != nil {
		return graphdomain.PathRequest{}, err
	}
	request := graphdomain.PathRequest{WorkspaceID: workspaceID, From: from, To: to, Direction: graphdomain.TraversalBoth, MaxDepth: graphdomain.DefaultPathDepth, MaxVisited: graphdomain.MaxNodes}
	if wire.Direction.Present {
		request.Direction = graphdomain.TraversalDirection(wire.Direction.Value)
	}
	if wire.RelationTypes.Present {
		request.RelationTypes = convertStrings[knowledge.RelationType](wire.RelationTypes.Value)
	}
	if wire.MaxDepth.Present {
		request.MaxDepth = wire.MaxDepth.Value
	}
	if wire.MaxVisited.Present {
		request.MaxVisited = wire.MaxVisited.Value
	}
	if err := graphdomain.ValidatePathRequest(request); err != nil {
		return graphdomain.PathRequest{}, err
	}
	return request, nil
}

func wireFilter(wire optional[filterRequest]) (graphdomain.GraphFilter, error) {
	if !wire.Present {
		return graphdomain.GraphFilter{}, nil
	}
	if wire.Null {
		return graphdomain.GraphFilter{}, invalidRequest()
	}
	value := wire.Value
	for _, invalid := range []bool{invalidOptional(value.NodeTypes), invalidOptional(value.RelationTypes), invalidOptional(value.TopicIDs), invalidOptional(value.RelationStatuses), invalidOptional(value.ClaimStatuses), invalidOptional(value.ClaimMinConfidence), invalidOptional(value.RelationMinConfidence), invalidOptional(value.UpdatedAfter)} {
		if invalid {
			return graphdomain.GraphFilter{}, invalidRequest()
		}
	}
	filter := graphdomain.GraphFilter{}
	if value.NodeTypes.Present {
		filter.NodeTypes = convertStrings[knowledge.NodeType](value.NodeTypes.Value)
	}
	if value.RelationTypes.Present {
		filter.RelationTypes = convertStrings[knowledge.RelationType](value.RelationTypes.Value)
	}
	if value.RelationStatuses.Present {
		filter.RelationStatuses = convertStrings[knowledge.RelationStatus](value.RelationStatuses.Value)
	}
	if value.ClaimStatuses.Present {
		filter.ClaimStatuses = convertStrings[knowledge.ClaimStatus](value.ClaimStatuses.Value)
	}
	if value.TopicIDs.Present {
		filter.TopicIDs = make([]foundation.ID, len(value.TopicIDs.Value))
		for index, raw := range value.TopicIDs.Value {
			id, err := parseID(raw)
			if err != nil {
				return graphdomain.GraphFilter{}, err
			}
			filter.TopicIDs[index] = id
		}
	}
	if value.ClaimMinConfidence.Present {
		filter.ClaimMinConfidence = &value.ClaimMinConfidence.Value
	}
	if value.RelationMinConfidence.Present {
		filter.RelationMinConfidence = &value.RelationMinConfidence.Value
	}
	if value.UpdatedAfter.Present {
		parsed, err := time.Parse(time.RFC3339Nano, value.UpdatedAfter.Value)
		if err != nil || strings.TrimSpace(value.UpdatedAfter.Value) != value.UpdatedAfter.Value {
			return graphdomain.GraphFilter{}, invalidRequest()
		}
		utc := parsed.UTC()
		filter.UpdatedAfter = &utc
	}
	if err := graphdomain.ValidateFilter(filter); err != nil {
		return graphdomain.GraphFilter{}, err
	}
	return filter, nil
}

func parseNodeSearchRequest(values url.Values) (graphdomain.NodeSearchRequest, error) {
	workspaceID, err := parseID(values.Get("workspace_id"))
	if err != nil {
		return graphdomain.NodeSearchRequest{}, err
	}
	limit, err := parseIntegerDefault(values, "limit", 20)
	if err != nil {
		return graphdomain.NodeSearchRequest{}, err
	}
	request := graphdomain.NodeSearchRequest{WorkspaceID: workspaceID, Query: values.Get("query"), Limit: limit}
	if err := graphdomain.ValidateNodeSearchRequest(request); err != nil {
		return graphdomain.NodeSearchRequest{}, err
	}
	return request, nil
}

func parseNodeRef(wire nodeRefRequest) (knowledge.NodeRef, error) {
	id, err := parseID(wire.ID)
	if err != nil || wire.Type != string(knowledge.NodeTypeTopic) && wire.Type != string(knowledge.NodeTypeClaim) {
		return knowledge.NodeRef{}, invalidRequest()
	}
	return knowledge.NodeRef{Type: knowledge.NodeType(wire.Type), ID: id}, nil
}

func requiredID(value optional[string]) (foundation.ID, error) {
	if !value.Present || value.Null {
		return "", invalidRequest()
	}
	return parseID(value.Value)
}

func parseID(value string) (foundation.ID, error) {
	if value == "" || strings.TrimSpace(value) != value {
		return "", invalidRequest()
	}
	id, err := foundation.ParseID(value)
	if err != nil || string(id) != value {
		return "", invalidRequest()
	}
	return id, nil
}

func parseTwoIDs(first, second string) (foundation.ID, foundation.ID, error) {
	a, err := parseID(first)
	if err != nil {
		return "", "", err
	}
	b, err := parseID(second)
	if err != nil {
		return "", "", invalidRequest()
	}
	return a, b, nil
}

func parseIntegerDefault(values url.Values, key string, fallback int) (int, error) {
	raw, present := values[key]
	if !present {
		return fallback, nil
	}
	value, err := strconv.Atoi(raw[0])
	if err != nil {
		return 0, invalidRequest()
	}
	return value, nil
}

func parseOptionalQueryString(values url.Values, key string) (string, error) {
	raw, present := values[key]
	if !present {
		return "", nil
	}
	if raw[0] == "" {
		return "", invalidRequest()
	}
	return raw[0], nil
}

func invalidOptional[T any](value optional[T]) bool { return value.Present && value.Null }

func convertStrings[T ~string](values []string) []T {
	result := make([]T, len(values))
	for index, value := range values {
		result[index] = T(value)
	}
	return result
}

func invalidRequest() error {
	return foundation.NewError(foundation.ErrorInvalidInput, graphdomain.ErrorCodeRequestInvalid, false, errors.New("graph request is invalid"))
}
