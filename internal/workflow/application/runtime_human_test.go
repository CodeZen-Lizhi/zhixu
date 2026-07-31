package application

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/CodeZen-Lizhi/zhixu/internal/capability"
	"github.com/CodeZen-Lizhi/zhixu/internal/workflow/domain"
)

func TestRuntimeHumanCoordinatorCarriesCallerCapabilities(t *testing.T) {
	state := &runtimeHumanStateStub{result: HumanTransitionResult{
		Task: domain.HumanTask{ID: id(3), RunID: id(2), Status: domain.HumanTaskSubmitted},
		Run:  domain.Run{ID: id(2)},
		Node: domain.NodeRun{RunID: id(2), Status: domain.NodeStatusSucceeded},
	}}
	coordinator, err := NewRuntimeHumanCoordinator(state)
	if err != nil {
		t.Fatal(err)
	}
	_, err = coordinator.SubmitHuman(context.Background(), HumanDecisionCommand{
		RunID: id(2), TaskID: id(3), TargetVersion: 1, Decision: json.RawMessage(`{"approved":true}`),
		CallerCapabilities: []capability.Capability{capability.WriteProposal},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(state.submitted.CallerCapabilities) != 1 || state.submitted.CallerCapabilities[0] != capability.WriteProposal {
		t.Fatalf("submitted capabilities=%v", state.submitted.CallerCapabilities)
	}
}

type runtimeHumanStateStub struct {
	submitted HumanDecisionTransition
	result    HumanTransitionResult
}

func (stub *runtimeHumanStateStub) WaitForHuman(context.Context, HumanWaitTransition) (HumanTransitionResult, error) {
	return HumanTransitionResult{}, nil
}

func (stub *runtimeHumanStateStub) SubmitHuman(_ context.Context, command HumanDecisionTransition) (HumanTransitionResult, error) {
	stub.submitted = command
	return stub.result, nil
}
