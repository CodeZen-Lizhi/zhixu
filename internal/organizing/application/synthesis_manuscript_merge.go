package application

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"sort"
	"strings"
	"unicode/utf8"

	changeapp "github.com/CodeZen-Lizhi/zhixu/internal/changecontrol/application"
	changedomain "github.com/CodeZen-Lizhi/zhixu/internal/changecontrol/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	"github.com/CodeZen-Lizhi/zhixu/internal/organizing/domain"
)

const (
	SynthesisManuscriptMergeVersion = "synthesis-manuscript-merge/v1"
	SynthesisMergeCandidateStage    = "CANDIDATE_MANUAL_CONTENT"
	SynthesisMergeWorkspaceStage    = "WORKSPACE_MANUAL_CONTENT"
)

// SynthesisManuscriptMergeInput 包含服务端读取的基线，不能作为 HTTP 请求。
// 调用方须证明发布、范围、路径授权与当前所属模块绑定，持久保存捕获结果，
// 并在应用和发布时重新核验。预览这些字节不授予修改笔记或 Workspace 文件的权限。
type SynthesisManuscriptMergeInput struct {
	Latest      domain.SynthesisRevision          `json:"latest"`
	Published   *domain.SynthesisRevision         `json:"published,omitempty"`
	FileExists  bool                              `json:"file_exists"`
	FileContent string                            `json:"file_content"`
	NextMachine domain.SynthesisManuscriptMachine `json:"next_machine"`
}

// 冲突候选只用于审阅预览。现有合并器在冲突处生成的无标记候选保留 Current，
// 它不是已裁决全文，不能交给候选存储。
type SynthesisManuscriptMergeReview struct {
	Stage     string                            `json:"stage"`
	Base      string                            `json:"base"`
	Current   string                            `json:"current"`
	Proposed  string                            `json:"proposed"`
	Candidate string                            `json:"candidate"`
	Conflicts []changeapp.RevisionMergeConflict `json:"conflicts"`
}

type SynthesisManuscriptMergePreview struct {
	Version     string                          `json:"version"`
	Algorithm   string                          `json:"algorithm"`
	Contract    string                          `json:"contract"`
	Manuscript  *domain.SynthesisManuscript     `json:"manuscript,omitempty"`
	Review      *SynthesisManuscriptMergeReview `json:"review,omitempty"`
	Fingerprint string                          `json:"fingerprint"`
}

// PreviewSynthesisManuscript 复用 Change Control 的固定 Git 合并端口。
// 先保留 L 中已有的人工内容，再将结果分支与 P/F 合并。
// 若以 L 作为文件基线，会静默删除未发布内容。此操作只做预览，不改文件、修订或审批状态。
func PreviewSynthesisManuscript(ctx context.Context, input SynthesisManuscriptMergeInput, engine changeapp.RevisionMergeEngine, mapper domain.SynthesisManuscriptMapper) (SynthesisManuscriptMergePreview, error) {
	if engine == nil || mapper == nil {
		return SynthesisManuscriptMergePreview{}, manuscriptMergeInvalid("merge engine or mapper missing")
	}
	if err := validateManuscriptMergeInput(input); err != nil {
		return SynthesisManuscriptMergePreview{}, err
	}
	for _, revision := range []*domain.SynthesisRevision{&input.Latest, input.Published} {
		if revision != nil && revision.Manuscript != nil {
			if err := revision.Manuscript.Validate(revision.Manuscript.Machine, mapper); err != nil {
				return SynthesisManuscriptMergePreview{}, err
			}
		}
	}
	next := input.NextMachine
	// 后续更新须延续已失去信任的状态，即使旧机器块恰好再次出现也不例外。
	// 只有经过单独验证的新条目才能获得信任。
	excluded := make(map[foundation.ID]bool)
	for _, id := range next.IneligibleItemIDs {
		excluded[id] = true
	}
	if previous := input.Latest.Manuscript; previous != nil {
		for _, id := range previous.Machine.IneligibleItemIDs {
			excluded[id] = true
		}
		for _, item := range previous.Assessment.ReviewItems {
			excluded[item.ItemID] = true
		}
	}
	if len(excluded) > domain.MaxSynthesisManuscriptExclusions {
		return SynthesisManuscriptMergePreview{}, foundation.NewError(foundation.ErrorManualRecoveryRequired, domain.ErrorCodeSynthesisManuscriptHistoryReviewRequired, false, errors.New("historical exclusion limit exceeded; review required"))
	}
	// 不筛除当前缺失的 ID，也不修改调用方切片。稳定排序使同一历史并集
	// 在多次重放中绑定到同一个不可变封装。
	next.IneligibleItemIDs = make([]foundation.ID, 0, len(excluded))
	for id := range excluded {
		next.IneligibleItemIDs = append(next.IneligibleItemIDs, id)
	}
	sort.Slice(next.IneligibleItemIDs, func(i, j int) bool { return next.IneligibleItemIDs[i] < next.IneligibleItemIDs[j] })
	proposed, err := domain.RenderSynthesisMarkdown(next.WorkspaceID, next.NoteID, next.MachineTitle, next.MachineItems)
	if err != nil {
		return SynthesisManuscriptMergePreview{}, err
	}
	preview := SynthesisManuscriptMergePreview{Version: SynthesisManuscriptMergeVersion, Algorithm: changedomain.ProposalRevisionMergeAlgorithm, Contract: changedomain.ProposalRevisionMergeAlgorithmVersion}
	if manuscript := input.Latest.Manuscript; manuscript != nil && manuscript.ManualChanges {
		base, err := domain.RenderSynthesisMarkdown(manuscript.Machine.WorkspaceID, manuscript.Machine.NoteID, manuscript.Machine.MachineTitle, manuscript.Machine.MachineItems)
		if err != nil {
			return SynthesisManuscriptMergePreview{}, err
		}
		proposed, preview.Review, err = mergeManuscriptStage(ctx, engine, SynthesisMergeCandidateStage, base, manuscript.FullContent, proposed)
		if err != nil {
			return SynthesisManuscriptMergePreview{}, err
		}
		if preview.Review != nil {
			return fingerprintManuscriptPreview(input, preview)
		}
	}
	if input.Published != nil {
		base, err := input.Published.Content()
		if err != nil {
			return SynthesisManuscriptMergePreview{}, err
		}
		proposed, preview.Review, err = mergeManuscriptStage(ctx, engine, SynthesisMergeWorkspaceStage, base, input.FileContent, proposed)
		if err != nil {
			return SynthesisManuscriptMergePreview{}, err
		}
		if preview.Review != nil {
			return fingerprintManuscriptPreview(input, preview)
		}
	}
	manuscript, err := domain.NewSynthesisManuscript(next, proposed, mapper)
	if err != nil {
		return SynthesisManuscriptMergePreview{}, err
	}
	preview.Manuscript = &manuscript
	return fingerprintManuscriptPreview(input, preview)
}

func validateManuscriptMergeInput(input SynthesisManuscriptMergeInput) error {
	if err := input.Latest.Validate(); err != nil {
		return err
	}
	if err := input.NextMachine.Validate(); err != nil {
		return err
	}
	if input.NextMachine.WorkspaceID != input.Latest.WorkspaceID || input.NextMachine.NoteID != input.Latest.NoteID || !validManuscriptMergeText(input.FileContent) {
		return manuscriptMergeInvalid("next machine or captured file does not match note")
	}
	if input.Published == nil {
		if input.FileExists || input.FileContent != "" {
			return manuscriptMergeInvalid("unpublished note requires an absent target")
		}
		return nil
	}
	if err := input.Published.Validate(); err != nil {
		return err
	}
	p, l := input.Published, input.Latest
	if !input.FileExists || p.WorkspaceID != l.WorkspaceID || p.NoteID != l.NoteID || p.DocumentID != l.DocumentID || p.RevisionNo > l.RevisionNo || (p.RevisionNo == l.RevisionNo && (p.ID != l.ID || p.Hash != l.Hash)) {
		return manuscriptMergeInvalid("published baseline or current file is missing or mismatched")
	}
	return nil
}

func mergeManuscriptStage(ctx context.Context, engine changeapp.RevisionMergeEngine, stage, base, current, proposed string) (string, *SynthesisManuscriptMergeReview, error) {
	result, err := engine.Merge(ctx, changeapp.RevisionMergeDocuments{Base: []byte(base), Current: []byte(current), Proposed: []byte(proposed)})
	if err != nil {
		return "", nil, err
	}
	if result.Algorithm != changedomain.ProposalRevisionMergeAlgorithm || result.Contract != changedomain.ProposalRevisionMergeAlgorithmVersion || !validManuscriptMergeText(string(result.Candidate)) {
		return "", nil, manuscriptMergeInvalid("merge result has invalid contract or content")
	}
	if len(result.Conflicts) == 0 {
		return string(result.Candidate), nil, nil
	}
	conflicts := make([]changeapp.RevisionMergeConflict, len(result.Conflicts))
	for i, conflict := range result.Conflicts {
		if conflict.Ordinal != i+1 || !validManuscriptMergeText(string(conflict.Base)) || !validManuscriptMergeText(string(conflict.Current)) || !validManuscriptMergeText(string(conflict.Proposed)) {
			return "", nil, manuscriptMergeInvalid("invalid structured merge conflict")
		}
		conflicts[i] = changeapp.RevisionMergeConflict{Ordinal: conflict.Ordinal, Base: append([]byte{}, conflict.Base...), Current: append([]byte{}, conflict.Current...), Proposed: append([]byte{}, conflict.Proposed...)}
	}
	return "", &SynthesisManuscriptMergeReview{Stage: stage, Base: base, Current: current, Proposed: proposed, Candidate: string(result.Candidate), Conflicts: conflicts}, nil
}

func fingerprintManuscriptPreview(input SynthesisManuscriptMergeInput, preview SynthesisManuscriptMergePreview) (SynthesisManuscriptMergePreview, error) {
	encoded, err := json.Marshal(struct {
		Input   SynthesisManuscriptMergeInput   `json:"input"`
		Preview SynthesisManuscriptMergePreview `json:"preview"`
	}{input, preview})
	if err != nil {
		return SynthesisManuscriptMergePreview{}, manuscriptMergeInvalid("cannot encode merge preview")
	}
	sum := sha256.Sum256(encoded)
	preview.Fingerprint = hex.EncodeToString(sum[:])
	return preview, nil
}

func validManuscriptMergeText(value string) bool {
	return len(value) <= domain.MaxSynthesisManuscriptBytes && utf8.ValidString(value) && !strings.ContainsRune(value, '\x00')
}

func manuscriptMergeInvalid(message string) error {
	return foundation.NewError(foundation.ErrorInvalidInput, "SYNTHESIS_MANUSCRIPT_MERGE_INVALID", false, errors.New(message))
}
