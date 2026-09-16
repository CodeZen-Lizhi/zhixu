package application

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/CodeZen-Lizhi/zhixu/internal/organizing/domain"
)

func TestAnchorModelOutputKeepsScopeRecommendationsSeparate(t *testing.T) {
	valid := `{"recommendation":{"title":"Redis 复习","kind":"INITIAL_SCOPE","scope":{"topics":["Redis"],"audiences":["复习"],"description":"Redis 使用与机制"},"reason":"资料重点讨论 Redis","evidence":["S001"]},"no_recommendation":false}`
	out, err := DecodeAnchorModelOutput([]byte(valid))
	if err != nil || out.Recommendation == nil || out.Recommendation.Scope == nil {
		t.Fatalf("decode %+v %v", out, err)
	}
	if _, err = DecodeAnchorModelOutput([]byte(`{"recommendation":null,"no_recommendation":true}`)); err != nil {
		t.Fatal(err)
	}
	for _, bad := range []string{
		strings.Replace(valid, `"INITIAL_SCOPE"`, `"SOURCE_ASSOCIATION"`, 1), strings.Replace(valid, `"no_recommendation":false`, `"no_recommendation":true`, 1),
		strings.Replace(valid, `"S001"`, `"S001","S001"`, 1), strings.Replace(valid, `"S001"`, `"S033"`, 1), strings.Replace(valid, `"S001"`, `"S01"`, 1),
		strings.Replace(valid, `"title":"Redis 复习"`, `"Title":"Redis 复习"`, 1), strings.Replace(valid, `"topics":["Redis"]`, `"topics":["Redis"],"permission":"publish"`, 1),
		strings.Replace(valid, `"no_recommendation":false`, `"no_recommendation":null`, 1), valid + `{}`, strings.Replace(valid, `"reason":`, `"reason":"other","reason":`, 1),
	} {
		if _, err := DecodeAnchorModelOutput([]byte(bad)); err == nil {
			t.Fatalf("accepted invalid output %s", bad)
		}
	}
	scope := out.Recommendation.Scope
	out.Recommendation.Kind = "SOURCE_ASSOCIATION"
	out.Recommendation.Scope = nil
	raw, _ := json.Marshal(out)
	if _, err = DecodeAnchorModelOutput(raw); err != nil {
		t.Fatal(err)
	}
	out.Recommendation.Kind = "SCOPE_ADJUSTMENT"
	out.Recommendation.Scope = scope
	raw, _ = json.Marshal(out)
	if _, err = DecodeAnchorModelOutput(raw); err != nil {
		t.Fatal(err)
	}
	if _, ok := AnchorEvidenceIndex("S002", 1); ok {
		t.Fatal("label outside frozen evidence accepted")
	}
}

func TestBindAnchorModelOutputSelectsExactFrozenEvidence(t *testing.T) {
	input, _ := synthesisContractFixture()
	first := input.Sources[0].Reference
	second := first
	second.SourceSpanID = synthesisContractID(98)
	request := AnchorRecommendationRequest{ID: synthesisContractID(90), WorkspaceID: first.Source.WorkspaceID, NoteID: synthesisContractID(91), BasisRevisionID: synthesisContractID(92), AnchorID: synthesisContractID(93), Kind: "SOURCE_ASSOCIATION", ExpectedScopeVersion: 1, Source: &first.Source, Evidence: []domain.SynthesisSourceRef{first, second}}
	raw := []byte(`{"recommendation":{"title":"Redis","kind":"SOURCE_ASSOCIATION","scope":null,"reason":"第二片段与维护目的相关","evidence":["S002"]},"no_recommendation":false}`)
	bound, err := BindAnchorModelOutput(raw, request)
	if err != nil || bound.Recommendation == nil || len(bound.Recommendation.Evidence) != 1 || bound.Recommendation.Evidence[0] != second {
		t.Fatalf("binding %+v %v", bound, err)
	}
	request.Evidence = request.Evidence[:1]
	if _, err = BindAnchorModelOutput(raw, request); err == nil {
		t.Fatal("unopened label accepted")
	}
	request.Evidence = []domain.SynthesisSourceRef{first, second}
	request.WorkspaceID = synthesisContractID(99)
	if _, err = BindAnchorModelOutput(raw, request); err == nil {
		t.Fatal("foreign workspace evidence accepted")
	}
	request.WorkspaceID = first.Source.WorkspaceID
	request.Kind = AnchorInitialScopeRecommendation
	request.AnchorID = ""
	request.ExpectedScopeVersion = 0
	request.Source = nil
	if _, err = BindAnchorModelOutput(raw, request); err == nil {
		t.Fatal("initial inference turned into source approval")
	}
	if _, err = BindAnchorModelOutput([]byte(`{"recommendation":null,"no_recommendation":true}`), request); err != nil {
		t.Fatal(err)
	}
}
