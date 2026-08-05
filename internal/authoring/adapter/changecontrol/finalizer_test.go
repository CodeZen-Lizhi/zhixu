package changecontrol

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	authoringapp "github.com/CodeZen-Lizhi/zhixu/internal/authoring/application"
	changecontrolapp "github.com/CodeZen-Lizhi/zhixu/internal/changecontrol/application"
	changecontroldomain "github.com/CodeZen-Lizhi/zhixu/internal/changecontrol/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
)

func TestPublicationFinalizerReconcilesExactProposalRevision(t *testing.T) {
	now := time.Date(2026, 8, 3, 12, 0, 0, 0, time.UTC)
	repository := &publicationRepositoryFake{}
	finalizer, err := NewPublicationFinalizer(repository, foundation.FixedClock{Value: now})
	if err != nil {
		t.Fatal(err)
	}
	publication := finalizerPublicationFixture()
	if err := finalizer.FinalizePublication(context.Background(), publication); err != nil {
		t.Fatal(err)
	}
	query := repository.query
	if repository.calls != 1 || query.WorkspaceID != publication.WorkspaceID ||
		query.ProposalID != publication.ProposalID || query.ProposalRevisionID != publication.ProposalRevisionID ||
		query.DocumentID != "" || query.Limit != 1 || !query.Now.Equal(now) || repository.restoreCalls != 1 ||
		repository.restoreRecord.WritebackID != publication.WritebackID || !repository.restoreRecord.PublishedAt.Equal(now) {
		t.Fatalf("calls=%d query=%#v", repository.calls, query)
	}
}

func TestPublicationFinalizerValidatesRestoreOwnerFacts(t *testing.T) {
	current := strings.Repeat("a", 64)
	targetContent := "restored\n"
	proposal := changecontroldomain.Proposal{
		ID: authoringAdapterID(24), WorkspaceID: authoringAdapterID(20),
		Type: changecontroldomain.ProposalTypeRestoreDocument, TargetPath: "notes/a.md",
		Revision: changecontroldomain.Revision{
			ID: authoringAdapterID(25), ProposalID: authoringAdapterID(24), TargetPath: "notes/a.md",
			TargetMode: changecontroldomain.TargetModeReplace, BaseHash: current, Content: targetContent,
			RestoreDocument: &changecontroldomain.RestoreDocument{
				WorkspaceID: authoringAdapterID(20), DocumentID: authoringAdapterID(26),
				TargetCommit: strings.Repeat("b", 40), ExpectedHead: strings.Repeat("c", 40),
				ExpectedDocumentVersion: 7, PreviewHash: strings.Repeat("d", 64),
				CurrentContentHash: current, TargetContentHash: changecontroldomain.ComputeContentHash([]byte(targetContent)),
				SchemaVersion: changecontroldomain.RestoreDocumentSchemaVersion,
			},
		},
	}
	repository := &publicationRepositoryFake{}
	finalizer, err := NewPublicationFinalizer(repository, foundation.FixedClock{Value: time.Now()})
	if err != nil {
		t.Fatal(err)
	}
	if err := finalizer.ValidateWritebackPreparation(context.Background(), proposal); err != nil {
		t.Fatal(err)
	}
	if repository.validateCalls != 1 || repository.check.DocumentID != authoringAdapterID(26) ||
		repository.check.ExpectedDocumentVersion != 7 || repository.check.CurrentContentHash != current {
		t.Fatalf("calls=%d check=%#v", repository.validateCalls, repository.check)
	}
}

func TestPublicationFinalizerFailsClosedAndPropagatesRepositoryError(t *testing.T) {
	if _, err := NewPublicationFinalizer(nil, foundation.SystemClock{}); err == nil {
		t.Fatal("expected missing repository rejection")
	}
	cause := errors.New("database unavailable")
	repository := &publicationRepositoryFake{err: cause}
	finalizer, err := NewPublicationFinalizer(repository, foundation.FixedClock{Value: time.Now()})
	if err != nil {
		t.Fatal(err)
	}
	invalid := finalizerPublicationFixture()
	invalid.GitCommit = "invalid"
	if err := finalizer.FinalizePublication(context.Background(), invalid); err == nil || repository.calls != 0 {
		t.Fatalf("invalid error=%v calls=%d", err, repository.calls)
	}
	if err := finalizer.FinalizePublication(context.Background(), finalizerPublicationFixture()); !errors.Is(err, cause) {
		t.Fatalf("error=%v", err)
	}
}

type publicationRepositoryFake struct {
	query         authoringapp.ReconcileQuery
	check         authoringapp.RestoreWritebackCheck
	restoreRecord authoringapp.RestorePublicationRecord
	calls         int
	validateCalls int
	restoreCalls  int
	err           error
}

func (fake *publicationRepositoryFake) ReconcilePublications(_ context.Context, query authoringapp.ReconcileQuery) (int, error) {
	fake.calls++
	fake.query = query
	return 0, fake.err
}

func (fake *publicationRepositoryFake) ValidateRestoreWriteback(_ context.Context, check authoringapp.RestoreWritebackCheck) error {
	fake.validateCalls++
	fake.check = check
	return fake.err
}

func (fake *publicationRepositoryFake) FinalizeRestorePublication(_ context.Context, record authoringapp.RestorePublicationRecord) (bool, error) {
	fake.restoreCalls++
	fake.restoreRecord = record
	return false, fake.err
}

func finalizerPublicationFixture() changecontrolapp.WritebackPublication {
	return changecontrolapp.WritebackPublication{
		WorkspaceID: authoringAdapterID(20), ProposalID: authoringAdapterID(21),
		ProposalRevisionID: authoringAdapterID(22), WritebackID: authoringAdapterID(23),
		GitCommit: strings.Repeat("a", 40), ResultHash: strings.Repeat("b", 64),
	}
}
