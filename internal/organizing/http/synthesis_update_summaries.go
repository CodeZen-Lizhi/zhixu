package organizinghttp

import (
	"context"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	app "github.com/CodeZen-Lizhi/zhixu/internal/organizing/application"
	"net/http"
	"net/url"
	"strings"
)

func (handler *SynthesisHandler) updateSummaries(writer http.ResponseWriter, request *http.Request) {
	workspaceID, err := parseID(request.PathValue("workspace_id"))
	if err != nil {
		writeError(writer, err)
		return
	}
	values, err := url.ParseQuery(request.URL.RawQuery)
	if err != nil || len(values) != 1 || len(values["note_ids"]) != 1 {
		writeError(writer, requestInvalid("summary query is invalid"))
		return
	}
	raw := strings.Split(values.Get("note_ids"), ",")
	if len(raw) < 1 || len(raw) > app.MaxSynthesisUpdateSummaryNotes {
		writeError(writer, requestInvalid("summary batch exceeds bound"))
		return
	}
	query := app.SynthesisUpdateSummaryQuery{WorkspaceID: workspaceID}
	requested := map[foundation.ID]bool{}
	for _, v := range raw {
		id, err := parseID(v)
		if err != nil || requested[id] {
			writeError(writer, requestInvalid("summary note IDs must be valid and unique"))
			return
		}
		requested[id] = true
		query.NoteIDs = append(query.NoteIDs, id)
	}
	if !handler.available(writer) {
		return
	}
	reader, ok := handler.notes.(app.SynthesisUpdateSummaryReader)
	if !ok || nilDependency(reader) {
		writeProblem(writer, 503, "SYNTHESIS_HTTP_UNAVAILABLE", "更新提醒暂不可用", true)
		return
	}
	ctx, cancel := context.WithTimeout(request.Context(), handler.timeout)
	defer cancel()
	result, err := reader.ReadSynthesisUpdateSummaries(ctx, query)
	if err != nil {
		writeError(writer, err)
		return
	}
	if result.WorkspaceID != workspaceID || len(result.Items) != len(query.NoteIDs) {
		writeError(writer, synthesisInvalidResult())
		return
	}
	seen := map[foundation.ID]bool{}
	for _, item := range result.Items {
		if !requested[item.NoteID] || seen[item.NoteID] || !validResponseID(item.CurrentRevisionID) || (item.PublishedRevisionID != "" && !validResponseID(item.PublishedRevisionID)) || len(item.Items) > 2 || item.Items == nil {
			writeError(writer, synthesisInvalidResult())
			return
		}
		seen[item.NoteID] = true
		revisions := map[foundation.ID]bool{}
		for _, r := range item.Items {
			if !validResponseID(r.RevisionID) || revisions[r.RevisionID] || (r.RevisionID != item.CurrentRevisionID && r.RevisionID != item.PublishedRevisionID) || r.SourceReviewCount < 0 || r.BodyReviewCount < 0 || r.SourceReviewCount > 9007199254740991 || r.BodyReviewCount > 9007199254740991 {
				writeError(writer, synthesisInvalidResult())
				return
			}
			revisions[r.RevisionID] = true
		}
		if !revisions[item.CurrentRevisionID] || (item.PublishedRevisionID != "" && !revisions[item.PublishedRevisionID]) {
			writeError(writer, synthesisInvalidResult())
			return
		}
	}
	writeJSON(writer, http.StatusOK, result)
}
