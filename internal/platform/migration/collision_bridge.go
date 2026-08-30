package migration

import (
	"context"
	"database/sql"
	"fmt"
	"strconv"
	"strings"
)

const (
	legacyCollisionFirstVersion  int64 = 78
	legacyCollisionSecondVersion int64 = 79
	legacyCollisionGapFirst      int64 = 80
	legacyCollisionGapSecond     int64 = 81
	legacyCollisionGapThird      int64 = 82
	adoptedEinoFirstVersion      int64 = 83
	adoptedEinoSecondVersion     int64 = 84
)

type fingerprintState string

const (
	fingerprintAbsent  fingerprintState = "absent"
	fingerprintPresent fingerprintState = "present"
	fingerprintPartial fingerprintState = "partial"
)

type schemaFingerprint struct {
	name   string
	checks []fingerprintCheck
}

type fingerprintCheck struct {
	name  string
	query string
}

var legacyEino78Fingerprint = schemaFingerprint{
	name: "legacy Eino 78",
	checks: []fingerprintCheck{
		{
			name: "model-call phase constraint",
			query: `SELECT EXISTS (
SELECT 1 FROM pg_constraint
WHERE conrelid=to_regclass('agent.model_call')
  AND conname='agent_model_call_phase_check'
  AND pg_get_constraintdef(oid) LIKE '%''AGENT''%'
  AND pg_get_constraintdef(oid) LIKE '%''ANSWER''%'
)`,
		},
		{
			name: "model-call phase order constraint",
			query: `SELECT EXISTS (
SELECT 1 FROM pg_constraint
WHERE conrelid=to_regclass('agent.model_call')
  AND conname='agent_model_call_phase_order'
  AND pg_get_constraintdef(oid) LIKE '%''AGENT''%'
  AND pg_get_constraintdef(oid) LIKE '%''ANSWER''%'
)`,
		},
		{
			name: "model-call phase index",
			query: `SELECT EXISTS (
SELECT 1 FROM pg_index
WHERE indexrelid=to_regclass('agent.uq_agent_model_call_generation_phase')
  AND pg_get_indexdef(indexrelid) LIKE '%''ANSWER''%'
)`,
		},
		{
			name: "model-call phase guard",
			query: `SELECT COALESCE(
pg_get_functiondef(to_regprocedure('agent.guard_model_call_phase_sequence()'))
    LIKE '%agent model call first phase is invalid%', false)`,
		},
	},
}

var legacyEino79Fingerprint = schemaFingerprint{
	name: "legacy Eino 79",
	checks: []fingerprintCheck{
		{
			name: "answer workspace workflow constraint",
			query: `SELECT EXISTS (
SELECT 1 FROM pg_constraint
WHERE conrelid=to_regclass('agent.answer')
  AND conname='uq_agent_answer_id_workspace_workflow'
)`,
		},
		{
			name: "node attempt identity constraint",
			query: `SELECT EXISTS (
SELECT 1 FROM pg_constraint
WHERE conrelid=to_regclass('workflow.node_attempt')
  AND conname='uq_workflow_node_attempt_id_node_no'
)`,
		},
		{
			name:  "answer draft session",
			query: `SELECT to_regclass('agent.answer_draft_session') IS NOT NULL`,
		},
		{
			name:  "answer draft chunks",
			query: `SELECT to_regclass('agent.answer_draft_chunk') IS NOT NULL`,
		},
		{
			name: "answer draft transition guard",
			query: `SELECT COALESCE(
pg_get_functiondef(to_regprocedure('agent.enforce_answer_draft_session_transition()'))
    LIKE '%answer draft session identity is immutable%', false)`,
		},
		{
			name: "answer draft transition trigger",
			query: `SELECT EXISTS (
SELECT 1 FROM pg_trigger
WHERE tgrelid=to_regclass('agent.answer_draft_session')
  AND tgname='answer_draft_session_transition_guard'
  AND NOT tgisinternal
)`,
		},
		{
			name:  "answer draft visible session index",
			query: `SELECT to_regclass('agent.uq_answer_draft_session_current') IS NOT NULL`,
		},
		{
			name:  "answer draft cleanup index",
			query: `SELECT to_regclass('agent.idx_answer_draft_session_cleanup') IS NOT NULL`,
		},
	},
}

var canonicalModelSettings78Fingerprint = schemaFingerprint{
	name: "canonical model settings 78",
	checks: []fingerprintCheck{
		{
			name: "chat API style column",
			query: `SELECT EXISTS (
SELECT 1 FROM information_schema.columns
WHERE table_schema='ops' AND table_name='model_settings_revisions'
  AND column_name='chat_api_style'
)`,
		},
		{
			name: "chat API style constraint",
			query: `SELECT EXISTS (
SELECT 1 FROM pg_constraint
WHERE conrelid=to_regclass('ops.model_settings_revisions')
  AND conname='ops_model_settings_chat_api_style'
)`,
		},
	},
}

var canonicalModelSettings79Fingerprint = schemaFingerprint{
	name: "canonical model settings 79",
	checks: []fingerprintCheck{
		{
			name:  "hot activation participant table",
			query: `SELECT to_regclass('ops.model_settings_rollout_participant') IS NOT NULL`,
		},
		{
			name: "hot activation participant guard",
			query: `SELECT COALESCE(
pg_get_functiondef(to_regprocedure('ops.enforce_model_settings_participant()'))
    LIKE '%model settings participant history cannot be deleted%', false)`,
		},
		{
			name: "hot activation participant transition trigger",
			query: `SELECT EXISTS (
SELECT 1 FROM pg_trigger
WHERE tgrelid=to_regclass('ops.model_settings_rollout_participant')
  AND tgname='model_settings_participant_guard'
  AND NOT tgisinternal
)`,
		},
		{
			name: "hot activation participant truncate trigger",
			query: `SELECT EXISTS (
SELECT 1 FROM pg_trigger
WHERE tgrelid=to_regclass('ops.model_settings_rollout_participant')
  AND tgname='model_settings_participant_reject_truncate'
  AND NOT tgisinternal
)`,
		},
		{
			name: "hot activation state guard",
			query: `SELECT COALESCE(
pg_get_functiondef(to_regprocedure('ops.enforce_model_settings_state()'))
    LIKE '%both model settings activation roles must be fresh and armed%', false)`,
		},
		{
			name: "hot activation runtime guard",
			query: `SELECT COALESCE(
pg_get_functiondef(to_regprocedure('ops.enforce_model_settings_runtime()'))
    LIKE '%model settings runtime activation binding is invalid%', false)`,
		},
	},
}

func collisionFingerprintError(legacyFirst, legacySecond, canonicalFirst, canonicalSecond fingerprintState) error {
	return fmt.Errorf(
		"MIGRATION_VERSION_COLLISION_FINGERPRINT_AMBIGUOUS: legacy78=%s legacy79=%s canonical78=%s canonical79=%s",
		legacyFirst, legacySecond, canonicalFirst, canonicalSecond,
	)
}

func inspectSchemaFingerprint(ctx context.Context, db *sql.DB, fingerprint schemaFingerprint) (fingerprintState, error) {
	matched := 0
	for _, check := range fingerprint.checks {
		var present bool
		if err := db.QueryRowContext(ctx, check.query).Scan(&present); err != nil {
			return fingerprintPartial, fmt.Errorf("inspect %s fingerprint %s: %w", fingerprint.name, check.name, err)
		}
		if present {
			matched++
		}
	}
	switch {
	case matched == 0:
		return fingerprintAbsent, nil
	case matched == len(fingerprint.checks):
		return fingerprintPresent, nil
	default:
		return fingerprintPartial, nil
	}
}

func parseMigrationFileVersion(name string) (int64, bool) {
	if !strings.HasSuffix(name, ".sql") {
		return 0, false
	}
	versionText, _, ok := strings.Cut(name, "_")
	if !ok {
		return 0, false
	}
	version, err := strconv.ParseInt(versionText, 10, 64)
	return version, err == nil && version > 0
}
