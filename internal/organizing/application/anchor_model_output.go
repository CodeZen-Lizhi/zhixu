package application

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	agentdomain "github.com/CodeZen-Lizhi/zhixu/internal/agent/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/organizing/domain"
)

// AnchorModelOutput 只包含模型标签，不能包含模型分配的身份标识。
// 模型目录与持久证明门禁使用同一个解码器。
type AnchorModelOutput struct {
	Recommendation   *AnchorModelRecommendation `json:"recommendation"`
	NoRecommendation bool                       `json:"no_recommendation"`
}
type AnchorModelRecommendation struct {
	Title    string              `json:"title"`
	Kind     string              `json:"kind"`
	Scope    *domain.AnchorScope `json:"scope"`
	Reason   string              `json:"reason"`
	Evidence []string            `json:"evidence"`
}

func DecodeAnchorModelOutput(raw []byte) (AnchorModelOutput, error) {
	var result AnchorModelOutput
	limits := agentdomain.DefaultDecodeLimits()
	limits.MaxDocumentBytes = 32768
	limits.MaxArrayItems = 64
	limits.MaxObjectFields = 6
	object, err := agentdomain.DecodeStrict[map[string]json.RawMessage](raw, limits, nil)
	if err != nil || len(object) != 2 || object["recommendation"] == nil || object["no_recommendation"] == nil {
		return result, AnchorInvalid()
	}
	var no *bool
	if json.Unmarshal(object["no_recommendation"], &no) != nil || no == nil {
		return result, AnchorInvalid()
	}
	result.NoRecommendation = *no
	if bytes.Equal(bytes.TrimSpace(object["recommendation"]), []byte("null")) {
		if !*no {
			return result, AnchorInvalid()
		}
		return result, nil
	}
	if *no {
		return result, AnchorInvalid()
	}
	fields, err := agentdomain.DecodeStrict[map[string]json.RawMessage](object["recommendation"], limits, nil)
	if err != nil || len(fields) != 5 {
		return result, AnchorInvalid()
	}
	for _, key := range []string{"title", "kind", "scope", "reason", "evidence"} {
		if fields[key] == nil {
			return result, AnchorInvalid()
		}
	}
	var recommendation AnchorModelRecommendation
	if json.Unmarshal(object["recommendation"], &recommendation) != nil || !anchorModelText(recommendation.Title, 512) || !anchorModelText(recommendation.Reason, 2048) || len(recommendation.Evidence) < 1 || len(recommendation.Evidence) > 32 {
		return result, AnchorInvalid()
	}
	switch recommendation.Kind {
	case AnchorInitialScopeRecommendation, domain.AnchorScopeAdjustment:
		if recommendation.Scope == nil || recommendation.Scope.Validate() != nil {
			return result, AnchorInvalid()
		}
		scopeFields, e := agentdomain.DecodeStrict[map[string]json.RawMessage](fields["scope"], limits, nil)
		if e != nil || len(scopeFields) != 3 || scopeFields["topics"] == nil || scopeFields["audiences"] == nil || scopeFields["description"] == nil {
			return result, AnchorInvalid()
		}
	case domain.AnchorSourceAssociation:
		if recommendation.Scope != nil {
			return result, AnchorInvalid()
		}
	default:
		return result, AnchorInvalid()
	}
	seen := map[string]bool{}
	for _, label := range recommendation.Evidence {
		if _, ok := AnchorEvidenceIndex(label, 32); !ok || seen[label] {
			return result, AnchorInvalid()
		}
		seen[label] = true
	}
	result.Recommendation = &recommendation
	return result, nil
}
func AnchorEvidenceIndex(label string, count int) (int, bool) {
	if len(label) != 4 || label[0] != 'S' {
		return 0, false
	}
	n, err := strconv.Atoi(label[1:])
	return n - 1, err == nil && n >= 1 && n <= count && fmt.Sprintf("S%03d", n) == label
}
func anchorModelText(text string, max int) bool {
	if text == "" || len(text) > max || !utf8.ValidString(text) || strings.TrimSpace(text) != text {
		return false
	}
	for _, c := range text {
		if unicode.IsControl(c) {
			return false
		}
	}
	return true
}

// BoundAnchorModelOutput 将每个证据标签映射回不可变请求。
// 持久化必须使用此结果，不能使用调用方提供的字段。
type BoundAnchorModelOutput struct {
	Recommendation   *BoundAnchorRecommendation
	NoRecommendation bool
}
type BoundAnchorRecommendation struct {
	Title    string
	Kind     string
	Scope    *domain.AnchorScope
	Reason   string
	Evidence []domain.SynthesisSourceRef
}

func BindAnchorModelOutput(raw []byte, request AnchorRecommendationRequest) (BoundAnchorModelOutput, error) {
	var result BoundAnchorModelOutput
	if !anchorID(request.ID) || !anchorID(request.WorkspaceID) || !anchorID(request.NoteID) || !anchorID(request.BasisRevisionID) || len(request.Evidence) < 1 || len(request.Evidence) > 32 {
		return result, AnchorInvalid()
	}
	initial := request.Kind == AnchorInitialScopeRecommendation
	if initial {
		if request.AnchorID != "" || request.ExpectedScopeVersion != 0 || request.Source != nil {
			return result, AnchorInvalid()
		}
	} else if request.Kind != domain.AnchorSourceAssociation && request.Kind != domain.AnchorScopeAdjustment || !anchorID(request.AnchorID) || request.ExpectedScopeVersion < 1 {
		return result, AnchorInvalid()
	}
	seen := map[string]bool{}
	for _, ref := range request.Evidence {
		key, err := ref.IdentityKey()
		if err != nil || ref.Source.WorkspaceID != request.WorkspaceID || seen[key] || request.Source != nil && ref.Source != *request.Source {
			return result, AnchorInvalid()
		}
		seen[key] = true
	}
	output, err := DecodeAnchorModelOutput(raw)
	if err != nil {
		return result, err
	}
	result.NoRecommendation = output.NoRecommendation
	if output.Recommendation == nil {
		return result, nil
	}
	recommendation := output.Recommendation
	if initial != (recommendation.Kind == AnchorInitialScopeRecommendation) {
		return result, AnchorInvalid()
	}
	bound := &BoundAnchorRecommendation{Title: recommendation.Title, Kind: recommendation.Kind, Scope: recommendation.Scope, Reason: recommendation.Reason, Evidence: make([]domain.SynthesisSourceRef, 0, len(recommendation.Evidence))}
	for _, label := range recommendation.Evidence {
		index, ok := AnchorEvidenceIndex(label, len(request.Evidence))
		if !ok {
			return result, AnchorInvalid()
		}
		bound.Evidence = append(bound.Evidence, request.Evidence[index])
	}
	result.Recommendation = bound
	return result, nil
}
