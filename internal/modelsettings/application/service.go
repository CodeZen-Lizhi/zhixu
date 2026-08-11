package application

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	"github.com/CodeZen-Lizhi/zhixu/internal/modelsettings/domain"
)

const (
	defaultRuntimeStaleAfter = DefaultRuntimeFreshWithin
	maxLeaseDuration         = 10 * time.Minute
)

// Service is the stable model settings interface used by HTTP, composition roots, and modelctl.
type Service struct {
	revisions         RevisionStore
	rollouts          RolloutStore
	runtimes          RuntimeStore
	availability      RuntimeAvailabilityStore
	activations       ActivationStore
	participants      ParticipantStore
	validator         Validator
	tester            ResolvedConnectionTester
	runtimeStaleAfter time.Duration
}

// String returns a dependency-only summary without credentials or endpoints.
func (service Service) String() string {
	return fmt.Sprintf("Service{revisions_configured:%t rollouts_configured:%t runtimes_configured:%t validator_configured:%t tester_configured:%t}",
		!nilInterface(service.revisions), !nilInterface(service.rollouts), !nilInterface(service.runtimes),
		!nilInterface(service.validator), !nilInterface(service.tester))
}

// GoString uses the same safe dependency-only summary.
func (service Service) GoString() string { return service.String() }

var (
	_ SettingsManager            = (*Service)(nil)
	_ RevisionLoader             = (*Service)(nil)
	_ RolloutControl             = (*Service)(nil)
	_ RuntimeController          = (*Service)(nil)
	_ RuntimeAvailabilityControl = (*Service)(nil)
	_ ActivationControl          = (*Service)(nil)
)

// NewService creates the compatibility facade while callers migrate to the narrow module interfaces.
func NewService(revisions RevisionStore, validator Validator, runtimeStaleAfter time.Duration) (*Service, error) {
	if nilInterface(revisions) || nilInterface(validator) {
		return nil, unavailable(errors.New("model settings dependency is unavailable"))
	}
	if runtimeStaleAfter == 0 {
		runtimeStaleAfter = defaultRuntimeStaleAfter
	}
	if !ValidRuntimeFreshWithin(runtimeStaleAfter) {
		return nil, invalid(errors.New("runtime stale interval is invalid"))
	}
	rollouts, _ := revisions.(RolloutStore)
	runtimes, _ := revisions.(RuntimeStore)
	availability, _ := revisions.(RuntimeAvailabilityStore)
	activations, _ := revisions.(ActivationStore)
	participants, _ := revisions.(ParticipantStore)
	return &Service{
		revisions: revisions, rollouts: rollouts, runtimes: runtimes, availability: availability,
		activations: activations, participants: participants,
		validator: validator, runtimeStaleAfter: runtimeStaleAfter,
	}, nil
}

// NewSettingsManager creates the HTTP-facing module and keeps resolved credentials inside it.
func NewSettingsManager(revisions RevisionStore, validator Validator, tester ResolvedConnectionTester, runtimeStaleAfter time.Duration) (SettingsManager, error) {
	if nilInterface(tester) {
		return nil, unavailable(errors.New("model settings connection tester is unavailable"))
	}
	service, err := NewService(revisions, validator, runtimeStaleAfter)
	if err != nil {
		return nil, err
	}
	service.tester = tester
	return service, nil
}

// Snapshot returns desired, active, rollout, and per-role applied projections without credentials.
func (service *Service) Snapshot(ctx context.Context) (domain.Snapshot, error) {
	if err := service.ready(ctx); err != nil {
		return domain.Snapshot{}, err
	}
	return service.revisions.Snapshot(ctx, service.runtimeStaleAfter)
}

// Save validates and atomically appends a new desired revision; it never changes active.
func (service *Service) Save(ctx context.Context, command SaveCommand) (domain.Snapshot, error) {
	if err := service.ready(ctx); err != nil {
		return domain.Snapshot{}, err
	}
	canonical, err := domain.CanonicalizeSettings(command.Settings)
	if err != nil {
		return domain.Snapshot{}, err
	}
	command.Settings = canonical
	if err := validateSaveCommand(command); err != nil {
		return domain.Snapshot{}, err
	}
	snapshot, err := service.revisions.Snapshot(ctx, service.runtimeStaleAfter)
	if err != nil {
		return domain.Snapshot{}, err
	}
	if snapshot.DesiredRevision != command.ExpectedRevision {
		return domain.Snapshot{}, revisionConflict(errors.New("desired revision changed"))
	}
	secretConfiguration, err := projectedSecretConfiguration(snapshot.DesiredSettings, command.Settings, command.ChatSecret, command.EmbeddingSecret)
	if err != nil {
		return domain.Snapshot{}, err
	}
	if err := service.validator.ValidateModelSettings(ctx, command.Settings, secretConfiguration); err != nil {
		return domain.Snapshot{}, err
	}
	return service.revisions.SaveDesired(ctx, command)
}

// Test resolves, exercises, and destroys one non-persistent settings draft inside the module.
func (service *Service) Test(ctx context.Context, command TestCommand) (TestResult, error) {
	if err := service.ready(ctx); err != nil {
		return TestResult{}, err
	}
	if nilInterface(service.tester) {
		return TestResult{}, unavailable(errors.New("model settings connection tester is unavailable"))
	}
	if command.Target != ConnectionTargetChat && command.Target != ConnectionTargetEmbedding {
		return TestResult{}, invalid(errors.New("model settings connection target is invalid"))
	}
	resolved, err := service.resolveDraft(ctx, command.Draft)
	if err != nil {
		return TestResult{}, err
	}
	defer resolved.ChatAPIKey.Destroy()
	defer resolved.EmbeddingAPIKey.Destroy()
	if resolved.Revision != command.Draft.ExpectedRevision {
		return TestResult{}, foundation.NewError(foundation.ErrorConsistencyViolation, domain.ErrorCodeCorrupt, false, errors.New("resolved model settings revision is inconsistent"))
	}
	startedAt := time.Now()
	if err := service.tester.TestResolvedConnection(ctx, command.Target, resolved); err != nil {
		return TestResult{}, err
	}
	result := TestResult{Target: command.Target, LatencyMS: time.Since(startedAt).Milliseconds()}
	switch command.Target {
	case ConnectionTargetChat:
		result.Provider = string(resolved.Settings.Chat.Provider)
		result.Model = resolved.Settings.Chat.Model
		result.APIStyle = resolved.Settings.Chat.APIStyle
		if result.APIStyle == domain.ChatAPIStyleResponses {
			result.EndpointPath = "/v1/responses"
		} else {
			result.EndpointPath = "/v1/chat/completions"
		}
	case ConnectionTargetEmbedding:
		result.Provider = string(resolved.Settings.Embedding.Provider)
		result.Model = resolved.Settings.Embedding.Model
		result.EndpointPath = "/v1/embeddings"
	}
	return result, nil
}

func (service *Service) resolveDraft(ctx context.Context, command DraftCommand) (domain.ResolvedSettings, error) {
	if err := service.ready(ctx); err != nil {
		return domain.ResolvedSettings{}, err
	}
	canonical, err := domain.CanonicalizeSettings(command.Settings)
	if err != nil {
		return domain.ResolvedSettings{}, err
	}
	command.Settings = canonical
	if command.ExpectedRevision < 0 || command.ChatSecret.Validate() != nil || command.EmbeddingSecret.Validate() != nil {
		return domain.ResolvedSettings{}, invalid(errors.New("model settings draft is invalid"))
	}
	snapshot, err := service.revisions.Snapshot(ctx, service.runtimeStaleAfter)
	if err != nil {
		return domain.ResolvedSettings{}, err
	}
	if snapshot.DesiredRevision != command.ExpectedRevision {
		return domain.ResolvedSettings{}, revisionConflict(errors.New("desired revision changed"))
	}
	secrets, err := projectedSecretConfiguration(snapshot.DesiredSettings, command.Settings, command.ChatSecret, command.EmbeddingSecret)
	if err != nil {
		return domain.ResolvedSettings{}, err
	}
	if err := service.validator.ValidateModelSettings(ctx, command.Settings, secrets); err != nil {
		return domain.ResolvedSettings{}, err
	}
	return service.revisions.ResolveDraft(ctx, command)
}

// LoadRevision decrypts revision zero or one fixed persisted revision for process composition.
func (service *Service) LoadRevision(ctx context.Context, revision int64) (domain.ResolvedSettings, error) {
	if err := service.ready(ctx); err != nil {
		return domain.ResolvedSettings{}, err
	}
	if revision < 0 {
		return domain.ResolvedSettings{}, invalid(errors.New("model settings revision is invalid"))
	}
	resolved, err := service.revisions.LoadRevision(ctx, revision)
	if err != nil {
		return domain.ResolvedSettings{}, err
	}
	secrets := domain.SecretConfiguration{ChatConfigured: resolved.ChatAPIKey.Configured(), EmbeddingConfigured: resolved.EmbeddingAPIKey.Configured()}
	if err := service.validator.ValidateModelSettings(ctx, resolved.Settings, secrets); err != nil {
		resolved.ChatAPIKey.Destroy()
		resolved.EmbeddingAPIKey.Destroy()
		return domain.ResolvedSettings{}, err
	}
	return resolved, nil
}

func (service *Service) BeginRollout(ctx context.Context, command BeginRolloutCommand) (domain.RolloutState, error) {
	if err := service.readyRollout(ctx); err != nil {
		return domain.RolloutState{}, err
	}
	if !validID(command.RolloutID) || !validLease(command.LeaseDuration) {
		return domain.RolloutState{}, invalid(errors.New("rollout begin command is invalid"))
	}
	return service.rollouts.BeginRollout(ctx, command)
}

func (service *Service) RenewRollout(ctx context.Context, command RenewRolloutCommand) (domain.RolloutState, error) {
	if err := service.readyRollout(ctx); err != nil {
		return domain.RolloutState{}, err
	}
	if !validID(command.RolloutID) || command.ExpectedPhase == domain.RolloutPhaseIdle || command.ExpectedPhase == domain.RolloutPhaseFailed || !validLease(command.LeaseDuration) {
		return domain.RolloutState{}, invalid(errors.New("rollout renewal command is invalid"))
	}
	return service.rollouts.RenewRollout(ctx, command)
}

func (service *Service) AdvanceRollout(ctx context.Context, command AdvanceRolloutCommand) (domain.RolloutState, error) {
	if err := service.readyRollout(ctx); err != nil {
		return domain.RolloutState{}, err
	}
	if !validID(command.RolloutID) || !validLease(command.LeaseDuration) {
		return domain.RolloutState{}, invalid(errors.New("rollout advance command is invalid"))
	}
	if err := domain.ValidateRolloutTransition(command.ExpectedPhase, command.NextPhase); err != nil {
		return domain.RolloutState{}, err
	}
	return service.rollouts.AdvanceRollout(ctx, command)
}

func (service *Service) FailRollout(ctx context.Context, command FailRolloutCommand) (domain.RolloutState, error) {
	if err := service.readyRollout(ctx); err != nil {
		return domain.RolloutState{}, err
	}
	if !validID(command.RolloutID) || !canonicalErrorCode(command.ErrorCode) {
		return domain.RolloutState{}, invalid(errors.New("rollout failure command is invalid"))
	}
	return service.rollouts.FailRollout(ctx, command)
}

func (service *Service) CommitRollout(ctx context.Context, command CommitRolloutCommand) (domain.RolloutState, error) {
	if err := service.readyRollout(ctx); err != nil {
		return domain.RolloutState{}, err
	}
	if !validID(command.RolloutID) || !ValidRuntimeFreshWithin(command.FreshWithin) {
		return domain.RolloutState{}, invalid(errors.New("rollout commit command is invalid"))
	}
	return service.rollouts.CommitRollout(ctx, command)
}

func (service *Service) RecoverExpiredRollout(ctx context.Context) (domain.RolloutState, bool, error) {
	if err := service.readyRollout(ctx); err != nil {
		return domain.RolloutState{}, false, err
	}
	return service.rollouts.RecoverExpiredRollout(ctx)
}

func (service *Service) RegisterRuntime(ctx context.Context, registration RuntimeRegistration) (domain.RuntimeRecord, error) {
	if err := service.readyRuntime(ctx); err != nil {
		return domain.RuntimeRecord{}, err
	}
	if err := validateRuntimeRegistration(registration); err != nil {
		return domain.RuntimeRecord{}, err
	}
	if registration.StaleAfter == 0 {
		registration.StaleAfter = service.runtimeStaleAfter
	}
	return service.runtimes.RegisterRuntime(ctx, registration)
}

func (service *Service) HeartbeatRuntime(ctx context.Context, heartbeat RuntimeHeartbeat) (domain.RuntimeRecord, error) {
	if err := service.readyRuntime(ctx); err != nil {
		return domain.RuntimeRecord{}, err
	}
	if !domain.ValidRuntimeRole(heartbeat.Role) || !validID(heartbeat.InstanceID) || !validOptionalID(heartbeat.RolloutID) {
		return domain.RuntimeRecord{}, invalid(errors.New("runtime heartbeat is invalid"))
	}
	return service.runtimes.HeartbeatRuntime(ctx, heartbeat)
}

func (service *Service) SetRuntimePhase(ctx context.Context, command RuntimePhaseCommand) (domain.RuntimeRecord, error) {
	if err := service.readyRuntime(ctx); err != nil {
		return domain.RuntimeRecord{}, err
	}
	if !domain.ValidRuntimeRole(command.Role) || !validID(command.InstanceID) || !validOptionalID(command.RolloutID) ||
		!domain.ValidRuntimePhase(command.ExpectedPhase) || !domain.ValidRuntimePhase(command.NextPhase) || command.ExpectedPhase == command.NextPhase {
		return domain.RuntimeRecord{}, invalid(errors.New("runtime phase command is invalid"))
	}
	if command.RolloutID == nil {
		return domain.RuntimeRecord{}, invalid(errors.New("runtime phase rollout is missing"))
	}
	if err := domain.ValidateRuntimeTransition(command.ExpectedPhase, command.NextPhase); err != nil {
		return domain.RuntimeRecord{}, err
	}
	return service.runtimes.SetRuntimePhase(ctx, command)
}

// RestoreRuntimeAvailability publishes readiness only after the exact local
// generation has been rebuilt. It cannot change owner or applied revision.
func (service *Service) RestoreRuntimeAvailability(ctx context.Context, command RestoreRuntimeAvailabilityCommand) (domain.RuntimeRecord, error) {
	if err := service.readyRuntimeAvailability(ctx); err != nil {
		return domain.RuntimeRecord{}, err
	}
	if !domain.ValidRuntimeRole(command.Role) || !validID(command.InstanceID) || command.AppliedRevision <= 0 {
		return domain.RuntimeRecord{}, invalid(errors.New("runtime availability restore command is invalid"))
	}
	return service.availability.RestoreRuntimeAvailability(ctx, command)
}

func (service *Service) ready(ctx context.Context) error {
	if service == nil || nilInterface(service.revisions) || nilInterface(service.validator) {
		return unavailable(errors.New("model settings service is unavailable"))
	}
	if ctx == nil {
		return invalid(errors.New("model settings context is nil"))
	}
	return nil
}

func (service *Service) readyRollout(ctx context.Context) error {
	if err := service.ready(ctx); err != nil {
		return err
	}
	if nilInterface(service.rollouts) {
		return unavailable(errors.New("model settings rollout store is unavailable"))
	}
	return nil
}

func (service *Service) readyRuntime(ctx context.Context) error {
	if err := service.ready(ctx); err != nil {
		return err
	}
	if nilInterface(service.runtimes) {
		return unavailable(errors.New("model settings runtime store is unavailable"))
	}
	return nil
}

func (service *Service) readyRuntimeAvailability(ctx context.Context) error {
	if err := service.ready(ctx); err != nil {
		return err
	}
	if nilInterface(service.availability) {
		return unavailable(errors.New("model settings runtime availability store is unavailable"))
	}
	return nil
}

func validateSaveCommand(command SaveCommand) error {
	if command.ExpectedRevision < 0 || command.Settings.ValidateStructural() != nil || command.ChatSecret.Validate() != nil || command.EmbeddingSecret.Validate() != nil ||
		command.CreatedBy == "" || command.CreatedBy != strings.TrimSpace(command.CreatedBy) || len(command.CreatedBy) > 256 || strings.ContainsAny(command.CreatedBy, "\r\n\x00") {
		return invalid(errors.New("model settings save command is invalid"))
	}
	return nil
}

func projectedSecretConfiguration(previous domain.SettingsSummary, next domain.Settings, chatAction, embeddingAction domain.SecretAction) (domain.SecretConfiguration, error) {
	if err := domain.ValidateSecretChange(string(previous.Settings.Chat.Provider), previous.Settings.Chat.BaseURL, previous.Secrets.ChatConfigured,
		string(next.Chat.Provider), next.Chat.BaseURL, chatAction); err != nil {
		return domain.SecretConfiguration{}, err
	}
	if err := domain.ValidateSecretChange(string(previous.Settings.Embedding.Provider), previous.Settings.Embedding.BaseURL, previous.Secrets.EmbeddingConfigured,
		string(next.Embedding.Provider), next.Embedding.BaseURL, embeddingAction); err != nil {
		return domain.SecretConfiguration{}, err
	}
	configured := func(previous bool, action domain.SecretAction) bool {
		switch action.Kind {
		case domain.SecretActionKeep:
			return previous
		case domain.SecretActionReplace:
			return true
		default:
			return false
		}
	}
	return domain.SecretConfiguration{
		ChatConfigured:      configured(previous.Secrets.ChatConfigured, chatAction),
		EmbeddingConfigured: configured(previous.Secrets.EmbeddingConfigured, embeddingAction),
	}, nil
}

func validateRuntimeRegistration(registration RuntimeRegistration) error {
	if !domain.ValidRuntimeRole(registration.Role) || !validID(registration.InstanceID) || registration.AppliedRevision < 0 ||
		!validOptionalID(registration.RolloutID) || !domain.ValidRuntimePhase(registration.Phase) {
		return invalid(errors.New("runtime registration is invalid"))
	}
	if registration.StaleAfter != 0 && !ValidRuntimeFreshWithin(registration.StaleAfter) {
		return invalid(errors.New("runtime registration stale interval is invalid"))
	}
	if registration.RolloutID == nil && registration.Phase != domain.RuntimePhaseActive && registration.Phase != domain.RuntimePhaseUnavailable {
		return invalid(errors.New("non-rollout runtime phase is invalid"))
	}
	if registration.RolloutID != nil && registration.Phase != domain.RuntimePhasePrepared && registration.Phase != domain.RuntimePhaseVerifying {
		return invalid(errors.New("rollout runtime phase is invalid"))
	}
	return nil
}

func validID(id foundation.ID) bool {
	parsed, err := foundation.ParseID(string(id))
	return err == nil && parsed == id
}

func validOptionalID(id *foundation.ID) bool { return id == nil || validID(*id) }

func validLease(duration time.Duration) bool {
	return duration >= time.Second && duration <= maxLeaseDuration && duration%time.Microsecond == 0
}

func canonicalErrorCode(value string) bool {
	if value == "" || value != strings.TrimSpace(value) || len(value) > 128 {
		return false
	}
	for _, character := range value {
		if character != '_' && (character < 'A' || character > 'Z') && (character < '0' || character > '9') {
			return false
		}
	}
	return true
}

func nilInterface(value any) bool {
	if value == nil {
		return true
	}
	reflected := reflect.ValueOf(value)
	switch reflected.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return reflected.IsNil()
	default:
		return false
	}
}

func invalid(cause error) error {
	return foundation.NewError(foundation.ErrorInvalidInput, domain.ErrorCodeInvalid, false, cause)
}

func unavailable(cause error) error {
	return foundation.NewError(foundation.ErrorDependencyUnavailable, domain.ErrorCodeUnavailable, true, cause)
}

func revisionConflict(cause error) error {
	return foundation.NewError(foundation.ErrorVersionConflict, domain.ErrorCodeRevisionConflict, false, cause)
}
