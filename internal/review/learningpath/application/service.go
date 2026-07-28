package application

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	"github.com/CodeZen-Lizhi/zhixu/internal/review/learningpath/domain"
)

// Service 只接受服务端冻结的 Review Snapshot，绝不接收客户端 gap 或 Evidence。
type Service struct {
	store     Store
	artifacts ArtifactBridge
	clock     foundation.Clock
}

// ReservationMaintainer 只拥有 Review Path 超时预留的应用策略。
type ReservationMaintainer struct {
	store Store
	clock foundation.Clock
}

// NewService 构造共享 Learning Path 应用服务。
func NewService(dependencies Dependencies) (*Service, error) {
	if dependencies.Store == nil || dependencies.ArtifactBridge == nil || dependencies.Clock == nil {
		return nil, domain.UnavailableError(domain.ErrorCodeDependencyUnavailable, "learning path dependency is missing")
	}
	return &Service{store: dependencies.Store, artifacts: dependencies.ArtifactBridge, clock: dependencies.Clock}, nil
}

// NewReservationMaintainer 构造 Worker 使用的 Review Path 超时维护服务。
func NewReservationMaintainer(store Store, clock foundation.Clock) (*ReservationMaintainer, error) {
	if store == nil || clock == nil {
		return nil, domain.UnavailableError(domain.ErrorCodeDependencyUnavailable, "learning path maintenance dependency is missing")
	}
	return &ReservationMaintainer{store: store, clock: clock}, nil
}

// CreateForReview 从不可变 Review Answer 评分及当前可验证 Evidence 创建或重放路径。
func (service *Service) CreateForReview(ctx context.Context, command CreateReviewCommand) (Result, error) {
	if !validID(command.WorkspaceID) || !validID(command.ReviewAnswerID) || !validKey(command.IdempotencyKey) {
		return Result{}, domain.InvalidError(domain.ErrorCodePathInvalid, "review learning path command is invalid")
	}
	requestHash, err := hash(struct {
		Command   string        `json:"command"`
		Workspace foundation.ID `json:"workspace_id"`
		Answer    foundation.ID `json:"review_answer_id"`
	}{CommandTypeCreateReviewPath, command.WorkspaceID, command.ReviewAnswerID})
	if err != nil {
		return Result{}, err
	}
	if replay, found, err := service.store.FindCreateReplay(ctx, command.WorkspaceID, command.IdempotencyKey, requestHash); err != nil || found {
		return replay, err
	}
	reservation, snapshot, terminal, err := service.store.BeginReviewCreate(ctx, command.WorkspaceID, command.ReviewAnswerID, command.IdempotencyKey, requestHash)
	if err != nil {
		return Result{}, err
	}
	if terminal != nil {
		return *terminal, nil
	}
	if snapshot.ReviewAnswerID != command.ReviewAnswerID || ValidateReviewSnapshot(snapshot) != nil {
		return Result{}, domain.InvalidError(domain.ErrorCodePersistenceInvalid, "review path source snapshot is invalid")
	}
	if !snapshot.Gap.Actionable() {
		return Result{}, domain.ConflictError(domain.ErrorCodeNotActionable, "review score has no actionable learning gap")
	}
	pathID, err := derivedID(command.ReviewAnswerID, "review-learning-path")
	if err != nil {
		return Result{}, err
	}
	path, steps, request, err := service.buildReviewPath(command.WorkspaceID, command.ReviewAnswerID, pathID, snapshot.Gap, reservation.CreatedAt)
	if err != nil {
		return Result{}, err
	}
	digest, err := resolveReviewArtifactAttemptDigest(request, snapshot.Gap, reservation)
	if err != nil {
		return Result{}, err
	}
	baseKey, err := artifactBaseKey(command.ReviewAnswerID, digest)
	if err != nil {
		return Result{}, err
	}
	request.ArtifactDigest = digest
	request.IdempotencyBaseKey = baseKey
	reservation, terminal, err = service.store.PrepareReviewCreate(ctx, reservation, digest)
	if err != nil {
		return Result{}, err
	}
	if terminal != nil {
		return *terminal, nil
	}
	if reservation.Status != ReservationPending || reservation.ArtifactDigest != digest {
		return Result{}, domain.ConflictError(domain.ErrorCodeArtifactConflict, "learning path reservation digest drifted")
	}
	binding, err := service.artifacts.CreateDraft(ctx, request)
	if err != nil {
		return Result{}, err
	}
	path.Artifact = binding
	if err := domain.ValidatePath(path); err != nil {
		return Result{}, err
	}
	return service.store.CompleteReviewCreate(ctx, reservation, path, steps)
}

// GetForReviewAnswer 返回一个 Answer 所属的共享 Path。
func (service *Service) GetForReviewAnswer(ctx context.Context, workspaceID, answerID foundation.ID) (Result, error) {
	if !validID(workspaceID) || !validID(answerID) {
		return Result{}, domain.InvalidError(domain.ErrorCodePathInvalid, "review answer lookup is invalid")
	}
	return service.store.GetByReviewAnswer(ctx, workspaceID, answerID)
}

// UpdateStatus 通过版本 CAS 推进 Path 状态。
func (service *Service) UpdateStatus(ctx context.Context, command UpdatePathStatusCommand) (Result, error) {
	if !validID(command.WorkspaceID) || !validID(command.PathID) || command.ExpectedVersion < 1 || !validKey(command.IdempotencyKey) {
		return Result{}, domain.InvalidError(domain.ErrorCodePathInvalid, "learning path status command is invalid")
	}
	requestHash, err := hash(command)
	if err != nil {
		return Result{}, err
	}
	if replay, found, err := service.store.FindPathStatusReplay(ctx, command.WorkspaceID, command.IdempotencyKey, requestHash, command.ExpectedVersion); err != nil || found {
		return replay, err
	}
	now, err := service.now()
	if err != nil {
		return Result{}, err
	}
	return service.store.UpdatePathStatus(ctx, UpdatePathStatusRecord{WorkspaceID: command.WorkspaceID, PathID: command.PathID, ExpectedVersion: command.ExpectedVersion, Status: command.Status, IdempotencyKey: command.IdempotencyKey, RequestHash: requestHash, At: now})
}

// UpdateStep 通过版本 CAS 推进一条 Path Step。
func (service *Service) UpdateStep(ctx context.Context, command UpdateStepCommand) (StepResult, error) {
	if !validID(command.WorkspaceID) || !validID(command.PathID) || !validID(command.StepID) || command.ExpectedVersion < 1 ||
		!domain.IsStepTransitionTarget(command.Status) || !validKey(command.IdempotencyKey) {
		return StepResult{}, domain.InvalidError(domain.ErrorCodePathInvalid, "learning path step command is invalid")
	}
	requestHash, err := hash(command)
	if err != nil {
		return StepResult{}, err
	}
	if replay, found, err := service.store.FindStepReplay(ctx, command.WorkspaceID, command.IdempotencyKey, requestHash, command.ExpectedVersion); err != nil || found {
		return replay, err
	}
	now, err := service.now()
	if err != nil {
		return StepResult{}, err
	}
	return service.store.UpdateStep(ctx, UpdateStepRecord{WorkspaceID: command.WorkspaceID, PathID: command.PathID, StepID: command.StepID, ExpectedVersion: command.ExpectedVersion, Status: command.Status, IdempotencyKey: command.IdempotencyKey, RequestHash: requestHash, At: now})
}

// MaintainExpiredReservations 有界地将超时预留交给持久化层转换为 ABANDONED/ORPHANED。
func (service *Service) MaintainExpiredReservations(ctx context.Context) (int, error) {
	if service == nil {
		return 0, domain.UnavailableError(domain.ErrorCodeDependencyUnavailable, "learning path maintenance service is unavailable")
	}
	return maintainExpiredReservations(ctx, service.store, service.clock)
}

// MaintainExpiredReservations 有界地执行 Review Path reservation/hold 维护。
func (maintainer *ReservationMaintainer) MaintainExpiredReservations(ctx context.Context) (int, error) {
	if maintainer == nil {
		return 0, domain.UnavailableError(domain.ErrorCodeDependencyUnavailable, "learning path maintenance service is unavailable")
	}
	return maintainExpiredReservations(ctx, maintainer.store, maintainer.clock)
}

func maintainExpiredReservations(ctx context.Context, store Store, clock foundation.Clock) (int, error) {
	if store == nil || clock == nil {
		return 0, domain.UnavailableError(domain.ErrorCodeDependencyUnavailable, "learning path maintenance dependency is missing")
	}
	now := clock.Now().UTC()
	if now.IsZero() {
		return 0, domain.InvalidError(domain.ErrorCodePersistenceInvalid, "learning path clock returned zero")
	}
	return store.MaintainReservations(ctx, now.Add(-24*time.Hour), MaxMaintenanceBatch)
}

func (service *Service) buildReviewPath(workspaceID, answerID, pathID foundation.ID, gap domain.Gap, now time.Time) (domain.Path, []domain.Step, DraftRequest, error) {
	canonicalGap, err := domain.CanonicalGap(gap)
	if err != nil {
		return domain.Path{}, nil, DraftRequest{}, err
	}
	title := "复习评分缺口"
	rationale := reviewRationale(canonicalGap)
	selected := canonicalGap.Citations
	if len(selected) > domain.MaxSteps {
		selected = selected[:domain.MaxSteps]
	}
	steps := make([]domain.Step, 0, len(selected))
	for index, citation := range selected {
		stepID, err := derivedID(answerID, fmt.Sprintf("review-learning-step:%02d:%s", index+1, citation.EvidenceHash))
		if err != nil {
			return domain.Path{}, nil, DraftRequest{}, err
		}
		step := domain.Step{
			ID: stepID, WorkspaceID: workspaceID, PathID: pathID, StepNo: index + 1,
			ClaimID: citation.ClaimID, TopicID: citation.TopicID, SourceVersionID: citation.SourceVersionID,
			SourceSpanID: citation.SourceSpanID, EvidenceHash: citation.EvidenceHash,
			Title: fmt.Sprintf("复习知识缺口 %d", index+1), Rationale: rationale,
			Status: domain.StepStatusPending, Version: 1, CreatedAt: now, UpdatedAt: now,
		}
		if err := domain.ValidateStep(step); err != nil {
			return domain.Path{}, nil, DraftRequest{}, err
		}
		steps = append(steps, step)
	}
	path := domain.Path{ID: pathID, WorkspaceID: workspaceID, OriginType: domain.OriginReview, ReviewAnswerID: &answerID, SourcePolicyVersion: domain.ReviewGapSchemaVersion, Status: domain.StatusActive, Version: 1, CreatedAt: now, UpdatedAt: now}
	scope, err := json.Marshal(struct {
		Schema    string        `json:"schema_version"`
		Workspace foundation.ID `json:"workspace_id"`
		Answer    foundation.ID `json:"review_answer_id"`
		Path      foundation.ID `json:"learning_path_id"`
	}{domain.SchemaVersion, workspaceID, answerID, pathID})
	if err != nil {
		return domain.Path{}, nil, DraftRequest{}, domain.InvalidError(domain.ErrorCodePathInvalid, "learning path artifact scope cannot be encoded")
	}
	markdown := "# " + title + "\n\n" + rationale
	return path, steps, DraftRequest{WorkspaceID: workspaceID, ReviewAnswerID: answerID, Title: title, Scope: scope, Markdown: markdown, Citations: append([]domain.Citation(nil), canonicalGap.Citations...)}, nil
}

func reviewRationale(gap domain.Gap) string {
	parts := append(append([]string{}, gap.Errors...), gap.Omissions...)
	if len(parts) > 0 {
		return strings.Join(parts, "；")
	}
	return fmt.Sprintf("评分维度均值不足 0.75（正确性 %.2f，覆盖度 %.2f，边界 %.2f）", gap.Correctness, gap.Coverage, gap.Boundaries)
}

func artifactBaseKey(answerID foundation.ID, digest string) (string, error) {
	key := "lp1:review:" + string(answerID) + ":LEARNING_PATH:" + digest
	if !validID(answerID) || len(digest) != 64 || len(key)+2 > 128 {
		return "", domain.InvalidError(domain.ErrorCodePathInvalid, "learning path artifact key is invalid")
	}
	if _, err := hex.DecodeString(digest); err != nil {
		return "", domain.InvalidError(domain.ErrorCodePathInvalid, "learning path artifact digest is invalid")
	}
	return key, nil
}

func reviewArtifactAttemptDigest(request DraftRequest, sourceSnapshotDigest string, attemptNo int64) (string, error) {
	if !validID(request.WorkspaceID) || !validID(request.ReviewAnswerID) || request.WorkspaceID == request.ReviewAnswerID ||
		request.ArtifactDigest != "" || request.IdempotencyBaseKey != "" || strings.TrimSpace(request.Title) != request.Title ||
		request.Title == "" || len(request.Scope) == 0 || !json.Valid(request.Scope) || strings.TrimSpace(request.Markdown) != request.Markdown ||
		request.Markdown == "" || len(request.Citations) == 0 || len(sourceSnapshotDigest) != 64 ||
		sourceSnapshotDigest != strings.ToLower(sourceSnapshotDigest) || attemptNo < 1 {
		return "", domain.InvalidError(domain.ErrorCodePathInvalid, "learning path artifact attempt binding is invalid")
	}
	if _, err := hex.DecodeString(sourceSnapshotDigest); err != nil {
		return "", domain.InvalidError(domain.ErrorCodePathInvalid, "learning path source snapshot digest is invalid")
	}
	for _, citation := range request.Citations {
		if err := domain.ValidateCitation(citation); err != nil {
			return "", err
		}
	}
	return hash(struct {
		Schema                    string            `json:"schema_version"`
		ArtifactKind              string            `json:"artifact_kind"`
		SectionKey                string            `json:"section_key"`
		Creator                   string            `json:"creator"`
		CoverageStatus            string            `json:"coverage_status"`
		PromptVersion             string            `json:"prompt_version"`
		ModelVersion              string            `json:"model_version"`
		WorkflowDefinitionVersion string            `json:"workflow_definition_version"`
		ArtifactSchemaVersion     string            `json:"artifact_schema_version"`
		Workspace                 foundation.ID     `json:"workspace_id"`
		Answer                    foundation.ID     `json:"review_answer_id"`
		SourceSnapshotDigest      string            `json:"source_snapshot_digest"`
		AttemptNo                 int64             `json:"attempt_no"`
		Title                     string            `json:"title"`
		Scope                     string            `json:"scope"`
		Markdown                  string            `json:"markdown"`
		Citations                 []domain.Citation `json:"citations"`
	}{
		Schema: "review-learning-path-artifact-attempt/v2", ArtifactKind: ArtifactKind,
		SectionKey: ReviewArtifactSectionKey, Creator: ReviewArtifactCreator, CoverageStatus: ReviewArtifactCoverageStatus,
		PromptVersion: ReviewArtifactPromptVersion, ModelVersion: ReviewArtifactModelVersion,
		WorkflowDefinitionVersion: ReviewArtifactWorkflowVersion, ArtifactSchemaVersion: ReviewArtifactSchemaVersion,
		Workspace: request.WorkspaceID, Answer: request.ReviewAnswerID, SourceSnapshotDigest: sourceSnapshotDigest,
		AttemptNo: attemptNo, Title: request.Title, Scope: string(request.Scope), Markdown: request.Markdown,
		Citations: append([]domain.Citation(nil), request.Citations...),
	})
}

// resolveReviewArtifactAttemptDigest 只为已 Prepare 的 v1 在途预留保留精确恢复；新预留和重开尝试一律使用 v2。
func resolveReviewArtifactAttemptDigest(request DraftRequest, gap domain.Gap, reservation Reservation) (string, error) {
	digest, err := reviewArtifactAttemptDigest(request, reservation.SourceSnapshotDigest, reservation.AttemptNo)
	if err != nil {
		return "", err
	}
	if reservation.ArtifactDigest == "" || reservation.ArtifactDigest == digest {
		return digest, nil
	}
	gapDigest, err := gap.Digest(request.ReviewAnswerID)
	if err != nil {
		return "", err
	}
	legacyDigest, err := hash(struct {
		Schema    string        `json:"schema_version"`
		Answer    foundation.ID `json:"review_answer_id"`
		GapDigest string        `json:"gap_digest"`
		AttemptNo int64         `json:"attempt_no"`
	}{"review-learning-path-artifact-attempt/v1", request.ReviewAnswerID, gapDigest, reservation.AttemptNo})
	if err != nil {
		return "", err
	}
	if reservation.ArtifactDigest != legacyDigest {
		return "", domain.ConflictError(domain.ErrorCodeArtifactConflict, "learning path artifact digest conflicts")
	}
	return legacyDigest, nil
}

func (service *Service) now() (time.Time, error) {
	if service == nil || service.clock == nil {
		return time.Time{}, domain.UnavailableError(domain.ErrorCodeDependencyUnavailable, "learning path clock is unavailable")
	}
	now := service.clock.Now().UTC()
	if now.IsZero() {
		return time.Time{}, domain.InvalidError(domain.ErrorCodePersistenceInvalid, "learning path clock returned zero")
	}
	return now, nil
}
func hash(value any) (string, error) {
	encoded, err := json.Marshal(value)
	if err != nil {
		return "", domain.InvalidError(domain.ErrorCodePersistenceInvalid, "learning path request cannot be encoded")
	}
	sum := sha256.Sum256(encoded)
	return hex.EncodeToString(sum[:]), nil
}
func derivedID(source foundation.ID, purpose string) (foundation.ID, error) {
	sum := sha256.Sum256([]byte(string(source) + ":" + purpose))
	raw := sum[:16]
	raw[6] = raw[6]&0x0f | 0x50
	raw[8] = raw[8]&0x3f | 0x80
	return foundation.ParseID(fmt.Sprintf("%08x-%04x-%04x-%04x-%012x", raw[0:4], raw[4:6], raw[6:8], raw[8:10], raw[10:16]))
}
func validID(value foundation.ID) bool {
	parsed, err := foundation.ParseID(string(value))
	return err == nil && parsed == value
}
func validKey(value string) bool {
	return strings.TrimSpace(value) == value && value != "" && len(value) <= 128 && !strings.ContainsAny(value, "\r\n")
}
