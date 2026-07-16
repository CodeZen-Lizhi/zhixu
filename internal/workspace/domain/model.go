package domain

import (
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
)

// WorkspaceStatus is the persisted lifecycle state of a Workspace.
type WorkspaceStatus string

const (
	// WorkspaceStatusActive marks the single Workspace currently available to users.
	WorkspaceStatusActive WorkspaceStatus = "active"
)

// GitBaseline records the repository state observed when a Workspace is opened.
type GitBaseline struct {
	RepositoryPath string
	Branch         string
	Head           string
	Dirty          bool
	CheckedAt      time.Time
}

// Workspace is the stable database mapping for a user-controlled file root.
type Workspace struct {
	ID        foundation.ID
	Name      string
	RootPath  string
	Git       GitBaseline
	Status    WorkspaceStatus
	Version   int64
	CreatedAt time.Time
	UpdatedAt time.Time
}

// Source is one logical imported resource. Its path is metadata, not identity.
type Source struct {
	ID               foundation.ID
	WorkspaceID      foundation.ID
	Type             string
	LogicalName      string
	OriginalLocation string
	CreatedAt        time.Time
}

// ContentArtifact is an immutable managed copy of the exact source bytes.
type ContentArtifact struct {
	ID              foundation.ID
	WorkspaceID     foundation.ID
	ContentHash     string
	ByteSize        int64
	ManagedLocation string
	CreatedAt       time.Time
}

// SourceVersion is one immutable captured content version of a Source.
type SourceVersion struct {
	ID                      foundation.ID
	SourceID                foundation.ID
	ContentArtifactID       foundation.ID
	ContentHash             string
	ByteSize                int64
	MediaType               string
	OriginalContentLocation string
	SecurityStatus          string
	ParserVersion           string
	CapturedAt              time.Time
}

// SourceMaterial 是 Parser 重读不可变 SourceVersion 所需的只读聚合。
// Workspace 根路径只用于受控 FileScanner，不应暴露到 HTTP 或 Parser。
type SourceMaterial struct {
	WorkspaceID       foundation.ID
	WorkspaceRootPath string
	SourceID          foundation.ID
	SourceVersion     SourceVersion
	ContentArtifact   ContentArtifact
}

// SourceRegistration supplies candidate IDs for a new Source and SourceVersion.
// Existing records keep their original IDs when the stable location or hash matches.
type SourceRegistration struct {
	Source   Source
	Artifact ContentArtifact
	Version  SourceVersion
}

// SourceRegistrationResult reports the persisted records and whether a version was created.
type SourceRegistrationResult struct {
	Source          Source
	Artifact        ContentArtifact
	Version         SourceVersion
	ArtifactCreated bool
	Created         bool
}
