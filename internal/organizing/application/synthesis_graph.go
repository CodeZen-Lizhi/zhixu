package application

import (
	"context"
	"errors"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	"github.com/CodeZen-Lizhi/zhixu/internal/organizing/domain"
)

type SynthesisSourceGraphQuery struct {
	WorkspaceID foundation.ID
	NoteID      foundation.ID
	RevisionID  foundation.ID
	AfterNoteID foundation.ID
	Limit       int
}

// 共享来源边是读取投影，不能证明一份笔记依赖另一份笔记的正文。
// 每个相邻节点都绑定到其已发布修订。
type SynthesisSharedSourceNote struct {
	NoteID     foundation.ID               `json:"note_id"`
	RevisionID foundation.ID               `json:"revision_id"`
	RevisionNo int64                       `json:"revision_no"`
	Title      string                      `json:"title"`
	Sources    []domain.SynthesisSourceRef `json:"sources"`
}

type SynthesisSourceGraph struct {
	WorkspaceID     foundation.ID               `json:"workspace_id"`
	NoteID          foundation.ID               `json:"note_id"`
	RevisionID      foundation.ID               `json:"revision_id"`
	Sources         []domain.SynthesisSourceRef `json:"sources"`
	SharedNotes     []SynthesisSharedSourceNote `json:"shared_notes"`
	NextAfterNoteID *foundation.ID              `json:"next_after_note_id"`
}

type SynthesisSourceGraphReader interface {
	ReadSynthesisSourceGraph(context.Context, SynthesisSourceGraphQuery) (SynthesisSourceGraph, error)
}

func (service *SynthesisService) ReadSynthesisSourceGraph(ctx context.Context, query SynthesisSourceGraphQuery) (SynthesisSourceGraph, error) {
	if err := service.ready(ctx, query.WorkspaceID, query.NoteID, query.RevisionID); err != nil {
		return SynthesisSourceGraph{}, err
	}
	reader, ok := service.dependencies.Store.(SynthesisSourceGraphReader)
	if !ok || nilSynthesisDependency(reader) {
		return SynthesisSourceGraph{}, foundation.NewError(foundation.ErrorDependencyUnavailable, ErrorCodeSynthesisUnavailable, true, errors.New("source graph reader is unavailable"))
	}
	return reader.ReadSynthesisSourceGraph(ctx, query)
}
