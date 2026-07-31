package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestRunWritesManifestWithoutMillionRecordSideEffect(t *testing.T) {
	directory := filepath.Join(t.TempDir(), "capacity")
	var stdout, stderr bytes.Buffer
	if err := run([]string{
		"--out", directory, "--seed", "capacity-command-test", "--chunks", "3", "--relations", "2",
	}, &stdout, &stderr); err != nil {
		t.Fatalf("run capacity command: %v stderr=%s", err, stderr.String())
	}
	if !strings.Contains(stdout.String(), "generated=false") {
		t.Fatalf("stdout=%q", stdout.String())
	}
	for _, name := range []string{"chunks.jsonl", "relations.jsonl"} {
		if _, err := os.Stat(filepath.Join(directory, name)); !os.IsNotExist(err) {
			t.Fatalf("unexpected generated file %s err=%v", name, err)
		}
	}
	var summary runSummary
	payload, err := os.ReadFile(filepath.Join(directory, "run.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(payload, &summary); err != nil {
		t.Fatal(err)
	}
	if summary.Generated || summary.ChunkCount != 3 || summary.RelationCount != 2 || !slices.Equal(summary.Files, []string{"manifest.json"}) {
		t.Fatalf("summary=%#v", summary)
	}
	assertCapacityCommandModes(t, directory, []string{"manifest.json", "run.json"})
}

func TestRunGenerateStreamsRequestedRecords(t *testing.T) {
	directory := filepath.Join(t.TempDir(), "capacity")
	if err := run([]string{
		"--out", directory, "--seed", "capacity-command-generate-test", "--chunks", "2", "--relations", "3", "--generate",
	}, &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	for name, count := range map[string]int{"chunks.jsonl": 2, "relations.jsonl": 3} {
		payload, err := os.ReadFile(filepath.Join(directory, name))
		if err != nil {
			t.Fatal(err)
		}
		if lines := strings.Count(string(payload), "\n"); lines != count {
			t.Fatalf("%s lines=%d want=%d", name, lines, count)
		}
	}
	assertCapacityCommandModes(t, directory, []string{"manifest.json", "run.json", "chunks.jsonl", "relations.jsonl"})
}

func TestRunWithoutGenerateRemovesPreviousJSONLArtifacts(t *testing.T) {
	directory := filepath.Join(t.TempDir(), "capacity")
	if err := run([]string{
		"--out", directory, "--seed", "capacity-command-cleanup-test", "--chunks", "2", "--relations", "3", "--generate",
	}, &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	if err := run([]string{
		"--out", directory, "--seed", "capacity-command-cleanup-test", "--chunks", "2", "--relations", "3",
	}, &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"chunks.jsonl", "relations.jsonl"} {
		if _, err := os.Stat(filepath.Join(directory, name)); !os.IsNotExist(err) {
			t.Fatalf("stale generated file %s err=%v", name, err)
		}
	}
	var summary runSummary
	payload, err := os.ReadFile(filepath.Join(directory, "run.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(payload, &summary); err != nil {
		t.Fatal(err)
	}
	if summary.Generated || !slices.Equal(summary.Files, []string{"manifest.json"}) {
		t.Fatalf("summary=%#v", summary)
	}
}

func assertCapacityCommandModes(t *testing.T, directory string, files []string) {
	t.Helper()
	info, err := os.Stat(directory)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o700 {
		t.Fatalf("directory mode=%#o", info.Mode().Perm())
	}
	for _, name := range files {
		info, err := os.Stat(filepath.Join(directory, name))
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm() != 0o600 {
			t.Fatalf("%s mode=%#o", name, info.Mode().Perm())
		}
	}
}
