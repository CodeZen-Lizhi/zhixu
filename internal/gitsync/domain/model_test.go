package domain

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
)

const (
	testWorkspaceID foundation.ID = "76000000-0000-4000-8000-000000000001"
	testRunID       foundation.ID = "76000000-0000-4000-8000-000000000002"
)

func TestTokenIsRedactedAndDestroyClearsOwnedBytes(t *testing.T) {
	token, err := NewToken("top-secret-token")
	if err != nil {
		t.Fatal(err)
	}
	if got := fmt.Sprintf("%v %#v", token, token); strings.Contains(got, "top-secret-token") || !strings.Contains(got, "redacted") {
		t.Fatalf("unsafe token formatting: %q", got)
	}
	bytes := token.Bytes()
	if string(bytes) != "top-secret-token" {
		t.Fatalf("token bytes=%q", bytes)
	}
	clear(bytes)
	token.Destroy()
	if token.Configured() || len(token.Bytes()) != 0 {
		t.Fatal("destroyed token remained configured")
	}
}

func TestSecretActionRejectsAmbiguousShapes(t *testing.T) {
	replacement, err := ReplaceSecret("replacement-token")
	if err != nil || replacement.Validate() != nil {
		t.Fatalf("valid replacement error=%v", err)
	}
	if err := (SecretAction{Kind: SecretActionKeep, Value: replacement.Value}).Validate(); err == nil {
		t.Fatal("keep accepted a token value")
	}
	if err := (SecretAction{Kind: SecretActionReplace}).Validate(); err == nil {
		t.Fatal("replace accepted no token")
	}
}

func TestRemoteConfigAllowsExplicitlyClearedTokenButCannotRun(t *testing.T) {
	now := time.Date(2026, 8, 4, 8, 0, 0, 0, time.UTC)
	config := RemoteConfig{
		WorkspaceID: testWorkspaceID, Configured: true, RemoteURL: "https://git.example.test/team/notes.git",
		Branch: "main", Revision: 2, CreatedAt: now, UpdatedAt: now,
	}
	if err := config.Validate(); err != nil {
		t.Fatalf("cleared-token config should remain readable: %v", err)
	}
	if err := config.ValidateConfigured(); err == nil {
		t.Fatal("cleared-token config was accepted for synchronization")
	}
}

func TestValidateBranchRejectsInvalidGitRefComponents(t *testing.T) {
	for _, branch := range []string{"-topic", "feature//topic", "feature/.hidden", "feature/topic.lock", "feature/foo.lock/bar"} {
		t.Run(branch, func(t *testing.T) {
			if err := ValidateBranch(branch); err == nil {
				t.Fatalf("invalid Git branch %q was accepted", branch)
			}
		})
	}
	if err := ValidateBranch("feature/topic"); err != nil {
		t.Fatalf("valid Git branch was rejected: %v", err)
	}
}

func TestComparisonRejectsInconsistentAncestryAndUnsafePaths(t *testing.T) {
	comparison := Comparison{
		Attached: true, Branch: "main", HeadOID: strings.Repeat("1", 40), RemoteOID: strings.Repeat("2", 40),
		WorktreeClean: true, Relation: RelationRemoteAhead, Behind: 1,
		ChangedFiles: []FileChange{{Path: "docs/note.md", Kind: FileModified}},
	}
	if err := comparison.Validate(); err != nil {
		t.Fatal(err)
	}
	comparison.Ahead = 1
	if err := comparison.Validate(); err == nil {
		t.Fatal("remote-ahead comparison accepted local-ahead count")
	}
	comparison.Ahead = 0
	comparison.ChangedFiles[0].Path = "../outside.md"
	if err := comparison.Validate(); err == nil {
		t.Fatal("comparison accepted path traversal")
	}
}

func TestSyncRunKeepsGitAndIndexResultsIndependent(t *testing.T) {
	now := time.Date(2026, 8, 4, 9, 0, 0, 0, time.UTC)
	completed := now.Add(time.Second)
	run := SyncRun{
		ID: testRunID, WorkspaceID: testWorkspaceID, ConfigRevision: 1,
		RemoteURL: "https://git.example.test/team/notes.git", Branch: "main", Trigger: TriggerManual,
		IdempotencyKey: "manual-1", RequestHash: strings.Repeat("a", 64), Status: RunSucceeded,
		Direction: DirectionPull, FailureClass: FailureNone,
		ExpectedHeadOID: strings.Repeat("1", 40), ExpectedRemoteOID: strings.Repeat("2", 40),
		VerifiedHeadOID: strings.Repeat("2", 40), VerifiedRemoteOID: strings.Repeat("2", 40),
		IndexStatus: IndexFailed, IndexErrorCode: ErrorCodeIndexFailed, IndexRetryable: true,
		AttemptCount: 1, Version: 7, CreatedAt: now, UpdatedAt: completed, CompletedAt: &completed,
	}
	if err := run.Validate(); err != nil {
		t.Fatalf("Git success with failed index follow-up should be valid: %v", err)
	}
	run.VerifiedRemoteOID = strings.Repeat("3", 40)
	if err := run.Validate(); err == nil {
		t.Fatal("successful run accepted unequal verified refs")
	}
}

func TestSyncRunAllowsNoIndexSuccessAndValidatesOptionalIdentity(t *testing.T) {
	now := time.Date(2026, 8, 4, 9, 0, 0, 0, time.UTC)
	completed := now.Add(time.Second)
	run := SyncRun{
		ID: testRunID, WorkspaceID: testWorkspaceID, ConfigRevision: 1,
		RemoteURL: "https://git.example.test/team/notes.git", Branch: "main", Trigger: TriggerManual,
		IdempotencyKey: "manual-index", RequestHash: strings.Repeat("b", 64), Status: RunSucceeded,
		Direction: DirectionPull, FailureClass: FailureNone,
		ExpectedHeadOID: strings.Repeat("1", 40), ExpectedRemoteOID: strings.Repeat("2", 40),
		VerifiedHeadOID: strings.Repeat("2", 40), VerifiedRemoteOID: strings.Repeat("2", 40),
		IndexStatus: IndexSucceeded, AttemptCount: 1, Version: 8,
		CreatedAt: now, UpdatedAt: completed, CompletedAt: &completed,
	}
	if err := run.Validate(); err != nil {
		t.Fatalf("successful no-index follow-up rejected: %v", err)
	}
	run.IndexVersionID = "invalid"
	if err := run.Validate(); err == nil {
		t.Fatal("successful index follow-up accepted an invalid index identity")
	}
	run.IndexVersionID = "30000000-0000-4000-8000-000000000001"
	if err := run.Validate(); err != nil {
		t.Fatalf("valid index identity rejected: %v", err)
	}
	run.IndexStatus = IndexPending
	if err := run.Validate(); err == nil {
		t.Fatal("pending index follow-up accepted a terminal index identity")
	}
}
