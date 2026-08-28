package postgres

const (
	getDocumentSQL = `SELECT
		id::text,workspace_id::text,canonical_path,title,lifecycle_status,
		COALESCE(current_published_revision_id::text,''),version
		FROM core.document
		WHERE workspace_id=$1 AND id=$2`

	// Proposal commits do not store document_id. The Application service first
	// resolves the exact Document and supplies its workspace-unique canonical
	// path, so proposal_mapping deliberately retains the existing workspace/path
	// binding while Article revisions bind the Document directly.
	commitMappingSQL = `WITH requested(git_commit,ordinality) AS (
		SELECT git_commit,ordinality
		FROM unnest($4::text[]) WITH ORDINALITY AS input(git_commit,ordinality)
	), revision_mapping AS (
		SELECT requested.ordinality,requested.git_commit,revision.id,revision.revision_no
		FROM requested
		JOIN core.article_revision AS revision
		  ON revision.workspace_id=$1 AND revision.document_id=$2
		 AND revision.git_commit=requested.git_commit
		UNION
		SELECT requested.ordinality,requested.git_commit,revision.id,revision.revision_no
		FROM requested
		JOIN authoring.document_publication_binding AS binding
		  ON binding.workspace_id=$1 AND binding.document_id=$2
		 AND binding.target_path=$3 AND binding.git_commit=requested.git_commit
		JOIN core.article_revision AS revision
		  ON revision.workspace_id=binding.workspace_id
		 AND revision.document_id=binding.document_id
		 AND revision.id=binding.article_revision_id
	), proposal_mapping AS (
		SELECT requested.ordinality,requested.git_commit,
		       proposal_commit.proposal_id,proposal_commit.revision_id,
		       proposal_commit.approval_id,proposal.workflow_run_id,
		       proposal_commit.writeback_execution_id,proposal.proposal_type,
		       approval.decided_at
		FROM requested
		JOIN change_control.proposal_commit AS proposal_commit
		  ON proposal_commit.workspace_id=$1
		 AND proposal_commit.target_path=$3
		 AND proposal_commit.git_commit=requested.git_commit
		JOIN change_control.proposal AS proposal
		  ON proposal.id=proposal_commit.proposal_id
		 AND proposal.workspace_id=proposal_commit.workspace_id
		JOIN change_control.approval AS approval
		  ON approval.id=proposal_commit.approval_id
		 AND approval.proposal_id=proposal_commit.proposal_id
		 AND approval.revision_id=proposal_commit.revision_id
	)
	SELECT requested.git_commit,
	       COALESCE(revision_mapping.id::text,''),COALESCE(revision_mapping.revision_no,0),
	       COALESCE(proposal_mapping.proposal_id::text,''),COALESCE(proposal_mapping.revision_id::text,''),
	       COALESCE(proposal_mapping.approval_id::text,''),COALESCE(proposal_mapping.workflow_run_id::text,''),
	       COALESCE(proposal_mapping.writeback_execution_id::text,''),COALESCE(proposal_mapping.proposal_type,''),
	       proposal_mapping.decided_at
	FROM requested
	LEFT JOIN revision_mapping USING(ordinality,git_commit)
	LEFT JOIN proposal_mapping USING(ordinality,git_commit)
	ORDER BY requested.ordinality`

	// GORM Raw binds question-mark placeholders and lets the PostgreSQL
	// Dialector render them as $n. Keep these variants beside the pgx SQL so
	// their statement shape and projection order remain directly reviewable.
	gormGetDocumentSQL = `SELECT
		id::text,workspace_id::text,canonical_path,title,lifecycle_status,
		COALESCE(current_published_revision_id::text,''),version
		FROM core.document
		WHERE workspace_id=? AND id=?`

	gormCommitMappingSQL = `WITH requested(git_commit,ordinality) AS (
		SELECT git_commit,ordinality
		FROM unnest(?::text[]) WITH ORDINALITY AS input(git_commit,ordinality)
	), revision_mapping AS (
		SELECT requested.ordinality,requested.git_commit,revision.id,revision.revision_no
		FROM requested
		JOIN core.article_revision AS revision
		  ON revision.workspace_id=? AND revision.document_id=?
		 AND revision.git_commit=requested.git_commit
		UNION
		SELECT requested.ordinality,requested.git_commit,revision.id,revision.revision_no
		FROM requested
		JOIN authoring.document_publication_binding AS binding
		  ON binding.workspace_id=? AND binding.document_id=?
		 AND binding.target_path=? AND binding.git_commit=requested.git_commit
		JOIN core.article_revision AS revision
		  ON revision.workspace_id=binding.workspace_id
		 AND revision.document_id=binding.document_id
		 AND revision.id=binding.article_revision_id
	), proposal_mapping AS (
		SELECT requested.ordinality,requested.git_commit,
		       proposal_commit.proposal_id,proposal_commit.revision_id,
		       proposal_commit.approval_id,proposal.workflow_run_id,
		       proposal_commit.writeback_execution_id,proposal.proposal_type,
		       approval.decided_at
		FROM requested
		JOIN change_control.proposal_commit AS proposal_commit
		  ON proposal_commit.workspace_id=?
		 AND proposal_commit.target_path=?
		 AND proposal_commit.git_commit=requested.git_commit
		JOIN change_control.proposal AS proposal
		  ON proposal.id=proposal_commit.proposal_id
		 AND proposal.workspace_id=proposal_commit.workspace_id
		JOIN change_control.approval AS approval
		  ON approval.id=proposal_commit.approval_id
		 AND approval.proposal_id=proposal_commit.proposal_id
		 AND approval.revision_id=proposal_commit.revision_id
	)
	SELECT requested.git_commit,
	       COALESCE(revision_mapping.id::text,''),COALESCE(revision_mapping.revision_no,0),
	       COALESCE(proposal_mapping.proposal_id::text,''),COALESCE(proposal_mapping.revision_id::text,''),
	       COALESCE(proposal_mapping.approval_id::text,''),COALESCE(proposal_mapping.workflow_run_id::text,''),
	       COALESCE(proposal_mapping.writeback_execution_id::text,''),COALESCE(proposal_mapping.proposal_type,''),
	       proposal_mapping.decided_at
	FROM requested
	LEFT JOIN revision_mapping USING(ordinality,git_commit)
	LEFT JOIN proposal_mapping USING(ordinality,git_commit)
	ORDER BY requested.ordinality`
)
