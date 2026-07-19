//go:build integration

package migration

import (
	"context"
	"testing"
	"time"

	projectmigrations "github.com/CodeZen-Lizhi/zhixu/migrations"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestKnowledgeDomainMigrationSchemaAndEmptyDownUp(t *testing.T) {
	ctx := context.Background()
	pool, cleanup := newMigrationTestDatabase(t, ctx)
	defer cleanup()
	runner, err := NewRunner(pool, projectmigrations.FS)
	if err != nil {
		t.Fatal(err)
	}
	if err := runner.Up(ctx); err != nil {
		t.Fatal(err)
	}
	if err := runner.Up(ctx); err != nil {
		t.Fatal(err)
	}

	assertKnowledgeMigrationShape(t, ctx, pool)
	provider := migrationProvider(t, pool)
	if _, err := provider.Down(ctx); err != nil {
		t.Fatalf("00017 empty Down failed: %v", err)
	}
	var tables, schemaMeta int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM information_schema.tables
		WHERE table_schema='core' AND table_name IN (
			'topic','topic_alias','claim','claim_source','relation','relation_evidence',
			'conflict','conflict_member','knowledge_command_receipt'
		)`).Scan(&tables); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM core.schema_meta
		WHERE key='knowledge_domain'`).Scan(&schemaMeta); err != nil {
		t.Fatal(err)
	}
	if tables != 0 || schemaMeta != 0 {
		t.Fatalf("knowledge Down tables=%d schema_meta=%d", tables, schemaMeta)
	}
	if _, err := provider.Up(ctx); err != nil {
		t.Fatalf("00017 Up after Down failed: %v", err)
	}
	assertKnowledgeMigrationShape(t, ctx, pool)
}

func TestKnowledgeDomainMigrationGuardedDown(t *testing.T) {
	ctx := context.Background()
	pool, cleanup := newMigrationTestDatabase(t, ctx)
	defer cleanup()
	runner, err := NewRunner(pool, projectmigrations.FS)
	if err != nil {
		t.Fatal(err)
	}
	if err := runner.Up(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
INSERT INTO core.workspace(
    id,name,root_path,git_repository_path,git_checked_at,status,created_at,updated_at
) VALUES(
    'a1000000-0000-4000-8000-000000000001','knowledge-guard',
    '/tmp/knowledge-guard','/tmp/knowledge-guard',CURRENT_TIMESTAMP,'active',
    CURRENT_TIMESTAMP,CURRENT_TIMESTAMP
);
INSERT INTO core.topic(
    id,workspace_id,name,normalized_name,description,status,version,created_at,updated_at
) VALUES(
    'a2000000-0000-4000-8000-000000000001',
    'a1000000-0000-4000-8000-000000000001',
    'Guard Topic','guard topic','', 'ACTIVE',1,CURRENT_TIMESTAMP,CURRENT_TIMESTAMP
);`); err != nil {
		t.Fatal(err)
	}
	_, err = migrationProvider(t, pool).Down(ctx)
	assertPostgresCode(t, err, "55000")
}

func TestKnowledgeDomainMigrationFailClosedConstraints(t *testing.T) {
	t.Run("topic alias cannot collide with topic name", func(t *testing.T) {
		ctx := context.Background()
		pool, cleanup := newMigrationTestDatabase(t, ctx)
		defer cleanup()
		migrateKnowledgeTestDatabase(t, ctx, pool)
		insertKnowledgeWorkspace(t, ctx, pool, "b1000000-0000-4000-8000-000000000001")
		if _, err := pool.Exec(ctx, `INSERT INTO core.topic(
			id,workspace_id,name,normalized_name,description,status,version,created_at,updated_at
		) VALUES($1,$2,'Topic A','topic a','', 'ACTIVE',1,CURRENT_TIMESTAMP,CURRENT_TIMESTAMP)`,
			"b2000000-0000-4000-8000-000000000001",
			"b1000000-0000-4000-8000-000000000001"); err != nil {
			t.Fatal(err)
		}
		_, err := pool.Exec(ctx, `INSERT INTO core.topic_alias(
			id,workspace_id,topic_id,alias,normalized_alias,created_at
		) VALUES($1,$2,$3,'TOPIC A','topic a',CURRENT_TIMESTAMP)`,
			"b3000000-0000-4000-8000-000000000001",
			"b1000000-0000-4000-8000-000000000001",
			"b2000000-0000-4000-8000-000000000001")
		assertPostgresCode(t, err, "23514")
	})

	t.Run("confirmed claim requires supporting source", func(t *testing.T) {
		ctx := context.Background()
		pool, cleanup := newMigrationTestDatabase(t, ctx)
		defer cleanup()
		migrateKnowledgeTestDatabase(t, ctx, pool)
		workspaceID := "c1000000-0000-4000-8000-000000000001"
		insertKnowledgeWorkspace(t, ctx, pool, workspaceID)
		tx, err := pool.Begin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := tx.Exec(ctx, `INSERT INTO core.claim(
				id,workspace_id,statement,normalized_statement,applicability,
				applicability_schema_version,applicability_hash,status,confidence_score,
				confidence_factors,fingerprint,version,created_at,updated_at
			) VALUES(
				$1,$2,'A confirmed claim','A confirmed claim','{}',
				'knowledge-applicability/v1',repeat('1',64),'SUGGESTED',NULL,
				'{}',repeat('2',64),1,CURRENT_TIMESTAMP,CURRENT_TIMESTAMP
			)`, "c2000000-0000-4000-8000-000000000001", workspaceID); err != nil {
			_ = tx.Rollback(ctx)
			t.Fatal(err)
		}
		if _, err := tx.Exec(ctx, `UPDATE core.claim
			SET status='CONFIRMED',version=2,updated_at=CURRENT_TIMESTAMP
			WHERE id=$1 AND workspace_id=$2`, "c2000000-0000-4000-8000-000000000001", workspaceID); err != nil {
			_ = tx.Rollback(ctx)
			t.Fatal(err)
		}
		assertPostgresCode(t, tx.Commit(ctx), "55000")
	})

	t.Run("confirmed aggregates cannot bypass initial state", func(t *testing.T) {
		ctx := context.Background()
		pool, cleanup := newMigrationTestDatabase(t, ctx)
		defer cleanup()
		migrateKnowledgeTestDatabase(t, ctx, pool)
		workspaceID := "c1100000-0000-4000-8000-000000000001"
		claimA := "c1200000-0000-4000-8000-000000000001"
		claimB := "c1200000-0000-4000-8000-000000000002"
		insertKnowledgeWorkspace(t, ctx, pool, workspaceID)
		_, err := pool.Exec(ctx, `INSERT INTO core.claim(
				id,workspace_id,statement,normalized_statement,applicability,
				applicability_schema_version,applicability_hash,status,confidence_score,
				confidence_factors,fingerprint,version,created_at,updated_at
			) VALUES(
				$1,$2,'direct confirmed','direct confirmed','{}','knowledge-applicability/v1',
				repeat('1',64),'CONFIRMED',NULL,'{}',repeat('2',64),1,
				CURRENT_TIMESTAMP,CURRENT_TIMESTAMP
			)`, claimA, workspaceID)
		assertPostgresCode(t, err, "23514")

		if _, err := pool.Exec(ctx, `INSERT INTO core.claim(
				id,workspace_id,statement,normalized_statement,applicability,
				applicability_schema_version,applicability_hash,status,confidence_score,
				confidence_factors,fingerprint,version,created_at,updated_at
			) VALUES
				($1,$3,'claim a','claim a','{}','knowledge-applicability/v1',repeat('1',64),
				 'SUGGESTED',NULL,'{}',repeat('3',64),1,CURRENT_TIMESTAMP,CURRENT_TIMESTAMP),
				($2,$3,'claim b','claim b','{}','knowledge-applicability/v1',repeat('1',64),
				 'SUGGESTED',NULL,'{}',repeat('4',64),1,CURRENT_TIMESTAMP,CURRENT_TIMESTAMP)`,
			claimA, claimB, workspaceID); err != nil {
			t.Fatal(err)
		}
		_, err = pool.Exec(ctx, `INSERT INTO core.relation(
				id,workspace_id,source_node_type,source_node_id,target_node_type,target_node_id,
				relation_type,status,confidence_score,fingerprint,evidence_fingerprint,
				confirmation_method,confirmation_ref,valid_from,valid_to,version,created_at,updated_at
			) VALUES(
				'c1300000-0000-4000-8000-000000000001',$1,'CLAIM',$2,'CLAIM',$3,
				'SUPPORTS','CONFIRMED',NULL,repeat('5',64),repeat('6',64),
				'USER_APPROVAL','approval:direct',NULL,NULL,1,CURRENT_TIMESTAMP,CURRENT_TIMESTAMP
			)`, workspaceID, claimA, claimB)
		assertPostgresCode(t, err, "23514")
	})

	t.Run("command receipt requires known command aggregate binding", func(t *testing.T) {
		ctx := context.Background()
		pool, cleanup := newMigrationTestDatabase(t, ctx)
		defer cleanup()
		migrateKnowledgeTestDatabase(t, ctx, pool)
		workspaceID := "c2100000-0000-4000-8000-000000000001"
		topicID := "c2200000-0000-4000-8000-000000000001"
		insertKnowledgeWorkspace(t, ctx, pool, workspaceID)
		if _, err := pool.Exec(ctx, `INSERT INTO core.topic(
				id,workspace_id,name,normalized_name,description,status,version,created_at,updated_at
			) VALUES($1,$2,'receipt topic','receipt topic','','ACTIVE',1,CURRENT_TIMESTAMP,CURRENT_TIMESTAMP)`,
			topicID, workspaceID); err != nil {
			t.Fatal(err)
		}
		_, err := pool.Exec(ctx, `INSERT INTO core.knowledge_command_receipt(
				workspace_id,idempotency_key,request_hash,command_type,aggregate_type,
				aggregate_id,aggregate_version,created_at
			) VALUES($1,'unknown-command',repeat('1',64),'unknown.command','TOPIC',$2,1,CURRENT_TIMESTAMP)`,
			workspaceID, topicID)
		assertPostgresCode(t, err, "23514")
		_, err = pool.Exec(ctx, `INSERT INTO core.knowledge_command_receipt(
				workspace_id,idempotency_key,request_hash,command_type,aggregate_type,
				aggregate_id,aggregate_version,created_at
			) VALUES($1,'wrong-aggregate',repeat('2',64),'claim.suggest','TOPIC',$2,1,CURRENT_TIMESTAMP)`,
			workspaceID, topicID)
		assertPostgresCode(t, err, "23514")
	})

	t.Run("relation matrix rejects invalid belongs to endpoints", func(t *testing.T) {
		ctx := context.Background()
		pool, cleanup := newMigrationTestDatabase(t, ctx)
		defer cleanup()
		migrateKnowledgeTestDatabase(t, ctx, pool)
		workspaceID := "d1000000-0000-4000-8000-000000000001"
		insertKnowledgeWorkspace(t, ctx, pool, workspaceID)
		for _, topic := range []struct{ id, name string }{
			{"d2000000-0000-4000-8000-000000000001", "topic one"},
			{"d2000000-0000-4000-8000-000000000002", "topic two"},
		} {
			if _, err := pool.Exec(ctx, `INSERT INTO core.topic(
				id,workspace_id,name,normalized_name,description,status,version,created_at,updated_at
			) VALUES($1,$2,$3,$3,'','ACTIVE',1,CURRENT_TIMESTAMP,CURRENT_TIMESTAMP)`,
				topic.id, workspaceID, topic.name); err != nil {
				t.Fatal(err)
			}
		}
		_, err := pool.Exec(ctx, `INSERT INTO core.relation(
			id,workspace_id,source_node_type,source_node_id,target_node_type,target_node_id,
			relation_type,status,confidence_score,fingerprint,evidence_fingerprint,
			confirmation_method,confirmation_ref,valid_from,valid_to,version,created_at,updated_at
		) VALUES(
			$1,$2,'TOPIC',$3,'TOPIC',$4,'BELONGS_TO','SUGGESTED',NULL,
			repeat('3',64),NULL,NULL,NULL,NULL,NULL,1,CURRENT_TIMESTAMP,CURRENT_TIMESTAMP
		)`, "d3000000-0000-4000-8000-000000000001", workspaceID,
			"d2000000-0000-4000-8000-000000000001",
			"d2000000-0000-4000-8000-000000000002")
		assertPostgresCode(t, err, "23514")
	})

	t.Run("historical claim requires relations to retire first", func(t *testing.T) {
		ctx := context.Background()
		pool, cleanup := newMigrationTestDatabase(t, ctx)
		defer cleanup()
		migrateKnowledgeTestDatabase(t, ctx, pool)
		workspaceID := "f1000000-0000-4000-8000-000000000001"
		claimA := "f2000000-0000-4000-8000-000000000001"
		claimB := "f2000000-0000-4000-8000-000000000002"
		relationID := "f3000000-0000-4000-8000-000000000001"
		insertKnowledgeWorkspace(t, ctx, pool, workspaceID)
		if _, err := pool.Exec(ctx, `INSERT INTO core.claim(
				id,workspace_id,statement,normalized_statement,applicability,
				applicability_schema_version,applicability_hash,status,confidence_score,
				confidence_factors,fingerprint,version,created_at,updated_at
			) VALUES
				($1,$3,'claim a','claim a','{}','knowledge-applicability/v1',repeat('1',64),
				 'SUGGESTED',NULL,'{}',repeat('2',64),1,CURRENT_TIMESTAMP,CURRENT_TIMESTAMP),
				($2,$3,'claim b','claim b','{}','knowledge-applicability/v1',repeat('1',64),
				 'SUGGESTED',NULL,'{}',repeat('3',64),1,CURRENT_TIMESTAMP,CURRENT_TIMESTAMP);
			`, claimA, claimB, workspaceID); err != nil {
			t.Fatal(err)
		}
		if _, err := pool.Exec(ctx, `INSERT INTO core.relation(
				id,workspace_id,source_node_type,source_node_id,target_node_type,target_node_id,
				relation_type,status,confidence_score,fingerprint,evidence_fingerprint,
				confirmation_method,confirmation_ref,valid_from,valid_to,version,created_at,updated_at
			) VALUES(
					$1,$2,'CLAIM',$3,'CLAIM',$4,'SUPPORTS','SUGGESTED',NULL,repeat('4',64),
				NULL,NULL,NULL,NULL,NULL,1,CURRENT_TIMESTAMP,CURRENT_TIMESTAMP
			)`, relationID, workspaceID, claimA, claimB); err != nil {
			t.Fatal(err)
		}

		tx, err := pool.Begin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := tx.Exec(ctx, `UPDATE core.claim
				SET status='INVALID',version=2,updated_at=CURRENT_TIMESTAMP
				WHERE id=$1 AND workspace_id=$2`, claimA, workspaceID); err != nil {
			_ = tx.Rollback(ctx)
			t.Fatal(err)
		}
		assertPostgresCode(t, tx.Commit(ctx), "55000")

		tx, err = pool.Begin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := tx.Exec(ctx, `UPDATE core.relation
				SET status='REJECTED',version=2,updated_at=CURRENT_TIMESTAMP
				WHERE id=$1 AND workspace_id=$2`, relationID, workspaceID); err != nil {
			_ = tx.Rollback(ctx)
			t.Fatal(err)
		}
		if _, err := tx.Exec(ctx, `UPDATE core.claim
				SET status='INVALID',version=2,updated_at=CURRENT_TIMESTAMP
				WHERE id=$1 AND workspace_id=$2`, claimA, workspaceID); err != nil {
			_ = tx.Rollback(ctx)
			t.Fatal(err)
		}
		if err := tx.Commit(ctx); err != nil {
			t.Fatalf("retire relation and claim in one transaction: %v", err)
		}
	})

	t.Run("historical topic requires relations to retire first", func(t *testing.T) {
		ctx := context.Background()
		pool, cleanup := newMigrationTestDatabase(t, ctx)
		defer cleanup()
		migrateKnowledgeTestDatabase(t, ctx, pool)
		workspaceID := "a1100000-0000-4000-8000-000000000001"
		topicA := "a1200000-0000-4000-8000-000000000001"
		topicB := "a1200000-0000-4000-8000-000000000002"
		relationID := "a1300000-0000-4000-8000-000000000001"
		insertKnowledgeWorkspace(t, ctx, pool, workspaceID)
		if _, err := pool.Exec(ctx, `INSERT INTO core.topic(
				id,workspace_id,name,normalized_name,description,status,version,created_at,updated_at
			) VALUES
				($1,$3,'topic a','topic a','','ACTIVE',1,CURRENT_TIMESTAMP,CURRENT_TIMESTAMP),
				($2,$3,'topic b','topic b','','ACTIVE',1,CURRENT_TIMESTAMP,CURRENT_TIMESTAMP)`,
			topicA, topicB, workspaceID); err != nil {
			t.Fatal(err)
		}
		if _, err := pool.Exec(ctx, `INSERT INTO core.relation(
				id,workspace_id,source_node_type,source_node_id,target_node_type,target_node_id,
				relation_type,status,confidence_score,fingerprint,evidence_fingerprint,
				confirmation_method,confirmation_ref,valid_from,valid_to,version,created_at,updated_at
			) VALUES(
				$1,$2,'TOPIC',$3,'TOPIC',$4,'IMPACTS','SUGGESTED',NULL,repeat('5',64),
				NULL,NULL,NULL,NULL,NULL,1,CURRENT_TIMESTAMP,CURRENT_TIMESTAMP
			)`, relationID, workspaceID, topicA, topicB); err != nil {
			t.Fatal(err)
		}

		tx, err := pool.Begin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := tx.Exec(ctx, `UPDATE core.topic
				SET status='DEPRECATED',version=2,updated_at=CURRENT_TIMESTAMP
				WHERE id=$1 AND workspace_id=$2`, topicA, workspaceID); err != nil {
			_ = tx.Rollback(ctx)
			t.Fatal(err)
		}
		assertPostgresCode(t, tx.Commit(ctx), "55000")

		tx, err = pool.Begin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := tx.Exec(ctx, `UPDATE core.relation
				SET status='REJECTED',version=2,updated_at=CURRENT_TIMESTAMP
				WHERE id=$1 AND workspace_id=$2`, relationID, workspaceID); err != nil {
			_ = tx.Rollback(ctx)
			t.Fatal(err)
		}
		if _, err := tx.Exec(ctx, `UPDATE core.topic
				SET status='DEPRECATED',version=2,updated_at=CURRENT_TIMESTAMP
				WHERE id=$1 AND workspace_id=$2`, topicA, workspaceID); err != nil {
			_ = tx.Rollback(ctx)
			t.Fatal(err)
		}
		if err := tx.Commit(ctx); err != nil {
			t.Fatalf("retire relation and topic in one transaction: %v", err)
		}
	})

	t.Run("relation insert serializes with claim retirement", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		pool, cleanup := newMigrationTestDatabase(t, ctx)
		defer cleanup()
		migrateKnowledgeTestDatabase(t, ctx, pool)
		workspaceID := "a2100000-0000-4000-8000-000000000001"
		claimA := "a2200000-0000-4000-8000-000000000001"
		claimB := "a2200000-0000-4000-8000-000000000002"
		relationID := "a2300000-0000-4000-8000-000000000001"
		insertKnowledgeWorkspace(t, ctx, pool, workspaceID)
		if _, err := pool.Exec(ctx, `INSERT INTO core.claim(
				id,workspace_id,statement,normalized_statement,applicability,
				applicability_schema_version,applicability_hash,status,confidence_score,
				confidence_factors,fingerprint,version,created_at,updated_at
			) VALUES
				($1,$3,'claim a','claim a','{}','knowledge-applicability/v1',repeat('1',64),
				 'SUGGESTED',NULL,'{}',repeat('6',64),1,CURRENT_TIMESTAMP,CURRENT_TIMESTAMP),
				($2,$3,'claim b','claim b','{}','knowledge-applicability/v1',repeat('1',64),
				 'SUGGESTED',NULL,'{}',repeat('7',64),1,CURRENT_TIMESTAMP,CURRENT_TIMESTAMP)`,
			claimA, claimB, workspaceID); err != nil {
			t.Fatal(err)
		}

		retireTx, err := pool.Begin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := retireTx.Exec(ctx, `UPDATE core.claim
				SET status='INVALID',version=2,updated_at=CURRENT_TIMESTAMP
				WHERE id=$1 AND workspace_id=$2`, claimA, workspaceID); err != nil {
			_ = retireTx.Rollback(ctx)
			t.Fatal(err)
		}
		relationTx, err := pool.Begin(ctx)
		if err != nil {
			_ = retireTx.Rollback(ctx)
			t.Fatal(err)
		}
		inserted := make(chan error, 1)
		go func() {
			_, insertErr := relationTx.Exec(ctx, `INSERT INTO core.relation(
					id,workspace_id,source_node_type,source_node_id,target_node_type,target_node_id,
					relation_type,status,confidence_score,fingerprint,evidence_fingerprint,
					confirmation_method,confirmation_ref,valid_from,valid_to,version,created_at,updated_at
				) VALUES(
					$1,$2,'CLAIM',$3,'CLAIM',$4,'SUPPORTS','SUGGESTED',NULL,repeat('8',64),
					NULL,NULL,NULL,NULL,NULL,1,CURRENT_TIMESTAMP,CURRENT_TIMESTAMP
				)`, relationID, workspaceID, claimA, claimB)
			inserted <- insertErr
		}()
		select {
		case insertErr := <-inserted:
			_ = relationTx.Rollback(ctx)
			_ = retireTx.Rollback(ctx)
			t.Fatalf("relation insert did not wait for endpoint lifecycle lock: %v", insertErr)
		case <-time.After(100 * time.Millisecond):
		}
		if err := retireTx.Commit(ctx); err != nil {
			_ = relationTx.Rollback(ctx)
			t.Fatal(err)
		}
		select {
		case insertErr := <-inserted:
			assertPostgresCode(t, insertErr, "23514")
		case <-ctx.Done():
			_ = relationTx.Rollback(context.Background())
			t.Fatal("relation insert remained blocked after claim retirement committed")
		}
		_ = relationTx.Rollback(context.Background())
	})

	t.Run("endpoint lifecycle locks cover both commit orders", func(t *testing.T) {
		cases := []struct {
			name          string
			nodeType      string
			relationFirst bool
		}{
			{name: "claim relation first", nodeType: "CLAIM", relationFirst: true},
			{name: "topic retirement first", nodeType: "TOPIC", relationFirst: false},
			{name: "topic relation first", nodeType: "TOPIC", relationFirst: true},
		}
		for _, test := range cases {
			t.Run(test.name, func(t *testing.T) {
				ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer cancel()
				pool, cleanup := newMigrationTestDatabase(t, ctx)
				defer cleanup()
				migrateKnowledgeTestDatabase(t, ctx, pool)
				workspaceID := "a3100000-0000-4000-8000-000000000001"
				sourceID := "a3200000-0000-4000-8000-000000000001"
				targetID := "a3200000-0000-4000-8000-000000000002"
				relationID := "a3300000-0000-4000-8000-000000000001"
				insertKnowledgeWorkspace(t, ctx, pool, workspaceID)
				if test.nodeType == "CLAIM" {
					if _, err := pool.Exec(ctx, `INSERT INTO core.claim(
							id,workspace_id,statement,normalized_statement,applicability,
							applicability_schema_version,applicability_hash,status,confidence_score,
							confidence_factors,fingerprint,version,created_at,updated_at
						) VALUES
							($1,$3,'claim a','claim a','{}','knowledge-applicability/v1',repeat('1',64),
							 'SUGGESTED',NULL,'{}',repeat('9',64),1,CURRENT_TIMESTAMP,CURRENT_TIMESTAMP),
							($2,$3,'claim b','claim b','{}','knowledge-applicability/v1',repeat('1',64),
							 'SUGGESTED',NULL,'{}',repeat('a',64),1,CURRENT_TIMESTAMP,CURRENT_TIMESTAMP)`,
						sourceID, targetID, workspaceID); err != nil {
						t.Fatal(err)
					}
				} else {
					if _, err := pool.Exec(ctx, `INSERT INTO core.topic(
							id,workspace_id,name,normalized_name,description,status,version,created_at,updated_at
						) VALUES
							($1,$3,'topic a','topic a','','ACTIVE',1,CURRENT_TIMESTAMP,CURRENT_TIMESTAMP),
							($2,$3,'topic b','topic b','','ACTIVE',1,CURRENT_TIMESTAMP,CURRENT_TIMESTAMP)`,
						sourceID, targetID, workspaceID); err != nil {
						t.Fatal(err)
					}
				}

				insertRelation := func(tx pgx.Tx) error {
					_, err := tx.Exec(ctx, `INSERT INTO core.relation(
							id,workspace_id,source_node_type,source_node_id,target_node_type,target_node_id,
							relation_type,status,confidence_score,fingerprint,evidence_fingerprint,
							confirmation_method,confirmation_ref,valid_from,valid_to,version,created_at,updated_at
						) VALUES(
							$1,$2,$3,$4,$3,$5,'IMPACTS','SUGGESTED',NULL,repeat('b',64),
							NULL,NULL,NULL,NULL,NULL,1,CURRENT_TIMESTAMP,CURRENT_TIMESTAMP
						)`, relationID, workspaceID, test.nodeType, sourceID, targetID)
					return err
				}
				retireEndpoint := func(tx pgx.Tx) error {
					if test.nodeType == "CLAIM" {
						_, err := tx.Exec(ctx, `UPDATE core.claim
								SET status='INVALID',version=2,updated_at=CURRENT_TIMESTAMP
								WHERE id=$1 AND workspace_id=$2`, sourceID, workspaceID)
						return err
					}
					_, err := tx.Exec(ctx, `UPDATE core.topic
							SET status='DEPRECATED',version=2,updated_at=CURRENT_TIMESTAMP
							WHERE id=$1 AND workspace_id=$2`, sourceID, workspaceID)
					return err
				}

				if test.relationFirst {
					relationTx, err := pool.Begin(ctx)
					if err != nil {
						t.Fatal(err)
					}
					if err := insertRelation(relationTx); err != nil {
						_ = relationTx.Rollback(ctx)
						t.Fatal(err)
					}
					retireTx, err := pool.Begin(ctx)
					if err != nil {
						_ = relationTx.Rollback(ctx)
						t.Fatal(err)
					}
					retired := make(chan error, 1)
					go func() { retired <- retireEndpoint(retireTx) }()
					select {
					case retireErr := <-retired:
						_ = retireTx.Rollback(ctx)
						_ = relationTx.Rollback(ctx)
						t.Fatalf("endpoint retirement did not wait for relation endpoint lock: %v", retireErr)
					case <-time.After(100 * time.Millisecond):
					}
					if err := relationTx.Commit(ctx); err != nil {
						_ = retireTx.Rollback(ctx)
						t.Fatal(err)
					}
					select {
					case retireErr := <-retired:
						if retireErr != nil {
							_ = retireTx.Rollback(ctx)
							t.Fatal(retireErr)
						}
					case <-ctx.Done():
						_ = retireTx.Rollback(context.Background())
						t.Fatal("endpoint retirement remained blocked after relation committed")
					}
					assertPostgresCode(t, retireTx.Commit(ctx), "55000")
					return
				}

				retireTx, err := pool.Begin(ctx)
				if err != nil {
					t.Fatal(err)
				}
				if err := retireEndpoint(retireTx); err != nil {
					_ = retireTx.Rollback(ctx)
					t.Fatal(err)
				}
				relationTx, err := pool.Begin(ctx)
				if err != nil {
					_ = retireTx.Rollback(ctx)
					t.Fatal(err)
				}
				inserted := make(chan error, 1)
				go func() { inserted <- insertRelation(relationTx) }()
				select {
				case insertErr := <-inserted:
					_ = relationTx.Rollback(ctx)
					_ = retireTx.Rollback(ctx)
					t.Fatalf("relation insert did not wait for endpoint retirement lock: %v", insertErr)
				case <-time.After(100 * time.Millisecond):
				}
				if err := retireTx.Commit(ctx); err != nil {
					_ = relationTx.Rollback(ctx)
					t.Fatal(err)
				}
				select {
				case insertErr := <-inserted:
					assertPostgresCode(t, insertErr, "23514")
				case <-ctx.Done():
					_ = relationTx.Rollback(context.Background())
					t.Fatal("relation insert remained blocked after endpoint retirement committed")
				}
				_ = relationTx.Rollback(context.Background())
			})
		}
	})

	t.Run("conflict requires two members", func(t *testing.T) {
		ctx := context.Background()
		pool, cleanup := newMigrationTestDatabase(t, ctx)
		defer cleanup()
		migrateKnowledgeTestDatabase(t, ctx, pool)
		workspaceID := "e1000000-0000-4000-8000-000000000001"
		insertKnowledgeWorkspace(t, ctx, pool, workspaceID)
		tx, err := pool.Begin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := tx.Exec(ctx, `INSERT INTO core.conflict(
			id,workspace_id,topic_id,status,severity,summary,applicability_assessment,
			applicability_hash,overlap_reason,fingerprint,resolution,resolution_reference,
			version,created_at,updated_at,resolved_at
		) VALUES(
			$1,$2,NULL,'OPEN','HIGH','missing members','EXACT',repeat('4',64),NULL,
			repeat('5',64),NULL,NULL,1,CURRENT_TIMESTAMP,CURRENT_TIMESTAMP,NULL
		)`, "e2000000-0000-4000-8000-000000000001", workspaceID); err != nil {
			_ = tx.Rollback(ctx)
			t.Fatal(err)
		}
		assertPostgresCode(t, tx.Commit(ctx), "55000")
	})
}

func assertKnowledgeMigrationShape(t *testing.T, ctx context.Context, pool *pgxpool.Pool) {
	t.Helper()
	var tables, triggers, indexes, schemaMeta int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM information_schema.tables
		WHERE table_schema='core' AND table_name IN (
			'topic','topic_alias','claim','claim_source','relation','relation_evidence',
			'conflict','conflict_member','knowledge_command_receipt'
		)`).Scan(&tables); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM pg_trigger
		WHERE tgname LIKE 'knowledge_%' AND NOT tgisinternal`).Scan(&triggers); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM pg_indexes
		WHERE schemaname='core' AND indexname LIKE '%knowledge_%'`).Scan(&indexes); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM core.schema_meta
		WHERE key='knowledge_domain' AND value='m5-05'`).Scan(&schemaMeta); err != nil {
		t.Fatal(err)
	}
	if tables != 9 || triggers != 27 || indexes < 14 || schemaMeta != 1 {
		t.Fatalf("knowledge shape tables=%d triggers=%d indexes=%d schema_meta=%d", tables, triggers, indexes, schemaMeta)
	}
}

func migrateKnowledgeTestDatabase(t *testing.T, ctx context.Context, pool *pgxpool.Pool) {
	t.Helper()
	runner, err := NewRunner(pool, projectmigrations.FS)
	if err != nil {
		t.Fatal(err)
	}
	if err := runner.Up(ctx); err != nil {
		t.Fatal(err)
	}
}

func insertKnowledgeWorkspace(t *testing.T, ctx context.Context, pool *pgxpool.Pool, workspaceID string) {
	t.Helper()
	if _, err := pool.Exec(ctx, `INSERT INTO core.workspace(
		id,name,root_path,git_repository_path,git_checked_at,status,created_at,updated_at
	) VALUES($1,'knowledge-test','/tmp/knowledge-test','/tmp/knowledge-test',
		CURRENT_TIMESTAMP,'active',CURRENT_TIMESTAMP,CURRENT_TIMESTAMP)`, workspaceID); err != nil {
		t.Fatal(err)
	}
}
