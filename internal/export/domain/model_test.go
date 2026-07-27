package domain

import (
	"strings"
	"testing"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
)

func TestJobValidateRequiresFullExactlyWhenSensitive(t *testing.T) {
	job := validPendingJobForTest()
	if err := job.Validate(); err != nil {
		t.Fatalf("valid masked job: %v", err)
	}

	fullWithoutSensitive := job
	fullWithoutSensitive.Redaction = RedactionFull
	if err := fullWithoutSensitive.Validate(); err == nil {
		t.Fatal("FULL without include_sensitive was accepted")
	}

	maskedSensitive := job
	maskedSensitive.IncludeSensitive = true
	if err := maskedSensitive.Validate(); err == nil {
		t.Fatal("MASKED with include_sensitive was accepted")
	}

	fullSensitive := job
	fullSensitive.Redaction = RedactionFull
	fullSensitive.IncludeSensitive = true
	if err := fullSensitive.Validate(); err != nil {
		t.Fatalf("FULL sensitive job: %v", err)
	}
}

func TestJobValidatePreparedFailureCanExpireAndClean(t *testing.T) {
	job := validPendingJobForTest()
	startedAt := job.CreatedAt.Add(time.Second)
	preparedAt := startedAt.Add(time.Second)
	completedAt := preparedAt.Add(time.Second)
	exactCount := int64(3)
	job.Status = StatusFailed
	job.Version = 4
	job.UpdatedAt = completedAt
	job.StartedAt = &startedAt
	job.CompletedAt = &completedAt
	job.ReadModelRevision = strings.Repeat("b", 64)
	job.ExactCount = &exactCount
	job.PreparedAt = &preparedAt
	job.PreparedStagingPath = ".knowledge/exports/.staging/20000000-0000-4000-8000-000000000001-00000000000000000000000000000000.md.stage"
	job.FilePath = ".knowledge/exports/20000000-0000-4000-8000-000000000001.md"
	job.FileHash = strings.Repeat("c", 64)
	job.FileSize = 42
	job.ErrorCode = "EXPORT_RESULT_INCONSISTENT"
	job.ErrorMessage = "export generation failed"
	job.LeaseExpiresAt = nil
	if err := job.Validate(); err != nil {
		t.Fatalf("prepared failed job: %v", err)
	}

	expiredAt := completedAt.Add(time.Second)
	job.Status = StatusExpired
	job.Version++
	job.UpdatedAt = expiredAt
	job.CompletedAt = &completedAt
	job.ErrorCode = ""
	job.ErrorMessage = ""
	job.CleanupStatus = CleanupPending
	if err := job.Validate(); err != nil {
		t.Fatalf("expired prepared job: %v", err)
	}

	deletedAt := expiredAt.Add(time.Second)
	job.Version++
	job.UpdatedAt = deletedAt
	job.CleanupStatus = CleanupSucceeded
	job.CleanupAttemptCount = 1
	job.CleanupUpdatedAt = &deletedAt
	job.FileDeletedAt = &deletedAt
	if err := job.Validate(); err != nil {
		t.Fatalf("cleaned prepared job: %v", err)
	}
}

func TestSameRequestBindsHashAndTTL(t *testing.T) {
	left := validPendingJobForTest()
	right := left
	right.Fields = append([]Field(nil), left.Fields...)
	if !SameRequest(left, right) {
		t.Fatal("identical request was not equal")
	}
	right.RequestTTLSeconds++
	if SameRequest(left, right) {
		t.Fatal("different TTL was treated as the same request")
	}
	right = left
	right.RequestHash = strings.Repeat("d", 64)
	if SameRequest(left, right) {
		t.Fatal("different request hash was treated as the same request")
	}
}

func TestM9OnlyAcceptsDeliveredKindsAndFields(t *testing.T) {
	if ValidKind(Kind("EVALUATION_JSON")) || ValidKind(Kind("AUDIT_JSON")) {
		t.Fatal("deferred export kinds were accepted")
	}
	if ValidField(Field("evaluation")) || ValidField(Field("audit_summary")) {
		t.Fatal("deferred export fields were accepted")
	}
	job := validPendingJobForTest()
	job.Kind = Kind("EVALUATION_JSON")
	if err := job.Validate(); err == nil {
		t.Fatal("job accepted a deferred export kind")
	}
	job = validPendingJobForTest()
	job.Fields = []Field{Field("evaluation")}
	if err := job.Validate(); err == nil {
		t.Fatal("job accepted a deferred export field")
	}
}

func validPendingJobForTest() Job {
	createdAt := time.Date(2026, 7, 26, 8, 0, 0, 0, time.UTC)
	collectionID := foundation.ID("20000000-0000-4000-8000-000000000002")
	collectionVersion := int64(7)
	return Job{
		ID:          foundation.ID("20000000-0000-4000-8000-000000000001"),
		WorkspaceID: foundation.ID("20000000-0000-4000-8000-000000000003"),
		Kind:        KindMarkdown, SchemaVersion: "export/v1",
		Scope:  Scope{CollectionID: &collectionID, CollectionVersion: &collectionVersion, QueryHash: strings.Repeat("a", 64)},
		Fields: []Field{FieldID, FieldTitle}, Redaction: RedactionMasked,
		PermissionScope: "READ_LOCAL", RequestedBy: "USER:test", IdempotencyKey: "export-test",
		RequestHash: strings.Repeat("9", 64), RequestTTLSeconds: 3600,
		Status: StatusPending, Version: 1, ExpiresAt: createdAt.Add(time.Hour),
		CreatedAt: createdAt, UpdatedAt: createdAt, CleanupStatus: CleanupNotRequired,
	}
}
