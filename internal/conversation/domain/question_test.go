package domain

import (
	"encoding/json"
	"errors"
	"reflect"
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
	if canonical.Mode != QuestionModeRAG || canonical.QuestionText != "How does runtime recovery work?" || canonical.AnswerDepth != AnswerDepthStandard ||
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

func TestCanonicalizeQuestionModeDefaultsToRAGAndRejectsUnknown(t *testing.T) {
	for _, test := range []struct {
		input QuestionMode
		want  QuestionMode
	}{
		{want: QuestionModeRAG},
		{input: QuestionModeRAG, want: QuestionModeRAG},
		{input: QuestionModeWorkspaceAnalysis, want: QuestionModeWorkspaceAnalysis},
	} {
		got, err := CanonicalizeQuestionMode(test.input)
		if err != nil || got != test.want {
			t.Fatalf("CanonicalizeQuestionMode(%q)=%q, err=%v, want %q", test.input, got, err, test.want)
		}
	}
	_, err := CanonicalizeQuestionMode("unbounded_agent")
	var typed *foundation.Error
	if !errors.As(err, &typed) || typed.Code != WorkspaceAnalysisModeInvalidCode || typed.Kind != foundation.ErrorInvalidInput {
		t.Fatalf("unknown mode err=%v", err)
	}
}

func TestQuestionRequestHashPreservesRAGV1AndSeparatesWorkspaceAnalysisV2(t *testing.T) {
	base := QuestionRequest{
		WorkspaceID: testWorkspaceID, ConversationID: testConversationID,
		QuestionText: "How does runtime recovery work?", Scope: QuestionScope{RetrievalMode: retrievaldomain.SearchModeHybrid},
	}
	omittedHash, err := ComputeQuestionRequestHash(base)
	if err != nil {
		t.Fatal(err)
	}
	explicitRAG := base
	explicitRAG.Mode = QuestionModeRAG
	ragHash, err := ComputeQuestionRequestHash(explicitRAG)
	if err != nil || ragHash != omittedHash || ragHash != "be89092d1d00b9a1360fa3ec81ceea575b40166dce4bd4410a726c344ddd2bd6" {
		t.Fatalf("omitted hash=%s explicit rag hash=%s err=%v", omittedHash, ragHash, err)
	}
	workspaceAnalysis := base
	workspaceAnalysis.Mode = QuestionModeWorkspaceAnalysis
	workspaceHash, err := ComputeQuestionRequestHash(workspaceAnalysis)
	if err != nil {
		t.Fatal(err)
	}
	if workspaceHash != "c9a855df8d3eb12dac678a8474b2ec6f58f1f69c21007b29755b97950a32ded3" || workspaceHash == ragHash {
		t.Fatalf("workspace analysis hash=%s rag hash=%s", workspaceHash, ragHash)
	}
}

func TestCanonicalizeQuestionRequestRejectsUnsafeBoundaries(t *testing.T) {
	base := QuestionRequest{
		WorkspaceID: testWorkspaceID, ConversationID: testConversationID,
		QuestionText: "valid question", Scope: QuestionScope{RetrievalMode: retrievaldomain.SearchModeHybrid},
	}
	invalidUTF8 := string([]byte{'q', 0xff})
	tests := []struct {
		name     string
		wantCode string
		mutate   func(*QuestionRequest)
	}{
		{name: "invalid workspace", mutate: func(value *QuestionRequest) { value.WorkspaceID = "bad" }},
		{name: "same identities", mutate: func(value *QuestionRequest) { value.ConversationID = value.WorkspaceID }},
		{name: "empty question", mutate: func(value *QuestionRequest) { value.QuestionText = "  " }},
		{name: "invalid utf8", mutate: func(value *QuestionRequest) { value.QuestionText = invalidUTF8 }},
		{name: "nul question", mutate: func(value *QuestionRequest) { value.QuestionText = "unsafe\x00question" }},
		{name: "oversized question", mutate: func(value *QuestionRequest) { value.QuestionText = strings.Repeat("x", MaxQuestionBytes+1) }},
		{name: "unknown depth", mutate: func(value *QuestionRequest) { value.AnswerDepth = "verbose" }},
		{name: "unknown format", mutate: func(value *QuestionRequest) { value.OutputFormat = "json" }},
		{name: "unknown mode", wantCode: WorkspaceAnalysisModeInvalidCode, mutate: func(value *QuestionRequest) { value.Mode = "unbounded_agent" }},
		{name: "workspace analysis web scope", wantCode: WorkspaceAnalysisScopeUnsupportedCode, mutate: func(value *QuestionRequest) {
			value.Mode = QuestionModeWorkspaceAnalysis
			value.Scope.AllowWeb = true
		}},
		{name: "workspace analysis original source scope", wantCode: WorkspaceAnalysisScopeUnsupportedCode, mutate: func(value *QuestionRequest) {
			value.Mode = QuestionModeWorkspaceAnalysis
			value.Scope.AllowOriginalSources = true
		}},
		{name: "workspace analysis source filter", wantCode: WorkspaceAnalysisScopeUnsupportedCode, mutate: func(value *QuestionRequest) {
			value.Mode = QuestionModeWorkspaceAnalysis
			value.Scope.Filter.SourceIDs = []foundation.ID{"10000000-0000-4000-8000-000000000003"}
		}},
		{name: "workspace analysis version filter", wantCode: WorkspaceAnalysisScopeUnsupportedCode, mutate: func(value *QuestionRequest) {
			value.Mode = QuestionModeWorkspaceAnalysis
			value.Scope.Filter.SourceVersionIDs = []foundation.ID{"10000000-0000-4000-8000-000000000004"}
		}},
		{name: "workspace analysis path filter", wantCode: WorkspaceAnalysisScopeUnsupportedCode, mutate: func(value *QuestionRequest) {
			value.Mode = QuestionModeWorkspaceAnalysis
			value.Scope.Filter.PathPrefixes = []string{"docs"}
		}},
		{name: "workspace analysis time filter", wantCode: WorkspaceAnalysisScopeUnsupportedCode, mutate: func(value *QuestionRequest) {
			value.Mode = QuestionModeWorkspaceAnalysis
			capturedAt := time.Date(2026, 8, 16, 0, 0, 0, 0, time.UTC)
			value.Scope.Filter.CapturedAtFrom = &capturedAt
		}},
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
			wantCode := test.wantCode
			if wantCode == "" {
				wantCode = ErrorCodeQuestionInvalid
			}
			if !errors.As(err, &typed) || typed.Code != wantCode || typed.Kind != foundation.ErrorInvalidInput {
				t.Fatalf("err=%v", err)
			}
		})
	}
}

func TestDecodeQuestionScopeStrictlyRestoresCanonicalPersistenceDocument(t *testing.T) {
	raw := json.RawMessage(`{
		"allow_web":false,
		"captured_at_before":null,
		"source_version_ids":[],
		"path_prefixes":["docs/runtime"],
		"retrieval_mode":"hybrid",
		"allow_original_sources":true,
		"source_ids":["10000000-0000-4000-8000-000000000003"],
		"captured_at_from":"2026-07-19T00:00:00Z"
	}`)

	decoded, err := DecodeQuestionScope(testWorkspaceID, raw)
	if err != nil {
		t.Fatal(err)
	}
	from := time.Date(2026, 7, 19, 0, 0, 0, 0, time.UTC)
	want := QuestionScope{
		RetrievalMode: retrievaldomain.SearchModeHybrid,
		Filter: retrievaldomain.SearchFilter{
			SourceIDs:        []foundation.ID{"10000000-0000-4000-8000-000000000003"},
			SourceVersionIDs: []foundation.ID{},
			PathPrefixes:     []string{"docs/runtime"},
			CapturedAtFrom:   &from,
		},
		AllowOriginalSources: true,
	}
	if !reflect.DeepEqual(decoded, want) {
		t.Fatalf("decoded=%#v want=%#v", decoded, want)
	}

	request := QuestionRequest{
		WorkspaceID: testWorkspaceID, ConversationID: testConversationID, QuestionText: "question", Scope: decoded,
	}
	encoded, err := EncodeQuestionScope(request)
	if err != nil {
		t.Fatal(err)
	}
	roundTrip, err := DecodeQuestionScope(testWorkspaceID, encoded)
	if err != nil || !reflect.DeepEqual(roundTrip, decoded) {
		t.Fatalf("round trip=%#v err=%v encoded=%s", roundTrip, err, encoded)
	}
}

func TestDecodeQuestionScopeRejectsPersistenceDrift(t *testing.T) {
	valid := `{"retrieval_mode":"hybrid","source_ids":[],"source_version_ids":[],"path_prefixes":[],"captured_at_from":null,"captured_at_before":null,"allow_original_sources":false,"allow_web":false}`
	tests := []struct {
		name        string
		workspaceID foundation.ID
		raw         string
	}{
		{name: "invalid workspace", workspaceID: "bad", raw: valid},
		{name: "unknown field", workspaceID: testWorkspaceID, raw: valid[:len(valid)-1] + `,"unknown":true}`},
		{name: "duplicate field", workspaceID: testWorkspaceID, raw: valid[:len(valid)-1] + `,"allow_web":false}`},
		{name: "trailing value", workspaceID: testWorkspaceID, raw: valid + `{}`},
		{name: "invalid type", workspaceID: testWorkspaceID, raw: strings.Replace(valid, `"allow_web":false`, `"allow_web":"false"`, 1)},
		{name: "null boolean", workspaceID: testWorkspaceID, raw: strings.Replace(valid, `"allow_web":false`, `"allow_web":null`, 1)},
		{name: "missing boolean", workspaceID: testWorkspaceID, raw: strings.Replace(valid, `,"allow_web":false`, "", 1)},
		{name: "missing nullable time", workspaceID: testWorkspaceID, raw: strings.Replace(valid, `"captured_at_from":null,`, "", 1)},
		{name: "missing required list", workspaceID: testWorkspaceID, raw: strings.Replace(valid, `"source_ids":[],`, "", 1)},
		{name: "invalid time", workspaceID: testWorkspaceID, raw: strings.Replace(valid, `"captured_at_from":null`, `"captured_at_from":"tomorrow"`, 1)},
		{name: "non canonical time", workspaceID: testWorkspaceID, raw: strings.Replace(valid, `"captured_at_from":null`, `"captured_at_from":"2026-07-19T08:00:00+08:00"`, 1)},
		{name: "non canonical list", workspaceID: testWorkspaceID, raw: strings.Replace(valid, `"source_ids":[]`, `"source_ids":["10000000-0000-4000-8000-000000000004","10000000-0000-4000-8000-000000000003"]`, 1)},
		{name: "oversized document", workspaceID: testWorkspaceID, raw: strings.Repeat(" ", MaxQuestionScopeBytes) + valid},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := DecodeQuestionScope(test.workspaceID, json.RawMessage(test.raw))
			var typed *foundation.Error
			if !errors.As(err, &typed) || typed.Code != ErrorCodeQuestionInvalid || typed.Kind != foundation.ErrorInvalidInput {
				t.Fatalf("err=%v", err)
			}
		})
	}
}
