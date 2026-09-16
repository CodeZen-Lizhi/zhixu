//go:build integration

package postgres

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	authoringdomain "github.com/CodeZen-Lizhi/zhixu/internal/authoring/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/capability"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	app "github.com/CodeZen-Lizhi/zhixu/internal/organizing/application"
	"github.com/CodeZen-Lizhi/zhixu/internal/organizing/domain"
)

func TestSynthesisHistoricalRepublishPostgreSQLGit(t *testing.T) {
	for _, oldCandidate := range []bool{false, true} {
		name := "published-v1"
		if oldCandidate {
			name = "unpublished-candidate"
		}
		t.Run(name, func(t *testing.T) {
			f := newSynthesisGitFixtureWithRootBinding(t, 128, true)
			ctx := t.Context()
			check := func(err error) {
				t.Helper()
				if err != nil {
					t.Fatal(err)
				}
			}
			first := f.generation(t, 146000, nil, "First scheduling evidence.")
			first.Generation.Notes = []app.SynthesisGeneratedNote{f.newTopic(first, "historical scheduling")}
			created, err := f.service.ApplyGeneration(ctx, first.Input, first.Generation)
			check(err)
			selected, err := f.service.GetNote(ctx, f.workspace, created.Publications[0].NoteID)
			check(err)
			path, err := authoringdomain.DefaultGeneratedTargetPath(selected.Note.ID)
			check(err)
			publish := func(d app.SynthesisNoteDetail) {
				t.Helper()
				p := f.preparePublication(t, d)
				written, err := f.node.Execute(ctx, p.input, p.identity)
				check(err)
				f.assertPublished(t, d, path, written)
			}
			if !oldCandidate {
				publish(selected)
			}
			latest, err := f.service.GetNote(ctx, f.workspace, selected.Note.ID)
			check(err)
			second := f.generation(t, 82500, []app.SynthesisGenerationNote{{Note: latest.Note, Revision: *latest.CurrentRevision}}, "Second scheduling support.")
			second.Generation.Notes = []app.SynthesisGeneratedNote{extendedSynthesisNote(latest, second.Input.Sources[0].Reference)}
			_, err = f.service.ApplyGeneration(ctx, second.Input, second.Generation)
			check(err)
			latest, err = f.service.GetNote(ctx, f.workspace, selected.Note.ID)
			check(err)
			publish(latest)
			latest, err = f.service.GetNote(ctx, f.workspace, selected.Note.ID)
			check(err)
			// 选定版本之后创建的 Profile 不得替换其历史 NULL 值。
			seedDiscoveryRuntimeProfile(t, f.synthesisDBFixture, first)
			check(f.db.Exec(`UPDATE core.source SET removed_at=clock_timestamp() WHERE id=?`, string(first.Input.SourceEvent.Source.SourceID)).Error)
			runtime, _ := candidateRemergeOwner(t, f)
			owner, err := runtime.HistoricalRepublish(app.NewSynthesisManuscriptCaller([]capability.Capability{capability.ReadLocal, capability.WriteProposal}))
			check(err)
			absolute := filepath.Join(f.root, path)
			current, err := os.ReadFile(absolute)
			check(err)
			edited := append([]byte("<!-- Human F differs from P. -->\n"), current...)
			check(os.WriteFile(absolute, edited, 0600))
			synthesisGit(t, ctx, f.root, "add", "--", path)
			synthesisGit(t, ctx, f.root, "commit", "-m", "Human file baseline")
			target, err := owner.Target(ctx, f.workspace, selected.Note.ID, selected.CurrentRevision.ID)
			check(err)
			if (target.SelectedPublicationID == "") != oldCandidate {
				t.Fatal("selected publication identity wrong")
			}
			begin := app.BeginSynthesisHistoricalRepublish{SynthesisHistoricalRepublishTarget: target, IdempotencyKey: "historical-select"}
			review, err := owner.Begin(ctx, begin)
			check(err)
			selectedText, err := selected.CurrentRevision.Content()
			check(err)
			if len(review.Warnings) < 2 {
				t.Fatal("removed source warning missing")
			}
			if review.Candidate != selectedText || review.CurrentContent != string(edited) || review.PublishedContent != string(current) {
				t.Fatal("preview is not exact F/P/selected")
			}
			replay, err := owner.Read(ctx, app.ReadSynthesisHistoricalRepublish{WorkspaceID: f.workspace, NoteID: selected.Note.ID, IdempotencyKey: begin.IdempotencyKey})
			check(err)
			if replay.AttemptID != review.AttemptID {
				t.Fatal("lost begin recovery differs")
			}
			// 直接 SQL 载荷中的哈希不构成授权；更改选定身份或任意替换字节，必须被数据库校验拒绝。
			var frozen historicalRepublishAttempt
			found, readErr := readHistoricalEvent(f.db, f.workspace, selected.Note.ID, "BEGIN", "", review.AttemptID, &frozen)
			check(readErr)
			if !found {
				t.Fatal("missing persisted begin")
			}
			for _, kind := range []string{"selected", "content"} {
				forged := frozen
				forged.ID, err = (foundation.UUIDGenerator{}).New()
				check(err)
				forged.Command.IdempotencyKey = "forged-" + kind
				if kind == "selected" {
					forged.Command.SelectedRevisionID = latest.CurrentRevision.ID
				} else {
					forged.Candidate = "unproved arbitrary replacement"
				}
				raw, encodeErr := encodeManuscriptRecord(forged)
				check(encodeErr)
				forged.Fingerprint = manuscriptBytesHash(raw)
				if err = owner.(*synthesisHistoricalRepublish).insert(f.db, forged.ID, f.workspace, selected.Note.ID, forged.ID, "BEGIN", forged.Command.IdempotencyKey, forged); err == nil {
					t.Fatalf("direct SQL forged %s accepted", kind)
				}
			}
			changed := begin
			changed.SelectedRevisionID = latest.CurrentRevision.ID
			if _, err = owner.Begin(ctx, changed); err == nil {
				t.Fatal("same key different selected accepted")
			}
			apply := app.ApplySynthesisHistoricalRepublish{WorkspaceID: f.workspace, NoteID: selected.Note.ID, AttemptID: review.AttemptID, IdempotencyKey: "historical-apply", PreviewFingerprint: review.PreviewFingerprint, ConfirmExactRestore: true}
			publisher := runtime.dependencies.Service.Publications
			if oldCandidate {
				runtime.dependencies.Service.Publications = candidateRemergeBeforePublisher{}
			}
			applied, err := owner.Apply(ctx, apply)
			if oldCandidate {
				if err == nil || applied.State != "APPLIED" || applied.Result == nil || applied.Result.ProposalID != "" {
					t.Fatal("expected durable apply before publisher interruption")
				}
				recovered, readErr := owner.Read(ctx, app.ReadSynthesisHistoricalRepublish{WorkspaceID: f.workspace, NoteID: selected.Note.ID, AttemptID: review.AttemptID})
				check(readErr)
				if recovered.Result == nil || recovered.Result.RevisionID != applied.Result.RevisionID {
					t.Fatal("lost apply response was not readable")
				}
				runtime.dependencies.Service.Publications = publisher
				applied, err = owner.Resume(ctx, app.ResumeSynthesisHistoricalRepublish{WorkspaceID: f.workspace, NoteID: selected.Note.ID, AttemptID: review.AttemptID})
			}
			check(err)
			after, err := f.service.GetNote(ctx, f.workspace, selected.Note.ID)
			check(err)
			if after.CurrentRevision.ID == selected.CurrentRevision.ID || after.CurrentRevision.ParentRevisionID != latest.CurrentRevision.ID || after.PublishedRevision.ID != latest.PublishedRevision.ID || after.CurrentRevision.ModelRunID != selected.CurrentRevision.ModelRunID || !reflect.DeepEqual(after.CurrentRevision.Items, selected.CurrentRevision.Items) {
				t.Fatal("historical candidate lineage or publication changed")
			}
			bytes, err := os.ReadFile(absolute)
			check(err)
			if string(bytes) != string(edited) {
				t.Fatal("candidate changed file")
			}
			var wrongProfiles int64
			check(f.db.Raw(`SELECT count(*) FROM organizing.synthesis_revision_source restored JOIN organizing.synthesis_revision_source selected ON selected.workspace_id=restored.workspace_id AND selected.source_span_id=restored.source_span_id AND selected.revision_id=? WHERE restored.revision_id=? AND restored.profile_revision_id IS DISTINCT FROM selected.profile_revision_id`, string(selected.CurrentRevision.ID), string(after.CurrentRevision.ID)).Scan(&wrongProfiles).Error)
			if wrongProfiles != 0 {
				t.Fatal("historical profile snapshot changed")
			}
			again, err := owner.Apply(ctx, apply)
			check(err)
			if !again.Replayed || again.Result.RevisionID != applied.Result.RevisionID {
				t.Fatal("apply replay created another revision")
			}
			resumed, err := owner.Resume(ctx, app.ResumeSynthesisHistoricalRepublish{WorkspaceID: f.workspace, NoteID: selected.Note.ID, AttemptID: review.AttemptID})
			check(err)
			if resumed.Result.ProposalID != applied.Result.ProposalID {
				t.Fatal("resume changed proposal")
			}
			publish(after)
			published, err := f.service.ReadPublishedSynthesisNote(ctx, f.workspace, selected.Note.ID)
			check(err)
			text, err := published.Content()
			check(err)
			if text != selectedText || published.RevisionID != after.CurrentRevision.ID {
				t.Fatal("published snapshot differs")
			}
			// 即使发布推进了 L、P 和文档版本，Begin 仍须可重放。
			again, err = owner.Begin(ctx, begin)
			check(err)
			if again.Result == nil || again.Result.RevisionID != after.CurrentRevision.ID {
				t.Fatal("post-publication replay lost identity")
			}
			_, err = f.store.ReconcileSynthesisPublicationEvents(ctx, 100)
			check(err)
			var eventCount int64
			check(f.db.Raw(`SELECT count(*) FROM workflow.outbox_event WHERE event_type='organizing.synthesis.published' AND payload->>'revision_id'=?`, string(after.CurrentRevision.ID)).Scan(&eventCount).Error)
			if eventCount != 1 {
				t.Fatal("restoration did not produce its independent publication event")
			}
			check(f.db.Exec(`UPDATE core.source SET removed_at=NULL WHERE id=?`, string(first.Input.SourceEvent.Source.SourceID)).Error)
			// 后续普通生成增量仍走合法的 Generated 路径。
			fresh, err := f.service.GetNote(ctx, f.workspace, selected.Note.ID)
			check(err)
			third := f.generation(t, 84000, []app.SynthesisGenerationNote{{Note: fresh.Note, Revision: *fresh.CurrentRevision}}, "Later independent scheduling evidence.")
			third.Generation.Notes = []app.SynthesisGeneratedNote{extendedSynthesisNote(fresh, third.Input.Sources[0].Reference)}
			_, err = f.service.ApplyGeneration(ctx, third.Input, third.Generation)
			check(err)
			next, err := f.service.GetNote(ctx, f.workspace, selected.Note.ID)
			check(err)
			if next.CurrentRevision.ParentRevisionID != fresh.CurrentRevision.ID {
				t.Fatal("subsequent increment lost historical parent")
			}
		})
	}
}

func TestSynthesisHistoricalRepublishManuscriptPostgreSQLGit(t *testing.T) {
	f, selected := newCandidateRemergeFixture(t)
	ctx := t.Context()
	check := func(err error) {
		t.Helper()
		if err != nil {
			for cause := err; cause != nil; cause = errors.Unwrap(cause) {
				t.Logf("cause: %v", cause)
			}
			t.Fatal(err)
		}
	}
	runtime, _ := candidateRemergeOwner(t, f)
	owner, err := runtime.HistoricalRepublish(app.NewSynthesisManuscriptCaller([]capability.Capability{capability.ReadLocal, capability.WriteProposal}))
	check(err)
	if selected.CurrentRevision.Manuscript == nil {
		t.Fatal("fixture did not create v2 manuscript")
	}
	path, err := authoringdomain.DefaultGeneratedTargetPath(selected.Note.ID)
	check(err)
	absolute := filepath.Join(f.root, path)
	content, err := os.ReadFile(absolute)
	check(err)
	changed := append([]byte("<!-- new human change after v2 candidate -->\n"), content...)
	check(os.WriteFile(absolute, changed, 0600))
	synthesisGit(t, ctx, f.root, "add", "--", path)
	synthesisGit(t, ctx, f.root, "commit", "-m", "Current human manuscript baseline")
	target, err := owner.Target(ctx, f.workspace, selected.Note.ID, selected.CurrentRevision.ID)
	check(err)
	if !target.RequiresRetirement {
		t.Fatal("current pending candidate replacement not explicit")
	}
	review, err := owner.Begin(ctx, app.BeginSynthesisHistoricalRepublish{SynthesisHistoricalRepublishTarget: target, IdempotencyKey: "v2-history"})
	check(err)
	apply := app.ApplySynthesisHistoricalRepublish{WorkspaceID: f.workspace, NoteID: selected.Note.ID, AttemptID: review.AttemptID, IdempotencyKey: "v2-history-apply", PreviewFingerprint: review.PreviewFingerprint, ConfirmExactRestore: true, RetireCurrentCandidate: true}
	// 文件变化不得静默将冻结的选定版本转换为一次新合并。
	check(os.WriteFile(absolute, append(changed, []byte("\nDrift\n")...), 0600))
	if _, err = owner.Apply(ctx, apply); err == nil {
		t.Fatal("file drift accepted")
	}
	check(os.WriteFile(absolute, changed, 0600))
	anchors, err := NewGORMAnchorStore(f.platform, f.sources, anchorModelProofFixture{})
	check(err)
	anchor, err := anchors.CreateAnchor(ctx, app.CreateAnchorCommand{WorkspaceID: f.workspace, IdempotencyKey: "historical-current-scope", NoteID: selected.Note.ID, ExpectedNoteVersion: selected.Note.Version, BasisRevisionID: selected.CurrentRevision.ID, Title: "Current narrowed scope", Scope: domain.AnchorScope{Topics: []string{"Scheduling"}, Audiences: []string{"Developers"}, Description: "Current scope remains unchanged by historical selection"}})
	check(err)
	if _, err = owner.Apply(ctx, apply); err == nil {
		t.Fatal("anchor created after preview was not detected")
	}
	target, err = owner.Target(ctx, f.workspace, selected.Note.ID, selected.CurrentRevision.ID)
	check(err)
	review, err = owner.Begin(ctx, app.BeginSynthesisHistoricalRepublish{SynthesisHistoricalRepublishTarget: target, IdempotencyKey: "v2-history-current-scope"})
	check(err)
	if string(review.CurrentScope) == "null" || len(review.CurrentScope) == 0 {
		t.Fatal("current anchor scope not exposed for review")
	}
	apply.AttemptID = review.AttemptID
	apply.PreviewFingerprint = review.PreviewFingerprint
	apply.IdempotencyKey = "v2-history-current-scope-apply"
	applied, err := owner.Apply(ctx, apply)
	check(err)
	fresh, err := f.service.GetNote(ctx, f.workspace, selected.Note.ID)
	check(err)
	var scopeVersion int64
	check(f.db.Raw(`SELECT scope_version FROM organizing.knowledge_anchor WHERE id=?`, string(anchor.Anchor.ID)).Scan(&scopeVersion).Error)
	if scopeVersion != anchor.Anchor.ScopeVersion {
		t.Fatal("historical selection changed anchor scope")
	}
	if !reflect.DeepEqual(fresh.CurrentRevision.Manuscript, selected.CurrentRevision.Manuscript) || !reflect.DeepEqual(fresh.CurrentRevision.Items, selected.CurrentRevision.Items) || fresh.CurrentRevision.ParentRevisionID != selected.CurrentRevision.ID {
		t.Fatal("v2 manuscript/trust mapping was changed")
	}
	var status string
	check(f.db.Raw(`SELECT status FROM change_control.proposal WHERE id=?`, string(selected.Publication.ProposalID)).Scan(&status).Error)
	if status != "needs_revision" {
		t.Fatal("old pending proposal not retired")
	}
	bad := apply
	bad.ConfirmExactRestore = false
	if _, err = owner.Apply(ctx, bad); err == nil {
		t.Fatal("same apply key changed confirmation accepted")
	}
	p := f.preparePublication(t, fresh)
	written, err := f.node.Execute(ctx, p.input, p.identity)
	check(err)
	f.assertPublished(t, fresh, path, written)
	resumed, err := owner.Resume(ctx, app.ResumeSynthesisHistoricalRepublish{WorkspaceID: f.workspace, NoteID: selected.Note.ID, AttemptID: review.AttemptID})
	check(err)
	if resumed.Result.RevisionID != applied.Result.RevisionID || resumed.Result.ProposalID != applied.Result.ProposalID {
		t.Fatal("published resume changed identity")
	}
	// 直接 SQL 不能使复制记录脱离其精确选定身份，也不能用今天的选择替换历史来源 Profile。
	err = f.db.Exec(`UPDATE organizing.synthesis_historical_republish_event SET payload=payload WHERE attempt_id=?`, string(review.AttemptID)).Error
	if err == nil {
		t.Fatal("historical operation was mutable")
	}
	var wrong int64
	check(f.db.Raw(`SELECT count(*) FROM organizing.synthesis_revision_source r JOIN organizing.synthesis_revision_source s ON s.workspace_id=r.workspace_id AND s.revision_id=? AND s.source_span_id=r.source_span_id WHERE r.revision_id=? AND r.profile_revision_id IS DISTINCT FROM s.profile_revision_id`, string(selected.CurrentRevision.ID), string(fresh.CurrentRevision.ID)).Scan(&wrong).Error)
	if wrong != 0 {
		t.Fatal("v2 source profile changed")
	}
}

func TestSynthesisHistoricalRepublishBodyReferencePostgreSQLGit(t *testing.T) {
	f := newSynthesisGitFixtureWithRootBinding(t, 128, true)
	ctx := t.Context()
	check := func(err error) {
		t.Helper()
		if err != nil {
			for cause := err; cause != nil; cause = errors.Unwrap(cause) {
				t.Logf("cause: %v", cause)
			}
			t.Fatal(err)
		}
	}
	apply := func(record app.SynthesisApplyRecord) app.SynthesisNoteDetail {
		t.Helper()
		r, err := f.service.ApplyGeneration(ctx, record.Input, record.Generation)
		check(err)
		d, err := f.service.GetNote(ctx, f.workspace, r.Publications[0].NoteID)
		check(err)
		return d
	}
	publish := func(d app.SynthesisNoteDetail) app.SynthesisNoteDetail {
		t.Helper()
		p := f.preparePublication(t, d)
		written, err := f.node.Execute(ctx, p.input, p.identity)
		check(err)
		path, err := authoringdomain.DefaultGeneratedTargetPath(d.Note.ID)
		check(err)
		f.assertPublished(t, d, path, written)
		d, err = f.service.GetNote(ctx, f.workspace, d.Note.ID)
		check(err)
		return d
	}
	seed := f.generation(t, 91000, nil, "Original upstream scheduling evidence.")
	seed.Generation.Notes = []app.SynthesisGeneratedNote{f.newTopic(seed, "historical upstream")}
	upstream := publish(apply(seed))
	var upPublication foundation.ID
	check(f.db.Raw(`SELECT publication_id FROM organizing.synthesis_proven_publication WHERE revision_id=?`, string(upstream.CurrentRevision.ID)).Scan(&upPublication).Error)
	up := app.SynthesisGenerationNote{Note: upstream.Note, Revision: *upstream.CurrentRevision, PublicationID: upPublication}
	included := f.generation(t, 92500, []app.SynthesisGenerationNote{up}, "Consumer scheduling context.")
	included.Input.Sources = append(included.Input.Sources, seed.Input.Sources...)
	bn := f.newTopic(included, "historical dependent")
	op, err := domain.IncludeSynthesisPublishedItem(up.Revision, up.PublicationID, up.Revision.Items[0].ID, organizingIntegrationID(92530))
	check(err)
	bn.Delta.Operations = []domain.SynthesisOperation{op}
	included.Generation.Notes = []app.SynthesisGeneratedNote{bn}
	first := publish(apply(included))
	extended := f.generation(t, 94000, []app.SynthesisGenerationNote{{Note: first.Note, Revision: *first.CurrentRevision}}, "Local extension of inherited item.")
	extended.Generation.Notes = []app.SynthesisGeneratedNote{extendedSynthesisNote(first, extended.Input.Sources[0].Reference)}
	selected := publish(apply(extended))
	if len(selected.CurrentRevision.Items[0].Fact.Sources) <= len(up.Revision.Items[0].Fact.Sources) {
		t.Fatal("selected body reference has no local extension")
	}
	next := f.generation(t, 95500, []app.SynthesisGenerationNote{{Note: selected.Note, Revision: *selected.CurrentRevision}}, "A later independent finding.")
	next.Generation.Notes = []app.SynthesisGeneratedNote{extendedSynthesisNote(selected, next.Input.Sources[0].Reference)}
	latest := publish(apply(next))
	// 真实下游依赖纳入了恢复操作将撤回的条目。
	last := latest.CurrentRevision.Items[len(latest.CurrentRevision.Items)-1]
	var latestPublication foundation.ID
	check(f.db.Raw(`SELECT publication_id FROM organizing.synthesis_proven_publication WHERE revision_id=?`, string(latest.CurrentRevision.ID)).Scan(&latestPublication).Error)
	depSource := app.SynthesisGenerationNote{Note: latest.Note, Revision: *latest.CurrentRevision, PublicationID: latestPublication}
	dependent := f.generation(t, 97000, []app.SynthesisGenerationNote{depSource}, "A consumer of the later finding.")
	dependent.Input.Sources = append(dependent.Input.Sources, next.Input.Sources...)
	cn := f.newTopic(dependent, "historical downstream")
	op, err = domain.IncludeSynthesisPublishedItem(depSource.Revision, depSource.PublicationID, last.ID, organizingIntegrationID(97030))
	check(err)
	cn.Delta.Operations = []domain.SynthesisOperation{op}
	dependent.Generation.Notes = []app.SynthesisGeneratedNote{cn}
	consumer := publish(apply(dependent))
	_, err = f.store.ReconcileSynthesisPublicationEvents(ctx, 100)
	check(err)
	_, err = f.store.ReconcileSynthesisBodyImpacts(ctx, 100)
	check(err)
	runtime, _ := candidateRemergeOwner(t, f)
	owner, err := runtime.HistoricalRepublish(app.NewSynthesisManuscriptCaller([]capability.Capability{capability.ReadLocal, capability.WriteProposal}))
	check(err)
	target, err := owner.Target(ctx, f.workspace, selected.Note.ID, selected.CurrentRevision.ID)
	check(err)
	review, err := owner.Begin(ctx, app.BeginSynthesisHistoricalRepublish{SynthesisHistoricalRepublishTarget: target, IdempotencyKey: "body-history"})
	check(err)
	_, err = owner.Apply(ctx, app.ApplySynthesisHistoricalRepublish{WorkspaceID: f.workspace, NoteID: selected.Note.ID, AttemptID: review.AttemptID, IdempotencyKey: "body-history-apply", PreviewFingerprint: review.PreviewFingerprint, ConfirmExactRestore: true})
	check(err)
	restored, err := f.service.GetNote(ctx, f.workspace, selected.Note.ID)
	check(err)
	if !reflect.DeepEqual(restored.CurrentRevision.Items, selected.CurrentRevision.Items) {
		t.Fatal("historical included item or local extension changed")
	}
	f.count(t, "organizing.synthesis_revision_body_reference", "revision_id", string(restored.CurrentRevision.ID), 1)
	restored = publish(restored)
	_, err = f.store.ReconcileSynthesisPublicationEvents(ctx, 100)
	check(err)
	count, err := f.store.ReconcileSynthesisBodyImpacts(ctx, 100)
	check(err)
	if count < 1 {
		t.Fatal("historical retraction did not reach actual downstream dependency")
	}
	_, err = f.store.ReconcileSynthesisBodyRefreshRequests(ctx, 100)
	check(err)
	untouched, err := f.service.GetNote(ctx, f.workspace, consumer.Note.ID)
	check(err)
	if untouched.PublishedRevision.ID != consumer.PublishedRevision.ID {
		t.Fatal("downstream changed without approval")
	}
	count, err = f.store.ReconcileSynthesisBodyImpacts(ctx, 100)
	check(err)
	if count != 0 {
		t.Fatal("publication scan repeated historical impacts")
	}
}

// 选定版本的字节即使相同，仍产生不同的已批准发布身份。
func TestSynthesisHistoricalRepublishSameBytesPostgreSQLGit(t *testing.T) {
	f := newSynthesisGitFixtureWithRootBinding(t, 128, true)
	ctx := t.Context()
	check := func(err error) {
		t.Helper()
		if err != nil {
			for cause := err; cause != nil; cause = errors.Unwrap(cause) {
				t.Logf("cause: %v", cause)
			}
			t.Fatal(err)
		}
	}
	first := f.generation(t, 99000, nil, "Same bytes with independent history identity.")
	first.Generation.Notes = []app.SynthesisGeneratedNote{f.newTopic(first, "same bytes historical")}
	result, err := f.service.ApplyGeneration(ctx, first.Input, first.Generation)
	check(err)
	selected, err := f.service.GetNote(ctx, f.workspace, result.Publications[0].NoteID)
	check(err)
	p := f.preparePublication(t, selected)
	written, err := f.node.Execute(ctx, p.input, p.identity)
	check(err)
	path, err := authoringdomain.DefaultGeneratedTargetPath(selected.Note.ID)
	check(err)
	f.assertPublished(t, selected, path, written)
	runtime, _ := candidateRemergeOwner(t, f)
	owner, err := runtime.HistoricalRepublish(app.NewSynthesisManuscriptCaller([]capability.Capability{capability.ReadLocal, capability.WriteProposal}))
	check(err)
	target, err := owner.Target(ctx, f.workspace, selected.Note.ID, selected.CurrentRevision.ID)
	check(err)
	review, err := owner.Begin(ctx, app.BeginSynthesisHistoricalRepublish{SynthesisHistoricalRepublishTarget: target, IdempotencyKey: "same-bytes"})
	check(err)
	applied, err := owner.Apply(ctx, app.ApplySynthesisHistoricalRepublish{WorkspaceID: f.workspace, NoteID: selected.Note.ID, AttemptID: review.AttemptID, IdempotencyKey: "same-bytes-apply", PreviewFingerprint: review.PreviewFingerprint, ConfirmExactRestore: true})
	check(err)
	if applied.Result == nil || applied.Result.RevisionID == selected.CurrentRevision.ID {
		t.Fatal("same bytes silently treated as old identity")
	}
	fresh, err := f.service.GetNote(ctx, f.workspace, selected.Note.ID)
	check(err)
	pending := f.preparePublication(t, fresh)
	published, err := f.node.Execute(ctx, pending.input, pending.identity)
	check(err)
	f.assertPublished(t, fresh, path, published)
	if published.GitCommit == written.GitCommit {
		t.Fatal("same-byte publication reused old commit")
	}
	var receipt string
	check(f.db.Raw(`SELECT historical_republish_id FROM authoring.document_publication_reservation WHERE article_revision_id=?`, string(fresh.CurrentRevision.ArticleRevisionID)).Scan(&receipt).Error)
	message := synthesisGit(t, ctx, f.root, "show", "-s", "--format=%B", published.GitCommit)
	if !strings.Contains(message, "Zhixu-Historical-Republish-Receipt: "+receipt) {
		t.Fatal("new commit lacks exact history receipt")
	}
	if paths := synthesisGit(t, ctx, f.root, "diff", "--name-only", written.GitCommit, published.GitCommit); paths != "" {
		t.Fatal("same-byte commit changed a path")
	}
	current, err := f.service.ReadPublishedSynthesisNote(ctx, f.workspace, selected.Note.ID)
	check(err)
	if current.RevisionID != fresh.CurrentRevision.ID {
		t.Fatal("new published identity missing")
	}
	// 当 P 已变成另一份字节相同的历史版本时，再次选择 R1。
	target, err = owner.Target(ctx, f.workspace, selected.Note.ID, selected.CurrentRevision.ID)
	check(err)
	nextReview, err := owner.Begin(ctx, app.BeginSynthesisHistoricalRepublish{SynthesisHistoricalRepublishTarget: target, IdempotencyKey: "same-bytes-old-identity"})
	check(err)
	command := app.ApplySynthesisHistoricalRepublish{WorkspaceID: f.workspace, NoteID: selected.Note.ID, AttemptID: nextReview.AttemptID, IdempotencyKey: "same-bytes-old-identity-apply", PreviewFingerprint: nextReview.PreviewFingerprint, ConfirmExactRestore: true}
	nextApplied, err := owner.Apply(ctx, command)
	check(err)
	next, err := f.service.GetNote(ctx, f.workspace, selected.Note.ID)
	check(err)
	nextPending := f.preparePublication(t, next)
	nextWritten, err := f.node.Execute(ctx, nextPending.input, nextPending.identity)
	check(err)
	f.assertPublished(t, next, path, nextWritten)
	if nextWritten.GitCommit == published.GitCommit || next.CurrentRevision.ParentRevisionID != fresh.CurrentRevision.ID {
		t.Fatal("different same-byte history identity lost")
	}
	recovered, err := owner.Resume(ctx, app.ResumeSynthesisHistoricalRepublish{WorkspaceID: f.workspace, NoteID: selected.Note.ID, AttemptID: nextReview.AttemptID})
	check(err)
	if recovered.Result.RevisionID != nextApplied.Result.RevisionID || recovered.Result.ProposalID != nextApplied.Result.ProposalID {
		t.Fatal("same-byte resume changed R3/P3")
	}
	if head := synthesisGit(t, ctx, f.root, "rev-parse", "HEAD"); head != nextWritten.GitCommit {
		t.Fatal("resume made another commit")
	}
	_, err = f.store.ReconcileSynthesisPublicationEvents(ctx, 100)
	check(err)
	var events int64
	check(f.db.Raw(`SELECT count(*) FROM workflow.outbox_event WHERE event_type='organizing.synthesis.published' AND payload->>'note_id'=?`, string(selected.Note.ID)).Scan(&events).Error)
	if events != 3 {
		t.Fatalf("same-byte publications lost distinct events: %d", events)
	}

}

func TestSynthesisHistoricalRepublishBeforeFirstPublication(t *testing.T) {
	f := newSynthesisGitFixtureWithRootBinding(t, 128, true)
	ctx := t.Context()
	check := func(err error) {
		t.Helper()
		if err != nil {
			for cause := err; cause != nil; cause = errors.Unwrap(cause) {
				t.Logf("cause: %v", cause)
			}
			t.Fatal(err)
		}
	}
	first := f.generation(t, 101000, nil, "An unreviewed old candidate.")
	first.Generation.Notes = []app.SynthesisGeneratedNote{f.newTopic(first, "unpublished history")}
	result, err := f.service.ApplyGeneration(ctx, first.Input, first.Generation)
	check(err)
	selected, err := f.service.GetNote(ctx, f.workspace, result.Publications[0].NoteID)
	check(err)
	second := f.generation(t, 102500, []app.SynthesisGenerationNote{{Note: selected.Note, Revision: *selected.CurrentRevision}}, "Another unreviewed candidate.")
	second.Generation.Notes = []app.SynthesisGeneratedNote{extendedSynthesisNote(selected, second.Input.Sources[0].Reference)}
	_, err = f.service.ApplyGeneration(ctx, second.Input, second.Generation)
	check(err)
	runtime, _ := candidateRemergeOwner(t, f)
	owner, err := runtime.HistoricalRepublish(app.NewSynthesisManuscriptCaller([]capability.Capability{capability.ReadLocal, capability.WriteProposal}))
	check(err)
	target, err := owner.Target(ctx, f.workspace, selected.Note.ID, selected.CurrentRevision.ID)
	check(err)
	if target.ExpectedPublishedRevisionID != "" || target.SelectedPublicationID != "" || !target.RequiresRetirement {
		t.Fatal("unpublished history target incorrectly requires P")
	}
	review, err := owner.Begin(ctx, app.BeginSynthesisHistoricalRepublish{SynthesisHistoricalRepublishTarget: target, IdempotencyKey: "before-first-publication"})
	check(err)
	if review.CurrentContent != "" || review.PublishedContent != "" {
		t.Fatal("absent file or publication was fabricated")
	}
	applied, err := owner.Apply(ctx, app.ApplySynthesisHistoricalRepublish{WorkspaceID: f.workspace, NoteID: selected.Note.ID, AttemptID: review.AttemptID, IdempotencyKey: "before-first-publication-apply", PreviewFingerprint: review.PreviewFingerprint, ConfirmExactRestore: true, RetireCurrentCandidate: true})
	check(err)
	fresh, err := f.service.GetNote(ctx, f.workspace, selected.Note.ID)
	check(err)
	if fresh.PublishedRevision != nil || fresh.CurrentRevision.ID != applied.Result.RevisionID {
		t.Fatal("unapproved restoration moved P")
	}
	p := f.preparePublication(t, fresh)
	written, err := f.node.Execute(ctx, p.input, p.identity)
	check(err)
	path, err := authoringdomain.DefaultGeneratedTargetPath(selected.Note.ID)
	check(err)
	f.assertPublished(t, fresh, path, written)
	snapshot, err := f.service.ReadPublishedSynthesisNote(ctx, f.workspace, selected.Note.ID)
	check(err)
	text, err := snapshot.Content()
	check(err)
	want, err := selected.CurrentRevision.Content()
	check(err)
	if text != want {
		t.Fatal("first publication did not restore exact old candidate")
	}
}
