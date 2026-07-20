package postgres

import (
	"encoding/json"
	"errors"
	"unicode/utf8"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	graphdomain "github.com/CodeZen-Lizhi/zhixu/internal/graph/domain"
	knowledge "github.com/CodeZen-Lizhi/zhixu/internal/knowledge/domain"
)

type rowScanner interface{ Scan(...any) error }

func scanTopicNode(row rowScanner) (graphdomain.TopicNode, error) {
	var node graphdomain.TopicNode
	var id, workspaceID, status string
	if err := row.Scan(&id, &workspaceID, &node.Name, &node.Description, &status, &node.Version, &node.UpdatedAt); err != nil {
		return graphdomain.TopicNode{}, err
	}
	node.Ref = knowledge.NodeRef{Type: knowledge.NodeTypeTopic, ID: foundation.ID(id)}
	node.WorkspaceID = foundation.ID(workspaceID)
	node.Status = knowledge.TopicStatus(status)
	return node, nil
}

func scanClaimNode(row rowScanner) (graphdomain.ClaimNode, error) {
	var node graphdomain.ClaimNode
	var id, workspaceID, status, schemaVersion, applicabilityHash string
	var applicabilityRaw []byte
	if err := row.Scan(&id, &workspaceID, &node.Statement, &applicabilityRaw, &schemaVersion, &applicabilityHash, &status, &node.Confidence, &node.Version, &node.UpdatedAt); err != nil {
		return graphdomain.ClaimNode{}, err
	}
	applicability, err := knowledge.ParseApplicability(json.RawMessage(applicabilityRaw))
	if err != nil || applicability.SchemaVersion != schemaVersion || applicability.Hash != applicabilityHash {
		return graphdomain.ClaimNode{}, errors.New("claim applicability projection is inconsistent")
	}
	node.Statement = displaySummary(node.Statement, 512)
	node.Ref = knowledge.NodeRef{Type: knowledge.NodeTypeClaim, ID: foundation.ID(id)}
	node.WorkspaceID = foundation.ID(workspaceID)
	node.Status = knowledge.ClaimStatus(status)
	node.Applicability = applicability
	return node, nil
}

func displaySummary(value string, maximumBytes int) string {
	if len(value) <= maximumBytes {
		return value
	}
	value = value[:maximumBytes]
	for !utf8.ValidString(value) {
		value = value[:len(value)-1]
	}
	return value
}
