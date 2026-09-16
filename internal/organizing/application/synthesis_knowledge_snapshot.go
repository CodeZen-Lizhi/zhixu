package application

import (
	"context"
	"errors"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
)

func (service *SynthesisService) ReadSynthesisKnowledgeBinding(ctx context.Context, query SynthesisKnowledgeBindingQuery) (foundation.ID, error) {
	if err := service.ready(ctx, query.Reference.Source.WorkspaceID, query.NoteID, query.RevisionID); err != nil {
		return "", err
	}
	reader, ok := service.dependencies.Store.(SynthesisKnowledgeBindingReader)
	if !ok || nilSynthesisDependency(reader) {
		return "", foundation.NewError(foundation.ErrorDependencyUnavailable, ErrorCodeSynthesisUnavailable, true, errors.New("synthesis knowledge snapshot reader is unavailable"))
	}
	return reader.ReadSynthesisKnowledgeBinding(ctx, query)
}
