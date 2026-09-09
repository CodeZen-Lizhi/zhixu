//go:build integration

package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	authoringchange "github.com/CodeZen-Lizhi/zhixu/internal/authoring/adapter/changecontrol"
	authoringapp "github.com/CodeZen-Lizhi/zhixu/internal/authoring/application"
	authoringdomain "github.com/CodeZen-Lizhi/zhixu/internal/authoring/domain"
	changecontrollocalfs "github.com/CodeZen-Lizhi/zhixu/internal/changecontrol/adapter/localfs"
	changecontrolapp "github.com/CodeZen-Lizhi/zhixu/internal/changecontrol/application"
	changecontroldomain "github.com/CodeZen-Lizhi/zhixu/internal/changecontrol/domain"
	changecontrolworkflow "github.com/CodeZen-Lizhi/zhixu/internal/changecontrol/workflow"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	organizingapp "github.com/CodeZen-Lizhi/zhixu/internal/organizing/application"
	"github.com/CodeZen-Lizhi/zhixu/internal/organizing/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/platform/gitcli"
	"github.com/CodeZen-Lizhi/zhixu/internal/platform/gitoperation"
	toolcatalog "github.com/CodeZen-Lizhi/zhixu/internal/tools/adapter/catalog"
	toolchangecontrol "github.com/CodeZen-Lizhi/zhixu/internal/tools/adapter/changecontrol"
	toolpostgres "github.com/CodeZen-Lizhi/zhixu/internal/tools/adapter/postgres"
	toolsapp "github.com/CodeZen-Lizhi/zhixu/internal/tools/application"
	workflowpostgres "github.com/CodeZen-Lizhi/zhixu/internal/workflow/adapter/postgres"
	workspacepostgres "github.com/CodeZen-Lizhi/zhixu/internal/workspace/adapter/postgres"
	workspacedomain "github.com/CodeZen-Lizhi/zhixu/internal/workspace/domain"
)

// Publication uses the production approval, authorization, audit, filesystem,
// Git, Saga and Authoring finalizer. Only source/model inputs and the running
// Workflow lease are fixture data; no approval, commit or published pointer is
// fabricated. Every filesystem write and git commit stays under t.TempDir().
func TestSynthesisPostgreSQLRealGitPublication(t *testing.T) {
	f := newSynthesisGitFixture(t)
	ctx := t.Context()
	first := f.generation(t, 54000, nil, "The scheduler manages runnable work. Fairness depends on workload. A version-specific limit remains unknown.")
	first.Generation.Notes = []organizingapp.SynthesisGeneratedNote{f.newTopic(first, "scheduling publication")}
	ref := first.Input.Sources[0].Reference
	first.Generation.Notes[0].Delta.Operations = append(first.Generation.Notes[0].Delta.Operations,
		domain.SynthesisOperation{Kind: domain.SynthesisAddConflict, Item: &domain.SynthesisItem{ID: organizingIntegrationID(54030), Kind: domain.SynthesisConflictItem, Conflict: &domain.SynthesisConflictContent{Subject: "Scheduling fairness", Alternatives: []domain.SynthesisStatement{
			{Text: "Runnable tasks receive execution time.", Applicability: "Ordinary workload", Sources: []domain.SynthesisSourceRef{ref}},
			{Text: "Execution time can be delayed.", Applicability: "Saturated workload", Sources: []domain.SynthesisSourceRef{ref}},
		}}}},
		domain.SynthesisOperation{Kind: domain.SynthesisAddGap, Item: &domain.SynthesisItem{ID: organizingIntegrationID(54031), Kind: domain.SynthesisGapItem, Gap: &domain.SynthesisGapContent{Question: "Which runtime version changes the limit?", Context: "The source leaves the version-specific limit open.", Sources: []domain.SynthesisSourceRef{ref}}}},
	)
	created, err := f.service.ApplyGeneration(ctx, first.Input, first.Generation)
	if err != nil || len(created.Publications) != 1 {
		t.Fatalf("create governed candidate: %+v %v", created, err)
	}
	noteID := created.Publications[0].NoteID
	pending, err := f.service.GetNote(ctx, f.workspace, noteID)
	if err != nil || pending.Publication == nil || pending.PublishedRevision != nil {
		t.Fatalf("pending candidate: %+v %v", pending, err)
	}
	target, err := authoringdomain.DefaultGeneratedTargetPath(noteID)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(f.root, target)
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("unapproved candidate touched a file: %v", err)
	}
	if head := synthesisGit(t, ctx, f.root, "rev-parse", "HEAD"); head != f.initialHead {
		t.Fatal("unapproved candidate created a git commit")
	}
	if _, err := f.service.ReadPublishedSynthesisNote(ctx, f.workspace, noteID); !organizingIntegrationError(err, foundation.ErrorVersionConflict, "SYNTHESIS_NOTE_NOT_PUBLISHED") {
		t.Fatalf("unapproved candidate became interview material: %v", err)
	}

	firstExecution := f.preparePublication(t, pending)
	firstWritten, err := f.node.Execute(ctx, firstExecution.input, firstExecution.identity)
	if err != nil {
		t.Fatalf("first real writeback: %v", err)
	}
	f.assertPublished(t, pending, target, firstWritten)
	if replay, err := f.node.Execute(ctx, firstExecution.input, firstExecution.identity); err != nil || replay != firstWritten {
		t.Fatalf("writeback replay changed its commit: %+v %v", replay, err)
	}
	if count := synthesisGit(t, ctx, f.root, "rev-list", "--count", "HEAD"); count != "2" {
		t.Fatalf("first publication/replay commit count=%s", count)
	}
	firstContent, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	ready, err := f.service.GetNote(ctx, f.workspace, noteID)
	if err != nil || ready.Note.Status != domain.SynthesisReady || ready.PublishedRevision == nil {
		t.Fatalf("first ready note: %+v %v", ready, err)
	}
	second := f.generation(t, 55000, []organizingapp.SynthesisGenerationNote{{Note: ready.Note, Revision: *ready.CurrentRevision}}, "Additional evidence for the existing scheduling fact.")
	second.Generation.Notes = []organizingapp.SynthesisGeneratedNote{supportedSynthesisNote(ready, second.Input.Sources[0].Reference)}
	if _, err := f.service.ApplyGeneration(ctx, second.Input, second.Generation); err != nil {
		t.Fatalf("prepare governed replacement: %v", err)
	}
	replacement, err := f.service.GetNote(ctx, f.workspace, noteID)
	if err != nil || replacement.Publication == nil || replacement.PublishedRevision == nil || replacement.PublishedRevision.ID != pending.CurrentRevision.ID || replacement.CurrentRevision.RevisionNo != 2 {
		t.Fatalf("replacement lost the published baseline: %+v %v", replacement, err)
	}
	if content, err := os.ReadFile(path); err != nil || string(content) != string(firstContent) {
		t.Fatalf("unapproved replacement changed the file: %v", err)
	}
	if snapshot, err := f.service.ReadPublishedSynthesisNote(ctx, f.workspace, noteID); err != nil || snapshot.RevisionID != pending.CurrentRevision.ID {
		t.Fatalf("unapproved replacement changed interview material: %+v %v", snapshot, err)
	}
	secondExecution := f.preparePublication(t, replacement)
	secondWritten, err := f.node.Execute(ctx, secondExecution.input, secondExecution.identity)
	if err != nil {
		t.Fatalf("second real writeback: %v", err)
	}
	f.assertPublished(t, replacement, target, secondWritten)
	if secondWritten.GitCommit == firstWritten.GitCommit || synthesisGit(t, ctx, f.root, "rev-list", "--count", "HEAD") != "3" {
		t.Fatal("replacement did not make exactly one new commit")
	}
	history, err := f.service.GetSynthesisRevision(ctx, f.workspace, noteID, pending.CurrentRevision.ID)
	if err != nil || !reflect.DeepEqual(history, *pending.CurrentRevision) || replacement.CurrentRevision.Items[0].ID != history.Items[0].ID || replacement.CurrentRevision.Items[0].Fact.Text != history.Items[0].Fact.Text {
		t.Fatalf("published history or stable item changed: %v", err)
	}

	// A human edit after approval and authorization must survive a resumed Saga.
	latest, err := f.service.GetNote(ctx, f.workspace, noteID)
	if err != nil {
		t.Fatal(err)
	}
	third := f.generation(t, 56000, []organizingapp.SynthesisGenerationNote{{Note: latest.Note, Revision: *latest.CurrentRevision}}, "Further evidence pending user review.")
	third.Generation.Notes = []organizingapp.SynthesisGeneratedNote{supportedSynthesisNote(latest, third.Input.Sources[0].Reference)}
	if _, err := f.service.ApplyGeneration(ctx, third.Input, third.Generation); err != nil {
		t.Fatal(err)
	}
	drifted, err := f.service.GetNote(ctx, f.workspace, noteID)
	if err != nil {
		t.Fatal(err)
	}
	thirdExecution := f.preparePublication(t, drifted)
	humanContent := "# Human change\n\nPreserve this edit after approval.\n"
	if err := os.WriteFile(path, []byte(humanContent), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err = f.node.Execute(ctx, thirdExecution.input, thirdExecution.identity)
	var conflict *foundation.Error
	if !errors.As(err, &conflict) || conflict.Kind != foundation.ErrorVersionConflict {
		t.Fatalf("changed approved baseline did not produce a version conflict: %v", err)
	}
	failed, err := f.proposals.GetWritebackExecution(ctx, thirdExecution.input.ExecutionID)
	if err != nil || failed.Status != changecontroldomain.WritebackStatusNeedsRevision {
		t.Fatalf("conflicting writeback has no visible needs-revision state: status=%s err=%v", failed.Status, err)
	}
	if content, err := os.ReadFile(path); err != nil || string(content) != humanContent {
		t.Fatalf("human edit was overwritten: %v", err)
	}
	if synthesisGit(t, ctx, f.root, "rev-parse", "HEAD") != secondWritten.GitCommit {
		t.Fatal("failed writeback created a git commit")
	}
	f.count(t, "change_control.proposal_commit", "proposal_id", string(drifted.Publication.ProposalID), 0)
	if snapshot, err := f.service.ReadPublishedSynthesisNote(ctx, f.workspace, noteID); err != nil || snapshot.RevisionID != replacement.CurrentRevision.ID {
		t.Fatalf("failed candidate replaced the published snapshot: %+v %v", snapshot, err)
	}
}

type synthesisGitFixture struct {
	*synthesisDBFixture
	root, initialHead string
	definitionID      foundation.ID
	changes           *changecontrolapp.Service
	writeback         *changecontrolapp.WritebackService
	node              *changecontrolworkflow.Node
}

func newSynthesisGitFixture(t *testing.T) *synthesisGitFixture {
	t.Helper()
	return newSynthesisGitFixtureAtVersion(t, 95)
}

func newSynthesisGitFixtureAtVersion(t *testing.T, version int64) *synthesisGitFixture {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Fatal("real publication validation requires git")
	}
	f := &synthesisGitFixture{synthesisDBFixture: newSynthesisDBFixtureAtVersion(t, version), root: t.TempDir(), definitionID: organizingIntegrationID(53001)}
	ctx := t.Context()
	if err := os.WriteFile(filepath.Join(f.root, ".gitignore"), []byte(".knowledge/\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	synthesisGit(t, ctx, f.root, "init", "--initial-branch=main")
	synthesisGit(t, ctx, f.root, "config", "user.name", "Synthesis Integration")
	synthesisGit(t, ctx, f.root, "config", "user.email", "synthesis@example.invalid")
	synthesisGit(t, ctx, f.root, "config", "commit.gpgSign", "false")
	synthesisGit(t, ctx, f.root, "config", "core.hooksPath", os.DevNull)
	synthesisGit(t, ctx, f.root, "add", "--", ".gitignore")
	synthesisGit(t, ctx, f.root, "commit", "-m", "isolated synthesis baseline")
	f.initialHead = synthesisGit(t, ctx, f.root, "rev-parse", "HEAD")
	workspaces, err := workspacepostgres.NewGORMRepository(f.platform)
	if err != nil {
		t.Fatal(err)
	}
	f.workspace = organizingIntegrationID(53000)
	now := time.Now().UTC().Truncate(time.Microsecond)
	if _, err := workspaces.CreateWorkspace(ctx, workspacedomain.Workspace{ID: f.workspace, Name: "Synthesis publication integration", RootPath: f.root,
		Git: workspacedomain.GitBaseline{RepositoryPath: f.root, Branch: "main", Head: f.initialHead, CheckedAt: now}, Status: workspacedomain.WorkspaceStatusActive, Version: 1, CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	targets, err := changecontrollocalfs.NewReader(workspaces)
	if err != nil {
		t.Fatal(err)
	}
	git, err := gitcli.NewWritebackClient(gitcli.New(""), workspaces)
	if err != nil {
		t.Fatal(err)
	}
	ids, clock := foundation.UUIDGenerator{}, foundation.SystemClock{}
	f.changes, err = changecontrolapp.NewService(f.proposals, ids, clock, targets, git)
	if err != nil {
		t.Fatal(err)
	}
	creator, err := authoringchange.NewProposalCreator(f.changes, targets)
	if err != nil {
		t.Fatal(err)
	}
	publisher, err := authoringapp.NewService(authoringapp.Dependencies{Repository: f.authoring, IDs: ids, Clock: clock, Proposals: creator})
	if err != nil {
		t.Fatal(err)
	}
	f.service, err = organizingapp.NewSynthesisService(organizingapp.SynthesisDependencies{Store: f.store, Sources: f.sources, Publications: publisher, IDs: ids, Clock: clock})
	if err != nil {
		t.Fatal(err)
	}
	validator, err := changecontrollocalfs.NewDefaultMarkdownValidator()
	if err != nil {
		t.Fatal(err)
	}
	writer, err := changecontrollocalfs.NewWriter(workspaces, validator)
	if err != nil {
		t.Fatal(err)
	}
	registry, err := toolcatalog.NewFrozenContractRegistry()
	if err != nil {
		t.Fatal(err)
	}
	policy, err := workflowpostgres.NewGORMToolExecutionPolicySnapshot(f.platform)
	if err != nil {
		t.Fatal(err)
	}
	recovery, err := workflowpostgres.NewGORMToolCallRecoveryFence(f.platform)
	if err != nil {
		t.Fatal(err)
	}
	toolStore, err := toolpostgres.NewGORMRepository(f.platform, policy, recovery)
	if err != nil {
		t.Fatal(err)
	}
	auditService, err := toolsapp.NewTrustedWriteAuditService(registry, toolStore, ids, clock)
	if err != nil {
		t.Fatal(err)
	}
	audit, err := toolchangecontrol.NewWritebackAuditRecorder(auditService)
	if err != nil {
		t.Fatal(err)
	}
	locker, err := gitoperation.NewPostgresLocker(f.platform.DB())
	if err != nil {
		t.Fatal(err)
	}
	finalizer, err := authoringchange.NewPublicationFinalizer(f.authoring, clock)
	if err != nil {
		t.Fatal(err)
	}
	f.writeback, err = changecontrolapp.NewWritebackService(changecontrolapp.WritebackServiceDependencies{Repository: f.proposals, Workspace: writer, Git: git, GitOperations: locker, Audit: audit, Publication: finalizer, IDs: ids, Clock: clock})
	if err != nil {
		t.Fatal(err)
	}
	f.node, err = changecontrolworkflow.NewNode(f.writeback)
	if err != nil {
		t.Fatal(err)
	}
	definition := changecontrolworkflow.RegisteredDefinition()
	graph, err := json.Marshal(definition.Graph)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.db.Exec(`INSERT INTO workflow.definition(id,workspace_id,key,version,graph,created_at) VALUES(?,?,?,?,?::jsonb,?)`, string(f.definitionID), string(f.workspace), definition.Key, definition.Version, string(graph), now).Error; err != nil {
		t.Fatal(err)
	}
	return f
}

type synthesisPreparedPublication struct {
	input    changecontrolworkflow.Input
	identity changecontrolapp.WritebackResumeIdentity
}

func (f *synthesisGitFixture) preparePublication(t *testing.T, detail organizingapp.SynthesisNoteDetail) synthesisPreparedPublication {
	t.Helper()
	ctx := t.Context()
	if detail.Publication == nil {
		t.Fatal("candidate has no publication")
	}
	proposal, err := f.proposals.GetProposal(ctx, detail.Publication.ProposalID)
	if err != nil {
		t.Fatal(err)
	}
	head := synthesisGit(t, ctx, f.root, "rev-parse", "HEAD")
	approval, err := f.changes.DecideProposal(ctx, proposal.ID, proposal.Revision.ID, proposal.Revision.ChangeHash, changecontroldomain.DecisionApproved)
	if err != nil || approval.ApprovedGitHead == nil || *approval.ApprovedGitHead != head {
		t.Fatalf("real proposal approval: %v", err)
	}
	ids := foundation.UUIDGenerator{}
	identity := changecontrolapp.WritebackResumeIdentity{WorkspaceID: f.workspace, DefinitionID: f.definitionID, DefinitionVersion: changecontrolworkflow.RegisteredDefinition().Version, DefinitionHash: changecontrolworkflow.RegisteredDefinition().GraphHash, NodeKey: changecontrolworkflow.SafeWritebackNodeKey, LeaseOwner: "synthesis-publication-test", LeaseFence: 1}
	for _, target := range []*foundation.ID{&identity.WorkflowRunID, &identity.NodeRunID, &identity.NodeAttemptID} {
		*target, err = ids.New()
		if err != nil {
			t.Fatal(err)
		}
	}
	now := time.Now().UTC().Truncate(time.Microsecond)
	lease := now.Add(10 * time.Minute)
	// The fixed running node is the only Workflow fixture seam. Approval and
	// authorizations are persisted through their actual owners, as in the
	// existing direct-node Safe Writeback integration test.
	statements := []struct {
		sql  string
		args []any
	}{
		{`INSERT INTO workflow.run(id,workspace_id,definition_id,status,input,version,created_at,updated_at) VALUES(?,?,?,'running','{}',1,?,?)`, []any{string(identity.WorkflowRunID), string(f.workspace), string(f.definitionID), now, now}},
		{`INSERT INTO workflow.node_run(id,run_id,node_key,node_type,status,attempt,input,idempotency_key,input_schema_version,output_schema_version,dispatch_no,lease_owner,lease_until,version,created_at,updated_at) VALUES(?,?,?,?,'running',1,'{}',?,1,1,1,?,?,2,?,?)`, []any{string(identity.NodeRunID), string(identity.WorkflowRunID), identity.NodeKey, changecontrolworkflow.SafeWritebackNodeKind, "synthesis-writeback-node:" + string(identity.NodeRunID), identity.LeaseOwner, lease, now, now}},
		{`INSERT INTO workflow.node_attempt(id,node_run_id,attempt_no,dispatch_no,retry_no,delivery_id,lease_owner,lease_until,status,started_at,heartbeat_at) VALUES(?,?,1,1,0,?,?,?,'running',?,?)`, []any{string(identity.NodeAttemptID), string(identity.NodeRunID), "synthesis-writeback-delivery:" + string(identity.NodeAttemptID), identity.LeaseOwner, lease, now, now}},
		{`UPDATE change_control.proposal SET workflow_run_id=?,version=version+1,updated_at=? WHERE id=? AND workflow_run_id IS NULL`, []any{string(identity.WorkflowRunID), now, string(proposal.ID)}},
		{`INSERT INTO change_control.proposal_revision_dispatch(workspace_id,proposal_id,revision_id,approval_id,workflow_run_id,created_at) VALUES(?,?,?,?,?,?)`, []any{string(f.workspace), string(proposal.ID), string(proposal.Revision.ID), string(approval.ID), string(identity.WorkflowRunID), now}},
	}
	for _, statement := range statements {
		if err := f.db.Exec(statement.sql, statement.args...).Error; err != nil {
			t.Fatal(err)
		}
	}
	issue := func(tool string, capability changecontroldomain.Capability) changecontroldomain.AuthorizationIssueResult {
		result, err := f.changes.IssueWriteAuthorization(ctx, changecontroldomain.AuthorizationIssue{WorkspaceID: f.workspace, WorkflowRunID: identity.WorkflowRunID, NodeRunID: identity.NodeRunID, ProposalID: proposal.ID, RevisionID: proposal.Revision.ID, ApprovalID: approval.ID, ToolName: tool, Capability: capability, Scope: changecontroldomain.ExpectedAuthorizationScopeForTarget(proposal.TargetPath, proposal.Revision.TargetMode), IdempotencyKey: "synthesis-auth:" + tool + ":" + string(identity.WorkflowRunID), TTL: 2 * time.Minute})
		if err != nil {
			t.Fatalf("real writeback authorization: %v", err)
		}
		return result
	}
	writeAuth := issue("ApplyApprovedPatch", changecontroldomain.CapabilityWriteKnowledge)
	gitAuth := issue("CreateGitCommit", changecontroldomain.CapabilityGitWrite)
	begin, err := f.writeback.Begin(ctx, changecontrolapp.BeginWritebackCommand{WorkspaceID: f.workspace, WorkflowRunID: identity.WorkflowRunID, NodeRunID: identity.NodeRunID, ProposalID: proposal.ID, LeaseOwner: identity.LeaseOwner, IdempotencyKey: "synthesis-writeback:" + string(identity.WorkflowRunID), WriteCredential: writeAuth.Credential, GitCredential: gitAuth.Credential, WriteAuthorizationKey: writeAuth.Authorization.IdempotencyKey, GitAuthorizationKey: gitAuth.Authorization.IdempotencyKey})
	if err != nil || begin.Status != changecontroldomain.WritebackStatusPrepared {
		t.Fatalf("real atomic writeback begin: status=%s err=%v", begin.Status, err)
	}
	return synthesisPreparedPublication{input: changecontrolworkflow.Input{SchemaVersion: changecontrolapp.SafeWritebackSchemaVersion, ExecutionID: begin.ExecutionID, WorkspaceID: f.workspace, WorkflowRunID: identity.WorkflowRunID, NodeRunID: identity.NodeRunID}, identity: identity}
}

func (f *synthesisGitFixture) assertPublished(t *testing.T, detail organizingapp.SynthesisNoteDetail, target string, written changecontrolworkflow.Output) {
	t.Helper()
	ctx := t.Context()
	snapshot, err := f.service.ReadPublishedSynthesisNote(ctx, f.workspace, detail.Note.ID)
	if err != nil {
		t.Fatalf("read proven published snapshot: %v", err)
	}
	want, err := domain.SynthesisSnapshotFromRevision(*detail.CurrentRevision)
	if err != nil || !reflect.DeepEqual(snapshot, want) || written.ResultHash != snapshot.ContentHash {
		t.Fatalf("published snapshot differs from the approved revision: %v", err)
	}
	content, err := os.ReadFile(filepath.Join(f.root, target))
	if err != nil || authoringdomain.ComputeContentHash(string(content)) != snapshot.ContentHash {
		t.Fatalf("published file/hash mismatch: %v", err)
	}
	if head := synthesisGit(t, ctx, f.root, "rev-parse", "HEAD"); head != written.GitCommit {
		t.Fatalf("git HEAD does not match publication: %s", head)
	}
	committed := synthesisGitOutput(t, ctx, f.root, "show", written.GitCommit+":"+target)
	if committed != string(content) {
		t.Fatal("git contains different published content")
	}
	var count int64
	err = f.db.Raw(`SELECT count(*) FROM change_control.proposal_commit WHERE workspace_id=? AND proposal_id=? AND revision_id=? AND writeback_execution_id=? AND target_path=? AND result_hash=? AND git_commit=?`, string(f.workspace), string(detail.Publication.ProposalID), string(detail.Publication.ProposalRevisionID), string(written.ExecutionID), target, snapshot.ContentHash, written.GitCommit).Scan(&count).Error
	if err != nil || count != 1 {
		t.Fatalf("publication cannot be traced to its exact commit: count=%d err=%v", count, err)
	}
	f.count(t, "workflow.tool_call", "side_effect_id", string(written.ExecutionID), 2)
}

func supportedSynthesisNote(detail organizingapp.SynthesisNoteDetail, source domain.SynthesisSourceRef) organizingapp.SynthesisGeneratedNote {
	return organizingapp.SynthesisGeneratedNote{NoteID: detail.Note.ID, BaseRevisionID: detail.CurrentRevision.ID, TopicKey: detail.Note.TopicKey, Title: detail.Note.Title, Aliases: detail.Note.Aliases, Delta: domain.SynthesisDelta{Operations: []domain.SynthesisOperation{{Kind: domain.SynthesisAddSupport, TargetItemID: detail.CurrentRevision.Items[0].ID, Sources: []domain.SynthesisSourceRef{source}}}}}
}

func synthesisGit(t *testing.T, ctx context.Context, root string, arguments ...string) string {
	t.Helper()
	return strings.TrimSpace(synthesisGitOutput(t, ctx, root, arguments...))
}

func synthesisGitOutput(t *testing.T, ctx context.Context, root string, arguments ...string) string {
	t.Helper()
	command := exec.CommandContext(ctx, "git", append([]string{"-C", root}, arguments...)...)
	// Like the production Git runner, discard process-level repository/index
	// overrides so even fixture setup cannot escape the temporary repository.
	for _, entry := range os.Environ() {
		name, _, found := strings.Cut(entry, "=")
		if found && !strings.HasPrefix(name, "GIT_") && name != "LC_ALL" && name != "LANG" {
			command.Env = append(command.Env, entry)
		}
	}
	command.Env = append(command.Env, "LC_ALL=C", "LANG=C", "GIT_TERMINAL_PROMPT=0", "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL="+os.DevNull)
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("isolated git %v failed: %v: %s", arguments, err, output)
	}
	return string(output)
}
