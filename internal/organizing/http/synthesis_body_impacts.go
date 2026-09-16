package organizinghttp

import (
	"context"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	app "github.com/CodeZen-Lizhi/zhixu/internal/organizing/application"
)

type synthesisBodyImpactResponse struct {
	ID                    foundation.ID `json:"id"`
	ItemID                foundation.ID `json:"item_id"`
	UpstreamNoteID        foundation.ID `json:"upstream_note_id"`
	UpstreamRevisionID    foundation.ID `json:"upstream_revision_id"`
	UpstreamItemID        foundation.ID `json:"upstream_item_id"`
	UpstreamPublicationID foundation.ID `json:"upstream_publication_id"`
	PublicationID         foundation.ID `json:"publication_id"`
	PublishedRevisionID   foundation.ID `json:"published_revision_id"`
	Reason                string        `json:"reason"`
	DetectedAt            time.Time     `json:"detected_at"`
}

func (handler *SynthesisHandler) bodyImpacts(writer http.ResponseWriter, request *http.Request) {
	query := app.SynthesisBodyImpactQuery{Limit: 20}
	for name, target := range map[string]*foundation.ID{"workspace_id": &query.WorkspaceID, "note_id": &query.NoteID, "revision_id": &query.BaseRevisionID} {
		id, err := parseID(request.PathValue(name))
		if err != nil {
			writeError(writer, err)
			return
		}
		*target = id
	}
	values, err := url.ParseQuery(request.URL.RawQuery)
	if err != nil {
		writeError(writer, requestInvalid("body impact query is invalid"))
		return
	}
	for key, entries := range values {
		if len(entries) != 1 || (key != "limit" && key != "after_id") {
			writeError(writer, requestInvalid("body impact query is invalid"))
			return
		}
		if key == "limit" {
			query.Limit, err = strconv.Atoi(entries[0])
			if err != nil || query.Limit < 1 || query.Limit > 100 || strconv.Itoa(query.Limit) != entries[0] {
				writeError(writer, requestInvalid("body impact limit is invalid"))
				return
			}
		} else {
			query.AfterID, err = parseID(entries[0])
			if err != nil {
				writeError(writer, err)
				return
			}
		}
	}
	if !handler.available(writer) {
		return
	}
	reader, ok := handler.notes.(app.SynthesisBodyImpactReader)
	if !ok || nilDependency(reader) {
		writeProblem(writer, 503, "SYNTHESIS_HTTP_UNAVAILABLE", "主笔记引用提醒暂不可用", true)
		return
	}
	ctx, cancel := context.WithTimeout(request.Context(), handler.timeout)
	defer cancel()
	result, err := reader.ReadSynthesisBodyImpacts(ctx, query)
	if err != nil {
		writeError(writer, err)
		return
	}
	if len(result.Items) > query.Limit {
		writeError(writer, synthesisInvalidResult())
		return
	}
	items := make([]synthesisBodyImpactResponse, len(result.Items))
	previous := query.AfterID
	seen := map[string]bool{}
	for i, item := range result.Items {
		key := string(item.ItemID) + ":" + string(item.PublicationID)
		if item.WorkspaceID != query.WorkspaceID || item.NoteID != query.NoteID || item.BaseRevisionID != query.BaseRevisionID || item.ID <= previous || seen[key] || item.UpstreamNoteID == query.NoteID || item.UpstreamRevisionID == item.PublishedRevisionID || item.UpstreamPublicationID == item.PublicationID || item.DetectedAt.IsZero() || (item.Reason != "CONTENT_CHANGED" && item.Reason != "ITEM_MISSING") {
			writeError(writer, synthesisInvalidResult())
			return
		}
		for _, id := range []foundation.ID{item.ID, item.ItemID, item.UpstreamNoteID, item.UpstreamRevisionID, item.UpstreamItemID, item.UpstreamPublicationID, item.PublicationID, item.PublishedRevisionID, item.EventID} {
			if !validResponseID(id) {
				writeError(writer, synthesisInvalidResult())
				return
			}
		}
		previous = item.ID
		seen[key] = true
		items[i] = synthesisBodyImpactResponse{item.ID, item.ItemID, item.UpstreamNoteID, item.UpstreamRevisionID, item.UpstreamItemID, item.UpstreamPublicationID, item.PublicationID, item.PublishedRevisionID, item.Reason, item.DetectedAt}
	}
	if result.NextAfterID != "" && (len(items) != query.Limit || result.NextAfterID != previous) {
		writeError(writer, synthesisInvalidResult())
		return
	}
	writeJSON(writer, http.StatusOK, struct {
		WorkspaceID foundation.ID                 `json:"workspace_id"`
		NoteID      foundation.ID                 `json:"note_id"`
		RevisionID  foundation.ID                 `json:"revision_id"`
		Items       []synthesisBodyImpactResponse `json:"items"`
		NextAfterID *string                       `json:"next_after_id"`
	}{query.WorkspaceID, query.NoteID, query.BaseRevisionID, items, optionalID(result.NextAfterID)})
}
