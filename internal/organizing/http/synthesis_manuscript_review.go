package organizinghttp

import (
	"context"
	"errors"
	"net/http"

	authhttp "github.com/CodeZen-Lizhi/zhixu/internal/auth/http"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	app "github.com/CodeZen-Lizhi/zhixu/internal/organizing/application"
	workflowapp "github.com/CodeZen-Lizhi/zhixu/internal/workflow/application"
)

type SynthesisManuscriptReviewFactory interface {
	HumanReview(app.SynthesisManuscriptCaller, *workflowapp.RuntimeHumanCoordinator) (*app.SynthesisManuscriptReviewService, error)
}
type manuscriptReviewDependencies struct {
	factory     SynthesisManuscriptReviewFactory
	coordinator *workflowapp.RuntimeHumanCoordinator
}

// WithManuscriptReviews 返回组合时的副本。请求主体身份传给新的所属模块调用，
// 不能安装到共享 Handler 或 Store 上。
func (h *SynthesisHandler) WithManuscriptReviews(factory SynthesisManuscriptReviewFactory, coordinator *workflowapp.RuntimeHumanCoordinator) *SynthesisHandler {
	out := *h
	out.manuscriptReviews = &manuscriptReviewDependencies{factory: factory, coordinator: coordinator}
	return &out
}
func (h *SynthesisHandler) manuscriptService(w http.ResponseWriter, r *http.Request) (*app.SynthesisManuscriptReviewService, bool) {
	principal, ok := authhttp.PrincipalFromContext(r.Context())
	if !ok {
		writeError(w, foundation.NewError(foundation.ErrorPermissionDenied, "SYNTHESIS_MANUSCRIPT_HUMAN_FORBIDDEN", false, errors.New("authenticated principal is required")))
		return nil, false
	}
	if h.manuscriptReviews == nil || nilDependency(h.manuscriptReviews.factory) || h.manuscriptReviews.coordinator == nil {
		writeProblem(w, 503, "SYNTHESIS_MANUSCRIPT_HTTP_UNAVAILABLE", "正文冲突处理暂不可用", true)
		return nil, false
	}
	service, err := h.manuscriptReviews.factory.HumanReview(app.NewSynthesisManuscriptCaller(principal.Scopes), h.manuscriptReviews.coordinator)
	if err != nil {
		writeError(w, err)
		return nil, false
	}
	return service, true
}
func (h *SynthesisHandler) manuscriptSummary(w http.ResponseWriter, r *http.Request) {
	workspace, processing, ok := queryRoute(w, r, "processing_id")
	if !ok {
		return
	}
	if r.URL.RawQuery != "" {
		writeError(w, requestInvalid("review does not accept query parameters"))
		return
	}
	service, ok := h.manuscriptService(w, r)
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), h.timeout)
	defer cancel()
	result, err := service.Read(ctx, workspace, processing)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, result)
}
func (h *SynthesisHandler) manuscriptDetail(w http.ResponseWriter, r *http.Request) {
	workspace, processing, ok := queryRoute(w, r, "processing_id")
	if !ok {
		return
	}
	note, err := parseID(r.PathValue("note_id"))
	if err != nil {
		writeError(w, err)
		return
	}
	if r.URL.RawQuery != "" {
		writeError(w, requestInvalid("review does not accept query parameters"))
		return
	}
	service, ok := h.manuscriptService(w, r)
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), h.timeout)
	defer cancel()
	result, err := service.ReadNote(ctx, workspace, processing, note)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, manuscriptDetailWire(result))
}
func (h *SynthesisHandler) manuscriptDecide(w http.ResponseWriter, r *http.Request) {
	workspace, processing, ok := queryRoute(w, r, "processing_id")
	if !ok {
		return
	}
	note, err := parseID(r.PathValue("note_id"))
	if err != nil {
		writeError(w, err)
		return
	}
	if r.URL.RawQuery != "" {
		writeError(w, requestInvalid("review does not accept query parameters"))
		return
	}
	command, err := decodeJSON[app.DecideSynthesisManuscript](r, 8<<20, 1<<20, 1024)
	if err != nil {
		writeError(w, err)
		return
	}
	if command.Binding.WorkspaceID != workspace || command.Binding.ProcessingID != processing || command.NoteID != note || len(r.Header.Values("Idempotency-Key")) > 1 || r.Header.Get("Idempotency-Key") != "" && r.Header.Get("Idempotency-Key") != command.IdempotencyKey {
		writeError(w, requestInvalid("review command differs from route or idempotency key"))
		return
	}
	service, ok := h.manuscriptService(w, r)
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), h.timeout)
	defer cancel()
	result, err := service.Decide(ctx, command)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, result)
}

// manuscriptResume 只接受不可变的所属模块绑定，不能接受决策。
func (h *SynthesisHandler) manuscriptResume(w http.ResponseWriter, r *http.Request) {
	workspace, processing, ok := queryRoute(w, r, "processing_id")
	if !ok {
		return
	}
	if r.URL.RawQuery != "" {
		writeError(w, requestInvalid("review does not accept query parameters"))
		return
	}
	command, err := decodeJSON[struct {
		Binding app.SynthesisManuscriptHumanBinding `json:"binding"`
	}](r, 8<<10, 1024, 16)
	if err != nil {
		writeError(w, err)
		return
	}
	if command.Binding.WorkspaceID != workspace || command.Binding.ProcessingID != processing {
		writeError(w, requestInvalid("review binding differs from route"))
		return
	}
	service, ok := h.manuscriptService(w, r)
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), h.timeout)
	defer cancel()
	result, err := service.Resume(ctx, command.Binding)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, result)
}

// 内部合并冲突使用字节切片，且不带传输标签。
// 只投影审阅者可查看的 UTF-8 文本，不暴露账本。
type manuscriptConflictResponse struct {
	Ordinal  int    `json:"ordinal"`
	Base     string `json:"base"`
	Current  string `json:"current"`
	Proposed string `json:"proposed"`
}
type manuscriptReviewResponse struct {
	Stage     string                       `json:"stage"`
	Base      string                       `json:"base"`
	Current   string                       `json:"current"`
	Proposed  string                       `json:"proposed"`
	Candidate string                       `json:"candidate"`
	Conflicts []manuscriptConflictResponse `json:"conflicts"`
}
type manuscriptDetailResponse struct {
	Binding app.SynthesisManuscriptHumanBinding `json:"binding"`
	Target  app.SynthesisManuscriptReviewItem   `json:"target"`
	Review  *manuscriptReviewResponse           `json:"review,omitempty"`
}

func manuscriptDetailWire(detail app.SynthesisManuscriptReviewDetail) manuscriptDetailResponse {
	out := manuscriptDetailResponse{Binding: detail.Binding, Target: detail.Target}
	if detail.Review != nil {
		r := detail.Review
		out.Review = &manuscriptReviewResponse{Stage: r.Stage, Base: r.Base, Current: r.Current, Proposed: r.Proposed, Candidate: r.Candidate, Conflicts: make([]manuscriptConflictResponse, 0, len(r.Conflicts))}
		for _, c := range r.Conflicts {
			out.Review.Conflicts = append(out.Review.Conflicts, manuscriptConflictResponse{Ordinal: c.Ordinal, Base: string(c.Base), Current: string(c.Current), Proposed: string(c.Proposed)})
		}
	}
	return out
}
