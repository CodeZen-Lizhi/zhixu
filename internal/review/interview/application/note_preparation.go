package application

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"time"

	agentdomain "github.com/CodeZen-Lizhi/zhixu/internal/agent/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	organizingapp "github.com/CodeZen-Lizhi/zhixu/internal/organizing/application"
	organizingdomain "github.com/CodeZen-Lizhi/zhixu/internal/organizing/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/review/interview/domain"
	workflowapp "github.com/CodeZen-Lizhi/zhixu/internal/workflow/application"
)

const (
	NotePreparationDefinitionKey           = "synthesis-note-interview"
	NotePreparationDefinitionVersion int64 = 1
	NotePreparationNodeKey                 = "prepare_interview"
	NotePreparationNodeKind                = "interview.synthesis_note.prepare"
	NotePreparationSchemaVersion           = 1
	ErrorCodeNotePreparationInvalid        = "INTERVIEW_NOTE_PREPARATION_INVALID"
	ErrorCodeNotePreparationNotFound       = "INTERVIEW_NOTE_PREPARATION_NOT_FOUND"
	ErrorCodeNotePreparationConflict       = "INTERVIEW_NOTE_PREPARATION_CONFLICT"
	ErrorCodeNotePlanInvalid               = "INTERVIEW_NOTE_PLAN_INVALID"
	ErrorCodeNotePlanUnavailable           = "INTERVIEW_NOTE_PLAN_UNAVAILABLE"
	ErrorCodeNoteRecoveryRequired          = "INTERVIEW_NOTE_RECOVERY_REQUIRED"
)

type NotePreparationStatus string

const (
	NotePreparationQueued           NotePreparationStatus = "QUEUED"
	NotePreparationGenerating       NotePreparationStatus = "GENERATING"
	NotePreparationReady            NotePreparationStatus = "READY"
	NotePreparationFailed           NotePreparationStatus = "FAILED"
	NotePreparationUnavailable      NotePreparationStatus = "CAPABILITY_UNAVAILABLE"
	NotePreparationRecoveryRequired NotePreparationStatus = "RECOVERY_REQUIRED"
)

type NoteInterviewOptions struct {
	Role            string            `json:"role"`
	Difficulty      domain.Difficulty `json:"difficulty"`
	DurationMinutes int               `json:"duration_minutes"`
	QuestionCount   int               `json:"question_count"`
	MaxFollowUps    int               `json:"max_follow_ups"`
}

func (options NoteInterviewOptions) Config(ref domain.NoteRevisionRef) domain.Config {
	return domain.Config{SchemaVersion: domain.SchemaVersion, Role: options.Role, Scope: domain.Scope{NoteRevision: &ref}, Difficulty: options.Difficulty,
		DurationMinutes: options.DurationMinutes, QuestionCount: options.QuestionCount, MaxFollowUps: options.MaxFollowUps}
}

func (options NoteInterviewOptions) Validate() error {
	config := domain.Config{SchemaVersion: domain.SchemaVersion, Role: options.Role, Difficulty: options.Difficulty,
		DurationMinutes: options.DurationMinutes, QuestionCount: options.QuestionCount, MaxFollowUps: options.MaxFollowUps}
	return domain.ValidateConfigSettings(config)
}

type NotePreparationFailure struct {
	Code      string `json:"code"`
	Retryable bool   `json:"retryable"`
}

// NotePreparation stores the frozen material separately from its redacted API
// projection. Model answers and plan bytes never enter Workflow output or SSE.
type NotePreparation struct {
	ID                    foundation.ID                          `json:"id"`
	WorkspaceID           foundation.ID                          `json:"workspace_id"`
	NoteRevision          domain.NoteRevisionRef                 `json:"note_revision"`
	Status                NotePreparationStatus                  `json:"status"`
	WorkflowRunID         foundation.ID                          `json:"workflow_run_id"`
	SessionID             *foundation.ID                         `json:"session_id"`
	Failure               *NotePreparationFailure                `json:"failure"`
	CreatedAt             time.Time                              `json:"created_at"`
	UpdatedAt             time.Time                              `json:"updated_at"`
	Snapshot              organizingdomain.SynthesisNoteSnapshot `json:"-"`
	Options               NoteInterviewOptions                   `json:"options"`
	NodeRunID             foundation.ID                          `json:"-"`
	NodeAttemptID         foundation.ID                          `json:"-"`
	ModelRunID            foundation.ID                          `json:"-"`
	ModelOutput           json.RawMessage                        `json:"-"`
	ModelSettingsRevision *int64                                 `json:"-"`
	Version               int64                                  `json:"-"`
	IdempotencyKey        string                                 `json:"-"`
	RequestHash           string                                 `json:"-"`
	RetryOf               *foundation.ID                         `json:"-"`
}

func (preparation NotePreparation) Validate() error {
	ref, err := domain.NoteRevisionFromSnapshot(preparation.Snapshot)
	if err != nil || preparation.NoteRevision != ref || preparation.WorkspaceID != ref.WorkspaceID || !validID(preparation.ID) ||
		!validID(preparation.WorkflowRunID) || !validID(preparation.NodeRunID) || domain.ValidateConfig(preparation.Options.Config(ref)) != nil || preparation.Version < 1 ||
		preparation.CreatedAt.IsZero() || preparation.UpdatedAt.Before(preparation.CreatedAt) || domain.ValidateIdempotencyKey(preparation.IdempotencyKey) != nil || !validNoteHash(preparation.RequestHash) {
		return domain.InvalidError(ErrorCodeNotePreparationInvalid, "interview preparation binding is invalid")
	}
	if (preparation.Status == NotePreparationGenerating || preparation.Status == NotePreparationReady) && !validID(preparation.NodeAttemptID) {
		return domain.InvalidError(ErrorCodeNotePreparationInvalid, "interview preparation attempt is missing")
	}
	if preparation.Status == NotePreparationQueued && (preparation.NodeAttemptID != "" || preparation.ModelRunID != "" || preparation.ModelSettingsRevision != nil) {
		return domain.InvalidError(ErrorCodeNotePreparationInvalid, "queued interview preparation already has a model binding")
	}
	if preparation.Status != NotePreparationReady && len(preparation.ModelOutput) != 0 {
		return domain.InvalidError(ErrorCodeNotePreparationInvalid, "pending or failed preparation cannot retain accepted model output")
	}
	if preparation.ModelRunID != "" && !validID(preparation.ModelRunID) || preparation.NodeAttemptID != "" && !validID(preparation.NodeAttemptID) ||
		preparation.ModelSettingsRevision != nil && *preparation.ModelSettingsRevision < 0 || preparation.RetryOf != nil && !validID(*preparation.RetryOf) {
		return domain.InvalidError(ErrorCodeNotePreparationInvalid, "interview preparation runtime binding is invalid")
	}
	switch preparation.Status {
	case NotePreparationQueued, NotePreparationGenerating:
		if preparation.SessionID != nil || preparation.Failure != nil {
			return domain.InvalidError(ErrorCodeNotePreparationInvalid, "pending interview preparation has a terminal result")
		}
	case NotePreparationReady:
		if preparation.SessionID == nil || !validID(*preparation.SessionID) || preparation.Failure != nil || preparation.ModelRunID == "" || len(preparation.ModelOutput) == 0 || len(preparation.ModelOutput) > MaxNotePlanBytes || !json.Valid(preparation.ModelOutput) {
			return domain.InvalidError(ErrorCodeNotePreparationInvalid, "ready interview preparation is incomplete")
		}
	case NotePreparationFailed, NotePreparationUnavailable, NotePreparationRecoveryRequired:
		if preparation.SessionID != nil || preparation.Failure == nil || preparation.Failure.Code == "" || preparation.Status == NotePreparationRecoveryRequired && preparation.Failure.Retryable {
			return domain.InvalidError(ErrorCodeNotePreparationInvalid, "failed interview preparation is incomplete")
		}
	default:
		return domain.InvalidError(ErrorCodeNotePreparationInvalid, "interview preparation status is invalid")
	}
	return nil
}

type PrepareNoteInterviewCommand struct {
	WorkspaceID    foundation.ID
	NoteID         foundation.ID
	Options        NoteInterviewOptions
	IdempotencyKey string
}
type RetryNoteInterviewCommand struct {
	WorkspaceID    foundation.ID
	NoteID         foundation.ID
	PreparationID  foundation.ID
	Options        NoteInterviewOptions
	IdempotencyKey string
}
type NotePreparationResult struct {
	Preparation NotePreparation `json:"preparation"`
	Replayed    bool            `json:"replayed"`
}

type NotePreparationStore interface {
	FindNotePreparationReplay(context.Context, foundation.ID, string, string) (NotePreparationResult, bool, error)
	CreateNotePreparation(context.Context, NotePreparation) (NotePreparationResult, error)
	GetNotePreparation(context.Context, foundation.ID, foundation.ID) (NotePreparation, error)
	ListNotePreparations(context.Context, foundation.ID, foundation.ID) ([]NotePreparation, error)
}

type NotePreparationDependencies struct {
	Store     NotePreparationStore
	Snapshots organizingapp.SynthesisNoteSnapshotReader
	IDs       foundation.IDGenerator
	Clock     foundation.Clock
}
type NotePreparationService struct{ dependencies NotePreparationDependencies }

func NewNotePreparationService(dependencies NotePreparationDependencies) (*NotePreparationService, error) {
	if nilNotePort(dependencies.Store) || nilNotePort(dependencies.Snapshots) || nilNotePort(dependencies.IDs) || nilNotePort(dependencies.Clock) {
		return nil, domain.UnavailableError(ErrorCodeNotePlanUnavailable, "interview note preparation dependencies are unavailable")
	}
	return &NotePreparationService{dependencies: dependencies}, nil
}

func (service *NotePreparationService) Prepare(ctx context.Context, command PrepareNoteInterviewCommand) (NotePreparationResult, error) {
	if err := validateNoteCommand(command.WorkspaceID, command.NoteID, command.Options, command.IdempotencyKey); err != nil {
		return NotePreparationResult{}, err
	}
	hash, err := requestHash(struct {
		Kind        string
		WorkspaceID foundation.ID
		NoteID      foundation.ID
		Options     NoteInterviewOptions
	}{"PREPARE_NOTE_INTERVIEW", command.WorkspaceID, command.NoteID, command.Options})
	if err != nil {
		return NotePreparationResult{}, err
	}
	if result, found, err := service.dependencies.Store.FindNotePreparationReplay(ctx, command.WorkspaceID, command.IdempotencyKey, hash); err != nil || found {
		return result, err
	}
	snapshot, err := service.dependencies.Snapshots.ReadPublishedSynthesisNote(ctx, command.WorkspaceID, command.NoteID)
	if err != nil {
		return NotePreparationResult{}, err
	}
	if snapshot.WorkspaceID != command.WorkspaceID || snapshot.NoteID != command.NoteID {
		return NotePreparationResult{}, domain.InvalidError(ErrorCodeNotePreparationInvalid, "published note reader returned a different binding")
	}
	return service.create(ctx, snapshot, command.Options, command.IdempotencyKey, hash, nil)
}

func (service *NotePreparationService) Get(ctx context.Context, workspaceID, noteID, preparationID foundation.ID) (NotePreparation, error) {
	if !validID(workspaceID) || !validID(noteID) || !validID(preparationID) {
		return NotePreparation{}, domain.InvalidError(ErrorCodeNotePreparationInvalid, "interview preparation lookup is invalid")
	}
	preparation, err := service.dependencies.Store.GetNotePreparation(ctx, workspaceID, preparationID)
	if err != nil {
		return NotePreparation{}, err
	}
	if preparation.WorkspaceID != workspaceID || preparation.NoteRevision.NoteID != noteID {
		return NotePreparation{}, domain.NotFoundError(ErrorCodeNotePreparationNotFound, "interview preparation was not found")
	}
	if err := preparation.Validate(); err != nil {
		return NotePreparation{}, err
	}
	return preparation, nil
}

func (service *NotePreparationService) List(ctx context.Context, workspaceID, noteID foundation.ID) ([]NotePreparation, error) {
	if !validID(workspaceID) || !validID(noteID) {
		return nil, domain.InvalidError(ErrorCodeNotePreparationInvalid, "interview preparation list is invalid")
	}
	items, err := service.dependencies.Store.ListNotePreparations(ctx, workspaceID, noteID)
	if err != nil {
		return nil, err
	}
	if len(items) > 20 {
		return nil, domain.InvalidError(ErrorCodeNotePreparationInvalid, "interview preparation list exceeded its limit")
	}
	for _, item := range items {
		if item.Validate() != nil || item.WorkspaceID != workspaceID || item.NoteRevision.NoteID != noteID {
			return nil, domain.InvalidError(ErrorCodeNotePreparationInvalid, "interview preparation list binding is invalid")
		}
	}
	return items, nil
}

func (service *NotePreparationService) Retry(ctx context.Context, command RetryNoteInterviewCommand) (NotePreparationResult, error) {
	if err := validateNoteCommand(command.WorkspaceID, command.NoteID, command.Options, command.IdempotencyKey); err != nil {
		return NotePreparationResult{}, err
	}
	if !validID(command.PreparationID) {
		return NotePreparationResult{}, domain.InvalidError(ErrorCodeNotePreparationInvalid, "interview retry identity is invalid")
	}
	hash, err := requestHash(struct {
		Kind          string
		WorkspaceID   foundation.ID
		PreparationID foundation.ID
		NoteID        foundation.ID
		Options       NoteInterviewOptions
	}{"RETRY_NOTE_INTERVIEW", command.WorkspaceID, command.PreparationID, command.NoteID, command.Options})
	if err != nil {
		return NotePreparationResult{}, err
	}
	if result, found, err := service.dependencies.Store.FindNotePreparationReplay(ctx, command.WorkspaceID, command.IdempotencyKey, hash); err != nil || found {
		return result, err
	}
	previous, err := service.Get(ctx, command.WorkspaceID, command.NoteID, command.PreparationID)
	if err != nil {
		return NotePreparationResult{}, err
	}
	if !previous.RetryAllowed() || previous.Options != command.Options {
		return NotePreparationResult{}, domain.ConflictError(ErrorCodeNotePreparationConflict, "interview preparation cannot be retried with this configuration")
	}
	return service.create(ctx, previous.Snapshot, previous.Options, command.IdempotencyKey, hash, &previous.ID)
}

func (preparation NotePreparation) RetryAllowed() bool {
	return (preparation.Status == NotePreparationUnavailable || preparation.Status == NotePreparationFailed) && preparation.Failure != nil && preparation.Failure.Retryable
}

func (service *NotePreparationService) create(ctx context.Context, snapshot organizingdomain.SynthesisNoteSnapshot, options NoteInterviewOptions, key, hash string, retryOf *foundation.ID) (NotePreparationResult, error) {
	ref, err := domain.NoteRevisionFromSnapshot(snapshot)
	if err != nil {
		return NotePreparationResult{}, err
	}
	kinds := make(map[organizingdomain.SynthesisItemKind]bool)
	for _, item := range snapshot.Items {
		kinds[item.Kind] = true
	}
	if options.QuestionCount < len(kinds) {
		return NotePreparationResult{}, domain.InvalidError(ErrorCodeNotePreparationInvalid, "question count must cover each fact, conflict and gap kind present in the note")
	}
	id, err := service.dependencies.IDs.New()
	if err != nil {
		return NotePreparationResult{}, err
	}
	now := service.dependencies.Clock.Now().UTC()
	preparation := NotePreparation{ID: id, WorkspaceID: snapshot.WorkspaceID, NoteRevision: ref, Snapshot: snapshot, Options: options, Status: NotePreparationQueued,
		Version: 1, IdempotencyKey: key, RequestHash: hash, CreatedAt: now, UpdatedAt: now, RetryOf: retryOf}
	result, err := service.dependencies.Store.CreateNotePreparation(ctx, preparation)
	if err != nil {
		return NotePreparationResult{}, err
	}
	if result.Preparation.Validate() != nil || result.Preparation.WorkspaceID != snapshot.WorkspaceID || result.Preparation.NoteRevision != ref || result.Preparation.Options != options || !reflect.DeepEqual(result.Preparation.RetryOf, retryOf) || result.Preparation.RequestHash != hash || result.Preparation.IdempotencyKey != key {
		return NotePreparationResult{}, domain.InvalidError(ErrorCodeNotePreparationInvalid, "interview preparation persistence returned an invalid binding")
	}
	return result, nil
}

func validateNoteCommand(workspaceID, noteID foundation.ID, options NoteInterviewOptions, key string) error {
	if !validID(workspaceID) || !validID(noteID) || options.Validate() != nil {
		return domain.InvalidError(ErrorCodeNotePreparationInvalid, "interview note preparation input is invalid")
	}
	return domain.ValidateIdempotencyKey(key)
}

// NotePlannedQuestion is an accepted model plan with source labels resolved by
// the server. It is persisted only together with the original model call proof.
type NotePlannedQuestion struct {
	Prompt       string                    `json:"prompt"`
	AnswerPoints []string                  `json:"answer_points"`
	Source       domain.NoteQuestionSource `json:"source"`
	FollowUps    []domain.NoteFollowUp     `json:"follow_ups"`
}

type NoteGenerationStore interface {
	NotePreparationStore
	BeginNoteGeneration(context.Context, foundation.ID, workflowapp.ExecutionContext) (NotePreparation, error)
	BindNoteModelRun(context.Context, NotePreparation, agentdomain.ModelRun) (NotePreparation, error)
	CompleteNoteGeneration(context.Context, NoteGenerationCompletion) (NotePreparation, error)
	FailNoteGeneration(context.Context, NotePreparation, string, bool, bool, time.Time) error
}

type NoteGenerationCompletion struct {
	Preparation NotePreparation
	Execution   workflowapp.ExecutionContext
	ModelRun    agentdomain.ModelRun
	Output      json.RawMessage
	Plan        []NotePlannedQuestion
	Start       StartRecord
	At          time.Time
}

// BuildNoteInterviewStart creates the ordinary Session/Question facts from a
// validated model plan. There is no second Session lifecycle for note interviews.
func BuildNoteInterviewStart(preparation NotePreparation, plan []NotePlannedQuestion, at time.Time) (StartRecord, error) {
	if preparation.Validate() != nil || preparation.Status != NotePreparationGenerating || len(plan) != preparation.Options.QuestionCount || at.Before(preparation.CreatedAt) {
		return StartRecord{}, domain.InvalidError(ErrorCodeNotePlanInvalid, "interview note plan cannot start a session")
	}
	config := preparation.Options.Config(preparation.NoteRevision)
	sessionID, err := derivedID(preparation.ID, "note-interview-session")
	if err != nil {
		return StartRecord{}, err
	}
	session := domain.Session{ID: sessionID, WorkspaceID: preparation.WorkspaceID, Config: config, Status: domain.SessionStatusActive, Version: 1, StartedAt: at}
	questions := make([]domain.Question, 0, len(plan))
	kinds := make(map[organizingdomain.SynthesisItemKind]bool)
	for index, planned := range plan {
		if planned.Source.Revision != preparation.NoteRevision || !noteSourceMatchesSnapshot(planned.Source, preparation.Snapshot) || len(planned.FollowUps) > domain.MaxNoteFollowUps ||
			(preparation.Options.MaxFollowUps == 0 && len(planned.FollowUps) != 0) {
			return StartRecord{}, domain.InvalidError(ErrorCodeNotePlanInvalid, "interview question does not bind the frozen note")
		}
		id, err := derivedID(sessionID, "question:"+string(rune('A'+index)))
		if err != nil {
			return StartRecord{}, err
		}
		question := domain.Question{ID: id, WorkspaceID: preparation.WorkspaceID, SessionID: sessionID, QuestionNo: index + 1, SourceKind: domain.QuestionSourceNoteRevision,
			NoteSource: domain.CloneNoteSource(&planned.Source), Prompt: planned.Prompt, AnswerPoints: append([]string(nil), planned.AnswerPoints...), Evidence: []domain.EvidenceRef{},
			FollowUpPlan: domain.CloneNoteFollowUps(planned.FollowUps), Status: domain.QuestionStatusPending, CreatedAt: at}
		question.Fingerprint, err = domain.ComputeNoteQuestionFingerprint(config, planned.Source, planned.Prompt, planned.AnswerPoints, planned.FollowUps, index+1, 0)
		if err != nil {
			return StartRecord{}, err
		}
		if err := domain.ValidateQuestion(question); err != nil {
			return StartRecord{}, err
		}
		questions = append(questions, question)
		kinds[planned.Source.ItemKind] = true
	}
	for _, item := range preparation.Snapshot.Items {
		if !kinds[item.Kind] {
			return StartRecord{}, domain.InvalidError(ErrorCodeNotePlanInvalid, "interview plan omitted a fact, conflict or gap kind")
		}
	}
	key := "note-interview:" + string(preparation.ID)
	hash, err := requestHash(struct {
		PreparationID foundation.ID
		Config        domain.Config
		Questions     []domain.Question
	}{preparation.ID, config, questions})
	if err != nil {
		return StartRecord{}, err
	}
	return StartRecord{Session: session, Questions: questions, IdempotencyKey: key, RequestHash: hash}, nil
}

func noteSourceMatchesSnapshot(source domain.NoteQuestionSource, snapshot organizingdomain.SynthesisNoteSnapshot) bool {
	for _, item := range snapshot.Items {
		if item.ID == source.ItemID {
			expected := domain.NoteQuestionSource{Revision: source.Revision, ItemID: item.ID, ItemKind: item.Kind, Sources: item.SourceReferences()}
			return domain.SameNoteSource(&source, &expected)
		}
	}
	return false
}

func nilNotePort(value any) bool {
	if value == nil {
		return true
	}
	rv := reflect.ValueOf(value)
	switch rv.Kind() {
	case reflect.Pointer, reflect.Interface, reflect.Func, reflect.Slice, reflect.Map, reflect.Chan:
		return rv.IsNil()
	default:
		return false
	}
}

func NoteFailure(err error) (code string, retryable bool, unknown bool) {
	code = ErrorCodeNotePlanInvalid
	var classified *foundation.Error
	if errors.As(err, &classified) {
		code, retryable = classified.Code, classified.Retryable
		unknown = classified.Kind == foundation.ErrorManualRecoveryRequired
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		unknown = true
	}
	if unknown {
		retryable = false
	}
	return
}

func validNoteHash(value string) bool {
	if len(value) != 64 {
		return false
	}
	for _, c := range value {
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			return false
		}
	}
	return true
}
