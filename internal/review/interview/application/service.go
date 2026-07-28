package application

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation/strictjson"
	"github.com/CodeZen-Lizhi/zhixu/internal/review/interview/domain"
)

// Service 实现 Interview 的选题、评分、报告及 Learning Path 编排。
type Service struct {
	store           Store
	questionSource  QuestionSource
	contextLoader   ContextLoader
	candidateWriter MemoryCandidateWriter
	scorer          Scorer
	artifactBridge  ArtifactBridge
	ids             foundation.IDGenerator
	clock           foundation.Clock
}

// NewService 构造不带 HTTP、pgx 或 Artifact Repository 依赖的 Interview 服务。
func NewService(dependencies Dependencies) (*Service, error) {
	if dependencies.Store == nil || dependencies.QuestionSource == nil || dependencies.ContextLoader == nil || dependencies.CandidateWriter == nil || dependencies.Scorer == nil || dependencies.ArtifactBridge == nil || dependencies.IDGenerator == nil || dependencies.Clock == nil {
		return nil, domain.UnavailableError(domain.ErrorCodeDependencyUnavailable, "interview service dependency is missing")
	}
	if strings.TrimSpace(dependencies.Scorer.Version()) == "" {
		return nil, domain.UnavailableError(domain.ErrorCodeScorerUnavailable, "interview scorer version is missing")
	}
	return &Service{
		store:           dependencies.Store,
		questionSource:  dependencies.QuestionSource,
		contextLoader:   dependencies.ContextLoader,
		candidateWriter: dependencies.CandidateWriter,
		scorer:          dependencies.Scorer,
		artifactBridge:  dependencies.ArtifactBridge,
		ids:             dependencies.IDGenerator,
		clock:           dependencies.Clock,
	}, nil
}

// SuggestMemoryCandidate 从已完成面试的一条 Learning Path 步骤派生 GOAL Candidate。
// 该命令只创建待确认候选，不会自动确认或写入有效上下文。
func (s *Service) SuggestMemoryCandidate(ctx context.Context, command SuggestMemoryCandidateCommand) (MemoryCandidateResult, error) {
	if !validID(command.WorkspaceID) || !validID(command.SessionID) || !validID(command.PathID) || !validID(command.StepID) {
		return MemoryCandidateResult{}, domain.InvalidError(domain.ErrorCodePathInvalid, "interview memory candidate identity is invalid")
	}
	if err := domain.ValidateIdempotencyKey(command.IdempotencyKey); err != nil {
		return MemoryCandidateResult{}, err
	}
	path, err := s.store.GetPath(ctx, command.WorkspaceID, command.PathID)
	if err != nil {
		return MemoryCandidateResult{}, err
	}
	if domain.ValidateLearningPath(path.Path) != nil || path.Path.WorkspaceID != command.WorkspaceID || path.Path.ID != command.PathID || path.Path.SessionID != command.SessionID {
		return MemoryCandidateResult{}, domain.InvalidError(domain.ErrorCodePathInvalid, "learning path does not belong to the interview session")
	}
	var selected domain.PathStep
	found := false
	for index := range path.Steps {
		if path.Steps[index].ID == command.StepID {
			selected = path.Steps[index]
			found = true
			break
		}
	}
	if !found || selected.WorkspaceID != command.WorkspaceID || selected.PathID != command.PathID || domain.ValidatePathStep(selected) != nil {
		return MemoryCandidateResult{}, domain.InvalidError(domain.ErrorCodePathInvalid, "learning path step is unavailable for a memory candidate")
	}
	content, err := json.Marshal(struct {
		Goal      string `json:"goal"`
		Rationale string `json:"rationale"`
	}{Goal: selected.Title, Rationale: selected.Rationale})
	if err != nil {
		return MemoryCandidateResult{}, domain.InvalidError(domain.ErrorCodePersistenceInvalid, "interview memory candidate content cannot be encoded")
	}
	result, err := s.candidateWriter.CreateCandidate(ctx, MemoryCandidateRequest{
		WorkspaceID: path.Path.WorkspaceID,
		SessionID:   path.Path.SessionID,
		PathID:      path.Path.ID,
		StepID:      selected.ID,
		Content:     content,
		// 客户端命令键必须原样传给 Memory；同一键绑定不同步骤时要在任意副作用前冲突。
		// 同一步骤的新键由 Memory 的 INTERVIEW provenance receipt 层精确重放。
		IdempotencyKey: command.IdempotencyKey,
	})
	if err != nil {
		return MemoryCandidateResult{}, err
	}
	if !validID(result.MemoryID) {
		return MemoryCandidateResult{}, domain.InvalidError(domain.ErrorCodePersistenceInvalid, "interview memory candidate result is invalid")
	}
	return result, nil
}

// Start 冻结基于 CONFIRMED Claim 与 SUPPORTS Evidence 的初始题目。
func (s *Service) Start(ctx context.Context, command StartCommand) (StartResult, error) {
	if !validID(command.WorkspaceID) {
		return StartResult{}, domain.InvalidError(domain.ErrorCodeConfigInvalid, "interview workspace is invalid")
	}
	if err := domain.ValidateIdempotencyKey(command.IdempotencyKey); err != nil {
		return StartResult{}, err
	}
	config, err := domain.CanonicalConfig(command.Config)
	if err != nil {
		return StartResult{}, err
	}
	requestHash, err := requestHash(struct {
		Command   string        `json:"command"`
		Workspace foundation.ID `json:"workspace_id"`
		Config    domain.Config `json:"config"`
	}{"START", command.WorkspaceID, config})
	if err != nil {
		return StartResult{}, err
	}
	if replay, found, err := s.store.FindStartReplay(ctx, command.WorkspaceID, command.IdempotencyKey, requestHash); err != nil || found {
		return replay, err
	}

	materials, err := s.questionSource.Select(ctx, Selection{WorkspaceID: command.WorkspaceID, Scope: config.Scope, Limit: config.QuestionCount})
	if err != nil {
		return StartResult{}, err
	}
	materials, err = canonicalMaterials(materials)
	if err != nil {
		return StartResult{}, err
	}
	if len(materials) < config.QuestionCount {
		return StartResult{}, domain.InvalidError(domain.ErrorCodeEvidenceInvalid, "not enough confirmed claim evidence for interview questions")
	}

	now := s.clock.Now()
	sessionID, err := s.newID()
	if err != nil {
		return StartResult{}, err
	}
	session := domain.Session{
		ID: sessionID, WorkspaceID: command.WorkspaceID, Config: config, Status: domain.SessionStatusActive,
		Version: 1, StartedAt: now,
	}
	questions := make([]domain.Question, 0, config.QuestionCount)
	for index, material := range materials[:config.QuestionCount] {
		questionID, err := s.newID()
		if err != nil {
			return StartResult{}, err
		}
		fingerprint, err := domain.ComputeQuestionFingerprint(config, material, index+1, 0)
		if err != nil {
			return StartResult{}, err
		}
		prompt, err := initialQuestionPrompt(config, material)
		if err != nil {
			return StartResult{}, err
		}
		question := domain.Question{
			ID: questionID, WorkspaceID: command.WorkspaceID, SessionID: sessionID, QuestionNo: index + 1,
			ClaimID: material.ClaimID, TopicID: cloneID(material.TopicID),
			Prompt:       prompt,
			AnswerPoints: append([]string(nil), material.AnswerPoints...), Evidence: append([]domain.EvidenceRef(nil), material.Evidence...),
			Status: domain.QuestionStatusPending, Fingerprint: fingerprint, CreatedAt: now,
		}
		if err := domain.ValidateQuestion(question); err != nil {
			return StartResult{}, err
		}
		questions = append(questions, question)
	}
	result, err := s.store.Start(ctx, StartRecord{Session: session, Questions: questions, IdempotencyKey: command.IdempotencyKey, RequestHash: requestHash})
	if err != nil {
		return StartResult{}, err
	}
	return result, nil
}

// Get 恢复已持久化的 Interview Session、题目、Turn、报告与 Path 事实。
func (s *Service) Get(ctx context.Context, workspaceID, sessionID foundation.ID) (Snapshot, error) {
	if !validID(workspaceID) || !validID(sessionID) {
		return Snapshot{}, domain.InvalidError(domain.ErrorCodeSessionNotFound, "interview lookup identity is invalid")
	}
	return s.store.Get(ctx, workspaceID, sessionID)
}

// List 返回一个 Workspace 内按开始时间稳定倒序的有界 Interview Session 页面。
func (s *Service) List(ctx context.Context, query SessionListQuery) (SessionListPage, error) {
	if err := validateSessionListQuery(query); err != nil {
		return SessionListPage{}, err
	}
	page, err := s.store.List(ctx, query)
	if err != nil {
		return SessionListPage{}, err
	}
	if err := validateSessionListPage(query, page); err != nil {
		return SessionListPage{}, err
	}
	return page, nil
}

// SubmitTurn 只接受当前题的用户文本，并由服务端确定性评分后原子持久化。
func (s *Service) SubmitTurn(ctx context.Context, command SubmitTurnCommand) (SubmitTurnResult, error) {
	if !validID(command.WorkspaceID) || !validID(command.SessionID) || !validID(command.QuestionID) || len(command.UserAnswer) > 64*1024 || !utf8.ValidString(command.UserAnswer) || strings.ContainsRune(command.UserAnswer, '\x00') {
		return SubmitTurnResult{}, domain.InvalidError(domain.ErrorCodeTurnInvalid, "interview turn input is invalid")
	}
	if err := domain.ValidateIdempotencyKey(command.IdempotencyKey); err != nil {
		return SubmitTurnResult{}, err
	}
	requestHash, err := requestHash(struct {
		Command    string        `json:"command"`
		Workspace  foundation.ID `json:"workspace_id"`
		Session    foundation.ID `json:"session_id"`
		Question   foundation.ID `json:"question_id"`
		UserAnswer string        `json:"user_answer"`
	}{"SUBMIT", command.WorkspaceID, command.SessionID, command.QuestionID, command.UserAnswer})
	if err != nil {
		return SubmitTurnResult{}, err
	}
	if replay, found, err := s.store.FindSubmitReplay(ctx, command.WorkspaceID, command.IdempotencyKey, requestHash); err != nil || found {
		return replay, err
	}

	snapshot, err := s.store.Get(ctx, command.WorkspaceID, command.SessionID)
	if err != nil {
		return SubmitTurnResult{}, err
	}
	if snapshot.Session.Status != domain.SessionStatusActive {
		return SubmitTurnResult{}, domain.ConflictError(domain.ErrorCodeSessionClosed, "interview session is closed")
	}
	if !domain.SubmitAllowedAt(snapshot.Session, s.clock.Now()) {
		return SubmitTurnResult{}, domain.ConflictError(domain.ErrorCodeSessionExpired, "interview session deadline has passed")
	}
	current := domain.OrderedPendingQuestion(snapshot.Questions)
	if current == nil || current.ID != command.QuestionID {
		return SubmitTurnResult{}, domain.ConflictError(domain.ErrorCodeQuestionOrderConflict, "submitted question is not the current interview question")
	}
	personalContext, err := s.contextLoader.Load(ctx, ContextQuery{
		WorkspaceID: command.WorkspaceID,
		TaskScopeID: command.SessionID,
		Limit:       maxContextEntries,
	})
	if err != nil {
		return SubmitTurnResult{}, err
	}
	personalContext, err = canonicalPersonalContext(personalContext, maxContextEntries)
	if err != nil {
		return SubmitTurnResult{}, err
	}
	score, err := s.scorer.Score(ctx, ScoreInput{
		Question:        *current,
		UserAnswer:      command.UserAnswer,
		PersonalContext: personalContext,
	})
	if err != nil {
		return SubmitTurnResult{}, err
	}
	if err := domain.ValidateScore(score, current.Evidence); err != nil {
		return SubmitTurnResult{}, err
	}
	now := s.clock.Now()
	if !domain.SubmitAllowedAt(snapshot.Session, now) {
		return SubmitTurnResult{}, domain.ConflictError(domain.ErrorCodeSessionExpired, "interview session deadline passed while scoring")
	}
	turnID, err := s.newID()
	if err != nil {
		return SubmitTurnResult{}, err
	}
	var followUp *domain.Question
	if snapshot.Session.FollowUpCount < snapshot.Session.Config.MaxFollowUps && domain.NeedsFollowUp(score) {
		followUp, err = s.newFollowUp(snapshot.Session, *current, score, now)
		if err != nil {
			return SubmitTurnResult{}, err
		}
	}
	virtualQuestions := append([]domain.Question(nil), snapshot.Questions...)
	for index := range virtualQuestions {
		if virtualQuestions[index].ID == current.ID {
			virtualQuestions[index].Status = domain.QuestionStatusAnswered
			virtualQuestions[index].AnsweredAt = &now
			break
		}
	}
	if followUp != nil {
		virtualQuestions = append(virtualQuestions, *followUp)
	}
	next := domain.OrderedPendingQuestion(virtualQuestions)
	decision := domain.TurnDecision{FollowUpCreated: followUp != nil, NextQuestionID: idPointer(next)}
	if followUp != nil {
		followUpID := followUp.ID
		decision.FollowUpQuestionID = &followUpID
	}
	turn := domain.Turn{
		ID: turnID, WorkspaceID: command.WorkspaceID, SessionID: command.SessionID, QuestionID: current.ID,
		IdempotencyKey: command.IdempotencyKey, RequestHash: requestHash, UserAnswer: command.UserAnswer,
		Score: score, Decision: decision, ScorerVersion: s.scorer.Version(), CreatedAt: now,
	}
	if err := domain.ValidateTurn(turn, *current); err != nil {
		return SubmitTurnResult{}, err
	}
	return s.store.Submit(ctx, SubmitRecord{
		WorkspaceID: command.WorkspaceID, SessionID: command.SessionID, ExpectedVersion: snapshot.Session.Version,
		Question: *current, Turn: turn, FollowUp: followUp, IdempotencyKey: command.IdempotencyKey, RequestHash: requestHash,
	})
}

// Complete 固定一次报告、步骤进度事实及其 Artifact 关联；它不写 Review Answer 或 FSRS。
func (s *Service) Complete(ctx context.Context, command CompleteCommand) (CompleteResult, error) {
	if !validID(command.WorkspaceID) || !validID(command.SessionID) {
		return CompleteResult{}, domain.InvalidError(domain.ErrorCodeSessionNotFound, "interview completion identity is invalid")
	}
	if err := domain.ValidateIdempotencyKey(command.IdempotencyKey); err != nil {
		return CompleteResult{}, err
	}
	requestHash, err := requestHash(struct {
		Command   string        `json:"command"`
		Workspace foundation.ID `json:"workspace_id"`
		Session   foundation.ID `json:"session_id"`
		Manual    bool          `json:"manual_end"`
	}{"COMPLETE", command.WorkspaceID, command.SessionID, command.ManualEnd})
	if err != nil {
		return CompleteResult{}, err
	}
	if replay, found, err := s.store.FindCompleteReplay(ctx, command.WorkspaceID, command.IdempotencyKey, requestHash); err != nil || found {
		return replay, err
	}

	begin, err := s.store.BeginComplete(ctx, BeginCompleteRecord{
		WorkspaceID: command.WorkspaceID, SessionID: command.SessionID, ManualEnd: command.ManualEnd,
		IdempotencyKey: command.IdempotencyKey, RequestHash: requestHash,
	})
	if err != nil {
		return CompleteResult{}, err
	}
	if begin.Terminal != nil {
		return *begin.Terminal, nil
	}
	if begin.Reservation.Status != CompletionReservationPending || begin.Reservation.SnapshotVersion != begin.Snapshot.Session.Version {
		return CompleteResult{}, domain.InvalidError(domain.ErrorCodePersistenceInvalid, "interview completion reservation snapshot is invalid")
	}
	snapshot := begin.Snapshot
	now := begin.Reservation.CreatedAt
	reportID, err := derivedID(command.SessionID, "report")
	if err != nil {
		return CompleteResult{}, err
	}
	pathID, err := derivedID(command.SessionID, "learning-path")
	if err != nil {
		return CompleteResult{}, err
	}
	report, path, steps, err := s.buildCompletion(snapshot, reportID, pathID, now)
	if err != nil {
		return CompleteResult{}, err
	}
	digest, reportDraft, pathDraft, err := completionArtifactRequests(snapshot.Session.Version, report, path, steps)
	if err != nil {
		return CompleteResult{}, err
	}
	prepared, err := s.store.PrepareComplete(ctx, PrepareCompleteRecord{
		WorkspaceID: command.WorkspaceID, SessionID: command.SessionID, IdempotencyKey: command.IdempotencyKey,
		RequestHash: requestHash, ManualEnd: command.ManualEnd, SnapshotVersion: snapshot.Session.Version,
		ArtifactDigest: digest,
	})
	if err != nil {
		return CompleteResult{}, err
	}
	if prepared.Terminal != nil {
		return *prepared.Terminal, nil
	}
	if prepared.Reservation.Status != CompletionReservationPending || prepared.Reservation.ArtifactDigest != digest {
		return CompleteResult{}, domain.InvalidError(domain.ErrorCodePersistenceInvalid, "interview completion reservation digest is invalid")
	}
	if err := s.bindCompletionArtifacts(ctx, reportDraft, pathDraft, &report, &path); err != nil {
		return CompleteResult{}, err
	}
	if err := domain.ValidateReport(report); err != nil {
		return CompleteResult{}, err
	}
	if err := domain.ValidateLearningPath(path); err != nil {
		return CompleteResult{}, err
	}
	return s.store.Complete(ctx, CompleteRecord{
		WorkspaceID: command.WorkspaceID, SessionID: command.SessionID, ExpectedVersion: snapshot.Session.Version, ManualEnd: command.ManualEnd,
		ArtifactDigest: digest, Report: report, Path: path, Steps: steps,
		IdempotencyKey: command.IdempotencyKey, RequestHash: requestHash, At: now,
	})
}

// UpdatePathStep 持久化用户对学习步骤的确认、进行中、完成或跳过状态。
func (s *Service) UpdatePathStep(ctx context.Context, command UpdatePathStepCommand) (PathStepResult, error) {
	if !validID(command.WorkspaceID) || !validID(command.PathID) || !validID(command.StepID) || command.ExpectedVersion < 1 || !validStepStatus(command.Status) {
		return PathStepResult{}, domain.InvalidError(domain.ErrorCodePathInvalid, "learning path step command is invalid")
	}
	if err := domain.ValidateIdempotencyKey(command.IdempotencyKey); err != nil {
		return PathStepResult{}, err
	}
	requestHash, err := requestHash(struct {
		Command   string            `json:"command"`
		Workspace foundation.ID     `json:"workspace_id"`
		Path      foundation.ID     `json:"path_id"`
		Step      foundation.ID     `json:"step_id"`
		Version   int64             `json:"expected_version"`
		Status    domain.StepStatus `json:"status"`
	}{"PATH_STEP", command.WorkspaceID, command.PathID, command.StepID, command.ExpectedVersion, command.Status})
	if err != nil {
		return PathStepResult{}, err
	}
	if replay, found, err := s.store.FindPathStepReplay(ctx, command.WorkspaceID, command.IdempotencyKey, requestHash); err != nil || found {
		return replay, err
	}
	path, err := s.store.GetPath(ctx, command.WorkspaceID, command.PathID)
	if err != nil {
		return PathStepResult{}, err
	}
	if path.Path.Version != command.ExpectedVersion {
		return PathStepResult{}, domain.ConflictError(domain.ErrorCodePathInvalid, "learning path version changed")
	}
	var step *domain.PathStep
	for index := range path.Steps {
		if path.Steps[index].ID == command.StepID {
			value := path.Steps[index]
			step = &value
			break
		}
	}
	if step == nil || !validStepTransition(step.Status, command.Status) {
		return PathStepResult{}, domain.ConflictError(domain.ErrorCodePathInvalid, "learning path step transition is invalid")
	}
	return s.store.UpdatePathStep(ctx, UpdatePathStepRecord{
		WorkspaceID: command.WorkspaceID, PathID: command.PathID, StepID: command.StepID, ExpectedVersion: command.ExpectedVersion,
		Status: command.Status, IdempotencyKey: command.IdempotencyKey, RequestHash: requestHash, At: s.clock.Now(),
	})
}

// UpdatePathStatus 暂停、恢复或完成整个 Learning Path。
func (s *Service) UpdatePathStatus(ctx context.Context, command UpdatePathStatusCommand) (PathStatusResult, error) {
	if !validID(command.WorkspaceID) || !validID(command.PathID) || command.ExpectedVersion < 1 || !validPathStatus(command.Status) {
		return PathStatusResult{}, domain.InvalidError(domain.ErrorCodePathInvalid, "learning path status command is invalid")
	}
	if err := domain.ValidateIdempotencyKey(command.IdempotencyKey); err != nil {
		return PathStatusResult{}, err
	}
	requestHash, err := requestHash(struct {
		Command   string            `json:"command"`
		Workspace foundation.ID     `json:"workspace_id"`
		Path      foundation.ID     `json:"path_id"`
		Version   int64             `json:"expected_version"`
		Status    domain.PathStatus `json:"status"`
	}{"PATH_STATUS", command.WorkspaceID, command.PathID, command.ExpectedVersion, command.Status})
	if err != nil {
		return PathStatusResult{}, err
	}
	if replay, found, err := s.store.FindPathStatusReplay(ctx, command.WorkspaceID, command.IdempotencyKey, requestHash); err != nil || found {
		return replay, err
	}
	path, err := s.store.GetPath(ctx, command.WorkspaceID, command.PathID)
	if err != nil {
		return PathStatusResult{}, err
	}
	if path.Path.Version != command.ExpectedVersion || !validPathTransition(path.Path.Status, command.Status) ||
		(command.Status == domain.PathStatusCompleted && !domain.AllPathStepsTerminal(path.Steps)) {
		return PathStatusResult{}, domain.ConflictError(domain.ErrorCodePathInvalid, "learning path transition is invalid")
	}
	return s.store.UpdatePathStatus(ctx, UpdatePathStatusRecord{
		WorkspaceID: command.WorkspaceID, PathID: command.PathID, ExpectedVersion: command.ExpectedVersion,
		Status: command.Status, IdempotencyKey: command.IdempotencyKey, RequestHash: requestHash, At: s.clock.Now(),
	})
}

func (s *Service) newFollowUp(session domain.Session, parent domain.Question, score domain.Score, now time.Time) (*domain.Question, error) {
	id, err := s.newID()
	if err != nil {
		return nil, err
	}
	material := domain.Material{ClaimID: parent.ClaimID, ClaimStatus: "CONFIRMED", TopicID: cloneID(parent.TopicID), Statement: parent.AnswerPoints[0], AnswerPoints: append([]string(nil), parent.AnswerPoints...), Evidence: append([]domain.EvidenceRef(nil), parent.Evidence...)}
	fingerprint, err := domain.ComputeQuestionFingerprint(session.Config, material, parent.QuestionNo, parent.FollowUpNo+1)
	if err != nil {
		return nil, err
	}
	prompt, err := followUpQuestionPrompt(session.Config, score, parent.FollowUpNo+1)
	if err != nil {
		return nil, err
	}
	parentID := parent.ID
	question := &domain.Question{
		ID: id, WorkspaceID: session.WorkspaceID, SessionID: session.ID, QuestionNo: parent.QuestionNo, FollowUpNo: parent.FollowUpNo + 1,
		ParentQuestionID: &parentID, ClaimID: parent.ClaimID, TopicID: cloneID(parent.TopicID),
		Prompt:       prompt,
		AnswerPoints: append([]string(nil), parent.AnswerPoints...), Evidence: append([]domain.EvidenceRef(nil), parent.Evidence...),
		Status: domain.QuestionStatusPending, Fingerprint: fingerprint, CreatedAt: now,
	}
	if err := domain.ValidateQuestion(*question); err != nil {
		return nil, err
	}
	return question, nil
}

func (s *Service) buildCompletion(snapshot Snapshot, reportID, pathID foundation.ID, now time.Time) (domain.Report, domain.LearningPath, []domain.PathStep, error) {
	turns := make(map[foundation.ID]domain.Turn, len(snapshot.Turns))
	for _, turn := range snapshot.Turns {
		turns[turn.QuestionID] = turn
	}
	questions := append([]domain.Question(nil), snapshot.Questions...)
	sort.Slice(questions, func(i, j int) bool {
		if questions[i].QuestionNo == questions[j].QuestionNo {
			return questions[i].FollowUpNo < questions[j].FollowUpNo
		}
		return questions[i].QuestionNo < questions[j].QuestionNo
	})
	report := domain.Report{ID: reportID, WorkspaceID: snapshot.Session.WorkspaceID, SessionID: snapshot.Session.ID, SchemaVersion: domain.ReportSchemaVersion, CreatedAt: now}
	path := domain.LearningPath{ID: pathID, WorkspaceID: snapshot.Session.WorkspaceID, SessionID: snapshot.Session.ID, ReportID: reportID, Status: domain.PathStatusActive, Version: 1, CreatedAt: now, UpdatedAt: now}
	var totals struct{ correctness, coverage, boundaries, clarity float64 }
	steps := make([]domain.PathStep, 0, snapshot.Session.Config.QuestionCount)
	seenEvidence := make(map[string]domain.EvidenceRef)
	for _, question := range questions {
		for _, evidence := range question.Evidence {
			seenEvidence[evidence.EvidenceHash] = evidence
		}
	}
	for start := 0; start < len(questions); {
		end := start + 1
		for end < len(questions) && questions[end].QuestionNo == questions[start].QuestionNo {
			end++
		}
		terminal := questions[end-1]
		turn, answered := turns[terminal.ID]
		report.Summary.QuestionsTotal++
		if !answered {
			report.Summary.SkippedTotal++
			gap := domain.Finding{
				ClaimID: terminal.ClaimID, TopicID: cloneID(terminal.TopicID),
				Detail:   "This question chain ended without an answer. Review the claim and its linked supporting evidence before the next interview.",
				Evidence: append([]domain.EvidenceRef(nil), terminal.Evidence...),
			}
			report.Gaps = append(report.Gaps, gap)
			step, err := completionPathStep(snapshot.Session, pathID, terminal, len(steps)+1, gap.Detail, now)
			if err != nil {
				return domain.Report{}, domain.LearningPath{}, nil, err
			}
			steps = append(steps, step)
		} else {
			report.Summary.AnsweredTotal++
			totals.correctness += turn.Score.Correctness.Value
			totals.coverage += turn.Score.Coverage.Value
			totals.boundaries += turn.Score.Boundaries.Value
			totals.clarity += turn.Score.Clarity.Value
			average := (turn.Score.Correctness.Value + turn.Score.Coverage.Value + turn.Score.Boundaries.Value) / 3
			if average >= 0.75 {
				report.Strengths = append(report.Strengths, domain.Finding{ClaimID: terminal.ClaimID, TopicID: cloneID(terminal.TopicID), Detail: "The final answer covered the claim and its supporting evidence.", Evidence: append([]domain.EvidenceRef(nil), terminal.Evidence...)})
			} else {
				gap := domain.Finding{ClaimID: terminal.ClaimID, TopicID: cloneID(terminal.TopicID), Detail: "Review this claim, its conditions, and the linked supporting evidence before the next interview.", Evidence: append([]domain.EvidenceRef(nil), terminal.Evidence...)}
				report.Gaps = append(report.Gaps, gap)
				step, err := completionPathStep(snapshot.Session, pathID, terminal, len(steps)+1, gap.Detail, now)
				if err != nil {
					return domain.Report{}, domain.LearningPath{}, nil, err
				}
				steps = append(steps, step)
			}
			if turn.Score.Clarity.Value < 0.65 {
				report.Expression = append(report.Expression, domain.Finding{ClaimID: terminal.ClaimID, TopicID: cloneID(terminal.TopicID), Detail: "State the claim first, then its conditions and evidence in a clear order.", Evidence: append([]domain.EvidenceRef(nil), terminal.Evidence...)})
			}
		}
		start = end
	}
	if report.Summary.AnsweredTotal > 0 {
		count := float64(report.Summary.AnsweredTotal)
		report.Summary.Correctness = totals.correctness / count
		report.Summary.Coverage = totals.coverage / count
		report.Summary.Boundaries = totals.boundaries / count
		report.Summary.Clarity = totals.clarity / count
	}
	report.Evidence = make([]domain.EvidenceRef, 0, len(seenEvidence))
	for _, evidence := range seenEvidence {
		report.Evidence = append(report.Evidence, evidence)
	}
	sort.Slice(report.Evidence, func(i, j int) bool { return report.Evidence[i].EvidenceHash < report.Evidence[j].EvidenceHash })
	for _, step := range steps {
		if err := domain.ValidatePathStep(step); err != nil {
			return domain.Report{}, domain.LearningPath{}, nil, err
		}
	}
	return report, path, steps, nil
}

func completionPathStep(session domain.Session, pathID foundation.ID, question domain.Question, stepNo int, rationale string, now time.Time) (domain.PathStep, error) {
	stepID, err := derivedID(session.ID, fmt.Sprintf("learning-path-step:%d", question.QuestionNo))
	if err != nil {
		return domain.PathStep{}, err
	}
	evidence := question.Evidence[0]
	return domain.PathStep{
		ID: stepID, WorkspaceID: session.WorkspaceID, PathID: pathID, StepNo: stepNo,
		ClaimID: question.ClaimID, TopicID: cloneID(question.TopicID), SourceVersionID: evidence.SourceVersionID,
		SourceSpanID: evidence.SourceSpanID, EvidenceHash: evidence.EvidenceHash, Title: "Revisit evidence-backed claim",
		Rationale: rationale, Status: domain.StepStatusPending, Version: 1, CreatedAt: now, UpdatedAt: now,
	}, nil
}

// completionArtifactRequests 计算完整 digest，并构造两个尚未执行的 Artifact 请求。
func completionArtifactRequests(snapshotVersion int64, report domain.Report, path domain.LearningPath, steps []domain.PathStep) (string, ArtifactDraftRequest, ArtifactDraftRequest, error) {
	reportScope, err := artifactScope(report.WorkspaceID, report.SessionID, report.ID, path.ID, domain.ReportSchemaVersion)
	if err != nil {
		return "", ArtifactDraftRequest{}, ArtifactDraftRequest{}, err
	}
	pathScope, err := artifactScope(path.WorkspaceID, path.SessionID, report.ID, path.ID, domain.LearningPathSchemaVersion)
	if err != nil {
		return "", ArtifactDraftRequest{}, ArtifactDraftRequest{}, err
	}
	reportMarkdown := renderReportMarkdown(report)
	pathMarkdown := renderLearningPathMarkdown(steps)
	digest, err := completionArtifactDigest(report.SessionID, snapshotVersion, reportScope, pathScope, reportMarkdown, pathMarkdown, report.Evidence)
	if err != nil {
		return "", ArtifactDraftRequest{}, ArtifactDraftRequest{}, err
	}
	reportBaseKey, err := completionArtifactBaseKey(report.SessionID, ArtifactKindInterviewDocument, digest)
	if err != nil {
		return "", ArtifactDraftRequest{}, ArtifactDraftRequest{}, err
	}
	pathBaseKey, err := completionArtifactBaseKey(path.SessionID, ArtifactKindLearningPath, digest)
	if err != nil {
		return "", ArtifactDraftRequest{}, ArtifactDraftRequest{}, err
	}
	reportDraft := ArtifactDraftRequest{
		WorkspaceID: report.WorkspaceID,
		Kind:        ArtifactKindInterviewDocument,
		Title:       "Interview report",
		Scope:       reportScope,
		Section: ArtifactDraftSection{
			Key: "report", Title: "Interview report", Markdown: reportMarkdown,
			Evidence: append([]domain.EvidenceRef(nil), report.Evidence...),
		},
		IdempotencyBaseKey: reportBaseKey,
		VisibilityHold: ArtifactVisibilityHold{
			OwnerID:       report.SessionID,
			Role:          ArtifactVisibilityHoldRoleReport,
			AttemptDigest: digest,
		},
	}
	pathDraft := ArtifactDraftRequest{
		WorkspaceID: path.WorkspaceID,
		Kind:        ArtifactKindLearningPath,
		Title:       "Learning path",
		Scope:       pathScope,
		Section: ArtifactDraftSection{
			Key: "learning-path", Title: "Learning path", Markdown: pathMarkdown,
			Evidence: append([]domain.EvidenceRef(nil), report.Evidence...),
		},
		IdempotencyBaseKey: pathBaseKey,
		VisibilityHold: ArtifactVisibilityHold{
			OwnerID:       path.SessionID,
			Role:          ArtifactVisibilityHoldRolePath,
			AttemptDigest: digest,
		},
	}
	return digest, reportDraft, pathDraft, nil
}

// bindCompletionArtifacts 以 Prepare 阶段持久化的 digest stage keys 创建并绑定两个草稿。
func (s *Service) bindCompletionArtifacts(ctx context.Context, reportRequest, pathRequest ArtifactDraftRequest, report *domain.Report, path *domain.LearningPath) error {
	reportDraft, err := s.artifactBridge.CreateDraft(ctx, reportRequest)
	if err != nil {
		return err
	}
	if err := domain.ValidateArtifactBinding(reportDraft.Binding, ArtifactKindInterviewDocument); err != nil {
		return err
	}
	pathDraft, err := s.artifactBridge.CreateDraft(ctx, pathRequest)
	if err != nil {
		return err
	}
	if err := domain.ValidateArtifactBinding(pathDraft.Binding, ArtifactKindLearningPath); err != nil {
		return err
	}
	report.Artifact = reportDraft.Binding
	path.Artifact = pathDraft.Binding
	return nil
}

// renderReportMarkdown 仅把已派生的汇总与 Finding 渲染为 Artifact 正文。
func renderReportMarkdown(report domain.Report) string {
	var body strings.Builder
	body.WriteString("# Interview report\n\n## Summary\n\n")
	fmt.Fprintf(&body, "- Questions: %d\n- Answered: %d\n- Skipped: %d\n- Correctness: %.4f\n- Coverage: %.4f\n- Boundaries: %.4f\n- Clarity: %.4f\n",
		report.Summary.QuestionsTotal, report.Summary.AnsweredTotal, report.Summary.SkippedTotal,
		report.Summary.Correctness, report.Summary.Coverage, report.Summary.Boundaries, report.Summary.Clarity)
	for _, section := range []struct {
		title    string
		findings []domain.Finding
	}{
		{title: "Strengths", findings: report.Strengths},
		{title: "Gaps", findings: report.Gaps},
		{title: "Expression", findings: report.Expression},
	} {
		fmt.Fprintf(&body, "\n## %s\n\n", section.title)
		if len(section.findings) == 0 {
			body.WriteString("- None.\n")
			continue
		}
		for _, finding := range section.findings {
			fmt.Fprintf(&body, "- Claim `%s`: %s\n", finding.ClaimID, finding.Detail)
		}
	}
	return strings.TrimSpace(body.String())
}

// renderLearningPathMarkdown 冻结初始计划定义；运行状态只由 Path/Step 持久事实拥有。
func renderLearningPathMarkdown(steps []domain.PathStep) string {
	var body strings.Builder
	body.WriteString("# Learning path\n\n## Steps\n\n")
	if len(steps) == 0 {
		body.WriteString("No follow-up learning steps were generated.\n")
		return strings.TrimSpace(body.String())
	}
	for _, step := range steps {
		fmt.Fprintf(&body, "### %d. %s\n\n%s\n\n- Claim: `%s`\n- Source version: `%s`\n- Source span: `%s`\n\n",
			step.StepNo, step.Title, step.Rationale, step.ClaimID, step.SourceVersionID, step.SourceSpanID)
	}
	return strings.TrimSpace(body.String())
}

// completionArtifactDigest 绑定安全渲染内容、冻结证据与完成时的 Session 版本。
func completionArtifactDigest(
	sessionID foundation.ID,
	snapshotVersion int64,
	reportScope, pathScope json.RawMessage,
	reportMarkdown, pathMarkdown string,
	evidence []domain.EvidenceRef,
) (string, error) {
	if !validID(sessionID) || snapshotVersion < 1 || strings.TrimSpace(reportMarkdown) == "" || strings.TrimSpace(pathMarkdown) == "" || len(evidence) == 0 {
		return "", domain.InvalidError(domain.ErrorCodeReportInvalid, "interview artifact content binding is invalid")
	}
	canonicalEvidence := append([]domain.EvidenceRef(nil), evidence...)
	sort.Slice(canonicalEvidence, func(i, j int) bool {
		left := canonicalEvidence[i]
		right := canonicalEvidence[j]
		return string(left.IndexVersionID)+"\x00"+string(left.ChunkID)+"\x00"+string(left.SourceVersionID)+"\x00"+string(left.SourceSpanID) <
			string(right.IndexVersionID)+"\x00"+string(right.ChunkID)+"\x00"+string(right.SourceVersionID)+"\x00"+string(right.SourceSpanID)
	})
	for _, value := range canonicalEvidence {
		if err := domain.ValidateEvidenceRef(value); err != nil {
			return "", err
		}
	}
	encoded, err := json.Marshal(struct {
		SchemaVersion   string               `json:"schema_version"`
		SessionID       foundation.ID        `json:"session_id"`
		SnapshotVersion int64                `json:"snapshot_version"`
		ReportScope     json.RawMessage      `json:"report_scope"`
		PathScope       json.RawMessage      `json:"path_scope"`
		ReportMarkdown  string               `json:"report_markdown"`
		PathMarkdown    string               `json:"path_markdown"`
		Evidence        []domain.EvidenceRef `json:"evidence"`
	}{
		SchemaVersion: "interview-completion-artifacts/v1", SessionID: sessionID, SnapshotVersion: snapshotVersion,
		ReportScope: reportScope, PathScope: pathScope, ReportMarkdown: reportMarkdown, PathMarkdown: pathMarkdown, Evidence: canonicalEvidence,
	})
	if err != nil {
		return "", domain.InvalidError(domain.ErrorCodeReportInvalid, "interview artifact content binding cannot be encoded")
	}
	digest := sha256.Sum256(encoded)
	return hex.EncodeToString(digest[:]), nil
}

// completionArtifactBaseKey 为各 Artifact 阶段生成可追加短后缀的稳定幂等基键。
func completionArtifactBaseKey(sessionID foundation.ID, kind, digest string) (string, error) {
	key := fmt.Sprintf("iv1:%s:%s:%s", sessionID, kind, digest)
	if !validID(sessionID) || (kind != ArtifactKindInterviewDocument && kind != ArtifactKindLearningPath) || len(digest) != 64 || len(key)+2 > 128 {
		return "", domain.InvalidError(domain.ErrorCodeReportInvalid, "interview artifact idempotency binding is invalid")
	}
	if _, err := hex.DecodeString(digest); err != nil {
		return "", domain.InvalidError(domain.ErrorCodeReportInvalid, "interview artifact idempotency digest is invalid")
	}
	return key, nil
}

func (s *Service) newID() (foundation.ID, error) { return s.ids.New() }

// derivedID makes completion-side facts stable across a response-loss retry
// before their local receipt can be read. It is not used for user-controlled
// identifiers or Start/Submit commands.
func derivedID(sessionID foundation.ID, purpose string) (foundation.ID, error) {
	digest := sha256.Sum256([]byte(string(sessionID) + ":" + purpose))
	raw := digest[:16]
	raw[6] = (raw[6] & 0x0f) | 0x50
	raw[8] = (raw[8] & 0x3f) | 0x80
	return foundation.ParseID(fmt.Sprintf("%08x-%04x-%04x-%04x-%012x", raw[0:4], raw[4:6], raw[6:8], raw[8:10], raw[10:16]))
}

func canonicalMaterials(materials []domain.Material) ([]domain.Material, error) {
	values := append([]domain.Material(nil), materials...)
	sort.Slice(values, func(i, j int) bool { return values[i].ClaimID < values[j].ClaimID })
	seen := make(map[foundation.ID]struct{}, len(values))
	for index := range values {
		if err := domain.ValidateMaterial(values[index]); err != nil {
			return nil, err
		}
		if _, found := seen[values[index].ClaimID]; found {
			return nil, domain.InvalidError(domain.ErrorCodeEvidenceInvalid, "question source returned duplicate claim material")
		}
		seen[values[index].ClaimID] = struct{}{}
	}
	return values, nil
}

func canonicalPersonalContext(value PersonalContext, limit int) (PersonalContext, error) {
	if limit < 1 || len(value.Preferences)+len(value.Context) > limit {
		return PersonalContext{}, domain.InvalidError(domain.ErrorCodePersistenceInvalid, "interview context exceeded its bounded contract")
	}
	preferences, err := canonicalContextDocuments(value.Preferences)
	if err != nil {
		return PersonalContext{}, err
	}
	contextDocuments, err := canonicalContextDocuments(value.Context)
	if err != nil {
		return PersonalContext{}, err
	}
	return PersonalContext{Preferences: preferences, Context: contextDocuments}, nil
}

func canonicalContextDocuments(values []json.RawMessage) ([]json.RawMessage, error) {
	result := make([]json.RawMessage, 0, len(values))
	for _, value := range values {
		if len(value) == 0 || len(value) > maxContextEntryBytes {
			return nil, domain.InvalidError(domain.ErrorCodePersistenceInvalid, "interview context entry is invalid")
		}
		object, err := strictjson.DecodeObject[map[string]any](value, strictjson.Limits{
			MaxDocumentBytes: maxContextEntryBytes,
			MaxDepth:         8,
			MaxStringBytes:   4096,
			MaxArrayItems:    128,
			MaxObjectFields:  64,
		}, nil)
		if err != nil || len(object) == 0 {
			return nil, domain.InvalidError(domain.ErrorCodePersistenceInvalid, "interview context entry is invalid")
		}
		canonical, err := json.Marshal(object)
		if err != nil || !bytes.Equal(canonical, value) {
			return nil, domain.InvalidError(domain.ErrorCodePersistenceInvalid, "interview context entry is not canonical")
		}
		result = append(result, append(json.RawMessage(nil), value...))
	}
	return result, nil
}

func artifactScope(workspaceID, sessionID, reportID, pathID foundation.ID, schemaVersion string) (json.RawMessage, error) {
	encoded, err := json.Marshal(struct {
		SchemaVersion string        `json:"schema_version"`
		WorkspaceID   foundation.ID `json:"workspace_id"`
		SessionID     foundation.ID `json:"interview_session_id"`
		ReportID      foundation.ID `json:"report_id"`
		PathID        foundation.ID `json:"learning_path_id"`
	}{schemaVersion, workspaceID, sessionID, reportID, pathID})
	if err != nil {
		return nil, domain.InvalidError(domain.ErrorCodeReportInvalid, "artifact scope cannot be encoded")
	}
	return encoded, nil
}

func requestHash(value any) (string, error) {
	encoded, err := json.Marshal(value)
	if err != nil {
		return "", domain.InvalidError(domain.ErrorCodePersistenceInvalid, "interview request cannot be encoded")
	}
	digest := sha256.Sum256(encoded)
	return hex.EncodeToString(digest[:]), nil
}

func idPointer(question *domain.Question) *foundation.ID {
	if question == nil {
		return nil
	}
	id := question.ID
	return &id
}

func cloneID(value *foundation.ID) *foundation.ID {
	if value == nil {
		return nil
	}
	cloned := *value
	return &cloned
}

func validID(value foundation.ID) bool {
	_, err := foundation.ParseID(string(value))
	return err == nil
}

func validStepStatus(value domain.StepStatus) bool {
	return value == domain.StepStatusPending || value == domain.StepStatusInProgress || value == domain.StepStatusCompleted || value == domain.StepStatusSkipped
}

func validPathStatus(value domain.PathStatus) bool {
	return value == domain.PathStatusActive || value == domain.PathStatusPaused || value == domain.PathStatusCompleted
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

func validateSessionListQuery(query SessionListQuery) error {
	if !validID(query.WorkspaceID) || query.Limit < 1 || query.Limit > MaxSessionListLimit {
		return domain.InvalidError(domain.ErrorCodeConfigInvalid, "interview session list query is invalid")
	}
	if query.After != nil && (query.After.StartedAt.IsZero() || !validID(query.After.ID)) {
		return domain.InvalidError(domain.ErrorCodeConfigInvalid, "interview session list cursor is invalid")
	}
	return nil
}

func validateSessionListPage(query SessionListQuery, page SessionListPage) error {
	if len(page.Items) > query.Limit {
		return domain.InvalidError(domain.ErrorCodePersistenceInvalid, "interview session list exceeded its requested limit")
	}
	for index, session := range page.Items {
		if session.WorkspaceID != query.WorkspaceID || domain.ValidateSession(session) != nil {
			return domain.InvalidError(domain.ErrorCodePersistenceInvalid, "interview session list escaped its requested workspace")
		}
		if index > 0 {
			previous := page.Items[index-1]
			if session.StartedAt.After(previous.StartedAt) || session.StartedAt.Equal(previous.StartedAt) && session.ID >= previous.ID {
				return domain.InvalidError(domain.ErrorCodePersistenceInvalid, "interview session list order is unstable")
			}
		}
	}
	if page.Next != nil {
		if len(page.Items) == 0 {
			return domain.InvalidError(domain.ErrorCodePersistenceInvalid, "interview session list cursor has no boundary item")
		}
		last := page.Items[len(page.Items)-1]
		if !page.Next.StartedAt.Equal(last.StartedAt) || page.Next.ID != last.ID {
			return domain.InvalidError(domain.ErrorCodePersistenceInvalid, "interview session list cursor does not match its boundary item")
		}
	}
	return nil
}
