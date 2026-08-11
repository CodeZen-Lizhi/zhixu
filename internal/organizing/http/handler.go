// Package organizinghttp exposes the strict Workspace-scoped Organizing HTTP boundary.
package organizinghttp

import (
	"context"
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

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation/strictjson"
	"github.com/CodeZen-Lizhi/zhixu/internal/httpapi"
	organizingapp "github.com/CodeZen-Lizhi/zhixu/internal/organizing/application"
	"github.com/CodeZen-Lizhi/zhixu/internal/organizing/domain"
	workflowdomain "github.com/CodeZen-Lizhi/zhixu/internal/workflow/domain"
	"github.com/gin-gonic/gin"
)

const (
	defaultTimeout               = 5 * time.Second
	maxSmallRequestBodyBytes     = 16 * 1024
	maxTemplateRequestBytes      = 64 * 1024
	defaultTemplateListLimit     = 100
	maxTemplateListLimit         = 100
	errorCodeUnavailable         = "ORGANIZING_HTTP_UNAVAILABLE"
	errorCodeRunUnavailable      = "ORGANIZING_RUN_PROJECTION_UNAVAILABLE"
	errorCodeWorkflowUnavailable = "ORGANIZING_WORKFLOW_PROJECTION_UNAVAILABLE"
	errorCodeRequestInvalid      = "ORGANIZING_REQUEST_INVALID"
	errorCodeInvalidJSON         = "INVALID_JSON"
	errorCodeUnsupportedMedia    = "UNSUPPORTED_MEDIA_TYPE"
	errorCodeResultInvalid       = "ORGANIZING_HTTP_RESULT_INCONSISTENT"
	errorCodeRequestTimeout      = "ORGANIZING_REQUEST_TIMEOUT"
	errorCodeRequestCancelled    = "ORGANIZING_REQUEST_CANCELLED"
)

// Service is the complete synchronous Organizing application contract exposed to HTTP.
type Service interface {
	CreateDraft(context.Context, organizingapp.CreateDraftCommand) (organizingapp.DraftResult, error)
	GetDraft(context.Context, foundation.ID, foundation.ID) (domain.Draft, error)
	UpdateDraft(context.Context, organizingapp.UpdateDraftCommand) (organizingapp.DraftResult, error)
	AddMaterial(context.Context, organizingapp.AddMaterialCommand) (organizingapp.DraftResult, error)
	RemoveMaterial(context.Context, organizingapp.RemoveMaterialCommand) (organizingapp.DraftResult, error)
	SetMaterialSelection(context.Context, organizingapp.SetMaterialSelectionCommand) (organizingapp.DraftResult, error)
	Suggest(context.Context, organizingapp.SuggestCommand) (organizingapp.DraftResult, error)
	ConfirmDraft(context.Context, organizingapp.ConfirmCommand) (organizingapp.ConfirmResult, error)
	GetSnapshot(context.Context, foundation.ID, foundation.ID) (domain.Snapshot, error)
	CreateTemplate(context.Context, organizingapp.CreateTemplateCommand) (organizingapp.TemplateResult, error)
	CloneTemplate(context.Context, organizingapp.CloneTemplateCommand) (organizingapp.TemplateResult, error)
	ReviseTemplate(context.Context, organizingapp.ReviseTemplateCommand) (organizingapp.TemplateResult, error)
	GetTemplate(context.Context, foundation.ID, foundation.ID) (organizingapp.TemplateDetail, error)
	ListTemplates(context.Context, organizingapp.TemplateListQuery) (organizingapp.TemplatePage, error)
	SearchMaterials(context.Context, organizingapp.MaterialSearchQuery) (organizingapp.MaterialSearchPage, error)
}

// RunReader prevents HTTP from inferring PENDING from a missing Run binding.
type RunReader interface {
	GetRunProjection(context.Context, foundation.ID, foundation.ID) (organizingapp.RunProjection, error)
}

// WorkflowReader exposes the authoritative persisted Workflow Run state.
type WorkflowReader interface {
	Get(context.Context, foundation.ID) (workflowdomain.Run, error)
}

// Handler maps strict identity-only requests to the Organizing application service.
type Handler struct {
	service   Service
	runs      RunReader
	workflows WorkflowReader
	timeout   time.Duration
}

// NewHandler creates a fail-closed Organizing handler.
func NewHandler(service Service, runs RunReader, workflows WorkflowReader, timeout time.Duration) *Handler {
	if timeout <= 0 {
		timeout = defaultTimeout
	}
	return &Handler{service: service, runs: runs, workflows: workflows, timeout: timeout}
}

// Available reports whether synchronous Draft, Snapshot, and Template commands are assembled.
func (handler *Handler) Available() bool {
	return handler != nil && !nilDependency(handler.service)
}

// Routes registers Organizing commands and read models beneath /api/v1.
func (handler *Handler) Routes(router gin.IRouter) {
	router.POST("/workspaces/:workspace_id/organizing/drafts", httpapi.GinHandler(handler.createDraft))
	router.GET("/workspaces/:workspace_id/organizing/drafts/:draft_id", httpapi.GinHandler(handler.getDraft))
	router.PUT("/workspaces/:workspace_id/organizing/drafts/:draft_id", httpapi.GinHandler(handler.updateDraft))
	router.POST("/workspaces/:workspace_id/organizing/drafts/:draft_id/suggestions", httpapi.GinHandler(handler.suggest))
	router.POST("/workspaces/:workspace_id/organizing/drafts/:draft_id/materials", httpapi.GinHandler(handler.addMaterial))
	router.PATCH("/workspaces/:workspace_id/organizing/drafts/:draft_id/materials/:material_id", httpapi.GinHandler(handler.setMaterialSelection))
	router.DELETE("/workspaces/:workspace_id/organizing/drafts/:draft_id/materials/:material_id", httpapi.GinHandler(handler.removeMaterial))
	router.POST("/workspaces/:workspace_id/organizing/drafts/:draft_id/confirm", httpapi.GinHandler(handler.confirmDraft))
	router.GET("/workspaces/:workspace_id/organizing/materials/search", httpapi.GinHandler(handler.searchMaterials))
	router.GET("/workspaces/:workspace_id/organizing/snapshots/:snapshot_id", httpapi.GinHandler(handler.getSnapshot))
	router.GET("/workspaces/:workspace_id/organizing/templates", httpapi.GinHandler(handler.listTemplates))
	router.POST("/workspaces/:workspace_id/organizing/templates", httpapi.GinHandler(handler.createTemplate))
	router.GET("/workspaces/:workspace_id/organizing/templates/:template_id", httpapi.GinHandler(handler.getTemplate))
	router.POST("/workspaces/:workspace_id/organizing/templates/:template_id/clone", httpapi.GinHandler(handler.cloneTemplate))
	router.POST("/workspaces/:workspace_id/organizing/templates/:template_id/revisions", httpapi.GinHandler(handler.reviseTemplate))
	router.GET("/workspaces/:workspace_id/organizing/runs/:snapshot_id", httpapi.GinHandler(handler.getRun))
}

type createDraftRequest struct {
	Intent string `json:"intent"`
}

type updateDraftRequest struct {
	ExpectedVersion    int64  `json:"expected_version"`
	Intent             string `json:"intent"`
	TemplateRevisionID string `json:"template_revision_id"`
}

type expectedVersionRequest struct {
	ExpectedVersion int64 `json:"expected_version"`
}

type materialSelectionRequest struct {
	ExpectedVersion int64 `json:"expected_version"`
	Selected        *bool `json:"selected"`
}

type suggestRequest struct {
	ExpectedVersion int64 `json:"expected_version"`
	Limit           int   `json:"limit,omitempty"`
}

type addMaterialRequest struct {
	ExpectedVersion   int64               `json:"expected_version"`
	Kind              domain.MaterialKind `json:"kind"`
	SourceVersionID   string              `json:"source_version_id,omitempty"`
	DocumentID        string              `json:"document_id,omitempty"`
	ArticleRevisionID string              `json:"article_revision_id,omitempty"`
	ClaimID           string              `json:"claim_id,omitempty"`
	CollectionID      string              `json:"collection_id,omitempty"`
}

type confirmDraftRequest struct {
	ExpectedVersion    int64  `json:"expected_version"`
	TemplateRevisionID string `json:"template_revision_id"`
}

type createTemplateRequest struct {
	Declaration templateDeclarationRequest `json:"declaration"`
}

type cloneTemplateRequest struct {
	Name string `json:"name"`
}

type reviseTemplateRequest struct {
	ExpectedVersion int64                      `json:"expected_version"`
	Declaration     templateDeclarationRequest `json:"declaration"`
}

type templateDeclarationRequest struct {
	SchemaVersion          string                    `json:"schema_version"`
	Kind                   domain.TemplateKind       `json:"kind"`
	Name                   string                    `json:"name"`
	Description            string                    `json:"description"`
	Materials              materialPolicyRequest     `json:"materials"`
	Sections               []templateSectionRequest  `json:"sections"`
	Presentation           presentationPolicyRequest `json:"presentation"`
	Output                 outputDefaultsRequest     `json:"output"`
	AdditionalInstructions string                    `json:"additional_instructions"`
}

type materialPolicyRequest struct {
	AllowedKinds []domain.MaterialKind `json:"allowed_kinds"`
	MinMaterials int                   `json:"min_materials"`
	MaxMaterials int                   `json:"max_materials"`
}

type templateSectionRequest struct {
	Key      string `json:"key"`
	Title    string `json:"title"`
	Required bool   `json:"required"`
}

type presentationPolicyRequest struct {
	Audience        string              `json:"audience"`
	Language        string              `json:"language"`
	Tone            string              `json:"tone"`
	Length          domain.LengthPreset `json:"length"`
	IncludeCode     bool                `json:"include_code"`
	IncludeExamples bool                `json:"include_examples"`
	IncludeFAQ      bool                `json:"include_faq"`
}

type outputDefaultsRequest struct {
	Directory       string `json:"directory"`
	FilenamePattern string `json:"filename_pattern"`
}

func (handler *Handler) createDraft(writer http.ResponseWriter, request *http.Request) {
	workspaceID, key, ok := commandRoute(writer, request)
	if !ok {
		return
	}
	wire, err := decodeJSON[createDraftRequest](request, maxSmallRequestBodyBytes, 4096, 0)
	if err != nil {
		writeError(writer, err)
		return
	}
	if !handler.available(writer) {
		return
	}
	ctx, cancel := context.WithTimeout(request.Context(), handler.timeout)
	defer cancel()
	result, err := handler.service.CreateDraft(ctx, organizingapp.CreateDraftCommand{WorkspaceID: workspaceID, Intent: wire.Intent, IdempotencyKey: key})
	if err != nil {
		writeError(writer, err)
		return
	}
	response, err := toDraftCommandResponse(result, workspaceID, "")
	if err != nil || result.Draft.Version != 1 || result.Draft.Intent != strings.TrimSpace(wire.Intent) {
		if err == nil {
			err = resultInvalid("application returned an invalid created Draft")
		}
		writeError(writer, err)
		return
	}
	status := http.StatusCreated
	if result.Replayed {
		status = http.StatusOK
	}
	writeJSON(writer, status, response)
}

func (handler *Handler) getDraft(writer http.ResponseWriter, request *http.Request) {
	workspaceID, draftID, ok := queryRoute(writer, request, "draft_id")
	if !ok || !handler.available(writer) {
		return
	}
	ctx, cancel := context.WithTimeout(request.Context(), handler.timeout)
	defer cancel()
	draft, err := handler.service.GetDraft(ctx, workspaceID, draftID)
	if err != nil {
		writeError(writer, err)
		return
	}
	response, err := toDraftResponse(draft, workspaceID, draftID)
	if err != nil {
		writeError(writer, err)
		return
	}
	writeJSON(writer, http.StatusOK, struct {
		Draft draftResponse `json:"draft"`
	}{response})
}

func (handler *Handler) updateDraft(writer http.ResponseWriter, request *http.Request) {
	workspaceID, draftID, key, ok := identifiedCommandRoute(writer, request, "draft_id")
	if !ok {
		return
	}
	wire, err := decodeJSON[updateDraftRequest](request, maxSmallRequestBodyBytes, 4096, 0)
	templateRevisionID, templateErr := parseID(wire.TemplateRevisionID)
	if err != nil || templateErr != nil || wire.ExpectedVersion < 1 {
		if err == nil {
			err = templateErr
		}
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
	result, err := handler.service.UpdateDraft(ctx, organizingapp.UpdateDraftCommand{WorkspaceID: workspaceID, DraftID: draftID,
		ExpectedVersion: wire.ExpectedVersion, Intent: wire.Intent, TemplateRevisionID: templateRevisionID, IdempotencyKey: key})
	if err != nil {
		writeError(writer, err)
		return
	}
	response, err := toDraftCommandResponse(result, workspaceID, draftID)
	if err != nil || result.Draft.Version != wire.ExpectedVersion+1 || result.Draft.TemplateRevisionID != templateRevisionID ||
		result.Draft.Intent != strings.TrimSpace(wire.Intent) {
		if err == nil {
			err = resultInvalid("application returned an invalid Draft CAS or Template Revision binding")
		}
		writeError(writer, err)
		return
	}
	writeJSON(writer, http.StatusOK, response)
}

func (handler *Handler) suggest(writer http.ResponseWriter, request *http.Request) {
	workspaceID, draftID, key, ok := identifiedCommandRoute(writer, request, "draft_id")
	if !ok {
		return
	}
	wire, err := decodeJSON[suggestRequest](request, maxSmallRequestBodyBytes, 4096, 0)
	if err != nil || wire.ExpectedVersion < 1 || wire.Limit < 0 || wire.Limit > domain.MaxDraftMaterials {
		if err == nil {
			err = requestInvalid("suggestion version or limit is invalid")
		}
		writeError(writer, err)
		return
	}
	if !handler.available(writer) {
		return
	}
	ctx, cancel := context.WithTimeout(request.Context(), handler.timeout)
	defer cancel()
	result, err := handler.service.Suggest(ctx, organizingapp.SuggestCommand{WorkspaceID: workspaceID, DraftID: draftID,
		ExpectedVersion: wire.ExpectedVersion, Limit: wire.Limit, IdempotencyKey: key})
	if err != nil {
		writeError(writer, err)
		return
	}
	response, err := toDraftCommandResponse(result, workspaceID, draftID)
	if err != nil || result.Draft.Version != wire.ExpectedVersion+1 {
		if err == nil {
			err = resultInvalid("application returned an invalid Suggest CAS version")
		}
		writeError(writer, err)
		return
	}
	writeJSON(writer, http.StatusOK, response)
}

func (handler *Handler) addMaterial(writer http.ResponseWriter, request *http.Request) {
	workspaceID, draftID, key, ok := identifiedCommandRoute(writer, request, "draft_id")
	if !ok {
		return
	}
	wire, err := decodeJSON[addMaterialRequest](request, maxSmallRequestBodyBytes, 4096, 0)
	selector, selectorErr := wire.selector()
	if err != nil || selectorErr != nil || wire.ExpectedVersion < 1 {
		if err == nil {
			err = selectorErr
		}
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
	result, err := handler.service.AddMaterial(ctx, organizingapp.AddMaterialCommand{WorkspaceID: workspaceID, DraftID: draftID,
		ExpectedVersion: wire.ExpectedVersion, Selector: selector, IdempotencyKey: key})
	if err != nil {
		writeError(writer, err)
		return
	}
	response, err := toDraftCommandResponse(result, workspaceID, draftID)
	if err != nil || result.Draft.Version != wire.ExpectedVersion+1 {
		if err == nil {
			err = resultInvalid("application returned an invalid Add Material CAS version")
		}
		writeError(writer, err)
		return
	}
	status := http.StatusCreated
	if result.Replayed {
		status = http.StatusOK
	}
	writeJSON(writer, status, response)
}

func (handler *Handler) removeMaterial(writer http.ResponseWriter, request *http.Request) {
	workspaceID, draftID, key, ok := identifiedCommandRoute(writer, request, "draft_id")
	if !ok {
		return
	}
	materialID, err := parseID(request.PathValue("material_id"))
	if err != nil {
		writeError(writer, err)
		return
	}
	wire, err := decodeJSON[expectedVersionRequest](request, maxSmallRequestBodyBytes, 4096, 0)
	if err != nil || wire.ExpectedVersion < 1 {
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
	result, err := handler.service.RemoveMaterial(ctx, organizingapp.RemoveMaterialCommand{WorkspaceID: workspaceID, DraftID: draftID,
		MaterialID: materialID, ExpectedVersion: wire.ExpectedVersion, IdempotencyKey: key})
	if err != nil {
		writeError(writer, err)
		return
	}
	response, err := toDraftCommandResponse(result, workspaceID, draftID)
	if err != nil || result.Draft.Version != wire.ExpectedVersion+1 {
		if err == nil {
			err = resultInvalid("application returned an invalid Remove Material CAS version")
		}
		writeError(writer, err)
		return
	}
	writeJSON(writer, http.StatusOK, response)
}

func (handler *Handler) setMaterialSelection(writer http.ResponseWriter, request *http.Request) {
	workspaceID, draftID, key, ok := identifiedCommandRoute(writer, request, "draft_id")
	if !ok {
		return
	}
	materialID, err := parseID(request.PathValue("material_id"))
	if err != nil {
		writeError(writer, err)
		return
	}
	wire, err := decodeJSON[materialSelectionRequest](request, maxSmallRequestBodyBytes, 4096, 0)
	if err != nil || wire.ExpectedVersion < 1 || wire.Selected == nil {
		if err == nil {
			err = requestInvalid("expected_version and selected are required")
		}
		writeError(writer, err)
		return
	}
	if !handler.available(writer) {
		return
	}
	ctx, cancel := context.WithTimeout(request.Context(), handler.timeout)
	defer cancel()
	result, err := handler.service.SetMaterialSelection(ctx, organizingapp.SetMaterialSelectionCommand{
		WorkspaceID: workspaceID, DraftID: draftID, MaterialID: materialID, ExpectedVersion: wire.ExpectedVersion,
		Selected: *wire.Selected, IdempotencyKey: key,
	})
	if err != nil {
		writeError(writer, err)
		return
	}
	response, err := toDraftCommandResponse(result, workspaceID, draftID)
	selectionMatches := false
	for _, material := range result.Draft.Materials {
		if material.ID == materialID && material.Selected == *wire.Selected {
			selectionMatches = true
			break
		}
	}
	if err != nil || result.Draft.Version != wire.ExpectedVersion+1 || !selectionMatches {
		if err == nil {
			err = resultInvalid("application returned an invalid Material Selection CAS result")
		}
		writeError(writer, err)
		return
	}
	writeJSON(writer, http.StatusOK, response)
}

func (handler *Handler) confirmDraft(writer http.ResponseWriter, request *http.Request) {
	workspaceID, draftID, key, ok := identifiedCommandRoute(writer, request, "draft_id")
	if !ok {
		return
	}
	wire, err := decodeJSON[confirmDraftRequest](request, maxSmallRequestBodyBytes, 4096, 0)
	templateRevisionID, idErr := parseID(wire.TemplateRevisionID)
	if err != nil || idErr != nil || wire.ExpectedVersion < 1 {
		if err == nil {
			err = idErr
		}
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
	result, err := handler.service.ConfirmDraft(ctx, organizingapp.ConfirmCommand{WorkspaceID: workspaceID, DraftID: draftID,
		ExpectedVersion: wire.ExpectedVersion, TemplateRevisionID: templateRevisionID, IdempotencyKey: key})
	if err != nil {
		writeError(writer, err)
		return
	}
	response, err := toConfirmResponse(result, workspaceID, draftID, wire.ExpectedVersion, templateRevisionID)
	if err != nil {
		writeError(writer, err)
		return
	}
	status := http.StatusAccepted
	if result.Replayed {
		status = http.StatusOK
	}
	writeJSON(writer, status, response)
}

func (handler *Handler) getSnapshot(writer http.ResponseWriter, request *http.Request) {
	workspaceID, snapshotID, ok := queryRoute(writer, request, "snapshot_id")
	if !ok || !handler.available(writer) {
		return
	}
	ctx, cancel := context.WithTimeout(request.Context(), handler.timeout)
	defer cancel()
	snapshot, err := handler.service.GetSnapshot(ctx, workspaceID, snapshotID)
	if err != nil {
		writeError(writer, err)
		return
	}
	response, err := toSnapshotResponse(snapshot, workspaceID, snapshotID)
	if err != nil {
		writeError(writer, err)
		return
	}
	writeJSON(writer, http.StatusOK, struct {
		Snapshot snapshotResponse `json:"snapshot"`
	}{response})
}

func (handler *Handler) searchMaterials(writer http.ResponseWriter, request *http.Request) {
	workspaceID, query, kind, limit, err := parseMaterialSearchRequest(request)
	if err != nil {
		writeError(writer, err)
		return
	}
	if !handler.available(writer) {
		return
	}
	ctx, cancel := context.WithTimeout(request.Context(), handler.timeout)
	defer cancel()
	page, err := handler.service.SearchMaterials(ctx, organizingapp.MaterialSearchQuery{
		WorkspaceID: workspaceID, Query: query, Kind: kind, Limit: limit,
	})
	if err != nil {
		writeError(writer, err)
		return
	}
	response, err := toMaterialSearchPageResponse(page, workspaceID, query, kind, limit)
	if err != nil {
		writeError(writer, err)
		return
	}
	writeJSON(writer, http.StatusOK, response)
}

func (handler *Handler) listTemplates(writer http.ResponseWriter, request *http.Request) {
	workspaceID, kind, limit, err := parseTemplateListRequest(request)
	if err != nil {
		writeError(writer, err)
		return
	}
	if !handler.available(writer) {
		return
	}
	ctx, cancel := context.WithTimeout(request.Context(), handler.timeout)
	defer cancel()
	page, err := handler.service.ListTemplates(ctx, organizingapp.TemplateListQuery{WorkspaceID: workspaceID, Kind: kind, Limit: limit})
	if err != nil {
		writeError(writer, err)
		return
	}
	response, err := toTemplatePageResponse(page, workspaceID, limit)
	if err != nil {
		writeError(writer, err)
		return
	}
	writeJSON(writer, http.StatusOK, response)
}

func (handler *Handler) createTemplate(writer http.ResponseWriter, request *http.Request) {
	workspaceID, key, ok := commandRoute(writer, request)
	if !ok {
		return
	}
	wire, err := decodeJSON[createTemplateRequest](request, maxTemplateRequestBytes, 8192, domain.MaxSnapshotMaterials)
	if err != nil {
		writeError(writer, err)
		return
	}
	declaration := wire.Declaration.domainValue()
	if !handler.available(writer) {
		return
	}
	ctx, cancel := context.WithTimeout(request.Context(), handler.timeout)
	defer cancel()
	result, err := handler.service.CreateTemplate(ctx, organizingapp.CreateTemplateCommand{WorkspaceID: workspaceID, Declaration: declaration, IdempotencyKey: key})
	if err != nil {
		writeError(writer, err)
		return
	}
	response, err := toTemplateCommandResponse(result, workspaceID, "")
	if err != nil || result.Detail.Template.Owner != domain.TemplateCustom || result.Detail.Template.Version != 1 {
		if err == nil {
			err = resultInvalid("application returned an invalid created Template")
		}
		writeError(writer, err)
		return
	}
	status := http.StatusCreated
	if result.Replayed {
		status = http.StatusOK
	}
	writeJSON(writer, status, response)
}

func (handler *Handler) getTemplate(writer http.ResponseWriter, request *http.Request) {
	workspaceID, templateID, ok := queryRoute(writer, request, "template_id")
	if !ok || !handler.available(writer) {
		return
	}
	ctx, cancel := context.WithTimeout(request.Context(), handler.timeout)
	defer cancel()
	detail, err := handler.service.GetTemplate(ctx, workspaceID, templateID)
	if err != nil {
		writeError(writer, err)
		return
	}
	response, err := toTemplateResponse(detail, workspaceID, templateID)
	if err != nil {
		writeError(writer, err)
		return
	}
	writeJSON(writer, http.StatusOK, struct {
		Template templateResponse `json:"template"`
	}{response})
}

func (handler *Handler) cloneTemplate(writer http.ResponseWriter, request *http.Request) {
	workspaceID, sourceTemplateID, key, ok := identifiedCommandRoute(writer, request, "template_id")
	if !ok {
		return
	}
	wire, err := decodeJSON[cloneTemplateRequest](request, maxSmallRequestBodyBytes, 4096, 0)
	if err != nil {
		writeError(writer, err)
		return
	}
	if !handler.available(writer) {
		return
	}
	ctx, cancel := context.WithTimeout(request.Context(), handler.timeout)
	defer cancel()
	result, err := handler.service.CloneTemplate(ctx, organizingapp.CloneTemplateCommand{WorkspaceID: workspaceID,
		SourceTemplateID: sourceTemplateID, Name: wire.Name, IdempotencyKey: key})
	if err != nil {
		writeError(writer, err)
		return
	}
	response, err := toTemplateCommandResponse(result, workspaceID, "")
	if err != nil || result.Detail.Template.Owner != domain.TemplateCustom || result.Detail.Template.Version != 1 {
		if err == nil {
			err = resultInvalid("application returned an invalid cloned Template")
		}
		writeError(writer, err)
		return
	}
	status := http.StatusCreated
	if result.Replayed {
		status = http.StatusOK
	}
	writeJSON(writer, status, response)
}

func (handler *Handler) reviseTemplate(writer http.ResponseWriter, request *http.Request) {
	workspaceID, templateID, key, ok := identifiedCommandRoute(writer, request, "template_id")
	if !ok {
		return
	}
	wire, err := decodeJSON[reviseTemplateRequest](request, maxTemplateRequestBytes, 8192, domain.MaxSnapshotMaterials)
	if err != nil || wire.ExpectedVersion < 1 {
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
	result, err := handler.service.ReviseTemplate(ctx, organizingapp.ReviseTemplateCommand{WorkspaceID: workspaceID, TemplateID: templateID,
		ExpectedVersion: wire.ExpectedVersion, Declaration: wire.Declaration.domainValue(), IdempotencyKey: key})
	if err != nil {
		writeError(writer, err)
		return
	}
	response, err := toTemplateCommandResponse(result, workspaceID, templateID)
	if err != nil || result.Detail.Template.Version != wire.ExpectedVersion+1 {
		if err == nil {
			err = resultInvalid("application returned an invalid Template CAS version")
		}
		writeError(writer, err)
		return
	}
	status := http.StatusCreated
	if result.Replayed {
		status = http.StatusOK
	}
	writeJSON(writer, status, response)
}

func (handler *Handler) getRun(writer http.ResponseWriter, request *http.Request) {
	workspaceID, snapshotID, ok := queryRoute(writer, request, "snapshot_id")
	if !ok {
		return
	}
	if nilDependency(handler.runs) {
		writeProblem(writer, http.StatusServiceUnavailable, errorCodeRunUnavailable, "整理流程状态暂不可用", true)
		return
	}
	ctx, cancel := context.WithTimeout(request.Context(), handler.timeout)
	defer cancel()
	view, err := handler.runs.GetRunProjection(ctx, workspaceID, snapshotID)
	if err != nil {
		writeError(writer, err)
		return
	}
	if err := validateRunProjection(view, workspaceID, snapshotID); err != nil {
		writeError(writer, err)
		return
	}
	var workflowRun *workflowdomain.Run
	if view.Status == organizingapp.StartStarted {
		if nilDependency(handler.workflows) {
			writeProblem(writer, http.StatusServiceUnavailable, errorCodeWorkflowUnavailable, "工作流状态暂不可用", true)
			return
		}
		persisted, workflowErr := handler.workflows.Get(ctx, view.Binding.WorkflowRunID)
		if workflowErr != nil {
			writeError(writer, workflowErr)
			return
		}
		workflowRun = &persisted
		if persisted.Status == workflowdomain.RunStatusSucceeded && view.Result == nil {
			refreshed, refreshErr := handler.runs.GetRunProjection(ctx, workspaceID, snapshotID)
			if refreshErr != nil {
				writeError(writer, refreshErr)
				return
			}
			if err := validateRunProjection(refreshed, workspaceID, snapshotID); err != nil || refreshed.Status != organizingapp.StartStarted ||
				refreshed.Binding == nil || !sameRunBinding(*view.Binding, *refreshed.Binding) || refreshed.Result == nil {
				if err == nil {
					err = resultInvalid("refreshed Run projection does not match the succeeded Workflow binding")
				}
				writeError(writer, err)
				return
			}
			view = refreshed
		}
	}
	response, err := toRunResponse(view, workflowRun, workspaceID, snapshotID)
	if err != nil {
		writeError(writer, err)
		return
	}
	writeJSON(writer, http.StatusOK, struct {
		Run runResponse `json:"run"`
	}{response})
}

func (request addMaterialRequest) selector() (organizingapp.MaterialSelector, error) {
	selector := organizingapp.MaterialSelector{Kind: request.Kind}
	parseOptional := func(value string) (foundation.ID, error) {
		if value == "" {
			return "", nil
		}
		return parseID(value)
	}
	var err error
	if selector.SourceVersionID, err = parseOptional(request.SourceVersionID); err != nil {
		return organizingapp.MaterialSelector{}, requestInvalid("source_version_id is invalid")
	}
	if selector.DocumentID, err = parseOptional(request.DocumentID); err != nil {
		return organizingapp.MaterialSelector{}, requestInvalid("document_id is invalid")
	}
	if selector.ArticleRevisionID, err = parseOptional(request.ArticleRevisionID); err != nil {
		return organizingapp.MaterialSelector{}, requestInvalid("article_revision_id is invalid")
	}
	if selector.ClaimID, err = parseOptional(request.ClaimID); err != nil {
		return organizingapp.MaterialSelector{}, requestInvalid("claim_id is invalid")
	}
	if selector.CollectionID, err = parseOptional(request.CollectionID); err != nil {
		return organizingapp.MaterialSelector{}, requestInvalid("collection_id is invalid")
	}
	valid := false
	switch selector.Kind {
	case domain.MaterialSourceVersion:
		valid = selector.SourceVersionID != "" && selector.DocumentID == "" && selector.ArticleRevisionID == "" && selector.ClaimID == "" && selector.CollectionID == ""
	case domain.MaterialDocumentRevision:
		valid = selector.DocumentID != "" && selector.ArticleRevisionID != "" && selector.SourceVersionID == "" && selector.ClaimID == "" && selector.CollectionID == ""
	case domain.MaterialClaim:
		valid = selector.ClaimID != "" && selector.SourceVersionID == "" && selector.DocumentID == "" && selector.ArticleRevisionID == "" && selector.CollectionID == ""
	case domain.MaterialSmartCollection:
		valid = selector.CollectionID != "" && selector.SourceVersionID == "" && selector.DocumentID == "" && selector.ArticleRevisionID == "" && selector.ClaimID == ""
	}
	if !valid {
		return organizingapp.MaterialSelector{}, requestInvalid("material identity does not match kind")
	}
	return selector, nil
}

func (request templateDeclarationRequest) domainValue() domain.TemplateDeclaration {
	allowedKinds := append([]domain.MaterialKind(nil), request.Materials.AllowedKinds...)
	sections := make([]domain.TemplateSection, len(request.Sections))
	for index, section := range request.Sections {
		sections[index] = domain.TemplateSection{Key: section.Key, Title: section.Title, Required: section.Required}
	}
	return domain.TemplateDeclaration{SchemaVersion: request.SchemaVersion, Kind: request.Kind, Name: request.Name,
		Description: request.Description, Materials: domain.MaterialPolicy{AllowedKinds: allowedKinds,
			MinMaterials: request.Materials.MinMaterials, MaxMaterials: request.Materials.MaxMaterials}, Sections: sections,
		Presentation: domain.PresentationPolicy{Audience: request.Presentation.Audience, Language: request.Presentation.Language,
			Tone: request.Presentation.Tone, Length: request.Presentation.Length, IncludeCode: request.Presentation.IncludeCode,
			IncludeExamples: request.Presentation.IncludeExamples, IncludeFAQ: request.Presentation.IncludeFAQ},
		Output:                 domain.OutputDefaults{Directory: request.Output.Directory, FilenamePattern: request.Output.FilenamePattern},
		AdditionalInstructions: request.AdditionalInstructions}
}

func commandRoute(writer http.ResponseWriter, request *http.Request) (foundation.ID, string, bool) {
	if err := rejectQuery(request); err != nil {
		writeError(writer, err)
		return "", "", false
	}
	workspaceID, err := parseID(request.PathValue("workspace_id"))
	if err != nil {
		writeError(writer, err)
		return "", "", false
	}
	key, err := parseIdempotencyKey(request)
	if err != nil {
		writeError(writer, err)
		return "", "", false
	}
	return workspaceID, key, true
}

func identifiedCommandRoute(writer http.ResponseWriter, request *http.Request, resourceParam string) (foundation.ID, foundation.ID, string, bool) {
	workspaceID, key, ok := commandRoute(writer, request)
	if !ok {
		return "", "", "", false
	}
	resourceID, err := parseID(request.PathValue(resourceParam))
	if err != nil {
		writeError(writer, err)
		return "", "", "", false
	}
	return workspaceID, resourceID, key, true
}

func queryRoute(writer http.ResponseWriter, request *http.Request, resourceParam string) (foundation.ID, foundation.ID, bool) {
	if err := rejectQuery(request); err != nil {
		writeError(writer, err)
		return "", "", false
	}
	workspaceID, err := parseID(request.PathValue("workspace_id"))
	if err != nil {
		writeError(writer, err)
		return "", "", false
	}
	resourceID, err := parseID(request.PathValue(resourceParam))
	if err != nil {
		writeError(writer, err)
		return "", "", false
	}
	return workspaceID, resourceID, true
}

func decodeJSON[T any](request *http.Request, maxDocumentBytes, maxStringBytes, maxArrayItems int) (T, error) {
	var zero T
	contentTypes := request.Header.Values("Content-Type")
	if len(contentTypes) != 1 {
		return zero, unsupportedMedia()
	}
	mediaType, _, err := mime.ParseMediaType(contentTypes[0])
	if err != nil || !strings.EqualFold(mediaType, "application/json") {
		return zero, unsupportedMedia()
	}
	if request.ContentLength > int64(maxDocumentBytes) {
		return zero, invalidJSON("request JSON is oversized")
	}
	body, err := io.ReadAll(io.LimitReader(request.Body, int64(maxDocumentBytes)+1))
	if err != nil || len(body) == 0 || len(body) > maxDocumentBytes {
		return zero, invalidJSON("request JSON is empty, unreadable, or oversized")
	}
	limits := strictjson.DefaultLimits()
	limits.MaxDocumentBytes = maxDocumentBytes
	limits.MaxStringBytes = maxStringBytes
	limits.MaxDepth = 8
	limits.MaxObjectFields = 16
	limits.MaxArrayItems = maxArrayItems
	value, err := strictjson.DecodeObject[T](body, limits, nil)
	if err != nil {
		return zero, invalidJSON("request JSON does not match the Organizing contract")
	}
	return value, nil
}

func parseIdempotencyKey(request *http.Request) (string, error) {
	values := request.Header.Values("Idempotency-Key")
	if len(values) != 1 || !utf8.ValidString(values[0]) {
		return "", foundation.NewError(foundation.ErrorInvalidInput, organizingapp.ErrorCodeIdempotencyInvalid, false, errors.New("exactly one Idempotency-Key is required"))
	}
	key := values[0]
	if key == "" || key != strings.TrimSpace(key) || len(key) > organizingapp.MaxIdempotencyKeyBytes {
		return "", foundation.NewError(foundation.ErrorInvalidInput, organizingapp.ErrorCodeIdempotencyInvalid, false, errors.New("Idempotency-Key is invalid"))
	}
	for _, character := range key {
		if unicode.IsControl(character) {
			return "", foundation.NewError(foundation.ErrorInvalidInput, organizingapp.ErrorCodeIdempotencyInvalid, false, errors.New("Idempotency-Key is invalid"))
		}
	}
	return key, nil
}

func parseID(value string) (foundation.ID, error) {
	id, err := foundation.ParseID(value)
	if err != nil {
		return "", requestInvalid("resource id is invalid")
	}
	return id, nil
}

func rejectQuery(request *http.Request) error {
	if request.URL.RawQuery != "" {
		return requestInvalid("query parameters are not allowed")
	}
	return nil
}

func parseTemplateListRequest(request *http.Request) (foundation.ID, domain.TemplateKind, int, error) {
	workspaceID, err := parseID(request.PathValue("workspace_id"))
	if err != nil {
		return "", "", 0, err
	}
	values, err := url.ParseQuery(request.URL.RawQuery)
	if err != nil {
		return "", "", 0, requestInvalid("template list query is invalid")
	}
	for key, entries := range values {
		if (key != "kind" && key != "limit") || len(entries) != 1 || entries[0] == "" || entries[0] != strings.TrimSpace(entries[0]) {
			return "", "", 0, requestInvalid("template list query parameter is invalid")
		}
	}
	kind := domain.TemplateKind(values.Get("kind"))
	if kind != "" && !kind.Valid() {
		return "", "", 0, requestInvalid("template kind is invalid")
	}
	limit := defaultTemplateListLimit
	if raw := values.Get("limit"); raw != "" {
		limit, err = strconv.Atoi(raw)
		if err != nil || limit < 1 || limit > maxTemplateListLimit {
			return "", "", 0, requestInvalid("template list limit is invalid")
		}
	}
	return workspaceID, kind, limit, nil
}

func parseMaterialSearchRequest(request *http.Request) (foundation.ID, string, domain.MaterialKind, int, error) {
	workspaceID, err := parseID(request.PathValue("workspace_id"))
	if err != nil {
		return "", "", "", 0, err
	}
	values, err := url.ParseQuery(request.URL.RawQuery)
	if err != nil {
		return "", "", "", 0, requestInvalid("material search query is invalid")
	}
	for key, entries := range values {
		if (key != "q" && key != "kind" && key != "limit") || len(entries) != 1 || entries[0] == "" || entries[0] != strings.TrimSpace(entries[0]) {
			return "", "", "", 0, requestInvalid("material search query parameter is invalid")
		}
	}
	query := values.Get("q")
	kind := domain.MaterialKind(values.Get("kind"))
	if len([]byte(query)) < 2 || len([]byte(query)) > organizingapp.MaxMaterialSearchQueryBytes || !utf8.ValidString(query) || containsControl(query) || !kind.Valid() {
		return "", "", "", 0, requestInvalid("material search query is invalid")
	}
	limit := organizingapp.DefaultMaterialSearchLimit
	if raw := values.Get("limit"); raw != "" {
		limit, err = strconv.Atoi(raw)
		if err != nil || limit < 1 || limit > organizingapp.MaxMaterialSearchLimit {
			return "", "", "", 0, requestInvalid("material search limit is invalid")
		}
	}
	return workspaceID, query, kind, limit, nil
}

func containsControl(value string) bool {
	for _, character := range value {
		if unicode.IsControl(character) {
			return true
		}
	}
	return false
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

func (handler *Handler) available(writer http.ResponseWriter) bool {
	if !handler.Available() {
		writeProblem(writer, http.StatusServiceUnavailable, errorCodeUnavailable, "整理服务暂不可用", true)
		return false
	}
	return true
}

func requestInvalid(message string) error {
	return foundation.NewError(foundation.ErrorInvalidInput, errorCodeRequestInvalid, false, errors.New(message))
}

func invalidJSON(message string) error {
	return foundation.NewError(foundation.ErrorInvalidInput, errorCodeInvalidJSON, false, errors.New(message))
}

func unsupportedMedia() error {
	return foundation.NewError(foundation.ErrorInvalidInput, errorCodeUnsupportedMedia, false, errors.New("request requires application/json"))
}

func resultInvalid(message string) error {
	return foundation.NewError(foundation.ErrorConsistencyViolation, errorCodeResultInvalid, false, errors.New(message))
}

func writeJSON(writer http.ResponseWriter, status int, value any) {
	writer.Header().Set("Cache-Control", "private, no-store")
	httpapi.WriteJSON(writer, status, value)
}

func writeProblem(writer http.ResponseWriter, status int, code, message string, retryable bool) {
	writer.Header().Set("Cache-Control", "private, no-store")
	httpapi.WriteProblem(writer, status, code, message, retryable, nil)
}

func writeError(writer http.ResponseWriter, err error) {
	if errors.Is(err, context.DeadlineExceeded) {
		writeProblem(writer, http.StatusServiceUnavailable, errorCodeRequestTimeout, "整理请求超时", true)
		return
	}
	if errors.Is(err, context.Canceled) {
		writeProblem(writer, http.StatusServiceUnavailable, errorCodeRequestCancelled, "整理请求已取消", false)
		return
	}
	var classified *foundation.Error
	if !errors.As(err, &classified) {
		writeProblem(writer, http.StatusInternalServerError, "INTERNAL_ERROR", "整理请求失败", false)
		return
	}
	status := httpapi.StatusForErrorKind(classified.Kind)
	message := "整理请求未完成"
	switch classified.Code {
	case errorCodeUnsupportedMedia:
		status, message = http.StatusUnsupportedMediaType, "请求必须使用 application/json"
	case errorCodeInvalidJSON, errorCodeRequestInvalid,
		organizingapp.ErrorCodeIdempotencyInvalid, domain.ErrorCodeDraftInvalid, domain.ErrorCodeMaterialInvalid,
		domain.ErrorCodeTemplateInvalid, domain.ErrorCodeSnapshotInvalid, "INVALID_ID":
		status, message = http.StatusBadRequest, "整理请求参数无效"
	case organizingapp.ErrorCodeNotFound:
		status, message = http.StatusNotFound, "请求的整理资源不存在"
	case organizingapp.ErrorCodeIdempotencyConflict, organizingapp.ErrorCodeMaterialStale,
		organizingapp.ErrorCodeTemplateReadOnly, domain.ErrorCodeVersionConflict:
		status, message = http.StatusConflict, "整理版本、材料或状态冲突"
	case errorCodeResultInvalid, organizingapp.ErrorCodeResultInvalid:
		status, message = http.StatusInternalServerError, "整理响应校验失败"
	case organizingapp.ErrorCodeDependencyUnavailable, organizingapp.ErrorCodeRepositoryUnavailable:
		status, message = http.StatusServiceUnavailable, "整理依赖暂不可用"
	}
	writeProblem(writer, status, classified.Code, message, classified.Retryable)
}

var _ Service = (*organizingapp.Service)(nil)
var _ RunReader = (*organizingapp.Service)(nil)
