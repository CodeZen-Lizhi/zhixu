package application

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"sort"

	"github.com/CodeZen-Lizhi/zhixu/internal/capability"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	workflowapp "github.com/CodeZen-Lizhi/zhixu/internal/workflow/application"
)

// SynthesisManuscriptCaller 是每个请求独立复制的已认证能力集合。
// 其零值始终被拒绝，包括开发环境；上下文标志不构成授权。
type SynthesisManuscriptCaller struct{ capabilities []capability.Capability }

func NewSynthesisManuscriptCaller(capabilities []capability.Capability) SynthesisManuscriptCaller {
	return SynthesisManuscriptCaller{capabilities: append([]capability.Capability{}, capabilities...)}
}
func (c SynthesisManuscriptCaller) Capabilities() []capability.Capability {
	return append([]capability.Capability{}, c.capabilities...)
}
func (c SynthesisManuscriptCaller) Authorize() error {
	found := map[capability.Capability]bool{}
	for _, permission := range c.capabilities {
		found[permission] = true
	}
	if !found[capability.ReadLocal] || !found[capability.WriteProposal] {
		return foundation.NewError(foundation.ErrorPermissionDenied, "SYNTHESIS_MANUSCRIPT_HUMAN_FORBIDDEN", false, errors.New("authenticated read and proposal capabilities are required"))
	}
	return nil
}

// 任务只接受所属模块的结果身份。枚举同时冻结完整笔记集合，
// 不暴露模型日志、权限或任何可编辑正文。
func SynthesisManuscriptHumanSchema(processing, run foundation.ID, notes []foundation.ID) (json.RawMessage, error) {
	ordered := append([]foundation.ID{}, notes...)
	sort.Slice(ordered, func(i, j int) bool { return ordered[i] < ordered[j] })
	return json.Marshal(map[string]any{
		"type": "object", "additionalProperties": false,
		"required": []string{"version", "processing_id", "workflow_run_id", "note_ids", "result_hash"},
		"properties": map[string]any{
			"version":         map[string]any{"type": "string", "enum": []string{SynthesisManuscriptReviewVersion}},
			"processing_id":   map[string]any{"type": "string", "enum": []foundation.ID{processing}},
			"workflow_run_id": map[string]any{"type": "string", "enum": []foundation.ID{run}},
			"note_ids":        map[string]any{"type": "array", "enum": [][]foundation.ID{ordered}},
			"result_hash":     map[string]any{"type": "string", "pattern": "^[0-9a-f]{64}$"},
		},
	})
}

type SynthesisManuscriptReviewSummary struct {
	Binding   SynthesisManuscriptHumanBinding `json:"binding"`
	Ready     bool                            `json:"ready"`
	Submitted bool                            `json:"submitted"`
	Targets   []SynthesisManuscriptReviewItem `json:"targets"`
}
type SynthesisManuscriptReviewItem struct {
	NoteID             foundation.ID `json:"note_id"`
	AttemptID          foundation.ID `json:"attempt_id"`
	CaptureID          foundation.ID `json:"capture_id"`
	Stage              string        `json:"stage,omitempty"`
	PreviewFingerprint string        `json:"preview_fingerprint"`
	Ready              bool          `json:"ready"`
}
type SynthesisManuscriptReviewDetail struct {
	Binding SynthesisManuscriptHumanBinding `json:"binding"`
	Target  SynthesisManuscriptReviewItem   `json:"target"`
	Review  *SynthesisManuscriptMergeReview `json:"review,omitempty"`
}
type SynthesisManuscriptHumanTaskReader interface {
	ReadSynthesisManuscriptHumanTask(context.Context, foundation.ID, foundation.ID) (SynthesisManuscriptHumanBinding, bool, error)
}
type SynthesisManuscriptReviewService struct {
	store  SynthesisManuscriptReviewStore
	tasks  SynthesisManuscriptHumanTaskReader
	human  *workflowapp.RuntimeHumanCoordinator
	caller SynthesisManuscriptCaller
}

func NewSynthesisManuscriptReviewService(store SynthesisManuscriptReviewStore, tasks SynthesisManuscriptHumanTaskReader, human *workflowapp.RuntimeHumanCoordinator, caller SynthesisManuscriptCaller) (*SynthesisManuscriptReviewService, error) {
	if store == nil || tasks == nil || human == nil {
		return nil, manuscriptMergeInvalid("review service dependencies missing")
	}
	if err := caller.Authorize(); err != nil {
		return nil, err
	}
	return &SynthesisManuscriptReviewService{store: store, tasks: tasks, human: human, caller: caller}, nil
}
func reviewItem(t SynthesisManuscriptReviewTarget) SynthesisManuscriptReviewItem {
	out := SynthesisManuscriptReviewItem{NoteID: t.Attempt.Command.NoteID, AttemptID: t.Attempt.ID, CaptureID: t.Attempt.CaptureID, PreviewFingerprint: t.Preview.Fingerprint, Ready: t.Receipt != nil}
	if t.Preview.Review != nil {
		out.Stage = t.Preview.Review.Stage
	}
	return out
}
func reviewSummary(m SynthesisManuscriptReviewManifest, submitted bool) SynthesisManuscriptReviewSummary {
	out := SynthesisManuscriptReviewSummary{Binding: m.Binding, Ready: m.Ready, Submitted: submitted, Targets: []SynthesisManuscriptReviewItem{}}
	for _, t := range m.Targets {
		out.Targets = append(out.Targets, reviewItem(t))
	}
	return out
}
func (s *SynthesisManuscriptReviewService) Read(ctx context.Context, workspace, processing foundation.ID) (SynthesisManuscriptReviewSummary, error) {
	b, submitted, err := s.tasks.ReadSynthesisManuscriptHumanTask(ctx, workspace, processing)
	if err != nil {
		return SynthesisManuscriptReviewSummary{}, err
	}
	m, err := s.store.ReadManifest(ctx, b)
	if err != nil {
		return SynthesisManuscriptReviewSummary{}, err
	}
	return reviewSummary(m, submitted), nil
}
func (s *SynthesisManuscriptReviewService) ReadNote(ctx context.Context, workspace, processing, note foundation.ID) (SynthesisManuscriptReviewDetail, error) {
	b, _, err := s.tasks.ReadSynthesisManuscriptHumanTask(ctx, workspace, processing)
	if err != nil {
		return SynthesisManuscriptReviewDetail{}, err
	}
	m, err := s.store.ReadManifest(ctx, b)
	if err != nil {
		return SynthesisManuscriptReviewDetail{}, err
	}
	for _, t := range m.Targets {
		if t.Attempt.Command.NoteID == note {
			return SynthesisManuscriptReviewDetail{Binding: b, Target: reviewItem(t), Review: t.Preview.Review}, nil
		}
	}
	return SynthesisManuscriptReviewDetail{}, manuscriptMergeInvalid("note is not a review target")
}

// Decide 在提交通用 HumanTask 前精确持久保存一个阶段。
// 丢失响应后的重试恢复同一份账本，再完成同一个任务。
func (s *SynthesisManuscriptReviewService) Decide(ctx context.Context, c DecideSynthesisManuscript) (SynthesisManuscriptReviewSummary, error) {
	if _, err := s.store.Decide(ctx, c); err != nil {
		return SynthesisManuscriptReviewSummary{}, err
	}
	m, err := s.store.ReadManifest(ctx, c.Binding)
	if err != nil {
		return SynthesisManuscriptReviewSummary{}, err
	}
	if !m.Ready {
		return reviewSummary(m, false), nil
	}
	return s.submitReady(ctx, m)
}

// Resume 只使用不可变绑定恢复最终回执与任务提交之间的间隙。
// ReadManifest 会重新授权调用方、根目录和精确任务。
func (s *SynthesisManuscriptReviewService) Resume(ctx context.Context, binding SynthesisManuscriptHumanBinding) (SynthesisManuscriptReviewSummary, error) {
	m, err := s.store.ReadManifest(ctx, binding)
	if err != nil {
		return SynthesisManuscriptReviewSummary{}, err
	}
	return s.submitReady(ctx, m)
}

func (s *SynthesisManuscriptReviewService) submitReady(ctx context.Context, m SynthesisManuscriptReviewManifest) (SynthesisManuscriptReviewSummary, error) {
	decision, err := SynthesisManuscriptOwnerResult(m)
	if err != nil {
		return SynthesisManuscriptReviewSummary{}, err
	}
	_, err = s.human.SubmitHuman(ctx, workflowapp.HumanDecisionCommand{RunID: m.Binding.RunID, TaskID: m.Binding.HumanTaskID, TargetVersion: m.Binding.TargetVersion, Decision: decision, CallerCapabilities: s.caller.Capabilities()})
	if err != nil {
		return SynthesisManuscriptReviewSummary{}, err
	}
	return reviewSummary(m, true), nil
}
func SynthesisManuscriptOwnerResult(m SynthesisManuscriptReviewManifest) (json.RawMessage, error) {
	if !m.Ready || len(m.Targets) == 0 {
		return nil, manuscriptMergeInvalid("all review receipts are required")
	}
	type identity struct {
		NoteID    foundation.ID `json:"note_id"`
		ReceiptID foundation.ID `json:"receipt_id"`
		Hash      string        `json:"hash"`
	}
	identities := []identity{}
	for _, t := range m.Targets {
		if t.Receipt == nil {
			return nil, manuscriptMergeInvalid("missing review receipt")
		}
		identities = append(identities, identity{t.Attempt.Command.NoteID, t.Receipt.ID, t.Receipt.Hash})
	}
	sort.Slice(identities, func(i, j int) bool { return identities[i].NoteID < identities[j].NoteID })
	raw, err := json.Marshal(identities)
	if err != nil {
		return nil, err
	}
	sum := sha256.Sum256(raw)
	notes := []foundation.ID{}
	for _, id := range identities {
		notes = append(notes, id.NoteID)
	}
	return json.Marshal(struct {
		Version      string          `json:"version"`
		ProcessingID foundation.ID   `json:"processing_id"`
		RunID        foundation.ID   `json:"workflow_run_id"`
		Notes        []foundation.ID `json:"note_ids"`
		Hash         string          `json:"result_hash"`
	}{SynthesisManuscriptReviewVersion, m.Binding.ProcessingID, m.Binding.RunID, notes, hex.EncodeToString(sum[:])})
}
