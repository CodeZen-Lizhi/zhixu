package application

import (
	"context"
	"strings"
	"testing"

	gitmerge "github.com/CodeZen-Lizhi/zhixu/internal/changecontrol/adapter/gitmerge"
	"github.com/CodeZen-Lizhi/zhixu/internal/organizing/adapter/manuscript"
	"github.com/CodeZen-Lizhi/zhixu/internal/organizing/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/platform/gitcli"
)

func TestSynthesisManuscriptResolutionRequiresBothConflictStages(t *testing.T) {
	engine, _ := gitmerge.New(gitcli.New("git"))
	p := manuscriptMergeRevision(t, 1, "原始结论")
	l := manuscriptMergeRevision(t, 2, "原始结论")
	content, _ := l.Content()
	machine := manuscriptMergeMachine(l)
	envelope, err := domain.NewSynthesisManuscript(machine, strings.ReplaceAll(content, "原始结论", "候选里已有人工结论"), manuscript.Mapper{})
	if err != nil {
		t.Fatal(err)
	}
	l.Manuscript, l.RendererVersion, l.ContentHash = &envelope, domain.SynthesisRendererVersionV2, envelope.ContentHash
	l.Items, err = envelope.TrustedItems(machine, manuscript.Mapper{})
	if err != nil {
		t.Fatal(err)
	}
	l.Hash, err = domain.ComputeSynthesisRevisionHash(l)
	if err != nil {
		t.Fatal(err)
	}
	next := manuscriptMergeRevision(t, 3, "最新 AI 结论")
	file, _ := p.Content()
	input := SynthesisManuscriptMergeInput{Latest: l, Published: &p, FileExists: true, FileContent: strings.ReplaceAll(file, "原始结论", "磁盘上最新人工结论"), NextMachine: manuscriptMergeMachine(next)}
	first, err := PreviewSynthesisManuscript(context.Background(), input, engine, manuscript.Mapper{})
	if err != nil || first.Review == nil || first.Review.Stage != SynthesisMergeCandidateStage {
		t.Fatalf("missing first review: %+v %v", first, err)
	}
	firstDecision := SynthesisManuscriptResolution{Stage: first.Review.Stage, PreviewFingerprint: first.Fingerprint, AcknowledgedOrdinals: []int{1}, FinalContent: first.Review.Proposed}
	second, err := ResolveSynthesisManuscript(context.Background(), input, []SynthesisManuscriptResolution{firstDecision}, engine, manuscript.Mapper{})
	if err != nil || second.Preview.Manuscript != nil || second.Preview.Review == nil || second.Preview.Review.Stage != SynthesisMergeWorkspaceStage || second.Preview.Fingerprint == first.Fingerprint {
		t.Fatalf("second conflict falsely resolved by first decision: %+v %v", second, err)
	}
	secondDecision := SynthesisManuscriptResolution{Stage: second.Preview.Review.Stage, PreviewFingerprint: second.Preview.Fingerprint, AcknowledgedOrdinals: []int{1}, FinalContent: second.Preview.Review.Current}
	decisions := []SynthesisManuscriptResolution{firstDecision, secondDecision}
	final, err := ResolveSynthesisManuscript(context.Background(), input, decisions, engine, manuscript.Mapper{})
	if err != nil || final.Preview.Manuscript == nil || final.Preview.Review != nil || final.Preview.Manuscript.FullContent != input.FileContent || len(final.Preview.Manuscript.Assessment.Mappings) != 0 {
		t.Fatalf("final resolved content/trust mismatch: %+v %v", final, err)
	}
	replayed, err := ResolveSynthesisManuscript(context.Background(), input, decisions, engine, manuscript.Mapper{})
	if err != nil || replayed.Hash != final.Hash {
		t.Fatalf("decision replay changed: %v", err)
	}
	decisions[0].AcknowledgedOrdinals[0] = 99
	if final.Resolutions[0].AcknowledgedOrdinals[0] != 1 {
		t.Fatal("result aliases input decisions")
	}
	if _, err := ResolveSynthesisManuscript(context.Background(), input, decisions, engine, manuscript.Mapper{}); err == nil {
		t.Fatal("wrong conflict acknowledgement accepted")
	}
	decisions[0].AcknowledgedOrdinals[0] = 1
	input.FileContent += "新一轮人工编辑\n"
	if _, err := ResolveSynthesisManuscript(context.Background(), input, decisions, engine, manuscript.Mapper{}); err == nil {
		t.Fatal("stale preview decision survived changed capture")
	}
}
