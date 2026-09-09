package interviewhttp

import (
	"encoding/json"
	"net/http"
	"reflect"
	"testing"

	interviewapp "github.com/CodeZen-Lizhi/zhixu/internal/review/interview/application"
	"github.com/CodeZen-Lizhi/zhixu/internal/review/interview/domain"
)

// These expected shapes are the Claim wire at a4c16248ce1082ce500aea2a99d640e4e0195ddf.
// Adding NOTE_REVISION must not add fields to a response that old strict clients can read.
func TestClaimInterviewHTTPPreservesLegacyWire(t *testing.T) {
	for _, sourceKind := range []domain.QuestionSourceKind{"", domain.QuestionSourceClaim} {
		name := string(sourceKind)
		if name == "" {
			name = "historical_implicit_claim"
		}
		t.Run(name, func(t *testing.T) {
			fixture := newInterviewHTTPFixture(t)
			fixture.answeredQuestion.SourceKind = sourceKind
			fixture.report.Strengths[0].SourceKind = sourceKind
			fixture.pendingStep.SourceKind = sourceKind
			service := &fakeInterviewService{getResult: interviewapp.Snapshot{
				Session: fixture.completedSession, Questions: []domain.Question{fixture.answeredQuestion}, Turns: []domain.Turn{fixture.turn},
				Report: &fixture.report, Path: &fixture.path, Steps: []domain.PathStep{fixture.pendingStep},
			}}
			router := protectedInterviewRouter(t, service, defaultInterviewPrincipal())
			response := serveInterview(t, router, http.MethodGet, "/api/v1/review/interviews/"+string(fixture.sessionID)+"?workspace_id="+string(fixture.workspaceID), "", nil)
			if response.Code != http.StatusOK {
				t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
			}
			var wire struct {
				Session struct {
					Config struct {
						Scope json.RawMessage `json:"scope"`
					} `json:"config"`
				} `json:"session"`
				Questions []json.RawMessage `json:"questions"`
				Turns     []struct {
					Score json.RawMessage `json:"score"`
				} `json:"turns"`
				Report struct {
					Strengths []json.RawMessage `json:"strengths"`
				} `json:"report"`
				Steps []json.RawMessage `json:"steps"`
			}
			if err := json.Unmarshal(response.Body.Bytes(), &wire); err != nil {
				t.Fatal(err)
			}
			if len(wire.Questions) != 1 || len(wire.Turns) != 1 || len(wire.Report.Strengths) != 1 || len(wire.Steps) != 1 {
				t.Fatal("legacy completed snapshot lost a projection")
			}
			sourceVersionID := string(fixture.pendingStep.SourceVersionID)
			sourceSpanID := string(fixture.pendingStep.SourceSpanID)
			sourceHref := "/api/v1/workspaces/" + string(fixture.workspaceID) + "/source-versions/" + sourceVersionID
			evidence := []map[string]any{{
				"schema_version": "interview-evidence/v1", "claim_id": string(fixture.claimID), "source_version_id": sourceVersionID,
				"source_span_id": sourceSpanID, "evidence_hash": fixture.pendingStep.EvidenceHash, "support_type": "SUPPORTS",
				"source_version_href": sourceHref, "source_span_href": sourceHref + "/spans/" + sourceSpanID,
			}}
			dimension := map[string]any{"value": 0.8, "rationale": "deterministic score rationale"}
			cases := []struct {
				name string
				got  json.RawMessage
				want map[string]any
			}{
				{"scope", wire.Session.Config.Scope, map[string]any{"claim_ids": []string{string(fixture.claimID)}}},
				{"question", wire.Questions[0], map[string]any{
					"id": string(fixture.questionID), "workspace_id": string(fixture.workspaceID), "session_id": string(fixture.sessionID),
					"question_no": 1, "follow_up_no": 0, "claim_id": string(fixture.claimID), "prompt": "Explain idempotency.",
					"status": "ANSWERED", "created_at": "2026-07-27T12:00:00Z", "answered_at": "2026-07-27T12:01:00Z",
				}},
				{"score", wire.Turns[0].Score, map[string]any{
					"schema_version": "interview-score/v1", "correctness": dimension, "coverage": dimension,
					"boundaries": dimension, "clarity": dimension, "evidence": evidence,
				}},
				{"finding", wire.Report.Strengths[0], map[string]any{"claim_id": string(fixture.claimID), "detail": "The answer was evidence-backed.", "evidence": evidence}},
				{"path_step", wire.Steps[0], map[string]any{
					"id": string(fixture.stepID), "workspace_id": string(fixture.workspaceID), "path_id": string(fixture.pathID),
					"step_no": 1, "claim_id": string(fixture.claimID), "source_version_id": sourceVersionID, "source_span_id": sourceSpanID,
					"evidence_hash": fixture.pendingStep.EvidenceHash, "title": "Review idempotency", "rationale": "Revisit the evidence.",
					"status": "PENDING", "version": 1, "created_at": "2026-07-27T12:01:00Z", "updated_at": "2026-07-27T12:01:00Z",
				}},
			}
			for _, test := range cases {
				t.Run(test.name, func(t *testing.T) {
					wantJSON, err := json.Marshal(test.want)
					if err != nil {
						t.Fatal(err)
					}
					var got, want any
					if err := json.Unmarshal(test.got, &got); err != nil {
						t.Fatal(err)
					}
					if err := json.Unmarshal(wantJSON, &want); err != nil {
						t.Fatal(err)
					}
					if !reflect.DeepEqual(got, want) {
						t.Fatalf("legacy wire changed: got %s; want %s", test.got, wantJSON)
					}
				})
			}
		})
	}
}
