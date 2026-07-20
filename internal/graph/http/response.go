package graphhttp

import (
	"encoding/json"
	"time"

	graphdomain "github.com/CodeZen-Lizhi/zhixu/internal/graph/domain"
	knowledge "github.com/CodeZen-Lizhi/zhixu/internal/knowledge/domain"
)

type nodeRefResponse struct {
	Type string `json:"type"`
	ID   string `json:"id"`
}

type applicabilityResponse struct {
	SchemaVersion string          `json:"schema_version"`
	Value         json.RawMessage `json:"value"`
	Hash          string          `json:"hash"`
}

type nodeResponse interface{ graphNodeResponse() }

type topicNodeResponse struct {
	Type        string `json:"type"`
	ID          string `json:"id"`
	WorkspaceID string `json:"workspace_id"`
	Name        string `json:"name"`
	Description string `json:"description"`
	TopicStatus string `json:"topic_status"`
	Version     int64  `json:"version"`
	UpdatedAt   string `json:"updated_at"`
}

func (topicNodeResponse) graphNodeResponse() {}

type claimNodeResponse struct {
	Type          string                `json:"type"`
	ID            string                `json:"id"`
	WorkspaceID   string                `json:"workspace_id"`
	Statement     string                `json:"statement"`
	ClaimStatus   string                `json:"claim_status"`
	Confidence    *float64              `json:"confidence"`
	Applicability applicabilityResponse `json:"applicability"`
	Version       int64                 `json:"version"`
	UpdatedAt     string                `json:"updated_at"`
}

func (claimNodeResponse) graphNodeResponse() {}

type edgeResponse struct {
	RelationID          string          `json:"relation_id"`
	WorkspaceID         string          `json:"workspace_id"`
	Source              nodeRefResponse `json:"source"`
	Target              nodeRefResponse `json:"target"`
	Type                string          `json:"type"`
	Status              string          `json:"status"`
	Traversal           string          `json:"traversal"`
	Confidence          *float64        `json:"confidence"`
	Version             int64           `json:"version"`
	EvidenceCount       int             `json:"evidence_count"`
	EvidenceFingerprint string          `json:"evidence_fingerprint"`
	EvidenceHref        string          `json:"evidence_href"`
	UpdatedAt           string          `json:"updated_at"`
}

type pageMetaResponse struct {
	Fingerprint string `json:"fingerprint"`
	NextCursor  string `json:"next_cursor,omitempty"`
	Complete    bool   `json:"complete"`
	Truncated   bool   `json:"truncated"`
	Reason      string `json:"reason,omitempty"`
}

type globalClusterResponse struct {
	Topic                 topicNodeResponse `json:"topic"`
	DirectClaimCount      int               `json:"direct_claim_count"`
	IncidentRelationCount int               `json:"incident_relation_count"`
	ClusterScore          int               `json:"cluster_score"`
	UpdatedAt             string            `json:"updated_at"`
}

type globalResponse struct {
	WorkspaceID string                  `json:"workspace_id"`
	Clusters    []globalClusterResponse `json:"clusters"`
	Meta        pageMetaResponse        `json:"meta"`
}

type nodeSearchMatchResponse struct {
	Kind string       `json:"kind"`
	Node nodeResponse `json:"node"`
}

type nodeSearchResponse struct {
	WorkspaceID string                    `json:"workspace_id"`
	Matches     []nodeSearchMatchResponse `json:"matches"`
}

type neighborhoodResponse struct {
	WorkspaceID    string            `json:"workspace_id"`
	Center         nodeRefResponse   `json:"center"`
	Nodes          []nodeResponse    `json:"nodes"`
	Edges          []edgeResponse    `json:"edges"`
	BoundaryNodes  []nodeRefResponse `json:"boundary_nodes"`
	LayerCounts    []int             `json:"layer_counts"`
	CompletedDepth int               `json:"completed_depth"`
	Meta           pageMetaResponse  `json:"meta"`
}

type pathResponse struct {
	WorkspaceID            string              `json:"workspace_id"`
	From                   nodeRefResponse     `json:"from"`
	To                     nodeRefResponse     `json:"to"`
	Status                 string              `json:"status"`
	Nodes                  []nodeResponse      `json:"nodes"`
	Edges                  []edgeResponse      `json:"edges"`
	HopCount               int                 `json:"hop_count"`
	ExploredNodes          int                 `json:"explored_nodes"`
	CommonTopicSuggestions []topicNodeResponse `json:"common_topic_suggestions"`
}

type confirmationResponse struct {
	Method    string `json:"method"`
	Reference string `json:"reference"`
}

type relationDetailResponse struct {
	Edge         edgeResponse          `json:"edge"`
	Confirmation *confirmationResponse `json:"confirmation"`
	Fingerprint  string                `json:"fingerprint"`
	ValidFrom    *string               `json:"valid_from"`
	ValidTo      *string               `json:"valid_to"`
	CreatedAt    string                `json:"created_at"`
	UpdatedAt    string                `json:"updated_at"`
}

type provenanceResponse struct {
	WorkspaceID     string `json:"workspace_id"`
	SourceVersionID string `json:"source_version_id"`
	SourceSpanID    string `json:"source_span_id"`
}

type relationEvidenceItemResponse struct {
	ID            string                `json:"id"`
	WorkspaceID   string                `json:"workspace_id"`
	RelationID    string                `json:"relation_id"`
	Provenance    provenanceResponse    `json:"provenance"`
	Reason        string                `json:"reason"`
	Applicability applicabilityResponse `json:"applicability"`
	Confirmation  *confirmationResponse `json:"confirmation"`
	SourceHref    string                `json:"source_href"`
	SpanHref      string                `json:"span_href"`
	CreatedAt     string                `json:"created_at"`
}

type relationEvidenceResponse struct {
	WorkspaceID string                         `json:"workspace_id"`
	RelationID  string                         `json:"relation_id"`
	Items       []relationEvidenceItemResponse `json:"items"`
	Meta        pageMetaResponse               `json:"meta"`
}

func toGlobalResponse(value graphdomain.GlobalPage) globalResponse {
	clusters := make([]globalClusterResponse, len(value.Clusters))
	for index, cluster := range value.Clusters {
		clusters[index] = globalClusterResponse{Topic: toTopicNodeResponse(cluster.Topic), DirectClaimCount: cluster.DirectClaimCount, IncidentRelationCount: cluster.IncidentRelationCount, ClusterScore: cluster.ClusterScore, UpdatedAt: formatTime(cluster.UpdatedAt)}
	}
	return globalResponse{WorkspaceID: string(value.WorkspaceID), Clusters: clusters, Meta: toPageMeta(value.Meta)}
}

func toNodeSearchResponse(value graphdomain.NodeSearchResult) nodeSearchResponse {
	matches := make([]nodeSearchMatchResponse, len(value.Matches))
	for index, match := range value.Matches {
		matches[index] = nodeSearchMatchResponse{Kind: string(match.Kind), Node: toNodeResponse(match.Node)}
	}
	return nodeSearchResponse{WorkspaceID: string(value.WorkspaceID), Matches: matches}
}

func toNeighborhoodResponse(value graphdomain.Neighborhood) neighborhoodResponse {
	return neighborhoodResponse{WorkspaceID: string(value.WorkspaceID), Center: toNodeRef(value.Center), Nodes: toNodes(value.Nodes), Edges: toEdges(value.Edges), BoundaryNodes: toNodeRefs(value.BoundaryNodes), LayerCounts: append([]int{}, value.LayerCounts...), CompletedDepth: value.CompletedDepth, Meta: toPageMeta(value.Meta)}
}

func toPathResponse(value graphdomain.PathResult) pathResponse {
	topics := make([]topicNodeResponse, len(value.CommonTopicSuggestions))
	for index := range value.CommonTopicSuggestions {
		topics[index] = toTopicNodeResponse(value.CommonTopicSuggestions[index])
	}
	return pathResponse{WorkspaceID: string(value.WorkspaceID), From: toNodeRef(value.From), To: toNodeRef(value.To), Status: string(value.Status), Nodes: toNodes(value.Nodes), Edges: toEdges(value.Edges), HopCount: value.HopCount, ExploredNodes: value.ExploredNodes, CommonTopicSuggestions: topics}
}

func toNodeResponse(value graphdomain.GraphNode) nodeResponse {
	if value.Topic != nil {
		return toTopicNodeResponse(*value.Topic)
	}
	return claimNodeResponse{Type: string(value.Claim.Ref.Type), ID: string(value.Claim.Ref.ID), WorkspaceID: string(value.Claim.WorkspaceID), Statement: value.Claim.Statement, ClaimStatus: string(value.Claim.Status), Confidence: value.Claim.Confidence, Applicability: toApplicability(value.Claim.Applicability), Version: value.Claim.Version, UpdatedAt: formatTime(value.Claim.UpdatedAt)}
}

func toTopicNodeResponse(value graphdomain.TopicNode) topicNodeResponse {
	return topicNodeResponse{Type: string(value.Ref.Type), ID: string(value.Ref.ID), WorkspaceID: string(value.WorkspaceID), Name: value.Name, Description: value.Description, TopicStatus: string(value.Status), Version: value.Version, UpdatedAt: formatTime(value.UpdatedAt)}
}

func toRelationDetailResponse(value graphdomain.RelationDetail) relationDetailResponse {
	return relationDetailResponse{Edge: toEdge(value.Edge), Confirmation: toConfirmation(value.Confirmation), Fingerprint: value.Fingerprint, ValidFrom: formatOptionalTime(value.ValidFrom), ValidTo: formatOptionalTime(value.ValidTo), CreatedAt: formatTime(value.CreatedAt), UpdatedAt: formatTime(value.UpdatedAt)}
}

func toRelationEvidenceResponse(value graphdomain.RelationEvidencePage) relationEvidenceResponse {
	items := make([]relationEvidenceItemResponse, len(value.Items))
	for index, item := range value.Items {
		items[index] = relationEvidenceItemResponse{ID: string(item.ID), WorkspaceID: string(item.WorkspaceID), RelationID: string(item.RelationID), Provenance: provenanceResponse{WorkspaceID: string(item.Provenance.WorkspaceID), SourceVersionID: string(item.Provenance.SourceVersionID), SourceSpanID: string(item.Provenance.SourceSpanID)}, Reason: item.Reason, Applicability: toApplicability(item.Applicability), Confirmation: toConfirmation(item.Confirmation), SourceHref: item.SourceHref, SpanHref: item.SpanHref, CreatedAt: formatTime(item.CreatedAt)}
	}
	return relationEvidenceResponse{WorkspaceID: string(value.WorkspaceID), RelationID: string(value.RelationID), Items: items, Meta: toPageMeta(value.Meta)}
}

func toNodes(values []graphdomain.GraphNode) []nodeResponse {
	result := make([]nodeResponse, len(values))
	for index := range values {
		result[index] = toNodeResponse(values[index])
	}
	return result
}

func toEdges(values []graphdomain.GraphEdge) []edgeResponse {
	result := make([]edgeResponse, len(values))
	for index := range values {
		result[index] = toEdge(values[index])
	}
	return result
}

func toEdge(value graphdomain.GraphEdge) edgeResponse {
	return edgeResponse{RelationID: string(value.RelationID), WorkspaceID: string(value.WorkspaceID), Source: toNodeRef(value.Source), Target: toNodeRef(value.Target), Type: string(value.Type), Status: string(value.Status), Traversal: string(value.Traversal), Confidence: value.Confidence, Version: value.Version, EvidenceCount: value.EvidenceCount, EvidenceFingerprint: value.EvidenceFingerprint, EvidenceHref: value.EvidenceHref, UpdatedAt: formatTime(value.UpdatedAt)}
}

func toPageMeta(value graphdomain.PageMeta) pageMetaResponse {
	return pageMetaResponse{Fingerprint: value.Fingerprint, NextCursor: value.NextCursor, Complete: value.Complete, Truncated: value.Truncated, Reason: value.Reason}
}

func toNodeRef(value knowledge.NodeRef) nodeRefResponse {
	return nodeRefResponse{Type: string(value.Type), ID: string(value.ID)}
}

func toNodeRefs(values []knowledge.NodeRef) []nodeRefResponse {
	result := make([]nodeRefResponse, len(values))
	for index := range values {
		result[index] = toNodeRef(values[index])
	}
	return result
}

func toApplicability(value knowledge.Applicability) applicabilityResponse {
	return applicabilityResponse{SchemaVersion: value.SchemaVersion, Value: append(json.RawMessage(nil), value.CanonicalJSON...), Hash: value.Hash}
}

func toConfirmation(value *knowledge.Confirmation) *confirmationResponse {
	if value == nil {
		return nil
	}
	return &confirmationResponse{Method: string(value.Method), Reference: value.Reference}
}

func formatTime(value time.Time) string { return value.UTC().Format(time.RFC3339Nano) }
func formatOptionalTime(value *time.Time) *string {
	if value == nil {
		return nil
	}
	formatted := formatTime(*value)
	return &formatted
}
