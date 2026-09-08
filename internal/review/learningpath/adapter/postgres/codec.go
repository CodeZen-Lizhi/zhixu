package postgres

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"strings"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	pathapp "github.com/CodeZen-Lizhi/zhixu/internal/review/learningpath/application"
	pathdomain "github.com/CodeZen-Lizhi/zhixu/internal/review/learningpath/domain"
)

func decodeReviewSnapshot(encoded []byte, wantDigest string) (pathapp.ReviewSnapshot, error) {
	var snapshot pathapp.ReviewSnapshot
	if err := decodeStrict(encoded, &snapshot); err != nil {
		return pathapp.ReviewSnapshot{}, pathdomain.InvalidError(pathdomain.ErrorCodePersistenceInvalid, "persisted review path snapshot is invalid")
	}
	canonical, _, digest, err := pathapp.CanonicalReviewSnapshot(snapshot)
	if err != nil || digest != wantDigest {
		return pathapp.ReviewSnapshot{}, pathdomain.InvalidError(pathdomain.ErrorCodePersistenceInvalid, "persisted review path snapshot digest is invalid")
	}
	return canonical, nil
}

func validateReservation(value pathapp.Reservation, workspaceID, answerID foundation.ID) error {
	if value.WorkspaceID != workspaceID || value.ReviewAnswerID != answerID || value.SourceSnapshot.ReviewAnswerID != answerID ||
		!validID(value.WorkspaceID) || !validID(value.ReviewAnswerID) || !validKey(value.IdempotencyKey) ||
		!validHash(value.RequestHash) || !validHash(value.SourceSnapshotDigest) || value.AttemptNo < 1 ||
		(value.ArtifactDigest != "" && !validHash(value.ArtifactDigest)) || value.CreatedAt.IsZero() || value.UpdatedAt.Before(value.CreatedAt) {
		return pathdomain.InvalidError(pathdomain.ErrorCodePersistenceInvalid, "persisted learning path reservation is invalid")
	}
	switch value.Status {
	case pathapp.ReservationPending, pathapp.ReservationCompleted, pathapp.ReservationAbandoned:
		return nil
	default:
		return pathdomain.InvalidError(pathdomain.ErrorCodePersistenceInvalid, "persisted learning path reservation status is invalid")
	}
}

func validateResult(result pathapp.Result, workspaceID, pathID foundation.ID, pathVersion int64) error {
	if result.Replayed || pathdomain.ValidatePath(result.Path) != nil || result.Path.WorkspaceID != workspaceID ||
		result.Path.ID != pathID || result.Path.Version != pathVersion || result.Path.OriginType != pathdomain.OriginReview ||
		result.Path.ReviewAnswerID == nil || len(result.Steps) == 0 || len(result.Steps) > pathdomain.MaxSteps {
		return pathdomain.InvalidError(pathdomain.ErrorCodePersistenceInvalid, "learning path aggregate binding is invalid")
	}
	seenIDs := make(map[foundation.ID]struct{}, len(result.Steps))
	for index, step := range result.Steps {
		if pathdomain.ValidateStep(step) != nil || step.WorkspaceID != workspaceID || step.PathID != pathID || step.StepNo != index+1 {
			return pathdomain.InvalidError(pathdomain.ErrorCodePersistenceInvalid, "learning path step binding is invalid")
		}
		if _, duplicate := seenIDs[step.ID]; duplicate {
			return pathdomain.InvalidError(pathdomain.ErrorCodePersistenceInvalid, "learning path step identity is duplicated")
		}
		seenIDs[step.ID] = struct{}{}
	}
	if result.Path.Status == pathdomain.StatusCompleted && !pathdomain.AllStepsTerminal(result.Steps) {
		return pathdomain.InvalidError(pathdomain.ErrorCodePersistenceInvalid, "completed learning path has non-terminal steps")
	}
	return nil
}

func decodeStrict(encoded []byte, target any) error {
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return errors.New("unexpected trailing JSON value")
	}
	return nil
}

func validID(value foundation.ID) bool {
	parsed, err := foundation.ParseID(string(value))
	return err == nil && parsed == value
}

func validKey(value string) bool {
	return strings.TrimSpace(value) == value && value != "" && len(value) <= 128 && !strings.ContainsAny(value, "\r\n")
}

func validHash(value string) bool {
	if len(value) != 64 || value != strings.ToLower(value) {
		return false
	}
	for _, character := range value {
		if (character < '0' || character > '9') && (character < 'a' || character > 'f') {
			return false
		}
	}
	return true
}
