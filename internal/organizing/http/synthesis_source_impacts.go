package organizinghttp

import (
	"context"
	"net/http"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	app "github.com/CodeZen-Lizhi/zhixu/internal/organizing/application"
	"github.com/CodeZen-Lizhi/zhixu/internal/organizing/domain"
)

func (handler *SynthesisHandler) sourceImpacts(writer http.ResponseWriter, request *http.Request) {
	query := app.SynthesisSourceImpactQuery{}
	for name, target := range map[string]*foundation.ID{"workspace_id": &query.WorkspaceID, "note_id": &query.NoteID, "revision_id": &query.RevisionID} {
		id, err := parseID(request.PathValue(name))
		if err != nil {
			writeError(writer, err)
			return
		}
		*target = id
	}
	if request.URL.RawQuery != "" {
		writeError(writer, requestInvalid("source impact query is invalid"))
		return
	}
	if !handler.available(writer) {
		return
	}
	reader, ok := handler.notes.(app.SynthesisSourceImpactReader)
	if !ok || nilDependency(reader) {
		writeProblem(writer, 503, "SYNTHESIS_HTTP_UNAVAILABLE", "来源复核提醒暂不可用", true)
		return
	}
	ctx, cancel := context.WithTimeout(request.Context(), handler.timeout)
	defer cancel()
	result, err := reader.ReadSynthesisSourceImpacts(ctx, query)
	if err != nil {
		writeError(writer, err)
		return
	}
	if result.WorkspaceID != query.WorkspaceID || result.NoteID != query.NoteID || result.RevisionID != query.RevisionID || result.Items == nil || len(result.Items) > domain.MaxSynthesisSources*2 {
		writeError(writer, synthesisInvalidResult())
		return
	}
	seen := map[string]bool{}
	for _, item := range result.Items {
		key := string(item.ID) + ":" + string(item.Reference.SourceSpanID)
		if !validResponseID(item.ID) || item.DetectedAt.IsZero() || (item.Reason != "SOURCE_REMOVED" && item.Reason != "SOURCE_QUARANTINED") || item.Reference.Validate() != nil || item.Reference.Source.WorkspaceID != query.WorkspaceID || len(item.ItemIDs) < 1 || len(item.ItemIDs) > domain.MaxSynthesisItems || seen[key] {
			writeError(writer, synthesisInvalidResult())
			return
		}
		seen[key] = true
		ids := map[foundation.ID]bool{}
		for _, id := range item.ItemIDs {
			if !validResponseID(id) || ids[id] {
				writeError(writer, synthesisInvalidResult())
				return
			}
			ids[id] = true
		}
	}
	writeJSON(writer, http.StatusOK, result)
}
