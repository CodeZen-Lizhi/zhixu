package domain

import (
	"encoding/json"
	"testing"
	"time"

	agentdomain "github.com/CodeZen-Lizhi/zhixu/internal/agent/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	retrievaldomain "github.com/CodeZen-Lizhi/zhixu/internal/retrieval/domain"
)

func TestCanonicalizePublishedResultOwnsStrictDocumentAndHash(t *testing.T) {
	raw := []byte(`{
		"payload":{"suggested_scopes":[],"question":"Which environment?","reason":"scope is ambiguous"},
		"model_run_ref":"10000000-0000-4000-8000-000000000006",
		"schema_version":"v1","schema_id":"conversation.clarification","result_type":"clarification"
	}`)
	result, err := CanonicalizePublishedResult(AnswerResultClarification, raw)
	if err != nil {
		t.Fatal(err)
	}
	if result.ModelRunID != "10000000-0000-4000-8000-000000000006" ||
		result.Hash != "565150082ac1be740c534fa8ccd4b956477faf2cff8e384e75d52051065e6d30" ||
		string(result.Document) != `{"result_type":"clarification","schema_id":"conversation.clarification","schema_version":"v1","model_run_ref":"10000000-0000-4000-8000-000000000006","payload":{"reason":"scope is ambiguous","question":"Which environment?","suggested_scopes":[]}}` {
		t.Fatalf("result=%#v document=%s", result, result.Document)
	}
	if _, err := CanonicalizePublishedResult(AnswerResultRAGAnswer, raw); errorCode(err) != ErrorCodeAnswerInvalid {
		t.Fatalf("mismatched result err=%v", err)
	}
	unknown := append(append([]byte(nil), raw[:len(raw)-2]...), []byte(`,"unknown":true}`)...)
	if _, err := CanonicalizePublishedResult(AnswerResultClarification, unknown); errorCode(err) != ErrorCodeAnswerInvalid {
		t.Fatalf("unknown field err=%v", err)
	}
	if _, err := CanonicalizePublishedResult("draft", raw); errorCode(err) != ErrorCodeAnswerInvalid {
		t.Fatalf("unknown type err=%v", err)
	}
}

func TestValidateAnswerRequiresOneCanonicalTerminalPublication(t *testing.T) {
	now := time.Date(2026, 7, 19, 10, 0, 0, 0, time.UTC)
	pending := Answer{
		ID: "10000000-0000-4000-8000-000000000030", WorkspaceID: testWorkspaceID,
		ConversationID: testConversationID, QuestionID: "10000000-0000-4000-8000-000000000010",
		WorkflowRunID:     "10000000-0000-4000-8000-000000000031",
		PublicationStatus: AnswerPublicationPending, Version: 1, CreatedAt: now, UpdatedAt: now,
	}
	if err := ValidateAnswer(pending); err != nil {
		t.Fatalf("pending answer: %v", err)
	}

	published := validRAGPublishedResult(t)
	summary := validCompletedRetrievalSummary(t)
	publishedAt := now.Add(time.Second)
	completed := pending
	completed.ModelRunID = &published.ModelRunID
	completed.PublicationStatus = AnswerPublicationCompleted
	completed.ResultType = published.Type
	completed.Result = published.Document
	completed.ResultHash = published.Hash
	completed.RetrievalSummary = &summary
	completed.Version = 2
	completed.UpdatedAt = publishedAt
	completed.PublishedAt = &publishedAt
	if err := ValidateAnswer(completed); err != nil {
		t.Fatalf("completed answer: %v", err)
	}

	invalids := []func(*Answer){
		func(value *Answer) { value.ResultHash = "bad" },
		func(value *Answer) { value.Result = append([]byte(" \n"), value.Result...) },
		func(value *Answer) { value.ResultType = AnswerResultRefusal },
		func(value *Answer) { value.PublicationStatus = AnswerPublicationRefused },
		func(value *Answer) {
			other := foundation.ID("10000000-0000-4000-8000-000000000099")
			value.ModelRunID = &other
		},
		func(value *Answer) { value.RetrievalSummary.SelectedCount = 0 },
		func(value *Answer) { value.Version = 3 },
		func(value *Answer) { value.PublishedAt = nil },
	}
	for _, mutate := range invalids {
		value := completed
		copiedSummary := *completed.RetrievalSummary
		value.RetrievalSummary = &copiedSummary
		mutate(&value)
		if err := ValidateAnswer(value); errorCode(err) != ErrorCodeAnswerInvalid {
			t.Fatalf("answer=%#v err=%v", value, err)
		}
	}

	pendingWithResult := pending
	pendingWithResult.Result = published.Document
	if err := ValidateAnswer(pendingWithResult); errorCode(err) != ErrorCodeAnswerInvalid {
		t.Fatalf("pending result err=%v", err)
	}
}

func TestAnswerPublicationTransitionIsSingleTerminalChoice(t *testing.T) {
	for _, terminal := range []AnswerPublicationStatus{
		AnswerPublicationCompleted, AnswerPublicationRefused, AnswerPublicationClarificationRequired,
	} {
		if err := ValidateAnswerPublicationTransition(AnswerPublicationPending, terminal); err != nil {
			t.Fatalf("terminal=%s err=%v", terminal, err)
		}
	}
	for _, invalid := range [][2]AnswerPublicationStatus{
		{"", AnswerPublicationCompleted},
		{AnswerPublicationCompleted, AnswerPublicationRefused},
		{AnswerPublicationPending, AnswerPublicationPending},
		{AnswerPublicationPending, "failed"},
	} {
		if err := ValidateAnswerPublicationTransition(invalid[0], invalid[1]); errorCode(err) != ErrorCodeAnswerTransitionInvalid {
			t.Fatalf("transition=%q->%q err=%v", invalid[0], invalid[1], err)
		}
	}
}

func validRAGPublishedResult(t *testing.T) PublishedResult {
	t.Helper()
	payload := agentdomain.RAGAnswerPayloadV2{
		RAGAnswerPayload: agentdomain.RAGAnswerPayload{
			Conclusion: "Approved evidence supports the answer.",
			Assertions: []agentdomain.Assertion{{
				ID: "assertion-1", Text: "The fact is supported.", Kind: agentdomain.AssertionFactual, CitationIDs: []string{"citation-1"},
			}},
			Citations: []agentdomain.Citation{{
				ID: "citation-1", WorkspaceID: testWorkspaceID,
				IndexVersionID: "10000000-0000-4000-8000-000000000020", ChunkID: "10000000-0000-4000-8000-000000000021",
				SourceVersionID: "10000000-0000-4000-8000-000000000022", SourceSpanID: "10000000-0000-4000-8000-000000000023",
			}},
			ConflictPositions: []agentdomain.ConflictPosition{}, ConflictSummary: "",
		},
		RelatedTopics: []agentdomain.RelatedTopic{{
			TopicID: "10000000-0000-4000-8000-000000000024", Name: "Runtime safety", CitationIDs: []string{"citation-1"},
		}},
		FollowUpQuestions: []string{"What changed in the approved source?"},
	}
	envelope := agentdomain.RAGAnswerResultV2{
		ResultType: agentdomain.ResultTypeRAGAnswer, SchemaID: agentdomain.RAGAnswerSchemaID,
		SchemaVersion: agentdomain.OutputSchemaVersionV2, ModelRunRef: "10000000-0000-4000-8000-000000000006",
		Payload: payload,
	}
	raw, err := json.Marshal(envelope)
	if err != nil {
		t.Fatal(err)
	}
	result, err := CanonicalizePublishedResult(AnswerResultRAGAnswer, raw)
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func validCompletedRetrievalSummary(t *testing.T) RetrievalSummary {
	t.Helper()
	summary, err := CanonicalizeRetrievalSummary(testWorkspaceID, RetrievalSummary{
		Rewrites: []string{"runtime recovery"}, RequestedMode: retrievaldomain.SearchModeHybrid,
		EffectiveMode:  retrievaldomain.SearchModeKeyword,
		Scope:          RetrievalScopeSummary{SourceIDs: []foundation.ID{}, SourceVersionIDs: []foundation.ID{}, PathPrefixes: []string{}},
		IndexVersionID: stringIDPointer("10000000-0000-4000-8000-000000000020"),
		CandidateCount: 1, SelectedCount: 1, Degradations: []RetrievalDegradation{{
			Capability: retrievaldomain.SearchDegradationVector, Code: "EMBEDDING_UNAVAILABLE", Retryable: true,
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	return summary
}
