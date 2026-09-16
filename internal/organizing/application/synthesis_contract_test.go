package application

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	manuscriptadapter "github.com/CodeZen-Lizhi/zhixu/internal/organizing/adapter/manuscript"
	"github.com/CodeZen-Lizhi/zhixu/internal/organizing/domain"
)

func TestSynthesisGenerationContractBindsExactSourceBytesAndResult(t *testing.T) {
	t.Parallel()
	input, result := synthesisContractFixture()
	if err := input.Validate(); err != nil {
		t.Fatal(err)
	}
	if err := result.Validate(input); err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name   string
		mutate func(*SynthesisGenerationInput)
	}{
		{"different excerpt bytes", func(input *SynthesisGenerationInput) { input.Sources[0].Text += "tampered" }},
		{"foreign workspace", func(input *SynthesisGenerationInput) {
			input.Sources[0].Reference.Source.WorkspaceID = synthesisContractID(99)
		}},
		{"unopened event source", func(input *SynthesisGenerationInput) {
			input.SourceEvent.Source.ParseProjectionID = synthesisContractID(99)
		}},
		{"duplicate source", func(input *SynthesisGenerationInput) { input.Sources = append(input.Sources, input.Sources[0]) }},
		{"outside frozen source scope", func(input *SynthesisGenerationInput) {
			outside := input.Sources[0]
			outside.Reference.Source.SourceID = synthesisContractID(99)
			input.Sources = append(input.Sources, outside)
		}},
		{"oversized excerpt", func(input *SynthesisGenerationInput) {
			input.Sources[0].Text = strings.Repeat("x", MaxSynthesisSourceExcerptBytes+1)
			input.Sources[0].Reference.ExcerptHash = synthesisContractHash(input.Sources[0].Text)
		}},
		{"invalid hash", func(input *SynthesisGenerationInput) { input.RequestHash = "not-a-hash" }},
	}
	for _, current := range cases {
		t.Run(current.name, func(t *testing.T) {
			input, _ := synthesisContractFixture()
			current.mutate(&input)
			if err := input.Validate(); err == nil {
				t.Fatal("invalid generation input was accepted")
			}
		})
	}
	result.RequestHash = synthesisContractHash("another request")
	assertOrganizingApplicationError(t, result.Validate(input), foundation.ErrorInvalidInput, ErrorCodeSynthesisGenerationInvalid)
	_, result = synthesisContractFixture()
	result.Notes[0].NoteID = synthesisContractID(99)
	assertOrganizingApplicationError(t, result.Validate(input), foundation.ErrorInvalidInput, ErrorCodeSynthesisGenerationInvalid)
	_, result = synthesisContractFixture()
	result.Notes[0].Delta = domain.SynthesisDelta{}
	assertOrganizingApplicationError(t, result.Validate(input), foundation.ErrorInvalidInput, ErrorCodeSynthesisGenerationInvalid)
}

func TestSynthesisGenerationContractRejectsCandidateMetadataAndBaseDrift(t *testing.T) {
	t.Parallel()
	input, initial := synthesisContractFixture()
	noteID, documentID, revisionID := synthesisContractID(30), synthesisContractID(31), synthesisContractID(32)
	items, err := domain.ApplySynthesisDelta(input.SourceEvent.Source.WorkspaceID, nil, initial.Notes[0].Delta, []domain.SynthesisSourceRef{input.Sources[0].Reference})
	if err != nil {
		t.Fatal(err)
	}
	body, err := domain.RenderSynthesisMarkdown(input.SourceEvent.Source.WorkspaceID, noteID, initial.Notes[0].Title, items.Items)
	if err != nil {
		t.Fatal(err)
	}
	revision := domain.SynthesisRevision{
		ID: revisionID, WorkspaceID: input.SourceEvent.Source.WorkspaceID, NoteID: noteID, DocumentID: documentID,
		ArticleRevisionID: synthesisContractID(33), RevisionNo: 1, ArticleRevisionNo: 1, Title: initial.Notes[0].Title,
		RendererVersion: domain.SynthesisRendererVersion, ContentHash: synthesisContractHash(body), Items: items.Items, Delta: initial.Notes[0].Delta,
		SourceEventID: input.SourceEvent.ID, WorkflowRunID: input.WorkflowRunID, ModelRunID: initial.ModelRunID, CreatedAt: input.SourceEvent.CreatedAt,
	}
	revision.Hash, err = domain.ComputeSynthesisRevisionHash(revision)
	if err != nil {
		t.Fatal(err)
	}
	note := domain.SynthesisNote{
		ID: noteID, WorkspaceID: revision.WorkspaceID, DocumentID: documentID, CurrentRevisionID: revisionID,
		TopicKey: initial.Notes[0].TopicKey, Title: initial.Notes[0].Title, Aliases: []string{"cache"},
		Version: 1, Status: domain.SynthesisPendingApproval, CreatedAt: input.SourceEvent.CreatedAt, UpdatedAt: input.SourceEvent.CreatedAt,
	}
	input.Notes = []SynthesisGenerationNote{{Note: note, Revision: revision}}
	result := initial
	result.Notes = []SynthesisGeneratedNote{{NoteID: noteID, BaseRevisionID: revisionID, TopicKey: note.TopicKey, Title: note.Title, Aliases: []string{"cache"}, Delta: domain.SynthesisDelta{}}}
	if err := result.Validate(input); err != nil {
		t.Fatalf("empty delta on a frozen existing candidate should be valid: %v", err)
	}
	for _, field := range []string{"title", "key", "aliases", "base"} {
		t.Run(field, func(t *testing.T) {
			modified := result
			modified.Notes = append([]SynthesisGeneratedNote{}, result.Notes...)
			switch field {
			case "title":
				modified.Notes[0].Title = "重写标题"
			case "key":
				modified.Notes[0].TopicKey = "different key"
			case "aliases":
				modified.Notes[0].Aliases = []string{"replacement"}
			case "base":
				modified.Notes[0].BaseRevisionID = synthesisContractID(99)
			}
			assertOrganizingApplicationError(t, modified.Validate(input), foundation.ErrorInvalidInput, ErrorCodeSynthesisGenerationInvalid)
		})
	}
	duplicateTopic := initial
	duplicateTopic.Notes[0].TopicKey = "cache"
	assertOrganizingApplicationError(t, duplicateTopic.Validate(input), foundation.ErrorInvalidInput, ErrorCodeSynthesisGenerationInvalid)
}

func TestSynthesisHistoricalSourceViewCannotRedirectUnavailableEvidence(t *testing.T) {
	t.Parallel()
	input, _ := synthesisContractFixture()
	source := input.Sources[0]
	view := SynthesisSourceView{Reference: source.Reference, Availability: domain.MaterialAvailable, Text: source.Text}
	if err := view.Validate(source.Reference.Source.WorkspaceID); err != nil {
		t.Fatal(err)
	}
	for _, status := range []domain.MaterialAvailability{domain.MaterialStale, domain.MaterialUnavailable} {
		view.Availability = status
		view.Text = "latest content from a different version"
		assertOrganizingApplicationError(t, view.Validate(source.Reference.Source.WorkspaceID), foundation.ErrorInvalidInput, domain.ErrorCodeSynthesisSourceInvalid)
		view.Text = ""
		if err := view.Validate(source.Reference.Source.WorkspaceID); err != nil {
			t.Fatal(err)
		}
		view.SnapshotText = source.Text
		if err := view.Validate(source.Reference.Source.WorkspaceID); err != nil {
			t.Fatal(err)
		}
		view.SnapshotText = "replacement from another version"
		assertOrganizingApplicationError(t, view.Validate(source.Reference.Source.WorkspaceID), foundation.ErrorInvalidInput, domain.ErrorCodeSynthesisSourceInvalid)
		view.SnapshotText = ""
	}
}

func TestSynthesisGenerationContractRejectsTotalSourceInputBudgetOverflow(t *testing.T) {
	t.Parallel()
	input, _ := synthesisContractFixture()
	input.Sources[0].Text = strings.Repeat("x", MaxSynthesisSourceExcerptBytes)
	input.Sources[0].Reference.ExcerptHash = synthesisContractHash(input.Sources[0].Text)
	for index := 1; index <= MaxSynthesisSourceInputBytes/MaxSynthesisSourceExcerptBytes; index++ {
		source := input.Sources[0]
		source.Reference.SourceSpanID = synthesisContractID(100 + index)
		input.Sources = append(input.Sources, source)
	}
	assertOrganizingApplicationError(t, input.Validate(), foundation.ErrorInvalidInput, ErrorCodeSynthesisGenerationInvalid)
}

func synthesisContractFixture() (SynthesisGenerationInput, SynthesisGenerationResult) {
	excerpt := "缓存可以降低重复读取的延迟。\n适用于可复用的查询结果。"
	reference := domain.SynthesisSourceRef{
		Source: domain.SynthesisSourceVersion{WorkspaceID: synthesisContractID(1), SourceID: synthesisContractID(2), SourceVersionID: synthesisContractID(3),
			ContentArtifactID: synthesisContractID(4), ParseProjectionID: synthesisContractID(5), ContentHash: synthesisContractHash("original document")},
		SourceSpanID: synthesisContractID(6), ExcerptHash: synthesisContractHash(excerpt), Title: "缓存资料",
	}
	input := SynthesisGenerationInput{
		ProcessingID: synthesisContractID(7), WorkflowRunID: synthesisContractID(8), NodeRunID: synthesisContractID(9), NodeAttemptID: synthesisContractID(10),
		SourceEvent: domain.SynthesisSourceReady{ID: synthesisContractID(11), Source: reference.Source, IngestionAttemptID: synthesisContractID(12),
			ProcessorVersion: domain.SynthesisProcessorVersion, CreatedAt: time.Date(2026, 9, 8, 1, 0, 0, 0, time.UTC)},
		RequestHash: synthesisContractHash("frozen request"), Sources: []SynthesisSourceExcerpt{{Reference: reference, Text: excerpt}},
	}
	item := domain.SynthesisItem{ID: synthesisContractID(20), Kind: domain.SynthesisFactItem, Fact: &domain.SynthesisStatement{Text: "缓存可以降低重复读取的延迟", Sources: []domain.SynthesisSourceRef{reference}}}
	result := SynthesisGenerationResult{ModelRunID: synthesisContractID(21), RequestHash: input.RequestHash, OutputHash: synthesisContractHash("accepted raw response"),
		Notes: []SynthesisGeneratedNote{{TopicKey: "缓存设计", Title: "缓存设计", Aliases: []string{}, Delta: domain.SynthesisDelta{Operations: []domain.SynthesisOperation{{Kind: domain.SynthesisAddFact, Item: &item}}}}},
	}
	return input, result
}

func synthesisContractID(value int) foundation.ID {
	return foundation.ID(fmt.Sprintf("60000000-0000-4000-8000-%012d", value))
}

func synthesisContractHash(value string) string {
	digest := sha256.Sum256([]byte(value))
	return hex.EncodeToString(digest[:])
}

func TestSynthesisDistinctSourceMustReachItsExistingAssertionSlot(t *testing.T) {
	for _, kind := range []string{"FACT", "CONFLICT"} {
		for _, mode := range []string{"omitted", "support", "other item", "wrong alternative", "already supplemented", "same identity", "unadmitted", "different bytes", "manual text", "legacy"} {
			if mode == "wrong alternative" && kind != "CONFLICT" {
				continue
			}
			t.Run(kind+"/"+mode, func(t *testing.T) {
				input, result := synthesisDistinctSourceFixture(t, kind)
				candidate := &input.Notes[0]
				incoming := input.Sources[1].Reference
				slot := -1
				if kind == "CONFLICT" {
					slot = 0
				}
				op := domain.SynthesisOperation{Kind: domain.SynthesisAddSupport, TargetItemID: candidate.Revision.Items[0].ID, Sources: []domain.SynthesisSourceRef{incoming}}
				if slot >= 0 {
					op.AlternativeIndex = &slot
				}
				wantError := mode == "omitted" || mode == "other item" || mode == "wrong alternative"
				switch mode {
				case "support":
					result.Notes[0].Delta.Operations = []domain.SynthesisOperation{op}
				case "other item":
					fact := domain.SynthesisItem{ID: synthesisContractID(90), Kind: domain.SynthesisFactItem, Fact: &domain.SynthesisStatement{Text: "Different assertion", Sources: []domain.SynthesisSourceRef{incoming}}}
					result.Notes[0].Delta.Operations = []domain.SynthesisOperation{{Kind: domain.SynthesisAddFact, Item: &fact}}
				case "wrong alternative":
					wrong := 1
					op.AlternativeIndex = &wrong
					result.Notes[0].Delta.Operations = []domain.SynthesisOperation{op}
				case "already supplemented":
					candidate.Supplements = []SynthesisSourceSupplement{{ID: synthesisContractID(91), WorkspaceID: candidate.Note.WorkspaceID, NoteID: candidate.Note.ID, BaseRevisionID: candidate.Revision.ID, ItemID: op.TargetItemID, Slot: kind, AlternativeIndex: slot, ProcessingID: synthesisContractID(92), Reference: incoming, CreatedAt: input.SourceEvent.CreatedAt}}
				case "same identity":
					input.SourceEvent.Source = input.Sources[0].Reference.Source
					input.Sources = append(input.Sources[:1], input.Sources[2:]...)
				case "unadmitted":
					other := input.Sources[1]
					other.Reference.SourceSpanID = synthesisContractID(93)
					input.Sources = append(input.Sources, other)
					candidate.Anchor = &SynthesisAnchorBinding{AnchorID: synthesisContractID(94), ScopeVersion: 1, Scope: domain.AnchorScope{Topics: []string{"cache"}, Audiences: []string{"review"}, Description: "cache only"}, AllowedSources: []domain.SynthesisSourceRef{other.Reference}}
					// 另一段已准入原文不同，因此不会产生此补源义务。
					input.Sources[len(input.Sources)-1].Text = "different admitted material"
					input.Sources[len(input.Sources)-1].Reference.ExcerptHash = synthesisContractHash(input.Sources[len(input.Sources)-1].Text)
					candidate.Anchor.AllowedSources[0] = input.Sources[len(input.Sources)-1].Reference
					input.GenerationPromptVersion = SynthesisSourceIdentityAnchoredPromptVersion
				case "different bytes":
					input.Sources[1].Text += " extra context"
					input.Sources[1].Reference.ExcerptHash = synthesisContractHash(input.Sources[1].Text)
				case "manual text":
					machine := domain.SynthesisManuscriptMachine{WorkspaceID: candidate.Note.WorkspaceID, NoteID: candidate.Note.ID, MachineTitle: candidate.Note.Title, MachineItems: candidate.Revision.Items}
					manuscript, err := domain.NewSynthesisManuscript(machine, "人工改写：旧结论不再适用。\n", manuscriptadapter.Mapper{})
					if err != nil {
						t.Fatal(err)
					}
					candidate.Revision.Manuscript = &manuscript
					candidate.Revision.RendererVersion = domain.SynthesisRendererVersionV2
					candidate.Revision.ContentHash = manuscript.ContentHash
					candidate.Revision.Items = []domain.SynthesisItem{}
					candidate.Revision.Hash, err = domain.ComputeSynthesisRevisionHash(candidate.Revision)
					if err != nil {
						t.Fatal(err)
					}
					// 历史 MachineItems 不能授权在本请求中打开旧原文片段。
					input.Sources = input.Sources[1:2]
				case "legacy":
					input.GenerationPromptVersion, input.SemanticPromptVersion = "", SynthesisSemanticFormatPromptVersion
				}
				if err := input.Validate(); err != nil {
					t.Fatalf("invalid test input: %v", err)
				}
				if err := result.Validate(input); (err != nil) != wantError {
					t.Fatalf("omission check err=%v wantError=%t", err, wantError)
				}
				if mode == "omitted" {
					result.Notes = nil
					if err := result.Validate(input); err == nil {
						t.Fatal("empty notes silently discarded new source")
					}
				}
			})
		}
	}
}

func synthesisDistinctSourceFixture(t *testing.T, kind string) (SynthesisGenerationInput, SynthesisGenerationResult) {
	t.Helper()
	input, result := synthesisContractFixture()
	initial := result.Notes[0]
	item := *initial.Delta.Operations[0].Item
	incoming := input.Sources[0]
	incoming.Reference.Source.SourceID = synthesisContractID(80)
	incoming.Reference.Source.SourceVersionID = synthesisContractID(81)
	incoming.Reference.Source.ContentArtifactID = synthesisContractID(82)
	incoming.Reference.Source.ParseProjectionID = synthesisContractID(83)
	incoming.Reference.SourceSpanID = synthesisContractID(84)
	input.SourceEvent.Source = incoming.Reference.Source
	input.Sources = append(input.Sources, incoming)
	if kind == "CONFLICT" {
		other := input.Sources[0]
		other.Text = "An independent conflicting condition."
		other.Reference.ExcerptHash = synthesisContractHash(other.Text)
		other.Reference.SourceSpanID = synthesisContractID(85)
		input.Sources = append(input.Sources, other)
		item.Kind, item.Fact = domain.SynthesisConflictItem, nil
		item.Conflict = &domain.SynthesisConflictContent{Subject: "缓存效果", Alternatives: []domain.SynthesisStatement{{Text: "降低延迟", Sources: []domain.SynthesisSourceRef{input.Sources[0].Reference}}, {Text: "增加延迟", Sources: []domain.SynthesisSourceRef{other.Reference}}}}
		initial.Delta.Operations[0] = domain.SynthesisOperation{Kind: domain.SynthesisAddConflict, Item: &item}
	}
	note := domain.SynthesisNote{ID: synthesisContractID(30), WorkspaceID: input.SourceEvent.Source.WorkspaceID, DocumentID: synthesisContractID(31), CurrentRevisionID: synthesisContractID(32), TopicKey: initial.TopicKey, Title: initial.Title, Aliases: initial.Aliases, Version: 1, Status: domain.SynthesisPendingApproval, CreatedAt: input.SourceEvent.CreatedAt, UpdatedAt: input.SourceEvent.CreatedAt}
	body, err := domain.RenderSynthesisMarkdown(note.WorkspaceID, note.ID, note.Title, []domain.SynthesisItem{item})
	if err != nil {
		t.Fatal(err)
	}
	revision := domain.SynthesisRevision{ID: note.CurrentRevisionID, WorkspaceID: note.WorkspaceID, NoteID: note.ID, DocumentID: note.DocumentID, ArticleRevisionID: synthesisContractID(33), RevisionNo: 1, ArticleRevisionNo: 1, Title: note.Title, RendererVersion: domain.SynthesisRendererVersion, ContentHash: synthesisContractHash(body), Items: []domain.SynthesisItem{item}, Delta: initial.Delta, SourceEventID: input.SourceEvent.ID, WorkflowRunID: input.WorkflowRunID, ModelRunID: result.ModelRunID, CreatedAt: input.SourceEvent.CreatedAt}
	revision.Hash, err = domain.ComputeSynthesisRevisionHash(revision)
	if err != nil {
		t.Fatal(err)
	}
	input.Notes = []SynthesisGenerationNote{{Note: note, Revision: revision}}
	input.GenerationPromptVersion, input.SemanticPromptVersion = SynthesisSourceIdentityLegacyPromptVersion, SynthesisSourceIdentitySemanticPromptVersion
	result.Notes = []SynthesisGeneratedNote{{NoteID: note.ID, BaseRevisionID: revision.ID, TopicKey: note.TopicKey, Title: note.Title, Aliases: note.Aliases, Delta: domain.SynthesisDelta{Operations: []domain.SynthesisOperation{}}}}
	return input, result
}
