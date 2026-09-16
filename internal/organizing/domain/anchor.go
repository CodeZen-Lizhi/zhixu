package domain

import (
	"strings"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
)

// Anchor 是绑定到一份笔记的稳定目标。范围修订
// 独立于笔记正文及其发布指针。
type Anchor struct {
	ID              foundation.ID `json:"id"`
	WorkspaceID     foundation.ID `json:"workspace_id"`
	NoteID          foundation.ID `json:"note_id"`
	BasisRevisionID foundation.ID `json:"basis_revision_id"`
	Title           string        `json:"title"`
	Scope           AnchorScope   `json:"scope"`
	ScopeVersion    int64         `json:"scope_version"`
	Version         int64         `json:"version"`
	CreatedAt       time.Time     `json:"created_at"`
	UpdatedAt       time.Time     `json:"updated_at"`
}
type AnchorScope struct {
	Topics      []string `json:"topics"`
	Audiences   []string `json:"audiences"`
	Description string   `json:"description"`
}
type AnchorDecision string

// AnchorRecommendationStatus 表达有模型依据的建议的持久生命周期。
// 它与 AnchorProposal 决策不同：请求可能正常完成而不提出任何相关建议。
type AnchorRecommendationStatus string

const (
	AnchorPending  AnchorDecision = "PENDING"
	AnchorAccepted AnchorDecision = "ACCEPTED"
	AnchorRejected AnchorDecision = "REJECTED"
)

const (
	AnchorRecommendationPending          AnchorRecommendationStatus = "PENDING"
	AnchorRecommendationRunning          AnchorRecommendationStatus = "RUNNING"
	AnchorRecommendationSucceeded        AnchorRecommendationStatus = "SUCCEEDED"
	AnchorRecommendationNoRecommendation AnchorRecommendationStatus = "NO_RECOMMENDATION"
	AnchorRecommendationFailed           AnchorRecommendationStatus = "FAILED"
	AnchorRecommendationRecoveryRequired AnchorRecommendationStatus = "RECOVERY_REQUIRED"
)

func (status AnchorRecommendationStatus) Valid() bool {
	return status == AnchorRecommendationPending || status == AnchorRecommendationRunning || status == AnchorRecommendationSucceeded ||
		status == AnchorRecommendationNoRecommendation || status == AnchorRecommendationFailed || status == AnchorRecommendationRecoveryRequired
}

// 建议创建后不可变，决策是单独的事实。
type AnchorProposal struct {
	ID           foundation.ID        `json:"id"`
	WorkspaceID  foundation.ID        `json:"workspace_id"`
	AnchorID     foundation.ID        `json:"anchor_id"`
	Kind         string               `json:"kind"`
	ScopeVersion int64                `json:"scope_version"`
	Before       *AnchorScope         `json:"before"`
	Suggested    *AnchorScope         `json:"suggested"`
	Reason       string               `json:"reason"`
	Evidence     []SynthesisSourceRef `json:"evidence"`
	ModelRunID   foundation.ID        `json:"model_run_id"`
	Status       AnchorDecision       `json:"status"`
	Version      int64                `json:"version"`
	CreatedAt    time.Time            `json:"created_at"`
}

const (
	AnchorScopeAdjustment   = "SCOPE_ADJUSTMENT"
	AnchorSourceAssociation = "SOURCE_ASSOCIATION"
)

func (s AnchorScope) Validate() error {
	if !anchorText(s.Description, 2048) || len(s.Topics) == 0 || len(s.Topics) > 64 || len(s.Audiences) == 0 || len(s.Audiences) > 32 {
		return invalid("ANCHOR_INVALID", "anchor scope is invalid")
	}
	for _, values := range [][]string{s.Topics, s.Audiences} {
		seen := map[string]bool{}
		for _, v := range values {
			if !anchorText(v, 256) || seen[v] {
				return invalid("ANCHOR_INVALID", "anchor scope entry is invalid")
			}
			seen[v] = true
		}
	}
	return nil
}
func (a Anchor) Validate() error {
	for _, id := range []foundation.ID{a.ID, a.WorkspaceID, a.NoteID, a.BasisRevisionID} {
		if !validID(id) {
			return invalid("ANCHOR_INVALID", "anchor identity is invalid")
		}
	}
	if !anchorText(a.Title, 512) || a.ScopeVersion < 1 || a.Version < 1 || a.CreatedAt.IsZero() || a.UpdatedAt.Before(a.CreatedAt) {
		return invalid("ANCHOR_INVALID", "anchor metadata is invalid")
	}
	return a.Scope.Validate()
}
func (p AnchorProposal) Validate(workspace foundation.ID) error {
	if !validID(p.ID) || p.WorkspaceID != workspace || !validID(workspace) || !validID(p.AnchorID) || !validID(p.ModelRunID) || p.ScopeVersion < 1 || p.Version < 1 || p.CreatedAt.IsZero() || !anchorText(p.Reason, 2048) || len(p.Evidence) < 1 || len(p.Evidence) > 32 {
		return invalid("ANCHOR_INVALID", "anchor proposal is invalid")
	}
	if p.Status != AnchorPending && p.Status != AnchorAccepted && p.Status != AnchorRejected {
		return invalid("ANCHOR_INVALID", "anchor decision is invalid")
	}
	if p.Kind == AnchorScopeAdjustment {
		if p.Before == nil || p.Suggested == nil || p.Before.Validate() != nil || p.Suggested.Validate() != nil {
			return invalid("ANCHOR_INVALID", "scope recommendation is invalid")
		}
	} else if p.Kind != AnchorSourceAssociation || p.Before != nil || p.Suggested != nil {
		return invalid("ANCHOR_INVALID", "association cannot modify scope")
	}
	seen := map[foundation.ID]bool{}
	for _, ref := range p.Evidence {
		if ref.Validate() != nil || ref.Source.WorkspaceID != workspace || seen[ref.SourceSpanID] {
			return invalid("ANCHOR_INVALID", "proposal evidence is invalid")
		}
		seen[ref.SourceSpanID] = true
	}
	return nil
}
func anchorText(value string, max int) bool {
	return value != "" && value == strings.TrimSpace(value) && len(value) <= max
}
