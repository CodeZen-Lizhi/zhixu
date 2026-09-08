package postgres

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	interviewapp "github.com/CodeZen-Lizhi/zhixu/internal/review/interview/application"
	"github.com/CodeZen-Lizhi/zhixu/internal/review/interview/domain"
)

const completionReservationSelect = `SELECT
	workspace_id::text,session_id::text,idempotency_key,request_hash,manual_end,snapshot_version,artifact_digest,
	report_id::text,report_artifact_id::text,report_artifact_revision_id::text,report_artifact_version,
	path_id::text,path_artifact_id::text,path_artifact_revision_id::text,path_artifact_version,
	status,created_at,prepared_at,completed_at,abandoned_at,updated_at
	FROM learning.interview_completion_reservation`

// validateCompletionReservation 验证从 PostgreSQL 恢复的 reservation 完整形状。
func validateCompletionReservation(reservation interviewapp.CompletionReservation) error {
	if !validID(reservation.WorkspaceID) || !validID(reservation.SessionID) ||
		domain.ValidateIdempotencyKey(reservation.IdempotencyKey) != nil || !validHash(reservation.RequestHash) ||
		reservation.SnapshotVersion < 1 || reservation.CreatedAt.IsZero() || reservation.UpdatedAt.IsZero() {
		return domain.InvalidError(domain.ErrorCodePersistenceInvalid, "interview completion reservation identity is invalid")
	}
	if reservation.ArtifactDigest != "" && !validHash(reservation.ArtifactDigest) {
		return domain.InvalidError(domain.ErrorCodePersistenceInvalid, "interview completion reservation digest is invalid")
	}
	hasBindings := reservation.Bindings.ReportID != "" || reservation.Bindings.PathID != ""
	switch reservation.Status {
	case interviewapp.CompletionReservationPending:
		if reservation.CompletedAt != nil || reservation.AbandonedAt != nil || hasBindings {
			return domain.InvalidError(domain.ErrorCodePersistenceInvalid, "pending interview completion reservation is malformed")
		}
	case interviewapp.CompletionReservationAbandoned:
		if reservation.CompletedAt != nil || reservation.AbandonedAt == nil || hasBindings {
			return domain.InvalidError(domain.ErrorCodePersistenceInvalid, "abandoned interview completion reservation is malformed")
		}
	case interviewapp.CompletionReservationCompleted:
		if reservation.ArtifactDigest == "" || reservation.CompletedAt == nil || reservation.AbandonedAt != nil ||
			!validID(reservation.Bindings.ReportID) || !validID(reservation.Bindings.PathID) ||
			domain.ValidateArtifactBinding(reservation.Bindings.ReportArtifact, "INTERVIEW_DOC") != nil ||
			domain.ValidateArtifactBinding(reservation.Bindings.PathArtifact, "LEARNING_PATH") != nil {
			return domain.InvalidError(domain.ErrorCodePersistenceInvalid, "completed interview completion reservation is malformed")
		}
	default:
		return domain.InvalidError(domain.ErrorCodePersistenceInvalid, "interview completion reservation status is invalid")
	}
	if (reservation.ArtifactDigest == "") != (reservation.PreparedAt == nil) {
		return domain.InvalidError(domain.ErrorCodePersistenceInvalid, "interview completion reservation prepare time is invalid")
	}
	return nil
}

// utcTimePointer 复制并规范化可空 PostgreSQL 时间。
func utcTimePointer(value *time.Time) *time.Time {
	if value == nil {
		return nil
	}
	result := value.UTC()
	return &result
}

func decodeJSON(raw []byte, destination any) error {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(destination); err != nil {
		return err
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("trailing JSON value")
		}
		return err
	}
	return nil
}

func encodeJSON(value any) ([]byte, error) {
	encoded, err := json.Marshal(value)
	if err != nil {
		return nil, persistenceInvalid("encode interview persistence JSON", err)
	}
	return encoded, nil
}

// canonicalJSON makes an object hash stable across PostgreSQL jsonb key-order
// normalization while retaining json.Number instead of coercing numbers to float64.
func canonicalJSON(raw []byte) ([]byte, error) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return nil, err
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		if err == nil {
			return nil, errors.New("trailing JSON value")
		}
		return nil, err
	}
	return json.Marshal(value)
}

func validHash(value string) bool {
	if len(value) != 64 {
		return false
	}
	for _, value := range value {
		if !(value >= '0' && value <= '9') && !(value >= 'a' && value <= 'f') {
			return false
		}
	}
	return true
}

type receipt struct {
	RequestHash string
	CommandType string
	SessionID   foundation.ID
	Response    []byte
}
