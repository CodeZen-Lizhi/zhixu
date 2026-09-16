package application

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	changeapp "github.com/CodeZen-Lizhi/zhixu/internal/changecontrol/application"
	changedomain "github.com/CodeZen-Lizhi/zhixu/internal/changecontrol/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	"github.com/CodeZen-Lizhi/zhixu/internal/organizing/domain"
	"sort"
)

// 候选重新合并使用最初捕获的文件作为完整人工全文的共同祖先，
// 不能使用已发布文章或机器渲染结果代替。
func PreviewSynthesisCandidateRemerge(ctx context.Context, original domain.SynthesisRevision, base, current string, engine changeapp.RevisionMergeEngine, mapper domain.SynthesisManuscriptMapper, resolution *SynthesisManuscriptResolution) (SynthesisManuscriptMergePreview, error) {
	if engine == nil || mapper == nil || original.Manuscript == nil || original.Validate() != nil || !validManuscriptMergeText(base) || !validManuscriptMergeText(current) {
		return SynthesisManuscriptMergePreview{}, manuscriptMergeInvalid("invalid candidate remerge baselines")
	}
	content, review, err := mergeManuscriptStage(ctx, engine, SynthesisMergeWorkspaceStage, base, current, original.Manuscript.FullContent)
	if err != nil {
		return SynthesisManuscriptMergePreview{}, err
	}
	preview := SynthesisManuscriptMergePreview{Version: SynthesisManuscriptMergeVersion, Algorithm: changedomain.ProposalRevisionMergeAlgorithm, Contract: changedomain.ProposalRevisionMergeAlgorithmVersion, Review: review}
	raw, err := json.Marshal(struct {
		Revision string
		Base     string
		Current  string
		Preview  SynthesisManuscriptMergePreview
	}{original.Hash, base, current, preview})
	if err != nil {
		return preview, err
	}
	sum := sha256.Sum256(raw)
	preview.Fingerprint = hex.EncodeToString(sum[:])
	if review != nil {
		if resolution == nil {
			return preview, nil
		}
		if resolution.Stage != review.Stage || resolution.PreviewFingerprint != preview.Fingerprint || len(resolution.AcknowledgedOrdinals) != len(review.Conflicts) || !validManuscriptMergeText(resolution.FinalContent) {
			return preview, manuscriptMergeInvalid("decision does not match candidate remerge preview")
		}
		for i, c := range review.Conflicts {
			if resolution.AcknowledgedOrdinals[i] != c.Ordinal {
				return preview, manuscriptMergeInvalid("every exact conflict must be acknowledged in order")
			}
		}
		content = resolution.FinalContent
		preview.Review = nil
	} else if resolution != nil {
		return preview, manuscriptMergeInvalid("clean candidate has no conflict to resolve")
	}
	machine := original.Manuscript.Machine
	excluded := map[foundation.ID]bool{}
	for _, id := range machine.IneligibleItemIDs {
		excluded[id] = true
	}
	for _, item := range original.Manuscript.Assessment.ReviewItems {
		excluded[item.ItemID] = true
	}
	machine.IneligibleItemIDs = make([]foundation.ID, 0, len(excluded))
	for id := range excluded {
		machine.IneligibleItemIDs = append(machine.IneligibleItemIDs, id)
	}
	sort.Slice(machine.IneligibleItemIDs, func(i, j int) bool { return machine.IneligibleItemIDs[i] < machine.IneligibleItemIDs[j] })
	manuscript, err := domain.NewSynthesisManuscript(machine, content, mapper)
	if err != nil {
		return preview, err
	}
	preview.Manuscript = &manuscript
	return preview, nil
}
