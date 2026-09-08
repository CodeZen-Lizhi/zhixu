package migration

import (
	"strings"
	"testing"

	"ariga.io/atlas/sql/migrate"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/riverqueue/river/rivermigrate"
)

func TestNewRunnerRejectsMissingDependencies(t *testing.T) {
	if _, err := NewAtlasRunner(nil, nil); err == nil {
		t.Fatal("nil pool was accepted")
	}
	pool := &pgxpool.Pool{}
	if _, err := NewAtlasRunner(pool, nil); err == nil {
		t.Fatal("nil atlas directory was accepted")
	}
}

func TestValidationMessage(t *testing.T) {
	if got := validationMessage(nil); got != "empty validation result" {
		t.Fatalf("nil validation message=%q", got)
	}
	if got := validationMessage(&rivermigrate.ValidateResult{}); got != "validation failed without details" {
		t.Fatalf("empty validation message=%q", got)
	}
	if got := validationMessage(&rivermigrate.ValidateResult{Messages: []string{"one", "two"}}); got != "one; two" {
		t.Fatalf("validation message=%q", got)
	}
}

func TestValidateAtlasGooseTransition(t *testing.T) {
	newRevision := func(version, operator string) *migrate.Revision {
		return &migrate.Revision{
			Version:         version,
			OperatorVersion: operator,
			Applied:         1,
			Total:           1,
		}
	}

	tests := []struct {
		name      string
		revisions []*migrate.Revision
		applied   map[int64]bool
		wantError bool
	}{
		{
			name: "adoption in progress",
			revisions: []*migrate.Revision{
				newRevision("00001", adoptOperatorVersion),
				newRevision("00002", atlasOperatorVersion),
			},
			applied: map[int64]bool{1: true},
		},
		{
			name: "missing adoption marker",
			revisions: []*migrate.Revision{
				newRevision("00001", atlasOperatorVersion),
			},
			applied:   map[int64]bool{1: true},
			wantError: true,
		},
		{
			name: "goose version is not adopted",
			revisions: []*migrate.Revision{
				newRevision("00001", adoptOperatorVersion),
			},
			applied:   map[int64]bool{2: true},
			wantError: true,
		},
		{
			name: "goose version was executed by Atlas",
			revisions: []*migrate.Revision{
				newRevision("00001", atlasOperatorVersion),
				newRevision("00002", adoptOperatorVersion),
			},
			applied:   map[int64]bool{1: true},
			wantError: true,
		},
		{
			name: "post-adoption revision is present",
			revisions: []*migrate.Revision{
				newRevision("00001", adoptOperatorVersion),
				newRevision("00092", atlasOperatorVersion),
			},
			applied:   map[int64]bool{1: true},
			wantError: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateAtlasGooseTransition(tt.revisions, tt.applied)
			if tt.wantError {
				if err == nil || !strings.Contains(err.Error(), conflictingHistoryError) {
					t.Fatalf("error=%v, want %s", err, conflictingHistoryError)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
		})
	}
}

func TestProposalRevisionCompatibilityRequiresExactTransactionalFiles(t *testing.T) {
	historical := migrate.NewLocalFile(proposalRevisionMigrationName, []byte("SELECT 1;"))
	compatibility := migrate.NewLocalFile(proposalRevisionCompatibilityName, []byte("SELECT 1;"))
	for _, test := range []struct {
		name      string
		file      migrate.File
		files     []migrate.File
		wantFile  bool
		wantError string
	}{
		{name: "unrelated migration", file: migrate.NewLocalFile("00081_other.sql", nil)},
		{name: "exact transaction pair", file: historical, files: []migrate.File{compatibility}, wantFile: true},
		{name: "missing compatibility", file: historical, wantError: "COMPATIBILITY_MISSING"},
		{name: "wrong compatibility name", file: historical, files: []migrate.File{migrate.NewLocalFile("00093_other.sql", nil)}, wantError: "COMPATIBILITY_MISSING"},
		{name: "nontransactional historical file", file: migrate.NewLocalFile(proposalRevisionMigrationName, []byte("-- atlas:txmode none\n\nSELECT 1;")), wantError: "REQUIRES_TRANSACTION"},
		{name: "nontransactional compatibility", file: historical, files: []migrate.File{migrate.NewLocalFile(proposalRevisionCompatibilityName, []byte("-- atlas:txmode none\n\nSELECT 1;"))}, wantError: "COMPATIBILITY_MISSING"},
	} {
		t.Run(test.name, func(t *testing.T) {
			dir := &migrate.MemDir{}
			if err := dir.CopyFiles(test.files); err != nil {
				t.Fatal(err)
			}
			got, err := proposalRevisionBackfillCompatibility(dir, test.file)
			if test.wantError != "" {
				if err == nil || !strings.Contains(err.Error(), test.wantError) {
					t.Fatalf("error=%v, want %s", err, test.wantError)
				}
				return
			}
			if err != nil || (got != nil) != test.wantFile {
				t.Fatalf("compatibility=%v error=%v", got, err)
			}
		})
	}
}
