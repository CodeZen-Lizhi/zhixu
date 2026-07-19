package domain

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	retrievaldomain "github.com/CodeZen-Lizhi/zhixu/internal/retrieval/domain"
)

const (
	testWorkspaceID    foundation.ID = "10000000-0000-4000-8000-000000000001"
	testConversationID foundation.ID = "10000000-0000-4000-8000-000000000002"
)

func TestCanonicalizeQuestionRequestOwnsScopeOptionsAndHash(t *testing.T) {
	from := time.Date(2026, 7, 19, 8, 0, 0, 0, time.FixedZone("CST", 8*60*60))
	request := QuestionRequest{
		WorkspaceID: testWorkspaceID, ConversationID: testConversationID,
		QuestionText: "  How does runtime recovery work?  ",
		Scope: QuestionScope{
			RetrievalMode: retrievaldomain.SearchModeHybrid,
			Filter: retrievaldomain.SearchFilter{
				SourceIDs: []foundation.ID{
					"10000000-0000-4000-8000-000000000004",
					"10000000-0000-4000-8000-000000000003",
					"10000000-0000-4000-8000-000000000004",
				},
				PathPrefixes: []string{" docs/runtime/ ", "docs/runtime"}, CapturedAtFrom: &from,
			},
		},
	}
	canonical, err := CanonicalizeQuestionRequest(request)
	if err != nil {
		t.Fatal(err)
	}
	if canonical.QuestionText != "How does runtime recovery work?" || canonical.AnswerDepth != AnswerDepthStandard ||
		canonical.OutputFormat != OutputFormatMarkdown || len(canonical.Scope.Filter.SourceIDs) != 2 ||
		canonical.Scope.Filter.SourceIDs[0] != "10000000-0000-4000-8000-000000000003" ||
		len(canonical.Scope.Filter.PathPrefixes) != 1 || canonical.Scope.Filter.PathPrefixes[0] != "docs/runtime" ||
		canonical.Scope.Filter.CapturedAtFrom == nil || canonical.Scope.Filter.CapturedAtFrom.Location() != time.UTC {
		t.Fatalf("canonical=%#v", canonical)
	}
	if request.QuestionText != "  How does runtime recovery work?  " || len(request.Scope.Filter.SourceIDs) != 3 || request.Scope.Filter.PathPrefixes[0] != " docs/runtime/ " {
		t.Fatal("canonicalization mutated caller input")
	}

	simple := QuestionRequest{
		WorkspaceID: testWorkspaceID, ConversationID: testConversationID,
		QuestionText: "How does runtime recovery work?",
		Scope:        QuestionScope{RetrievalMode: retrievaldomain.SearchModeHybrid},
	}
	hash, err := ComputeQuestionRequestHash(simple)
	if err != nil {
		t.Fatal(err)
	}
	if hash != "be89092d1d00b9a1360fa3ec81ceea575b40166dce4bd4410a726c344ddd2bd6" {
		t.Fatalf("question request hash=%s", hash)
	}
	equivalent := simple
	equivalent.QuestionText = "  How does runtime recovery work?  "
	equivalent.AnswerDepth = AnswerDepthStandard
	equivalent.OutputFormat = OutputFormatMarkdown
	equivalentHash, err := ComputeQuestionRequestHash(equivalent)
	if err != nil || equivalentHash != hash {
		t.Fatalf("equivalent hash=%s err=%v", equivalentHash, err)
	}
}

func TestCanonicalizeQuestionRequestRejectsUnsafeBoundaries(t *testing.T) {
	base := QuestionRequest{
		WorkspaceID: testWorkspaceID, ConversationID: testConversationID,
		QuestionText: "valid question", Scope: QuestionScope{RetrievalMode: retrievaldomain.SearchModeHybrid},
	}
	invalidUTF8 := string([]byte{'q', 0xff})
	tests := []struct {
		name   string
		mutate func(*QuestionRequest)
	}{
		{name: "invalid workspace", mutate: func(value *QuestionRequest) { value.WorkspaceID = "bad" }},
		{name: "same identities", mutate: func(value *QuestionRequest) { value.ConversationID = value.WorkspaceID }},
		{name: "empty question", mutate: func(value *QuestionRequest) { value.QuestionText = "  " }},
		{name: "invalid utf8", mutate: func(value *QuestionRequest) { value.QuestionText = invalidUTF8 }},
		{name: "nul question", mutate: func(value *QuestionRequest) { value.QuestionText = "unsafe\x00question" }},
		{name: "oversized question", mutate: func(value *QuestionRequest) { value.QuestionText = strings.Repeat("x", MaxQuestionBytes+1) }},
		{name: "unknown depth", mutate: func(value *QuestionRequest) { value.AnswerDepth = "verbose" }},
		{name: "unknown format", mutate: func(value *QuestionRequest) { value.OutputFormat = "json" }},
		{name: "unsafe path", mutate: func(value *QuestionRequest) { value.Scope.Filter.PathPrefixes = []string{"../secret"} }},
		{name: "oversized scope", mutate: func(value *QuestionRequest) {
			value.Scope.Filter.PathPrefixes = []string{strings.Repeat("x", MaxQuestionScopeBytes)}
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			value := base
			test.mutate(&value)
			_, err := CanonicalizeQuestionRequest(value)
			var typed *foundation.Error
			if !errors.As(err, &typed) || typed.Code != ErrorCodeQuestionInvalid || typed.Kind != foundation.ErrorInvalidInput {
				t.Fatalf("err=%v", err)
			}
		})
	}
}
