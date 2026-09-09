package postgres

import (
	"crypto/sha256"
	"encoding/hex"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	interviewapp "github.com/CodeZen-Lizhi/zhixu/internal/review/interview/application"
	"github.com/CodeZen-Lizhi/zhixu/internal/review/interview/domain"
)

func validateSelection(selection interviewapp.Selection) error {
	if !validID(selection.WorkspaceID) || selection.Limit < 1 || selection.Limit > 20 || (len(selection.Scope.ClaimIDs) == 0 && len(selection.Scope.TopicIDs) == 0) {
		return domain.InvalidError(domain.ErrorCodeConfigInvalid, "interview selection is invalid")
	}
	seen := make(map[foundation.ID]struct{}, len(selection.Scope.ClaimIDs)+len(selection.Scope.TopicIDs))
	for _, values := range [][]foundation.ID{selection.Scope.ClaimIDs, selection.Scope.TopicIDs} {
		for _, value := range values {
			if !validID(value) {
				return domain.InvalidError(domain.ErrorCodeConfigInvalid, "interview selection contains invalid identifiers")
			}
			if _, found := seen[value]; found {
				return domain.InvalidError(domain.ErrorCodeConfigInvalid, "interview selection contains duplicate identifiers")
			}
			seen[value] = struct{}{}
		}
	}
	return nil
}

func validateSessionListQuery(query interviewapp.SessionListQuery) error {
	if !validID(query.WorkspaceID) || query.Limit < 1 || query.Limit > interviewapp.MaxSessionListLimit {
		return domain.InvalidError(domain.ErrorCodeConfigInvalid, "interview session list query is invalid")
	}
	if query.After != nil && (query.After.StartedAt.IsZero() || !validID(query.After.ID)) {
		return domain.InvalidError(domain.ErrorCodeConfigInvalid, "interview session list cursor is invalid")
	}
	return nil
}

func stringsFromIDs(values []foundation.ID) []string {
	result := make([]string, len(values))
	for index := range values {
		result[index] = string(values[index])
	}
	return result
}

func validID(value foundation.ID) bool {
	_, err := foundation.ParseID(string(value))
	return err == nil
}

func min(left, right int) int {
	if left < right {
		return left
	}
	return right
}

func advisoryKey(workspaceID foundation.ID, key string) string {
	return string(workspaceID) + ":" + key
}

func validateStartRecord(record interviewapp.StartRecord) error {
	if err := domain.ValidateSession(record.Session); err != nil {
		return err
	}
	if record.Session.Status != domain.SessionStatusActive || len(record.Questions) != record.Session.Config.QuestionCount || !validHash(record.RequestHash) {
		return domain.InvalidError(domain.ErrorCodeConfigInvalid, "interview start record is invalid")
	}
	for index, question := range record.Questions {
		if question.SessionID != record.Session.ID || question.WorkspaceID != record.Session.WorkspaceID || question.QuestionNo != index+1 || question.FollowUpNo != 0 {
			return domain.InvalidError(domain.ErrorCodeQuestionInvalid, "interview initial question binding is invalid")
		}
		if err := domain.ValidateQuestion(question); err != nil {
			return err
		}
	}
	return domain.ValidateIdempotencyKey(record.IdempotencyKey)
}

func validateSubmitRecord(record interviewapp.SubmitRecord) error {
	if !validID(record.WorkspaceID) || !validID(record.SessionID) || record.ExpectedVersion < 1 || !validHash(record.RequestHash) || record.Question.WorkspaceID != record.WorkspaceID || record.Question.SessionID != record.SessionID || record.Turn.WorkspaceID != record.WorkspaceID || record.Turn.SessionID != record.SessionID {
		return domain.InvalidError(domain.ErrorCodeTurnInvalid, "interview submit record is invalid")
	}
	if record.FollowUp != nil && (record.FollowUp.WorkspaceID != record.WorkspaceID || record.FollowUp.SessionID != record.SessionID) {
		return domain.InvalidError(domain.ErrorCodeQuestionInvalid, "interview follow-up binding is invalid")
	}
	return domain.ValidateIdempotencyKey(record.IdempotencyKey)
}

// validateBeginCompleteRecord 验证 reservation Begin 的完整请求绑定。
func validateBeginCompleteRecord(record interviewapp.BeginCompleteRecord) error {
	if !validID(record.WorkspaceID) || !validID(record.SessionID) || !validHash(record.RequestHash) {
		return domain.InvalidError(domain.ErrorCodeReportInvalid, "interview completion begin record is invalid")
	}
	return domain.ValidateIdempotencyKey(record.IdempotencyKey)
}

// validatePrepareCompleteRecord 验证 digest Prepare 的冻结请求绑定。
func validatePrepareCompleteRecord(record interviewapp.PrepareCompleteRecord) error {
	if !validID(record.WorkspaceID) || !validID(record.SessionID) || !validHash(record.RequestHash) ||
		record.SnapshotVersion < 1 || !validHash(record.ArtifactDigest) {
		return domain.InvalidError(domain.ErrorCodeReportInvalid, "interview completion prepare record is invalid")
	}
	return domain.ValidateIdempotencyKey(record.IdempotencyKey)
}

// matchCompletionReservation 核对跨阶段不可变 reservation binding。
func matchCompletionReservation(reservation interviewapp.CompletionReservation, key, requestHash string, manualEnd bool, snapshotVersion int64) error {
	if reservation.IdempotencyKey != key || reservation.RequestHash != requestHash || reservation.ManualEnd != manualEnd {
		return domain.ConflictError(domain.ErrorCodeIdempotencyConflict, "interview completion reservation binding changed")
	}
	if reservation.SnapshotVersion != snapshotVersion {
		return domain.ConflictError(domain.ErrorCodeQuestionOrderConflict, "interview completion snapshot version changed")
	}
	return nil
}

func validateCompleteRecord(record interviewapp.CompleteRecord) error {
	if !validID(record.WorkspaceID) || !validID(record.SessionID) || record.ExpectedVersion < 1 || !validHash(record.RequestHash) || !validHash(record.ArtifactDigest) || record.At.IsZero() || record.Report.WorkspaceID != record.WorkspaceID || record.Report.SessionID != record.SessionID || record.Path.WorkspaceID != record.WorkspaceID || record.Path.SessionID != record.SessionID || record.Path.ReportID != record.Report.ID || record.Report.Artifact.ArtifactID == record.Path.Artifact.ArtifactID {
		return domain.InvalidError(domain.ErrorCodeReportInvalid, "interview completion record is invalid")
	}
	if err := domain.ValidateReport(record.Report); err != nil {
		return err
	}
	if err := domain.ValidateLearningPath(record.Path); err != nil {
		return err
	}
	for _, step := range record.Steps {
		if step.WorkspaceID != record.WorkspaceID || step.PathID != record.Path.ID {
			return domain.InvalidError(domain.ErrorCodePathInvalid, "learning path step does not bind its path")
		}
		if err := domain.ValidatePathStep(step); err != nil {
			return err
		}
	}
	return domain.ValidateIdempotencyKey(record.IdempotencyKey)
}

func validatePathStepRecord(record interviewapp.UpdatePathStepRecord) error {
	if !validID(record.WorkspaceID) || !validID(record.PathID) || !validID(record.StepID) || record.ExpectedVersion < 1 || !validHash(record.RequestHash) || record.At.IsZero() {
		return domain.InvalidError(domain.ErrorCodePathInvalid, "learning path step record is invalid")
	}
	return domain.ValidateIdempotencyKey(record.IdempotencyKey)
}

func validatePathStatusRecord(record interviewapp.UpdatePathStatusRecord) error {
	if !validID(record.WorkspaceID) || !validID(record.PathID) || record.ExpectedVersion < 1 || !validHash(record.RequestHash) || record.At.IsZero() {
		return domain.InvalidError(domain.ErrorCodePathInvalid, "learning path status record is invalid")
	}
	return domain.ValidateIdempotencyKey(record.IdempotencyKey)
}

func decodeStartResult(raw []byte) (interviewapp.StartResult, error) {
	var result interviewapp.StartResult
	if err := decodeJSON(raw, &result); err != nil {
		return interviewapp.StartResult{}, persistenceInvalid("decode interview start receipt", err)
	}
	if err := domain.ValidateSession(result.Session); err != nil {
		return interviewapp.StartResult{}, persistenceInvalid("validate interview start receipt session", err)
	}
	if len(result.Questions) != result.Session.Config.QuestionCount {
		return interviewapp.StartResult{}, persistenceInvalid("interview start receipt question count is invalid", nil)
	}
	for _, question := range result.Questions {
		if err := domain.ValidateQuestion(question); err != nil {
			return interviewapp.StartResult{}, persistenceInvalid("validate interview start receipt question", err)
		}
	}
	return result, nil
}

func decodeSubmitResult(raw []byte) (interviewapp.SubmitTurnResult, error) {
	var result interviewapp.SubmitTurnResult
	if err := decodeJSON(raw, &result); err != nil {
		return interviewapp.SubmitTurnResult{}, persistenceInvalid("decode interview submit receipt", err)
	}
	if result.FollowUp != nil {
		if err := domain.ValidateQuestion(*result.FollowUp); err != nil {
			return interviewapp.SubmitTurnResult{}, persistenceInvalid("validate interview submit receipt follow-up", err)
		}
	}
	if result.NextQuestion != nil {
		if err := domain.ValidateQuestion(*result.NextQuestion); err != nil {
			return interviewapp.SubmitTurnResult{}, persistenceInvalid("validate interview submit receipt next question", err)
		}
	}
	return result, nil
}

func decodeCompleteResult(raw []byte) (interviewapp.CompleteResult, error) {
	var result interviewapp.CompleteResult
	if err := decodeJSON(raw, &result); err != nil {
		return interviewapp.CompleteResult{}, persistenceInvalid("decode interview complete receipt", err)
	}
	if err := domain.ValidateSession(result.Session); err != nil {
		return interviewapp.CompleteResult{}, persistenceInvalid("validate interview complete receipt session", err)
	}
	if err := domain.ValidateReport(result.Report); err != nil {
		return interviewapp.CompleteResult{}, persistenceInvalid("validate interview complete receipt report", err)
	}
	if err := domain.ValidateLearningPath(result.Path); err != nil {
		return interviewapp.CompleteResult{}, persistenceInvalid("validate interview complete receipt path", err)
	}
	for _, step := range result.Steps {
		if err := domain.ValidatePathStep(step); err != nil {
			return interviewapp.CompleteResult{}, persistenceInvalid("validate interview complete receipt step", err)
		}
	}
	return result, nil
}

func decodePathStepResult(raw []byte) (interviewapp.PathStepResult, error) {
	var result interviewapp.PathStepResult
	if err := decodeJSON(raw, &result); err != nil {
		return interviewapp.PathStepResult{}, persistenceInvalid("decode interview path step receipt", err)
	}
	if err := domain.ValidateLearningPath(result.Path); err != nil {
		return interviewapp.PathStepResult{}, persistenceInvalid("validate interview path step receipt path", err)
	}
	if err := domain.ValidatePathStep(result.Step); err != nil {
		return interviewapp.PathStepResult{}, persistenceInvalid("validate interview path step receipt step", err)
	}
	return result, nil
}

func decodePathStatusResult(raw []byte) (interviewapp.PathStatusResult, error) {
	var result interviewapp.PathStatusResult
	if err := decodeJSON(raw, &result); err != nil {
		return interviewapp.PathStatusResult{}, persistenceInvalid("decode interview path status receipt", err)
	}
	if err := domain.ValidateLearningPath(result.Path); err != nil {
		return interviewapp.PathStatusResult{}, persistenceInvalid("validate interview path status receipt path", err)
	}
	return result, nil
}

func nullableID(value *foundation.ID) any {
	if value == nil {
		return nil
	}
	return string(*value)
}

func nullableTime(value *time.Time) any {
	if value == nil {
		return nil
	}
	return value.UTC()
}

func hashBytes(value []byte) string {
	digest := sha256.Sum256(value)
	return hex.EncodeToString(digest[:])
}

func cloneQuestion(question *domain.Question) *domain.Question {
	if question == nil {
		return nil
	}
	copy := *question
	copy.AnswerPoints = append([]string(nil), question.AnswerPoints...)
	copy.Evidence = append([]domain.EvidenceRef(nil), question.Evidence...)
	copy.NoteSource = domain.CloneNoteSource(question.NoteSource)
	copy.FollowUpPlan = domain.CloneNoteFollowUps(question.FollowUpPlan)
	return &copy
}

func idFromQuestion(question *domain.Question) *foundation.ID {
	if question == nil {
		return nil
	}
	id := question.ID
	return &id
}

func sameIDPointer(left, right *foundation.ID) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return *left == *right
}

func validStepTransition(from, to domain.StepStatus) bool {
	if from == to {
		return true
	}
	switch from {
	case domain.StepStatusPending:
		return to == domain.StepStatusInProgress || to == domain.StepStatusCompleted || to == domain.StepStatusSkipped
	case domain.StepStatusInProgress:
		return to == domain.StepStatusCompleted || to == domain.StepStatusSkipped
	default:
		return false
	}
}

func validPathTransition(from, to domain.PathStatus) bool {
	if from == to {
		return true
	}
	return (from == domain.PathStatusActive && (to == domain.PathStatusPaused || to == domain.PathStatusCompleted)) ||
		(from == domain.PathStatusPaused && to == domain.PathStatusActive)
}
