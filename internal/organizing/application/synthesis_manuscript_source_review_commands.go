package application

import (
	"context"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
)

type SynthesisManuscriptSourceReviewCommand struct {
	WorkspaceID     foundation.ID
	ReviewID        foundation.ID
	ExpectedVersion int64
	IdempotencyKey  string
}
type SynthesisManuscriptSourceReviewCommandStore interface {
	ExecuteSourceReviewCommand(context.Context, SynthesisManuscriptSourceReviewCommand, string) (SynthesisSourceReviewView, error)
	SourceReviewView(context.Context, foundation.ID, foundation.ID) (SynthesisSourceReviewView, error)
}
type SynthesisManuscriptSourceReviewCommandService struct {
	store  SynthesisManuscriptSourceReviewCommandStore
	caller SynthesisManuscriptCaller
}

func NewSynthesisManuscriptSourceReviewCommandService(store SynthesisManuscriptSourceReviewCommandStore, caller SynthesisManuscriptCaller) (*SynthesisManuscriptSourceReviewCommandService, error) {
	if store == nil {
		return nil, SourceReviewError("SYNTHESIS_SOURCE_REVIEW_UNAVAILABLE")
	}
	if err := caller.Authorize(); err != nil {
		return nil, err
	}
	return &SynthesisManuscriptSourceReviewCommandService{store, caller}, nil
}
func (s *SynthesisManuscriptSourceReviewCommandService) Recheck(ctx context.Context, c SynthesisManuscriptSourceReviewCommand) (SynthesisSourceReviewView, error) {
	return s.execute(ctx, c, "RECHECK")
}
func (s *SynthesisManuscriptSourceReviewCommandService) Recover(ctx context.Context, c SynthesisManuscriptSourceReviewCommand) (SynthesisSourceReviewView, error) {
	return s.execute(ctx, c, "RECOVER")
}
func (s *SynthesisManuscriptSourceReviewCommandService) execute(ctx context.Context, c SynthesisManuscriptSourceReviewCommand, op string) (SynthesisSourceReviewView, error) {
	if err := s.caller.Authorize(); err != nil {
		return SynthesisSourceReviewView{}, err
	}
	return s.store.ExecuteSourceReviewCommand(ctx, c, op)
}
func (s *SynthesisManuscriptSourceReviewCommandService) Read(ctx context.Context, w, id foundation.ID) (SynthesisSourceReviewView, error) {
	if err := s.caller.Authorize(); err != nil {
		return SynthesisSourceReviewView{}, err
	}
	return s.store.SourceReviewView(ctx, w, id)
}
