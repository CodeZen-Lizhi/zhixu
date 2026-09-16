package domain

import (
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"unicode/utf8"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
)

// SynthesisManuscriptVersion 独立于保持不变的 v1 渲染器。
const SynthesisManuscriptVersion = "synthesis-manuscript/v2"

// 完整全文须保持在既有 Git 三方合并输入上限内。
const MaxSynthesisManuscriptBytes = 1 << 20

// 不能为满足此上限而丢弃历史排除项。
// 累计集合超限时，所属模块必须停止处理并等待复核。
const MaxSynthesisManuscriptExclusions = MaxSynthesisItems

const ErrorCodeSynthesisManuscriptHistoryReviewRequired = "SYNTHESIS_MANUSCRIPT_HISTORY_REVIEW_REQUIRED"

// SynthesisManuscriptMachine 是经过服务端验证的输入，不是公开请求 DTO。
// 应用层须将其绑定到模型、来源、范围和合并基线。
// IneligibleItemIDs 延续此前被改写或不可信的身份；人工批准或恢复旧字节都不能清除此排除。
// 这是跨版本保留的墓碑集合，有效且唯一的 ID 可能不在当前 MachineItems 中。
// 所属模块必须完整延续此集合，不能与当前投影取交集，避免移除后重新引入使旧 ID 恢复信任。
type SynthesisManuscriptMachine struct {
	WorkspaceID       foundation.ID   `json:"workspace_id"`
	NoteID            foundation.ID   `json:"note_id"`
	MachineTitle      string          `json:"machine_title"`
	MachineItems      []SynthesisItem `json:"machine_items"`
	IneligibleItemIDs []foundation.ID `json:"ineligible_item_ids"`
}

type SynthesisManuscriptMapping struct {
	ItemID    foundation.ID `json:"item_id"`
	Start     int           `json:"start"`
	End       int           `json:"end"`
	BlockHash string        `json:"block_hash"`
}
type SynthesisManuscriptReview struct {
	ItemID foundation.ID `json:"item_id"`
	Reason string        `json:"reason"`
}

const (
	ManuscriptChangedOrMissing    = "CHANGED_OR_MISSING"
	ManuscriptAmbiguous           = "AMBIGUOUS"
	ManuscriptContextReview       = "CONTEXT_REVIEW"
	ManuscriptPreviouslyUntrusted = "PREVIOUSLY_UNTRUSTED"
)

type SynthesisManuscriptAssessment struct {
	Mappings              []SynthesisManuscriptMapping `json:"mappings"`
	ReviewItems           []SynthesisManuscriptReview  `json:"review_items"`
	ContextReviewRequired bool                         `json:"context_review_required"`
}

// SynthesisManuscriptMapper 必须解析块结构，不能接受客户端映射。
// 领域层不依赖具体 Markdown 解析器。
type SynthesisManuscriptMapper interface {
	Assess(SynthesisManuscriptMachine, string) (SynthesisManuscriptAssessment, error)
}

// SynthesisManuscript 保留最终全文的唯一完整字节序列。MachineItems
// 是更新与审计基线，不是全文对外可信的 Items。
// 哈希用于发现损坏，不是合并回执或来源授权。
type SynthesisManuscript struct {
	Version            string                        `json:"version"`
	Machine            SynthesisManuscriptMachine    `json:"machine"`
	FullContent        string                        `json:"full_content"`
	MachineContentHash string                        `json:"machine_content_hash"`
	ContentHash        string                        `json:"content_hash"`
	ManualChanges      bool                          `json:"manual_changes"`
	Assessment         SynthesisManuscriptAssessment `json:"assessment"`
	Hash               string                        `json:"hash"`
}

func (machine SynthesisManuscriptMachine) Validate() error {
	if !validID(machine.WorkspaceID) || !validID(machine.NoteID) || len(machine.MachineItems) == 0 {
		return manuscriptInvalid("machine identity or items missing")
	}
	rendered, err := RenderSynthesisMarkdown(machine.WorkspaceID, machine.NoteID, machine.MachineTitle, machine.MachineItems)
	if err != nil {
		return err
	}
	if len(rendered) > MaxSynthesisManuscriptBytes {
		return manuscriptInvalid("machine manuscript exceeds merge bound")
	}
	for _, item := range machine.MachineItems {
		if item.BodyReference != nil && item.BodyReference.NoteID == machine.NoteID {
			return manuscriptInvalid("self body reference")
		}
	}
	if len(machine.IneligibleItemIDs) > MaxSynthesisManuscriptExclusions {
		return foundation.NewError(foundation.ErrorManualRecoveryRequired, ErrorCodeSynthesisManuscriptHistoryReviewRequired, false, errors.New("historical exclusion limit exceeded; review required"))
	}
	seen := map[foundation.ID]bool{}
	for _, id := range machine.IneligibleItemIDs {
		if !validID(id) || seen[id] {
			return manuscriptInvalid("invalid ineligible item")
		}
		seen[id] = true
	}
	return nil
}

func NewSynthesisManuscript(machine SynthesisManuscriptMachine, fullContent string, mapper SynthesisManuscriptMapper) (SynthesisManuscript, error) {
	if err := machine.Validate(); err != nil {
		return SynthesisManuscript{}, err
	}
	if !utf8.ValidString(fullContent) || strings.ContainsRune(fullContent, '\x00') || len(fullContent) > MaxSynthesisManuscriptBytes {
		return SynthesisManuscript{}, manuscriptInvalid("full manuscript must be bounded UTF-8 without NUL")
	}
	if mapper == nil {
		return SynthesisManuscript{}, manuscriptInvalid("manuscript mapper missing")
	}
	// 不保留调用方持有的条目或来源切片。
	machine.MachineItems = cloneSynthesisItems(machine.MachineItems)
	machine.IneligibleItemIDs = append([]foundation.ID{}, machine.IneligibleItemIDs...)
	assessment, err := mapper.Assess(machine, fullContent)
	if err != nil {
		return SynthesisManuscript{}, err
	}
	rendered, err := RenderSynthesisMarkdown(machine.WorkspaceID, machine.NoteID, machine.MachineTitle, machine.MachineItems)
	if err != nil {
		return SynthesisManuscript{}, err
	}
	out := SynthesisManuscript{Version: SynthesisManuscriptVersion, Machine: machine, FullContent: fullContent, MachineContentHash: synthesisHash([]byte(rendered)), ContentHash: synthesisHash([]byte(fullContent)), ManualChanges: fullContent != rendered, Assessment: assessment}
	if err := out.validateRanges(); err != nil {
		return SynthesisManuscript{}, err
	}
	out.Hash, err = manuscriptHash(out)
	return out, err
}

// ValidateIntegrity 仅检查封装自身的结构、UTF-8 字节范围和哈希。
// 修订或快照的领域验证可在不依赖解析器时调用它。成功不等于 Markdown 映射或来源证明：
// 客户端仍可伪造内部一致的映射并重算所有哈希。
// 应用层和存储所属模块仍须使用可信映射器、独立验证的机器基线调用 Validate，并绑定合并回执。
func (manuscript SynthesisManuscript) ValidateIntegrity() error {
	if manuscript.Version != SynthesisManuscriptVersion {
		return manuscriptInvalid("unsupported manuscript version")
	}
	if err := manuscript.Machine.Validate(); err != nil {
		return err
	}
	if !utf8.ValidString(manuscript.FullContent) || strings.ContainsRune(manuscript.FullContent, '\x00') || len(manuscript.FullContent) > MaxSynthesisManuscriptBytes {
		return manuscriptInvalid("full manuscript must be bounded UTF-8 without NUL")
	}
	rendered, err := RenderSynthesisMarkdown(manuscript.Machine.WorkspaceID, manuscript.Machine.NoteID, manuscript.Machine.MachineTitle, manuscript.Machine.MachineItems)
	if err != nil {
		return err
	}
	if manuscript.MachineContentHash != synthesisHash([]byte(rendered)) || manuscript.ContentHash != synthesisHash([]byte(manuscript.FullContent)) || manuscript.ManualChanges != (manuscript.FullContent != rendered) {
		return manuscriptInvalid("manuscript content integrity mismatch")
	}
	if err := manuscript.validateRanges(); err != nil {
		return err
	}
	hash, err := manuscriptHash(manuscript)
	if err != nil {
		return err
	}
	if manuscript.Hash != hash {
		return manuscriptInvalid("manuscript envelope hash mismatch")
	}
	return nil
}

// Validate 在应用层和存储所属模块的创建与重读边界，
// 根据预期服务端基线重新计算解析器派生的映射。与 ValidateIntegrity 不同，
// 它会拒绝重算哈希的伪造映射和所属模块重新绑定。来源/范围核验及合并回执绑定由调用方负责；
// 此方法和映射器均不验证这些外部证明。
func (manuscript SynthesisManuscript) Validate(expected SynthesisManuscriptMachine, mapper SynthesisManuscriptMapper) error {
	rebuilt, err := NewSynthesisManuscript(expected, manuscript.FullContent, mapper)
	if err != nil {
		return err
	}
	if !reflect.DeepEqual(rebuilt, manuscript) {
		return manuscriptInvalid("manuscript differs from verified machine mapping")
	}
	return nil
}

// TrustedItems 是唯一可用于后续 v2 顶层 Items 的子集，保留机器投影顺序，允许为空。
// 人工文本不能通过 MachineItems 进入面试、事实推断或正文包含流程。
func (manuscript SynthesisManuscript) TrustedItems(expected SynthesisManuscriptMachine, mapper SynthesisManuscriptMapper) ([]SynthesisItem, error) {
	if err := manuscript.Validate(expected, mapper); err != nil {
		return nil, err
	}
	mapped := map[foundation.ID]bool{}
	for _, m := range manuscript.Assessment.Mappings {
		mapped[m.ItemID] = true
	}
	items := []SynthesisItem{}
	for _, item := range manuscript.Machine.MachineItems {
		if mapped[item.ID] {
			items = append(items, item)
		}
	}
	return cloneSynthesisItems(items), nil
}

func (manuscript SynthesisManuscript) validateRanges() error {
	known := map[foundation.ID]bool{}
	excluded := map[foundation.ID]bool{}
	seen := map[foundation.ID]bool{}
	for _, item := range manuscript.Machine.MachineItems {
		known[item.ID] = true
	}
	for _, id := range manuscript.Machine.IneligibleItemIDs {
		excluded[id] = true
	}
	end := 0
	for _, m := range manuscript.Assessment.Mappings {
		if !known[m.ItemID] || excluded[m.ItemID] || seen[m.ItemID] || m.Start < end || m.End <= m.Start || m.End > len(manuscript.FullContent) || !utf8.ValidString(manuscript.FullContent[:m.Start]) || !utf8.ValidString(manuscript.FullContent[m.Start:m.End]) || m.BlockHash != synthesisHash([]byte(manuscript.FullContent[m.Start:m.End])) {
			return manuscriptInvalid("invalid manuscript byte mapping")
		}
		seen[m.ItemID] = true
		end = m.End
	}
	for _, r := range manuscript.Assessment.ReviewItems {
		if !known[r.ItemID] || seen[r.ItemID] || (r.Reason != ManuscriptChangedOrMissing && r.Reason != ManuscriptAmbiguous && r.Reason != ManuscriptContextReview && r.Reason != ManuscriptPreviouslyUntrusted) {
			return manuscriptInvalid("invalid manuscript review item")
		}
		seen[r.ItemID] = true
	}
	if len(seen) != len(known) {
		return manuscriptInvalid("incomplete manuscript assessment")
	}
	return nil
}
func manuscriptHash(value SynthesisManuscript) (string, error) {
	value.Hash = ""
	b, err := json.Marshal(value)
	if err != nil {
		return "", manuscriptInvalid("cannot encode manuscript")
	}
	return synthesisHash(b), nil
}
func manuscriptInvalid(message string) error {
	return invalid(ErrorCodeSynthesisRevisionInvalid, message)
}
