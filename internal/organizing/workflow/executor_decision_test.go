package workflow

import (
	"strings"
	"testing"
)

func TestDecodeMergeDecisionAllowsRejectionWithoutTargetPath(t *testing.T) {
	decision, err := decodeMergeDecision([]byte(`{"approved":false}`))
	if err != nil {
		t.Fatal(err)
	}
	if decision.Approved || decision.TargetPath != "" {
		t.Fatalf("decision=%+v", decision)
	}

	decision, err = decodeMergeDecision([]byte(`{"approved":false,"target_path":"../ignored.md"}`))
	if err != nil {
		t.Fatal(err)
	}
	if decision.Approved || decision.TargetPath != "" {
		t.Fatalf("rejected decision retained target=%+v", decision)
	}
}

func TestDecodeMergeDecisionRequiresCanonicalWorkspaceRelativePathWhenApproved(t *testing.T) {
	decision, err := decodeMergeDecision([]byte(`{"approved":true,"target_path":"organized/merged.md"}`))
	if err != nil {
		t.Fatal(err)
	}
	if !decision.Approved || decision.TargetPath != "organized/merged.md" {
		t.Fatalf("decision=%+v", decision)
	}

	tooLong := strings.Repeat("a", 4094) + ".md"
	tests := map[string]string{
		"missing":       `{"approved":true}`,
		"empty":         `{"approved":true,"target_path":""}`,
		"absolute":      `{"approved":true,"target_path":"/tmp/merged.md"}`,
		"traversal":     `{"approved":true,"target_path":"../merged.md"}`,
		"non canonical": `{"approved":true,"target_path":"organized/../merged.md"}`,
		"backslash":     `{"approved":true,"target_path":"organized\\merged.md"}`,
		"not markdown":  `{"approved":true,"target_path":"organized/merged.txt"}`,
		"reserved":      `{"approved":true,"target_path":".git/merged.md"}`,
		"whitespace":    `{"approved":true,"target_path":" organized/merged.md "}`,
		"newline":       `{"approved":true,"target_path":"organized/merged\nname.md"}`,
		"too long":      `{"approved":true,"target_path":"` + tooLong + `"}`,
	}
	for name, raw := range tests {
		t.Run(name, func(t *testing.T) {
			if decision, err := decodeMergeDecision([]byte(raw)); err == nil {
				t.Fatalf("decision=%+v", decision)
			}
		})
	}
}
