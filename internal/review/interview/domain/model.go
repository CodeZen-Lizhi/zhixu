package domain

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"math"
	"sort"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
)

const (
	// SchemaVersion 是 Interview 持久化配置的版本。
	SchemaVersion = "interview/v1"
	// EvidenceSchemaVersion 是服务端持久化的完整面试证据绑定版本。
	EvidenceSchemaVersion = "interview-evidence/v2"
	// ScoreSchemaVersion 是确定性评分输出的版本。
	ScoreSchemaVersion = "interview-score/v1"
	// ReportSchemaVersion 是面试报告的版本。
	ReportSchemaVersion = "interview-report/v1"
	// LearningPathSchemaVersion 是学习路径事实的版本。
	LearningPathSchemaVersion = "interview-learning-path/v1"

	maxRoleBytes        = 256
	maxQuestionBytes    = 8192
	maxAnswerPointBytes = 4096
	maxAnswerPoints     = 64
	// MaxEvidenceItems 是单道题、评分或报告条目允许冻结的最大证据数。
	MaxEvidenceItems       = 64
	maxUserAnswerBytes     = 64 * 1024
	maxFeedbackItems       = 32
	maxFeedbackItemBytes   = 2048
	maxIdempotencyKeyBytes = 128
	maxPathTitleBytes      = 512
	maxPathRationaleBytes  = 4096
	maxQuestionCount       = 20
	maxFollowUpsPerSession = 20
	maxDurationMinutes     = 240
)

// Difficulty 表示服务端冻结的面试难度。
type Difficulty string

const (
	// DifficultyFoundation 面向基础知识回顾。
	DifficultyFoundation Difficulty = "FOUNDATION"
	// DifficultyIntermediate 面向常规工程场景。
	DifficultyIntermediate Difficulty = "INTERMEDIATE"
	// DifficultyAdvanced 面向复杂边界与取舍。
	DifficultyAdvanced Difficulty = "ADVANCED"
)

// SessionStatus 与 Review Session shell 的受控状态对齐。
type SessionStatus string

const (
	// SessionStatusActive 表示可以继续答题。
	SessionStatusActive SessionStatus = "ACTIVE"
	// SessionStatusCompleted 表示报告已经固定。
	SessionStatusCompleted SessionStatus = "COMPLETED"
	// SessionStatusCancelled 表示用户手动结束且未生成正常完成结果。
	SessionStatusCancelled SessionStatus = "CANCELLED"
)

// QuestionStatus 表示单题在 Interview 中的可答状态。
type QuestionStatus string

const (
	// QuestionStatusPending 表示题目尚未作答。
	QuestionStatusPending QuestionStatus = "PENDING"
	// QuestionStatusAnswered 表示已记录一份确定性评分的作答。
	QuestionStatusAnswered QuestionStatus = "ANSWERED"
	// QuestionStatusSkipped 表示用户手动结束时未答的题目。
	QuestionStatusSkipped QuestionStatus = "SKIPPED"
)

// PathStatus 表示 Learning Path 的用户生命周期。
type PathStatus string

const (
	// PathStatusActive 表示路径可继续执行。
	PathStatusActive PathStatus = "ACTIVE"
	// PathStatusPaused 表示路径暂停。
	PathStatusPaused PathStatus = "PAUSED"
	// PathStatusCompleted 表示所有可执行步骤已完成或跳过。
	PathStatusCompleted PathStatus = "COMPLETED"
)

// StepStatus 表示 Learning Path 单个步骤的用户进度。
type StepStatus string

const (
	// StepStatusPending 表示步骤尚未开始。
	StepStatusPending StepStatus = "PENDING"
	// StepStatusInProgress 表示步骤正在学习。
	StepStatusInProgress StepStatus = "IN_PROGRESS"
	// StepStatusCompleted 表示步骤已完成。
	StepStatusCompleted StepStatus = "COMPLETED"
	// StepStatusSkipped 表示用户显式跳过步骤。
	StepStatusSkipped StepStatus = "SKIPPED"
)

// Scope 只允许按正式 Claim 或 Topic 限定选题范围。
type Scope struct {
	ClaimIDs []foundation.ID `json:"claim_ids,omitempty"`
	TopicIDs []foundation.ID `json:"topic_ids,omitempty"`
}

// Config 是创建 Interview Session 时冻结的设置。
type Config struct {
	SchemaVersion   string     `json:"schema_version"`
	Role            string     `json:"role"`
	Scope           Scope      `json:"scope"`
	Difficulty      Difficulty `json:"difficulty"`
	DurationMinutes int        `json:"duration_minutes"`
	QuestionCount   int        `json:"question_count"`
	MaxFollowUps    int        `json:"max_follow_ups"`
}

// EvidenceRef 是一个只读的 Claim SUPPORTS Evidence 绑定。
type EvidenceRef struct {
	SchemaVersion   string        `json:"schema_version"`
	ClaimID         foundation.ID `json:"claim_id"`
	IndexVersionID  foundation.ID `json:"index_version_id"`
	ChunkID         foundation.ID `json:"chunk_id"`
	SourceVersionID foundation.ID `json:"source_version_id"`
	SourceSpanID    foundation.ID `json:"source_span_id"`
	EvidenceHash    string        `json:"evidence_hash"`
	SupportType     string        `json:"support_type"`
}

// Material 是 QuestionSource 返回的已确认 Claim 与支持性证据。
type Material struct {
	ClaimID      foundation.ID  `json:"claim_id"`
	ClaimStatus  string         `json:"claim_status"`
	TopicID      *foundation.ID `json:"topic_id,omitempty"`
	Statement    string         `json:"statement"`
	AnswerPoints []string       `json:"answer_points"`
	Evidence     []EvidenceRef  `json:"evidence"`
}

// Session 是 Interview 的专属聚合快照，外壳持久化在 review_session。
type Session struct {
	ID            foundation.ID `json:"id"`
	WorkspaceID   foundation.ID `json:"workspace_id"`
	Config        Config        `json:"config"`
	Status        SessionStatus `json:"status"`
	Version       int64         `json:"version"`
	FollowUpCount int           `json:"follow_up_count"`
	StartedAt     time.Time     `json:"started_at"`
	EndedAt       *time.Time    `json:"ended_at,omitempty"`
}

// Question 是被持久化的、不可重新抽取的面试题快照。
type Question struct {
	ID               foundation.ID  `json:"id"`
	WorkspaceID      foundation.ID  `json:"workspace_id"`
	SessionID        foundation.ID  `json:"session_id"`
	QuestionNo       int            `json:"question_no"`
	FollowUpNo       int            `json:"follow_up_no"`
	ParentQuestionID *foundation.ID `json:"parent_question_id,omitempty"`
	ClaimID          foundation.ID  `json:"claim_id"`
	TopicID          *foundation.ID `json:"topic_id,omitempty"`
	Prompt           string         `json:"prompt"`
	AnswerPoints     []string       `json:"answer_points"`
	Evidence         []EvidenceRef  `json:"evidence"`
	Status           QuestionStatus `json:"status"`
	Fingerprint      string         `json:"fingerprint"`
	CreatedAt        time.Time      `json:"created_at"`
	AnsweredAt       *time.Time     `json:"answered_at,omitempty"`
}

// ScoreDimension 是 0..1 的可解释评分维度。
type ScoreDimension struct {
	Value     float64 `json:"value"`
	Rationale string  `json:"rationale"`
}

// Score 是服务端确定性评分，始终保存对应题目的 Evidence。
type Score struct {
	SchemaVersion string         `json:"schema_version"`
	Correctness   ScoreDimension `json:"correctness"`
	Coverage      ScoreDimension `json:"coverage"`
	Boundaries    ScoreDimension `json:"boundaries"`
	Clarity       ScoreDimension `json:"clarity"`
	Errors        []string       `json:"errors,omitempty"`
	Omissions     []string       `json:"omissions,omitempty"`
	Evidence      []EvidenceRef  `json:"evidence"`
}

// TurnDecision 记录服务端基于本次评分作出的追问或切题决定。
type TurnDecision struct {
	FollowUpCreated    bool           `json:"follow_up_created"`
	FollowUpQuestionID *foundation.ID `json:"follow_up_question_id,omitempty"`
	NextQuestionID     *foundation.ID `json:"next_question_id,omitempty"`
}

// Turn 是一次 Interview 作答及其确定性评分。
type Turn struct {
	ID             foundation.ID `json:"id"`
	WorkspaceID    foundation.ID `json:"workspace_id"`
	SessionID      foundation.ID `json:"session_id"`
	QuestionID     foundation.ID `json:"question_id"`
	IdempotencyKey string        `json:"-"`
	RequestHash    string        `json:"-"`
	UserAnswer     string        `json:"user_answer"`
	Score          Score         `json:"score"`
	Decision       TurnDecision  `json:"decision"`
	ScorerVersion  string        `json:"scorer_version"`
	CreatedAt      time.Time     `json:"created_at"`
}

// Finding 是报告中可定位到 Claim/Evidence 的强项、弱项或表达问题。
type Finding struct {
	ClaimID  foundation.ID  `json:"claim_id"`
	TopicID  *foundation.ID `json:"topic_id,omitempty"`
	Detail   string         `json:"detail"`
	Evidence []EvidenceRef  `json:"evidence"`
}

// ReportSummary 聚合面试覆盖与评分计数。
type ReportSummary struct {
	QuestionsTotal int     `json:"questions_total"`
	AnsweredTotal  int     `json:"answered_total"`
	SkippedTotal   int     `json:"skipped_total"`
	Correctness    float64 `json:"correctness"`
	Coverage       float64 `json:"coverage"`
	Boundaries     float64 `json:"boundaries"`
	Clarity        float64 `json:"clarity"`
}

// ArtifactBinding 是 Interview 事实对 Artifact 当前不可变 Revision 的只读关联。
// 它不拥有 Artifact 生命周期，所有创建和 Revision 规则仍由 Artifact 模块维护。
type ArtifactBinding struct {
	Kind            string        `json:"kind"`
	ArtifactID      foundation.ID `json:"artifact_id"`
	RevisionID      foundation.ID `json:"revision_id"`
	ArtifactVersion int64         `json:"artifact_version"`
}

// Report 是同一 Session 只能生成一次的可解释汇总事实。
type Report struct {
	ID            foundation.ID   `json:"id"`
	WorkspaceID   foundation.ID   `json:"workspace_id"`
	SessionID     foundation.ID   `json:"session_id"`
	SchemaVersion string          `json:"schema_version"`
	Summary       ReportSummary   `json:"summary"`
	Strengths     []Finding       `json:"strengths"`
	Gaps          []Finding       `json:"gaps"`
	Expression    []Finding       `json:"expression"`
	Evidence      []EvidenceRef   `json:"evidence"`
	Artifact      ArtifactBinding `json:"artifact"`
	CreatedAt     time.Time       `json:"created_at"`
}

// LearningPath 是只保存学习步骤和进度的事实；它只引用 Artifact，绝不拥有文档或 Revision。
type LearningPath struct {
	ID          foundation.ID   `json:"id"`
	WorkspaceID foundation.ID   `json:"workspace_id"`
	SessionID   foundation.ID   `json:"session_id"`
	ReportID    foundation.ID   `json:"report_id"`
	Artifact    ArtifactBinding `json:"artifact"`
	Status      PathStatus      `json:"status"`
	Version     int64           `json:"version"`
	CreatedAt   time.Time       `json:"created_at"`
	UpdatedAt   time.Time       `json:"updated_at"`
}

// PathStep 是一条由可解释评分缺口产生的学习动作。
type PathStep struct {
	ID              foundation.ID  `json:"id"`
	WorkspaceID     foundation.ID  `json:"workspace_id"`
	PathID          foundation.ID  `json:"path_id"`
	StepNo          int            `json:"step_no"`
	ClaimID         foundation.ID  `json:"claim_id"`
	TopicID         *foundation.ID `json:"topic_id,omitempty"`
	SourceVersionID foundation.ID  `json:"source_version_id"`
	SourceSpanID    foundation.ID  `json:"source_span_id"`
	EvidenceHash    string         `json:"evidence_hash"`
	Title           string         `json:"title"`
	Rationale       string         `json:"rationale"`
	Status          StepStatus     `json:"status"`
	Version         int64          `json:"version"`
	CreatedAt       time.Time      `json:"created_at"`
	UpdatedAt       time.Time      `json:"updated_at"`
}

// CanonicalConfig 返回副本并稳定排序 Scope 中的 ID，供请求哈希和持久化使用。
func CanonicalConfig(config Config) (Config, error) {
	if err := ValidateConfig(config); err != nil {
		return Config{}, err
	}
	canonical := config
	canonical.Scope.ClaimIDs = canonicalIDs(config.Scope.ClaimIDs)
	canonical.Scope.TopicIDs = canonicalIDs(config.Scope.TopicIDs)
	return canonical, nil
}

// ValidateConfig 校验必须由服务端冻结的面试设置。
func ValidateConfig(config Config) error {
	if config.SchemaVersion != SchemaVersion || !validRequiredText(config.Role, maxRoleBytes) || !validDifficulty(config.Difficulty) ||
		config.DurationMinutes < 1 || config.DurationMinutes > maxDurationMinutes || config.QuestionCount < 1 || config.QuestionCount > maxQuestionCount ||
		config.MaxFollowUps < 0 || config.MaxFollowUps > maxFollowUpsPerSession {
		return invalid(ErrorCodeConfigInvalid, "interview configuration is invalid")
	}
	if len(config.Scope.ClaimIDs) == 0 && len(config.Scope.TopicIDs) == 0 {
		return invalid(ErrorCodeConfigInvalid, "interview scope is required")
	}
	if !validIDs(config.Scope.ClaimIDs) || !validIDs(config.Scope.TopicIDs) {
		return invalid(ErrorCodeConfigInvalid, "interview scope contains invalid or duplicate identifiers")
	}
	return nil
}

// ValidateEvidenceRef 校验 Evidence 必须是对应 Claim 的 SUPPORTS 绑定。
func ValidateEvidenceRef(evidence EvidenceRef) error {
	if evidence.SchemaVersion != EvidenceSchemaVersion || !validID(evidence.ClaimID) || !validID(evidence.IndexVersionID) || !validID(evidence.ChunkID) ||
		!validID(evidence.SourceVersionID) || !validID(evidence.SourceSpanID) ||
		evidence.SupportType != "SUPPORTS" || !validHash(evidence.EvidenceHash) {
		return invalid(ErrorCodeEvidenceInvalid, "interview evidence is invalid")
	}
	return nil
}

// ValidateMaterial 校验题目选择器交给领域层的正式知识材料。
func ValidateMaterial(material Material) error {
	if !validID(material.ClaimID) || material.ClaimStatus != "CONFIRMED" || !validIDPtr(material.TopicID) || !validRequiredText(material.Statement, maxQuestionBytes) ||
		len(material.AnswerPoints) == 0 || len(material.AnswerPoints) > maxAnswerPoints || len(material.Evidence) == 0 || len(material.Evidence) > MaxEvidenceItems {
		return invalid(ErrorCodeEvidenceInvalid, "question material is invalid")
	}
	if !validUniqueText(material.AnswerPoints, maxAnswerPointBytes) {
		return invalid(ErrorCodeEvidenceInvalid, "question material answer points are invalid")
	}
	seen := make(map[string]struct{}, len(material.Evidence))
	for _, evidence := range material.Evidence {
		if err := ValidateEvidenceRef(evidence); err != nil || evidence.ClaimID != material.ClaimID {
			return invalid(ErrorCodeEvidenceInvalid, "question material evidence does not bind its claim")
		}
		if _, exists := seen[evidence.EvidenceHash]; exists {
			return invalid(ErrorCodeEvidenceInvalid, "question material has duplicate evidence")
		}
		seen[evidence.EvidenceHash] = struct{}{}
	}
	return nil
}

// ValidateSession 校验 Interview Session 聚合快照。
func ValidateSession(session Session) error {
	if !validID(session.ID) || !validID(session.WorkspaceID) || !validSessionStatus(session.Status) || session.Version < 1 || session.FollowUpCount < 0 ||
		session.FollowUpCount > session.Config.MaxFollowUps || session.StartedAt.IsZero() || (session.EndedAt != nil && session.EndedAt.Before(session.StartedAt)) {
		return invalid(ErrorCodeConfigInvalid, "interview session identity or lifecycle is invalid")
	}
	if err := ValidateConfig(session.Config); err != nil {
		return err
	}
	if session.Status == SessionStatusActive && session.EndedAt != nil {
		return invalid(ErrorCodeConfigInvalid, "active interview session cannot have an end time")
	}
	if session.Status != SessionStatusActive && session.EndedAt == nil {
		return invalid(ErrorCodeConfigInvalid, "closed interview session requires an end time")
	}
	return nil
}

// SessionDeadline 从冻结的开始时间和时长派生面试截止时间，不建立第二份持久事实。
func SessionDeadline(session Session) time.Time {
	return session.StartedAt.Add(time.Duration(session.Config.DurationMinutes) * time.Minute)
}

// SubmitAllowedAt 判断一次新回答是否发生在会话有效时间窗内。
func SubmitAllowedAt(session Session, at time.Time) bool {
	return !at.IsZero() && !at.Before(session.StartedAt) && at.Before(SessionDeadline(session))
}

// ValidateQuestion 校验冻结题目与其正式知识绑定。
func ValidateQuestion(question Question) error {
	if !validID(question.ID) || !validID(question.WorkspaceID) || !validID(question.SessionID) || question.QuestionNo < 1 || question.FollowUpNo < 0 ||
		!validID(question.ClaimID) || !validIDPtr(question.TopicID) || !validQuestionStatus(question.Status) || !validRequiredText(question.Prompt, maxQuestionBytes) ||
		len(question.AnswerPoints) == 0 || len(question.AnswerPoints) > maxAnswerPoints || len(question.Evidence) == 0 || len(question.Evidence) > MaxEvidenceItems ||
		!validHash(question.Fingerprint) || question.CreatedAt.IsZero() {
		return invalid(ErrorCodeQuestionInvalid, "interview question is invalid")
	}
	if question.FollowUpNo == 0 && question.ParentQuestionID != nil {
		return invalid(ErrorCodeQuestionInvalid, "primary question cannot have a parent")
	}
	if question.FollowUpNo > 0 && (!validIDPtr(question.ParentQuestionID) || question.ParentQuestionID == nil) {
		return invalid(ErrorCodeQuestionInvalid, "follow-up question requires a parent")
	}
	if !validUniqueText(question.AnswerPoints, maxAnswerPointBytes) {
		return invalid(ErrorCodeQuestionInvalid, "question answer points are invalid")
	}
	for _, evidence := range question.Evidence {
		if err := ValidateEvidenceRef(evidence); err != nil || evidence.ClaimID != question.ClaimID {
			return invalid(ErrorCodeEvidenceInvalid, "question evidence does not bind its claim")
		}
	}
	if question.Status == QuestionStatusAnswered && question.AnsweredAt == nil {
		return invalid(ErrorCodeQuestionInvalid, "answered question requires a timestamp")
	}
	if question.Status != QuestionStatusAnswered && question.AnsweredAt != nil {
		return invalid(ErrorCodeQuestionInvalid, "only answered question may retain an answer timestamp")
	}
	return nil
}

// ValidateScore 校验评分不含调用方伪造的内容且引用题目的全部 Evidence。
func ValidateScore(score Score, evidence []EvidenceRef) error {
	if score.SchemaVersion != ScoreSchemaVersion || len(score.Evidence) == 0 || len(score.Evidence) != len(evidence) ||
		len(score.Errors) > maxFeedbackItems || len(score.Omissions) > maxFeedbackItems {
		return invalid(ErrorCodeScoreInvalid, "interview score shape is invalid")
	}
	for _, dimension := range []ScoreDimension{score.Correctness, score.Coverage, score.Boundaries, score.Clarity} {
		if invalidNumber(dimension.Value) || dimension.Value < 0 || dimension.Value > 1 || !validRequiredText(dimension.Rationale, maxFeedbackItemBytes) {
			return invalid(ErrorCodeScoreInvalid, "interview score dimension is invalid")
		}
	}
	if !validUniqueText(score.Errors, maxFeedbackItemBytes) || !validUniqueText(score.Omissions, maxFeedbackItemBytes) {
		return invalid(ErrorCodeScoreInvalid, "interview score feedback is invalid")
	}
	if !sameEvidence(score.Evidence, evidence) {
		return invalid(ErrorCodeScoreInvalid, "interview score evidence drifted")
	}
	return nil
}

// ValidateTurn 校验一个可持久化的作答、评分及转题决定。
func ValidateTurn(turn Turn, question Question) error {
	if !validID(turn.ID) || !validID(turn.WorkspaceID) || !validID(turn.SessionID) || !validID(turn.QuestionID) ||
		turn.QuestionID != question.ID || !validIdempotencyKey(turn.IdempotencyKey) || !validHash(turn.RequestHash) ||
		len(turn.UserAnswer) > maxUserAnswerBytes || !utf8.ValidString(turn.UserAnswer) || strings.ContainsRune(turn.UserAnswer, '\x00') ||
		!validRequiredText(turn.ScorerVersion, 128) || turn.CreatedAt.IsZero() {
		return invalid(ErrorCodeTurnInvalid, "interview turn is invalid")
	}
	if err := ValidateScore(turn.Score, question.Evidence); err != nil {
		return err
	}
	if turn.Decision.FollowUpCreated != (turn.Decision.FollowUpQuestionID != nil) || !validIDPtr(turn.Decision.FollowUpQuestionID) || !validIDPtr(turn.Decision.NextQuestionID) {
		return invalid(ErrorCodeTurnInvalid, "interview turn decision is invalid")
	}
	return nil
}

// ValidateReport 校验报告只汇总自身 Session 的可解释事实。
func ValidateReport(report Report) error {
	if !validID(report.ID) || !validID(report.WorkspaceID) || !validID(report.SessionID) || report.SchemaVersion != ReportSchemaVersion ||
		report.CreatedAt.IsZero() || report.Summary.QuestionsTotal < 1 || report.Summary.AnsweredTotal < 0 || report.Summary.SkippedTotal < 0 ||
		report.Summary.AnsweredTotal+report.Summary.SkippedTotal != report.Summary.QuestionsTotal {
		return invalid(ErrorCodeReportInvalid, "interview report summary is invalid")
	}
	if err := ValidateArtifactBinding(report.Artifact, "INTERVIEW_DOC"); err != nil {
		return err
	}
	for _, value := range []float64{report.Summary.Correctness, report.Summary.Coverage, report.Summary.Boundaries, report.Summary.Clarity} {
		if invalidNumber(value) || value < 0 || value > 1 {
			return invalid(ErrorCodeReportInvalid, "interview report score summary is invalid")
		}
	}
	if err := validateFindings(report.Strengths); err != nil {
		return err
	}
	if err := validateFindings(report.Gaps); err != nil {
		return err
	}
	if err := validateFindings(report.Expression); err != nil {
		return err
	}
	for _, evidence := range report.Evidence {
		if err := ValidateEvidenceRef(evidence); err != nil {
			return err
		}
	}
	return nil
}

// ValidateArtifactBinding 校验 Interview 只保存由 Artifact bridge 返回的稳定关联。
func ValidateArtifactBinding(binding ArtifactBinding, expectedKind string) error {
	if binding.Kind != expectedKind || !validID(binding.ArtifactID) || !validID(binding.RevisionID) || binding.ArtifactVersion < 1 {
		return invalid(ErrorCodeReportInvalid, "interview artifact binding is invalid")
	}
	return nil
}

// ValidateLearningPath 校验路径只保存面试报告派生的进度事实。
func ValidateLearningPath(path LearningPath) error {
	if !validID(path.ID) || !validID(path.WorkspaceID) || !validID(path.SessionID) || !validID(path.ReportID) || !validPathStatus(path.Status) ||
		path.Version < 1 || path.CreatedAt.IsZero() || path.UpdatedAt.IsZero() || path.UpdatedAt.Before(path.CreatedAt) {
		return invalid(ErrorCodePathInvalid, "learning path is invalid")
	}
	if err := ValidateArtifactBinding(path.Artifact, "LEARNING_PATH"); err != nil {
		return err
	}
	return nil
}

// ValidatePathStep 校验步骤只引用确认 Claim 的既有 Evidence 标识。
func ValidatePathStep(step PathStep) error {
	if !validID(step.ID) || !validID(step.WorkspaceID) || !validID(step.PathID) || step.StepNo < 1 || !validID(step.ClaimID) || !validIDPtr(step.TopicID) ||
		!validID(step.SourceVersionID) || !validID(step.SourceSpanID) || !validHash(step.EvidenceHash) || !validRequiredText(step.Title, maxPathTitleBytes) ||
		!validRequiredText(step.Rationale, maxPathRationaleBytes) || !validStepStatus(step.Status) || step.Version < 1 || step.CreatedAt.IsZero() || step.UpdatedAt.IsZero() || step.UpdatedAt.Before(step.CreatedAt) {
		return invalid(ErrorCodePathInvalid, "learning path step is invalid")
	}
	return nil
}

// ComputeQuestionFingerprint 为冻结问题计算稳定业务指纹。
func ComputeQuestionFingerprint(config Config, material Material, questionNo, followUpNo int) (string, error) {
	canonical, err := CanonicalConfig(config)
	if err != nil {
		return "", err
	}
	if err := ValidateMaterial(material); err != nil || questionNo < 1 || followUpNo < 0 {
		return "", invalid(ErrorCodeQuestionInvalid, "question fingerprint input is invalid")
	}
	type fingerprint struct {
		Schema     string        `json:"schema"`
		Role       string        `json:"role"`
		Difficulty Difficulty    `json:"difficulty"`
		ClaimID    foundation.ID `json:"claim_id"`
		Statement  string        `json:"statement"`
		Points     []string      `json:"points"`
		Evidence   []EvidenceRef `json:"evidence"`
		QuestionNo int           `json:"question_no"`
		FollowUpNo int           `json:"follow_up_no"`
	}
	evidence := append([]EvidenceRef(nil), material.Evidence...)
	sort.Slice(evidence, func(i, j int) bool { return evidence[i].EvidenceHash < evidence[j].EvidenceHash })
	payload, err := json.Marshal(fingerprint{SchemaVersion, canonical.Role, canonical.Difficulty, material.ClaimID, material.Statement, material.AnswerPoints, evidence, questionNo, followUpNo})
	if err != nil {
		return "", invalid(ErrorCodeQuestionInvalid, "question fingerprint cannot be encoded")
	}
	digest := sha256.Sum256(payload)
	return hex.EncodeToString(digest[:]), nil
}

// OrderedPendingQuestion 返回尚未作答题目中的稳定下一题。
func OrderedPendingQuestion(questions []Question) *Question {
	values := append([]Question(nil), questions...)
	sort.Slice(values, func(i, j int) bool {
		if values[i].QuestionNo == values[j].QuestionNo {
			return values[i].FollowUpNo < values[j].FollowUpNo
		}
		return values[i].QuestionNo < values[j].QuestionNo
	})
	for index := range values {
		if values[index].Status == QuestionStatusPending {
			question := values[index]
			return &question
		}
	}
	return nil
}

// AllPathStepsTerminal 表示 Learning Path 中不存在仍需执行的步骤。
func AllPathStepsTerminal(steps []PathStep) bool {
	for _, step := range steps {
		if step.Status != StepStatusCompleted && step.Status != StepStatusSkipped {
			return false
		}
	}
	return true
}

// NeedsFollowUp 把评分低于受控阈值的回答标记为可追问。
func NeedsFollowUp(score Score) bool {
	return (score.Correctness.Value+score.Coverage.Value+score.Boundaries.Value)/3 < 0.65
}

// ValidateIdempotencyKey 校验 Interview 命令幂等键。
func ValidateIdempotencyKey(value string) error {
	if !validIdempotencyKey(value) {
		return invalid(ErrorCodeTurnInvalid, "interview idempotency key is invalid")
	}
	return nil
}

func validateFindings(findings []Finding) error {
	if len(findings) > maxQuestionCount*2 {
		return invalid(ErrorCodeReportInvalid, "interview report has too many findings")
	}
	for _, finding := range findings {
		if !validID(finding.ClaimID) || !validIDPtr(finding.TopicID) || !validRequiredText(finding.Detail, maxPathRationaleBytes) || len(finding.Evidence) == 0 || len(finding.Evidence) > MaxEvidenceItems {
			return invalid(ErrorCodeReportInvalid, "interview report finding is invalid")
		}
		for _, evidence := range finding.Evidence {
			if err := ValidateEvidenceRef(evidence); err != nil || evidence.ClaimID != finding.ClaimID {
				return invalid(ErrorCodeReportInvalid, "interview report finding evidence is invalid")
			}
		}
	}
	return nil
}

func sameEvidence(left, right []EvidenceRef) bool {
	if len(left) != len(right) {
		return false
	}
	leftValues := append([]EvidenceRef(nil), left...)
	rightValues := append([]EvidenceRef(nil), right...)
	sort.Slice(leftValues, func(i, j int) bool { return leftValues[i].EvidenceHash < leftValues[j].EvidenceHash })
	sort.Slice(rightValues, func(i, j int) bool { return rightValues[i].EvidenceHash < rightValues[j].EvidenceHash })
	for index := range leftValues {
		if leftValues[index] != rightValues[index] {
			return false
		}
	}
	return true
}

func canonicalIDs(values []foundation.ID) []foundation.ID {
	canonical := append([]foundation.ID(nil), values...)
	sort.Slice(canonical, func(i, j int) bool { return canonical[i] < canonical[j] })
	return canonical
}

func validIDs(values []foundation.ID) bool {
	seen := make(map[foundation.ID]struct{}, len(values))
	for _, value := range values {
		if !validID(value) {
			return false
		}
		if _, found := seen[value]; found {
			return false
		}
		seen[value] = struct{}{}
	}
	return true
}

func validID(value foundation.ID) bool {
	if value == "" {
		return false
	}
	_, err := foundation.ParseID(string(value))
	return err == nil
}

func validIDPtr(value *foundation.ID) bool { return value == nil || validID(*value) }

func validDifficulty(value Difficulty) bool {
	return value == DifficultyFoundation || value == DifficultyIntermediate || value == DifficultyAdvanced
}

func validSessionStatus(value SessionStatus) bool {
	return value == SessionStatusActive || value == SessionStatusCompleted || value == SessionStatusCancelled
}

func validQuestionStatus(value QuestionStatus) bool {
	return value == QuestionStatusPending || value == QuestionStatusAnswered || value == QuestionStatusSkipped
}

func validPathStatus(value PathStatus) bool {
	return value == PathStatusActive || value == PathStatusPaused || value == PathStatusCompleted
}

func validStepStatus(value StepStatus) bool {
	return value == StepStatusPending || value == StepStatusInProgress || value == StepStatusCompleted || value == StepStatusSkipped
}

func validHash(value string) bool {
	if len(value) != 64 || strings.ToLower(value) != value {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}

func validRequiredText(value string, limit int) bool {
	return value != "" && value == strings.TrimSpace(value) && len(value) <= limit && utf8.ValidString(value) && !strings.ContainsRune(value, '\x00')
}

func validUniqueText(values []string, limit int) bool {
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		if !validRequiredText(value, limit) {
			return false
		}
		if _, found := seen[value]; found {
			return false
		}
		seen[value] = struct{}{}
	}
	return true
}

func validIdempotencyKey(value string) bool {
	if value == "" || value != strings.TrimSpace(value) || len(value) > maxIdempotencyKeyBytes || !utf8.ValidString(value) {
		return false
	}
	for _, runeValue := range value {
		if runeValue <= 0x20 || runeValue == 0x7f || unicode.IsSpace(runeValue) {
			return false
		}
	}
	return true
}

func invalidNumber(value float64) bool { return math.IsNaN(value) || math.IsInf(value, 0) }
