package organizinghttp

import (
	"bytes"
	"context"
	"errors"
	authhttp "github.com/CodeZen-Lizhi/zhixu/internal/auth/http"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation/strictjson"
	app "github.com/CodeZen-Lizhi/zhixu/internal/organizing/application"
	"net/http"
	"net/url"
)

type SynthesisHistoricalRepublishFactory interface {
	HistoricalRepublish(app.SynthesisManuscriptCaller) (app.SynthesisHistoricalRepublishOwner, error)
}

func (h *SynthesisHandler) WithHistoricalRepublish(f SynthesisHistoricalRepublishFactory) *SynthesisHandler {
	out := *h
	out.historicalRepublish = f
	return &out
}
func (h *SynthesisHandler) historicalOwner(w http.ResponseWriter, r *http.Request) (app.SynthesisHistoricalRepublishOwner, bool) {
	p, ok := authhttp.PrincipalFromContext(r.Context())
	if !ok {
		writeError(w, foundation.NewError(foundation.ErrorPermissionDenied, "SYNTHESIS_MANUSCRIPT_HUMAN_FORBIDDEN", false, errors.New("authenticated principal is required")))
		return nil, false
	}
	if nilDependency(h.historicalRepublish) {
		writeProblem(w, 503, "SYNTHESIS_MANUSCRIPT_HTTP_UNAVAILABLE", "历史版本重新审阅暂不可用", true)
		return nil, false
	}
	owner, err := h.historicalRepublish.HistoricalRepublish(app.NewSynthesisManuscriptCaller(p.Scopes))
	if err != nil {
		writeError(w, err)
		return nil, false
	}
	return owner, true
}

// 此显式投影不会序列化所属模块的授权、根目录或内部证明。
type historicalTargetWire struct {
	WorkspaceID                 foundation.ID `json:"workspace_id"`
	NoteID                      foundation.ID `json:"note_id"`
	SelectedRevisionID          foundation.ID `json:"selected_revision_id"`
	SelectedProjectionHash      string        `json:"selected_projection_hash"`
	SelectedPublicationID       foundation.ID `json:"selected_publication_id,omitempty"`
	SelectedProposalCommitID    foundation.ID `json:"selected_proposal_commit_id,omitempty"`
	ExpectedRevisionID          foundation.ID `json:"expected_revision_id"`
	ExpectedNoteVersion         int64         `json:"expected_note_version"`
	ExpectedDocumentID          foundation.ID `json:"expected_document_id"`
	ExpectedDocumentVersion     int64         `json:"expected_document_version"`
	ExpectedPublishedRevisionID foundation.ID `json:"expected_published_revision_id,omitempty"`
	ExpectedPublicationID       foundation.ID `json:"expected_publication_id,omitempty"`
	ExpectedProposalID          foundation.ID `json:"expected_proposal_id,omitempty"`
	ExpectedProposalRevisionID  foundation.ID `json:"expected_proposal_revision_id,omitempty"`
	ExpectedProposalVersion     int64         `json:"expected_proposal_version,omitempty"`
	RequiresRetirement          bool          `json:"requires_retirement"`
}

func historicalTarget(t app.SynthesisHistoricalRepublishTarget) historicalTargetWire {
	return historicalTargetWire{t.WorkspaceID, t.NoteID, t.SelectedRevisionID, t.SelectedProjectionHash, t.SelectedPublicationID, t.SelectedProposalCommitID, t.ExpectedRevisionID, t.ExpectedNoteVersion, t.ExpectedDocumentID, t.ExpectedDocumentVersion, t.ExpectedPublishedRevisionID, t.ExpectedPublicationID, t.ExpectedProposalID, t.ExpectedProposalRevisionID, t.ExpectedProposalVersion, t.RequiresRetirement}
}
func validHistoricalTarget(t app.SynthesisHistoricalRepublishTarget, w, n foundation.ID) bool {
	if t.WorkspaceID != w || t.NoteID != n || !synthesisValidHash(t.SelectedProjectionHash) || t.ExpectedNoteVersion < 1 || t.ExpectedDocumentVersion < 1 {
		return false
	}
	for _, id := range []foundation.ID{t.WorkspaceID, t.NoteID, t.SelectedRevisionID, t.ExpectedRevisionID, t.ExpectedDocumentID} {
		if !validResponseID(id) {
			return false
		}
	}
	for _, id := range []foundation.ID{t.SelectedPublicationID, t.SelectedProposalCommitID, t.ExpectedPublishedRevisionID, t.ExpectedPublicationID, t.ExpectedProposalID, t.ExpectedProposalRevisionID} {
		if id != "" && !validResponseID(id) {
			return false
		}
	}
	return (t.SelectedPublicationID == "") == (t.SelectedProposalCommitID == "") && (t.ExpectedProposalID == "") == (t.ExpectedProposalRevisionID == "") && (t.ExpectedProposalID == "" && t.ExpectedProposalVersion == 0 || t.ExpectedProposalID != "" && t.ExpectedProposalVersion > 0)
}
func (h *SynthesisHandler) historicalRepublishTarget(w http.ResponseWriter, r *http.Request) {
	workspace, note, ok := candidateRemergeRoute(w, r)
	if !ok {
		return
	}
	owner, ok := h.historicalOwner(w, r)
	if !ok {
		return
	}
	q, err := url.ParseQuery(r.URL.RawQuery)
	if err != nil || len(q) != 1 || len(q["selected_revision_id"]) != 1 {
		writeError(w, requestInvalid("one selected revision is required"))
		return
	}
	selected, err := parseID(q.Get("selected_revision_id"))
	if err != nil {
		writeError(w, err)
		return
	}
	if !remergeReadBody(w, r) {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), h.timeout)
	defer cancel()
	t, err := owner.Target(ctx, workspace, note, selected)
	if err != nil {
		writeError(w, err)
		return
	}
	if !validHistoricalTarget(t, workspace, note) || t.SelectedRevisionID != selected {
		writeError(w, synthesisInvalidResult())
		return
	}
	writeJSON(w, 200, historicalTarget(t))
}
func (h *SynthesisHandler) beginHistoricalRepublish(w http.ResponseWriter, r *http.Request) {
	workspace, note, ok := candidateRemergeRoute(w, r)
	if !ok {
		return
	}
	owner, ok := h.historicalOwner(w, r)
	if !ok {
		return
	}
	if r.URL.RawQuery != "" {
		writeError(w, requestInvalid("begin does not accept query parameters"))
		return
	}
	wire, err := decodeHistoricalBegin(r)
	if err != nil {
		writeError(w, err)
		return
	}
	if wire.RequiresRetirement == nil {
		writeError(w, requestInvalid("requires_retirement is required"))
		return
	}
	t := wire.historicalTargetWire
	c := app.BeginSynthesisHistoricalRepublish{SynthesisHistoricalRepublishTarget: app.SynthesisHistoricalRepublishTarget{WorkspaceID: t.WorkspaceID, NoteID: t.NoteID, SelectedRevisionID: t.SelectedRevisionID, SelectedProjectionHash: t.SelectedProjectionHash, SelectedPublicationID: t.SelectedPublicationID, SelectedProposalCommitID: t.SelectedProposalCommitID, ExpectedRevisionID: t.ExpectedRevisionID, ExpectedNoteVersion: t.ExpectedNoteVersion, ExpectedDocumentID: t.ExpectedDocumentID, ExpectedDocumentVersion: t.ExpectedDocumentVersion, ExpectedPublishedRevisionID: t.ExpectedPublishedRevisionID, ExpectedPublicationID: t.ExpectedPublicationID, ExpectedProposalID: t.ExpectedProposalID, ExpectedProposalRevisionID: t.ExpectedProposalRevisionID, ExpectedProposalVersion: t.ExpectedProposalVersion, RequiresRetirement: *wire.RequiresRetirement}, IdempotencyKey: wire.IdempotencyKey}
	if !validHistoricalTarget(c.SynthesisHistoricalRepublishTarget, workspace, note) || !validRemergeKey(c.IdempotencyKey) || !remergeHeaderMatches(r, c.IdempotencyKey) {
		writeError(w, requestInvalid("invalid historical begin identity"))
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), h.timeout)
	defer cancel()
	v, err := owner.Begin(ctx, c)
	if err != nil {
		writeError(w, err)
		return
	}
	h.writeHistorical(w, v, workspace, note, "", c.SelectedRevisionID)
}
func (h *SynthesisHandler) readHistoricalRepublish(w http.ResponseWriter, r *http.Request) {
	workspace, note, ok := candidateRemergeRoute(w, r)
	if !ok {
		return
	}
	owner, ok := h.historicalOwner(w, r)
	if !ok {
		return
	}
	if !remergeReadBody(w, r) {
		return
	}
	q, err := url.ParseQuery(r.URL.RawQuery)
	if err != nil || len(q) < 1 || len(q) > 2 {
		writeError(w, requestInvalid("begin key or attempt is required"))
		return
	}
	for k, v := range q {
		if (k != "key" && k != "attempt_id") || len(v) != 1 {
			writeError(w, requestInvalid("invalid read query"))
			return
		}
	}
	c := app.ReadSynthesisHistoricalRepublish{WorkspaceID: workspace, NoteID: note, IdempotencyKey: q.Get("key")}
	if _, ok := q["key"]; ok && (!validRemergeKey(c.IdempotencyKey) || !remergeHeaderMatches(r, c.IdempotencyKey)) {
		writeError(w, requestInvalid("invalid begin key"))
		return
	}
	if _, ok := q["attempt_id"]; ok {
		c.AttemptID, err = parseID(q.Get("attempt_id"))
		if err != nil {
			writeError(w, err)
			return
		}
	}
	ctx, cancel := context.WithTimeout(r.Context(), h.timeout)
	defer cancel()
	v, err := owner.Read(ctx, c)
	if err != nil {
		writeError(w, err)
		return
	}
	h.writeHistorical(w, v, workspace, note, c.AttemptID, "")
}
func (h *SynthesisHandler) applyHistoricalRepublish(w http.ResponseWriter, r *http.Request) {
	workspace, note, ok := candidateRemergeRoute(w, r)
	if !ok {
		return
	}
	owner, ok := h.historicalOwner(w, r)
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
	c, err := decodeJSON[struct {
		WorkspaceID            foundation.ID `json:"workspace_id"`
		NoteID                 foundation.ID `json:"note_id"`
		AttemptID              foundation.ID `json:"attempt_id"`
		IdempotencyKey         string        `json:"idempotency_key"`
		PreviewFingerprint     string        `json:"preview_fingerprint"`
		ConfirmExactRestore    *bool         `json:"confirm_exact_restore"`
		RetireCurrentCandidate *bool         `json:"retire_current_candidate"`
	}](r, 16<<10, 1024, 16)
	if err != nil {
		writeError(w, err)
		return
	}
	if c.WorkspaceID != workspace || c.NoteID != note || c.AttemptID != attempt || !validRemergeKey(c.IdempotencyKey) || !remergeHeaderMatches(r, c.IdempotencyKey) || !synthesisValidHash(c.PreviewFingerprint) || c.ConfirmExactRestore == nil || !*c.ConfirmExactRestore || c.RetireCurrentCandidate == nil {
		writeError(w, requestInvalid("exact restore and retirement confirmation are required"))
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), h.timeout)
	defer cancel()
	v, err := owner.Apply(ctx, app.ApplySynthesisHistoricalRepublish{WorkspaceID: workspace, NoteID: note, AttemptID: attempt, IdempotencyKey: c.IdempotencyKey, PreviewFingerprint: c.PreviewFingerprint, ConfirmExactRestore: *c.ConfirmExactRestore, RetireCurrentCandidate: *c.RetireCurrentCandidate})
	if err != nil {
		writeError(w, err)
		return
	}
	h.writeHistorical(w, v, workspace, note, attempt, "")
}
func (h *SynthesisHandler) resumeHistoricalRepublish(w http.ResponseWriter, r *http.Request) {
	workspace, note, ok := candidateRemergeRoute(w, r)
	if !ok {
		return
	}
	owner, ok := h.historicalOwner(w, r)
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
	v, err := owner.Resume(ctx, app.ResumeSynthesisHistoricalRepublish{WorkspaceID: workspace, NoteID: note, AttemptID: attempt})
	if err != nil {
		writeError(w, err)
		return
	}
	h.writeHistorical(w, v, workspace, note, attempt, "")
}

type historicalWarningWire struct {
	Code            string        `json:"code"`
	SourceID        foundation.ID `json:"source_id,omitempty"`
	SourceVersionID foundation.ID `json:"source_version_id,omitempty"`
}
type historicalResultWire struct {
	RevisionID         foundation.ID `json:"revision_id"`
	ArticleRevisionID  foundation.ID `json:"article_revision_id"`
	PublicationID      foundation.ID `json:"publication_id,omitempty"`
	ProposalID         foundation.ID `json:"proposal_id,omitempty"`
	ProposalRevisionID foundation.ID `json:"proposal_revision_id,omitempty"`
}
type historicalScopeWire struct {
	ID           foundation.ID `json:"id"`
	ScopeVersion int64         `json:"scope_version"`
	Scope        struct {
		Topics      []string `json:"topics"`
		Audiences   []string `json:"audiences"`
		Description string   `json:"description"`
	} `json:"scope"`
}

type historicalReviewWire struct {
	CurrentScope       *historicalScopeWire    `json:"current_scope"`
	WorkspaceID        foundation.ID           `json:"workspace_id"`
	NoteID             foundation.ID           `json:"note_id"`
	AttemptID          foundation.ID           `json:"attempt_id"`
	State              string                  `json:"state"`
	Target             historicalTargetWire    `json:"target"`
	Candidate          string                  `json:"candidate"`
	CurrentContent     string                  `json:"current_content"`
	PublishedContent   string                  `json:"published_content"`
	PreviewFingerprint string                  `json:"preview_fingerprint"`
	Warnings           []historicalWarningWire `json:"warnings"`
	Result             *historicalResultWire   `json:"result,omitempty"`
	Replayed           bool                    `json:"replayed"`
}

func (h *SynthesisHandler) writeHistorical(w http.ResponseWriter, v app.SynthesisHistoricalRepublishReview, workspace, note, attempt, selected foundation.ID) {
	if v.WorkspaceID != workspace || v.NoteID != note || !validResponseID(v.AttemptID) || attempt != "" && v.AttemptID != attempt || !validHistoricalTarget(v.Target, workspace, note) || selected != "" && v.Target.SelectedRevisionID != selected || !synthesisValidHash(v.PreviewFingerprint) || !remergeText(v.Candidate) || !remergeText(v.CurrentContent) || !remergeText(v.PublishedContent) || len(v.Warnings) > 1024 || (v.State != "READY" && v.State != "APPLIED") || (v.State == "APPLIED") != (v.Result != nil) {
		writeError(w, synthesisInvalidResult())
		return
	}
	out := historicalReviewWire{WorkspaceID: v.WorkspaceID, NoteID: v.NoteID, AttemptID: v.AttemptID, State: v.State, Target: historicalTarget(v.Target), Candidate: v.Candidate, CurrentContent: v.CurrentContent, PublishedContent: v.PublishedContent, PreviewFingerprint: v.PreviewFingerprint, Warnings: make([]historicalWarningWire, 0, len(v.Warnings)), Replayed: v.Replayed}
	if !bytes.Equal(bytes.TrimSpace(v.CurrentScope), []byte("null")) {
		limits := strictjson.DefaultLimits()
		limits.MaxDocumentBytes = 64 << 10
		limits.MaxStringBytes = 2048
		limits.MaxArrayItems = 64
		scope, err := strictjson.DecodeObject[historicalScopeWire](v.CurrentScope, limits, nil)
		if err != nil || !validResponseID(scope.ID) || scope.ScopeVersion < 1 || len(scope.Scope.Topics) < 1 || len(scope.Scope.Topics) > 64 || len(scope.Scope.Audiences) < 1 || len(scope.Scope.Audiences) > 32 || scope.Scope.Description == "" || len(scope.Scope.Description) > 2048 || !remergeText(scope.Scope.Description) {
			writeError(w, synthesisInvalidResult())
			return
		}
		for _, values := range [][]string{scope.Scope.Topics, scope.Scope.Audiences} {
			for _, value := range values {
				if value == "" || len(value) > 256 || !remergeText(value) {
					writeError(w, synthesisInvalidResult())
					return
				}
			}
		}
		out.CurrentScope = &scope
	}
	for _, warning := range v.Warnings {
		if warning.Code == "" || len(warning.Code) > 128 || !remergeText(warning.Code) || warning.SourceID != "" && !validResponseID(warning.SourceID) || warning.SourceVersionID != "" && !validResponseID(warning.SourceVersionID) {
			writeError(w, synthesisInvalidResult())
			return
		}
		out.Warnings = append(out.Warnings, historicalWarningWire{warning.Code, warning.SourceID, warning.SourceVersionID})
	}
	if r := v.Result; r != nil {
		if !validResponseID(r.RevisionID) || !validResponseID(r.ArticleRevisionID) || r.PublicationID != "" && !validResponseID(r.PublicationID) || (r.ProposalID == "") != (r.ProposalRevisionID == "") || r.ProposalID != "" && (r.PublicationID == "" || !validResponseID(r.ProposalID) || !validResponseID(r.ProposalRevisionID)) {
			writeError(w, synthesisInvalidResult())
			return
		}
		out.Result = &historicalResultWire{r.RevisionID, r.ArticleRevisionID, r.PublicationID, r.ProposalID, r.ProposalRevisionID}
	}
	writeJSON(w, 200, out)
}

// 完整历史身份包含十六个目标字段及原 Begin 键。
// 十七字段预算仅在此命令生效，其他整理命令保留各自预算。
type historicalBeginWire struct {
	historicalTargetWire
	IdempotencyKey     string `json:"idempotency_key"`
	RequiresRetirement *bool  `json:"requires_retirement"`
}

func decodeHistoricalBegin(r *http.Request) (historicalBeginWire, error) {
	return decodeJSONWithObjectFields[historicalBeginWire](r, 16<<10, 1024, 16, 17)
}
