//go:build integration

package postgres

import (
	"fmt"
	"reflect"
	"testing"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	app "github.com/CodeZen-Lizhi/zhixu/internal/organizing/application"
	"github.com/CodeZen-Lizhi/zhixu/internal/organizing/domain"
	platformpostgres "github.com/CodeZen-Lizhi/zhixu/internal/platform/postgres"
)

func TestSynthesisSupplementPersistence(t *testing.T) {
	f := newSynthesisDBFixture(t)
	ctx := t.Context()
	first := f.generation(t, 71000, nil, "The scheduler selects runnable work.")
	first.Generation.Notes = []app.SynthesisGeneratedNote{f.newTopic(first, "scheduler supplements")}
	created, err := f.service.ApplyGeneration(ctx, first.Input, first.Generation)
	if err != nil {
		t.Fatal(err)
	}
	noteID := created.Publications[0].NoteID
	detail, err := f.store.GetSynthesisNote(ctx, f.workspace, noteID)
	if err != nil {
		t.Fatal(err)
	}
	original := *detail.CurrentRevision
	second := f.generation(t, 72000, []app.SynthesisGenerationNote{{Note: detail.Note, Revision: original}}, "A second source also supports the scheduler statement.")
	second.Generation.Notes = []app.SynthesisGeneratedNote{supportedSynthesisNote(detail, second.Input.Sources[0].Reference)}
	added, err := f.service.ApplyGeneration(ctx, second.Input, second.Generation)
	if err != nil || added.Changed || !added.SourcesChanged || len(added.RevisionIDs) != 0 || len(added.Publications) != 0 {
		t.Fatalf("supplement result: %+v %v", added, err)
	}
	page, err := f.service.ListSupplements(ctx, app.SynthesisSupplementListQuery{WorkspaceID: f.workspace, NoteID: noteID, Limit: 1})
	if err != nil || len(page.Items) != 1 || page.NextID != "" {
		t.Fatalf("supplements: %+v %v", page, err)
	}
	supplement := page.Items[0]
	if supplement.Reference != second.Input.Sources[0].Reference || supplement.BaseRevisionID != original.ID || supplement.ItemID != original.Items[0].ID || supplement.Slot != "FACT" || supplement.ProcessingID != second.Input.ProcessingID {
		t.Fatalf("supplement binding: %+v", supplement)
	}
	_, view, err := f.service.OpenSupplement(ctx, f.workspace, noteID, supplement.ID)
	if err != nil || view.Text != second.Input.Sources[0].Text || view.Availability != domain.MaterialAvailable {
		t.Fatalf("open supplement: %+v %v", view, err)
	}
	if _, _, err := f.service.OpenSupplement(ctx, f.otherWorkspace, noteID, supplement.ID); !organizingIntegrationError(err, foundation.ErrorNotFound, "SYNTHESIS_SOURCE_NOT_FOUND") {
		t.Fatalf("foreign supplement: %v", err)
	}
	if _, err := f.service.ListSupplements(ctx, app.SynthesisSupplementListQuery{WorkspaceID: f.otherWorkspace, NoteID: noteID, Limit: 1}); !organizingIntegrationError(err, foundation.ErrorNotFound, app.ErrorCodeSynthesisNotFound) {
		t.Fatalf("foreign list: %v", err)
	}
	if _, err := f.service.OpenSource(ctx, f.workspace, noteID, original.ID, supplement.Reference); !organizingIntegrationError(err, foundation.ErrorNotFound, "SYNTHESIS_SOURCE_NOT_FOUND") {
		t.Fatalf("supplement leaked into historical citation: %v", err)
	}
	replay, err := f.service.ApplyGeneration(ctx, second.Input, second.Generation)
	if err != nil || !replay.Replayed || !replay.SourcesChanged || replay.Changed {
		t.Fatalf("replay: %+v %v", replay, err)
	}
	current, err := f.store.GetSynthesisNote(ctx, f.workspace, noteID)
	if err != nil || !reflect.DeepEqual(*current.CurrentRevision, original) || current.Publication.ProposalID != detail.Publication.ProposalID {
		t.Fatalf("supplement rewrote history/publication: %v", err)
	}
	f.count(t, "organizing.synthesis_source_supplement", "note_id", string(noteID), 1)
	f.count(t, "organizing.synthesis_revision", "note_id", string(noteID), 1)
	f.count(t, "authoring.document_publication_reservation", "document_id", string(detail.Note.DocumentID), 1)
	// 新处理事件可能引用完全相同的依据；应消费事件，但不新增账本行，也不声称增加了新来源。
	third := f.generation(t, 73000, []app.SynthesisGenerationNote{{Note: detail.Note, Revision: original}}, "Unrelated incoming source for exact evidence redelivery.")
	third.Input.Sources = append(third.Input.Sources, second.Input.Sources[0])
	// 独立补源目录使历史依据成为允许使用的来源。
	third.Input.Notes[0].Supplements = []app.SynthesisSourceSupplement{supplement}
	third.Generation.Notes = []app.SynthesisGeneratedNote{supportedSynthesisNote(detail, supplement.Reference)}
	repeated, err := f.service.ApplyGeneration(ctx, third.Input, third.Generation)
	if err != nil || repeated.Changed || repeated.SourcesChanged {
		t.Fatalf("same support redelivery: %+v %v", repeated, err)
	}
	// 同一次处理结果可同时包含新文本和新证据。
	fourth := f.generation(t, 74000, []app.SynthesisGenerationNote{{Note: detail.Note, Revision: original}}, "The scheduler has independent worker queues.")
	generated := supportedSynthesisNote(detail, fourth.Input.Sources[0].Reference)
	fact := domain.SynthesisItem{ID: organizingIntegrationID(74031), Kind: domain.SynthesisFactItem, Fact: &domain.SynthesisStatement{Text: "Workers have separate queues.", Sources: []domain.SynthesisSourceRef{fourth.Input.Sources[0].Reference}}}
	generated.Delta.Operations = append(generated.Delta.Operations, domain.SynthesisOperation{Kind: domain.SynthesisAddFact, Item: &fact})
	fourth.Generation.Notes = []app.SynthesisGeneratedNote{generated}
	mixed, err := f.service.ApplyGeneration(ctx, fourth.Input, fourth.Generation)
	if err != nil || !mixed.Changed || !mixed.SourcesChanged || len(mixed.RevisionIDs) != 1 {
		t.Fatalf("mixed result: %+v %v", mixed, err)
	}
	f.count(t, "organizing.synthesis_source_supplement", "note_id", string(noteID), 2)
	page, err = f.service.ListSupplements(ctx, app.SynthesisSupplementListQuery{WorkspaceID: f.workspace, NoteID: noteID, Limit: 1})
	if err != nil || len(page.Items) != 1 || page.NextTime == nil || page.NextID == "" {
		t.Fatalf("supplement first page: %+v %v", page, err)
	}
	tail, err := f.service.ListSupplements(ctx, app.SynthesisSupplementListQuery{WorkspaceID: f.workspace, NoteID: noteID, Limit: 1, BeforeTime: page.NextTime, BeforeID: page.NextID})
	if err != nil || len(tail.Items) != 1 || tail.Items[0].ID == page.Items[0].ID || tail.NextID != "" {
		t.Fatalf("supplement next page: %+v %v", tail, err)
	}
	history, err := f.store.GetSynthesisRevision(ctx, f.workspace, noteID, original.ID)
	if err != nil || !reflect.DeepEqual(history, original) {
		t.Fatalf("history changed: %v", err)
	}
	// 只追加限制和数据库所属模块元组检查同样保护直接 SQL 操作。
	for _, sql := range []string{"UPDATE organizing.synthesis_source_supplement SET title='changed' WHERE id=?", "DELETE FROM organizing.synthesis_source_supplement WHERE id=?"} {
		if err := f.db.Exec(sql, string(supplement.ID)).Error; platformpostgres.SQLState(err) != "55000" {
			t.Fatalf("append-only: %v", err)
		}
	}
	var row synthesisSupplementModel
	if err := f.db.Where("id=?", string(supplement.ID)).Take(&row).Error; err != nil {
		t.Fatal(err)
	}
	row.ID = string(organizingIntegrationID(75001))
	row.ItemID = string(organizingIntegrationID(75002))
	if err := f.db.Create(&row).Error; platformpostgres.SQLState(err) != "23514" {
		t.Fatalf("forged item: %v", err)
	}
	row.ItemID = string(supplement.ItemID)
	row.ExcerptHash = organizingIntegrationHash("forged")
	if err := f.db.Create(&row).Error; platformpostgres.SQLState(err) != "23514" {
		t.Fatalf("forged source hash: %v", err)
	}
	row.ExcerptHash = supplement.Reference.ExcerptHash
	row.WorkspaceID = string(f.otherWorkspace)
	if err := f.db.Create(&row).Error; platformpostgres.SQLState(err) != "23514" && platformpostgres.SQLState(err) != "23503" {
		t.Fatalf("foreign SQL: %v", err)
	}
	if err := f.db.Exec("UPDATE core.source SET removed_at=? WHERE id=?", f.now, string(supplement.Reference.Source.SourceID)).Error; err != nil {
		t.Fatal(err)
	}
	_, view, err = f.service.OpenSupplement(ctx, f.workspace, noteID, supplement.ID)
	if err != nil || view.Availability != domain.MaterialUnavailable || view.Text != "" {
		t.Fatalf("deleted source substituted: %+v %v", view, err)
	}
	replay, err = f.service.ApplyGeneration(ctx, second.Input, second.Generation)
	if err != nil || !replay.Replayed || !replay.SourcesChanged || replay.Changed {
		t.Fatalf("supplement receipt recovery after source removal: %+v %v", replay, err)
	}
}

// 持续增长的账本不得永久阻止后续生成。
func TestSynthesisSupplementLedgerBeyondGenerationBudget(t *testing.T) {
	f := newSynthesisDBFixture(t)
	first := f.generation(t, 100000, nil, "A stable cache conclusion.")
	first.Generation.Notes = []app.SynthesisGeneratedNote{f.newTopic(first, "large supplement ledger")}
	created, err := f.service.ApplyGeneration(t.Context(), first.Input, first.Generation)
	if err != nil {
		t.Fatal(err)
	}
	detail, err := f.store.GetSynthesisNote(t.Context(), f.workspace, created.Publications[0].NoteID)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < domain.MaxSynthesisSources+1; i++ {
		record := f.generation(t, 110000+i*100, []app.SynthesisGenerationNote{{Note: detail.Note, Revision: *detail.CurrentRevision}}, fmt.Sprintf("Further cache evidence %d.", i))
		record.Generation.Notes = []app.SynthesisGeneratedNote{supportedSynthesisNote(detail, record.Input.Sources[0].Reference)}
		result, err := f.service.ApplyGeneration(t.Context(), record.Input, record.Generation)
		if err != nil || result.Changed || !result.SourcesChanged {
			t.Fatalf("support %d: %+v %v", i, result, err)
		}
	}
	candidates, err := f.store.ListSynthesisCandidates(t.Context(), f.workspace)
	if err != nil || len(candidates) != 1 || len(candidates[0].Supplements) != domain.MaxSynthesisSources {
		t.Fatalf("bounded candidate window: %+v %v", candidates, err)
	}
	var count int64
	if err := f.db.Model(&synthesisSupplementModel{}).Where("note_id=?", string(detail.Note.ID)).Count(&count).Error; err != nil || count != domain.MaxSynthesisSources+1 {
		t.Fatalf("ledger was truncated: %d %v", count, err)
	}
	latest, err := f.store.GetSynthesisNote(t.Context(), f.workspace, detail.Note.ID)
	if err != nil || !reflect.DeepEqual(latest.CurrentRevision, detail.CurrentRevision) {
		t.Fatalf("ledger modified body: %v", err)
	}
}
