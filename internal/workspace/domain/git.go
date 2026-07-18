package domain

import (
	"context"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
)

// GitStatus describes the read-only Git baseline observed for a Workspace.
// A missing repository is an expected state represented by Present=false.
type GitStatus struct {
	Present        bool
	RepositoryPath string
	Branch         string
	Head           string
	Dirty          bool
}

// GitStatusReader inspects Git without changing repository or user files.
type GitStatusReader interface {
	Status(ctx context.Context, rootPath string) (GitStatus, error)
}

// CommittedBlob 是从指定 Git Commit 与相对路径读取的确切不可变 bytes。
type CommittedBlob struct {
	WorkspaceID  foundation.ID
	Commit       string
	RelativePath string
	Bytes        []byte
}

// CommittedBlobReader 只允许通过服务端 Workspace、Commit 和受控相对路径读取 Git Blob。
type CommittedBlobReader interface {
	ReadCommittedBlob(context.Context, foundation.ID, string, string) (CommittedBlob, error)
}

// GitInitializer establishes the Git repository required by a Workspace.
// Implementations must never overwrite an existing repository.
type GitInitializer interface {
	Initialize(ctx context.Context, rootPath string) (GitStatus, error)
}
