package domain

import (
	"bytes"
	"encoding/json"
	"io"
	"maps"
)

// ReasoningFunction 是组装时选定的可信产品能力。
// 它绝不从模型输出、用户内容或逐调用 SDK 选项推断。
type ReasoningFunction string

const (
	ReasoningFileProfile            ReasoningFunction = "file_profile"
	ReasoningKnowledgeOrganization  ReasoningFunction = "knowledge_organization"
	ReasoningAnchorScope            ReasoningFunction = "anchor_scope"
	ReasoningMainNoteSynthesis      ReasoningFunction = "main_note_synthesis"
	ReasoningManuscriptSourceReview ReasoningFunction = "manuscript_source_review"
	ReasoningKnowledgeQNA           ReasoningFunction = "knowledge_qna"
	ReasoningWorkspaceAnalysis      ReasoningFunction = "workspace_analysis"
	ReasoningNoteInterview          ReasoningFunction = "note_interview"
)

// ReasoningFunctions 返回封闭且稳定的产品能力集合。
func ReasoningFunctions() []ReasoningFunction {
	return []ReasoningFunction{ReasoningFileProfile, ReasoningKnowledgeOrganization, ReasoningAnchorScope,
		ReasoningMainNoteSynthesis, ReasoningManuscriptSourceReview, ReasoningKnowledgeQNA,
		ReasoningWorkspaceAnalysis, ReasoningNoteInterview}
}

// ValidReasoningFunction 在任何运行时选择前拒绝未知键。
func ValidReasoningFunction(function ReasoningFunction) bool {
	switch function {
	case ReasoningFileProfile, ReasoningKnowledgeOrganization, ReasoningAnchorScope, ReasoningMainNoteSynthesis,
		ReasoningManuscriptSourceReview, ReasoningKnowledgeQNA, ReasoningWorkspaceAnalysis, ReasoningNoteInterview:
		return true
	default:
		return false
	}
}

// ReasoningEffortOverrides 保留三种状态：缺省时继承全局
// 设置，显式空值请求 Provider 默认值，具体等级则固定强度。
type ReasoningEffortOverrides map[ReasoningFunction]string

// Validate 校验限定键及共用的强度取值。
func (overrides ReasoningEffortOverrides) Validate() error {
	if len(overrides) > 8 {
		return invalid("too many reasoning function overrides")
	}
	for function, effort := range overrides {
		if !ValidReasoningFunction(function) || !ValidReasoningEffort(effort) {
			return invalid("reasoning function override is invalid")
		}
	}
	return nil
}

// Clone 将配置输入与不可变版本/运行时边界隔离。
func (overrides ReasoningEffortOverrides) Clone() ReasoningEffortOverrides {
	if overrides == nil {
		return ReasoningEffortOverrides{}
	}
	return maps.Clone(overrides)
}

// Resolve 应用已存在的覆盖值，即使该值显式为空。
func (overrides ReasoningEffortOverrides) Resolve(function ReasoningFunction, fallback string) string {
	if effort, exists := overrides[function]; exists {
		return effort
	}
	return fallback
}

// ValidReasoningEffort 与持久化设置及 Provider 协议取值保持一致。
func ValidReasoningEffort(effort string) bool {
	switch effort {
	case "", "low", "medium", "high", "xhigh", "max":
		return true
	default:
		return false
	}
}

// ParseReasoningEffortOverrides 解码一个有界且限定键的对象，
// 拒绝 null、重复键、非字符串值和尾随文档。
func ParseReasoningEffortOverrides(raw []byte) (ReasoningEffortOverrides, error) {
	if len(raw) == 0 || len(raw) > 4096 {
		return nil, invalid("reasoning function overrides are invalid")
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	token, err := decoder.Token()
	if err != nil || token != json.Delim('{') {
		return nil, invalid("reasoning function overrides must be an object")
	}
	result := ReasoningEffortOverrides{}
	for decoder.More() {
		token, err = decoder.Token()
		if err != nil {
			return nil, invalid("reasoning function overrides are invalid")
		}
		key, ok := token.(string)
		if !ok || !ValidReasoningFunction(ReasoningFunction(key)) {
			return nil, invalid("reasoning function is invalid")
		}
		function := ReasoningFunction(key)
		if _, exists := result[function]; exists {
			return nil, invalid("reasoning function is duplicated")
		}
		var value json.RawMessage
		if decoder.Decode(&value) != nil || bytes.Equal(bytes.TrimSpace(value), []byte("null")) {
			return nil, invalid("reasoning function effort is invalid")
		}
		var effort string
		if json.Unmarshal(value, &effort) != nil || !ValidReasoningEffort(effort) {
			return nil, invalid("reasoning function effort is invalid")
		}
		result[function] = effort
	}
	if token, err = decoder.Token(); err != nil || token != json.Delim('}') {
		return nil, invalid("reasoning function overrides are invalid")
	}
	if _, err = decoder.Token(); err != io.EOF {
		return nil, invalid("reasoning function overrides contain trailing data")
	}
	return result, nil
}
