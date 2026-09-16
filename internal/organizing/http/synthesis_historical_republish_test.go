package organizinghttp

import (
	"context"
	"encoding/json"
	authhttp "github.com/CodeZen-Lizhi/zhixu/internal/auth/http"
	"github.com/CodeZen-Lizhi/zhixu/internal/capability"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	app "github.com/CodeZen-Lizhi/zhixu/internal/organizing/application"
	"github.com/gin-gonic/gin"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

type historicalHTTPStub struct {
	calls     int
	out       app.SynthesisHistoricalRepublishReview
	lastApply app.ApplySynthesisHistoricalRepublish
	lastBegin app.BeginSynthesisHistoricalRepublish
}

func (s *historicalHTTPStub) HistoricalRepublish(c app.SynthesisManuscriptCaller) (app.SynthesisHistoricalRepublishOwner, error) {
	if err := c.Authorize(); err != nil {
		return nil, err
	}
	return s, nil
}
func (s *historicalHTTPStub) Target(context.Context, foundation.ID, foundation.ID, foundation.ID) (app.SynthesisHistoricalRepublishTarget, error) {
	s.calls++
	return s.out.Target, nil
}
func (s *historicalHTTPStub) Begin(_ context.Context, c app.BeginSynthesisHistoricalRepublish) (app.SynthesisHistoricalRepublishReview, error) {
	s.calls++
	s.lastBegin = c
	return s.out, nil
}
func (s *historicalHTTPStub) Read(context.Context, app.ReadSynthesisHistoricalRepublish) (app.SynthesisHistoricalRepublishReview, error) {
	s.calls++
	return s.out, nil
}
func (s *historicalHTTPStub) Apply(_ context.Context, c app.ApplySynthesisHistoricalRepublish) (app.SynthesisHistoricalRepublishReview, error) {
	s.calls++
	s.lastApply = c
	return s.out, nil
}
func (s *historicalHTTPStub) Resume(context.Context, app.ResumeSynthesisHistoricalRepublish) (app.SynthesisHistoricalRepublishReview, error) {
	s.calls++
	return s.out, nil
}
func historicalRouter(t *testing.T, s *historicalHTTPStub, scopes []capability.Capability, authenticated bool) http.Handler {
	t.Helper()
	r := gin.New()
	g := r.Group("/api/v1")
	if authenticated {
		h, err := authhttp.NewHandler(remergeAuth{scopes: scopes}, authhttp.Options{AllowedOrigins: []string{"http://127.0.0.1:8080"}})
		if err != nil {
			t.Fatal(err)
		}
		g.Use(h.Middleware)
	}
	NewSynthesisHandler(synthesisNotesStub{}, synthesisProcessingStub{}, time.Second).WithHistoricalRepublish(s).Routes(g)
	return r
}
func TestHistoricalRepublishHTTPPrincipalAndExactCommands(t *testing.T) {
	base := "/api/v1/workspaces/" + string(testID(1)) + "/synthesis/notes/" + string(testID(2)) + "/historical-republish"
	targetURL := base + "/target?selected_revision_id=" + string(testID(4))
	applyURL := base + "/" + string(testID(3)) + "/apply"
	resumeURL := base + "/" + string(testID(3)) + "/resume"
	target := app.SynthesisHistoricalRepublishTarget{WorkspaceID: testID(1), NoteID: testID(2), SelectedRevisionID: testID(4), SelectedProjectionHash: strings.Repeat("a", 64), ExpectedRevisionID: testID(5), ExpectedNoteVersion: 2, ExpectedDocumentID: testID(6), ExpectedDocumentVersion: 3}
	s := &historicalHTTPStub{out: app.SynthesisHistoricalRepublishReview{CurrentScope: json.RawMessage("null"), WorkspaceID: testID(1), NoteID: testID(2), AttemptID: testID(3), State: "READY", Target: target, Candidate: "旧正文\n", CurrentContent: "人工内容\n", PublishedContent: "正式正文\n", PreviewFingerprint: strings.Repeat("b", 64)}}
	request := func(r http.Handler, method, path, body string, headers ...string) *httptest.ResponseRecorder {
		q := httptest.NewRequest(method, path, strings.NewReader(body))
		q.Header.Set("Authorization", "Bearer test")
		q.Header.Set("Content-Type", "application/json")
		for _, h := range headers {
			q.Header.Add("Idempotency-Key", h)
		}
		w := httptest.NewRecorder()
		r.ServeHTTP(w, q)
		return w
	}
	begin, _ := json.Marshal(app.BeginSynthesisHistoricalRepublish{SynthesisHistoricalRepublishTarget: target, IdempotencyKey: "begin"})
	apply, _ := json.Marshal(app.ApplySynthesisHistoricalRepublish{WorkspaceID: testID(1), NoteID: testID(2), AttemptID: testID(3), IdempotencyKey: "apply", PreviewFingerprint: strings.Repeat("b", 64), ConfirmExactRestore: true})
	for _, scopes := range [][]capability.Capability{nil, {capability.ReadLocal}, {capability.WriteProposal}} {
		r := historicalRouter(t, s, scopes, true)
		for _, tc := range []struct{ method, path, body string }{{"GET", targetURL, ""}, {"GET", base + "?key=begin", ""}, {"POST", base, string(begin)}, {"POST", applyURL, string(apply)}, {"POST", resumeURL, "{}"}} {
			before := s.calls
			w := request(r, tc.method, tc.path, tc.body)
			if w.Code != 403 || s.calls != before {
				t.Fatalf("unauthorized %v %s: %d %s", scopes, tc.path, w.Code, w.Body.String())
			}
		}
	}
	if w := request(historicalRouter(t, s, nil, false), "GET", targetURL, ""); w.Code != 403 {
		t.Fatalf("missing principal %d", w.Code)
	}
	r := historicalRouter(t, s, []capability.Capability{capability.ReadLocal, capability.WriteProposal}, true)
	for _, tc := range []struct{ method, path, body string }{{"GET", targetURL, ""}, {"GET", base + "?key=begin", ""}, {"GET", base + "?attempt_id=" + string(testID(3)), ""}, {"POST", base, string(begin)}} {
		w := request(r, tc.method, tc.path, tc.body)
		if w.Code != 200 {
			t.Fatalf("valid %s: %d %s", tc.path, w.Code, w.Body.String())
		}
		if strings.Contains(w.Body.String(), "Authority") || strings.Contains(w.Body.String(), "Root") {
			t.Fatal("internal projection leaked")
		}
	}
	for _, tc := range []struct{ method, path, body string }{{"GET", targetURL + "&selected_revision_id=" + string(testID(4)), ""}, {"GET", targetURL, "{}"}, {"GET", base + "?key=begin&scope=x", ""}, {"GET", base + "?key=a&key=b", ""}, {"POST", resumeURL, `{"model":"fake"}`}, {"POST", base, strings.TrimSuffix(string(begin), "}") + `,"scope":{}}`}, {"POST", base, strings.Replace(string(begin), `"idempotency_key":"begin"`, `"idempotency_key":"a","idempotency_key":"b"`, 1)}, {"POST", applyURL, strings.TrimSuffix(string(apply), "}") + `,"final_content":"replacement"}`}, {"POST", applyURL, strings.Replace(string(apply), `"confirm_exact_restore":true`, `"confirm_exact_restore":false`, 1)}, {"POST", applyURL, strings.Replace(string(apply), `,"retire_current_candidate":false`, "", 1)}, {"POST", applyURL, strings.Replace(string(apply), `"retire_current_candidate":false`, `"retire_current_candidate":null`, 1)}, {"POST", applyURL, strings.Replace(string(apply), string(testID(3)), string(testID(9)), 1)}} {
		before := s.calls
		w := request(r, tc.method, tc.path, tc.body)
		if w.Code != 400 || s.calls != before {
			t.Fatalf("invalid %s %s: %d %s", tc.path, tc.body, w.Code, w.Body.String())
		}
	}
	for _, headers := range [][]string{{"different"}, {"begin", "begin"}} {
		before := s.calls
		w := request(r, "POST", base, string(begin), headers...)
		if w.Code != 400 || s.calls != before {
			t.Fatal("header mismatch accepted")
		}
	}
	fullTarget := target
	fullTarget.SelectedPublicationID = testID(20)
	fullTarget.SelectedProposalCommitID = testID(21)
	fullTarget.ExpectedPublishedRevisionID = testID(22)
	fullTarget.ExpectedPublicationID = testID(23)
	fullTarget.ExpectedProposalID = testID(24)
	fullTarget.ExpectedProposalRevisionID = testID(25)
	fullTarget.ExpectedProposalVersion = 4
	fullBegin, _ := json.Marshal(app.BeginSynthesisHistoricalRepublish{SynthesisHistoricalRepublishTarget: fullTarget, IdempotencyKey: "full-original"})
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(fullBegin, &fields); err != nil || len(fields) != 17 {
		t.Fatalf("full Target fixture has %d fields: %v", len(fields), err)
	}
	s.out.Target = fullTarget
	if w := request(r, "POST", base, string(fullBegin), "full-original"); w.Code != 200 {
		t.Fatalf("complete seventeen-field Begin %d %s", w.Code, w.Body.String())
	}
	if s.lastBegin.SynthesisHistoricalRepublishTarget != fullTarget || s.lastBegin.IdempotencyKey != "full-original" {
		t.Fatal("complete Target changed before reaching owner")
	}
	defaultRequest := httptest.NewRequest("POST", base, strings.NewReader(string(fullBegin)))
	defaultRequest.Header.Set("Content-Type", "application/json")
	if _, err := decodeJSON[map[string]json.RawMessage](defaultRequest, 16<<10, 1024, 16); err == nil {
		t.Fatal("default decoder field budget was widened")
	}
	for _, bad := range []string{strings.TrimSuffix(string(fullBegin), "}") + `,"scope":{}}`, strings.Replace(string(fullBegin), `"idempotency_key":"full-original"`, `"idempotency_key":"full-original","idempotency_key":"other"`, 1)} {
		before := s.calls
		if w := request(r, "POST", base, bad); w.Code != 400 || s.calls != before {
			t.Fatalf("invalid full Begin accepted %d", w.Code)
		}
	}
	s.out.Target = target
	s.out.State = "APPLIED"
	s.out.Result = &app.SynthesisCandidateRemergeResult{RevisionID: testID(7), ArticleRevisionID: testID(8)}
	for _, tc := range []struct{ path, body string }{{applyURL, string(apply)}, {resumeURL, "{}"}} {
		w := request(r, "POST", tc.path, tc.body)
		if w.Code != 200 || !strings.Contains(w.Body.String(), `"candidate":"旧正文\n"`) {
			t.Fatalf("applied retained snapshot %d %s", w.Code, w.Body.String())
		}
	}
	if !s.lastApply.ConfirmExactRestore || s.lastApply.RetireCurrentCandidate {
		t.Fatal("confirmation changed")
	}
	s.out.CurrentScope = json.RawMessage(`{"id":"` + string(testID(10)) + `","scope_version":2,"scope":{"topics":["数据库"],"audiences":["复习"],"description":"历史范围"}}`)
	if w := request(r, "GET", base+"?key=begin", ""); w.Code != 200 || !strings.Contains(w.Body.String(), `"topics":["数据库"]`) {
		t.Fatalf("scope projection %d %s", w.Code, w.Body.String())
	}
	s.out.CurrentScope = json.RawMessage(`{"id":"` + string(testID(10)) + `","scope_version":2,"authority":"secret","scope":{"topics":["数据库"],"audiences":["复习"],"description":"历史范围"}}`)
	if w := request(r, "GET", base+"?key=begin", ""); w.Code != 409 || strings.Contains(w.Body.String(), "secret") {
		t.Fatalf("invalid scope leaked %d", w.Code)
	}
	s.out.CurrentScope = json.RawMessage("null")
	s.out.Target.SelectedRevisionID = testID(9)
	if w := request(r, "GET", targetURL, ""); w.Code != 409 {
		t.Fatalf("wrong selected accepted %d", w.Code)
	}
}
