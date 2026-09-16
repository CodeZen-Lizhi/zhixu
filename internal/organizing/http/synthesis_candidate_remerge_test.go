package organizinghttp

import (
	"context"
	"encoding/json"
	authdomain "github.com/CodeZen-Lizhi/zhixu/internal/auth/domain"
	authhttp "github.com/CodeZen-Lizhi/zhixu/internal/auth/http"
	"github.com/CodeZen-Lizhi/zhixu/internal/capability"
	changeapp "github.com/CodeZen-Lizhi/zhixu/internal/changecontrol/application"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	app "github.com/CodeZen-Lizhi/zhixu/internal/organizing/application"
	"github.com/gin-gonic/gin"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

type remergeAuth struct {
	authhttp.Service
	scopes []capability.Capability
}

func (a remergeAuth) AuthenticateAPIToken(context.Context, string) (authdomain.Principal, error) {
	return authdomain.Principal{Kind: authdomain.PrincipalAPIToken, ID: testID(90), Scopes: a.scopes}, nil
}

type remergeStub struct {
	app.SynthesisCandidateRemergeOwner
	calls int
	got   app.SynthesisManuscriptCaller
	out   app.SynthesisCandidateRemergeReview
}

func (s *remergeStub) CandidateRemerge(c app.SynthesisManuscriptCaller) (app.SynthesisCandidateRemergeOwner, error) {
	s.got = c
	if err := c.Authorize(); err != nil {
		return nil, err
	}
	return s, nil
}
func (s *remergeStub) Target(_ context.Context, workspace, note foundation.ID) (app.SynthesisCandidateRemergeTarget, error) {
	s.calls++
	return app.SynthesisCandidateRemergeTarget{WorkspaceID: workspace, NoteID: note, ExpectedRevisionID: testID(4), ExpectedNoteVersion: 1, ExpectedDocumentID: testID(5), ExpectedDocumentVersion: 2, ExpectedPublicationID: testID(6), ExpectedProposalID: testID(7), ExpectedProposalRevisionID: testID(8), ExpectedProposalVersion: 3}, nil
}
func (s *remergeStub) Begin(_ context.Context, c app.BeginSynthesisCandidateRemerge) (app.SynthesisCandidateRemergeReview, error) {
	s.calls++
	return s.out, nil
}
func (s *remergeStub) Read(_ context.Context, c app.ReadSynthesisCandidateRemerge) (app.SynthesisCandidateRemergeReview, error) {
	s.calls++
	return s.out, nil
}
func (s *remergeStub) Apply(_ context.Context, c app.ApplySynthesisCandidateRemerge) (app.SynthesisCandidateRemergeReview, error) {
	s.calls++
	return s.out, nil
}
func remergeHTTP(t *testing.T, s *remergeStub, scopes []capability.Capability, auth bool) http.Handler {
	t.Helper()
	r := gin.New()
	g := r.Group("/api/v1")
	if auth {
		h, err := authhttp.NewHandler(remergeAuth{scopes: scopes}, authhttp.Options{AllowedOrigins: []string{"http://127.0.0.1:8080"}})
		if err != nil {
			t.Fatal(err)
		}
		g.Use(h.Middleware)
	}
	NewSynthesisHandler(synthesisNotesStub{}, synthesisProcessingStub{}, time.Second).WithCandidateRemerge(s).Routes(g)
	return r
}
func TestCandidateRemergeHTTPPermissionsAndStrictWire(t *testing.T) {
	base := "/api/v1/workspaces/" + string(testID(1)) + "/synthesis/notes/" + string(testID(2)) + "/candidate-remerge"
	out := app.SynthesisCandidateRemergeReview{WorkspaceID: testID(1), NoteID: testID(2), AttemptID: testID(3), State: "CONFLICTS", PreviewFingerprint: strings.Repeat("a", 64), Review: &app.SynthesisManuscriptMergeReview{Stage: app.SynthesisMergeWorkspaceStage, Base: "基线", Current: "当前", Proposed: "完整人工候选", Candidate: "冲突全文", Conflicts: []changeapp.RevisionMergeConflict{{Ordinal: 1, Base: []byte("基线"), Current: []byte("当前"), Proposed: []byte("候选")}}}}
	request := func(router http.Handler, method, path, body string, headers ...string) *httptest.ResponseRecorder {
		q := httptest.NewRequest(method, path, strings.NewReader(body))
		q.Header.Set("Authorization", "Bearer test")
		q.Header.Set("Content-Type", "application/json")
		for _, v := range headers {
			q.Header.Add("Idempotency-Key", v)
		}
		w := httptest.NewRecorder()
		router.ServeHTTP(w, q)
		return w
	}
	for _, scopes := range [][]capability.Capability{{capability.ReadLocal}, {capability.WriteProposal}, {capability.ReadLocal, capability.WriteProposal}} {
		s := &remergeStub{out: out}
		r := remergeHTTP(t, s, scopes, true)
		w := request(r, "GET", base+"?key=begin", "")
		want := 403
		if len(scopes) == 2 {
			want = 200
		}
		if w.Code != want {
			t.Fatalf("scopes %v: %d %s", scopes, w.Code, w.Body.String())
		}
		if want == 403 && s.calls != 0 {
			t.Fatal("unauthorized owner call")
		}
	}
	s := &remergeStub{out: out}
	w := request(remergeHTTP(t, s, nil, false), "GET", base+"?key=begin", "")
	if w.Code != 403 || s.calls != 0 {
		t.Fatalf("missing principal: %d", w.Code)
	}
	r := remergeHTTP(t, s, []capability.Capability{capability.ReadLocal, capability.WriteProposal}, true)
	w = request(r, "GET", base+"/target", "")
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"expected_document_version":2`) || strings.Contains(w.Body.String(), `"idempotency_key"`) {
		t.Fatalf("target %d %s", w.Code, w.Body.String())
	}
	for _, suffix := range []string{"?key=a", "?unknown=1"} {
		if w = request(r, "GET", base+"/target"+suffix, ""); w.Code != 400 {
			t.Fatal("target query accepted")
		}
	}
	if w = request(r, "GET", base+"/target", "{}"); w.Code != 400 {
		t.Fatal("target body accepted")
	}
	w = request(r, "GET", base+"?key=begin", "")
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"ordinal":1`) || !strings.Contains(w.Body.String(), `"base":"基线"`) || strings.Contains(w.Body.String(), `"Base"`) {
		t.Fatalf("wire %d %s", w.Code, w.Body.String())
	}
	for _, q := range []string{"", "?key=", "?key=a&key=b", "?key=a&other=x", "?key=%00", "?key=a;b"} {
		before := s.calls
		w = request(r, "GET", base+q, "")
		if w.Code != 400 || s.calls != before {
			t.Fatalf("query %s: %d", q, w.Code)
		}
	}
	if w = request(r, "GET", base+"?key=a", "{}"); w.Code != 400 {
		t.Fatal("GET body accepted")
	}
	begin := app.BeginSynthesisCandidateRemerge{WorkspaceID: testID(1), NoteID: testID(2), ExpectedRevisionID: testID(4), ExpectedNoteVersion: 1, ExpectedDocumentID: testID(5), ExpectedDocumentVersion: 2, ExpectedPublicationID: testID(6), ExpectedProposalID: testID(7), ExpectedProposalRevisionID: testID(8), ExpectedProposalVersion: 3, IdempotencyKey: "original"}
	b, _ := json.Marshal(begin)
	if w = request(r, "POST", base, string(b), "original"); w.Code != 200 {
		t.Fatalf("begin %d %s", w.Code, w.Body.String())
	}
	for _, body := range []string{strings.Replace(string(b), `"note_id":"`+string(testID(2))+`"`, `"note_id":"`+string(testID(99))+`"`, 1), strings.Replace(string(b), `"idempotency_key":"original"`, `"idempotency_key":"a","idempotency_key":"b"`, 1), strings.TrimSuffix(string(b), "}") + `,"capture_id":"` + string(testID(9)) + `"}`, strings.Replace(string(b), "original", `\ud800`, 1)} {
		before := s.calls
		w = request(r, "POST", base, body)
		if w.Code != 400 || s.calls != before {
			t.Fatalf("bad begin %d %s", w.Code, w.Body.String())
		}
	}
	for _, headers := range [][]string{{"other"}, {"original", "original"}, {""}} {
		before := s.calls
		w = request(r, "POST", base, string(b), headers...)
		if w.Code != 400 || s.calls != before {
			t.Fatal("header mismatch accepted")
		}
	}
	apply := `{"workspace_id":"` + string(testID(1)) + `","note_id":"` + string(testID(2)) + `","attempt_id":"` + string(testID(3)) + `","idempotency_key":"apply","resolution":{"stage":"WORKSPACE_MANUAL_CONTENT","preview_fingerprint":"` + strings.Repeat("a", 64) + `","acknowledged_ordinals":[1],"final_content":"text"}}`
	for _, body := range []string{strings.Replace(apply, `,"final_content":"text"`, "", 1), strings.Replace(apply, `"final_content":"text"`, `"final_content":null`, 1), strings.Replace(apply, `"acknowledged_ordinals":[1]`, `"acknowledged_ordinals":[2]`, 1), strings.Replace(apply, "text", `\u0000`, 1), strings.Replace(apply, "text", `\ud800`, 1), strings.Replace(apply, "text", strings.Repeat("x", (1<<20)+1), 1), strings.Replace(apply, `"attempt_id":"`+string(testID(3))+`"`, `"attempt_id":"`+string(testID(99))+`"`, 1)} {
		before := s.calls
		w = request(r, "POST", base+"/"+string(testID(3))+"/apply", body)
		if w.Code != 400 || s.calls != before {
			t.Fatalf("bad apply %d", w.Code)
		}
	}

	original := s.out
	s.out.Review = nil
	s.out.State = "READY"
	s.out.Candidate = "完整新合并正文"
	if w = request(r, "GET", base+"?key=begin", ""); w.Code != 200 || !strings.Contains(w.Body.String(), `"candidate":"完整新合并正文"`) {
		t.Fatalf("clean preview %d %s", w.Code, w.Body.String())
	}
	s.out.Candidate = ""
	if w = request(r, "GET", base+"?key=begin", ""); w.Code != 200 || !strings.Contains(w.Body.String(), `"candidate":""`) {
		t.Fatal("empty clean preview omitted")
	}
	s.out = app.SynthesisCandidateRemergeReview{WorkspaceID: testID(1), NoteID: testID(2), AttemptID: testID(3), State: "APPLIED", PreviewFingerprint: strings.Repeat("a", 64), Result: &app.SynthesisCandidateRemergeResult{RevisionID: testID(9), ArticleRevisionID: testID(10)}}
	if w = request(r, "POST", base+"/"+string(testID(3))+"/apply", apply); w.Code != 200 || !strings.Contains(w.Body.String(), `"state":"APPLIED"`) || strings.Contains(w.Body.String(), `"proposal_id"`) {
		t.Fatalf("partial applied %d %s", w.Code, w.Body.String())
	}
	s.out = original
	s.out.Review.Conflicts[0].Base = []byte{0xff}
	if w = request(r, "GET", base+"?key=begin", ""); w.Code != 409 {
		t.Fatalf("invalid UTF8 response: %d %s", w.Code, w.Body.String())
	}
	s.out = out
	s.out.NoteID = testID(99)
	if w = request(r, "GET", base+"?key=begin", ""); w.Code != 409 {
		t.Fatal("cross scope response accepted")
	}
}

func (s *remergeStub) Resume(_ context.Context, c app.ResumeSynthesisCandidateRemerge) (app.SynthesisCandidateRemergeReview, error) {
	s.calls++
	if c.WorkspaceID != testID(1) || c.NoteID != testID(2) || c.AttemptID != testID(3) {
		return app.SynthesisCandidateRemergeReview{}, requestInvalid("wrong route identity")
	}
	return s.out, nil
}
func TestCandidateRemergeResumeBoundary(t *testing.T) {
	path := "/api/v1/workspaces/" + string(testID(1)) + "/synthesis/notes/" + string(testID(2)) + "/candidate-remerge/" + string(testID(3)) + "/resume"
	out := app.SynthesisCandidateRemergeReview{WorkspaceID: testID(1), NoteID: testID(2), AttemptID: testID(3), State: "APPLIED", PreviewFingerprint: strings.Repeat("a", 64), Result: &app.SynthesisCandidateRemergeResult{RevisionID: testID(9), ArticleRevisionID: testID(10)}}
	for _, tc := range []struct {
		body, suffix string
		scopes       []capability.Capability
		auth         bool
		want         int
	}{
		{"{}", "", []capability.Capability{capability.ReadLocal, capability.WriteProposal}, true, 200},
		{"{}", "", []capability.Capability{capability.ReadLocal}, true, 403},
		{"{}", "", []capability.Capability{capability.WriteProposal}, true, 403},
		{"{}", "", nil, false, 403},
		{"", "", []capability.Capability{capability.ReadLocal, capability.WriteProposal}, true, 400},
		{"null", "", []capability.Capability{capability.ReadLocal, capability.WriteProposal}, true, 400},
		{`{"attempt_id":"forged"}`, "", []capability.Capability{capability.ReadLocal, capability.WriteProposal}, true, 400},
		{"{} {}", "", []capability.Capability{capability.ReadLocal, capability.WriteProposal}, true, 400},
		{"{}", "?key=extra", []capability.Capability{capability.ReadLocal, capability.WriteProposal}, true, 400},
	} {
		s := &remergeStub{out: out}
		router := remergeHTTP(t, s, tc.scopes, tc.auth)
		q := httptest.NewRequest("POST", path+tc.suffix, strings.NewReader(tc.body))
		q.Header.Set("Authorization", "Bearer test")
		q.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		router.ServeHTTP(w, q)
		if w.Code != tc.want || tc.want != 200 && s.calls != 0 {
			t.Fatalf("body %q auth %v: %d %s calls %d", tc.body, tc.scopes, w.Code, w.Body.String(), s.calls)
		}
	}
	s := &remergeStub{out: out}
	s.out.AttemptID = testID(99)
	q := httptest.NewRequest("POST", path, strings.NewReader("{}"))
	q.Header.Set("Authorization", "Bearer test")
	q.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	remergeHTTP(t, s, []capability.Capability{capability.ReadLocal, capability.WriteProposal}, true).ServeHTTP(w, q)
	if w.Code != 409 {
		t.Fatalf("mismatched response accepted: %d", w.Code)
	}
}
