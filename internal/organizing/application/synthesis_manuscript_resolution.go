package application

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"

	changeapp "github.com/CodeZen-Lizhi/zhixu/internal/changecontrol/application"
	changedomain "github.com/CodeZen-Lizhi/zhixu/internal/changecontrol/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/organizing/domain"
)

// SynthesisManuscriptResolution 确认一次精确服务端预览中的每个冲突。
// 它是内容输入，不是审批或来源证明。所属模块须授权操作人并持久保存此决策，
// 之后才能解除 HumanWait。
type SynthesisManuscriptResolution struct {
	Stage                string `json:"stage"`
	PreviewFingerprint   string `json:"preview_fingerprint"`
	AcknowledgedOrdinals []int  `json:"acknowledged_ordinals"`
	FinalContent         string `json:"final_content"`
}

// SynthesisManuscriptResolvedPreview 仍可能包含外层阶段的 Review。
// 只有 Manuscript 非 nil 且 Review 为 nil 才表示两个合并阶段均已完成。
type SynthesisManuscriptResolvedPreview struct {
	Preview     SynthesisManuscriptMergePreview `json:"preview"`
	Resolutions []SynthesisManuscriptResolution `json:"resolutions"`
	Hash        string                          `json:"hash"`
}

// ResolveSynthesisManuscript 重放冻结的合并，仅应用对应精确冲突预览的决策。
// 第一阶段裁决可能暴露第二阶段冲突，后者须依据其独立指纹再次决策。
// 任何决策都不会修改文件、发布笔记或将人工文本标记为已验证。
func ResolveSynthesisManuscript(ctx context.Context, input SynthesisManuscriptMergeInput, resolutions []SynthesisManuscriptResolution, engine changeapp.RevisionMergeEngine, mapper domain.SynthesisManuscriptMapper) (SynthesisManuscriptResolvedPreview, error) {
	if engine == nil || len(resolutions) == 0 || len(resolutions) > 2 {
		return SynthesisManuscriptResolvedPreview{}, manuscriptMergeInvalid("one or two conflict decisions are required")
	}
	for _, resolution := range resolutions {
		if !validManuscriptMergeText(resolution.FinalContent) || len(resolution.AcknowledgedOrdinals) == 0 || len(resolution.AcknowledgedOrdinals) > 1024 {
			return SynthesisManuscriptResolvedPreview{}, manuscriptMergeInvalid("invalid conflict decision")
		}
	}
	call, consumed := 0, 0
	inner := input.Latest.Manuscript != nil && input.Latest.Manuscript.ManualChanges
	resolving := changeapp.RevisionMergeEngineFunc(func(ctx context.Context, documents changeapp.RevisionMergeDocuments) (changeapp.RevisionMergeResult, error) {
		stage := SynthesisMergeWorkspaceStage
		if call == 0 && inner {
			stage = SynthesisMergeCandidateStage
		}
		call++
		candidate, review, err := mergeManuscriptStage(ctx, engine, stage, string(documents.Base), string(documents.Current), string(documents.Proposed))
		if err != nil {
			return changeapp.RevisionMergeResult{}, err
		}
		out := changeapp.RevisionMergeResult{Candidate: []byte(candidate), Algorithm: changedomain.ProposalRevisionMergeAlgorithm, Contract: changedomain.ProposalRevisionMergeAlgorithmVersion}
		if review == nil {
			return out, nil
		}
		if consumed == len(resolutions) {
			out.Candidate, out.Conflicts = []byte(review.Candidate), review.Conflicts
			return out, nil
		}
		preview, err := fingerprintManuscriptPreview(input, SynthesisManuscriptMergePreview{Version: SynthesisManuscriptMergeVersion, Algorithm: out.Algorithm, Contract: out.Contract, Review: review})
		if err != nil {
			return changeapp.RevisionMergeResult{}, err
		}
		decision := resolutions[consumed]
		if decision.Stage != stage || decision.PreviewFingerprint != preview.Fingerprint || len(decision.AcknowledgedOrdinals) != len(review.Conflicts) {
			return changeapp.RevisionMergeResult{}, manuscriptMergeInvalid("conflict decision refers to another preview")
		}
		for i, conflict := range review.Conflicts {
			if decision.AcknowledgedOrdinals[i] != conflict.Ordinal {
				return changeapp.RevisionMergeResult{}, manuscriptMergeInvalid("all exact conflicts must be acknowledged in order")
			}
		}
		consumed++
		out.Candidate = []byte(decision.FinalContent)
		return out, nil
	})
	preview, err := PreviewSynthesisManuscript(ctx, input, resolving, mapper)
	if err != nil {
		return SynthesisManuscriptResolvedPreview{}, err
	}
	if consumed != len(resolutions) {
		return SynthesisManuscriptResolvedPreview{}, manuscriptMergeInvalid("decision has no corresponding merge conflict")
	}
	result := SynthesisManuscriptResolvedPreview{Preview: preview, Resolutions: make([]SynthesisManuscriptResolution, len(resolutions))}
	for i, resolution := range resolutions {
		result.Resolutions[i] = resolution
		result.Resolutions[i].AcknowledgedOrdinals = append([]int{}, resolution.AcknowledgedOrdinals...)
	}
	encoded, err := json.Marshal(result)
	if err != nil {
		return SynthesisManuscriptResolvedPreview{}, manuscriptMergeInvalid("cannot encode resolved preview")
	}
	sum := sha256.Sum256(encoded)
	result.Hash = hex.EncodeToString(sum[:])
	return result, nil
}
