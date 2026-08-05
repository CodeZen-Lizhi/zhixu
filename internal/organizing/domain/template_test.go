package domain

import (
	"testing"
	"time"
)

func TestBuiltInTemplatesCompileToFixedGovernance(t *testing.T) {
	templates, revisions, err := BuiltInTemplates(time.Date(2026, 8, 3, 10, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	if len(templates) != 4 || len(revisions) != 4 {
		t.Fatalf("got %d templates and %d revisions", len(templates), len(revisions))
	}
	wantResults := []ResultKind{ResultArtifact, ResultMergeProposal, ResultArtifact, ResultArtifact}
	for i := range templates {
		if templates[i].Owner != TemplateBuiltIn || templates[i].CurrentRevisionID != revisions[i].ID {
			t.Fatalf("invalid built-in binding %d", i)
		}
		compiled, err := Compile(revisions[i])
		if err != nil {
			t.Fatal(err)
		}
		if compiled.ResultKind != wantResults[i] || compiled.EvidencePolicy != "REQUIRED" || compiled.ConflictPolicy != "PRESERVE" || compiled.GapPolicy != "EXPLICIT" {
			t.Fatalf("invalid compiled policy: %#v", compiled)
		}
	}
	if !func() bool { compiled, _ := Compile(revisions[0]); return compiled.RequiresOutlineApproval }() {
		t.Fatal("topic article must require outline approval")
	}
	if !func() bool { compiled, _ := Compile(revisions[1]); return compiled.RequiresResultConfirmation }() {
		t.Fatal("merge must require result confirmation")
	}
}

func TestCustomTemplateRevisionIsAppendOnly(t *testing.T) {
	_, builtIns, err := BuiltInTemplates(time.Date(2026, 8, 3, 10, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	template, first, err := CloneBuiltIn(builtIns[2], testID(31), testID(32), testID(33), "Java AI 报告", time.Date(2026, 8, 3, 11, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	declaration := first.Declaration
	declaration.Presentation.Length = LengthLong
	next, second, err := ReviseCustomTemplate(template, 1, testID(34), declaration, time.Date(2026, 8, 3, 12, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	if next.Version != 2 || second.RevisionNo != 2 || first.DeclarationHash == second.DeclarationHash {
		t.Fatalf("revision was not appended: %#v %#v", next, second)
	}
	if _, _, err := ReviseCustomTemplate(next, 1, testID(35), declaration, time.Now()); err == nil {
		t.Fatal("stale template revision succeeded")
	}
	declaration.Kind = TemplateInterviewReview
	if _, _, err := ReviseCustomTemplate(next, 2, testID(36), declaration, time.Now()); err == nil {
		t.Fatal("template kind changed across revisions")
	}
}

func TestTemplateDeclarationRejectsUnsafeOutputAndDuplicateSections(t *testing.T) {
	_, revisions, err := BuiltInTemplates(time.Date(2026, 8, 3, 10, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	declaration := revisions[0].Declaration
	declaration.Output.Directory = "../outside"
	if _, _, err := NewCustomTemplate(testID(41), testID(42), testID(43), declaration, time.Now()); err == nil {
		t.Fatal("unsafe directory accepted")
	}
	declaration = revisions[0].Declaration
	declaration.Sections = append(declaration.Sections, declaration.Sections[0])
	if _, _, err := NewCustomTemplate(testID(44), testID(45), testID(46), declaration, time.Now()); err == nil {
		t.Fatal("duplicate section accepted")
	}
	declaration = revisions[0].Declaration
	declaration.Output.Directory = ".git/organized"
	if _, _, err := NewCustomTemplate(testID(47), testID(48), testID(49), declaration, time.Now()); err == nil {
		t.Fatal("reserved output directory accepted")
	}
}

func TestTemplateDeclarationRequiresGovernanceSections(t *testing.T) {
	_, revisions, err := BuiltInTemplates(time.Date(2026, 8, 3, 10, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range mandatoryGovernanceSections {
		t.Run("missing-"+key, func(t *testing.T) {
			declaration := revisions[0].Declaration
			sections := make([]TemplateSection, 0, len(declaration.Sections)-1)
			for _, section := range declaration.Sections {
				if section.Key != key {
					sections = append(sections, section)
				}
			}
			declaration.Sections = sections
			if _, _, err := NewCustomTemplate(testID(51), testID(52), testID(53), declaration, time.Now()); err == nil {
				t.Fatalf("missing governance section %q was accepted", key)
			}
		})
		t.Run("optional-"+key, func(t *testing.T) {
			declaration := revisions[0].Declaration
			for index := range declaration.Sections {
				if declaration.Sections[index].Key == key {
					declaration.Sections[index].Required = false
				}
			}
			if _, _, err := NewCustomTemplate(testID(54), testID(55), testID(56), declaration, time.Now()); err == nil {
				t.Fatalf("optional governance section %q was accepted", key)
			}
		})
	}
}
