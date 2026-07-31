// capacity-benchmark 生成 M10-03 容量基线的可复现 manifest，按需流式输出 500k Chunk/Relation。
package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/capacity"
)

type runSummary struct {
	SchemaVersion string    `json:"schema_version"`
	GeneratedAt   time.Time `json:"generated_at"`
	Seed          string    `json:"seed"`
	ChunkCount    int64     `json:"chunk_count"`
	RelationCount int64     `json:"relation_count"`
	Generated     bool      `json:"generated"`
	Files         []string  `json:"files"`
}

func main() {
	if err := run(os.Args[1:], os.Stdout, os.Stderr); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(args []string, stdout, stderr io.Writer) error {
	if stdout == nil || stderr == nil {
		return errors.New("capacity benchmark output writers are required")
	}
	flags := flag.NewFlagSet("capacity-benchmark", flag.ContinueOnError)
	flags.SetOutput(stderr)
	out := flags.String("out", "tmp/capacity-benchmark", "artifact directory")
	seed := flags.String("seed", "", "deterministic dataset seed")
	chunks := flags.Int64("chunks", capacity.DefaultChunkCount, "Chunk count")
	relations := flags.Int64("relations", capacity.DefaultRelationCount, "Relation count")
	generate := flags.Bool("generate", false, "also stream chunks.jsonl and relations.jsonl")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return fmt.Errorf("unexpected arguments: %v", flags.Args())
	}
	if *out == "" {
		return errors.New("artifact directory is required")
	}
	spec, err := capacity.DefaultSpec(*seed)
	if err != nil {
		return err
	}
	spec.ChunkCount, spec.RelationCount = *chunks, *relations
	if err := spec.Validate(); err != nil {
		return err
	}
	if err := capacity.PrepareArtifactDirectory(*out); err != nil {
		return err
	}

	files := []string{"manifest.json"}
	if err := capacity.WriteAtomicArtifact(filepath.Join(*out, "manifest.json"), func(writer io.Writer) error {
		return capacity.WriteManifest(writer, spec)
	}); err != nil {
		return fmt.Errorf("write capacity manifest: %w", err)
	}
	if *generate {
		for _, kind := range []string{"chunks", "relations"} {
			path := filepath.Join(*out, kind+".jsonl")
			if _, err := streamJSONL(path, spec, kind); err != nil {
				return fmt.Errorf("write capacity %s: %w", kind, err)
			}
			files = append(files, kind+".jsonl")
		}
	} else if err := removeGeneratedArtifacts(*out); err != nil {
		return err
	}
	summary := runSummary{
		SchemaVersion: "zhixu-capacity-run/v1", GeneratedAt: time.Now().UTC(), Seed: spec.Seed,
		ChunkCount: spec.ChunkCount, RelationCount: spec.RelationCount, Generated: *generate, Files: files,
	}
	if err := capacity.WriteJSONArtifact(filepath.Join(*out, "run.json"), summary); err != nil {
		return fmt.Errorf("write capacity run summary: %w", err)
	}
	fmt.Fprintf(stdout, "capacity manifest ready: seed=%s chunks=%d relations=%d generated=%t out=%s\n",
		spec.Seed, spec.ChunkCount, spec.RelationCount, *generate, *out)
	return nil
}

func removeGeneratedArtifacts(directory string) error {
	for _, name := range []string{"chunks.jsonl", "relations.jsonl"} {
		path := filepath.Join(directory, name)
		if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("remove stale capacity artifact %s: %w", name, err)
		}
	}
	return nil
}

func streamJSONL(path string, spec capacity.Spec, kind string) (int64, error) {
	returnCount := int64(0)
	err := capacity.WriteAtomicArtifact(path, func(writer io.Writer) error {
		var err error
		returnCount, err = capacity.WriteJSONL(writer, spec, kind)
		return err
	})
	return returnCount, err
}
