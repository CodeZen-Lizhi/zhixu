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

// CommittedTreeChangeKind 描述两个已提交 Git tree 之间某一路径的最终变更。
type CommittedTreeChangeKind string

const (
	// CommittedTreeChangeUpsert 表示目标提交中的路径需要被捕获或更新。
	CommittedTreeChangeUpsert CommittedTreeChangeKind = "upsert"
	// CommittedTreeChangeDelete 表示路径已从目标提交中删除。
	CommittedTreeChangeDelete CommittedTreeChangeKind = "delete"
)

// CommittedTreeChange 是服务端已验证的 Git 提交范围中一项规范化路径变更。
type CommittedTreeChange struct {
	Kind         CommittedTreeChangeKind
	RelativePath string
}

// CommittedTreeReader 只读取服务端 Workspace 内两个快进提交之间的完整 tree 差异。
type CommittedTreeReader interface {
	ReadCommittedTreeChanges(context.Context, foundation.ID, string, string) ([]CommittedTreeChange, error)
}

// GitInitializer establishes the Git repository required by a Workspace.
// Implementations must never overwrite an existing repository.
type GitInitializer interface {
	Initialize(ctx context.Context, rootPath string) (GitStatus, error)
}
