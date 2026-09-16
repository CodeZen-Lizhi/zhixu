package organizinghttp

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"unicode/utf8"

	authhttp "github.com/CodeZen-Lizhi/zhixu/internal/auth/http"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	app "github.com/CodeZen-Lizhi/zhixu/internal/organizing/application"
)

func (h *SynthesisHandler) WithSourceReviewCommands(store app.SynthesisManuscriptSourceReviewCommandStore) *SynthesisHandler {
	out := *h
	out.sourceReviewCommands = store
	return &out
}

func (h *SynthesisHandler) sourceReviewCommand(w http.ResponseWriter, r *http.Request, recover bool) {
	principal, ok := authhttp.PrincipalFromContext(r.Context())
	if !ok {
		writeError(w, foundation.NewError(foundation.ErrorPermissionDenied, "SYNTHESIS_SOURCE_REVIEW_FORBIDDEN", false, errors.New("authenticated principal is required")))
		return
	}
	caller := app.NewSynthesisManuscriptCaller(principal.Scopes)
	if err := caller.Authorize(); err != nil {
		writeError(w, err)
		return
	}
	if nilDependency(h.sourceReviewCommands) {
		writeProblem(w, 503, "SYNTHESIS_SOURCE_REVIEW_UNAVAILABLE", "当前正文补源操作暂不可用", true)
		return
	}
	ids, ok := sourceReviewIDs(w, r, "workspace_id", "review_id")
	if !ok {
		return
	}
	if r.URL.RawQuery != "" {
		writeError(w, requestInvalid("source review command does not accept query parameters"))
		return
	}
	wire, err := decodeJSON[struct {
		ExpectedVersion int64  `json:"expected_version"`
		IdempotencyKey  string `json:"idempotency_key"`
	}](r, 2048, 512, 2)
	if err != nil {
		writeError(w, err)
		return
	}
	key := wire.IdempotencyKey
	if wire.ExpectedVersion < 1 || len(key) < 1 || len(key) > 128 || !utf8.ValidString(key) || strings.ContainsRune(key, 0) || strings.TrimSpace(key) != key {
		writeError(w, requestInvalid("positive version and bounded idempotency key are required"))
		return
	}
	if values := r.Header.Values("Idempotency-Key"); len(values) > 1 || len(values) == 1 && values[0] != key {
		writeError(w, requestInvalid("idempotency header differs from command"))
		return
	}
	service, err := app.NewSynthesisManuscriptSourceReviewCommandService(h.sourceReviewCommands, caller)
	if err != nil {
		writeError(w, err)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), h.timeout)
	defer cancel()
	command := app.SynthesisManuscriptSourceReviewCommand{WorkspaceID: ids[0], ReviewID: ids[1], ExpectedVersion: wire.ExpectedVersion, IdempotencyKey: key}
	var result app.SynthesisSourceReviewView
	if recover {
		result, err = service.Recover(ctx, command)
	} else {
		result, err = service.Recheck(ctx, command)
	}
	if err != nil {
		writeError(w, err)
		return
	}
	if !validSourceReviewView(result, ids[0]) || (recover && result.ID != ids[1]) || (!recover && result.ID != ids[1] && result.SupersedesID != ids[1]) {
		writeError(w, synthesisInvalidResult())
		return
	}
	writeJSON(w, http.StatusOK, sourceReviewWire(result))
}

func (h *SynthesisHandler) recheckSourceReview(w http.ResponseWriter, r *http.Request) {
	h.sourceReviewCommand(w, r, false)
}

func (h *SynthesisHandler) recoverSourceReview(w http.ResponseWriter, r *http.Request) {
	h.sourceReviewCommand(w, r, true)
}
