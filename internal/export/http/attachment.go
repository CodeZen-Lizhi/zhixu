package http

import (
	"context"
	"errors"
	"fmt"
	"io"
	"mime"
	nethttp "net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	authhttp "github.com/CodeZen-Lizhi/zhixu/internal/auth/http"
	"github.com/CodeZen-Lizhi/zhixu/internal/capability"
	exportapp "github.com/CodeZen-Lizhi/zhixu/internal/export/application"
	"github.com/CodeZen-Lizhi/zhixu/internal/export/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation/strictjson"
	"github.com/CodeZen-Lizhi/zhixu/internal/httpapi"
	"github.com/go-chi/chi/v5"
)

const (
	attachmentSchemaVersion = "attachment-export/v1"
	attachmentRootVersion   = "workspace-attachments/v1"
)

type attachmentCreateRequest struct {
	Kind                          domain.Kind            `json:"kind"`
	SchemaVersion                 string                 `json:"schema_version"`
	AttachmentRootContractVersion string                 `json:"attachment_root_contract_version"`
	ContentPolicy                 domain.RedactionPolicy `json:"content_policy"`
	ExpiresInSeconds              *int64                 `json:"expires_in_seconds,omitempty"`
}

type attachmentCreateResponse struct {
	Job             attachmentJobResponse `json:"job"`
	Replayed        bool                  `json:"replayed"`
	DispatchPending bool                  `json:"dispatch_pending"`
}

type attachmentListResponse struct {
	WorkspaceID string                  `json:"workspace_id"`
	ScopeKind   domain.ScopeKind        `json:"scope_kind"`
	Items       []attachmentJobResponse `json:"items"`
	NextCursor  *string                 `json:"next_cursor,omitempty"`
}

type attachmentJobResponse struct {
	ID                            string                 `json:"id"`
	WorkspaceID                   string                 `json:"workspace_id"`
	ScopeKind                     domain.ScopeKind       `json:"scope_kind"`
	Kind                          domain.Kind            `json:"kind"`
	SchemaVersion                 string                 `json:"schema_version"`
	AttachmentRootContractVersion string                 `json:"attachment_root_contract_version"`
	ContentPolicy                 domain.RedactionPolicy `json:"content_policy"`
	Status                        domain.Status          `json:"status"`
	Version                       int64                  `json:"version"`
	ManifestSHA256                *string                `json:"manifest_sha256,omitempty"`
	EntryCount                    *int64                 `json:"entry_count,omitempty"`
	TotalUncompressedBytes        *int64                 `json:"total_uncompressed_bytes,omitempty"`
	ArchiveSHA256                 *string                `json:"archive_sha256,omitempty"`
	ArchiveSize                   *int64                 `json:"archive_size,omitempty"`
	ErrorCode                     string                 `json:"error_code,omitempty"`
	ErrorMessage                  string                 `json:"error_message,omitempty"`
	AttemptCount                  int                    `json:"attempt_count"`
	ExpiresAt                     time.Time              `json:"expires_at"`
	CreatedAt                     time.Time              `json:"created_at"`
	UpdatedAt                     time.Time              `json:"updated_at"`
	StartedAt                     *time.Time             `json:"started_at,omitempty"`
	CompletedAt                   *time.Time             `json:"completed_at,omitempty"`
	DownloadCount                 int                    `json:"download_count"`
	LastDownloadedAt              *time.Time             `json:"last_downloaded_at,omitempty"`
	DownloadURL                   string                 `json:"download_url,omitempty"`
}

func (handler *Handler) createAttachment(writer nethttp.ResponseWriter, request *nethttp.Request) {
	if !handler.Available() {
		writeUnavailable(writer)
		return
	}
	workspaceID, err := attachmentWorkspaceID(request)
	if err != nil {
		writeError(writer, err)
		return
	}
	input, err := decodeAttachmentCreateRequest(request)
	if err != nil {
		writeError(writer, err)
		return
	}
	if input.Kind != domain.KindAttachmentsZIP || input.SchemaVersion != attachmentSchemaVersion ||
		input.AttachmentRootContractVersion != attachmentRootVersion || input.ContentPolicy != domain.RedactionRawUserOwned {
		writeError(writer, invalid("attachment export contract is invalid"))
		return
	}
	key, err := parseIdempotencyKey(request)
	if err != nil {
		writeError(writer, err)
		return
	}
	expiresIn := time.Duration(0)
	if input.ExpiresInSeconds != nil {
		if *input.ExpiresInSeconds < 1 || *input.ExpiresInSeconds > int64(exportapp.MaxTTL/time.Second) {
			writeError(writer, invalid("attachment export expiry is invalid"))
			return
		}
		expiresIn = time.Duration(*input.ExpiresInSeconds) * time.Second
	}
	requestedBy := "local"
	if principal, ok := authhttp.PrincipalFromContext(request.Context()); ok {
		requestedBy = fmt.Sprintf("%s:%s", principal.Kind, principal.ID)
	}
	ctx, cancel := context.WithTimeout(request.Context(), handler.timeout)
	defer cancel()
	result, err := handler.service.Create(ctx, domain.CreateRequest{
		WorkspaceID: workspaceID, Kind: domain.KindAttachmentsZIP, SchemaVersion: attachmentSchemaVersion,
		Scope:     domain.Scope{Kind: domain.ScopeWorkspaceAttachments, AttachmentRootContractVersion: attachmentRootVersion},
		Redaction: domain.RedactionRawUserOwned, IdempotencyKey: key, RequestedBy: requestedBy,
		PermissionScope: string(capability.ReadLocal), ExpiresIn: expiresIn,
	})
	if err != nil {
		writeError(writer, err)
		return
	}
	if err := validateAttachmentJob(result.Job, workspaceID, result.Job.ID); err != nil {
		writeError(writer, err)
		return
	}
	response := attachmentCreateResponse{
		Job: toAttachmentJobResponse(result.Job), Replayed: result.Replayed, DispatchPending: result.DispatchPending,
	}
	writer.Header().Set("Location", attachmentJobURL(result.Job))
	writer.Header().Set("Cache-Control", "no-store")
	status := nethttp.StatusAccepted
	if result.Replayed {
		status = nethttp.StatusOK
	}
	httpapi.WriteJSON(writer, status, response)
}

func (handler *Handler) listAttachments(writer nethttp.ResponseWriter, request *nethttp.Request) {
	if !handler.Available() {
		writeUnavailable(writer)
		return
	}
	workspaceID, err := parseID(chi.URLParam(request, "workspace_id"))
	if err != nil {
		writeError(writer, err)
		return
	}
	values, err := parseQuery(request, "limit", "cursor")
	if err != nil {
		writeError(writer, err)
		return
	}
	limit := exportapp.DefaultListLimit
	if raw := values.Get("limit"); raw != "" {
		parsed, parseErr := strconv.Atoi(raw)
		if parseErr != nil || parsed < 1 || parsed > exportapp.MaxListLimit {
			writeError(writer, invalid("attachment export list limit is invalid"))
			return
		}
		limit = parsed
	}
	cursor := values.Get("cursor")
	if len(cursor) > maxCursorBytes {
		writeError(writer, invalid("attachment export list cursor is invalid"))
		return
	}
	ctx, cancel := context.WithTimeout(request.Context(), handler.timeout)
	defer cancel()
	page, err := handler.service.List(ctx, exportapp.ListQuery{
		WorkspaceID: workspaceID, ScopeKind: domain.ScopeWorkspaceAttachments, Limit: limit, Cursor: cursor,
	})
	if err != nil {
		writeError(writer, err)
		return
	}
	response := attachmentListResponse{
		WorkspaceID: string(workspaceID), ScopeKind: domain.ScopeWorkspaceAttachments,
		Items: make([]attachmentJobResponse, 0, len(page.Items)),
	}
	for _, job := range page.Items {
		if err := validateAttachmentJob(job, workspaceID, job.ID); err != nil {
			writeError(writer, err)
			return
		}
		response.Items = append(response.Items, toAttachmentJobResponse(job))
	}
	if len(page.NextCursor) > maxCursorBytes {
		writeError(writer, invalidResult("attachment export list returned an invalid cursor"))
		return
	}
	if page.NextCursor != "" {
		response.NextCursor = &page.NextCursor
	}
	writer.Header().Set("Cache-Control", "no-store")
	httpapi.WriteJSON(writer, nethttp.StatusOK, response)
}

func (handler *Handler) getAttachment(writer nethttp.ResponseWriter, request *nethttp.Request) {
	if !handler.Available() {
		writeUnavailable(writer)
		return
	}
	workspaceID, exportID, err := attachmentJobIDs(request)
	if err != nil {
		writeError(writer, err)
		return
	}
	ctx, cancel := context.WithTimeout(request.Context(), handler.timeout)
	defer cancel()
	job, err := handler.service.GetForScope(ctx, workspaceID, exportID, domain.ScopeWorkspaceAttachments)
	if err != nil {
		writeError(writer, err)
		return
	}
	if err := validateAttachmentJob(job, workspaceID, exportID); err != nil {
		writeError(writer, err)
		return
	}
	writer.Header().Set("Cache-Control", "no-store")
	httpapi.WriteJSON(writer, nethttp.StatusOK, toAttachmentJobResponse(job))
}

func (handler *Handler) downloadAttachment(writer nethttp.ResponseWriter, request *nethttp.Request) {
	if !handler.Available() {
		writeUnavailable(writer)
		return
	}
	workspaceID, exportID, err := attachmentJobIDs(request)
	if err != nil {
		writeError(writer, err)
		return
	}
	actor, err := downloadActor(request.Context())
	if err != nil {
		writeError(writer, err)
		return
	}
	job, content, err := handler.service.DownloadAsForScope(request.Context(), workspaceID, exportID, domain.ScopeWorkspaceAttachments, actor)
	if err != nil {
		writeError(writer, err)
		return
	}
	if content == nil {
		writeError(writer, invalidResult("attachment export download returned a nil result stream"))
		return
	}
	defer content.Close()
	if err := validateAttachmentJob(job, workspaceID, exportID); err != nil || job.Status != domain.StatusSucceeded {
		if err == nil {
			err = invalidResult("attachment export download returned an inconsistent result")
		}
		writeError(writer, err)
		return
	}
	filename := fmt.Sprintf("workspace-attachments-%s.zip", job.ID)
	writer.Header().Set("Content-Type", "application/zip")
	writer.Header().Set("Content-Disposition", `attachment; filename="`+filename+`"`)
	writer.Header().Set("Content-Length", strconv.FormatInt(job.FileSize, 10))
	writer.Header().Set("Cache-Control", "private, no-store")
	writer.Header().Set("X-Content-Type-Options", "nosniff")
	writer.WriteHeader(nethttp.StatusOK)
	_, _ = io.CopyN(writer, content, job.FileSize)
}

func decodeAttachmentCreateRequest(request *nethttp.Request) (attachmentCreateRequest, error) {
	mediaType, _, err := mime.ParseMediaType(request.Header.Get("Content-Type"))
	if err != nil || !strings.EqualFold(mediaType, "application/json") {
		return attachmentCreateRequest{}, foundation.NewError(foundation.ErrorInvalidInput, "UNSUPPORTED_MEDIA_TYPE", false, errors.New("request requires application/json"))
	}
	body, err := io.ReadAll(io.LimitReader(request.Body, maxRequestBodyBytes+1))
	if err != nil || len(body) == 0 || len(body) > maxRequestBodyBytes {
		return attachmentCreateRequest{}, foundation.NewError(foundation.ErrorInvalidInput, "INVALID_JSON", false, errors.New("request JSON is empty or oversized"))
	}
	limits := strictjson.DefaultLimits()
	limits.MaxDocumentBytes = maxRequestBodyBytes
	decoded, err := strictjson.DecodeObject[attachmentCreateRequest](body, limits, nil)
	if err != nil {
		return attachmentCreateRequest{}, foundation.NewError(foundation.ErrorInvalidInput, "INVALID_JSON", false, err)
	}
	return decoded, nil
}

func attachmentWorkspaceID(request *nethttp.Request) (foundation.ID, error) {
	if _, err := parseQuery(request); err != nil {
		return "", err
	}
	return parseID(chi.URLParam(request, "workspace_id"))
}

func attachmentJobIDs(request *nethttp.Request) (foundation.ID, foundation.ID, error) {
	workspaceID, err := attachmentWorkspaceID(request)
	if err != nil {
		return "", "", err
	}
	exportID, err := parseID(chi.URLParam(request, "export_id"))
	return workspaceID, exportID, err
}

func validateAttachmentJob(job domain.Job, workspaceID, exportID foundation.ID) error {
	if err := validateJobResult(job, workspaceID, exportID); err != nil {
		return err
	}
	if job.Scope.Kind != domain.ScopeWorkspaceAttachments || job.Kind != domain.KindAttachmentsZIP ||
		job.SchemaVersion != attachmentSchemaVersion || job.Scope.AttachmentRootContractVersion != attachmentRootVersion ||
		job.Scope.CollectionID != nil || job.Scope.CollectionVersion != nil || job.Scope.QueryHash != "" ||
		len(job.Fields) != 0 || job.Redaction != domain.RedactionRawUserOwned || job.IncludeSensitive {
		return invalidResult("attachment export returned an inconsistent tagged scope")
	}
	return nil
}

func toAttachmentJobResponse(job domain.Job) attachmentJobResponse {
	response := attachmentJobResponse{
		ID: string(job.ID), WorkspaceID: string(job.WorkspaceID), ScopeKind: job.Scope.Kind, Kind: job.Kind,
		SchemaVersion: job.SchemaVersion, AttachmentRootContractVersion: job.Scope.AttachmentRootContractVersion,
		ContentPolicy: job.Redaction, Status: job.Status, Version: job.Version,
		ErrorCode: job.ErrorCode, ErrorMessage: job.ErrorMessage, AttemptCount: job.AttemptCount,
		ExpiresAt: job.ExpiresAt, CreatedAt: job.CreatedAt, UpdatedAt: job.UpdatedAt,
		StartedAt: cloneTime(job.StartedAt), CompletedAt: cloneTime(job.CompletedAt),
		DownloadCount: job.DownloadCount, LastDownloadedAt: cloneTime(job.LastDownloadedAt),
	}
	if job.IsPrepared() {
		response.ManifestSHA256 = cloneString(job.ManifestHash)
		response.EntryCount = cloneInt64(job.EntryCount)
		response.TotalUncompressedBytes = cloneInt64(job.TotalUncompressedBytes)
		response.ArchiveSHA256 = cloneString(job.FileHash)
		response.ArchiveSize = cloneInt64Value(job.FileSize)
	}
	if job.Status == domain.StatusSucceeded {
		response.DownloadURL = attachmentJobURL(job) + "/download"
	}
	return response
}

func cloneInt64Value(value int64) *int64 {
	clone := value
	return &clone
}

func attachmentJobURL(job domain.Job) string {
	return "/api/v1/workspaces/" + url.PathEscape(string(job.WorkspaceID)) + "/attachment-exports/" + url.PathEscape(string(job.ID))
}
