package organizinghttp

import (
	"context"
	"net/http"
	"net/url"
	"strconv"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	app "github.com/CodeZen-Lizhi/zhixu/internal/organizing/application"
)

func (handler *SynthesisHandler) sourceGraph(writer http.ResponseWriter, request *http.Request) {
	query := app.SynthesisSourceGraphQuery{Limit: 20}
	for name, target := range map[string]*foundation.ID{"workspace_id": &query.WorkspaceID, "note_id": &query.NoteID, "revision_id": &query.RevisionID} {
		id, err := parseID(request.PathValue(name))
		if err != nil {
			writeError(writer, err)
			return
		}
		*target = id
	}
	values, err := url.ParseQuery(request.URL.RawQuery)
	if err != nil {
		writeError(writer, requestInvalid("source graph query is invalid"))
		return
	}
	for key, entries := range values {
		if len(entries) != 1 || (key != "limit" && key != "after_note_id") {
			writeError(writer, requestInvalid("source graph query is invalid"))
			return
		}
		switch key {
		case "limit":
			query.Limit, err = strconv.Atoi(entries[0])
			if err != nil || query.Limit < 1 || query.Limit > 20 || strconv.Itoa(query.Limit) != entries[0] {
				writeError(writer, requestInvalid("source graph limit is invalid"))
				return
			}
		case "after_note_id":
			query.AfterNoteID, err = parseID(entries[0])
			if err != nil {
				writeError(writer, err)
				return
			}
		}
	}
	if !handler.available(writer) {
		return
	}
	reader, ok := handler.notes.(app.SynthesisSourceGraphReader)
	if !ok || nilDependency(reader) {
		writeProblem(writer, 503, "SYNTHESIS_HTTP_UNAVAILABLE", "来源图谱暂不可用", true)
		return
	}
	ctx, cancel := context.WithTimeout(request.Context(), handler.timeout)
	defer cancel()
	graph, err := reader.ReadSynthesisSourceGraph(ctx, query)
	if err != nil {
		writeError(writer, err)
		return
	}
	if graph.WorkspaceID != query.WorkspaceID || graph.NoteID != query.NoteID || graph.RevisionID != query.RevisionID || len(graph.Sources) > 256 || len(graph.SharedNotes) > query.Limit {
		writeError(writer, synthesisInvalidResult())
		return
	}
	sourceIDs := map[foundation.ID]bool{}
	for _, ref := range graph.Sources {
		if ref.Validate() != nil || ref.Source.WorkspaceID != query.WorkspaceID {
			writeError(writer, synthesisInvalidResult())
			return
		}
		sourceIDs[ref.Source.SourceID] = true
	}
	previous := query.AfterNoteID
	for _, note := range graph.SharedNotes {
		if !validResponseID(note.NoteID) || !validResponseID(note.RevisionID) || note.NoteID == query.NoteID || note.NoteID <= previous || note.RevisionNo < 1 || note.Title == "" || len(note.Sources) < 1 || len(note.Sources) > 256 {
			writeError(writer, synthesisInvalidResult())
			return
		}
		previous = note.NoteID
		for _, ref := range note.Sources {
			if ref.Validate() != nil || ref.Source.WorkspaceID != query.WorkspaceID || !sourceIDs[ref.Source.SourceID] {
				writeError(writer, synthesisInvalidResult())
				return
			}
		}
	}
	if graph.NextAfterNoteID != nil && (len(graph.SharedNotes) != query.Limit || *graph.NextAfterNoteID != previous) {
		writeError(writer, synthesisInvalidResult())
		return
	}
	writeJSON(writer, http.StatusOK, graph)
}
