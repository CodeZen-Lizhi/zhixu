package application

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"path"
	"strings"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	"github.com/CodeZen-Lizhi/zhixu/internal/workspace/domain"
)

// CaptureCommittedSourceRequest 绑定一次指定 Git Commit Blob 的安全 SourceVersion 捕获。
type CaptureCommittedSourceRequest struct {
	WorkspaceID  foundation.ID
	GitCommit    string
	RelativePath string
	ExpectedHash string
}

// CaptureCommittedSourceVersion 从指定 Commit 捕获确切 bytes 并幂等注册 SourceVersion。
func (s *Service) CaptureCommittedSourceVersion(ctx context.Context, request CaptureCommittedSourceRequest) (domain.SourceRegistrationResult, error) {
	if s == nil || s.dependencies.Repository == nil || s.dependencies.CommittedGit == nil ||
		s.dependencies.CommittedFiles == nil || s.dependencies.IDs == nil || s.dependencies.Clock == nil {
		return domain.SourceRegistrationResult{}, dependencyError("COMMITTED_SOURCE_SERVICE_UNAVAILABLE")
	}
	if err := committedContextError(ctx); err != nil {
		return domain.SourceRegistrationResult{}, err
	}
	mediaType, err := validateCommittedSourceRequest(request)
	if err != nil {
		return domain.SourceRegistrationResult{}, err
	}
	workspace, err := s.dependencies.Repository.GetWorkspaceByID(ctx, request.WorkspaceID)
	if err != nil {
		return domain.SourceRegistrationResult{}, err
	}
	if workspace.ID != request.WorkspaceID || strings.TrimSpace(workspace.RootPath) == "" {
		return domain.SourceRegistrationResult{}, foundation.NewError(foundation.ErrorConsistencyViolation, "COMMITTED_SOURCE_WORKSPACE_BINDING_INVALID", false, errors.New("workspace lookup returned a different or incomplete workspace"))
	}
	blob, err := s.dependencies.CommittedGit.ReadCommittedBlob(ctx, request.WorkspaceID, request.GitCommit, request.RelativePath)
	if err != nil {
		return domain.SourceRegistrationResult{}, err
	}
	if blob.WorkspaceID != request.WorkspaceID || blob.Commit != request.GitCommit || blob.RelativePath != request.RelativePath {
		return domain.SourceRegistrationResult{}, foundation.NewError(foundation.ErrorConsistencyViolation, "COMMITTED_SOURCE_BLOB_BINDING_INVALID", false, errors.New("git reader returned a different blob binding"))
	}
	if int64(len(blob.Bytes)) > domain.MaxCommittedSourceBytes {
		return domain.SourceRegistrationResult{}, foundation.NewError(foundation.ErrorInvalidInput, "SOURCE_FILE_TOO_LARGE", false, errors.New("committed source exceeds the supported size"))
	}
	digest := sha256.Sum256(blob.Bytes)
	actualHash := hex.EncodeToString(digest[:])
	if actualHash != request.ExpectedHash {
		return domain.SourceRegistrationResult{}, foundation.NewError(foundation.ErrorVersionConflict, "SOURCE_RESULT_HASH_CONFLICT", false, errors.New("committed blob hash does not match writeback result"))
	}
	capture, err := s.dependencies.CommittedFiles.CaptureCommitted(ctx, workspace.RootPath, request.RelativePath, blob.Bytes, request.ExpectedHash)
	if err != nil {
		return domain.SourceRegistrationResult{}, err
	}
	if capture.ContentHash != request.ExpectedHash || capture.ByteSize != int64(len(blob.Bytes)) || strings.TrimSpace(capture.ManagedLocation) == "" {
		return domain.SourceRegistrationResult{}, foundation.NewError(foundation.ErrorConsistencyViolation, "COMMITTED_SOURCE_CAPTURE_BINDING_INVALID", false, errors.New("content store returned a different artifact binding"))
	}
	sourceID, err := s.dependencies.IDs.New()
	if err != nil {
		return domain.SourceRegistrationResult{}, err
	}
	versionID, err := s.dependencies.IDs.New()
	if err != nil {
		return domain.SourceRegistrationResult{}, err
	}
	artifactID, err := s.dependencies.IDs.New()
	if err != nil {
		return domain.SourceRegistrationResult{}, err
	}
	now := s.dependencies.Clock.Now()
	registration := domain.SourceRegistration{
		Source: domain.Source{
			ID: sourceID, WorkspaceID: workspace.ID, Type: sourceType(mediaType),
			LogicalName: path.Base(request.RelativePath), OriginalLocation: request.RelativePath, CreatedAt: now,
		},
		Artifact: domain.ContentArtifact{
			ID: artifactID, WorkspaceID: workspace.ID, ContentHash: capture.ContentHash,
			ByteSize: capture.ByteSize, ManagedLocation: capture.ManagedLocation, CreatedAt: now,
		},
		Version: domain.SourceVersion{
			ID: versionID, SourceID: sourceID, ContentArtifactID: artifactID,
			ContentHash: request.ExpectedHash, ByteSize: int64(len(blob.Bytes)), MediaType: mediaType,
			OriginalContentLocation: request.RelativePath, SecurityStatus: "pending", CapturedAt: now,
		},
	}
	result, err := s.dependencies.Repository.RegisterSourceVersion(ctx, registration)
	if err != nil {
		return domain.SourceRegistrationResult{}, err
	}
	if err := validateCommittedRegistrationResult(workspace.ID, request, mediaType, capture, result); err != nil {
		return domain.SourceRegistrationResult{}, err
	}
	return result, nil
}

func validateCommittedSourceRequest(request CaptureCommittedSourceRequest) (string, error) {
	parsedWorkspaceID, err := foundation.ParseID(string(request.WorkspaceID))
	if err != nil || parsedWorkspaceID != request.WorkspaceID || !lowerHex(request.GitCommit, 40) && !lowerHex(request.GitCommit, 64) ||
		!lowerHex(request.ExpectedHash, 64) || !canonicalCommittedPath(request.RelativePath) {
		return "", foundation.NewError(foundation.ErrorInvalidInput, "COMMITTED_SOURCE_REQUEST_INVALID", false, errors.New("committed source request is not canonical"))
	}
	switch strings.ToLower(path.Ext(request.RelativePath)) {
	case ".md", ".markdown":
		return "text/markdown", nil
	case ".txt":
		return "text/plain", nil
	default:
		return "", foundation.NewError(foundation.ErrorInvalidInput, "SOURCE_EXTENSION_UNSUPPORTED", false, errors.New("source extension is not supported by ingestion"))
	}
}

func validateCommittedRegistrationResult(workspaceID foundation.ID, request CaptureCommittedSourceRequest, mediaType string, capture domain.ContentCapture, result domain.SourceRegistrationResult) error {
	if result.Source.ID == "" || result.Source.WorkspaceID != workspaceID || result.Source.OriginalLocation != request.RelativePath ||
		result.Artifact.ID == "" || result.Artifact.WorkspaceID != workspaceID || result.Artifact.ContentHash != request.ExpectedHash ||
		result.Artifact.ByteSize != capture.ByteSize || result.Artifact.ManagedLocation != capture.ManagedLocation ||
		result.Version.ID == "" || result.Version.SourceID != result.Source.ID || result.Version.ContentArtifactID != result.Artifact.ID ||
		result.Version.ContentHash != request.ExpectedHash || result.Version.ByteSize != capture.ByteSize ||
		result.Version.MediaType != mediaType || result.Version.OriginalContentLocation != request.RelativePath {
		return foundation.NewError(foundation.ErrorConsistencyViolation, "COMMITTED_SOURCE_REGISTRATION_BINDING_INVALID", false, errors.New("repository returned a different source registration"))
	}
	return nil
}

func canonicalCommittedPath(value string) bool {
	return value != "" && strings.TrimSpace(value) == value && !strings.ContainsRune(value, '\x00') &&
		!strings.Contains(value, "\\") && !strings.HasPrefix(value, "/") && path.Clean(value) == value &&
		value != "." && value != ".." && !strings.HasPrefix(value, "../")
}

func lowerHex(value string, size int) bool {
	if len(value) != size || strings.ToLower(value) != value {
		return false
	}
	for _, character := range value {
		if !strings.ContainsRune("0123456789abcdef", character) {
			return false
		}
	}
	return true
}

func committedContextError(ctx context.Context) error {
	if ctx == nil || ctx.Err() == nil {
		return nil
	}
	return foundation.NewError(foundation.ErrorNonRetryableFailure, "COMMITTED_SOURCE_CAPTURE_CANCELLED", false, ctx.Err())
}
