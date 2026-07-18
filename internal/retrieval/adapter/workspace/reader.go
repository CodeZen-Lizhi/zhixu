// Package retrievalworkspace 通过 Workspace 安全边界读取 Evidence 的不可变 Content Artifact。
package retrievalworkspace

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strings"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	"github.com/CodeZen-Lizhi/zhixu/internal/retrieval/application"
	retrievaldomain "github.com/CodeZen-Lizhi/zhixu/internal/retrieval/domain"
	workspacedomain "github.com/CodeZen-Lizhi/zhixu/internal/workspace/domain"
)

const (
	evidenceArtifactReaderUnavailableCode = "RETRIEVAL_EVIDENCE_ARTIFACT_READER_UNAVAILABLE"
	evidenceReferenceNotFoundCode         = "RETRIEVAL_EVIDENCE_REFERENCE_NOT_FOUND"
	evidenceArtifactInvalidCode           = "RETRIEVAL_EVIDENCE_ARTIFACT_INVALID"
)

// Reader 仅通过 Source Version 身份解析 managed artifact，不接受相对路径输入。
type Reader struct {
	repository workspacedomain.SourceMaterialRepository
	files      workspacedomain.FileScanner
}

// NewReader 创建 EvidenceArtifactReader；依赖缺失时拒绝启动。
func NewReader(repository workspacedomain.SourceMaterialRepository, files workspacedomain.FileScanner) (*Reader, error) {
	if repository == nil || files == nil {
		return nil, unavailable("source material repository and file scanner are required")
	}
	return &Reader{repository: repository, files: files}, nil
}

// ReadEvidenceArtifact 通过受控 managed locator 读取并复核不可变 Artifact。
func (r *Reader) ReadEvidenceArtifact(
	ctx context.Context,
	workspaceID foundation.ID,
	sourceVersionID foundation.ID,
) (application.EvidenceArtifact, error) {
	if r == nil || r.repository == nil || r.files == nil {
		return application.EvidenceArtifact{}, unavailable("evidence artifact reader is unavailable")
	}
	if !validID(workspaceID) || !validID(sourceVersionID) {
		return application.EvidenceArtifact{}, foundation.NewError(
			foundation.ErrorInvalidInput,
			retrievaldomain.ErrorCodeEvidenceReferenceInvalid,
			false,
			errors.New("evidence reference identity is invalid"),
		)
	}
	material, err := r.repository.GetSourceMaterial(ctx, sourceVersionID)
	if err != nil {
		return application.EvidenceArtifact{}, err
	}
	if material.WorkspaceID != workspaceID {
		return application.EvidenceArtifact{}, foundation.NewError(
			foundation.ErrorNotFound,
			evidenceReferenceNotFoundCode,
			false,
			errors.New("evidence reference was not found"),
		)
	}
	if err := validateMaterial(material, sourceVersionID); err != nil {
		return application.EvidenceArtifact{}, err
	}
	content, err := r.files.ReadArtifact(ctx, material.WorkspaceRootPath, material.ContentArtifact)
	if err != nil {
		return application.EvidenceArtifact{}, err
	}
	if int64(len(content)) != material.ContentArtifact.ByteSize || artifactHash(content) != material.ContentArtifact.ContentHash {
		return application.EvidenceArtifact{}, invalidArtifact("content artifact bytes do not match immutable metadata")
	}
	return application.EvidenceArtifact{
		WorkspaceID:       material.WorkspaceID,
		SourceVersionID:   material.SourceVersion.ID,
		ContentArtifactID: material.ContentArtifact.ID,
		ContentHash:       material.ContentArtifact.ContentHash,
		ByteSize:          material.ContentArtifact.ByteSize,
		Bytes:             append([]byte(nil), content...),
	}, nil
}

func validateMaterial(material workspacedomain.SourceMaterial, requestedID foundation.ID) error {
	version := material.SourceVersion
	artifact := material.ContentArtifact
	if !validID(material.WorkspaceID) || strings.TrimSpace(material.WorkspaceRootPath) == "" || !validID(material.SourceID) ||
		version.ID != requestedID || version.SourceID != material.SourceID || !validID(version.ContentArtifactID) ||
		artifact.ID != version.ContentArtifactID || artifact.WorkspaceID != material.WorkspaceID {
		return invalidArtifact("source material scope is incomplete or inconsistent")
	}
	if version.ContentHash != artifact.ContentHash || version.ByteSize != artifact.ByteSize ||
		artifact.ByteSize < 0 || artifact.ByteSize > workspacedomain.MaxCommittedSourceBytes ||
		!canonicalArtifactHash(artifact.ContentHash) || artifact.ManagedLocation != ".knowledge/sources/"+artifact.ContentHash {
		return invalidArtifact("source version and content artifact metadata differ")
	}
	return nil
}

func validID(value foundation.ID) bool {
	parsed, err := foundation.ParseID(string(value))
	return err == nil && parsed == value
}

func artifactHash(content []byte) string {
	digest := sha256.Sum256(content)
	return hex.EncodeToString(digest[:])
}

func canonicalArtifactHash(value string) bool {
	decoded, err := hex.DecodeString(value)
	return err == nil && len(decoded) == sha256.Size && strings.ToLower(value) == value
}

func unavailable(message string) error {
	return foundation.NewError(foundation.ErrorDependencyUnavailable, evidenceArtifactReaderUnavailableCode, false, errors.New(message))
}

func invalidArtifact(message string) error {
	return foundation.NewError(foundation.ErrorConsistencyViolation, evidenceArtifactInvalidCode, false, errors.New(message))
}

var _ application.EvidenceArtifactReader = (*Reader)(nil)
