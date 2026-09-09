package application

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
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
