package organizinghttp

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
	"unicode/utf8"

	authhttp "github.com/CodeZen-Lizhi/zhixu/internal/auth/http"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation/strictjson"
	app "github.com/CodeZen-Lizhi/zhixu/internal/organizing/application"
)

type SynthesisCandidateRemergeFactory interface {
	CandidateRemerge(app.SynthesisManuscriptCaller) (app.SynthesisCandidateRemergeOwner, error)
}

func (h *SynthesisHandler) WithCandidateRemerge(factory SynthesisCandidateRemergeFactory) *SynthesisHandler {
	out := *h
	out.candidateRemerge = factory
	return &out
}
func (h *SynthesisHandler) remergeOwner(w http.ResponseWriter, r *http.Request) (app.SynthesisCandidateRemergeOwner, bool) {
	principal, ok := authhttp.PrincipalFromContext(r.Context())
	if !ok {
		writeError(w, foundation.NewError(foundation.ErrorPermissionDenied, "SYNTHESIS_MANUSCRIPT_HUMAN_FORBIDDEN", false, errors.New("authenticated principal is required")))
		return nil, false
	}
	if nilDependency(h.candidateRemerge) {
		writeProblem(w, 503, "SYNTHESIS_MANUSCRIPT_HTTP_UNAVAILABLE", "候选重新合并暂不可用", true)
		return nil, false
	}
	owner, err := h.candidateRemerge.CandidateRemerge(app.NewSynthesisManuscriptCaller(principal.Scopes))
	if err != nil {
		writeError(w, err)
		return nil, false
	}
	return owner, true
}
func validRemergeKey(key string) bool {
	return key != "" && len(key) <= 200 && key == strings.TrimSpace(key) && utf8.ValidString(key) && !strings.ContainsRune(key, 0)
}
func remergeHeaderMatches(r *http.Request, key string) bool {
	values := r.Header.Values("Idempotency-Key")
	return len(values) == 0 || len(values) == 1 && values[0] == key
}
func remergeReadBody(w http.ResponseWriter, r *http.Request) bool {
	if r.Body != nil {
		b, err := io.ReadAll(io.LimitReader(r.Body, 1))
		if err != nil || len(b) != 0 {
			writeError(w, requestInvalid("read does not accept a body"))
			return false
		}
	}
	return true
}
func (h *SynthesisHandler) beginCandidateRemerge(w http.ResponseWriter, r *http.Request) {
	workspace, note, ok := candidateRemergeRoute(w, r)
	if !ok {
		return
	}
	owner, ok := h.remergeOwner(w, r)
	if !ok {
		return
	}
	if r.URL.RawQuery != "" {
		writeError(w, requestInvalid("begin does not accept query parameters"))
		return
	}
	c, err := decodeJSON[app.BeginSynthesisCandidateRemerge](r, 16<<10, 1024, 16)
	if err != nil {
		writeError(w, err)
		return
	}
	if !validRemergeExpected(c) || c.WorkspaceID != workspace || c.NoteID != note || !validRemergeKey(c.IdempotencyKey) || !remergeHeaderMatches(r, c.IdempotencyKey) {
		writeError(w, requestInvalid("begin identity differs from route or key"))
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), h.timeout)
	defer cancel()
	result, err := owner.Begin(ctx, c)
	if err != nil {
		writeError(w, err)
		return
	}
	h.writeRemerge(w, result, workspace, note, "")
}
func (h *SynthesisHandler) readCandidateRemerge(w http.ResponseWriter, r *http.Request) {
	workspace, note, ok := candidateRemergeRoute(w, r)
	if !ok {
		return
	}
	owner, ok := h.remergeOwner(w, r)
	if !ok {
		return
	}
	if !remergeReadBody(w, r) {
		return
	}
	q, err := url.ParseQuery(r.URL.RawQuery)
	if err != nil || len(q) != 1 || len(q["key"]) != 1 || !validRemergeKey(q.Get("key")) || !remergeHeaderMatches(r, q.Get("key")) {
		writeError(w, requestInvalid("one begin key is required"))
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), h.timeout)
	defer cancel()
	result, err := owner.Read(ctx, app.ReadSynthesisCandidateRemerge{WorkspaceID: workspace, NoteID: note, IdempotencyKey: q.Get("key")})
	if err != nil {
		writeError(w, err)
		return
	}
	h.writeRemerge(w, result, workspace, note, "")
}
func (h *SynthesisHandler) applyCandidateRemerge(w http.ResponseWriter, r *http.Request) {
	workspace, note, ok := candidateRemergeRoute(w, r)
	if !ok {
		return
	}
	owner, ok := h.remergeOwner(w, r)
	if !ok {
		return
	}
	attempt, err := parseID(r.PathValue("attempt_id"))
	if err != nil {
		writeError(w, err)
		return
	}
	if r.URL.RawQuery != "" {
		writeError(w, requestInvalid("apply does not accept query parameters"))
		return
	}
	wire, err := decodeJSON[struct {
		app.ApplySynthesisCandidateRemerge
		Resolution json.RawMessage `json:"resolution"`
	}](r, 8<<20, 1<<20, 1024)
	if err != nil {
		writeError(w, err)
		return
	}
	c := wire.ApplySynthesisCandidateRemerge
	if len(wire.Resolution) > 0 {
		limits := strictjson.DefaultLimits()
		limits.MaxDocumentBytes = 8 << 20
		limits.MaxStringBytes = 1 << 20
		limits.MaxArrayItems = 1024
		var fields map[string]json.RawMessage
		if json.Unmarshal(wire.Resolution, &fields) != nil || len(fields) != 4 || len(fields["stage"]) == 0 || len(fields["preview_fingerprint"]) == 0 || len(fields["acknowledged_ordinals"]) == 0 || len(fields["final_content"]) == 0 || string(fields["final_content"]) == "null" {
			writeError(w, requestInvalid("complete resolution is required"))
			return
		}
		resolution, decodeErr := strictjson.DecodeObject[app.SynthesisManuscriptResolution](wire.Resolution, limits, nil)
		if decodeErr != nil || resolution.Stage != app.SynthesisMergeWorkspaceStage || !synthesisValidHash(resolution.PreviewFingerprint) || len(resolution.AcknowledgedOrdinals) == 0 {
			writeError(w, requestInvalid("invalid resolution"))
			return
		}
		for i, ordinal := range resolution.AcknowledgedOrdinals {
			if ordinal != i+1 {
				writeError(w, requestInvalid("ordered resolution ordinals are required"))
				return
			}
		}
		c.Resolution = &resolution
	}
	if c.WorkspaceID != workspace || c.NoteID != note || c.AttemptID != attempt || !validRemergeKey(c.IdempotencyKey) || !remergeHeaderMatches(r, c.IdempotencyKey) {
		writeError(w, requestInvalid("apply identity differs from route or key"))
		return
	}
	if c.Resolution != nil && !remergeText(c.Resolution.FinalContent) {
		writeError(w, requestInvalid("invalid manuscript text"))
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), h.timeout)
	defer cancel()
	result, err := owner.Apply(ctx, c)
	if err != nil {
		writeError(w, err)
		return
	}
	h.writeRemerge(w, result, workspace, note, attempt)
}
func remergeText(s string) bool {
	return utf8.ValidString(s) && !strings.ContainsRune(s, 0) && len(s) <= 1<<20
}

type candidateRemergeResponse struct {
	Candidate          *string                              `json:"candidate,omitempty"`
	WorkspaceID        foundation.ID                        `json:"workspace_id"`
	NoteID             foundation.ID                        `json:"note_id"`
	AttemptID          foundation.ID                        `json:"attempt_id"`
	State              string                               `json:"state"`
	PreviewFingerprint string                               `json:"preview_fingerprint"`
	Review             *manuscriptReviewResponse            `json:"review,omitempty"`
	Result             *app.SynthesisCandidateRemergeResult `json:"result,omitempty"`
	Replayed           bool                                 `json:"replayed"`
}

func (h *SynthesisHandler) writeRemerge(w http.ResponseWriter, r app.SynthesisCandidateRemergeReview, workspace, note, attempt foundation.ID) {
	if r.WorkspaceID != workspace || r.NoteID != note || !validResponseID(r.AttemptID) || attempt != "" && r.AttemptID != attempt || !synthesisValidHash(r.PreviewFingerprint) {
		writeError(w, synthesisInvalidResult())
		return
	}
	out := candidateRemergeResponse{WorkspaceID: r.WorkspaceID, NoteID: r.NoteID, AttemptID: r.AttemptID, State: r.State, PreviewFingerprint: r.PreviewFingerprint, Result: r.Result, Replayed: r.Replayed}
	switch r.State {
	case "READY":
		if r.Review != nil || r.Result != nil || !remergeText(r.Candidate) {
			writeError(w, synthesisInvalidResult())
			return
		}
		out.Candidate = &r.Candidate
	case "CONFLICTS":
		v := r.Review
		if v == nil || r.Result != nil || r.Candidate != "" || v.Stage != app.SynthesisMergeWorkspaceStage || len(v.Conflicts) < 1 || len(v.Conflicts) > 1024 || !remergeText(v.Base) || !remergeText(v.Current) || !remergeText(v.Proposed) || !remergeText(v.Candidate) {
			writeError(w, synthesisInvalidResult())
			return
		}
		out.Review = &manuscriptReviewResponse{Stage: v.Stage, Base: v.Base, Current: v.Current, Proposed: v.Proposed, Candidate: v.Candidate, Conflicts: make([]manuscriptConflictResponse, 0, len(v.Conflicts))}
		for i, c := range v.Conflicts {
			if c.Ordinal != i+1 || !remergeText(string(c.Base)) || !remergeText(string(c.Current)) || !remergeText(string(c.Proposed)) {
				writeError(w, synthesisInvalidResult())
				return
			}
			out.Review.Conflicts = append(out.Review.Conflicts, manuscriptConflictResponse{Ordinal: c.Ordinal, Base: string(c.Base), Current: string(c.Current), Proposed: string(c.Proposed)})
		}
	case "APPLIED":
		v := r.Result
		if r.Review != nil || r.Candidate != "" || v == nil || !validResponseID(v.RevisionID) || !validResponseID(v.ArticleRevisionID) || v.PublicationID != "" && !validResponseID(v.PublicationID) || (v.ProposalID == "") != (v.ProposalRevisionID == "") || v.ProposalID != "" && (v.PublicationID == "" || !validResponseID(v.ProposalID) || !validResponseID(v.ProposalRevisionID)) {
			writeError(w, synthesisInvalidResult())
			return
		}
	default:
		writeError(w, synthesisInvalidResult())
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func candidateRemergeRoute(w http.ResponseWriter, r *http.Request) (foundation.ID, foundation.ID, bool) {
	workspace, err := parseID(r.PathValue("workspace_id"))
	if err != nil {
		writeError(w, err)
		return "", "", false
	}
	note, err := parseID(r.PathValue("note_id"))
	if err != nil {
		writeError(w, err)
		return "", "", false
	}
	return workspace, note, true
}

func validRemergeExpected(c app.BeginSynthesisCandidateRemerge) bool {
	for _, id := range []foundation.ID{c.WorkspaceID, c.NoteID, c.ExpectedRevisionID, c.ExpectedDocumentID, c.ExpectedPublicationID, c.ExpectedProposalID, c.ExpectedProposalRevisionID} {
		if !validResponseID(id) {
			return false
		}
	}
	return c.ExpectedNoteVersion > 0 && c.ExpectedDocumentVersion > 0 && c.ExpectedProposalVersion > 0
}

func (h *SynthesisHandler) candidateRemergeTarget(w http.ResponseWriter, r *http.Request) {
	workspace, note, ok := candidateRemergeRoute(w, r)
	if !ok {
		return
	}
	owner, ok := h.remergeOwner(w, r)
	if !ok {
		return
	}
	if r.URL.RawQuery != "" {
		writeError(w, requestInvalid("target does not accept query parameters"))
		return
	}
	if !remergeReadBody(w, r) {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), h.timeout)
	defer cancel()
	target, err := owner.Target(ctx, workspace, note)
	if err != nil {
		writeError(w, err)
		return
	}
	if target.WorkspaceID != workspace || target.NoteID != note || target.ExpectedNoteVersion < 1 || target.ExpectedDocumentVersion < 1 || target.ExpectedProposalVersion < 1 {
		writeError(w, synthesisInvalidResult())
		return
	}
	for _, id := range []foundation.ID{target.ExpectedRevisionID, target.ExpectedDocumentID, target.ExpectedPublicationID, target.ExpectedProposalID, target.ExpectedProposalRevisionID} {
		if !validResponseID(id) {
			writeError(w, synthesisInvalidResult())
			return
		}
	}
	writeJSON(w, http.StatusOK, target)
}

func (h *SynthesisHandler) resumeCandidateRemerge(w http.ResponseWriter, r *http.Request) {
	workspace, note, ok := candidateRemergeRoute(w, r)
	if !ok {
		return
	}
	owner, ok := h.remergeOwner(w, r)
	if !ok {
		return
	}
	attempt, err := parseID(r.PathValue("attempt_id"))
	if err != nil {
		writeError(w, err)
		return
	}
	if r.URL.RawQuery != "" {
		writeError(w, requestInvalid("resume does not accept query parameters"))
		return
	}
	if _, err := decodeJSON[struct{}](r, 1024, 256, 1); err != nil {
		writeError(w, err)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), h.timeout)
	defer cancel()
	result, err := owner.Resume(ctx, app.ResumeSynthesisCandidateRemerge{WorkspaceID: workspace, NoteID: note, AttemptID: attempt})
	if err != nil {
		writeError(w, err)
		return
	}
	h.writeRemerge(w, result, workspace, note, attempt)
}
