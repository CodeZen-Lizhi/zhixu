//go:build integration

package postgres

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	authoringdomain "github.com/CodeZen-Lizhi/zhixu/internal/authoring/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	app "github.com/CodeZen-Lizhi/zhixu/internal/organizing/application"
	"github.com/CodeZen-Lizhi/zhixu/internal/organizing/domain"
)

// B 纳入 A 的缺口，C 再纳入 B 的该缺口。A 还在另一条目中纳入 B 的缺口，形成真实的 A/B 笔记环。初始来源发现、生成及锚点推荐使用既有所属模块测试数据；每次刷新均经过 River 和独立记录的模型。
func TestSynthesisBodyRoundtripPublicationPropagation(t *testing.T) {
	f := newSynthesisGitFixtureAtVersion(t, 134)
	ctx := t.Context()
	check := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	read := func(id foundation.ID) app.SynthesisNoteDetail {
		t.Helper()
		d, err := f.service.GetNote(ctx, f.workspace, id)
		check(err)
		return d
	}
	apply := func(r app.SynthesisApplyRecord) app.SynthesisNoteDetail {
		t.Helper()
		result, err := f.service.ApplyGeneration(ctx, r.Input, r.Generation)
		check(err)
		if len(result.RevisionIDs) != 1 || len(result.Publications) != 1 {
			t.Fatalf("fixture candidate: %+v", result)
		}
		return read(result.Publications[0].NoteID)
	}
	publish := func(d app.SynthesisNoteDetail) app.SynthesisNoteDetail {
		t.Helper()
		prepared := f.preparePublication(t, d)
		written, err := f.node.Execute(ctx, prepared.input, prepared.identity)
		check(err)
		target, err := authoringdomain.DefaultGeneratedTargetPath(d.Note.ID)
		check(err)
		f.assertPublished(t, d, target, written)
		got := read(d.Note.ID)
		if got.PublishedRevision == nil || got.PublishedRevision.ID != d.CurrentRevision.ID {
			t.Fatal("publication did not move formal pointer")
		}
		return got
	}
	published := func(id foundation.ID) app.SynthesisGenerationNote {
		t.Helper()
		notes, err := f.store.ListSynthesisCandidates(ctx, f.workspace)
		check(err)
		for _, n := range notes {
			if n.Note.ID == id && n.PublicationID != "" {
				return n
			}
		}
		t.Fatal("missing exact publication")
		return app.SynthesisGenerationNote{}
	}
	include := func(n app.SynthesisGenerationNote, item, target foundation.ID) domain.SynthesisOperation {
		t.Helper()
		op, err := domain.IncludeSynthesisPublishedItem(n.Revision, n.PublicationID, item, target)
		check(err)
		return op
	}
	file := func(d app.SynthesisNoteDetail) []byte {
		t.Helper()
		target, err := authoringdomain.DefaultGeneratedTargetPath(d.Note.ID)
		check(err)
		b, err := os.ReadFile(filepath.Join(f.root, target))
		check(err)
		return b
	}
	unchanged := func(before app.SynthesisNoteDetail, bytes []byte) {
		t.Helper()
		now := read(before.Note.ID)
		if now.PublishedRevision.ID != before.PublishedRevision.ID || !reflect.DeepEqual(file(now), bytes) {
			t.Fatal("unapproved candidate changed formal pointer/file")
		}
		snapshot, err := f.service.ReadPublishedSynthesisNote(ctx, f.workspace, before.Note.ID)
		check(err)
		if snapshot.RevisionID != before.PublishedRevision.ID {
			t.Fatal("formal reread exposed candidate")
		}
	}
	scan := func(wantImpacts, wantRequests int) {
		t.Helper()
		_, err := f.store.ReconcileSynthesisPublicationEvents(ctx, 100)
		check(err)
		n, err := f.store.ReconcileSynthesisBodyImpacts(ctx, 100)
		check(err)
		if n != wantImpacts {
			t.Fatalf("impacts=%d want=%d", n, wantImpacts)
		}
		n, err = f.store.ReconcileSynthesisBodyRefreshRequests(ctx, 100)
		check(err)
		if n != wantRequests {
			t.Fatalf("requests=%d want=%d", n, wantRequests)
		}
	}
	seed := f.generation(t, 71000, nil, "Scheduling is workload dependent. The version-specific limit is unknown.")
	an := f.newTopic(seed, "roundtrip a")
	aGap, bGap, cGap, aBack := organizingIntegrationID(71030), organizingIntegrationID(72030), organizingIntegrationID(73030), organizingIntegrationID(74030)
	an.Delta.Operations = append(an.Delta.Operations, domain.SynthesisOperation{Kind: domain.SynthesisAddGap, Item: &domain.SynthesisItem{ID: aGap, Kind: domain.SynthesisGapItem, Gap: &domain.SynthesisGapContent{Question: "Which version changes the limit?", Context: "The version is unspecified.", Sources: []domain.SynthesisSourceRef{seed.Input.Sources[0].Reference}}}})
	seed.Generation.Notes = []app.SynthesisGeneratedNote{an}
	a := publish(apply(seed))
	pa := published(a.Note.ID)
	br := f.generation(t, 72000, []app.SynthesisGenerationNote{pa}, "Independent application scheduling context.")
	br.Input.Sources = append(br.Input.Sources, seed.Input.Sources...)
	bn := f.newTopic(br, "roundtrip b")
	bn.Delta.Operations = append(bn.Delta.Operations, include(pa, aGap, bGap))
	br.Generation.Notes = []app.SynthesisGeneratedNote{bn}
	b := publish(apply(br))
	pb := published(b.Note.ID)
	cr := f.generation(t, 73000, []app.SynthesisGenerationNote{pb}, "Independent downstream documentation context.")
	cr.Input.Sources = append(cr.Input.Sources, seed.Input.Sources...)
	cn := f.newTopic(cr, "roundtrip c")
	cn.Delta.Operations = append(cn.Delta.Operations, include(pb, bGap, cGap))
	cr.Generation.Notes = []app.SynthesisGeneratedNote{cn}
	c := publish(apply(cr))
	back := f.generation(t, 74000, []app.SynthesisGenerationNote{pa, pb}, "Application scheduling cross-reference.")
	back.Input.Sources = append(back.Input.Sources, seed.Input.Sources...)
	backNote := supportedSynthesisNote(a, back.Input.Sources[0].Reference)
	backNote.Delta.Operations = []domain.SynthesisOperation{include(pb, bGap, aBack)}
	back.Generation.Notes = []app.SynthesisGeneratedNote{backNote}
	a = publish(apply(back))
	scan(0, 0)
	// 所有由测试数据生成的变更都发生在启用真实应用校验之前。
	ar := f.generation(t, 75000, []app.SynthesisGenerationNote{{Note: a.Note, Revision: *a.CurrentRevision}}, "Version 2 raises the workload limit.")
	update := supportedSynthesisNote(a, ar.Input.Sources[0].Reference)
	update.Delta.Operations = []domain.SynthesisOperation{{Kind: domain.SynthesisResolveGap, TargetItemID: aGap, Resolution: &domain.SynthesisStatement{Text: "Version 2 raises the limit.", Applicability: "Version 2", Sources: []domain.SynthesisSourceRef{ar.Input.Sources[0].Reference}}}}
	ar.Generation.Notes = []app.SynthesisGeneratedNote{update}
	aDraft := apply(ar)
	scan(0, 0) // A 未发布的草稿不能传播。
	a = publish(aDraft)
	aBytes, bBytes, cBytes := file(a), file(b), file(c)
	anchors, err := NewGORMAnchorStore(f.platform, f.sources, anchorModelProofFixture{})
	check(err)
	for i, d := range []app.SynthesisNoteDetail{a, b, c} {
		association := f.generation(t, 76000+i*100, nil, "Scope acceptance seed for "+string(d.Note.ID))
		acceptBodyRoundtripEvidence(t, f, anchors, d, association.Generation.ModelRunID, []domain.SynthesisSourceRef{seed.Input.Sources[0].Reference, ar.Input.Sources[0].Reference})
	}
	// 每个被纳入的缺口都保留相同原始证据，并增加新的解决结论；第三项语义检查验证精确的已发布正文绑定。
	review := `{"checks":[{"index":1,"verdict":"SUPPORTED","sources":[{"source":"S001","verdict":"SUPPORTED"}]},{"index":2,"verdict":"SUPPORTED","sources":[{"source":"S002","verdict":"SUPPORTED"}]},{"index":3,"verdict":"SUPPORTED","sources":[]}]}`
	refresh := `{"notes":[{"note":"N001","operations":[{"kind":"REFRESH_ITEM","target":"I002"}]}]}`
	// B 发布后，C 与 A 可按任一持久化请求顺序调度。下方根据持久化请求选择模型目标，不替换任何处理输出或模型日志事实。
	runtime := newBodyRoundtripRuntime(t, f, anchors, refresh, review)
	scan(1, 1)
	bNew := runBodyRoundtripRequest(t, f, runtime, b.Note.ID, 1)
	assertBodyRoundtripRefresh(t, f, b, bNew, 1, published(a.Note.ID), aGap)
	unchanged(b, bBytes)
	unchanged(c, cBytes)
	unchanged(a, aBytes)
	if read(c.Note.ID).CurrentRevision.ID != c.CurrentRevision.ID {
		t.Fatal("B draft propagated to C")
	}
	scan(0, 0)
	bNew = publish(bNew)
	bBytes = file(bNew)
	// C 的间接纳入关系与 A 的显式回引均发生变化。
	scan(2, 2)
	// 遵循持久化请求顺序，不改写数据行来控制调度。
	for round := 2; round <= 3; round++ {
		var next string
		check(f.db.Raw(`SELECT r.note_id FROM organizing.synthesis_body_refresh_request r WHERE r.workspace_id=? AND NOT EXISTS (SELECT 1 FROM organizing.synthesis_processing p WHERE p.body_refresh_request_id=r.id) ORDER BY r.created_at,r.id LIMIT 1`, string(f.workspace)).Scan(&next).Error)
		before, index := c, 1
		if foundation.ID(next) == a.Note.ID {
			before, index = a, 2
		} else if foundation.ID(next) != c.Note.ID {
			t.Fatalf("unexpected refresh target %s", next)
		}
		output := refresh
		if index == 2 {
			output = strings.ReplaceAll(output, "I002", "I003")
		}
		runtime.provider.configure(output, review)
		candidate := runBodyRoundtripRequest(t, f, runtime, before.Note.ID, round)
		assertBodyRoundtripRefresh(t, f, before, candidate, index, published(b.Note.ID), bGap)
		unchanged(a, aBytes)
		unchanged(c, cBytes)
		if before.Note.ID == a.Note.ID {
			a = publish(candidate)
			aBytes = file(a)
		} else {
			c = publish(candidate)
			cBytes = file(c)
		}
		scan(0, 0)
	}
	// A 刷新后的回引不得使 B 刷新其未改变的 A 缺口。重复扫描检查持久化数量、模型调用及所有指针。
	for range 3 {
		scan(0, 0)
		batch, err := runtime.dispatcher.DispatchBatch(ctx, 10)
		check(err)
		if batch.Started != 0 || runtime.provider.CallCount() != 6 {
			t.Fatal("cycle/replay produced another model execution")
		}
		f.count(t, "organizing.synthesis_body_refresh_request", "workspace_id", string(f.workspace), 3)
		f.count(t, "organizing.synthesis_processing", "workspace_id", string(f.workspace), 3)
		f.count(t, "organizing.synthesis_model_step", "workspace_id", string(f.workspace), 6)
		var modelCalls int64
		check(f.db.Raw(`SELECT count(*) FROM agent.model_call c JOIN organizing.synthesis_model_step s ON s.model_run_id=c.model_run_id WHERE s.workspace_id=? AND s.status='READY' AND c.status='SUCCEEDED'`, string(f.workspace)).Scan(&modelCalls).Error)
		if modelCalls != 6 {
			t.Fatalf("persisted model calls=%d want=6", modelCalls)
		}
		for _, final := range []app.SynthesisNoteDetail{a, bNew, c} {
			f.count(t, "organizing.synthesis_body_refresh_request", "note_id", string(final.Note.ID), 1)
			if read(final.Note.ID).CurrentRevision.ID != final.CurrentRevision.ID {
				t.Fatal("replay moved candidate pointer")
			}
		}
		f.count(t, "organizing.synthesis_revision", "note_id", string(a.Note.ID), 4)
		f.count(t, "organizing.synthesis_revision", "note_id", string(b.Note.ID), 2)
		f.count(t, "organizing.synthesis_revision", "note_id", string(c.Note.ID), 2)
		unchanged(a, aBytes)
		unchanged(bNew, bBytes)
		unchanged(c, cBytes)
	}

}

func runBodyRoundtripRequest(t *testing.T, f *synthesisGitFixture, r *bodyRoundtripRuntime, note foundation.ID, wantCalls int) app.SynthesisNoteDetail {
	t.Helper()
	ctx := t.Context()
	batch, err := r.dispatcher.DispatchBatch(ctx, 1)
	if err != nil || batch.Started != 1 {
		t.Fatalf("dispatch: %+v %v", batch, err)
	}
	var p app.SynthesisProcessing
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		var id string
		err = f.db.Raw(`SELECT p.id FROM organizing.synthesis_processing p JOIN organizing.synthesis_body_refresh_request r ON r.id=p.body_refresh_request_id WHERE r.workspace_id=? AND r.note_id=? ORDER BY p.created_at DESC LIMIT 1`, string(f.workspace), string(note)).Scan(&id).Error
		if err != nil {
			t.Fatal(err)
		}
		if id != "" {
			p, err = r.store.GetSynthesisProcessing(ctx, f.workspace, foundation.ID(id))
			if err != nil {
				t.Fatal(err)
			}
			if p.Status != app.SynthesisProcessingPending && p.Status != app.SynthesisProcessingRunning {
				break
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	if p.Status != app.SynthesisProcessingSucceeded || len(p.RevisionIDs) != 1 || r.provider.CallCount() != 2*wantCalls {
		t.Fatalf("refresh status=%s failure=%+v calls=%d", p.Status, p.Failure, r.provider.CallCount())
	}
	e, err := r.store.LoadSynthesisExecution(ctx, f.workspace, p.ID, p.WorkflowRunID)
	if err != nil {
		t.Fatal(err)
	}
	if e.Input == nil || e.Input.BodyRefresh == nil || e.Generation == nil || e.Semantic == nil || !e.Semantic.Semantic.Accepted || e.Generation.ModelRunID == e.Semantic.ModelRunID {
		t.Fatal("missing independent persisted model proofs")
	}
	after, err := f.service.GetNote(ctx, f.workspace, note)
	if err != nil {
		t.Fatal(err)
	}
	return after
}

func assertBodyRoundtripRefresh(t *testing.T, f *synthesisGitFixture, before, after app.SynthesisNoteDetail, index int, upstream app.SynthesisGenerationNote, item foundation.ID) {
	t.Helper()
	if after.CurrentRevision.RevisionNo != before.CurrentRevision.RevisionNo+1 || after.PublishedRevision.ID != before.PublishedRevision.ID {
		t.Fatal("candidate/formal revision mismatch")
	}
	oldBody, err := before.CurrentRevision.Content()
	if err != nil {
		t.Fatal(err)
	}
	newBody, err := after.CurrentRevision.Content()
	if err != nil {
		t.Fatal(err)
	}
	block := func(body string, id foundation.ID) string {
		t.Helper()
		begin, end := "<!-- synthesis-item:"+string(id)+" -->", "<!-- /synthesis-item:"+string(id)+" -->"
		_, tail, ok := strings.Cut(body, begin)
		if !ok {
			t.Fatal("missing item start marker")
		}
		result, _, ok := strings.Cut(tail, end)
		if !ok {
			t.Fatal("missing item end marker")
		}
		return result
	}
	for i, old := range before.CurrentRevision.Items {
		if i != index && (!reflect.DeepEqual(old, after.CurrentRevision.Items[i]) || block(oldBody, old.ID) != block(newBody, old.ID)) {
			t.Fatalf("unrelated item %d changed", i)
		}
	}
	got := after.CurrentRevision.Items[index]
	if got.Gap.Resolution == nil || !domain.MatchesSynthesisBodyItem(got, upstream.Revision, upstream.PublicationID) || got.BodyReference.ItemID != item {
		t.Fatal("candidate lost exact refreshed body reference")
	}
	var count int64
	err = f.db.Raw(`SELECT count(*) FROM organizing.synthesis_revision_body_reference WHERE workspace_id=? AND revision_id=? AND item_id=? AND upstream_revision_id=? AND publication_id=? AND upstream_item_id=? AND upstream_note_id=? AND upstream_projection_hash=?`, string(f.workspace), string(after.CurrentRevision.ID), string(got.ID), string(upstream.Revision.ID), string(upstream.PublicationID), string(item), string(upstream.Note.ID), upstream.Revision.Hash).Scan(&count).Error
	if err != nil || count != 1 {
		t.Fatalf("persisted exact body reference count=%d: %v", count, err)
	}
	historical, err := f.store.GetSynthesisRevision(t.Context(), f.workspace, before.Note.ID, before.CurrentRevision.ID)
	if err != nil || !reflect.DeepEqual(historical, *before.CurrentRevision) {
		t.Fatalf("immutable old reference changed: %v", err)
	}
}
