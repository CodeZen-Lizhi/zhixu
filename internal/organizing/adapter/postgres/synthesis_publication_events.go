package postgres

import (
	"context"
	"errors"

	app "github.com/CodeZen-Lizhi/zhixu/internal/organizing/application"
)

// ReconcileSynthesisPublicationEvents 补齐所有已证明的发布，包括 Worker 离线期间已被替代的版本。唯一 outbox 键使并发或重启后的协调无需游标也能安全执行。
func (store *GORMSynthesisStore) ReconcileSynthesisPublicationEvents(ctx context.Context, limit int) (int, error) {
	if err := store.ready(ctx); err != nil {
		return 0, err
	}
	if limit < 1 || limit > app.MaxSynthesisListLimit {
		return 0, invalid(errors.New("synthesis publication event batch limit is invalid"))
	}
	result := store.database.WithContext(ctx).Exec(`WITH candidates AS (
 SELECT p.workspace_id,p.note_id,p.revision_id,p.document_id,p.article_revision_id,
 p.projection_hash,p.content_hash,p.publication_id,p.proposal_commit_id,p.git_commit,p.published_at,
 'synthesis.note-published:v1:'||p.revision_id::text AS event_key
 FROM organizing.synthesis_proven_publication p
 WHERE NOT EXISTS (SELECT 1 FROM workflow.outbox_event e
 WHERE e.idempotency_key='synthesis.note-published:v1:'||p.revision_id::text)
 ORDER BY p.published_at,p.publication_id LIMIT ?
 ) INSERT INTO workflow.outbox_event(id,workspace_id,event_type,idempotency_key,event_key,
 schema_version,event_version,payload,occurred_at)
 SELECT gen_random_uuid(),workspace_id,'organizing.synthesis.published',event_key,event_key,1,1,
 jsonb_build_object('workspace_id',workspace_id,'note_id',note_id,'revision_id',revision_id,
 'document_id',document_id,'article_revision_id',article_revision_id,'projection_hash',projection_hash,
 'content_hash',content_hash,'publication_id',publication_id,'proposal_commit_id',proposal_commit_id,
 'git_commit',git_commit),published_at FROM candidates ON CONFLICT (idempotency_key) DO NOTHING`, limit)
	return int(result.RowsAffected), synthesisDBError(ctx, result.Error)
}

var _ app.SynthesisPublicationEventReconciler = (*GORMSynthesisStore)(nil)
