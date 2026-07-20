package application

import "testing"

func TestRAGCurrentStageAllowsOnlyFrozenProjectionValues(t *testing.T) {
	t.Parallel()
	for _, stage := range []RAGCurrentStage{
		RAGCurrentStagePlanStarted,
		RAGCurrentStagePlanCompleted,
		RAGCurrentStageRetrievalStarted,
		RAGCurrentStageRetrievalCompleted,
		RAGCurrentStageValidationStarted,
		RAGCurrentStageValidationCompleted,
	} {
		if !stage.Valid() {
			t.Fatalf("frozen stage %q is invalid", stage)
		}
	}
	for _, stage := range []RAGCurrentStage{"", "plan", "generation.started", "validation.completed.extra"} {
		if stage.Valid() {
			t.Fatalf("unexpected stage %q is valid", stage)
		}
	}
}
