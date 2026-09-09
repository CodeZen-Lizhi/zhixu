package domain

import "testing"

func TestSynthesisModelRunsHaveExplicitSourceOnlySchemasAndResults(t *testing.T) {
	for _, test := range []struct {
		schema string
		result string
	}{
		{SynthesisDeltaSchemaID, ResultTypeSynthesisDelta},
		{SynthesisSemanticReviewSchemaID, ResultTypeSynthesisSemanticReview},
		{SynthesisNoteInterviewPlanSchemaID, ResultTypeSynthesisNoteInterviewPlan},
	} {
		t.Run(test.result, func(t *testing.T) {
			run := validModelRun()
			run.Retrieval = RetrievalRef{}
			run.Schema, run.ReducedSchema = SchemaRef{ID: test.schema, Version: "v1"}, SchemaRef{ID: test.schema, Version: "v1"}
			if err := ValidateModelRun(run); err != nil {
				t.Fatal(err)
			}
			run.Status, run.FinalResultType = ModelRunSucceeded, test.result
			run.CompletedAt = &run.UpdatedAt
			if err := ValidateModelRun(run); err != nil {
				t.Fatal(err)
			}
			run.Schema.ID = "agent.unknown-synthesis"
			if err := ValidateModelRun(run); err == nil {
				t.Fatal("unknown schema bypassed the retrieval requirement")
			}
		})
	}
}
