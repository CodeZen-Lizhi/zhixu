package domain

import "context"

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

// GitInitializer establishes the Git repository required by a Workspace.
// Implementations must never overwrite an existing repository.
type GitInitializer interface {
	Initialize(ctx context.Context, rootPath string) (GitStatus, error)
}
