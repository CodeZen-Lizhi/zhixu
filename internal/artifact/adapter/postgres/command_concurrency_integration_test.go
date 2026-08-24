//go:build integration

package postgres

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	artifactchangecontrol "github.com/CodeZen-Lizhi/zhixu/internal/artifact/adapter/changecontrol"
	artifactlocalfs "github.com/CodeZen-Lizhi/zhixu/internal/artifact/adapter/localfs"
	artifactapp "github.com/CodeZen-Lizhi/zhixu/internal/artifact/application"
	artifactdomain "github.com/CodeZen-Lizhi/zhixu/internal/artifact/domain"
	changecontrolpostgres "github.com/CodeZen-Lizhi/zhixu/internal/changecontrol/adapter/postgres"
	changecontrolapp "github.com/CodeZen-Lizhi/zhixu/internal/changecontrol/application"
	changecontroldomain "github.com/CodeZen-Lizhi/zhixu/internal/changecontrol/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	workspacedomain "github.com/CodeZen-Lizhi/zhixu/internal/workspace/domain"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestGORMArtifactExternalReservationSerializesAndClearsAtomically(t *testing.T) {
	ctx := context.Background()
	repository, platformPool := newArtifactIntegrationGORMRepository(t, ctx)
	pool := platformPool.DB()
	workspaceID := artifactIntegrationID(3951)
	seedArtifactWorkspace(t, ctx, pool, workspaceID, "artifact-gorm-reservation-concurrency")
	base := time.Date(2026, 7, 27, 9, 0, 0, 0, time.UTC)
	approved := createApprovedArtifactForConcurrency(t, ctx, repository, workspaceID, base)
	bindings := [2]artifactapp.CommandBinding{
		artifactIntegrationBinding(workspaceID, approved.Artifact.ID, "gorm-reservation-one", '4', artifactapp.CommandExportMarkdown, approved.Artifact.Version),
		artifactIntegrationBinding(workspaceID, approved.Artifact.ID, "gorm-reservation-two", '5', artifactapp.CommandExportMarkdown, approved.Artifact.Version),
	}
	type reserveOutcome struct {
		binding artifactapp.CommandBinding
		state   artifactapp.State
		err     error
	}
	start := make(chan struct{})
	results := make(chan reserveOutcome, len(bindings))
	for _, binding := range bindings {
		binding := binding
		go func() {
			<-start
			state, err := repository.ReserveExternalTransition(ctx, binding)
			results <- reserveOutcome{binding: binding, state: state, err: err}
		}()
	}
	close(start)
	outcomes := [2]reserveOutcome{<-results, <-results}
	winning := -1
	for index, outcome := range outcomes {
		if outcome.err == nil {
			if winning != -1 {
				t.Fatalf("both GORM reservations succeeded: %#v", outcomes)
			}
			winning = index
			if outcome.state.Artifact.ID != approved.Artifact.ID || outcome.state.Artifact.Version != approved.Artifact.Version {
				t.Fatalf("GORM reservation state=%#v", outcome.state)
			}
			continue
		}
		if !artifactIntegrationErrorCode(outcome.err, artifactapp.ErrorCodeVersionConflict) {
			t.Fatalf("GORM reservation failure=%v", outcome.err)
		}
	}
	if winning == -1 {
		t.Fatalf("no GORM reservation succeeded: %#v", outcomes)
	}
	winningBinding := outcomes[winning].binding
	losingBinding := outcomes[1-winning].binding
	if replayedState, err := repository.ReserveExternalTransition(ctx, winningBinding); err != nil || replayedState.Artifact.ID != approved.Artifact.ID {
		t.Fatalf("GORM exact reservation replay=%#v err=%v", replayedState, err)
	}
	if probed, err := repository.ProbeExternalTransition(ctx, winningBinding); err != nil || probed.Artifact.Version != approved.Artifact.Version {
		t.Fatalf("GORM exact reservation probe=%#v err=%v", probed, err)
	}
	if _, err := repository.ProbeExternalTransition(ctx, losingBinding); !artifactIntegrationErrorCode(err, artifactapp.ErrorCodeVersionConflict) {
		t.Fatalf("GORM foreign reservation probe err=%v", err)
	}

	exportedArtifact, err := artifactdomain.MarkExported(approved.Artifact, approved.Revision, base.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	exported := artifactapp.State{Artifact: exportedArtifact, Revision: artifactdomain.CloneRevision(approved.Revision)}
	exportRecord := artifactIntegrationExportRecord(exported, artifactIntegrationID(3990), exported.Artifact.UpdatedAt)
	completed, err := repository.Transition(ctx, artifactapp.TransitionRecord{
		Binding: winningBinding, CurrentRevisionID: approved.Revision.ID, State: exported,
		NewRevision: false, Export: &exportRecord,
	})
	if err != nil || completed.Replayed || completed.Export == nil || completed.Export.ID != exportRecord.ID {
		t.Fatalf("GORM reserved transition=%#v err=%v", completed, err)
	}
	assertReservationCount(t, ctx, pool, workspaceID, approved.Artifact.ID, 0)
	if found, replayed, err := repository.FindCommand(ctx, winningBinding); err != nil || !replayed || !found.Replayed || found.Export == nil || found.Export.ID != exportRecord.ID {
		t.Fatalf("GORM export receipt=%#v found=%t err=%v", found, replayed, err)
	}
	if persisted, err := repository.GetExport(ctx, workspaceID, approved.Artifact.ID, exportRecord.ID); err != nil || persisted != exportRecord {
		t.Fatalf("GORM export binding=%#v err=%v", persisted, err)
	}
	if _, err := repository.ReserveExternalTransition(ctx, losingBinding); !artifactIntegrationErrorCode(err, artifactapp.ErrorCodeVersionConflict) {
		t.Fatalf("GORM stale reservation err=%v", err)
	}
	var exports, receipts int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM learning.artifact_export WHERE workspace_id=$1 AND artifact_id=$2`, string(workspaceID), string(approved.Artifact.ID)).Scan(&exports); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM learning.artifact_command WHERE workspace_id=$1 AND artifact_id=$2 AND command_type='EXPORT_MARKDOWN'`, string(workspaceID), string(approved.Artifact.ID)).Scan(&receipts); err != nil {
		t.Fatal(err)
	}
	if exports != 1 || receipts != 1 {
		t.Fatalf("GORM export closure exports=%d receipts=%d", exports, receipts)
	}
}

func TestArtifactCommandsConcurrentExportAndPublishKeepOneDurableBinding(t *testing.T) {
	ctx := context.Background()
	repository, pool := newArtifactIntegrationRepository(t, ctx)
	workspaceID := artifactIntegrationID(401)
	seedArtifactWorkspace(t, ctx, pool, workspaceID, "artifact-command-concurrency")

	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	exporter, err := artifactlocalfs.NewExporter(artifactConcurrencyWorkspaceReader{workspace: workspacedomain.Workspace{ID: workspaceID, RootPath: root}})
	if err != nil {
		t.Fatal(err)
	}
	baseTime := time.Date(2026, 7, 26, 13, 0, 0, 0, time.UTC)
	approved := createApprovedArtifactForConcurrency(t, ctx, repository, workspaceID, baseTime)

	firstExport := artifactCommandServiceForConcurrency(t, repository, exporter, nil, baseTime.Add(time.Minute))
	secondExport := artifactCommandServiceForConcurrency(t, repository, exporter, nil, baseTime.Add(2*time.Minute))
	exportResults := runParallelArtifactCommands(t, func() (artifactapp.CommandResult, error) {
		return firstExport.ExportMarkdown(ctx, artifactapp.RevisionPersistentCommand{
			WorkspaceID: workspaceID, ArtifactID: approved.Artifact.ID, ExpectedVersion: approved.Artifact.Version, IdempotencyKey: "concurrent-export-one",
		})
	}, func() (artifactapp.CommandResult, error) {
		return secondExport.ExportMarkdown(ctx, artifactapp.RevisionPersistentCommand{
			WorkspaceID: workspaceID, ArtifactID: approved.Artifact.ID, ExpectedVersion: approved.Artifact.Version, IdempotencyKey: "concurrent-export-two",
		})
	})
	exported := exactlyOneArtifactCommandSuccess(t, exportResults)
	if exported.Export == nil {
		t.Fatal("successful export omitted the durable export binding")
	}
	assertSingleExportFileBinding(t, ctx, pool, root, *exported.Export)

	changeRepository, err := changecontrolpostgres.NewRepository(pool)
	if err != nil {
		t.Fatal(err)
	}
	changeService, err := changecontrolapp.NewService(
		changeRepository,
		foundation.NewUUIDGenerator(nil),
		foundation.FixedClock{Value: baseTime.Add(3 * time.Minute)},
		artifactConcurrencyTargets{},
		artifactConcurrencyGit{},
	)
	if err != nil {
		t.Fatal(err)
	}
	publicationCreator, err := artifactchangecontrol.NewPublicationCreator(changeService)
	if err != nil {
		t.Fatal(err)
	}
	firstPublish := artifactCommandServiceForConcurrency(t, repository, nil, publicationCreator, baseTime.Add(4*time.Minute))
	secondPublish := artifactCommandServiceForConcurrency(t, repository, nil, publicationCreator, baseTime.Add(5*time.Minute))
	publishResults := runParallelArtifactCommands(t, func() (artifactapp.CommandResult, error) {
		return firstPublish.Publish(ctx, artifactapp.RevisionPersistentCommand{
			WorkspaceID: workspaceID, ArtifactID: approved.Artifact.ID, ExpectedVersion: exported.State.Artifact.Version, IdempotencyKey: "concurrent-publish-one",
		})
	}, func() (artifactapp.CommandResult, error) {
		return secondPublish.Publish(ctx, artifactapp.RevisionPersistentCommand{
			WorkspaceID: workspaceID, ArtifactID: approved.Artifact.ID, ExpectedVersion: exported.State.Artifact.Version, IdempotencyKey: "concurrent-publish-two",
		})
	})
	published := exactlyOneArtifactCommandSuccess(t, publishResults)
	if published.Publication == nil {
		t.Fatal("successful publish omitted the durable publication binding")
	}

	var proposalCount, bindingCount int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM change_control.proposal WHERE workspace_id=$1 AND proposal_type='publish_artifact'`, string(workspaceID)).Scan(&proposalCount); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM learning.artifact_publication WHERE workspace_id=$1 AND artifact_id=$2`, string(workspaceID), string(approved.Artifact.ID)).Scan(&bindingCount); err != nil {
		t.Fatal(err)
	}
	if proposalCount != 1 || bindingCount != 1 {
		t.Fatalf("publish concurrency created proposal_count=%d binding_count=%d", proposalCount, bindingCount)
	}
	var persistedProposalID string
	if err := pool.QueryRow(ctx, `SELECT proposal_id::text FROM learning.artifact_publication WHERE workspace_id=$1 AND artifact_id=$2`, string(workspaceID), string(approved.Artifact.ID)).Scan(&persistedProposalID); err != nil {
		t.Fatal(err)
	}
	if persistedProposalID != string(published.Publication.ProposalID) {
		t.Fatalf("persisted proposal=%q successful proposal=%q", persistedProposalID, published.Publication.ProposalID)
	}

	// A response-loss retry reuses the frozen Change Control proposal, regardless
	// of the caller's Artifact receipt key.
	request := artifactdomain.PublicationRequest{ProposalType: artifactdomain.PublishArtifactProposalType, WorkspaceID: published.Publication.WorkspaceID, ArtifactID: published.Publication.ArtifactID, RevisionID: published.Publication.RevisionID, ArtifactVersion: published.Publication.ArtifactVersion, RevisionNo: published.Publication.RevisionNo, ContentHash: published.Publication.ContentHash, SourceCoverage: published.State.Artifact.SourceCoverage, RequestedAt: published.Publication.CreatedAt}
	replayedProposal, err := publicationCreator.CreateArtifactPublication(ctx, request, "publish-response-loss-retry")
	if err != nil {
		t.Fatal(err)
	}
	if replayedProposal != published.Publication.ProposalID {
		t.Fatalf("response-loss retry proposal=%q successful=%q", replayedProposal, published.Publication.ProposalID)
	}
}

func TestArtifactCommandsConcurrentExportAndPublishLeaveNoUnboundSideEffects(t *testing.T) {
	ctx := context.Background()
	repository, pool := newArtifactIntegrationRepository(t, ctx)
	workspaceID := artifactIntegrationID(402)
	seedArtifactWorkspace(t, ctx, pool, workspaceID, "artifact-cross-command-concurrency")

	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	exporter, err := artifactlocalfs.NewExporter(artifactConcurrencyWorkspaceReader{workspace: workspacedomain.Workspace{ID: workspaceID, RootPath: root}})
	if err != nil {
		t.Fatal(err)
	}
	baseTime := time.Date(2026, 7, 26, 14, 0, 0, 0, time.UTC)
	approved := createApprovedArtifactForConcurrency(t, ctx, repository, workspaceID, baseTime)

	changeRepository, err := changecontrolpostgres.NewRepository(pool)
	if err != nil {
		t.Fatal(err)
	}
	changeService, err := changecontrolapp.NewService(
		changeRepository,
		foundation.NewUUIDGenerator(nil),
		foundation.FixedClock{Value: baseTime.Add(time.Minute)},
		artifactConcurrencyTargets{},
		artifactConcurrencyGit{},
	)
	if err != nil {
		t.Fatal(err)
	}
	publicationCreator, err := artifactchangecontrol.NewPublicationCreator(changeService)
	if err != nil {
		t.Fatal(err)
	}

	gate := &artifactCrossCommandGate{firstArrived: make(chan struct{}), release: make(chan struct{})}
	exportService := artifactCommandServiceForConcurrency(t, repository, &artifactCrossCommandExporter{delegate: exporter, gate: gate}, nil, baseTime.Add(2*time.Minute))
	publishService := artifactCommandServiceForConcurrency(t, repository, nil, &artifactCrossCommandPublisher{delegate: publicationCreator, gate: gate}, baseTime.Add(3*time.Minute))
	type outcome struct {
		result artifactapp.CommandResult
		err    error
	}
	exportDone := make(chan outcome, 1)
	go func() {
		result, callErr := exportService.ExportMarkdown(ctx, artifactapp.RevisionPersistentCommand{
			WorkspaceID: workspaceID, ArtifactID: approved.Artifact.ID, ExpectedVersion: approved.Artifact.Version, IdempotencyKey: "cross-command-export",
		})
		exportDone <- outcome{result: result, err: callErr}
	}()
	select {
	case <-gate.firstArrived:
	case <-time.After(10 * time.Second):
		t.Fatal("export did not reach the external side-effect boundary")
	}
	publishDone := make(chan outcome, 1)
	go func() {
		result, callErr := publishService.Publish(ctx, artifactapp.RevisionPersistentCommand{
			WorkspaceID: workspaceID, ArtifactID: approved.Artifact.ID, ExpectedVersion: approved.Artifact.Version, IdempotencyKey: "cross-command-publish",
		})
		publishDone <- outcome{result: result, err: callErr}
	}()
	var publish outcome
	select {
	case publish = <-publishDone:
	case <-time.After(10 * time.Second):
		t.Fatal("publish did not complete while export was paused")
	}
	close(gate.release)
	var exported outcome
	select {
	case exported = <-exportDone:
	case <-time.After(10 * time.Second):
		t.Fatal("export did not complete after release")
	}
	assertOneCrossCommandSuccess(t, exported.err, publish.err)

	if effects := gate.calls.Load(); effects != 1 {
		t.Fatalf("cross-command concurrency reached %d external side effects; exactly one command must reserve the Artifact version before Export or Publish", effects)
	}
	var exportRecords, proposals, publicationBindings int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM learning.artifact_export WHERE workspace_id=$1 AND artifact_id=$2`, string(workspaceID), string(approved.Artifact.ID)).Scan(&exportRecords); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM change_control.proposal WHERE workspace_id=$1 AND proposal_type='publish_artifact'`, string(workspaceID)).Scan(&proposals); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM learning.artifact_publication WHERE workspace_id=$1 AND artifact_id=$2`, string(workspaceID), string(approved.Artifact.ID)).Scan(&publicationBindings); err != nil {
		t.Fatal(err)
	}
	files, err := filepath.Glob(filepath.Join(root, ".knowledge", "exports", "artifacts", "*", "*.md"))
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != exportRecords {
		t.Fatalf("managed export files=%d durable export records=%d", len(files), exportRecords)
	}
	if proposals != publicationBindings {
		t.Fatalf("publish proposals=%d durable Artifact publication bindings=%d", proposals, publicationBindings)
	}
}

func TestArtifactCommandsConcurrentPublishThenExportBlocksFileSideEffect(t *testing.T) {
	ctx := context.Background()
	repository, pool := newArtifactIntegrationRepository(t, ctx)
	workspaceID := artifactIntegrationID(403)
	seedArtifactWorkspace(t, ctx, pool, workspaceID, "artifact-publish-first-concurrency")
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	exporter, err := artifactlocalfs.NewExporter(artifactConcurrencyWorkspaceReader{workspace: workspacedomain.Workspace{ID: workspaceID, RootPath: root}})
	if err != nil {
		t.Fatal(err)
	}
	baseTime := time.Date(2026, 7, 26, 15, 0, 0, 0, time.UTC)
	approved := createApprovedArtifactForConcurrency(t, ctx, repository, workspaceID, baseTime)
	changeRepository, err := changecontrolpostgres.NewRepository(pool)
	if err != nil {
		t.Fatal(err)
	}
	changeService, err := changecontrolapp.NewService(changeRepository, foundation.NewUUIDGenerator(nil), foundation.FixedClock{Value: baseTime.Add(time.Minute)}, artifactConcurrencyTargets{}, artifactConcurrencyGit{})
	if err != nil {
		t.Fatal(err)
	}
	publicationCreator, err := artifactchangecontrol.NewPublicationCreator(changeService)
	if err != nil {
		t.Fatal(err)
	}
	gate := &artifactCrossCommandGate{firstArrived: make(chan struct{}), release: make(chan struct{})}
	publishService := artifactCommandServiceForConcurrency(t, repository, nil, &artifactCrossCommandPublisher{delegate: publicationCreator, gate: gate}, baseTime.Add(2*time.Minute))
	exportService := artifactCommandServiceForConcurrency(t, repository, &artifactCrossCommandExporter{delegate: exporter, gate: gate}, nil, baseTime.Add(3*time.Minute))
	type outcome struct{ err error }
	publishDone := make(chan outcome, 1)
	go func() {
		_, callErr := publishService.Publish(ctx, artifactapp.RevisionPersistentCommand{WorkspaceID: workspaceID, ArtifactID: approved.Artifact.ID, ExpectedVersion: approved.Artifact.Version, IdempotencyKey: "publish-first"})
		publishDone <- outcome{err: callErr}
	}()
	select {
	case <-gate.firstArrived:
	case <-time.After(10 * time.Second):
		t.Fatal("publish did not reach the external side-effect boundary")
	}
	exportDone := make(chan outcome, 1)
	go func() {
		_, callErr := exportService.ExportMarkdown(ctx, artifactapp.RevisionPersistentCommand{WorkspaceID: workspaceID, ArtifactID: approved.Artifact.ID, ExpectedVersion: approved.Artifact.Version, IdempotencyKey: "export-second"})
		exportDone <- outcome{err: callErr}
	}()
	var exported outcome
	select {
	case exported = <-exportDone:
	case <-time.After(10 * time.Second):
		t.Fatal("export did not fail while publish owned the reservation")
	}
	close(gate.release)
	var published outcome
	select {
	case published = <-publishDone:
	case <-time.After(10 * time.Second):
		t.Fatal("publish did not complete after release")
	}
	assertOneCrossCommandSuccess(t, exported.err, published.err)
	if effects := gate.calls.Load(); effects != 1 {
		t.Fatalf("publish-first concurrency reached %d external side effects", effects)
	}
	files, err := filepath.Glob(filepath.Join(root, ".knowledge", "exports", "artifacts", "*", "*.md"))
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 0 {
		t.Fatalf("export created %d files after Publish reserved the Artifact", len(files))
	}
	var proposals, bindings int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM change_control.proposal WHERE workspace_id=$1 AND proposal_type='publish_artifact'`, string(workspaceID)).Scan(&proposals); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM learning.artifact_publication WHERE workspace_id=$1 AND artifact_id=$2`, string(workspaceID), string(approved.Artifact.ID)).Scan(&bindings); err != nil {
		t.Fatal(err)
	}
	if proposals != 1 || bindings != 1 {
		t.Fatalf("publish proposals=%d bindings=%d", proposals, bindings)
	}
}

func TestArtifactExternalTransitionPreflightRejectsInvalidStateWithoutReservation(t *testing.T) {
	tests := []struct {
		name      string
		workspace foundation.ID
		key       string
		wantCode  string
		run       func(context.Context, *artifactapp.CommandService, artifactapp.RevisionPersistentCommand) (artifactapp.CommandResult, error)
	}{
		{
			name:      "export",
			workspace: artifactIntegrationID(406),
			key:       "invalid-planning-export",
			wantCode:  artifactdomain.ErrorCodeArtifactTransitionInvalid,
			run: func(ctx context.Context, service *artifactapp.CommandService, command artifactapp.RevisionPersistentCommand) (artifactapp.CommandResult, error) {
				return service.ExportMarkdown(ctx, command)
			},
		},
		{
			name:      "publish",
			workspace: artifactIntegrationID(407),
			key:       "invalid-planning-publish",
			wantCode:  artifactdomain.ErrorCodeArtifactPublicationInvalid,
			run: func(ctx context.Context, service *artifactapp.CommandService, command artifactapp.RevisionPersistentCommand) (artifactapp.CommandResult, error) {
				return service.Publish(ctx, command)
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			ctx := context.Background()
			repository, pool := newArtifactIntegrationRepository(t, ctx)
			seedArtifactWorkspace(t, ctx, pool, test.workspace, "artifact-invalid-external-"+test.name)
			base := time.Date(2026, 7, 26, 15, 30, 0, 0, time.UTC)
			exporter := &artifactUnexpectedExporter{}
			publisher := &artifactUnexpectedPublisher{}
			service := artifactCommandServiceForConcurrency(t, repository, exporter, publisher, base)
			planned, err := service.Plan(ctx, artifactapp.PlanCommand{
				WorkspaceID: test.workspace, Type: "study-guide", Title: "Invalid external preflight",
				ScopeDefinition: "approved source boundary", IdempotencyKey: "invalid-external-plan-" + test.name,
			})
			if err != nil {
				t.Fatal(err)
			}
			command := artifactapp.RevisionPersistentCommand{
				WorkspaceID: test.workspace, ArtifactID: planned.State.Artifact.ID,
				ExpectedVersion: planned.State.Artifact.Version, IdempotencyKey: test.key,
			}
			if _, err := test.run(ctx, service, command); !artifactIntegrationErrorCode(err, test.wantCode) {
				t.Fatalf("invalid %s error=%v, want %s", test.name, err, test.wantCode)
			}
			assertReservationCount(t, ctx, pool, test.workspace, planned.State.Artifact.ID, 0)
			if exporter.calls.Load() != 0 || publisher.calls.Load() != 0 {
				t.Fatalf("invalid %s reached external dependency: exporter=%d publisher=%d", test.name, exporter.calls.Load(), publisher.calls.Load())
			}

			outlined, err := service.SubmitOutline(ctx, artifactapp.SubmitOutlinePersistentCommand{
				WorkspaceID: test.workspace, ArtifactID: planned.State.Artifact.ID,
				ExpectedVersion: planned.State.Artifact.Version, IdempotencyKey: "post-invalid-outline-" + test.name,
				Outline: []artifactdomain.OutlineSection{{Key: "summary", Title: "Summary"}},
			})
			if err != nil {
				t.Fatalf("valid command after rejected %s was blocked: %v", test.name, err)
			}
			if outlined.State.Artifact.Status != artifactdomain.StatusOutlineReview {
				t.Fatalf("post-invalid status=%s, want %s", outlined.State.Artifact.Status, artifactdomain.StatusOutlineReview)
			}
		})
	}
}

func TestArtifactExternalTransitionPreflightRejectsInvalidClockWithoutReservation(t *testing.T) {
	tests := []struct {
		name      string
		workspace foundation.ID
		key       string
		run       func(context.Context, *artifactapp.CommandService, artifactapp.RevisionPersistentCommand) (artifactapp.CommandResult, error)
	}{
		{
			name: "export", workspace: artifactIntegrationID(408), key: "invalid-clock-export",
			run: func(ctx context.Context, service *artifactapp.CommandService, command artifactapp.RevisionPersistentCommand) (artifactapp.CommandResult, error) {
				return service.ExportMarkdown(ctx, command)
			},
		},
		{
			name: "publish", workspace: artifactIntegrationID(409), key: "invalid-clock-publish",
			run: func(ctx context.Context, service *artifactapp.CommandService, command artifactapp.RevisionPersistentCommand) (artifactapp.CommandResult, error) {
				return service.Publish(ctx, command)
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			ctx := context.Background()
			repository, pool := newArtifactIntegrationRepository(t, ctx)
			seedArtifactWorkspace(t, ctx, pool, test.workspace, "artifact-invalid-clock-"+test.name)
			base := time.Date(2026, 7, 26, 15, 45, 0, 0, time.UTC)
			approved := createApprovedArtifactForConcurrency(t, ctx, repository, test.workspace, base)
			exporter := &artifactUnexpectedExporter{}
			publisher := &artifactUnexpectedPublisher{}
			service := artifactCommandServiceForConcurrency(t, repository, exporter, publisher, base.Add(-time.Second))
			command := artifactapp.RevisionPersistentCommand{
				WorkspaceID: test.workspace, ArtifactID: approved.Artifact.ID,
				ExpectedVersion: approved.Artifact.Version, IdempotencyKey: test.key,
			}
			if _, err := test.run(ctx, service, command); !artifactIntegrationErrorCode(err, artifactdomain.ErrorCodeArtifactInvalid) {
				t.Fatalf("invalid-clock %s error=%v, want %s", test.name, err, artifactdomain.ErrorCodeArtifactInvalid)
			}
			assertReservationCount(t, ctx, pool, test.workspace, approved.Artifact.ID, 0)
			if exporter.calls.Load() != 0 || publisher.calls.Load() != 0 {
				t.Fatalf("invalid-clock %s reached external dependency: exporter=%d publisher=%d", test.name, exporter.calls.Load(), publisher.calls.Load())
			}
		})
	}
}

func TestArtifactExternalReservationRecoversOnlyExactOwnerAndClearsAtomically(t *testing.T) {
	ctx := context.Background()
	repository, pool := newArtifactIntegrationRepository(t, ctx)
	workspaceID := artifactIntegrationID(404)
	seedArtifactWorkspace(t, ctx, pool, workspaceID, "artifact-reservation-recovery")
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	base := time.Date(2026, 7, 26, 16, 0, 0, 0, time.UTC)
	approved := createApprovedArtifactForConcurrency(t, ctx, repository, workspaceID, base)
	exporter, err := artifactlocalfs.NewExporter(artifactConcurrencyWorkspaceReader{workspace: workspacedomain.Workspace{ID: workspaceID, RootPath: root}})
	if err != nil {
		t.Fatal(err)
	}
	failingExport := &artifactFailOnceExporter{delegate: exporter, err: errors.New("response lost after export")}
	owner := artifactCommandServiceForConcurrency(t, repository, failingExport, nil, base.Add(time.Minute))
	command := artifactapp.RevisionPersistentCommand{WorkspaceID: workspaceID, ArtifactID: approved.Artifact.ID, ExpectedVersion: approved.Artifact.Version, IdempotencyKey: "reservation-export-owner"}
	if _, err := owner.ExportMarkdown(ctx, command); err == nil {
		t.Fatal("expected export response-loss failure")
	}
	assertReservationCount(t, ctx, pool, workspaceID, approved.Artifact.ID, 1)
	if _, err := artifactCommandServiceForConcurrency(t, repository, &artifactCountingExporter{delegate: exporter}, nil, base).ExportMarkdown(ctx, artifactapp.RevisionPersistentCommand{WorkspaceID: workspaceID, ArtifactID: approved.Artifact.ID, ExpectedVersion: approved.Artifact.Version, IdempotencyKey: "reservation-export-other"}); !artifactIntegrationErrorCode(err, artifactapp.ErrorCodeVersionConflict) {
		t.Fatalf("different export key=%v", err)
	}
	if _, err := artifactCommandServiceForConcurrency(t, repository, nil, nil, base).StartRevision(ctx, artifactapp.RevisionPersistentCommand{WorkspaceID: workspaceID, ArtifactID: approved.Artifact.ID, ExpectedVersion: approved.Artifact.Version, IdempotencyKey: "reservation-normal-command"}); !artifactIntegrationErrorCode(err, artifactapp.ErrorCodeVersionConflict) {
		t.Fatalf("ordinary transition=%v", err)
	}
	if state, err := repository.Get(ctx, workspaceID, approved.Artifact.ID); err != nil || state.Artifact.Version != approved.Artifact.Version || state.Artifact.Status != approved.Artifact.Status {
		t.Fatalf("pending reservation changed state=%+v err=%v", state, err)
	}
	completed, err := artifactCommandServiceForConcurrency(t, repository, exporter, nil, base.Add(2*time.Minute)).ExportMarkdown(ctx, command)
	if err != nil || completed.Export == nil {
		t.Fatalf("same-key export retry=%+v err=%v", completed, err)
	}
	assertReservationCount(t, ctx, pool, workspaceID, approved.Artifact.ID, 0)
	assertSingleExportFileBinding(t, ctx, pool, root, *completed.Export)
	replayExporter := &artifactCountingExporter{delegate: exporter}
	replayed, err := artifactCommandServiceForConcurrency(t, repository, replayExporter, nil, base.Add(3*time.Minute)).ExportMarkdown(ctx, command)
	if err != nil || !replayed.Replayed || replayExporter.calls.Load() != 0 {
		t.Fatalf("export receipt replay=%+v calls=%d err=%v", replayed, replayExporter.calls.Load(), err)
	}
	changeRepository, err := changecontrolpostgres.NewRepository(pool)
	if err != nil {
		t.Fatal(err)
	}
	changeService, err := changecontrolapp.NewService(changeRepository, foundation.NewUUIDGenerator(nil), foundation.FixedClock{Value: base.Add(4 * time.Minute)}, artifactConcurrencyTargets{}, artifactConcurrencyGit{})
	if err != nil {
		t.Fatal(err)
	}
	creator, err := artifactchangecontrol.NewPublicationCreator(changeService)
	if err != nil {
		t.Fatal(err)
	}
	publishCommand := artifactapp.RevisionPersistentCommand{WorkspaceID: workspaceID, ArtifactID: approved.Artifact.ID, ExpectedVersion: completed.State.Artifact.Version, IdempotencyKey: "reservation-publish-owner"}
	if _, err := artifactCommandServiceForConcurrency(t, repository, nil, &artifactFailOncePublisher{delegate: creator, err: errors.New("response lost after proposal")}, base.Add(5*time.Minute)).Publish(ctx, publishCommand); err == nil {
		t.Fatal("expected publish response-loss failure")
	}
	assertReservationCount(t, ctx, pool, workspaceID, approved.Artifact.ID, 1)
	otherPublisher := &artifactCountingPublisher{delegate: creator}
	if _, err := artifactCommandServiceForConcurrency(t, repository, nil, otherPublisher, base).Publish(ctx, artifactapp.RevisionPersistentCommand{WorkspaceID: workspaceID, ArtifactID: approved.Artifact.ID, ExpectedVersion: completed.State.Artifact.Version, IdempotencyKey: "reservation-publish-other"}); !artifactIntegrationErrorCode(err, artifactapp.ErrorCodeVersionConflict) || otherPublisher.calls.Load() != 0 {
		t.Fatalf("different publish key=%v calls=%d", err, otherPublisher.calls.Load())
	}
	published, err := artifactCommandServiceForConcurrency(t, repository, nil, creator, base.Add(6*time.Minute)).Publish(ctx, publishCommand)
	if err != nil || published.Publication == nil {
		t.Fatalf("same-key publish retry=%+v err=%v", published, err)
	}
	assertReservationCount(t, ctx, pool, workspaceID, approved.Artifact.ID, 0)
	replayPublisher := &artifactCountingPublisher{delegate: creator}
	replayed, err = artifactCommandServiceForConcurrency(t, repository, nil, replayPublisher, base.Add(7*time.Minute)).Publish(ctx, publishCommand)
	if err != nil || !replayed.Replayed || replayPublisher.calls.Load() != 0 {
		t.Fatalf("publish receipt replay=%+v calls=%d err=%v", replayed, replayPublisher.calls.Load(), err)
	}
}

func createApprovedArtifactForConcurrency(t *testing.T, ctx context.Context, repository artifactapp.Repository, workspaceID foundation.ID, at time.Time) artifactapp.State {
	t.Helper()
	service := artifactCommandServiceForConcurrency(t, repository, nil, nil, at)
	planned, err := service.Plan(ctx, artifactapp.PlanCommand{
		WorkspaceID: workspaceID, Type: "study-guide", Title: "Concurrent artifact", ScopeDefinition: "approved source boundary", IdempotencyKey: "concurrency-plan",
	})
	if err != nil {
		t.Fatal(err)
	}
	outlined, err := service.SubmitOutline(ctx, artifactapp.SubmitOutlinePersistentCommand{
		WorkspaceID: workspaceID, ArtifactID: planned.State.Artifact.ID, ExpectedVersion: planned.State.Artifact.Version,
		Outline: []artifactdomain.OutlineSection{{Key: "gap", Title: "Known gap"}}, IdempotencyKey: "concurrency-outline",
	})
	if err != nil {
		t.Fatal(err)
	}
	approvedOutline, err := service.ApproveOutline(ctx, artifactapp.RevisionPersistentCommand{
		WorkspaceID: workspaceID, ArtifactID: planned.State.Artifact.ID, ExpectedVersion: outlined.State.Artifact.Version, IdempotencyKey: "concurrency-approve-outline",
	})
	if err != nil {
		t.Fatal(err)
	}
	draft, err := service.RecordSection(ctx, artifactapp.RecordSectionPersistentCommand{
		RevisionPersistentCommand: artifactapp.RevisionPersistentCommand{WorkspaceID: workspaceID, ArtifactID: planned.State.Artifact.ID, ExpectedVersion: approvedOutline.State.Artifact.Version, IdempotencyKey: "concurrency-gap"},
		Section: artifactapp.SectionInput{Key: "gap", Title: "Known gap", Content: "", Citations: []artifactapp.CitationInput{}, Coverage: artifactdomain.Coverage{
			SectionKey: "gap", Status: artifactdomain.CoverageGap, Gaps: []artifactdomain.Gap{{Code: "NO_SOURCE", Description: "approved knowledge is unavailable"}},
		}},
		Creator: artifactdomain.CreatorHuman,
	})
	if err != nil {
		t.Fatal(err)
	}
	approved, err := service.ApproveDraft(ctx, artifactapp.RevisionPersistentCommand{
		WorkspaceID: workspaceID, ArtifactID: planned.State.Artifact.ID, ExpectedVersion: draft.State.Artifact.Version, IdempotencyKey: "concurrency-approve-draft",
	})
	if err != nil {
		t.Fatal(err)
	}
	return approved.State
}

func artifactCommandServiceForConcurrency(t *testing.T, repository artifactapp.Repository, exporter artifactapp.MarkdownExporter, publisher artifactapp.PublicationCreator, at time.Time) *artifactapp.CommandService {
	t.Helper()
	service, err := artifactapp.NewCommandService(artifactapp.Dependencies{
		Repository: repository, Exporter: exporter, Publisher: publisher, IDs: foundation.NewUUIDGenerator(nil), Clock: foundation.FixedClock{Value: at},
	})
	if err != nil {
		t.Fatal(err)
	}
	return service
}

type artifactCommandOutcome struct {
	result artifactapp.CommandResult
	err    error
}

func runConcurrentArtifactCommand(t *testing.T, firstArrived <-chan struct{}, first, second func() (artifactapp.CommandResult, error)) [2]artifactCommandOutcome {
	t.Helper()
	results := make(chan artifactCommandOutcome, 2)
	go func() {
		result, err := first()
		results <- artifactCommandOutcome{result: result, err: err}
	}()
	select {
	case <-firstArrived:
	case <-time.After(10 * time.Second):
		t.Fatal("first concurrent command did not reach its external side effect")
	}
	go func() {
		result, err := second()
		results <- artifactCommandOutcome{result: result, err: err}
	}()
	var outcomes [2]artifactCommandOutcome
	for index := range outcomes {
		select {
		case outcomes[index] = <-results:
		case <-time.After(10 * time.Second):
			t.Fatal("concurrent artifact command did not complete")
		}
	}
	return outcomes
}

func runParallelArtifactCommands(t *testing.T, first, second func() (artifactapp.CommandResult, error)) [2]artifactCommandOutcome {
	t.Helper()
	results := make(chan artifactCommandOutcome, 2)
	for _, command := range []func() (artifactapp.CommandResult, error){first, second} {
		go func(run func() (artifactapp.CommandResult, error)) {
			result, err := run()
			results <- artifactCommandOutcome{result: result, err: err}
		}(command)
	}
	var outcomes [2]artifactCommandOutcome
	for index := range outcomes {
		select {
		case outcomes[index] = <-results:
		case <-time.After(10 * time.Second):
			t.Fatal("parallel artifact command did not complete")
		}
	}
	return outcomes
}

func exactlyOneArtifactCommandSuccess(t *testing.T, outcomes [2]artifactCommandOutcome) artifactapp.CommandResult {
	t.Helper()
	var success *artifactapp.CommandResult
	for _, outcome := range outcomes {
		if outcome.err == nil {
			if success != nil {
				t.Fatalf("both concurrent commands succeeded: first=%+v second=%+v", *success, outcome.result)
			}
			copy := outcome.result
			success = &copy
			continue
		}
		var typed *foundation.Error
		if !errors.As(outcome.err, &typed) || typed.Code != artifactapp.ErrorCodeVersionConflict {
			t.Fatalf("concurrent command error=%v cause=%v, want stable version conflict", outcome.err, errors.Unwrap(outcome.err))
		}
	}
	if success == nil {
		t.Fatal("both concurrent commands failed")
	}
	return *success
}

func assertSingleExportFileBinding(t *testing.T, ctx context.Context, pool *pgxpool.Pool, root string, record artifactapp.ExportRecord) {
	t.Helper()
	var persisted artifactapp.ExportRecord
	var count int64
	if err := pool.QueryRow(ctx, `
		SELECT id::text,workspace_id::text,artifact_id::text,revision_id::text,artifact_version,revision_no,
		       revision_hash,output_path,output_hash,output_size,exported_at,count(*) OVER()
		FROM learning.artifact_export
		WHERE workspace_id=$1 AND artifact_id=$2
		ORDER BY exported_at DESC,id DESC
		LIMIT 1`, string(record.WorkspaceID), string(record.ArtifactID)).Scan(
		&persisted.ID, &persisted.WorkspaceID, &persisted.ArtifactID, &persisted.RevisionID, &persisted.ArtifactVersion,
		&persisted.RevisionNo, &persisted.RevisionHash, &persisted.OutputPath, &persisted.OutputHash, &persisted.OutputSize,
		&persisted.ExportedAt, &count,
	); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("export concurrency created %d export records", count)
	}
	persisted.ExportedAt = persisted.ExportedAt.UTC()
	if persisted.ID != record.ID || persisted.WorkspaceID != record.WorkspaceID || persisted.ArtifactID != record.ArtifactID || persisted.RevisionID != record.RevisionID || persisted.ArtifactVersion != record.ArtifactVersion || persisted.RevisionNo != record.RevisionNo || persisted.RevisionHash != record.RevisionHash || persisted.OutputPath != record.OutputPath || persisted.OutputHash != record.OutputHash || persisted.OutputSize != record.OutputSize || !persisted.ExportedAt.Equal(record.ExportedAt.UTC()) {
		t.Fatalf("persisted export binding differs from successful response: persisted=%+v response=%+v", persisted, record)
	}
	payload, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(persisted.OutputPath)))
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(payload)
	if hash := hex.EncodeToString(digest[:]); hash != persisted.OutputHash || int64(len(payload)) != persisted.OutputSize {
		t.Fatalf("export file does not match persisted binding: hash=%s size=%d record=%+v", hash, len(payload), persisted)
	}
}

func assertOneCrossCommandSuccess(t *testing.T, first, second error) {
	t.Helper()
	if (first == nil) == (second == nil) {
		t.Fatalf("cross-command outcomes export=%v publish=%v, want exactly one success", first, second)
	}
	failed := first
	if failed == nil {
		failed = second
	}
	var typed *foundation.Error
	if !errors.As(failed, &typed) || typed.Code != artifactapp.ErrorCodeVersionConflict {
		t.Fatalf("cross-command failure=%v, want stable version conflict", failed)
	}
}

func assertReservationCount(t *testing.T, ctx context.Context, pool *pgxpool.Pool, workspaceID, artifactID foundation.ID, want int) {
	t.Helper()
	var got int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM learning.artifact_external_transition_reservation WHERE workspace_id=$1 AND artifact_id=$2`, string(workspaceID), string(artifactID)).Scan(&got); err != nil || got != want {
		t.Fatalf("reservation count=%d want=%d err=%v", got, want, err)
	}
}

type artifactConcurrencyWorkspaceReader struct{ workspace workspacedomain.Workspace }

func (reader artifactConcurrencyWorkspaceReader) GetWorkspaceByID(_ context.Context, workspaceID foundation.ID) (workspacedomain.Workspace, error) {
	if reader.workspace.ID != workspaceID {
		return workspacedomain.Workspace{}, fmt.Errorf("workspace %s is unavailable", workspaceID)
	}
	return reader.workspace, nil
}

type artifactCrossCommandGate struct {
	firstArrived chan struct{}
	release      chan struct{}
	calls        atomic.Int32
}

func (gate *artifactCrossCommandGate) enter() {
	if gate.calls.Add(1) == 1 {
		close(gate.firstArrived)
		<-gate.release
	}
}

type artifactCrossCommandExporter struct {
	delegate artifactapp.MarkdownExporter
	gate     *artifactCrossCommandGate
}

func (exporter *artifactCrossCommandExporter) Export(ctx context.Context, snapshot artifactapp.ExportSnapshot) (artifactapp.ManagedExport, error) {
	exporter.gate.enter()
	return exporter.delegate.Export(ctx, snapshot)
}

type artifactCrossCommandPublisher struct {
	delegate artifactapp.PublicationCreator
	gate     *artifactCrossCommandGate
}

type artifactFailOnceExporter struct {
	delegate artifactapp.MarkdownExporter
	err      error
	calls    atomic.Int32
}

func (exporter *artifactFailOnceExporter) Export(ctx context.Context, snapshot artifactapp.ExportSnapshot) (artifactapp.ManagedExport, error) {
	result, err := exporter.delegate.Export(ctx, snapshot)
	if err != nil {
		return result, err
	}
	if exporter.calls.Add(1) == 1 {
		return result, exporter.err
	}
	return result, nil
}

type artifactCountingExporter struct {
	delegate artifactapp.MarkdownExporter
	calls    atomic.Int32
}

type artifactFailOncePublisher struct {
	delegate artifactapp.PublicationCreator
	err      error
	calls    atomic.Int32
}

func (publisher *artifactFailOncePublisher) CreateArtifactPublication(ctx context.Context, request artifactdomain.PublicationRequest, key string) (foundation.ID, error) {
	id, err := publisher.delegate.CreateArtifactPublication(ctx, request, key)
	if err != nil {
		return id, err
	}
	if publisher.calls.Add(1) == 1 {
		return id, publisher.err
	}
	return id, nil
}

type artifactCountingPublisher struct {
	delegate artifactapp.PublicationCreator
	calls    atomic.Int32
}

type artifactUnexpectedExporter struct{ calls atomic.Int32 }

func (exporter *artifactUnexpectedExporter) Export(context.Context, artifactapp.ExportSnapshot) (artifactapp.ManagedExport, error) {
	exporter.calls.Add(1)
	return artifactapp.ManagedExport{}, errors.New("unexpected export call")
}

type artifactUnexpectedPublisher struct{ calls atomic.Int32 }

func (publisher *artifactUnexpectedPublisher) CreateArtifactPublication(context.Context, artifactdomain.PublicationRequest, string) (foundation.ID, error) {
	publisher.calls.Add(1)
	return "", errors.New("unexpected publication call")
}

func (publisher *artifactCountingPublisher) CreateArtifactPublication(ctx context.Context, request artifactdomain.PublicationRequest, key string) (foundation.ID, error) {
	publisher.calls.Add(1)
	return publisher.delegate.CreateArtifactPublication(ctx, request, key)
}

func (exporter *artifactCountingExporter) Export(ctx context.Context, snapshot artifactapp.ExportSnapshot) (artifactapp.ManagedExport, error) {
	exporter.calls.Add(1)
	return exporter.delegate.Export(ctx, snapshot)
}

func (publisher *artifactCrossCommandPublisher) CreateArtifactPublication(ctx context.Context, request artifactdomain.PublicationRequest, idempotencyKey string) (foundation.ID, error) {
	publisher.gate.enter()
	return publisher.delegate.CreateArtifactPublication(ctx, request, idempotencyKey)
}

type artifactConcurrencyExporter struct {
	delegate     artifactapp.MarkdownExporter
	firstArrived chan struct{}
	release      chan struct{}
	calls        atomic.Int32
}

func (exporter *artifactConcurrencyExporter) Export(ctx context.Context, snapshot artifactapp.ExportSnapshot) (artifactapp.ManagedExport, error) {
	if exporter.calls.Add(1) == 1 {
		close(exporter.firstArrived)
		<-exporter.release
	} else {
		close(exporter.release)
	}
	return exporter.delegate.Export(ctx, snapshot)
}

type artifactConcurrencyPublisher struct {
	delegate     artifactapp.PublicationCreator
	firstArrived chan struct{}
	release      chan struct{}
	calls        atomic.Int32
	mu           sync.Mutex
	lastRequest  artifactdomain.PublicationRequest
}

func (publisher *artifactConcurrencyPublisher) CreateArtifactPublication(ctx context.Context, request artifactdomain.PublicationRequest, idempotencyKey string) (foundation.ID, error) {
	publisher.mu.Lock()
	publisher.lastRequest = request
	publisher.mu.Unlock()
	if publisher.calls.Add(1) == 1 {
		close(publisher.firstArrived)
		<-publisher.release
	} else {
		close(publisher.release)
	}
	return publisher.delegate.CreateArtifactPublication(ctx, request, idempotencyKey)
}

func (publisher *artifactConcurrencyPublisher) request() artifactdomain.PublicationRequest {
	publisher.mu.Lock()
	defer publisher.mu.Unlock()
	return publisher.lastRequest
}

type artifactConcurrencyTargets struct{}

func (artifactConcurrencyTargets) CurrentHash(context.Context, foundation.ID, string) (string, error) {
	return "", errors.New("target reading is not used while creating artifact publication proposals")
}

type artifactConcurrencyGit struct{}

func (artifactConcurrencyGit) CaptureApprovalSnapshot(context.Context, foundation.ID) (changecontroldomain.GitSnapshot, error) {
	return changecontroldomain.GitSnapshot{}, errors.New("git inspection is not used while creating artifact publication proposals")
}
