package application

import (
	"bytes"
	"context"
	"errors"
	"testing"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/agent/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
)

func TestWorkspaceAnalysisModelRefusalSignalRequiresExactClassifiedCode(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{
			name: "exact",
			err: foundation.NewError(
				foundation.ErrorNonRetryableFailure,
				string(domain.WorkspaceAnalysisRunModelRefused),
				false,
				errors.New("provider refused the controlled request"),
			),
			want: true,
		},
		{
			name: "near code",
			err: foundation.NewError(
				foundation.ErrorNonRetryableFailure,
				"WORKSPACE_ANALYSIS_MODEL_REFUSED_PROVIDER",
				false,
				errors.New("provider refused"),
			),
		},
		{
			name: "retryable kind",
			err: foundation.NewError(
				foundation.ErrorRetryableFailure,
				string(domain.WorkspaceAnalysisRunModelRefused),
				true,
				errors.New("provider unavailable"),
			),
		},
		{
			name: "retryable flag",
			err: foundation.NewError(
				foundation.ErrorNonRetryableFailure,
				string(domain.WorkspaceAnalysisRunModelRefused),
				true,
				errors.New("inconsistent adapter classification"),
			),
		},
		{name: "unclassified", err: errors.New("provider refused")},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := workspaceAnalysisModelRefusalSignal(test.err); got != test.want {
				t.Fatalf("workspaceAnalysisModelRefusalSignal()=%t want=%t", got, test.want)
			}
		})
	}
}

func TestWorkspaceAnalysisModelRunnersPersistExactRefusal(t *testing.T) {
	refusal := foundation.NewError(
		foundation.ErrorNonRetryableFailure,
		string(domain.WorkspaceAnalysisRunModelRefused),
		false,
		errors.New("provider refused the controlled request"),
	)
	usage := domain.TokenUsage{InputTokens: 9, OutputTokens: 1, TotalTokens: 10}
	content := []byte(`{"provider":"refused","reason":"bounded policy"}`)

	t.Run("plan", func(t *testing.T) {
		repository := &workspaceAnalysisPlanRepository{}
		model := &workspaceAnalysisPlanModel{response: ChatResponse{
			Model: workspaceAnalysisPlanModelRef(), Content: content, Usage: usage,
		}, err: refusal}
		runner := newWorkspaceAnalysisPlanRunner(t, model, repository)
		_, err := runner.Run(context.Background(), workspaceAnalysisPlanRequest())
		assertWorkspaceAnalysisRunnerRefusal(t, err, repository.finalizeCallCommands, content, usage)
	})

	t.Run("synthesis", func(t *testing.T) {
		repository := &workspaceAnalysisSynthesisRepository{}
		runtime := &workspaceAnalysisSynthesisRuntime{response: ChatResponse{
			Model: workspaceAnalysisPlanModelRef(), Content: content, Usage: usage,
		}, err: refusal}
		runner := newWorkspaceAnalysisSynthesisRunner(t, runtime, repository, &workspaceAnalysisSynthesisDraftCoordinator{})
		_, err := runner.Run(context.Background(), workspaceAnalysisSynthesisRequest())
		assertWorkspaceAnalysisRunnerRefusal(t, err, repository.finalizeCallCommands, content, usage)
	})

	t.Run("review", func(t *testing.T) {
		repository := &workspaceAnalysisReviewRepository{}
		model := &workspaceAnalysisPlanModel{response: ChatResponse{
			Model: workspaceAnalysisReviewModelRef(), Content: content, Usage: usage,
		}, err: refusal}
		runner := newWorkspaceAnalysisReviewRunner(t, model, repository)
		_, err := runner.Run(context.Background(), workspaceAnalysisReviewRequest(t))
		assertWorkspaceAnalysisRunnerRefusal(t, err, repository.finalizeCallCommands, content, usage)
	})
}

func TestWorkspaceAnalysisModelRunnersReplayPersistedRefusal(t *testing.T) {
	content := []byte(`{"provider":"refused","reason":"bounded policy"}`)
	usage := domain.TokenUsage{InputTokens: 7, OutputTokens: 1, TotalTokens: 8}
	replay := func(command AuthorizeWorkspaceAnalysisModelCallCommand) (WorkspaceAnalysisModelAuthorizationResult, error) {
		completedAt := command.Call.StartedAt.Add(time.Second)
		call, run := workspaceAnalysisModelRefusalTerminal(
			WorkspaceAnalysisModelAuthorizationResult{Call: command.Call, Run: command.Run},
			ChatResponse{Model: command.Call.Model, Content: content, Usage: usage},
			completedAt,
		)
		return WorkspaceAnalysisModelAuthorizationResult{
			Run: run, Call: call, OperationID: command.OperationID, ReservationID: command.ReservationID,
			Disposition: WorkspaceAnalysisModelAuthorizationReplayFailure,
		}, nil
	}

	t.Run("plan", func(t *testing.T) {
		repository := &workspaceAnalysisPlanRepository{authorize: replay}
		model := &workspaceAnalysisPlanModel{}
		runner := newWorkspaceAnalysisPlanRunner(t, model, repository)
		_, err := runner.Run(context.Background(), workspaceAnalysisPlanRequest())
		assertWorkspaceAnalysisReplayedRefusal(t, err)
		if model.CallCount() != 0 || repository.finalizeCallCalls != 0 || repository.finalizeResultCalls != 0 {
			t.Fatalf("replay crossed provider/finalizer provider=%d call=%d result=%d",
				model.CallCount(), repository.finalizeCallCalls, repository.finalizeResultCalls)
		}
	})

	t.Run("synthesis", func(t *testing.T) {
		repository := &workspaceAnalysisSynthesisRepository{authorize: replay}
		runtime := &workspaceAnalysisSynthesisRuntime{}
		runner := newWorkspaceAnalysisSynthesisRunner(t, runtime, repository, &workspaceAnalysisSynthesisDraftCoordinator{})
		_, err := runner.Run(context.Background(), workspaceAnalysisSynthesisRequest())
		assertWorkspaceAnalysisReplayedRefusal(t, err)
		if runtime.CallCount() != 0 || repository.finalizeCallCalls != 0 || repository.finalizeCandidateCalls != 0 {
			t.Fatalf("replay crossed provider/finalizer provider=%d call=%d candidate=%d",
				runtime.CallCount(), repository.finalizeCallCalls, repository.finalizeCandidateCalls)
		}
	})

	t.Run("review", func(t *testing.T) {
		repository := &workspaceAnalysisReviewRepository{authorize: replay}
		model := &workspaceAnalysisPlanModel{}
		runner := newWorkspaceAnalysisReviewRunner(t, model, repository)
		_, err := runner.Run(context.Background(), workspaceAnalysisReviewRequest(t))
		assertWorkspaceAnalysisReplayedRefusal(t, err)
		if model.CallCount() != 0 || repository.finalizeCallCalls != 0 || repository.finalizeResultCalls != 0 {
			t.Fatalf("replay crossed provider/finalizer provider=%d call=%d result=%d",
				model.CallCount(), repository.finalizeCallCalls, repository.finalizeResultCalls)
		}
	})
}

func TestWorkspaceAnalysisModelRefusalRequiresValidatedResponse(t *testing.T) {
	refusal := foundation.NewError(
		foundation.ErrorNonRetryableFailure,
		string(domain.WorkspaceAnalysisRunModelRefused),
		false,
		errors.New("provider refused the controlled request"),
	)
	runners := []struct {
		name     string
		response func() ChatResponse
		run      func(*testing.T, ChatResponse, error) (error, []FinalizeWorkspaceAnalysisModelCallCommand)
	}{
		{
			name: "plan",
			response: func() ChatResponse {
				return ChatResponse{
					Model: workspaceAnalysisPlanModelRef(), Content: []byte(`{"provider":"refused"}`),
					Usage: domain.TokenUsage{InputTokens: 4, OutputTokens: 1, TotalTokens: 5},
				}
			},
			run: func(t *testing.T, response ChatResponse, providerErr error) (error, []FinalizeWorkspaceAnalysisModelCallCommand) {
				repository := &workspaceAnalysisPlanRepository{}
				runner := newWorkspaceAnalysisPlanRunner(t, &workspaceAnalysisPlanModel{response: response, err: providerErr}, repository)
				_, err := runner.Run(context.Background(), workspaceAnalysisPlanRequest())
				return err, repository.finalizeCallCommands
			},
		},
		{
			name: "synthesis",
			response: func() ChatResponse {
				return ChatResponse{
					Model: workspaceAnalysisPlanModelRef(), Content: []byte(`{"provider":"refused"}`),
					Usage: domain.TokenUsage{InputTokens: 4, OutputTokens: 1, TotalTokens: 5},
				}
			},
			run: func(t *testing.T, response ChatResponse, providerErr error) (error, []FinalizeWorkspaceAnalysisModelCallCommand) {
				repository := &workspaceAnalysisSynthesisRepository{}
				runner := newWorkspaceAnalysisSynthesisRunner(
					t, &workspaceAnalysisSynthesisRuntime{response: response, err: providerErr},
					repository, &workspaceAnalysisSynthesisDraftCoordinator{},
				)
				_, err := runner.Run(context.Background(), workspaceAnalysisSynthesisRequest())
				return err, repository.finalizeCallCommands
			},
		},
		{
			name: "review",
			response: func() ChatResponse {
				return ChatResponse{
					Model: workspaceAnalysisReviewModelRef(), Content: []byte(`{"provider":"refused"}`),
					Usage: domain.TokenUsage{InputTokens: 4, OutputTokens: 1, TotalTokens: 5},
				}
			},
			run: func(t *testing.T, response ChatResponse, providerErr error) (error, []FinalizeWorkspaceAnalysisModelCallCommand) {
				repository := &workspaceAnalysisReviewRepository{}
				runner := newWorkspaceAnalysisReviewRunner(t, &workspaceAnalysisPlanModel{response: response, err: providerErr}, repository)
				_, err := runner.Run(context.Background(), workspaceAnalysisReviewRequest(t))
				return err, repository.finalizeCallCommands
			},
		},
	}
	shapes := []struct {
		name   string
		mutate func(*ChatResponse)
	}{
		{name: "invalid model", mutate: func(response *ChatResponse) { response.Model = domain.ModelRef{} }},
		{name: "mismatched model", mutate: func(response *ChatResponse) { response.Model.ModelID = "other-model" }},
		{name: "empty content", mutate: func(response *ChatResponse) { response.Content = nil }},
		{name: "empty usage", mutate: func(response *ChatResponse) { response.Usage = domain.TokenUsage{} }},
		{name: "oversized content", mutate: func(response *ChatResponse) {
			response.Content = bytes.Repeat([]byte("x"), int(MaxModelCallResponseBytes)+1)
		}},
	}
	for _, runner := range runners {
		for _, shape := range shapes {
			t.Run(runner.name+"/"+shape.name, func(t *testing.T) {
				response := runner.response()
				shape.mutate(&response)
				err, commands := runner.run(t, response, refusal)
				if len(commands) != 1 {
					t.Fatalf("finalize calls=%d error=%v", len(commands), err)
				}
				command := commands[0]
				if command.Call.Status != domain.ModelCallFailed || command.Run.Status != domain.ModelRunFailed ||
					command.Run.FinalResultType != "" ||
					command.Run.FinalErrorCode == string(domain.WorkspaceAnalysisRunModelRefused) || command.Validate() != nil {
					t.Fatalf("invalid refusal response became %#v", command)
				}
				var classified *foundation.Error
				if !errors.As(err, &classified) || classified.Code == string(domain.WorkspaceAnalysisRunModelRefused) {
					t.Fatalf("invalid refusal response error=%#v", err)
				}
				evidence, found := WorkspaceAnalysisModelTerminalEvidenceFromError(err)
				if !found || evidence.CallStatus != domain.ModelCallFailed || evidence.RunStatus != domain.ModelRunFailed {
					t.Fatalf("terminal evidence=%#v found=%t", evidence, found)
				}
			})
		}
	}
}

func TestWorkspaceAnalysisPlanRunnerDoesNotGeneralizeModelRefusalCode(t *testing.T) {
	tests := []error{
		foundation.NewError(foundation.ErrorNonRetryableFailure, "WORKSPACE_ANALYSIS_MODEL_REFUSED_PROVIDER", false, errors.New("near code")),
		foundation.NewError(foundation.ErrorConsistencyViolation, "MODEL_CHAT_RESPONSE_INVALID", false, errors.New("provider refusal violates the strict chat response contract")),
		foundation.NewError(foundation.ErrorRetryableFailure, string(domain.WorkspaceAnalysisRunModelRefused), true, errors.New("retryable")),
		foundation.NewError(foundation.ErrorNonRetryableFailure, string(domain.WorkspaceAnalysisRunModelRefused), true, errors.New("bad retryable flag")),
	}
	for index, providerErr := range tests {
		t.Run(string(rune('a'+index)), func(t *testing.T) {
			repository := &workspaceAnalysisPlanRepository{}
			model := &workspaceAnalysisPlanModel{response: ChatResponse{
				Model: workspaceAnalysisPlanModelRef(), Content: []byte(`{"provider":"error"}`),
				Usage: domain.TokenUsage{InputTokens: 4, OutputTokens: 1, TotalTokens: 5},
			}, err: providerErr}
			runner := newWorkspaceAnalysisPlanRunner(t, model, repository)
			_, _ = runner.Run(context.Background(), workspaceAnalysisPlanRequest())
			if len(repository.finalizeCallCommands) != 1 {
				t.Fatalf("finalize calls=%d", len(repository.finalizeCallCommands))
			}
			command := repository.finalizeCallCommands[0]
			if command.Call.Status != domain.ModelCallFailed || command.Run.Status != domain.ModelRunFailed ||
				command.Run.FinalResultType != "" {
				t.Fatalf("non-exact refusal became %#v", command)
			}
		})
	}
}

func assertWorkspaceAnalysisRunnerRefusal(
	t *testing.T,
	err error,
	commands []FinalizeWorkspaceAnalysisModelCallCommand,
	wantContent []byte,
	wantUsage domain.TokenUsage,
) {
	t.Helper()
	var classified *foundation.Error
	if !errors.As(err, &classified) || classified.Kind != foundation.ErrorNonRetryableFailure ||
		classified.Retryable || classified.Code != string(domain.WorkspaceAnalysisRunModelRefused) {
		t.Fatalf("runner error=%#v", err)
	}
	if len(commands) != 1 {
		t.Fatalf("finalize calls=%d", len(commands))
	}
	command := commands[0]
	if command.Call.Status != domain.ModelCallSucceeded || command.Call.ErrorCode != "" ||
		command.Call.ResponseHash != workspaceAnalysisSHA256(wantContent) ||
		command.Call.ResponseBytes != int64(len(wantContent)) || command.Call.Usage != wantUsage ||
		command.Run.Status != domain.ModelRunRefused || command.Run.FinalResultType != domain.ResultTypeRefusal ||
		command.Run.FinalErrorCode != string(domain.WorkspaceAnalysisRunModelRefused) || command.Validate() != nil {
		t.Fatalf("refusal terminal=%#v", command)
	}
	evidence, found := WorkspaceAnalysisModelTerminalEvidenceFromError(err)
	if !found || evidence.OperationID != command.OperationID || evidence.CallStatus != domain.ModelCallSucceeded ||
		evidence.RunStatus != domain.ModelRunRefused {
		t.Fatalf("terminal evidence=%#v found=%t", evidence, found)
	}
}

func assertWorkspaceAnalysisReplayedRefusal(t *testing.T, err error) {
	t.Helper()
	var classified *foundation.Error
	if !errors.As(err, &classified) || classified.Kind != foundation.ErrorNonRetryableFailure ||
		classified.Retryable || classified.Code != string(domain.WorkspaceAnalysisRunModelRefused) {
		t.Fatalf("Run error=%#v", err)
	}
	evidence, found := WorkspaceAnalysisModelTerminalEvidenceFromError(err)
	if !found || evidence.CallStatus != domain.ModelCallSucceeded || evidence.RunStatus != domain.ModelRunRefused {
		t.Fatalf("terminal evidence=%#v found=%t", evidence, found)
	}
}
