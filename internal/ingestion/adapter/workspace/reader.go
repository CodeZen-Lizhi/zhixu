// Package ingestionworkspace 组合 Workspace Repository 与安全文件读取边界。
package ingestionworkspace

import (
	"context"
	"errors"
	"strings"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	"github.com/CodeZen-Lizhi/zhixu/internal/ingestion/application"
	workspacedomain "github.com/CodeZen-Lizhi/zhixu/internal/workspace/domain"
)

// Reader 将不可变 Content Artifact 转换为 Ingestion Parser 输入。
type Reader struct {
	repository workspacedomain.SourceMaterialRepository
	files      workspacedomain.FileScanner
}

// NewReader 创建 SourceContentReader；依赖缺失时明确失败，不启用路径回退。
func NewReader(repository workspacedomain.SourceMaterialRepository, files workspacedomain.FileScanner) (*Reader, error) {
	if repository == nil || files == nil {
		return nil, foundation.NewError(foundation.ErrorDependencyUnavailable, "INGESTION_SOURCE_READER_UNAVAILABLE", false, errors.New("source material repository and file scanner are required"))
	}
	return &Reader{repository: repository, files: files}, nil
}

// GetSourceMetadata 读取创建 Attempt 所需的可信 Source 契约，不触碰文件内容。
func (r *Reader) GetSourceMetadata(ctx context.Context, sourceVersionID foundation.ID) (application.SourceMetadata, error) {
	if r == nil || r.repository == nil || r.files == nil {
		return application.SourceMetadata{}, foundation.NewError(foundation.ErrorDependencyUnavailable, "INGESTION_SOURCE_READER_UNAVAILABLE", false, errors.New("source content reader is unavailable"))
	}
	if sourceVersionID == "" {
		return application.SourceMetadata{}, foundation.NewError(foundation.ErrorInvalidInput, "SOURCE_VERSION_ID_INVALID", false, errors.New("source version id is required"))
	}
	material, err := r.repository.GetSourceMaterial(ctx, sourceVersionID)
	if err != nil {
		return application.SourceMetadata{}, err
	}
	if err := validateMaterial(material, sourceVersionID); err != nil {
		return application.SourceMetadata{}, err
	}
	return metadataFromMaterial(material), nil
}

// ReadSourceContent 在 Attempt 已创建后重读并校验不可变 Content Artifact。
func (r *Reader) ReadSourceContent(ctx context.Context, expected application.SourceMetadata) ([]byte, error) {
	if r == nil || r.repository == nil || r.files == nil {
		return nil, foundation.NewError(foundation.ErrorDependencyUnavailable, "INGESTION_SOURCE_READER_UNAVAILABLE", false, errors.New("source content reader is unavailable"))
	}
	material, err := r.repository.GetSourceMaterial(ctx, expected.SourceVersionID)
	if err != nil {
		return nil, err
	}
	if err := validateMaterial(material, expected.SourceVersionID); err != nil {
		return nil, err
	}
	if actual := metadataFromMaterial(material); actual != expected {
		return nil, foundation.NewError(foundation.ErrorConsistencyViolation, "INGESTION_SOURCE_METADATA_CONFLICT", false, errors.New("source metadata changed between attempt creation and content read"))
	}
	content, err := r.files.ReadArtifact(ctx, material.WorkspaceRootPath, material.ContentArtifact)
	if err != nil {
		return nil, err
	}
	return content, nil
}

func metadataFromMaterial(material workspacedomain.SourceMaterial) application.SourceMetadata {
	return application.SourceMetadata{
		WorkspaceID: material.WorkspaceID, SourceVersionID: material.SourceVersion.ID,
		ContentArtifactID: material.ContentArtifact.ID, MediaType: material.SourceVersion.MediaType,
		ByteSize: material.ContentArtifact.ByteSize,
	}
}

func validateMaterial(material workspacedomain.SourceMaterial, requestedID foundation.ID) error {
	version := material.SourceVersion
	artifact := material.ContentArtifact
	if material.WorkspaceID == "" || strings.TrimSpace(material.WorkspaceRootPath) == "" || material.SourceID == "" ||
		version.ID != requestedID || version.SourceID != material.SourceID || version.ContentArtifactID == "" ||
		artifact.ID != version.ContentArtifactID || artifact.WorkspaceID != material.WorkspaceID {
		return foundation.NewError(foundation.ErrorConsistencyViolation, "INGESTION_SOURCE_SCOPE_INVALID", false, errors.New("source material scope is incomplete or inconsistent"))
	}
	if strings.TrimSpace(version.MediaType) == "" || version.ContentHash != artifact.ContentHash || version.ByteSize != artifact.ByteSize {
		return foundation.NewError(foundation.ErrorConsistencyViolation, "INGESTION_SOURCE_METADATA_CONFLICT", false, errors.New("source version and content artifact metadata differ"))
	}
	return nil
}

var _ application.SourceContentReader = (*Reader)(nil)
