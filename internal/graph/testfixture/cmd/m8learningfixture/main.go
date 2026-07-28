//go:build integration

// m8learningfixture creates disposable formal-knowledge bindings for M8 smoke tests.
package main

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	"github.com/CodeZen-Lizhi/zhixu/internal/graph/testfixture"
	retrievaldomain "github.com/CodeZen-Lizhi/zhixu/internal/retrieval/domain"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

const commandTimeout = 60 * time.Second

// SeedOutput 仅暴露 smoke 所需的正式知识绑定标识，不包含证据正文。
type SeedOutput struct {
	WorkspaceID     string `json:"workspace_id"`
	ClaimID         string `json:"claim_id"`
	TopicID         string `json:"topic_id"`
	IndexVersionID  string `json:"index_version_id"`
	ChunkID         string `json:"chunk_id"`
	SourceVersionID string `json:"source_version_id"`
	SourceSpanID    string `json:"source_span_id"`
	EvidenceHash    string `json:"evidence_hash"`
}

func main() {
	if err := run(os.Args[1:], os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(args []string, output *os.File) error {
	if len(args) < 1 || output == nil {
		return errors.New("usage: database create|drop <database-url> <database-name> | seed <database-url>")
	}
	ctx, cancel := context.WithTimeout(context.Background(), commandTimeout)
	defer cancel()
	switch args[0] {
	case "database":
		if len(args) != 4 || (args[1] != "create" && args[1] != "drop") {
			return errors.New("usage: database create|drop <database-url> <database-name>")
		}
		return database(ctx, args[1], args[2], args[3], output)
	case "seed":
		if len(args) != 2 {
			return errors.New("usage: seed <database-url>")
		}
		return seed(ctx, args[1], output)
	default:
		return errors.New("m8 learning fixture command is invalid")
	}
}

func database(ctx context.Context, action, rawURL, name string, output *os.File) error {
	if err := validDatabaseName(name); err != nil {
		return err
	}
	maintenance, err := databaseURL(rawURL, "postgres")
	if err != nil {
		return err
	}
	conn, err := pgx.Connect(ctx, maintenance)
	if err != nil {
		return errors.New("maintenance database connection failed")
	}
	defer conn.Close(ctx)
	if action == "create" {
		if _, err := conn.Exec(ctx, "CREATE DATABASE "+quoteIdentifier(name)); err != nil {
			return errors.New("create disposable database failed")
		}
		smokeURL, err := databaseURL(rawURL, name)
		if err != nil {
			return err
		}
		_, err = fmt.Fprint(output, smokeURL)
		return err
	}
	_, _ = conn.Exec(ctx, `SELECT pg_terminate_backend(pid) FROM pg_stat_activity WHERE datname=$1 AND pid <> pg_backend_pid()`, name)
	_, err = conn.Exec(ctx, "DROP DATABASE IF EXISTS "+quoteIdentifier(name))
	return err
}

func seed(ctx context.Context, databaseURL string, output *os.File) error {
	pool, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		return errors.New("fixture database connection failed")
	}
	defer pool.Close()
	if err := pool.Ping(ctx); err != nil {
		return errors.New("fixture database connection failed")
	}
	fixture, err := testfixture.SeedFunctional(ctx, pool)
	if err != nil {
		return errors.New("formal knowledge fixture seed failed")
	}
	var sourceVersionID, sourceSpanID, evidenceHash string
	err = pool.QueryRow(ctx, `SELECT source_version_id::text, source_span_id::text, evidence_hash
		FROM core.claim_source WHERE workspace_id=$1 AND claim_id=$2 ORDER BY id LIMIT 1`, string(fixture.WorkspaceID), string(fixture.FirstClaimID)).Scan(&sourceVersionID, &sourceSpanID, &evidenceHash)
	if err != nil {
		return errors.New("formal evidence binding lookup failed")
	}
	retrieval, err := seedInterviewRetrieval(ctx, pool, fixture.WorkspaceID, foundation.ID(sourceVersionID), foundation.ID(sourceSpanID))
	if err != nil {
		return err
	}
	return json.NewEncoder(output).Encode(SeedOutput{
		WorkspaceID: string(fixture.WorkspaceID), ClaimID: string(fixture.FirstClaimID), TopicID: string(fixture.PrimaryTopicID),
		IndexVersionID: string(retrieval.indexVersionID), ChunkID: string(retrieval.chunkID),
		SourceVersionID: sourceVersionID, SourceSpanID: sourceSpanID, EvidenceHash: evidenceHash,
	})
}

type retrievalBinding struct {
	indexVersionID foundation.ID
	chunkID        foundation.ID
}

func seedInterviewRetrieval(ctx context.Context, pool *pgxpool.Pool, workspaceID, sourceVersionID, sourceSpanID foundation.ID) (retrievalBinding, error) {
	const fixtureContent = "graph integration provenance"
	const chunkStrategyVersion = "m8-learning-smoke/v1"
	var sourceID, projectionID, parserID, parserVersion, parserConfigHash, schemaVersion, contentHash, managedLocation string
	var byteSize int64
	err := pool.QueryRow(ctx, `SELECT source.id::text,projection.id::text,projection.parser_id,projection.parser_version,
		projection.parser_config_hash,projection.schema_version,artifact.content_hash,artifact.managed_location,artifact.byte_size
		FROM core.source_version AS version
		JOIN core.source AS source ON source.id=version.source_id AND source.workspace_id=version.workspace_id
		JOIN ingestion.source_version_projection AS binding ON binding.source_version_id=version.id AND binding.workspace_id=version.workspace_id
		JOIN ingestion.parse_projection AS projection ON projection.id=binding.parse_projection_id AND projection.workspace_id=binding.workspace_id
		JOIN ingestion.source_span AS span ON span.id=$3 AND span.workspace_id=version.workspace_id AND span.parse_projection_id=projection.id
		JOIN core.content_artifact AS artifact ON artifact.id=version.content_artifact_id AND artifact.workspace_id=version.workspace_id
		WHERE version.workspace_id=$1 AND version.id=$2`, string(workspaceID), string(sourceVersionID), string(sourceSpanID)).Scan(
		&sourceID, &projectionID, &parserID, &parserVersion, &parserConfigHash, &schemaVersion, &contentHash, &managedLocation, &byteSize,
	)
	if err != nil {
		return retrievalBinding{}, errors.New("interview retrieval provenance lookup failed")
	}
	content := []byte(fixtureContent)
	if hashBytes(content) != contentHash || int64(len(content)) != byteSize || managedLocation != ".knowledge/sources/"+contentHash {
		return retrievalBinding{}, errors.New("interview retrieval content binding is invalid")
	}
	root := "/tmp/graph-http-integration-" + string(workspaceID)
	managedPath := filepath.Join(root, filepath.FromSlash(managedLocation))
	if err := os.MkdirAll(filepath.Dir(managedPath), 0o700); err != nil {
		return retrievalBinding{}, errors.New("interview retrieval managed directory creation failed")
	}
	if err := os.WriteFile(managedPath, content, 0o600); err != nil {
		return retrievalBinding{}, errors.New("interview retrieval managed content creation failed")
	}

	chunkID, err := newFixtureID()
	if err != nil {
		return retrievalBinding{}, err
	}
	indexID, err := newFixtureID()
	if err != nil {
		return retrievalBinding{}, err
	}
	attemptID, err := newFixtureID()
	if err != nil {
		return retrievalBinding{}, err
	}
	activationID, err := newFixtureID()
	if err != nil {
		return retrievalBinding{}, err
	}
	now := time.Now().UTC().Truncate(time.Microsecond)
	manifest, manifestHash, err := retrievaldomain.CanonicalizeManifest(workspaceID, indexID, []retrievaldomain.ManifestChunk{{
		IndexVersionID: indexID, ChunkID: chunkID, WorkspaceID: workspaceID, ContentHash: contentHash, Sequence: 0,
		ParserVersion: parserVersion, ChunkStrategyVersion: chunkStrategyVersion, SchemaVersion: schemaVersion, CreatedAt: now,
	}})
	if err != nil {
		return retrievalBinding{}, fmt.Errorf("interview retrieval chunk manifest is invalid: %w", err)
	}
	if len(manifest) != 1 {
		return retrievalBinding{}, errors.New("interview retrieval chunk manifest count is invalid")
	}
	sources, sourceManifestHash, excludedSourceCount, err := retrievaldomain.CanonicalizeSourceManifest(workspaceID, indexID, []retrievaldomain.ManifestSource{{
		IndexVersionID: indexID, WorkspaceID: workspaceID, SourceID: foundation.ID(sourceID), SourceVersionID: sourceVersionID,
		ParseProjectionID: foundation.ID(projectionID), SelectionStatus: retrievaldomain.SourceSelectionIncluded, CreatedAt: now,
	}})
	if err != nil {
		return retrievalBinding{}, fmt.Errorf("interview retrieval source manifest is invalid: %w", err)
	}
	if len(sources) != 1 || excludedSourceCount != 0 {
		return retrievalBinding{}, errors.New("interview retrieval source manifest count is invalid")
	}
	if err := pgx.BeginFunc(ctx, pool, func(tx pgx.Tx) error {
		statements := []struct {
			query string
			args  []any
		}{
			{`INSERT INTO ingestion.attempt(
				id,workspace_id,source_version_id,parse_projection_id,status,security_status,parser_id,parser_version,
				parser_config_hash,chunk_strategy_version,schema_version,idempotency_key,attempt_number,warnings,
				started_at,completed_at,version
			) VALUES($1,$2,$3,$4,'chunked','passed',$5,$6,$7,$8,$9,$10,1,'[]',$11,$11,1)`, []any{
				string(attemptID), string(workspaceID), string(sourceVersionID), projectionID, parserID, parserVersion,
				parserConfigHash, chunkStrategyVersion, schemaVersion, "m8-learning-attempt-" + string(sourceVersionID), now,
			}},
			{`INSERT INTO ingestion.canonical_chunk(
				id,workspace_id,parse_projection_id,sequence,heading_path,content,content_hash,source_span_id,byte_count,rune_count,
				parser_version,chunk_strategy_version,schema_version,atomic_oversized,status,created_at
			) VALUES($1,$2,$3,0,'[]',$4,$5,$6,$7,$8,$9,$10,$11,false,'active',$12)`, []any{
				string(chunkID), string(workspaceID), projectionID, fixtureContent, contentHash, string(sourceSpanID), byteSize,
				utf8.RuneCount(content), parserVersion, chunkStrategyVersion, schemaVersion, now,
			}},
			{`INSERT INTO retrieval.index_version(
				id,workspace_id,tokenizer_id,tokenizer_version,tokenizer_config_hash,fusion_config,source_snapshot_ref,
				manifest_hash,expected_chunk_count,idempotency_key,status,degraded_capabilities,version,created_at,updated_at,
				source_manifest_hash,expected_source_count,source_parser_id,source_parser_version,source_parser_config_hash,
				source_chunk_strategy_version,source_schema_version
			) VALUES($1,$2,'unicode','v1',$3,'{}','m8-learning-smoke',$4,1,$5,'building','["vector"]',1,$6,$6,$7,1,$8,$9,$3,$10,$11)`, []any{
				string(indexID), string(workspaceID), parserConfigHash, manifestHash,
				"m8-learning-index-" + string(indexID), now, sourceManifestHash,
				parserID, parserVersion, chunkStrategyVersion, schemaVersion,
			}},
			{`INSERT INTO retrieval.index_manifest_chunk(
				index_version_id,chunk_id,workspace_id,content_hash,sequence,parser_version,chunk_strategy_version,schema_version,created_at
			) VALUES($1,$2,$3,$4,0,$5,$6,$7,$8)`, []any{
				string(indexID), string(chunkID), string(workspaceID), contentHash, parserVersion, chunkStrategyVersion, schemaVersion, now,
			}},
			{`INSERT INTO retrieval.index_manifest_source(
				index_version_id,workspace_id,source_id,source_version_id,parse_projection_id,selection_status,created_at
			) VALUES($1,$2,$3,$4,$5,'included',$6)`, []any{
				string(indexID), string(workspaceID), sourceID, string(sourceVersionID), projectionID, now,
			}},
			{`INSERT INTO retrieval.chunk_projection(
				index_version_id,chunk_id,workspace_id,search_vector,token_count,lexical_status,vector_status,created_at,updated_at
			) VALUES($1,$2,$3,to_tsvector('simple',$4),cardinality(tsvector_to_array(to_tsvector('simple',$4))),'ready','disabled',$5,$5)`, []any{
				string(indexID), string(chunkID), string(workspaceID), fixtureContent, now,
			}},
		}
		for _, statement := range statements {
			if _, err := tx.Exec(ctx, statement.query, statement.args...); err != nil {
				return fmt.Errorf("seed interview retrieval facts: %w", err)
			}
		}
		builtAt := now.Add(time.Microsecond)
		if _, err := tx.Exec(ctx, `UPDATE retrieval.index_version SET status='ready',version=2,built_at=$2,updated_at=$2 WHERE id=$1`, string(indexID), builtAt); err != nil {
			return fmt.Errorf("ready interview retrieval index: %w", err)
		}
		activatedAt := builtAt.Add(time.Microsecond)
		if _, err := tx.Exec(ctx, `INSERT INTO retrieval.index_activation(
			id,kind,workspace_id,target_index_version_id,target_version,idempotency_key,reason_code,created_at
		) VALUES($1,'activate',$2,$3,3,$4,'m8-learning-smoke',$5)`, string(activationID), string(workspaceID), string(indexID), "activate-"+string(indexID), activatedAt); err != nil {
			return fmt.Errorf("record interview retrieval activation: %w", err)
		}
		if _, err := tx.Exec(ctx, `UPDATE retrieval.index_version SET status='active',version=3,activated_at=$2,updated_at=$2 WHERE id=$1`, string(indexID), activatedAt); err != nil {
			return fmt.Errorf("activate interview retrieval index: %w", err)
		}
		if _, err := tx.Exec(ctx, `SET CONSTRAINTS ALL IMMEDIATE`); err != nil {
			return fmt.Errorf("validate interview retrieval index: %w", err)
		}
		return nil
	}); err != nil {
		return retrievalBinding{}, err
	}
	return retrievalBinding{indexVersionID: indexID, chunkID: chunkID}, nil
}

func newFixtureID() (foundation.ID, error) {
	id, err := foundation.NewUUIDGenerator(nil).New()
	if err != nil {
		return "", errors.New("M8 fixture ID generation failed")
	}
	return id, nil
}

func hashBytes(value []byte) string {
	sum := sha256.Sum256(value)
	return fmt.Sprintf("%x", sum[:])
}

func databaseURL(raw, database string) (string, error) {
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" || !strings.HasPrefix(parsed.Scheme, "postgres") {
		return "", errors.New("ZHIXU_TEST_DATABASE_URL must be a postgres URL")
	}
	parsed.Path = "/" + database
	return parsed.String(), nil
}

func validDatabaseName(value string) error {
	if value == "" || len(value) > 63 {
		return errors.New("database identifier is invalid")
	}
	for _, character := range value {
		if character != '_' && !unicode.IsDigit(character) && !unicode.IsLower(character) {
			return errors.New("database identifier is invalid")
		}
	}
	return nil
}

func quoteIdentifier(value string) string { return `"` + strings.ReplaceAll(value, `"`, `""`) + `"` }
