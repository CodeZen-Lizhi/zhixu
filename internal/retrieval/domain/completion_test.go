package domain

import (
	"errors"
	"testing"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
)

func TestValidateCompleteReindexCommandRequiresStableIdentityAndFullFence(t *testing.T) {
	valid := CompleteReindexCommand{
		WorkspaceID: "c1000000-0000-4000-8000-000000000001",
		Fence: DeliveryFence{
			DeliveryID: "c1000000-0000-4000-8000-000000000002", DispatchNo: 2,
			AttemptID: "c1000000-0000-4000-8000-000000000003", AttemptNo: 3,
			Owner: "reindex-worker", DeliveryVersion: 7,
		},
		ActivationID:             "c1000000-0000-4000-8000-000000000004",
		ActivationIdempotencyKey: "reindex-complete:event-1",
		ActivationReasonCode:     "REINDEX_REGRESSION_PASSED",
	}
	if err := ValidateCompleteReindexCommand(valid); err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name   string
		mutate func(*CompleteReindexCommand)
	}{
		{"workspace", func(value *CompleteReindexCommand) { value.WorkspaceID = "invalid" }},
		{"fence", func(value *CompleteReindexCommand) { value.Fence.Owner = "" }},
		{"activation", func(value *CompleteReindexCommand) { value.ActivationID = "invalid" }},
		{"idempotency", func(value *CompleteReindexCommand) { value.ActivationIdempotencyKey = " replay " }},
		{"reason", func(value *CompleteReindexCommand) { value.ActivationReasonCode = "" }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			candidate := valid
			test.mutate(&candidate)
			err := ValidateCompleteReindexCommand(candidate)
			var classified *foundation.Error
			if err == nil || !errors.As(err, &classified) || classified.Kind != foundation.ErrorInvalidInput || classified.Code != ErrorCodeCompletionInvalid {
				t.Fatalf("error=%#v", err)
			}
		})
	}
}
