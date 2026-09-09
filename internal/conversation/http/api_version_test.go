package conversationhttp

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	agentdomain "github.com/CodeZen-Lizhi/zhixu/internal/agent/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/conversation/application"
	conversationdomain "github.com/CodeZen-Lizhi/zhixu/internal/conversation/domain"
	conversationworkflow "github.com/CodeZen-Lizhi/zhixu/internal/conversation/workflow"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	workflowdomain "github.com/CodeZen-Lizhi/zhixu/internal/workflow/domain"
)

func TestConversationAPIVersionReadBoundaryIncludesPendingAndAllTerminalShapes(t *testing.T) {
	t.Parallel()
	for _, publication := range []conversationdomain.AnswerPublicationStatus{
		conversationdomain.AnswerPublicationPending, conversationdomain.AnswerPublicationCompleted,
		conversationdomain.AnswerPublicationRefused, conversationdomain.WorkspaceAnalysisPublicationFailed,
		conversationdomain.WorkspaceAnalysisPublicationCancelled,
	} {
		for _, definitionVersion := range []int64{1, 2} {
			for _, apiVersion := range []application.APIVersion{application.APIVersionV1, application.APIVersionV2} {
				t.Run(fmt.Sprintf("%s/definition%d/api%d", publication, definitionVersion, apiVersion), func(t *testing.T) {
					view := analysisHTTPAnswer(t, definitionVersion, publication)
					router := testRouter(&fakeService{answer: view})
					path := fmt.Sprintf("/api/v%d/answers/%s?workspace_id=%s", apiVersion, testAnswerID, testWorkspaceID)
					request := httptest.NewRequest(http.MethodGet, path, nil)
					if apiVersion == application.APIVersionV1 && definitionVersion == 2 {
						// A matching cached v2 ETag must never bypass the version fence.
						request.Header.Set("If-None-Match", answerETag(view.Answer.Version, view.Workflow.Version, view.CurrentStage))
					}
					recorder := httptest.NewRecorder()
					router.ServeHTTP(recorder, request)
					if apiVersion == application.APIVersionV1 && definitionVersion == 2 {
						assertVersionProblem(t, recorder)
						if recorder.Header().Get("ETag") != "" {
							t.Fatal("version-incompatible answer received an ETag")
						}
						return
					}
					if recorder.Code != http.StatusOK {
						t.Fatalf("read status=%d body=%s", recorder.Code, recorder.Body.String())
					}
					var wire answerResponse
					decodeBody(t, recorder, &wire)
					if wire.PublicationStatus != string(publication) || !bytes.Equal(wire.Result, view.Answer.Result) ||
						wire.AssistantText != view.AssistantText || len(wire.Citations) != len(view.Citations) {
						t.Fatalf("versioned read rewrote answer facts: %#v", wire)
					}
					if wire.Workflow.StatusURL != "/api/v1/workflows/"+string(view.Workflow.RunID) {
						t.Fatalf("shared workflow URL changed: %s", wire.Workflow.StatusURL)
					}
					if definitionVersion == 1 {
						assertLegacyAnswerWire(t, recorder.Body.Bytes(), publication)
					} else if publication == conversationdomain.AnswerPublicationCompleted &&
						(!bytes.Contains(wire.Result, []byte(`"git_status":null`)) || !bytes.Contains(wire.Result, []byte(`"model_calls":5`))) {
						t.Fatalf("v2 Git absence or actual budget was rewritten: %s", wire.Result)
					}
				})
			}
		}
	}
}

func TestConversationAPIVersionSubmitKeepsIdentityAndVersionedStatusURL(t *testing.T) {
	t.Parallel()
	for _, definitionVersion := range []int64{1, 2} {
		for _, apiVersion := range []application.APIVersion{application.APIVersionV1, application.APIVersionV2} {
			t.Run(fmt.Sprintf("definition%d/api%d", definitionVersion, apiVersion), func(t *testing.T) {
				view := analysisHTTPAnswer(t, definitionVersion, conversationdomain.AnswerPublicationCompleted)
				question := analysisHTTPQuestion(t, view)
				service := &fakeService{submission: &application.SubmitQuestionResult{
					Question: question, Answer: view.Answer, Workflow: view.Workflow, Replayed: true,
				}}
				router := testRouter(service)
				body := `{"workspace_id":"` + string(testWorkspaceID) + `","mode":"workspace_analysis","question":"分析工作区"}`
				request := httptest.NewRequest(http.MethodPost, fmt.Sprintf("/api/v%d/conversations/%s/questions", apiVersion, testConversationID), strings.NewReader(body))
				request.Header.Set("Idempotency-Key", "same-question-key")
				request.Header.Set("Content-Type", "application/json")
				recorder := httptest.NewRecorder()
				router.ServeHTTP(recorder, request)
				if service.question.APIVersion != apiVersion || service.question.IdempotencyKey != "same-question-key" {
					t.Fatalf("HTTP changed request identity or omitted the version: %#v", service.question)
				}
				if apiVersion == application.APIVersionV1 && definitionVersion == 2 {
					assertVersionProblem(t, recorder)
					return
				}
				if recorder.Code != http.StatusOK {
					t.Fatalf("replay status=%d body=%s", recorder.Code, recorder.Body.String())
				}
				var wire acceptedQuestionResponse
				decodeBody(t, recorder, &wire)
				wantURL := fmt.Sprintf("/api/v%d/answers/%s?workspace_id=%s", apiVersion, view.Answer.ID, testWorkspaceID)
				if wire.Question.ID != string(question.ID) || wire.Answer.ID != string(view.Answer.ID) || wire.StatusURL != wantURL ||
					wire.Answer.AssistantText != view.AssistantText || len(wire.Answer.Citations) != 1 ||
					!strings.HasPrefix(wire.Answer.Citations[0].Href, "/api/v1/workspaces/") {
					t.Fatalf("replay response lost identity, projection or links: %#v", wire)
				}
			})
		}
	}
}

func TestConversationAPIVersionListsAndTimelinesNeverDropUnsupportedItems(t *testing.T) {
	t.Parallel()
	view := analysisHTTPAnswer(t, 2, conversationdomain.AnswerPublicationRefused)
	timeline := validWorkspaceAnalysisTimelineResponse(testWorkspaceID, testAnswerID, httpAnalysisID(110))
	timeline.SchemaVersion = conversationdomain.WorkspaceAnalysisTimelineSchemaVersionV2
	for _, apiVersion := range []application.APIVersion{application.APIVersionV1, application.APIVersionV2} {
		service := &fakeService{turns: application.TurnPage{Items: []application.TurnView{{Question: analysisHTTPQuestion(t, view), Answer: &view}}}, timeline: timeline}
		router := testRouter(service)
		for _, suffix := range []string{"", "&latest=true"} {
			path := fmt.Sprintf("/api/v%d/conversations/%s/turns?workspace_id=%s%s", apiVersion, testConversationID, testWorkspaceID, suffix)
			recorder := httptest.NewRecorder()
			router.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, path, nil))
			if service.turnsQuery.APIVersion != apiVersion || service.turnsQuery.Latest != (suffix != "") {
				t.Fatalf("version/latest constraint did not reach the application: %#v", service.turnsQuery)
			}
			if apiVersion == application.APIVersionV1 {
				assertVersionProblem(t, recorder)
			} else if recorder.Code != http.StatusOK || !strings.Contains(recorder.Body.String(), string(view.Answer.ID)) {
				t.Fatalf("v2 list lost dynamic turn: %d %s", recorder.Code, recorder.Body.String())
			}
		}
		recorder := httptest.NewRecorder()
		router.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, fmt.Sprintf("/api/v%d/answers/%s/analysis-timeline?workspace_id=%s", apiVersion, testAnswerID, testWorkspaceID), nil))
		if service.timelineQuery.APIVersion != apiVersion {
			t.Fatal("timeline contract did not reach the application")
		}
		if apiVersion == application.APIVersionV1 {
			assertVersionProblem(t, recorder)
		} else if recorder.Code != http.StatusOK {
			t.Fatalf("v2 timeline rejected: %s", recorder.Body.String())
		}
	}
}

func assertVersionProblem(t *testing.T, recorder *httptest.ResponseRecorder) {
	t.Helper()
	var problem struct {
		ErrorCode string `json:"error_code"`
		Retryable bool   `json:"retryable"`
	}
	decodeBody(t, recorder, &problem)
	if recorder.Code != http.StatusConflict || problem.ErrorCode != application.ErrorCodeAPIVersionUnsupported || problem.Retryable {
		t.Fatalf("expected non-retryable version problem: %d %s", recorder.Code, recorder.Body.String())
	}
}

func TestConversationTurnCursorKeepsV1WireAndRejectsCrossVersionBeforeQuery(t *testing.T) {
	t.Parallel()
	codec := NewCursorCodec()
	boundary := conversationdomain.TurnCursor{Ordinal: 7, QuestionID: httpAnalysisID(120)}
	for _, apiVersion := range []application.APIVersion{application.APIVersionV1, application.APIVersionV2} {
		raw, err := codec.encodeTurn(testWorkspaceID, testConversationID, boundary, apiVersion)
		if err != nil {
			t.Fatal(err)
		}
		if apiVersion == application.APIVersionV1 {
			document, err := base64.RawURLEncoding.DecodeString(raw)
			legacy := fmt.Sprintf(`{"schema_version":1,"kind":"turn","workspace_id":"%s","conversation_id":"%s","ordinal":7,"id":"%s"}`, testWorkspaceID, testConversationID, boundary.QuestionID)
			if err != nil || string(document) != legacy {
				t.Fatalf("v1 cursor changed: %s, err=%v", document, err)
			}
		}
		service := &fakeService{}
		router := testRouter(service)
		path := fmt.Sprintf("/api/v%d/conversations/%s/turns?workspace_id=%s&cursor=%s", apiVersion, testConversationID, testWorkspaceID, raw)
		recorder := httptest.NewRecorder()
		router.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, path, nil))
		if recorder.Code != http.StatusOK || service.turnsQuery.Cursor == nil || *service.turnsQuery.Cursor != boundary {
			t.Fatalf("same-version cursor failed: %d %s", recorder.Code, recorder.Body.String())
		}
		service.turnsQuery = application.ListTurnsQuery{}
		otherVersion := application.APIVersionV1 + application.APIVersionV2 - apiVersion
		path = fmt.Sprintf("/api/v%d/conversations/%s/turns?workspace_id=%s&cursor=%s", otherVersion, testConversationID, testWorkspaceID, raw)
		recorder = httptest.NewRecorder()
		router.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, path, nil))
		if recorder.Code != http.StatusBadRequest || !strings.Contains(recorder.Body.String(), conversationdomain.ErrorCodeCursorInvalid) || service.turnsQuery.WorkspaceID != "" {
			t.Fatalf("cross-version cursor reached repository: %d %s query=%#v", recorder.Code, recorder.Body.String(), service.turnsQuery)
		}
	}
}

// The allowed fields and v1 result shape are the original public contract at
// a4c16248ce1082ce500aea2a99d640e4e0195ddf, independent of transport DTO structs.
func assertLegacyAnswerWire(t *testing.T, document []byte, publication conversationdomain.AnswerPublicationStatus) {
	t.Helper()
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(document, &fields); err != nil {
		t.Fatal(err)
	}
	allowed := " id workspace_id conversation_id question_id publication_status result_type result assistant_text citations retrieval_summary current_stage workflow version created_at updated_at "
	for name := range fields {
		if !strings.Contains(allowed, " "+name+" ") {
			t.Fatalf("new field escaped the legacy answer contract: %s", name)
		}
	}
	var workflow map[string]json.RawMessage
	if err := json.Unmarshal(fields["workflow"], &workflow); err != nil {
		t.Fatal(err)
	}
	if len(workflow) != 5 || workflow["run_id"] == nil || workflow["status"] == nil || workflow["version"] == nil || workflow["updated_at"] == nil || workflow["status_url"] == nil {
		t.Fatalf("internal definition metadata escaped the legacy workflow response: %s", fields["workflow"])
	}
	if publication == conversationdomain.AnswerPublicationCompleted {
		var legacy conversationdomain.WorkspaceAnalysisAnswerResult
		decoder := json.NewDecoder(bytes.NewReader(fields["result"]))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&legacy); err != nil {
			t.Fatal(err)
		}
		if err := legacy.Validate(); err != nil {
			t.Fatalf("legacy client rejected result: %v", err)
		}
	}
}

func analysisHTTPAnswer(t *testing.T, definitionVersion int64, status conversationdomain.AnswerPublicationStatus) application.AnswerView {
	t.Helper()
	view := validAnswerView()
	view.Workflow.DefinitionKey, view.Workflow.DefinitionVersion = conversationworkflow.WorkspaceAnalysisDefinitionKey, definitionVersion
	view.Citations = []agentdomain.Citation{}
	if status == conversationdomain.AnswerPublicationPending {
		return view
	}
	modelRunID := httpAnalysisID(101)
	citation := agentdomain.Citation{ID: "citation-1", WorkspaceID: testWorkspaceID, IndexVersionID: httpAnalysisID(102), ChunkID: httpAnalysisID(103), SourceVersionID: httpAnalysisID(104), SourceSpanID: httpAnalysisID(105)}
	var result any
	var resultType conversationdomain.AnswerResultType
	switch status {
	case conversationdomain.AnswerPublicationCompleted:
		resultType = conversationdomain.AnswerResultWorkspaceAnalysis
		if definitionVersion == 1 {
			result = conversationdomain.WorkspaceAnalysisAnswerResult{ResultType: resultType, SchemaID: conversationdomain.WorkspaceAnalysisAnswerSchemaID, SchemaVersion: "v1", ModelRunRef: modelRunID,
				Payload: conversationdomain.WorkspaceAnalysisAnswerPayload{AnswerMarkdown: "已有资料支持这一结论。", Citations: []agentdomain.Citation{citation}, GitStatus: conversationdomain.WorkspaceAnalysisGitStatus{Branch: "main", Head: strings.Repeat("a", 40), Clean: true},
					Budget: conversationdomain.WorkspaceAnalysisBudgetSummary{ModelCalls: 3, ToolCalls: agentdomain.WorkspaceAnalysisV1MinCompletedToolCalls, InputTokens: 120, OutputTokens: 30}, TerminationReason: conversationdomain.WorkspaceAnalysisCompleted}}
		} else {
			result = conversationdomain.WorkspaceAnalysisAnswerResultV2{ResultType: resultType, SchemaID: conversationdomain.WorkspaceAnalysisAnswerSchemaID, SchemaVersion: "v2", ModelRunRef: modelRunID,
				Payload: conversationdomain.WorkspaceAnalysisAnswerPayloadV2{AnswerMarkdown: "已有资料支持这一结论。", Citations: []agentdomain.Citation{citation},
					Budget: conversationdomain.WorkspaceAnalysisBudgetSummaryV2{ModelCalls: 5, ToolCalls: 3, InputTokens: 120, OutputTokens: 30}, TerminationReason: conversationdomain.WorkspaceAnalysisCompleted}}
		}
		view.Workflow.Status = workflowdomain.RunStatusSucceeded
	case conversationdomain.AnswerPublicationRefused:
		resultType = conversationdomain.AnswerResultWorkspaceAnalysisRefusal
		result = conversationdomain.WorkspaceAnalysisRefusalResult{ResultType: resultType, SchemaID: conversationdomain.WorkspaceAnalysisRefusalSchemaID, SchemaVersion: "v1",
			Payload: conversationdomain.WorkspaceAnalysisRefusalPayload{ReasonCode: conversationdomain.WorkspaceAnalysisEvidenceInsufficient, Summary: "资料不足。"}}
		view.Workflow.Status = workflowdomain.RunStatusSucceeded
	default:
		resultType = conversationdomain.AnswerResultWorkspaceAnalysisTermination
		reason := conversationdomain.WorkspaceAnalysisRuntimeFailed
		view.Workflow.Status = workflowdomain.RunStatusFailed
		if status == conversationdomain.WorkspaceAnalysisPublicationCancelled {
			reason, view.Workflow.Status = conversationdomain.WorkspaceAnalysisCancelled, workflowdomain.RunStatusCancelled
		}
		result = conversationdomain.WorkspaceAnalysisTerminationResult{ResultType: resultType, SchemaID: conversationdomain.WorkspaceAnalysisTerminationSchemaID, SchemaVersion: "v1",
			Payload: conversationdomain.WorkspaceAnalysisTerminationPayload{TerminationReason: reason, Summary: "分析已停止。"}}
	}
	document, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	published, err := conversationdomain.CanonicalizeWorkspaceAnalysisPublishedResult(status, resultType, document)
	if err != nil {
		t.Fatal(err)
	}
	projection, err := conversationdomain.ProjectPublishedAnswer(resultType, published.Document)
	if err != nil {
		t.Fatal(err)
	}
	at := view.Answer.CreatedAt.Add(time.Second)
	view.Answer.PublicationStatus, view.Answer.ResultType, view.Answer.Result = status, resultType, published.Document
	view.Answer.ResultHash, view.Answer.ModelRunID = published.Hash, published.ModelRunID
	view.Answer.Version, view.Answer.UpdatedAt, view.Answer.PublishedAt = 2, at, &at
	view.AssistantText, view.Citations = projection.AssistantText, projection.Citations
	if err := conversationdomain.ValidateAnswer(view.Answer); err != nil {
		t.Fatal(err)
	}
	return view
}

func analysisHTTPQuestion(t *testing.T, view application.AnswerView) conversationdomain.Question {
	t.Helper()
	request, err := conversationdomain.CanonicalizeQuestionRequest(conversationdomain.QuestionRequest{WorkspaceID: testWorkspaceID, ConversationID: testConversationID, Mode: conversationdomain.QuestionModeWorkspaceAnalysis, QuestionText: "分析工作区"})
	if err != nil {
		t.Fatal(err)
	}
	hash, err := conversationdomain.ComputeQuestionRequestHash(request)
	if err != nil {
		t.Fatal(err)
	}
	contextHash, _, _, err := conversationdomain.ComputeContextHash(nil)
	if err != nil {
		t.Fatal(err)
	}
	return conversationdomain.Question{ID: view.Answer.QuestionID, Request: request, Ordinal: 1, ContextHash: contextHash, RequestHash: hash, CreatedAt: view.Answer.CreatedAt}
}

func httpAnalysisID(value int) foundation.ID {
	return foundation.ID(fmt.Sprintf("81000000-0000-4000-8000-%012d", value))
}
