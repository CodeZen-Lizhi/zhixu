package postgres

import (
	"strings"
	"testing"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/agent/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
)

func TestSameModelRunResultRejectsDifferentMemoryContext(t *testing.T) {
	completedAt := time.Unix(1, 0).UTC()
	existing := domain.ModelRun{
		MemoryContext: domain.RAGMemoryContextRef{
			SnapshotID:    foundation.ID("68000000-0000-4000-8000-000000000001"),
			SchemaVersion: domain.RAGMemoryContextSchemaVersion,
			Digest:        strings.Repeat("a", 64),
			ItemCount:     1,
			ByteCount:     64,
		},
		Status:      domain.ModelRunRefused,
		Version:     2,
		CreatedAt:   completedAt.Add(-time.Second),
		UpdatedAt:   completedAt,
		CompletedAt: &completedAt,
	}
	if !sameModelRunResult(existing, existing) {
		t.Fatal("identical terminal model run was not accepted as an exact replay")
	}
	replayed := existing
	replayed.MemoryContext.Digest = strings.Repeat("b", 64)

	if sameModelRunResult(existing, replayed) {
		t.Fatal("terminal model run replay accepted a different memory context")
	}
}
