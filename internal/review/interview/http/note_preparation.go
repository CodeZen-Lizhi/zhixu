package interviewhttp

import (
	"context"
	"net/http"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	"github.com/CodeZen-Lizhi/zhixu/internal/httpapi"
	interviewapp "github.com/CodeZen-Lizhi/zhixu/internal/review/interview/application"
	"github.com/CodeZen-Lizhi/zhixu/internal/review/interview/domain"
	"github.com/gin-gonic/gin"
)

type NotePreparationService interface {
	Prepare(context.Context, interviewapp.PrepareNoteInterviewCommand) (interviewapp.NotePreparationResult, error)
	Get(context.Context, foundation.ID, foundation.ID, foundation.ID) (interviewapp.NotePreparation, error)
	Retry(context.Context, interviewapp.RetryNoteInterviewCommand) (interviewapp.NotePreparationResult, error)
	List(context.Context, foundation.ID, foundation.ID) ([]interviewapp.NotePreparation, error)
}

// NotePreparationHandler exposes only redacted durable preparation metadata.
// Answer points and source excerpts are never returned from these endpoints.
type NotePreparationHandler struct{ service NotePreparationService }

func NewNotePreparationHandler(service NotePreparationService) *NotePreparationHandler {
	return &NotePreparationHandler{service: service}
}
func (handler *NotePreparationHandler) Available() bool {
	return handler != nil && !nilDependency(handler.service)
}

func (handler *NotePreparationHandler) Routes(router gin.IRouter) {
	const base = "/workspaces/:workspace_id/synthesis/notes/:note_id/interviews"
	router.POST(base, httpapi.GinHandler(handler.prepare))
	router.GET(base, httpapi.GinHandler(handler.list))
	router.GET(base+"/:preparation_id", httpapi.GinHandler(handler.get))
	router.POST(base+"/:preparation_id/retry", httpapi.GinHandler(handler.retry))
}

func (handler *NotePreparationHandler) prepare(w http.ResponseWriter, r *http.Request) {
	handler.mutate(w, r, false)
}
func (handler *NotePreparationHandler) retry(w http.ResponseWriter, r *http.Request) {
	handler.mutate(w, r, true)
}

func (handler *NotePreparationHandler) mutate(w http.ResponseWriter, r *http.Request, retry bool) {
	noStore(w)
	if !requirePrincipal(w, r) {
		return
	}
	workspaceID, noteID, err := notePreparationPath(r)
	if err != nil {
		writeError(w, err)
		return
	}
	key, err := parseIdempotencyKey(r)
	if err != nil {
		writeError(w, err)
		return
	}
	wire, err := decodeJSON[noteOptionsRequest](r)
	if err != nil {
		writeError(w, err)
		return
	}
	options, err := wire.options()
	if err != nil {
		writeError(w, err)
		return
	}
	var preparationID foundation.ID
	if retry {
		preparationID, err = parseID(r.PathValue("preparation_id"))
		if err != nil {
			writeError(w, err)
			return
		}
	}
	if !handler.Available() {
		writeError(w, domain.UnavailableError(interviewapp.ErrorCodeNotePlanUnavailable, "note interview preparation is unavailable"))
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	var result interviewapp.NotePreparationResult
	if retry {
		result, err = handler.service.Retry(ctx, interviewapp.RetryNoteInterviewCommand{WorkspaceID: workspaceID, NoteID: noteID, PreparationID: preparationID, Options: options, IdempotencyKey: key})
	} else {
		result, err = handler.service.Prepare(ctx, interviewapp.PrepareNoteInterviewCommand{WorkspaceID: workspaceID, NoteID: noteID, Options: options, IdempotencyKey: key})
	}
	if err != nil {
		writeError(w, err)
		return
	}
	if err := validateNotePreparationResponse(result.Preparation, workspaceID, noteID); err != nil {
		writeError(w, err)
		return
	}
	httpapi.WriteJSON(w, http.StatusAccepted, result)
}

func (handler *NotePreparationHandler) get(w http.ResponseWriter, r *http.Request) {
	noStore(w)
	if !requirePrincipal(w, r) {
		return
	}
	workspaceID, noteID, err := notePreparationPath(r)
	if err != nil {
		writeError(w, err)
		return
	}
	preparationID, err := parseID(r.PathValue("preparation_id"))
	if err != nil {
		writeError(w, err)
		return
	}
	if !handler.Available() {
		writeError(w, domain.UnavailableError(interviewapp.ErrorCodeNotePlanUnavailable, "note interview preparation is unavailable"))
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), defaultRequestTimeout)
	defer cancel()
	preparation, err := handler.service.Get(ctx, workspaceID, noteID, preparationID)
	if err != nil {
		writeError(w, err)
		return
	}
	if err := validateNotePreparationResponse(preparation, workspaceID, noteID); err != nil || preparation.ID != preparationID {
		writeError(w, resultInvalid("note interview preparation result is inconsistent"))
		return
	}
	httpapi.WriteJSON(w, http.StatusOK, interviewapp.NotePreparationResult{Preparation: preparation})
}

func (handler *NotePreparationHandler) list(w http.ResponseWriter, r *http.Request) {
	noStore(w)
	if !requirePrincipal(w, r) {
		return
	}
	workspaceID, noteID, err := notePreparationPath(r)
	if err != nil {
		writeError(w, err)
		return
	}
	if !handler.Available() {
		writeError(w, domain.UnavailableError(interviewapp.ErrorCodeNotePlanUnavailable, "note interview preparation is unavailable"))
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), defaultRequestTimeout)
	defer cancel()
	items, err := handler.service.List(ctx, workspaceID, noteID)
	if err != nil {
		writeError(w, err)
		return
	}
	if len(items) > 20 {
		writeError(w, resultInvalid("note interview list exceeded its limit"))
		return
	}
	for _, item := range items {
		if err := validateNotePreparationResponse(item, workspaceID, noteID); err != nil {
			writeError(w, err)
			return
		}
	}
	if items == nil {
		items = []interviewapp.NotePreparation{}
	}
	httpapi.WriteJSON(w, http.StatusOK, struct {
		Items []interviewapp.NotePreparation `json:"items"`
	}{items})
}

func notePreparationPath(r *http.Request) (foundation.ID, foundation.ID, error) {
	if err := rejectQuery(r); err != nil {
		return "", "", err
	}
	workspaceID, err := parseID(r.PathValue("workspace_id"))
	if err != nil {
		return "", "", err
	}
	noteID, err := parseID(r.PathValue("note_id"))
	return workspaceID, noteID, err
}

func validateNotePreparationResponse(preparation interviewapp.NotePreparation, workspaceID, noteID foundation.ID) error {
	if preparation.Validate() != nil || preparation.WorkspaceID != workspaceID || preparation.NoteRevision.NoteID != noteID {
		return resultInvalid("note interview preparation result is inconsistent")
	}
	return nil
}

// Pointers distinguish a required zero follow-up budget from a missing field.
type noteOptionsRequest struct {
	Role            *string            `json:"role"`
	Difficulty      *domain.Difficulty `json:"difficulty"`
	DurationMinutes *int               `json:"duration_minutes"`
	QuestionCount   *int               `json:"question_count"`
	MaxFollowUps    *int               `json:"max_follow_ups"`
}

func (value noteOptionsRequest) options() (interviewapp.NoteInterviewOptions, error) {
	if value.Role == nil || value.Difficulty == nil || value.DurationMinutes == nil || value.QuestionCount == nil || value.MaxFollowUps == nil {
		return interviewapp.NoteInterviewOptions{}, domain.InvalidError(interviewapp.ErrorCodeNotePreparationInvalid, "all note interview options are required")
	}
	options := interviewapp.NoteInterviewOptions{Role: *value.Role, Difficulty: *value.Difficulty, DurationMinutes: *value.DurationMinutes, QuestionCount: *value.QuestionCount, MaxFollowUps: *value.MaxFollowUps}
	return options, options.Validate()
}
