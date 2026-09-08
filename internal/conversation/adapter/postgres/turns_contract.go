package postgres

import (
	"errors"
	conversationapplication "github.com/CodeZen-Lizhi/zhixu/internal/conversation/application"
	conversationdomain "github.com/CodeZen-Lizhi/zhixu/internal/conversation/domain"
)

const questionViewColumns = `
	q.id::text,q.workspace_id::text,q.conversation_id::text,q.ordinal,q.mode,q.question_text,q.scope::text,
	q.answer_depth,q.output_format,q.context_through_ordinal,q.context_hash,q.request_hash,q.created_at`

const questionColumns = `
	id::text,workspace_id::text,conversation_id::text,ordinal,mode,question_text,scope::text,
	answer_depth,output_format,context_through_ordinal,context_hash,request_hash,created_at`

const answerViewColumns = `
	a.id::text,a.workspace_id::text,a.conversation_id::text,a.question_id::text,a.workflow_run_id::text,
	a.model_run_id::text,a.publication_status,a.result_type,a.result::text,a.result_hash,a.retrieval_summary::text,
	a.version,a.created_at,a.updated_at,a.published_at,
	w.id::text,w.status,w.version,w.updated_at,
	(
		SELECT CASE
			WHEN (se.event_type,se.payload_summary->>'stage') IN (
				('rag.plan.started','plan.started'),
				('rag.plan.completed','plan.completed'),
				('rag.retrieval.started','retrieval.started'),
				('rag.retrieval.completed','retrieval.completed'),
				('rag.validation.started','validation.started'),
				('rag.validation.completed','validation.completed')
			) THEN se.payload_summary->>'stage'
			ELSE '__invalid__'
		END AS current_stage
		FROM ops.server_event se
		WHERE se.workspace_id=a.workspace_id
		  AND se.resource_ref='answer:' || a.id::text
		  AND se.payload_summary->>'answer_id'=a.id::text
		  AND se.event_type IN (
			'rag.plan.started','rag.plan.completed',
			'rag.retrieval.started','rag.retrieval.completed',
			'rag.validation.started','rag.validation.completed'
		  )
		ORDER BY se.seq DESC
		LIMIT 1
	) AS current_stage`

const turnViewColumns = questionViewColumns + `,` + answerViewColumns

func validateTurnListQuery(query conversationapplication.ListTurnsQuery) error {
	if err := validateScopedConversationIDs(query.WorkspaceID, query.ConversationID); err != nil {
		return err
	}
	if err := conversationdomain.ValidatePageLimit(query.Limit); err != nil {
		return err
	}
	if query.Cursor != nil {
		if err := query.Cursor.Validate(); err != nil {
			return err
		}
	}
	return nil
}

func validatePublishedContextQuery(query conversationapplication.PublishedContextQuery) error {
	if err := validateScopedConversationIDs(query.WorkspaceID, query.ConversationID); err != nil {
		return err
	}
	if query.ThroughOrdinal < 0 {
		return invalid(ErrorCodePersistenceInvalid, errors.New("published context ordinal is invalid"))
	}
	return nil
}
