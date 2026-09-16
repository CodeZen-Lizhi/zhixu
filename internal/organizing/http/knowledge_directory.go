package organizinghttp

import (
	"context"
	"net/http"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	app "github.com/CodeZen-Lizhi/zhixu/internal/organizing/application"
	"github.com/CodeZen-Lizhi/zhixu/internal/organizing/domain"
)

func (handler *SynthesisHandler) directoryAvailable(writer http.ResponseWriter) bool {
	if handler == nil || nilDependency(handler.directory) {
		writeProblem(writer, http.StatusServiceUnavailable, "KNOWLEDGE_DIRECTORY_UNAVAILABLE", "知识目录暂不可用", true)
		return false
	}
	return true
}

func (handler *SynthesisHandler) sourceKnowledgeDirectory(writer http.ResponseWriter, request *http.Request) {
	workspaceID, sourceVersionID, ok := queryRoute(writer, request, "source_version_id")
	if !ok || !handler.directoryAvailable(writer) {
		return
	}
	ctx, cancel := context.WithTimeout(request.Context(), handler.timeout)
	defer cancel()
	directory, err := handler.directory.GetSourceKnowledgeDirectory(ctx, app.SourceKnowledgeDirectoryQuery{WorkspaceID: workspaceID, SourceVersionID: sourceVersionID})
	if err != nil {
		writeError(writer, err)
		return
	}
	if directory.WorkspaceID != workspaceID || directory.SourceVersionID != sourceVersionID {
		writeError(writer, synthesisInvalidResult())
		return
	}
	writeJSON(writer, http.StatusOK, directory)
}

// 调用方选择已保存的修订和片段，不能提供替换的来源元组。
// 元数据与不可变正文证据分开读取。
func (handler *SynthesisHandler) sourceKnowledgePoints(writer http.ResponseWriter, request *http.Request) {
	spanID, err := parseID(request.PathValue("source_span_id"))
	if err != nil {
		writeError(writer, err)
		return
	}
	if !handler.directoryAvailable(writer) {
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
			if source.SourceSpanID != spanID {
				continue
			}
			if reference != nil && !sameSynthesisSource(*reference, source) {
				writeError(writer, synthesisInvalidResult())
				return
			}
			value := source
			reference = &value
		}
	}
	if reference == nil {
		writeProblem(writer, http.StatusNotFound, "SYNTHESIS_SOURCE_NOT_FOUND", "请求的笔记来源不存在", false)
		return
	}
	bindings, ok := handler.notes.(app.SynthesisKnowledgeBindingReader)
	snapshots, hasSnapshots := handler.directory.(app.SynthesisKnowledgeSnapshotReader)
	if !ok || !hasSnapshots || nilDependency(bindings) || nilDependency(snapshots) {
		writeProblem(writer, http.StatusServiceUnavailable, "KNOWLEDGE_DIRECTORY_UNAVAILABLE", "历史知识目录暂不可用", true)
		return
	}
	profileRevisionID, err := bindings.ReadSynthesisKnowledgeBinding(ctx, app.SynthesisKnowledgeBindingQuery{NoteID: revision.NoteID, RevisionID: revision.ID, Reference: *reference})
	if err != nil {
		writeError(writer, err)
		return
	}
	projection, err := snapshots.ProjectSynthesisKnowledgeSnapshot(ctx, *reference, profileRevisionID)
	if err != nil {
		writeError(writer, err)
		return
	}
	if projection.Reference != *reference || projection.Directory.WorkspaceID != revision.WorkspaceID || projection.Directory.SourceVersionID != reference.Source.SourceVersionID || projection.Directory.ProfileRevisionID != profileRevisionID {
		writeError(writer, synthesisInvalidResult())
		return
	}
	writeJSON(writer, http.StatusOK, struct {
		WorkspaceID foundation.ID `json:"workspace_id"`
		NoteID      foundation.ID `json:"note_id"`
		RevisionID  foundation.ID `json:"revision_id"`
		app.SynthesisKnowledgePointProjection
	}{revision.WorkspaceID, revision.NoteID, revision.ID, projection})
}
