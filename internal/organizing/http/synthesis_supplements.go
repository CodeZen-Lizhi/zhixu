package organizinghttp

import (
	"context"
	"net/http"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	app "github.com/CodeZen-Lizhi/zhixu/internal/organizing/application"
	"github.com/CodeZen-Lizhi/zhixu/internal/organizing/domain"
)

type SynthesisSupplementService interface {
	ListSupplements(context.Context, app.SynthesisSupplementListQuery) (app.SynthesisSupplementPage, error)
	OpenSupplement(context.Context, foundation.ID, foundation.ID, foundation.ID) (app.SynthesisSourceSupplement, app.SynthesisSourceView, error)
}

func validSupplement(value app.SynthesisSourceSupplement, workspaceID, noteID foundation.ID) bool {
	if value.WorkspaceID != workspaceID || value.NoteID != noteID || value.Reference.Source.WorkspaceID != workspaceID || value.Reference.Validate() != nil || value.CreatedAt.IsZero() {
		return false
	}
	for _, id := range []foundation.ID{value.ID, value.BaseRevisionID, value.ItemID, value.ProcessingID} {
		if !validResponseID(id) {
			return false
		}
	}
	switch value.Slot {
	case "FACT", "GAP_CONTEXT", "GAP_RESOLUTION":
		return value.AlternativeIndex == -1
	case "CONFLICT":
		return value.AlternativeIndex >= 0 && value.AlternativeIndex < domain.MaxSynthesisAlternatives
	}
	return false
}

func (handler *SynthesisHandler) supplements(writer http.ResponseWriter) (SynthesisSupplementService, bool) {
	if !handler.available(writer) {
		return nil, false
	}
	service, ok := handler.notes.(SynthesisSupplementService)
	if !ok || nilDependency(service) {
		writeProblem(writer, http.StatusServiceUnavailable, "SYNTHESIS_HTTP_UNAVAILABLE", "补充来源服务暂不可用", true)
		return nil, false
	}
	return service, true
}

func (handler *SynthesisHandler) listSupplements(writer http.ResponseWriter, request *http.Request) {
	position, err := handler.parseList(request, "supplements")
	if err != nil {
		writeError(writer, err)
		return
	}
	service, ok := handler.supplements(writer)
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(request.Context(), handler.timeout)
	defer cancel()
	page, err := service.ListSupplements(ctx, app.SynthesisSupplementListQuery{WorkspaceID: position.WorkspaceID, NoteID: position.NoteID, Limit: position.Limit, BeforeTime: position.BeforeTime, BeforeID: position.BeforeID})
	if err != nil {
		writeError(writer, err)
		return
	}
	if len(page.Items) > position.Limit {
		writeError(writer, synthesisInvalidResult())
		return
	}
	response := struct {
		synthesisPage[app.SynthesisSourceSupplement]
		NoteID foundation.ID `json:"note_id"`
	}{synthesisPage[app.SynthesisSourceSupplement]{WorkspaceID: position.WorkspaceID, Items: make([]app.SynthesisSourceSupplement, 0, len(page.Items))}, position.NoteID}
	previousTime, previousID := position.BeforeTime, position.BeforeID
	for _, value := range page.Items {
		if !validSupplement(value, position.WorkspaceID, position.NoteID) || !synthesisBefore(value.CreatedAt, value.ID, previousTime, previousID) {
			writeError(writer, synthesisInvalidResult())
			return
		}
		response.Items = append(response.Items, value)
		at := value.CreatedAt
		previousTime, previousID = &at, value.ID
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

func (handler *SynthesisHandler) openSupplement(writer http.ResponseWriter, request *http.Request) {
	workspaceID, noteID, ok := queryRoute(writer, request, "note_id")
	if !ok {
		return
	}
	supplementID, err := parseID(request.PathValue("supplement_id"))
	if err != nil {
		writeError(writer, err)
		return
	}
	service, ok := handler.supplements(writer)
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(request.Context(), handler.timeout)
	defer cancel()
	value, view, err := service.OpenSupplement(ctx, workspaceID, noteID, supplementID)
	if err != nil {
		writeError(writer, err)
		return
	}
	if value.ID != supplementID || !validSupplement(value, workspaceID, noteID) || view.Reference != value.Reference || view.Validate(workspaceID) != nil {
		writeError(writer, synthesisInvalidResult())
		return
	}
	var text *string
	if view.Availability == domain.MaterialAvailable {
		text = &view.Text
	}
	writeJSON(writer, http.StatusOK, struct {
		WorkspaceID  foundation.ID                 `json:"workspace_id"`
		NoteID       foundation.ID                 `json:"note_id"`
		Supplement   app.SynthesisSourceSupplement `json:"supplement"`
		Availability domain.MaterialAvailability   `json:"availability"`
		Text         *string                       `json:"text"`
		SnapshotText string                        `json:"snapshot_text,omitempty"`
	}{workspaceID, noteID, value, view.Availability, text, view.SnapshotText})
}
