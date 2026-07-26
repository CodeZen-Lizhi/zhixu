package postgres

import (
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	artifactapp "github.com/CodeZen-Lizhi/zhixu/internal/artifact/application"
	"github.com/CodeZen-Lizhi/zhixu/internal/artifact/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
)

func TestScanStateAcceptsCanonicalV1Snapshot(t *testing.T) {
	state := postgresTestState(t)
	decoded, err := scanState(postgresStateRow(state))
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(decoded, state) {
		t.Fatalf("decoded state mismatch: got=%+v want=%+v", decoded, state)
	}
}

func TestScanRevisionAcceptsCanonicalHistoricalSnapshot(t *testing.T) {
	revision := postgresTestState(t).Revision
	decoded, err := scanRevision(postgresRevisionRow(revision))
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(decoded, revision) {
		t.Fatalf("decoded revision mismatch: got=%+v want=%+v", decoded, revision)
	}
}

func TestCommandReceiptRejectsTamperedResponseBinding(t *testing.T) {
	state := postgresTestState(t)
	receipt := commandReceipt{
		SchemaVersion:   artifactReceiptSchemaVersion,
		WorkspaceID:     state.Artifact.WorkspaceID,
		ArtifactID:      state.Artifact.ID,
		IdempotencyKey:  "artifact-plan-1",
		RequestHash:     strings.Repeat("a", 64),
		CommandType:     artifactapp.CommandPlan,
		ExpectedVersion: 0,
		Result: artifactapp.CommandResult{
			State:          state,
			CommandVersion: state.Artifact.Version,
			RequestHash:    strings.Repeat("a", 64),
			CommandType:    artifactapp.CommandPlan,
		},
	}
	if err := receipt.validate(); err != nil {
		t.Fatalf("valid receipt rejected: %v", err)
	}
	receipt.Result.CommandVersion++
	if err := receipt.validate(); err == nil {
		t.Fatal("expected tampered receipt to be rejected")
	}
}

func TestCoverageFromSectionsPreservesExplicitEmptyGaps(t *testing.T) {
	sections := []domain.Section{{
		Coverage: domain.Coverage{
			SectionKey: "summary",
			Status:     domain.CoverageCovered,
			Gaps:       []domain.Gap{},
		},
	}}

	coverage := coverageFromSections(sections)
	if len(coverage) != 1 || coverage[0].Gaps == nil || len(coverage[0].Gaps) != 0 {
		t.Fatalf("coverage=%#v", coverage)
	}
	sections[0].Coverage.Gaps = nil
	if coverageFromSections(sections)[0].Gaps != nil {
		t.Fatal("invalid nil gaps should not be normalized into a valid explicit empty slice")
	}
}

func postgresTestState(t *testing.T) artifactapp.State {
	t.Helper()
	now := time.Date(2026, 7, 26, 12, 0, 0, 0, time.UTC)
	artifact, revision, err := domain.PlanArtifact(domain.PlanInput{
		ArtifactID:        postgresTestID(1),
		InitialRevisionID: postgresTestID(2),
		WorkspaceID:       postgresTestID(3),
		Type:              "study-guide",
		Title:             "PostgreSQL artifact",
		ScopeDefinition:   "approved knowledge only",
		CreatedAt:         now,
	})
	if err != nil {
		t.Fatal(err)
	}
	return artifactapp.State{Artifact: artifact, Revision: revision}
}

func postgresTestID(value int) foundation.ID {
	return foundation.ID(fmt.Sprintf("90000000-0000-4000-8000-%012d", value))
}

type postgresFakeStateRow struct{ values []any }

func (row postgresFakeStateRow) Scan(destinations ...any) error {
	if len(destinations) != len(row.values) {
		return fmt.Errorf("scan target count = %d, want %d", len(destinations), len(row.values))
	}
	for index, destination := range destinations {
		reflect.ValueOf(destination).Elem().Set(reflect.ValueOf(row.values[index]))
	}
	return nil
}

func postgresStateRow(state artifactapp.State) postgresFakeStateRow {
	return postgresFakeStateRow{values: []any{
		state.Artifact.ID, state.Artifact.WorkspaceID, state.Artifact.Type, state.Artifact.Title, string(state.Artifact.Status), state.Artifact.ScopeDefinition, marshalJSON(state.Artifact.SourceCoverage), state.Artifact.CurrentRevisionID, state.Artifact.Version, state.Artifact.CreatedAt, state.Artifact.UpdatedAt,
		state.Revision.ID, state.Revision.ArtifactID, state.Revision.RevisionNo, marshalJSON(state.Revision.Outline), marshalJSON(state.Revision.Sections), string(state.Revision.CreatedBy), []byte(nil), state.Revision.ContentHash, state.Revision.CreatedAt, marshalJSON(coverageFromSections(state.Revision.Sections)), []byte("[]"), []byte("[]"), markdownFromSections(state.Revision.Sections), marshalJSON(map[string]string{"schema_version": artifactRevisionSchemaVersion}),
	}}
}

func postgresRevisionRow(revision domain.Revision) postgresFakeStateRow {
	return postgresFakeStateRow{values: []any{
		revision.ID, revision.ArtifactID, revision.RevisionNo, marshalJSON(revision.Outline), marshalJSON(revision.Sections),
		string(revision.CreatedBy), []byte(nil), revision.ContentHash, revision.CreatedAt,
		marshalJSON(coverageFromSections(revision.Sections)), []byte("[]"), []byte("[]"),
		markdownFromSections(revision.Sections), marshalJSON(map[string]string{"schema_version": artifactRevisionSchemaVersion}),
	}}
}
