// Package capability 定义跨模块共享的普通能力词汇。
package capability

import (
	"errors"
	"strings"
)

// Capability 是服务端授权与 Workflow 声明使用的普通能力。
type Capability string

const (
	// ReadLocal 允许读取当前 Workspace 内的受控对象。
	ReadLocal Capability = "READ_LOCAL"
	// ReadExternal 允许访问策略明确允许的外部公开资源。
	ReadExternal Capability = "READ_EXTERNAL"
	// WriteProposal 允许创建候选内容或 Proposal。
	WriteProposal Capability = "WRITE_PROPOSAL"
	// WriteKnowledge 允许进入已批准的知识写回流程。
	WriteKnowledge Capability = "WRITE_KNOWLEDGE"
	// GitWrite 允许进入已批准的 Git 提交流程。
	GitWrite Capability = "GIT_WRITE"
	// IndexMaintenance 允许执行受控的索引维护操作。
	IndexMaintenance Capability = "INDEX_MAINTENANCE"
	// EvaluationRun 允许运行受控的版本化评测。
	EvaluationRun Capability = "EVALUATION_RUN"
	// ManageSystemSettings 允许管理实例级模型与运行设置。
	ManageSystemSettings Capability = "MANAGE_SYSTEM_SETTINGS"
)

var (
	// ErrInvalid 表示输入不是 canonical Capability。
	ErrInvalid = errors.New("capability is not canonical")
	all        = [...]Capability{
		ReadLocal,
		ReadExternal,
		WriteProposal,
		WriteKnowledge,
		GitWrite,
		IndexMaintenance,
		EvaluationRun,
		ManageSystemSettings,
	}
)

// All 返回全部 canonical Capability 的独立副本。
func All() []Capability {
	result := make([]Capability, len(all))
	copy(result, all[:])
	return result
}

// Parse 去除首尾空白并解析 canonical Capability。
func Parse(value string) (Capability, error) {
	parsed := Capability(strings.TrimSpace(value))
	if IsKnown(parsed) {
		return parsed, nil
	}
	return "", ErrInvalid
}

// IsKnown 判断输入是否为未带额外空白的 canonical Capability。
func IsKnown(value Capability) bool {
	switch value {
	case ReadLocal, ReadExternal, WriteProposal, WriteKnowledge, GitWrite, IndexMaintenance, EvaluationRun, ManageSystemSettings:
		return true
	default:
		return false
	}
}

// Validate 校验输入已经是未带额外空白的 canonical Capability。
func Validate(value Capability) error {
	if !IsKnown(value) {
		return ErrInvalid
	}
	return nil
}
