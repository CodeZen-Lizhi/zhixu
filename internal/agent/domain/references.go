package domain

import (
	"strings"
	"unicode/utf8"
)

const (
	maxReferenceIDBytes      = 128
	maxReferenceVersionBytes = 64
)

// ModelRef 固化一次调用实际使用的 Adapter 与模型版本。
type ModelRef struct {
	AdapterName    string `json:"adapter_name"`
	AdapterVersion string `json:"adapter_version"`
	ModelID        string `json:"model_id"`
	ModelVersion   string `json:"model_version"`
}

// Validate 校验 ModelRef 完整且为 canonical 文本。
func (ref ModelRef) Validate() error {
	if !canonicalReference(ref.AdapterName, maxReferenceIDBytes) ||
		!canonicalReference(ref.AdapterVersion, maxReferenceVersionBytes) ||
		!canonicalReference(ref.ModelID, maxReferenceIDBytes) ||
		!canonicalReference(ref.ModelVersion, maxReferenceVersionBytes) {
		return invalid(ErrorCodeReferenceInvalid, "model reference is invalid")
	}
	return nil
}

// ModelProfileRef 固化模型 Profile 的稳定 ID 与版本。
type ModelProfileRef struct {
	ID      string `json:"id"`
	Version string `json:"version"`
}

// Validate 校验 ModelProfileRef 完整且为 canonical 文本。
func (ref ModelProfileRef) Validate() error {
	return validateVersionedRef(ref.ID, ref.Version, "model profile")
}

// PromptRef 固化 Prompt 模板的稳定 ID 与版本。
type PromptRef struct {
	ID      string `json:"id"`
	Version string `json:"version"`
}

// Validate 校验 PromptRef 完整且为 canonical 文本。
func (ref PromptRef) Validate() error {
	return validateVersionedRef(ref.ID, ref.Version, "prompt")
}

// SchemaRef 固化结构化输出 Schema 的稳定 ID 与版本。
type SchemaRef struct {
	ID      string `json:"id"`
	Version string `json:"version"`
}

// Validate 校验 SchemaRef 完整且为 canonical 文本。
func (ref SchemaRef) Validate() error {
	return validateVersionedRef(ref.ID, ref.Version, "schema")
}

func validateVersionedRef(id, version, kind string) error {
	if !canonicalReference(id, maxReferenceIDBytes) || !canonicalReference(version, maxReferenceVersionBytes) {
		return invalid(ErrorCodeReferenceInvalid, kind+" reference is invalid")
	}
	return nil
}

func canonicalReference(value string, maximum int) bool {
	if value == "" || len(value) > maximum || !utf8.ValidString(value) || strings.TrimSpace(value) != value {
		return false
	}
	for _, character := range value {
		if character < 0x21 || character == 0x7f {
			return false
		}
	}
	return true
}
