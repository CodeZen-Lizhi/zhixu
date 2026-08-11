// Package artifacthttp exposes the strict public HTTP contract for Artifact.
package artifacthttp

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"net/url"
	"reflect"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	artifactapp "github.com/CodeZen-Lizhi/zhixu/internal/artifact/application"
	"github.com/CodeZen-Lizhi/zhixu/internal/artifact/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation/strictjson"
	"github.com/CodeZen-Lizhi/zhixu/internal/httpapi"
	"github.com/gin-gonic/gin"
)

const (
	defaultListLimit = 50
	maxListLimit     = 100
	maxBodyBytes     = 128 * 1024
	maxKeyBytes      = 128
	maxCursorBytes   = 1024

	errorCodeUnavailable                     = "ARTIFACT_HTTP_UNAVAILABLE"
	errorCodeGenerationCapabilityUnavailable = "ARTIFACT_GENERATION_CAPABILITY_UNAVAILABLE"
	errorCodeResultInvalid                   = "ARTIFACT_HTTP_RESULT_INCONSISTENT"
	errorCodeCursorInvalid                   = "ARTIFACT_CURSOR_INVALID"
)

// CommandService is the Artifact write contract required at the HTTP boundary.
type CommandService interface {
	Plan(context.Context, artifactapp.PlanCommand) (artifactapp.CommandResult, error)
	SubmitOutline(context.Context, artifactapp.SubmitOutlinePersistentCommand) (artifactapp.CommandResult, error)
	ApproveOutline(context.Context, artifactapp.RevisionPersistentCommand) (artifactapp.CommandResult, error)
	StartRevision(context.Context, artifactapp.RevisionPersistentCommand) (artifactapp.CommandResult, error)
	RecordSection(context.Context, artifactapp.RecordSectionPersistentCommand) (artifactapp.CommandResult, error)
	ApproveDraft(context.Context, artifactapp.RevisionPersistentCommand) (artifactapp.CommandResult, error)
	ExportMarkdown(context.Context, artifactapp.RevisionPersistentCommand) (artifactapp.CommandResult, error)
	Publish(context.Context, artifactapp.RevisionPersistentCommand) (artifactapp.CommandResult, error)
}

// QueryService is the Artifact read contract required at the HTTP boundary.
type QueryService interface {
	Get(context.Context, foundation.ID, foundation.ID) (artifactapp.State, error)
	List(context.Context, artifactapp.ListQuery) (artifactapp.ArtifactPage, error)
	ListSectionGenerations(context.Context, foundation.ID, foundation.ID) (artifactapp.SectionGenerationList, error)
	GetExport(context.Context, foundation.ID, foundation.ID, foundation.ID) (artifactapp.ExportRecord, error)
}

// Handler maps Artifact application services to strict workspace-scoped routes.
type Handler struct {
	commands   CommandService
	queries    QueryService
	generation artifactapp.SectionGenerationStarter
	timeout    time.Duration
}

// NewHandler constructs an Artifact handler. Missing dependencies fail closed.
func NewHandler(commands CommandService, queries QueryService, timeout time.Duration, generation ...artifactapp.SectionGenerationStarter) *Handler {
	if timeout <= 0 {
		timeout = 2 * time.Second
	}
	handler := &Handler{commands: commands, queries: queries, timeout: timeout}
	if len(generation) == 1 && !nilDependency(generation[0]) {
		handler.generation = generation[0]
	}
	return handler
}

// Available reports whether both read and write Artifact services are present.
func (handler *Handler) Available() bool {
	return handler != nil && !nilDependency(handler.commands) && !nilDependency(handler.queries)
}

// Routes registers Artifact routes beneath the caller's /api/v1 router.
func (handler *Handler) Routes(router gin.IRouter) {
	router.GET("/artifacts", httpapi.GinHandler(handler.list))
	router.POST("/artifacts", httpapi.GinHandler(handler.plan))
	router.GET("/artifacts/:artifact_id", httpapi.GinHandler(handler.get))
	router.POST("/artifacts/:artifact_id/outline", httpapi.GinHandler(handler.submitOutline))
	router.POST("/artifacts/:artifact_id/outline/approve", httpapi.GinHandler(handler.approveOutline))
	router.POST("/artifacts/:artifact_id/revisions", httpapi.GinHandler(handler.startRevision))
	router.POST("/artifacts/:artifact_id/sections", httpapi.GinHandler(handler.recordSection))
	router.POST("/artifacts/:artifact_id/sections/generate", httpapi.GinHandler(handler.generateSection))
	router.GET("/artifacts/:artifact_id/section-generations", httpapi.GinHandler(handler.listSectionGenerations))
	router.POST("/artifacts/:artifact_id/draft/approve", httpapi.GinHandler(handler.approveDraft))
	router.POST("/artifacts/:artifact_id/exports/markdown", httpapi.GinHandler(handler.exportMarkdown))
	router.GET("/artifacts/:artifact_id/exports/:export_id", httpapi.GinHandler(handler.getExport))
	router.POST("/artifacts/:artifact_id/publish-proposals", httpapi.GinHandler(handler.publish))
}

func (handler *Handler) list(w http.ResponseWriter, r *http.Request) {
	if !handler.available(w) {
		return
	}
	query, err := parseQuery(r, "workspace_id", "limit", "cursor")
	if err != nil {
		writeError(w, err)
		return
	}
	workspaceID, err := parseID(single(query, "workspace_id"))
	if err != nil {
		writeError(w, err)
		return
	}
	limit, err := parseLimit(singleDefault(query, "limit", strconv.Itoa(defaultListLimit)))
	if err != nil {
		writeError(w, err)
		return
	}
	after, err := decodeCursor(single(query, "cursor"), workspaceID)
	if err != nil {
		writeError(w, err)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), handler.timeout)
	defer cancel()
	page, err := handler.queries.List(ctx, artifactapp.ListQuery{WorkspaceID: workspaceID, Limit: limit, After: after})
	if err != nil {
		writeError(w, err)
		return
	}
	if len(page.Items) > limit {
		writeError(w, resultInvalid("artifact page exceeds requested limit"))
		return
	}
	items := make([]artifactResponse, 0, len(page.Items))
	for _, state := range page.Items {
		if err := validateState(state, workspaceID, ""); err != nil {
			writeError(w, err)
			return
		}
		items = append(items, toArtifactResponse(state))
	}
	next, err := encodeCursor(workspaceID, page.Next)
	if err != nil {
		writeError(w, err)
		return
	}
	httpapi.WriteJSON(w, http.StatusOK, artifactListResponse{WorkspaceID: string(workspaceID), Items: items, NextCursor: next})
}

func (handler *Handler) get(w http.ResponseWriter, r *http.Request) {
	if !handler.available(w) {
		return
	}
	workspaceID, artifactID, err := parsePathWorkspaceArtifact(r)
	if err != nil {
		writeError(w, err)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), handler.timeout)
	defer cancel()
	state, err := handler.queries.Get(ctx, workspaceID, artifactID)
	if err != nil {
		writeError(w, err)
		return
	}
	if err := validateState(state, workspaceID, artifactID); err != nil {
		writeError(w, err)
		return
	}
	httpapi.WriteJSON(w, http.StatusOK, toArtifactResponse(state))
}

func (handler *Handler) plan(w http.ResponseWriter, r *http.Request) {
	if err := rejectQuery(r); err != nil {
		writeError(w, err)
		return
	}
	key, err := parseIdempotencyKey(r)
	if err != nil {
		writeError(w, err)
		return
	}
	wire, err := decodeJSON[planRequest](r)
	if err != nil {
		writeError(w, err)
		return
	}
	workspaceID, err := parseID(wire.WorkspaceID)
	if err != nil {
		writeError(w, err)
		return
	}
	if !handler.available(w) {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), handler.timeout)
	defer cancel()
	result, err := handler.commands.Plan(ctx, artifactapp.PlanCommand{WorkspaceID: workspaceID, Type: wire.Type, Title: wire.Title, ScopeDefinition: wire.ScopeDefinition, IdempotencyKey: key})
	if err != nil {
		writeError(w, err)
		return
	}
	handler.writeCommand(w, result, workspaceID, "", artifactapp.CommandPlan, http.StatusCreated)
}

func (handler *Handler) submitOutline(w http.ResponseWriter, r *http.Request) {
	artifactID, key, ok := handler.mutationTarget(w, r)
	if !ok {
		return
	}
	value, err := decodeJSON[outlineRequest](r)
	if err != nil {
		writeError(w, err)
		return
	}
	workspaceID, err := parseID(value.WorkspaceID)
	if err != nil {
		writeError(w, err)
		return
	}
	if value.ExpectedVersion < 1 {
		writeError(w, requestInvalid("expected_version is invalid"))
		return
	}
	if !handler.available(w) {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), handler.timeout)
	defer cancel()
	result, err := handler.commands.SubmitOutline(ctx, artifactapp.SubmitOutlinePersistentCommand{WorkspaceID: workspaceID, ArtifactID: artifactID, ExpectedVersion: value.ExpectedVersion, Outline: toOutline(value.Outline), IdempotencyKey: key})
	if err != nil {
		writeError(w, err)
		return
	}
	handler.writeCommand(w, result, workspaceID, artifactID, artifactapp.CommandSubmitOutline, http.StatusOK)
}

func (handler *Handler) approveOutline(w http.ResponseWriter, r *http.Request) {
	handler.revisionCommand(w, r, artifactapp.CommandApproveOutline, func(ctx context.Context, command artifactapp.RevisionPersistentCommand) (artifactapp.CommandResult, error) {
		return handler.commands.ApproveOutline(ctx, command)
	})
}
func (handler *Handler) startRevision(w http.ResponseWriter, r *http.Request) {
	handler.revisionCommand(w, r, artifactapp.CommandStartRevision, func(ctx context.Context, command artifactapp.RevisionPersistentCommand) (artifactapp.CommandResult, error) {
		return handler.commands.StartRevision(ctx, command)
	})
}
func (handler *Handler) approveDraft(w http.ResponseWriter, r *http.Request) {
	handler.revisionCommand(w, r, artifactapp.CommandApproveDraft, func(ctx context.Context, command artifactapp.RevisionPersistentCommand) (artifactapp.CommandResult, error) {
		return handler.commands.ApproveDraft(ctx, command)
	})
}
func (handler *Handler) exportMarkdown(w http.ResponseWriter, r *http.Request) {
	handler.revisionCommand(w, r, artifactapp.CommandExportMarkdown, func(ctx context.Context, command artifactapp.RevisionPersistentCommand) (artifactapp.CommandResult, error) {
		return handler.commands.ExportMarkdown(ctx, command)
	})
}
func (handler *Handler) publish(w http.ResponseWriter, r *http.Request) {
	handler.revisionCommand(w, r, artifactapp.CommandPublish, func(ctx context.Context, command artifactapp.RevisionPersistentCommand) (artifactapp.CommandResult, error) {
		return handler.commands.Publish(ctx, command)
	})
}

func (handler *Handler) revisionCommand(w http.ResponseWriter, r *http.Request, commandType artifactapp.CommandType, execute func(context.Context, artifactapp.RevisionPersistentCommand) (artifactapp.CommandResult, error)) {
	artifactID, key, ok := handler.mutationTarget(w, r)
	if !ok {
		return
	}
	value, err := decodeJSON[revisionRequest](r)
	if err != nil {
		writeError(w, err)
		return
	}
	workspaceID, err := parseID(value.WorkspaceID)
	if err != nil {
		writeError(w, err)
		return
	}
	if value.ExpectedVersion < 1 {
		writeError(w, requestInvalid("expected_version is invalid"))
		return
	}
	if !handler.available(w) {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), handler.timeout)
	defer cancel()
	result, err := execute(ctx, artifactapp.RevisionPersistentCommand{WorkspaceID: workspaceID, ArtifactID: artifactID, ExpectedVersion: value.ExpectedVersion, IdempotencyKey: key})
	if err != nil {
		writeError(w, err)
		return
	}
	handler.writeCommand(w, result, workspaceID, artifactID, commandType, http.StatusOK)
}

func (handler *Handler) recordSection(w http.ResponseWriter, r *http.Request) {
	if err := rejectQuery(r); err != nil {
		writeError(w, err)
		return
	}
	key, err := parseIdempotencyKey(r)
	if err != nil {
		writeError(w, err)
		return
	}
	artifactID, err := parseID(r.PathValue("artifact_id"))
	if err != nil {
		writeError(w, err)
		return
	}
	wire, err := decodeJSON[sectionRequest](r)
	if err != nil {
		writeError(w, err)
		return
	}
	workspaceID, err := parseID(wire.WorkspaceID)
	if err != nil {
		writeError(w, err)
		return
	}
	if wire.ExpectedVersion < 1 {
		writeError(w, requestInvalid("expected_version is invalid"))
		return
	}
	if wire.Section.Citations == nil || wire.Section.Coverage.Gaps == nil {
		writeError(w, requestInvalid("section citations and coverage gaps must be explicit"))
		return
	}
	if !handler.available(w) {
		return
	}
	citations := make([]artifactapp.CitationInput, len(wire.Section.Citations))
	for index, citation := range wire.Section.Citations {
		indexVersionID, indexErr := parseID(citation.IndexVersionID)
		chunkID, chunkErr := parseID(citation.ChunkID)
		sourceVersionID, versionErr := parseID(citation.SourceVersionID)
		sourceSpanID, spanErr := parseID(citation.SourceSpanID)
		if indexErr != nil || chunkErr != nil || versionErr != nil || spanErr != nil {
			writeError(w, requestInvalid("section citation identity is invalid"))
			return
		}
		citations[index] = artifactapp.CitationInput{IndexVersionID: indexVersionID, ChunkID: chunkID, SourceVersionID: sourceVersionID, SourceSpanID: sourceSpanID}
	}
	section := artifactapp.SectionInput{Key: wire.Section.Key, Title: wire.Section.Title, Content: wire.Section.Content, Citations: citations, Coverage: toCoverage(wire.Section.Coverage)}
	ctx, cancel := context.WithTimeout(r.Context(), handler.timeout)
	defer cancel()
	result, err := handler.commands.RecordSection(ctx, artifactapp.RecordSectionPersistentCommand{RevisionPersistentCommand: artifactapp.RevisionPersistentCommand{WorkspaceID: workspaceID, ArtifactID: artifactID, ExpectedVersion: wire.ExpectedVersion, IdempotencyKey: key}, Section: section, Creator: domain.CreatorHuman})
	if err != nil {
		writeError(w, err)
		return
	}
	handler.writeCommand(w, result, workspaceID, artifactID, artifactapp.CommandRecordSection, http.StatusOK)
}

func (handler *Handler) generateSection(w http.ResponseWriter, r *http.Request) {
	if err := rejectQuery(r); err != nil {
		writeError(w, err)
		return
	}
	key, err := parseIdempotencyKey(r)
	if err != nil {
		writeError(w, err)
		return
	}
	artifactID, err := parseID(r.PathValue("artifact_id"))
	if err != nil {
		writeError(w, err)
		return
	}
	wire, err := decodeJSON[sectionGenerationRequest](r)
	if err != nil {
		writeError(w, err)
		return
	}
	workspaceID, err := parseID(wire.WorkspaceID)
	if err != nil {
		writeError(w, err)
		return
	}
	command := artifactapp.StartSectionGenerationCommand{
		WorkspaceID: workspaceID, ArtifactID: artifactID, ExpectedVersion: wire.ExpectedVersion,
		SectionKey: wire.SectionKey, IdempotencyKey: key,
	}
	if err := artifactapp.ValidateStartSectionGenerationCommand(command); err != nil {
		writeError(w, err)
		return
	}
	if !handler.generationAvailable(w) {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), handler.timeout)
	defer cancel()
	result, err := handler.generation.StartSectionGeneration(ctx, command)
	if err != nil {
		writeGenerationError(w, err)
		return
	}
	if err := validateSectionGenerationResult(result, command); err != nil {
		writeError(w, err)
		return
	}
	httpapi.WriteJSON(w, http.StatusAccepted, toSectionGenerationResponse(result))
}

func (handler *Handler) listSectionGenerations(w http.ResponseWriter, r *http.Request) {
	if !handler.available(w) {
		return
	}
	workspaceID, artifactID, err := parsePathWorkspaceArtifact(r)
	if err != nil {
		writeError(w, err)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), handler.timeout)
	defer cancel()
	result, err := handler.queries.ListSectionGenerations(ctx, workspaceID, artifactID)
	if err != nil {
		writeError(w, err)
		return
	}
	if artifactapp.ValidateSectionGenerationList(result) != nil ||
		result.WorkspaceID != workspaceID || result.ArtifactID != artifactID {
		writeError(w, resultInvalid("artifact section generation list crossed request binding"))
		return
	}
	items := make([]sectionGenerationItemResponse, len(result.Items))
	for index, generation := range result.Items {
		items[index] = toSectionGenerationItemResponse(generation)
	}
	httpapi.WriteJSON(w, http.StatusOK, sectionGenerationListResponse{
		WorkspaceID: string(workspaceID), ArtifactID: string(artifactID), Items: items,
	})
}

func (handler *Handler) getExport(w http.ResponseWriter, r *http.Request) {
	if !handler.available(w) {
		return
	}
	workspaceID, artifactID, err := parsePathWorkspaceArtifact(r)
	if err != nil {
		writeError(w, err)
		return
	}
	exportID, err := parseID(r.PathValue("export_id"))
	if err != nil {
		writeError(w, err)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), handler.timeout)
	defer cancel()
	record, err := handler.queries.GetExport(ctx, workspaceID, artifactID, exportID)
	if err != nil {
		writeError(w, err)
		return
	}
	if err := validateExport(record, workspaceID, artifactID, exportID); err != nil {
		writeError(w, err)
		return
	}
	httpapi.WriteJSON(w, http.StatusOK, toExportResponse(record))
}

// mutationTarget centralizes non-body mutation preconditions before service invocation.
func (handler *Handler) mutationTarget(w http.ResponseWriter, r *http.Request) (foundation.ID, string, bool) {
	if err := rejectQuery(r); err != nil {
		writeError(w, err)
		return "", "", false
	}
	key, err := parseIdempotencyKey(r)
	if err != nil {
		writeError(w, err)
		return "", "", false
	}
	artifactID, err := parseID(r.PathValue("artifact_id"))
	if err != nil {
		writeError(w, err)
		return "", "", false
	}
	return artifactID, key, true
}

func (handler *Handler) writeCommand(w http.ResponseWriter, result artifactapp.CommandResult, workspaceID, artifactID foundation.ID, commandType artifactapp.CommandType, firstStatus int) {
	if err := validateCommand(result, workspaceID, artifactID, commandType); err != nil {
		writeError(w, err)
		return
	}
	status := firstStatus
	if result.Replayed {
		status = http.StatusOK
	}
	httpapi.WriteJSON(w, status, commandResponse{Artifact: toArtifactResponse(result.State), Export: exportResponsePtr(result.Export), Publication: publicationResponsePtr(result.Publication), Replayed: result.Replayed})
}

type planRequest struct {
	WorkspaceID     string `json:"workspace_id"`
	Type            string `json:"type"`
	Title           string `json:"title"`
	ScopeDefinition string `json:"scope_definition"`
}
type outlineRequest struct {
	WorkspaceID     string                  `json:"workspace_id"`
	ExpectedVersion int64                   `json:"expected_version"`
	Outline         []outlineSectionRequest `json:"outline"`
}
type revisionRequest struct {
	WorkspaceID     string `json:"workspace_id"`
	ExpectedVersion int64  `json:"expected_version"`
}
type outlineSectionRequest struct {
	Key   string `json:"key"`
	Title string `json:"title"`
}
type citationRequest struct {
	IndexVersionID  string `json:"index_version_id"`
	ChunkID         string `json:"chunk_id"`
	SourceVersionID string `json:"source_version_id"`
	SourceSpanID    string `json:"source_span_id"`
}
type gapRequest struct {
	Code        string `json:"code"`
	Description string `json:"description"`
}
type coverageRequest struct {
	SectionKey string       `json:"section_key"`
	Status     string       `json:"status"`
	Gaps       []gapRequest `json:"gaps"`
}
type sectionPayloadRequest struct {
	Key       string            `json:"key"`
	Title     string            `json:"title"`
	Content   string            `json:"content"`
	Citations []citationRequest `json:"citations"`
	Coverage  coverageRequest   `json:"coverage"`
}
type metadataRequest struct {
	PromptVersion             string `json:"prompt_version"`
	ModelVersion              string `json:"model_version"`
	WorkflowDefinitionVersion string `json:"workflow_definition_version"`
	SchemaVersion             string `json:"schema_version"`
}
type sectionRequest struct {
	WorkspaceID     string                `json:"workspace_id"`
	ExpectedVersion int64                 `json:"expected_version"`
	Section         sectionPayloadRequest `json:"section"`
}
type sectionGenerationRequest struct {
	WorkspaceID     string `json:"workspace_id"`
	ExpectedVersion int64  `json:"expected_version"`
	SectionKey      string `json:"section_key"`
}

type artifactListResponse struct {
	WorkspaceID string             `json:"workspace_id"`
	Items       []artifactResponse `json:"items"`
	NextCursor  string             `json:"next_cursor,omitempty"`
}
type commandResponse struct {
	Artifact    artifactResponse     `json:"artifact"`
	Export      *exportResponse      `json:"export,omitempty"`
	Publication *publicationResponse `json:"publication,omitempty"`
	Replayed    bool                 `json:"replayed"`
}
type sectionGenerationItemResponse struct {
	GenerationID          string `json:"generation_id"`
	WorkspaceID           string `json:"workspace_id"`
	ArtifactID            string `json:"artifact_id"`
	SourceRevisionID      string `json:"source_revision_id"`
	SourceRevisionNo      int64  `json:"source_revision_no"`
	SourceArtifactVersion int64  `json:"source_artifact_version"`
	SectionKey            string `json:"section_key"`
	WorkflowRunID         string `json:"workflow_run_id"`
	NodeRunID             string `json:"node_run_id"`
	Status                string `json:"status"`
	Version               int64  `json:"version"`
	CreatedAt             string `json:"created_at"`
	UpdatedAt             string `json:"updated_at"`
	StatusURL             string `json:"status_url"`
}
type sectionGenerationResponse struct {
	sectionGenerationItemResponse
	Replayed bool `json:"replayed"`
}
type sectionGenerationListResponse struct {
	WorkspaceID string                          `json:"workspace_id"`
	ArtifactID  string                          `json:"artifact_id"`
	Items       []sectionGenerationItemResponse `json:"items"`
}
type artifactResponse struct {
	ID                string             `json:"id"`
	WorkspaceID       string             `json:"workspace_id"`
	Type              string             `json:"type"`
	Title             string             `json:"title"`
	Status            string             `json:"status"`
	ScopeDefinition   string             `json:"scope_definition"`
	SourceCoverage    []coverageResponse `json:"source_coverage"`
	CurrentRevisionID string             `json:"current_revision_id"`
	Version           int64              `json:"version"`
	CreatedAt         string             `json:"created_at"`
	UpdatedAt         string             `json:"updated_at"`
	Revision          revisionResponse   `json:"revision"`
}
type revisionResponse struct {
	ID          string                  `json:"id"`
	ArtifactID  string                  `json:"artifact_id"`
	RevisionNo  int64                   `json:"revision_no"`
	Outline     []outlineSectionRequest `json:"outline"`
	Sections    []sectionResponse       `json:"sections"`
	CreatedBy   string                  `json:"created_by"`
	Metadata    *metadataRequest        `json:"metadata,omitempty"`
	ContentHash string                  `json:"content_hash"`
	CreatedAt   string                  `json:"created_at"`
}
type sectionResponse struct {
	Key             string                   `json:"key"`
	Title           string                   `json:"title"`
	Content         string                   `json:"content"`
	Citations       []citationResponse       `json:"citations"`
	DocumentSources []documentSourceResponse `json:"document_sources"`
	Coverage        coverageResponse         `json:"coverage"`
}
type citationResponse struct {
	SourceVersionID     string `json:"source_version_id"`
	SourceSpanID        string `json:"source_span_id"`
	VerifiedContentHash string `json:"verified_content_hash"`
	Excerpt             string `json:"excerpt"`
	Verified            bool   `json:"verified"`
}
type documentSourceResponse struct {
	DocumentID          string `json:"document_id"`
	ArticleRevisionID   string `json:"article_revision_id"`
	RevisionNo          int64  `json:"revision_no"`
	VerifiedContentHash string `json:"verified_content_hash"`
	Verified            bool   `json:"verified"`
}
type coverageResponse struct {
	SectionKey string       `json:"section_key"`
	Status     string       `json:"status"`
	Gaps       []gapRequest `json:"gaps"`
}
type exportResponse struct {
	ID              string `json:"id"`
	WorkspaceID     string `json:"workspace_id"`
	ArtifactID      string `json:"artifact_id"`
	RevisionID      string `json:"revision_id"`
	ArtifactVersion int64  `json:"artifact_version"`
	RevisionNo      int64  `json:"revision_no"`
	RevisionHash    string `json:"revision_hash"`
	OutputHash      string `json:"output_hash"`
	OutputSize      int64  `json:"output_size"`
	ExportedAt      string `json:"exported_at"`
}
type publicationResponse struct {
	WorkspaceID     string `json:"workspace_id"`
	ArtifactID      string `json:"artifact_id"`
	RevisionID      string `json:"revision_id"`
	ArtifactVersion int64  `json:"artifact_version"`
	RevisionNo      int64  `json:"revision_no"`
	ContentHash     string `json:"content_hash"`
	ProposalID      string `json:"proposal_id"`
	CreatedAt       string `json:"created_at"`
}

func toOutline(value []outlineSectionRequest) []domain.OutlineSection {
	result := make([]domain.OutlineSection, len(value))
	for i := range value {
		result[i] = domain.OutlineSection{Key: value[i].Key, Title: value[i].Title}
	}
	return result
}
func toSectionGenerationResponse(result artifactapp.StartSectionGenerationResult) sectionGenerationResponse {
	return sectionGenerationResponse{sectionGenerationItemResponse: toSectionGenerationItemResponse(result.Generation), Replayed: result.Replayed}
}
func toSectionGenerationItemResponse(generation artifactapp.SectionGeneration) sectionGenerationItemResponse {
	return sectionGenerationItemResponse{
		GenerationID: string(generation.ID), WorkspaceID: string(generation.WorkspaceID), ArtifactID: string(generation.ArtifactID),
		SourceRevisionID: string(generation.SourceRevisionID), SourceRevisionNo: generation.SourceRevisionNo,
		SourceArtifactVersion: generation.SourceArtifactVersion, SectionKey: generation.SectionKey,
		WorkflowRunID: string(generation.WorkflowRunID), NodeRunID: string(generation.NodeRunID), Status: string(generation.Status),
		Version: generation.Version, CreatedAt: formatTime(generation.CreatedAt), UpdatedAt: formatTime(generation.UpdatedAt),
		StatusURL: "/api/v1/workflows/" + string(generation.WorkflowRunID),
	}
}
func toCoverage(value coverageRequest) domain.Coverage {
	gaps := make([]domain.Gap, len(value.Gaps))
	for i := range value.Gaps {
		gaps[i] = domain.Gap{Code: value.Gaps[i].Code, Description: value.Gaps[i].Description}
	}
	return domain.Coverage{SectionKey: value.SectionKey, Status: domain.CoverageStatus(value.Status), Gaps: gaps}
}
func toArtifactResponse(state artifactapp.State) artifactResponse {
	artifact, revision := state.Artifact, state.Revision
	outline := make([]outlineSectionRequest, len(revision.Outline))
	for i := range revision.Outline {
		outline[i] = outlineSectionRequest{Key: revision.Outline[i].Key, Title: revision.Outline[i].Title}
	}
	sections := make([]sectionResponse, len(revision.Sections))
	for i := range revision.Sections {
		section := revision.Sections[i]
		citations := make([]citationResponse, len(section.Citations))
		for j := range section.Citations {
			citation := section.Citations[j]
			citations[j] = citationResponse{SourceVersionID: string(citation.SourceVersionID), SourceSpanID: string(citation.SourceSpanID), VerifiedContentHash: citation.VerifiedContentHash, Excerpt: citation.Excerpt, Verified: citation.Verified}
		}
		documentSources := make([]documentSourceResponse, len(section.DocumentSources))
		for j := range section.DocumentSources {
			source := section.DocumentSources[j]
			documentSources[j] = documentSourceResponse{DocumentID: string(source.DocumentID), ArticleRevisionID: string(source.ArticleRevisionID), RevisionNo: source.RevisionNo, VerifiedContentHash: source.VerifiedContentHash, Verified: source.Verified}
		}
		sections[i] = sectionResponse{Key: section.Key, Title: section.Title, Content: section.Content, Citations: citations, DocumentSources: documentSources, Coverage: toCoverageResponse(section.Coverage)}
	}
	return artifactResponse{ID: string(artifact.ID), WorkspaceID: string(artifact.WorkspaceID), Type: artifact.Type, Title: artifact.Title, Status: string(artifact.Status), ScopeDefinition: artifact.ScopeDefinition, SourceCoverage: coverageResponses(artifact.SourceCoverage), CurrentRevisionID: string(artifact.CurrentRevisionID), Version: artifact.Version, CreatedAt: formatTime(artifact.CreatedAt), UpdatedAt: formatTime(artifact.UpdatedAt), Revision: revisionResponse{ID: string(revision.ID), ArtifactID: string(revision.ArtifactID), RevisionNo: revision.RevisionNo, Outline: outline, Sections: sections, CreatedBy: string(revision.CreatedBy), Metadata: metadataResponse(revision.Metadata), ContentHash: revision.ContentHash, CreatedAt: formatTime(revision.CreatedAt)}}
}
func coverageResponses(values []domain.Coverage) []coverageResponse {
	result := make([]coverageResponse, len(values))
	for i := range values {
		result[i] = toCoverageResponse(values[i])
	}
	return result
}
func toCoverageResponse(value domain.Coverage) coverageResponse {
	gaps := make([]gapRequest, len(value.Gaps))
	for i := range value.Gaps {
		gaps[i] = gapRequest{Code: value.Gaps[i].Code, Description: value.Gaps[i].Description}
	}
	return coverageResponse{SectionKey: value.SectionKey, Status: string(value.Status), Gaps: gaps}
}
func metadataResponse(value *domain.GenerationMetadata) *metadataRequest {
	if value == nil {
		return nil
	}
	return &metadataRequest{PromptVersion: value.PromptVersion, ModelVersion: value.ModelVersion, WorkflowDefinitionVersion: value.WorkflowDefinitionVersion, SchemaVersion: value.SchemaVersion}
}
func toExportResponse(value artifactapp.ExportRecord) exportResponse {
	return exportResponse{ID: string(value.ID), WorkspaceID: string(value.WorkspaceID), ArtifactID: string(value.ArtifactID), RevisionID: string(value.RevisionID), ArtifactVersion: value.ArtifactVersion, RevisionNo: value.RevisionNo, RevisionHash: value.RevisionHash, OutputHash: value.OutputHash, OutputSize: value.OutputSize, ExportedAt: formatTime(value.ExportedAt)}
}
func exportResponsePtr(value *artifactapp.ExportRecord) *exportResponse {
	if value == nil {
		return nil
	}
	response := toExportResponse(*value)
	return &response
}
func publicationResponsePtr(value *artifactapp.PublicationRecord) *publicationResponse {
	if value == nil {
		return nil
	}
	return &publicationResponse{WorkspaceID: string(value.WorkspaceID), ArtifactID: string(value.ArtifactID), RevisionID: string(value.RevisionID), ArtifactVersion: value.ArtifactVersion, RevisionNo: value.RevisionNo, ContentHash: value.ContentHash, ProposalID: string(value.ProposalID), CreatedAt: formatTime(value.CreatedAt)}
}

func validateState(state artifactapp.State, workspaceID, artifactID foundation.ID) error {
	if artifactapp.ValidateState(state) != nil || state.Artifact.WorkspaceID != workspaceID || (artifactID != "" && state.Artifact.ID != artifactID) {
		return resultInvalid("artifact response crossed request binding")
	}
	return nil
}
func validateCommand(result artifactapp.CommandResult, workspaceID, artifactID foundation.ID, commandType artifactapp.CommandType) error {
	if err := validateState(result.State, workspaceID, artifactID); err != nil || result.CommandType != commandType || result.CommandVersion != result.State.Artifact.Version {
		return resultInvalid("artifact command response is inconsistent")
	}
	if result.Export != nil {
		export := result.Export
		if validateExport(*export, workspaceID, result.State.Artifact.ID, "") != nil ||
			export.RevisionID != result.State.Revision.ID || export.ArtifactVersion != result.State.Artifact.Version ||
			export.RevisionNo != result.State.Revision.RevisionNo || export.RevisionHash != result.State.Revision.ContentHash {
			return resultInvalid("artifact export response is inconsistent")
		}
	}
	if result.Publication != nil {
		publication := result.Publication
		if publication.WorkspaceID != workspaceID || publication.ArtifactID != result.State.Artifact.ID || publication.RevisionID != result.State.Revision.ID ||
			publication.ArtifactVersion != result.State.Artifact.Version || publication.RevisionNo != result.State.Revision.RevisionNo || publication.ContentHash != result.State.Revision.ContentHash {
			return resultInvalid("artifact publication response is inconsistent")
		}
	}
	return nil
}
func validateSectionGenerationResult(result artifactapp.StartSectionGenerationResult, command artifactapp.StartSectionGenerationCommand) error {
	generation := result.Generation
	if artifactapp.ValidateSectionGeneration(generation) != nil || generation.WorkspaceID != command.WorkspaceID ||
		generation.ArtifactID != command.ArtifactID || generation.SourceArtifactVersion != command.ExpectedVersion ||
		generation.SectionKey != command.SectionKey || generation.IdempotencyKey != command.IdempotencyKey {
		return resultInvalid("artifact section generation response crossed request binding")
	}
	return nil
}
func validateExport(value artifactapp.ExportRecord, workspaceID, artifactID, exportID foundation.ID) error {
	if value.WorkspaceID != workspaceID || value.ArtifactID != artifactID || (exportID != "" && value.ID != exportID) || value.ID == "" || value.RevisionID == "" || value.RevisionHash == "" || value.OutputHash == "" || value.OutputSize < 0 || value.ExportedAt.IsZero() {
		return resultInvalid("artifact export response crossed request binding")
	}
	return nil
}

type artifactCursor struct {
	Version     int    `json:"version"`
	WorkspaceID string `json:"workspace_id"`
	UpdatedAt   string `json:"updated_at"`
	ID          string `json:"id"`
}

func encodeCursor(workspaceID foundation.ID, value *artifactapp.ArtifactCursor) (string, error) {
	if value == nil {
		return "", nil
	}
	if value.UpdatedAt.IsZero() || value.ID == "" {
		return "", cursorInvalid()
	}
	encoded, err := json.Marshal(artifactCursor{Version: 1, WorkspaceID: string(workspaceID), UpdatedAt: formatTime(value.UpdatedAt), ID: string(value.ID)})
	if err != nil {
		return "", cursorInvalid()
	}
	return base64.RawURLEncoding.EncodeToString(encoded), nil
}
func decodeCursor(raw string, workspaceID foundation.ID) (*artifactapp.ArtifactCursor, error) {
	if raw == "" {
		return nil, nil
	}
	decoded, err := base64.RawURLEncoding.DecodeString(raw)
	if err != nil || len(decoded) == 0 || len(decoded) > maxCursorBytes {
		return nil, cursorInvalid()
	}
	var cursor artifactCursor
	decoder := json.NewDecoder(strings.NewReader(string(decoded)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&cursor); err != nil {
		return nil, cursorInvalid()
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return nil, cursorInvalid()
	}
	at, err := time.Parse(time.RFC3339Nano, cursor.UpdatedAt)
	id, idErr := parseID(cursor.ID)
	if err != nil || idErr != nil || cursor.Version != 1 || cursor.WorkspaceID != string(workspaceID) {
		return nil, cursorInvalid()
	}
	return &artifactapp.ArtifactCursor{UpdatedAt: at.UTC(), ID: id}, nil
}

func decodeJSON[T any](r *http.Request) (T, error) {
	var zero T
	mediaType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || !strings.EqualFold(mediaType, "application/json") {
		return zero, foundation.NewError(foundation.ErrorInvalidInput, "UNSUPPORTED_MEDIA_TYPE", false, errors.New("request requires application/json"))
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, maxBodyBytes+1))
	if err != nil || len(body) == 0 || len(body) > maxBodyBytes {
		return zero, foundation.NewError(foundation.ErrorInvalidInput, "INVALID_JSON", false, errors.New("request JSON is empty or oversized"))
	}
	limits := strictjson.DefaultLimits()
	limits.MaxDocumentBytes = maxBodyBytes
	value, err := strictjson.DecodeObject[T](body, limits, nil)
	if err != nil {
		return zero, foundation.NewError(foundation.ErrorInvalidInput, "INVALID_JSON", false, err)
	}
	return value, nil
}
func parseIdempotencyKey(r *http.Request) (string, error) {
	values := r.Header.Values("Idempotency-Key")
	if len(values) != 1 || !utf8.ValidString(values[0]) {
		return "", requestInvalid("exactly one Idempotency-Key is required")
	}
	key := values[0]
	if key == "" || key != strings.TrimSpace(key) || len(key) > maxKeyBytes {
		return "", requestInvalid("Idempotency-Key is invalid")
	}
	for _, character := range key {
		if unicode.IsControl(character) {
			return "", requestInvalid("Idempotency-Key is invalid")
		}
	}
	return key, nil
}
func parseQuery(r *http.Request, allowed ...string) (url.Values, error) {
	values, err := url.ParseQuery(r.URL.RawQuery)
	if err != nil {
		return nil, requestInvalid("query parameters are invalid")
	}
	set := make(map[string]struct{}, len(allowed))
	for _, key := range allowed {
		set[key] = struct{}{}
	}
	for key, entries := range values {
		if _, ok := set[key]; !ok || len(entries) != 1 || entries[0] == "" || strings.TrimSpace(entries[0]) != entries[0] {
			return nil, requestInvalid("query parameter is invalid or not allowed")
		}
	}
	return values, nil
}
func rejectQuery(r *http.Request) error {
	if r.URL.RawQuery != "" {
		return requestInvalid("query parameters are not allowed")
	}
	return nil
}
func single(values url.Values, key string) string { return values.Get(key) }
func singleDefault(values url.Values, key, fallback string) string {
	if value := values.Get(key); value != "" {
		return value
	}
	return fallback
}
func parseLimit(raw string) (int, error) {
	value, err := strconv.Atoi(raw)
	if err != nil || value < 1 || value > maxListLimit {
		return 0, requestInvalid("limit is invalid")
	}
	return value, nil
}
func parseID(raw string) (foundation.ID, error) {
	if raw == "" || raw != strings.TrimSpace(raw) {
		return "", requestInvalid("UUID is invalid")
	}
	value, err := foundation.ParseID(raw)
	if err != nil || string(value) != raw {
		return "", requestInvalid("UUID is invalid")
	}
	return value, nil
}
func parsePathWorkspaceArtifact(r *http.Request) (foundation.ID, foundation.ID, error) {
	query, err := parseQuery(r, "workspace_id")
	if err != nil {
		return "", "", err
	}
	workspaceID, err := parseID(single(query, "workspace_id"))
	if err != nil {
		return "", "", err
	}
	artifactID, err := parseID(r.PathValue("artifact_id"))
	if err != nil {
		return "", "", err
	}
	return workspaceID, artifactID, nil
}
func formatTime(value time.Time) string { return value.UTC().Format(time.RFC3339Nano) }
func cursorInvalid() error {
	return foundation.NewError(foundation.ErrorInvalidInput, errorCodeCursorInvalid, false, errors.New("artifact cursor is invalid"))
}
func requestInvalid(message string) error {
	return foundation.NewError(foundation.ErrorInvalidInput, artifactapp.ErrorCodeRequestInvalid, false, errors.New(message))
}
func resultInvalid(message string) error {
	return foundation.NewError(foundation.ErrorConsistencyViolation, errorCodeResultInvalid, false, errors.New(message))
}
func nilDependency(value any) bool {
	if value == nil {
		return true
	}
	reflected := reflect.ValueOf(value)
	switch reflected.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return reflected.IsNil()
	default:
		return false
	}
}
func (handler *Handler) available(w http.ResponseWriter) bool {
	if !handler.Available() {
		httpapi.WriteProblem(w, http.StatusServiceUnavailable, errorCodeUnavailable, "Artifact 服务暂不可用", true, nil)
		return false
	}
	return true
}
func (handler *Handler) generationAvailable(w http.ResponseWriter) bool {
	if handler == nil || nilDependency(handler.generation) {
		writeGenerationUnavailable(w)
		return false
	}
	return true
}
func writeGenerationUnavailable(w http.ResponseWriter) {
	httpapi.WriteProblem(w, http.StatusServiceUnavailable, errorCodeGenerationCapabilityUnavailable, "Artifact 章节生成能力暂不可用", false, nil)
}
func writeGenerationError(w http.ResponseWriter, err error) {
	var classified *foundation.Error
	if errors.As(err, &classified) && classified.Kind == foundation.ErrorDependencyUnavailable {
		writeGenerationUnavailable(w)
		return
	}
	writeError(w, err)
}
func writeError(w http.ResponseWriter, err error) {
	if errors.Is(err, context.DeadlineExceeded) {
		httpapi.WriteProblem(w, http.StatusServiceUnavailable, artifactapp.ErrorCodeDependencyUnavailable, "Artifact 请求超时", true, nil)
		return
	}
	if errors.Is(err, context.Canceled) {
		httpapi.WriteProblem(w, http.StatusServiceUnavailable, artifactapp.ErrorCodeDependencyUnavailable, "Artifact 请求已取消", false, nil)
		return
	}
	var classified *foundation.Error
	if !errors.As(err, &classified) {
		httpapi.WriteProblem(w, http.StatusInternalServerError, "INTERNAL_ERROR", "Artifact 请求失败", false, nil)
		return
	}
	status, message := httpapi.StatusForErrorKind(classified.Kind), "Artifact 请求未完成"
	switch classified.Code {
	case "UNSUPPORTED_MEDIA_TYPE":
		status, message = http.StatusUnsupportedMediaType, "请求必须使用 application/json"
	case "INVALID_JSON", artifactapp.ErrorCodeRequestInvalid, artifactapp.ErrorCodeEvidenceInvalid, domain.ErrorCodeArtifactInvalid, domain.ErrorCodeArtifactCoverageInvalid:
		status, message = http.StatusBadRequest, "Artifact 请求参数无效"
	case artifactapp.ErrorCodeNotFound:
		status, message = http.StatusNotFound, "请求的 Artifact 资源不存在"
	case artifactapp.ErrorCodeIdempotencyConflict, artifactapp.ErrorCodeVersionConflict, domain.ErrorCodeArtifactTransitionInvalid, domain.ErrorCodeArtifactPublicationInvalid:
		status, message = http.StatusConflict, "Artifact 版本或状态冲突"
	case errorCodeResultInvalid, artifactapp.ErrorCodeResultInconsistent:
		status, message = http.StatusInternalServerError, "Artifact 响应校验失败"
	case artifactapp.ErrorCodeDependencyUnavailable:
		status, message = http.StatusServiceUnavailable, "Artifact 依赖暂不可用"
	}
	httpapi.WriteProblem(w, status, classified.Code, message, classified.Retryable, nil)
}

var _ CommandService = (*artifactapp.CommandService)(nil)
var _ QueryService = (*artifactapp.QueryService)(nil)
