package organizinghttp

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation/strictjson"
	"github.com/CodeZen-Lizhi/zhixu/internal/httpapi"
	app "github.com/CodeZen-Lizhi/zhixu/internal/organizing/application"
	"github.com/CodeZen-Lizhi/zhixu/internal/organizing/domain"
	"github.com/gin-gonic/gin"
)

// SynthesisService exposes bounded projections and exact historical sources.
type SynthesisService interface {
	ListNotes(context.Context, app.SynthesisListQuery) (app.SynthesisNotePage, error)
	GetNote(context.Context, foundation.ID, foundation.ID) (app.SynthesisNoteDetail, error)
	GetSynthesisRevision(context.Context, foundation.ID, foundation.ID, foundation.ID) (domain.SynthesisRevision, error)
	ListRevisions(context.Context, app.SynthesisRevisionListQuery) (app.SynthesisRevisionPage, error)
	OpenSource(context.Context, foundation.ID, foundation.ID, foundation.ID, domain.SynthesisSourceRef) (app.SynthesisSourceView, error)
}

// SynthesisProcessingService retains failures that have not produced any note.
type SynthesisProcessingService interface {
	ListProcessing(context.Context, app.SynthesisListQuery) (app.SynthesisProcessingPage, error)
	GetProcessing(context.Context, foundation.ID, foundation.ID) (app.SynthesisProcessing, error)
	RetryProcessing(context.Context, app.RetrySynthesisCommand) (app.RetrySynthesisResult, error)
}

type SynthesisHandler struct {
	notes      SynthesisService
	processing SynthesisProcessingService
	timeout    time.Duration
	cursorKey  []byte
}

// NewSynthesisHandler does not start any background work. Cursor signatures are
// process-local; a cursor from a previous process is explicitly invalid.
func NewSynthesisHandler(notes SynthesisService, processing SynthesisProcessingService, timeout time.Duration) *SynthesisHandler {
	if timeout <= 0 {
		timeout = defaultTimeout
	}
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		key = nil
	}
	return &SynthesisHandler{notes: notes, processing: processing, timeout: timeout, cursorKey: key}
}

func (handler *SynthesisHandler) Available() bool {
	return handler != nil && !nilDependency(handler.notes) && !nilDependency(handler.processing) && len(handler.cursorKey) == 32
}

func (handler *SynthesisHandler) Routes(router gin.IRouter) {
	base := "/workspaces/:workspace_id/synthesis"
	router.GET(base+"/notes", httpapi.GinHandler(handler.listNotes))
	router.GET(base+"/notes/:note_id", httpapi.GinHandler(handler.getNote))
	router.GET(base+"/notes/:note_id/revisions", httpapi.GinHandler(handler.listRevisions))
	router.GET(base+"/notes/:note_id/revisions/:revision_id", httpapi.GinHandler(handler.getRevision))
	router.GET(base+"/notes/:note_id/revisions/:revision_id/sources/:source_span_id", httpapi.GinHandler(handler.openSource))
	router.GET(base+"/processing", httpapi.GinHandler(handler.listProcessing))
	router.GET(base+"/processing/:processing_id", httpapi.GinHandler(handler.getProcessing))
	router.POST(base+"/processing/:processing_id/retry", httpapi.GinHandler(handler.retryProcessing))
}

func (handler *SynthesisHandler) available(writer http.ResponseWriter) bool {
	if !handler.Available() {
		writeProblem(writer, http.StatusServiceUnavailable, "SYNTHESIS_HTTP_UNAVAILABLE", "合成笔记服务暂不可用", true)
		return false
	}
	return true
}

type synthesisPage[T any] struct {
	WorkspaceID foundation.ID `json:"workspace_id"`
	Items       []T           `json:"items"`
	NextCursor  *string       `json:"next_cursor"`
}

type synthesisCursor struct {
	WorkspaceID foundation.ID `json:"workspace_id"`
	Kind        string        `json:"kind"`
	NoteID      foundation.ID `json:"note_id"`
	Limit       int           `json:"limit"`
	BeforeTime  *time.Time    `json:"before_time"`
	BeforeID    foundation.ID `json:"before_id"`
	RevisionNo  int64         `json:"revision_no"`
}

func (handler *SynthesisHandler) parseList(request *http.Request, kind string) (synthesisCursor, error) {
	workspaceID, err := parseID(request.PathValue("workspace_id"))
	if err != nil {
		return synthesisCursor{}, err
	}
	values, err := url.ParseQuery(request.URL.RawQuery)
	if err != nil {
		return synthesisCursor{}, requestInvalid("synthesis list query is invalid")
	}
	for key, entries := range values {
		if (key != "limit" && key != "cursor") || len(entries) != 1 || entries[0] == "" || entries[0] != strings.TrimSpace(entries[0]) {
			return synthesisCursor{}, requestInvalid("synthesis list query is invalid")
		}
	}
	limit := app.DefaultSynthesisListLimit
	if raw := values.Get("limit"); raw != "" {
		limit, err = strconv.Atoi(raw)
		if err != nil || limit < 1 || limit > app.MaxSynthesisListLimit || strconv.Itoa(limit) != raw {
			return synthesisCursor{}, requestInvalid("synthesis list limit is invalid")
		}
	}
	position := synthesisCursor{WorkspaceID: workspaceID, Kind: kind, Limit: limit}
	if kind == "revisions" {
		position.NoteID, err = parseID(request.PathValue("note_id"))
		if err != nil {
			return synthesisCursor{}, err
		}
	}
	if raw := values.Get("cursor"); raw != "" {
		return handler.decodeCursor(raw, position)
	}
	return position, nil
}

func (handler *SynthesisHandler) encodeCursor(position synthesisCursor) (*string, error) {
	if position.RevisionNo == 0 && position.BeforeTime == nil && position.BeforeID == "" {
		return nil, nil
	}
	if len(handler.cursorKey) != 32 || !validSynthesisPosition(position) {
		return nil, synthesisInvalidResult()
	}
	payload, err := json.Marshal(position)
	if err != nil {
		return nil, synthesisInvalidResult()
	}
	signature := hmac.New(sha256.New, handler.cursorKey)
	_, _ = signature.Write(payload)
	encoded := base64.RawURLEncoding.EncodeToString(payload) + "." + base64.RawURLEncoding.EncodeToString(signature.Sum(nil))
	return &encoded, nil
}

func (handler *SynthesisHandler) decodeCursor(raw string, expected synthesisCursor) (synthesisCursor, error) {
	invalid := foundation.NewError(foundation.ErrorInvalidInput, "SYNTHESIS_CURSOR_INVALID", false, errors.New("synthesis cursor is invalid or belongs to an earlier server session"))
	parts := strings.Split(raw, ".")
	if len(raw) > 2048 || len(parts) != 2 || len(handler.cursorKey) != 32 {
		return synthesisCursor{}, invalid
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return synthesisCursor{}, invalid
	}
	signature, err := base64.RawURLEncoding.DecodeString(parts[1])
	mac := hmac.New(sha256.New, handler.cursorKey)
	_, _ = mac.Write(payload)
	if err != nil || !hmac.Equal(mac.Sum(nil), signature) {
		return synthesisCursor{}, invalid
	}
	limits := strictjson.DefaultLimits()
	limits.MaxDocumentBytes = 2048
	position, err := strictjson.DecodeObject[synthesisCursor](payload, limits, nil)
	if err != nil || position.WorkspaceID != expected.WorkspaceID || position.Kind != expected.Kind || position.NoteID != expected.NoteID ||
		position.Limit != expected.Limit || !validSynthesisPosition(position) {
		return synthesisCursor{}, invalid
	}
	return position, nil
}

func validSynthesisPosition(position synthesisCursor) bool {
	if position.Kind == "revisions" {
		return validResponseID(position.NoteID) && position.RevisionNo > 0 && position.RevisionNo <= domain.MaxSynthesisRevisionNo && position.BeforeTime == nil && position.BeforeID == ""
	}
	return position.BeforeTime != nil && !position.BeforeTime.IsZero() && validResponseID(position.BeforeID) && position.RevisionNo == 0 && position.NoteID == ""
}

func (handler *SynthesisHandler) listNotes(writer http.ResponseWriter, request *http.Request) {
	position, err := handler.parseList(request, "notes")
	if err != nil {
		writeError(writer, err)
		return
	}
	if !handler.available(writer) {
		return
	}
	ctx, cancel := context.WithTimeout(request.Context(), handler.timeout)
	defer cancel()
	page, err := handler.notes.ListNotes(ctx, app.SynthesisListQuery{WorkspaceID: position.WorkspaceID, Limit: position.Limit, BeforeTime: position.BeforeTime, BeforeID: position.BeforeID})
	if err != nil {
		writeError(writer, err)
		return
	}
	response := synthesisPage[synthesisNoteSummaryResponse]{WorkspaceID: position.WorkspaceID, Items: make([]synthesisNoteSummaryResponse, len(page.Items))}
	if len(page.Items) > position.Limit {
		writeError(writer, synthesisInvalidResult())
		return
	}
	previousTime, previousID := position.BeforeTime, position.BeforeID
	for index, note := range page.Items {
		if !synthesisBefore(note.Note.UpdatedAt, note.Note.ID, previousTime, previousID) {
			writeError(writer, synthesisInvalidResult())
			return
		}
		response.Items[index], err = toSynthesisNoteSummary(note, position.WorkspaceID)
		if err != nil {
			writeError(writer, err)
			return
		}
		previousTime, previousID = &note.Note.UpdatedAt, note.Note.ID
	}
	position.BeforeTime, position.BeforeID = page.NextTime, page.NextID
	if !synthesisNextMatches(page.NextTime, page.NextID, previousTime, previousID, len(page.Items), position.Limit) {
		writeError(writer, synthesisInvalidResult())
		return
	}
	response.NextCursor, err = handler.encodeCursor(position)
	if err != nil {
		writeError(writer, err)
		return
	}
	writeJSON(writer, http.StatusOK, response)
}

func synthesisBefore(at time.Time, id foundation.ID, previous *time.Time, previousID foundation.ID) bool {
	return previous == nil || at.Before(*previous) || at.Equal(*previous) && id < previousID
}

func synthesisNextMatches(next *time.Time, nextID foundation.ID, last *time.Time, lastID foundation.ID, count, limit int) bool {
	if next == nil {
		return nextID == ""
	}
	return count == limit && last != nil && next.Equal(*last) && nextID == lastID
}

func (handler *SynthesisHandler) getNote(writer http.ResponseWriter, request *http.Request) {
	workspaceID, noteID, ok := queryRoute(writer, request, "note_id")
	if !ok || !handler.available(writer) {
		return
	}
	ctx, cancel := context.WithTimeout(request.Context(), handler.timeout)
	defer cancel()
	value, err := handler.notes.GetNote(ctx, workspaceID, noteID)
	if err != nil {
		writeError(writer, err)
		return
	}
	response, err := toSynthesisDetail(value, workspaceID, noteID)
	if err != nil {
		writeError(writer, err)
		return
	}
	writeJSON(writer, http.StatusOK, response)
}

func (handler *SynthesisHandler) listRevisions(writer http.ResponseWriter, request *http.Request) {
	position, err := handler.parseList(request, "revisions")
	if err != nil {
		writeError(writer, err)
		return
	}
	if !handler.available(writer) {
		return
	}
	ctx, cancel := context.WithTimeout(request.Context(), handler.timeout)
	defer cancel()
	page, err := handler.notes.ListRevisions(ctx, app.SynthesisRevisionListQuery{WorkspaceID: position.WorkspaceID, NoteID: position.NoteID, Limit: position.Limit, BeforeRevisionNo: position.RevisionNo})
	if err != nil {
		writeError(writer, err)
		return
	}
	response := struct {
		synthesisPage[synthesisRevisionSummaryResponse]
		NoteID foundation.ID `json:"note_id"`
	}{synthesisPage[synthesisRevisionSummaryResponse]{WorkspaceID: position.WorkspaceID, Items: make([]synthesisRevisionSummaryResponse, len(page.Items))}, position.NoteID}
	if len(page.Items) > position.Limit {
		writeError(writer, synthesisInvalidResult())
		return
	}
	lastRevisionNo := position.RevisionNo
	for index, revision := range page.Items {
		if _, err = toSynthesisRevision(&revision, position.WorkspaceID, position.NoteID); err != nil || lastRevisionNo > 0 && revision.RevisionNo >= lastRevisionNo {
			writeError(writer, synthesisInvalidResult())
			return
		}
		summary := synthesisSummary(revision)
		projection, projectionErr := toSynthesisRevisionSummary(&summary)
		if projectionErr != nil {
			writeError(writer, projectionErr)
			return
		}
		response.Items[index] = *projection
		lastRevisionNo = revision.RevisionNo
	}
	if page.NextBeforeRevisionNo != 0 && (len(page.Items) != position.Limit || page.NextBeforeRevisionNo != lastRevisionNo) {
		writeError(writer, synthesisInvalidResult())
		return
	}
	position.RevisionNo = page.NextBeforeRevisionNo
	response.NextCursor, err = handler.encodeCursor(position)
	if err != nil {
		writeError(writer, err)
		return
	}
	writeJSON(writer, http.StatusOK, response)
}

func (handler *SynthesisHandler) readRevision(writer http.ResponseWriter, request *http.Request) (domain.SynthesisRevision, bool) {
	workspaceID, noteID, ok := queryRoute(writer, request, "note_id")
	if !ok {
		return domain.SynthesisRevision{}, false
	}
	revisionID, err := parseID(request.PathValue("revision_id"))
	if err != nil {
		writeError(writer, err)
		return domain.SynthesisRevision{}, false
	}
	if !handler.available(writer) {
		return domain.SynthesisRevision{}, false
	}
	value, err := handler.notes.GetSynthesisRevision(request.Context(), workspaceID, noteID, revisionID)
	if err != nil {
		writeError(writer, err)
		return domain.SynthesisRevision{}, false
	}
	if value.ID != revisionID || value.NoteID != noteID || value.WorkspaceID != workspaceID || value.Validate() != nil {
		writeError(writer, synthesisInvalidResult())
		return domain.SynthesisRevision{}, false
	}
	return value, true
}

func (handler *SynthesisHandler) getRevision(writer http.ResponseWriter, request *http.Request) {
	ctx, cancel := context.WithTimeout(request.Context(), handler.timeout)
	defer cancel()
	value, ok := handler.readRevision(writer, request.WithContext(ctx))
	if !ok {
		return
	}
	revision, err := toSynthesisRevision(&value, value.WorkspaceID, value.NoteID)
	if err != nil {
		writeError(writer, err)
		return
	}
	writeJSON(writer, http.StatusOK, struct {
		WorkspaceID foundation.ID              `json:"workspace_id"`
		NoteID      foundation.ID              `json:"note_id"`
		Revision    *synthesisRevisionResponse `json:"revision"`
	}{value.WorkspaceID, value.NoteID, revision})
}

func (handler *SynthesisHandler) openSource(writer http.ResponseWriter, request *http.Request) {
	spanID, err := parseID(request.PathValue("source_span_id"))
	if err != nil {
		writeError(writer, err)
		return
	}
	ctx, cancel := context.WithTimeout(request.Context(), handler.timeout)
	defer cancel()
	revision, ok := handler.readRevision(writer, request.WithContext(ctx))
	if !ok {
		return
	}
	var reference *domain.SynthesisSourceRef
	for _, item := range revision.Items {
		for _, source := range item.SourceReferences() {
			if source.SourceSpanID == spanID {
				if reference != nil && !sameSynthesisSource(*reference, source) {
					writeError(writer, synthesisInvalidResult())
					return
				}
				copy := source
				reference = &copy
			}
		}
	}
	if reference == nil {
		writeProblem(writer, http.StatusNotFound, "SYNTHESIS_SOURCE_NOT_FOUND", "请求的笔记来源不存在", false)
		return
	}
	view, err := handler.notes.OpenSource(ctx, revision.WorkspaceID, revision.NoteID, revision.ID, *reference)
	if err != nil {
		writeError(writer, err)
		return
	}
	if !sameSynthesisSource(view.Reference, *reference) || view.Validate(revision.WorkspaceID) != nil {
		writeError(writer, synthesisInvalidResult())
		return
	}
	var text *string
	if view.Availability == domain.MaterialAvailable {
		text = &view.Text
	}
	writeJSON(writer, http.StatusOK, struct {
		WorkspaceID  foundation.ID               `json:"workspace_id"`
		NoteID       foundation.ID               `json:"note_id"`
		RevisionID   foundation.ID               `json:"revision_id"`
		Reference    domain.SynthesisSourceRef   `json:"reference"`
		Availability domain.MaterialAvailability `json:"availability"`
		Text         *string                     `json:"text"`
	}{revision.WorkspaceID, revision.NoteID, revision.ID, *reference, view.Availability, text})
}

func sameSynthesisSource(left, right domain.SynthesisSourceRef) bool {
	return left.Source == right.Source && left.SourceSpanID == right.SourceSpanID && left.ExcerptHash == right.ExcerptHash
}

func (handler *SynthesisHandler) listProcessing(writer http.ResponseWriter, request *http.Request) {
	position, err := handler.parseList(request, "processing")
	if err != nil {
		writeError(writer, err)
		return
	}
	if !handler.available(writer) {
		return
	}
	ctx, cancel := context.WithTimeout(request.Context(), handler.timeout)
	defer cancel()
	page, err := handler.processing.ListProcessing(ctx, app.SynthesisListQuery{WorkspaceID: position.WorkspaceID, Limit: position.Limit, BeforeTime: position.BeforeTime, BeforeID: position.BeforeID})
	if err != nil {
		writeError(writer, err)
		return
	}
	response := synthesisPage[synthesisProcessingResponse]{WorkspaceID: position.WorkspaceID, Items: make([]synthesisProcessingResponse, len(page.Items))}
	if len(page.Items) > position.Limit {
		writeError(writer, synthesisInvalidResult())
		return
	}
	previousTime, previousID := position.BeforeTime, position.BeforeID
	for index, processing := range page.Items {
		if !synthesisBefore(processing.UpdatedAt, processing.ID, previousTime, previousID) {
			writeError(writer, synthesisInvalidResult())
			return
		}
		response.Items[index], err = toSynthesisProcessing(processing, position.WorkspaceID)
		if err != nil {
			writeError(writer, err)
			return
		}
		previousTime, previousID = &processing.UpdatedAt, processing.ID
	}
	if !synthesisNextMatches(page.NextTime, page.NextID, previousTime, previousID, len(page.Items), position.Limit) {
		writeError(writer, synthesisInvalidResult())
		return
	}
	position.BeforeTime, position.BeforeID = page.NextTime, page.NextID
	response.NextCursor, err = handler.encodeCursor(position)
	if err != nil {
		writeError(writer, err)
		return
	}
	writeJSON(writer, http.StatusOK, response)
}

func (handler *SynthesisHandler) getProcessing(writer http.ResponseWriter, request *http.Request) {
	workspaceID, processingID, ok := queryRoute(writer, request, "processing_id")
	if !ok || !handler.available(writer) {
		return
	}
	ctx, cancel := context.WithTimeout(request.Context(), handler.timeout)
	defer cancel()
	value, err := handler.processing.GetProcessing(ctx, workspaceID, processingID)
	if err != nil {
		writeError(writer, err)
		return
	}
	processing, err := toSynthesisProcessing(value, workspaceID)
	if err != nil || processing.ID != processingID {
		writeError(writer, synthesisInvalidResult())
		return
	}
	writeJSON(writer, http.StatusOK, struct {
		WorkspaceID foundation.ID               `json:"workspace_id"`
		Processing  synthesisProcessingResponse `json:"processing"`
	}{workspaceID, processing})
}

func (handler *SynthesisHandler) retryProcessing(writer http.ResponseWriter, request *http.Request) {
	workspaceID, processingID, key, ok := identifiedCommandRoute(writer, request, "processing_id")
	if !ok {
		return
	}
	input, err := decodeJSON[expectedVersionRequest](request, maxSmallRequestBodyBytes, 512, 1)
	if err != nil || input.ExpectedVersion < 1 {
		if err == nil {
			err = requestInvalid("expected_version must be positive")
		}
		writeError(writer, err)
		return
	}
	if !handler.available(writer) {
		return
	}
	ctx, cancel := context.WithTimeout(request.Context(), handler.timeout)
	defer cancel()
	result, err := handler.processing.RetryProcessing(ctx, app.RetrySynthesisCommand{WorkspaceID: workspaceID, ProcessingID: processingID, ExpectedVersion: input.ExpectedVersion, IdempotencyKey: key})
	if err != nil {
		writeError(writer, err)
		return
	}
	processing, err := toSynthesisProcessing(result.Processing, workspaceID)
	if err != nil || processing.ID != processingID || processing.Version <= input.ExpectedVersion {
		writeError(writer, synthesisInvalidResult())
		return
	}
	writeJSON(writer, http.StatusAccepted, struct {
		WorkspaceID foundation.ID               `json:"workspace_id"`
		Processing  synthesisProcessingResponse `json:"processing"`
		Replayed    bool                        `json:"replayed"`
	}{workspaceID, processing, result.Replayed})
}
