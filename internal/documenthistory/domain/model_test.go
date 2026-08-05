package domain

import (
	"testing"
)

func TestValidPathRequiresCanonicalMarkdownOutsideReservedDirectories(t *testing.T) {
	valid := []string{"notes/java-ai.md", "README.MD", "a.md"}
	for _, value := range valid {
		if !ValidPath(value) {
			t.Fatalf("ValidPath(%q)=false", value)
		}
	}
	invalid := []string{
		"", ".", "..", "notes", "notes/a.markdown", "/notes/a.md", "notes\\a.md",
		"notes/../a.md", "notes/./a.md", "notes//a.md", "notes/a.md/", ".git/config.md",
		".GIT/config.md", ".knowledge/a.md", "notes/a.md\x00x", "notes/a.md\n",
	}
	for _, value := range invalid {
		if ValidPath(value) {
			t.Fatalf("ValidPath(%q)=true", value)
		}
	}
}

func TestComputePreviewHashBindsEveryRestoreIdentity(t *testing.T) {
	preview := RestorePreview{
		WorkspaceID:             "10000000-0000-4000-8000-000000000001",
		DocumentID:              "10000000-0000-4000-8000-000000000002",
		Path:                    "notes/java-ai.md",
		TargetCommit:            "1d75a4ebf3c77d0c6198f1a3c9f54705e7066708",
		ExpectedHead:            "2d75a4ebf3c77d0c6198f1a3c9f54705e7066708",
		ExpectedDocumentVersion: 7,
		CurrentContentHash:      ComputeHash([]byte("current")),
		TargetContentHash:       ComputeHash([]byte("target")),
		DiffHash:                ComputeHash([]byte("patch")),
	}
	first, err := ComputePreviewHash(preview)
	if err != nil || !ValidHash(first) {
		t.Fatalf("ComputePreviewHash()=(%q,%v)", first, err)
	}
	mutations := []func(*RestorePreview){
		func(value *RestorePreview) { value.WorkspaceID = "10000000-0000-4000-8000-000000000003" },
		func(value *RestorePreview) { value.DocumentID = "10000000-0000-4000-8000-000000000004" },
		func(value *RestorePreview) { value.Path = "notes/other.md" },
		func(value *RestorePreview) { value.TargetCommit = "3d75a4ebf3c77d0c6198f1a3c9f54705e7066708" },
		func(value *RestorePreview) { value.ExpectedHead = "4d75a4ebf3c77d0c6198f1a3c9f54705e7066708" },
		func(value *RestorePreview) { value.ExpectedDocumentVersion++ },
		func(value *RestorePreview) { value.CurrentContentHash = ComputeHash([]byte("other-current")) },
		func(value *RestorePreview) { value.TargetContentHash = ComputeHash([]byte("other-target")) },
		func(value *RestorePreview) { value.DiffHash = ComputeHash([]byte("other-patch")) },
	}
	for index, mutate := range mutations {
		changed := preview
		mutate(&changed)
		value, hashErr := ComputePreviewHash(changed)
		if hashErr != nil {
			t.Fatalf("mutation %d failed: %v", index, hashErr)
		}
		if value == first {
			t.Fatalf("mutation %d did not change preview hash", index)
		}
	}
}
