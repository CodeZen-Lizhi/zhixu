package application

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"mime"
	"net/url"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/CodeZen-Lizhi/zhixu/internal/capture/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	workspacedomain "github.com/CodeZen-Lizhi/zhixu/internal/workspace/domain"
)

const (
	commandCreateText   = "CREATE_TEXT"
	commandCreateURL    = "CREATE_URL"
	commandCreateFile   = "CREATE_FILE"
	commandCreateImage  = "CREATE_IMAGE"
	maxIdempotencyBytes = 128
	contentFinalizeWait = 5 * time.Second
)

// Service creates Capture records and their immutable Workspace source material.
type Service struct{ dependencies Dependencies }

// NewService constructs a Capture service with durable command and content ports.
func NewService(dependencies Dependencies) (*Service, error) {
	if dependencies.Repository == nil || dependencies.Content == nil || dependencies.IDs == nil || dependencies.Clock == nil {
		return nil, unavailable("capture repository, content writer, id generator, and clock are required")
	}
	return &Service{dependencies: dependencies}, nil
}

// Get returns one Workspace-bound Capture read model.
func (service *Service) Get(ctx context.Context, workspaceID, captureID foundation.ID) (domain.Capture, error) {
	if service == nil || service.dependencies.Repository == nil {
		return domain.Capture{}, unavailable("capture repository is unavailable")
	}
	if err := contextDone(ctx); err != nil {
		return domain.Capture{}, err
	}
	if !validID(workspaceID) || !validID(captureID) {
		return domain.Capture{}, invalid("CAPTURE_QUERY_INVALID", "capture query identity is invalid")
	}
	capture, err := service.dependencies.Repository.Get(ctx, workspaceID, captureID)
	if err != nil {
		return domain.Capture{}, err
	}
	if err := capture.Validate(); err != nil || capture.WorkspaceID != workspaceID || capture.ID != captureID {
		return domain.Capture{}, inconsistent("CAPTURE_RESULT_INVALID", "capture query returned an invalid binding")
	}
	return capture, nil
}

// List returns a bounded Workspace page ordered by capture time and identity.
func (service *Service) List(ctx context.Context, query ListQuery) (Page, error) {
	if service == nil || service.dependencies.Repository == nil {
		return Page{}, unavailable("capture repository is unavailable")
	}
	if err := contextDone(ctx); err != nil {
		return Page{}, err
	}
	if !validID(query.WorkspaceID) || query.Limit < 1 || query.Limit > 100 ||
		(query.Kind != "" && !query.Kind.Valid()) || (query.Status != "" && !query.Status.Valid()) ||
		(query.After != nil && (query.After.CapturedAt.IsZero() || !validID(query.After.ID))) {
		return Page{}, invalid("CAPTURE_LIST_INVALID", "capture list query is invalid")
	}
	page, err := service.dependencies.Repository.List(ctx, query)
	if err != nil {
		return Page{}, err
	}
	if len(page.Items) > query.Limit || (page.Next != nil && (page.Next.CapturedAt.IsZero() || !validID(page.Next.ID))) {
		return Page{}, inconsistent("CAPTURE_RESULT_INVALID", "capture list returned an invalid page")
	}
	for _, capture := range page.Items {
		if err := capture.Validate(); err != nil || capture.WorkspaceID != query.WorkspaceID {
			return Page{}, inconsistent("CAPTURE_RESULT_INVALID", "capture list crossed its Workspace binding")
		}
	}
	return page, nil
}

// CreateText saves plain text before asynchronous ingestion starts.
func (service *Service) CreateText(ctx context.Context, command TextCommand) (CreateResult, error) {
	if strings.TrimSpace(command.Text) == "" || !utf8.ValidString(command.Text) || strings.ContainsRune(command.Text, '\x00') {
		return CreateResult{}, invalid("CAPTURE_TEXT_INVALID", "capture text is empty or invalid")
	}
	content := []byte(command.Text)
	return service.createMaterialized(ctx, materializedCommand{
		WorkspaceID: command.WorkspaceID, Kind: domain.KindText, DisplayName: command.DisplayName,
		MediaType: "text/plain", Content: content, IdempotencyKey: command.IdempotencyKey,
		CommandType: commandCreateText, DefaultName: "Quick note",
	})
}

// CreateURL saves URL provenance and a Source before the asynchronous fetch starts.
func (service *Service) CreateURL(ctx context.Context, command URLCommand) (CreateResult, error) {
	if err := contextDone(ctx); err != nil {
		return CreateResult{}, err
	}
	if !validID(command.WorkspaceID) {
		return CreateResult{}, invalid("CAPTURE_WORKSPACE_INVALID", "capture workspace is invalid")
	}
	key, err := normalizeIdempotencyKey(command.IdempotencyKey)
	if err != nil {
		return CreateResult{}, err
	}
	rawURL, parsed, err := normalizeURL(command.URL)
	if err != nil {
		return CreateResult{}, err
	}
	displayName, err := normalizeDisplayName(command.DisplayName, parsed.Hostname())
	if err != nil {
		return CreateResult{}, err
	}
	binding, err := createBinding(command.WorkspaceID, key, commandCreateURL, createHashPayload{
		Kind: domain.KindURL, DisplayName: displayName, URL: rawURL,
	})
	if err != nil {
		return CreateResult{}, err
	}
	if replay, found, replayErr := service.dependencies.Repository.ReplayCreate(ctx, binding); replayErr != nil {
		return CreateResult{}, replayErr
	} else if found {
		return validateResult(replay, command.WorkspaceID, domain.KindURL, true)
	}
	ids, err := service.newCreateIDs(false)
	if err != nil {
		return CreateResult{}, err
	}
	now, err := service.now()
	if err != nil {
		return CreateResult{}, err
	}
	location := sourceLocation(ids.captureID)
	capture := domain.Capture{
		ID: ids.captureID, WorkspaceID: command.WorkspaceID, Kind: domain.KindURL, DisplayName: displayName,
		OriginalLocation: location, OriginalURL: rawURL, SourceID: ids.sourceID,
		Status: domain.StatusReceived, FetchStatus: domain.StagePending, IngestionStatus: domain.StagePending,
		IndexStatus: domain.StagePending, ProfileStatus: domain.StagePending,
		Version: 1, CapturedAt: now, UpdatedAt: now,
	}
	source := sourceFor(capture, now)
	result, err := service.dependencies.Repository.Create(ctx, CreateRecord{
		Binding: binding, Capture: capture, Source: source, OutboxID: ids.outboxID,
		EventKey: "capture.process:v1:" + string(ids.captureID), CreatedAt: now,
	})
	if err != nil {
		return CreateResult{}, err
	}
	return validateResult(result, command.WorkspaceID, domain.KindURL, result.Replayed)
}

// CreateUpload saves a bounded document or image without trusting the supplied filename as a path.
func (service *Service) CreateUpload(ctx context.Context, command UploadCommand) (CreateResult, error) {
	if command.Kind != domain.KindFile && command.Kind != domain.KindImage {
		return CreateResult{}, invalid("CAPTURE_KIND_INVALID", "upload capture kind is invalid")
	}
	mediaType, err := normalizeMediaType(command.Kind, command.MediaType)
	if err != nil {
		return CreateResult{}, err
	}
	defaultName := safeFileName(command.FileName)
	if defaultName == "" {
		if command.Kind == domain.KindImage {
			defaultName = "Captured image"
		} else {
			defaultName = "Uploaded file"
		}
	}
	commandType := commandCreateFile
	if command.Kind == domain.KindImage {
		commandType = commandCreateImage
	}
	return service.createMaterialized(ctx, materializedCommand{
		WorkspaceID: command.WorkspaceID, Kind: command.Kind, DisplayName: command.DisplayName,
		MediaType: mediaType, Content: append([]byte(nil), command.Content...), IdempotencyKey: command.IdempotencyKey,
		CommandType: commandType, DefaultName: defaultName,
	})
}

type materializedCommand struct {
	WorkspaceID    foundation.ID
	Kind           domain.Kind
	DisplayName    string
	MediaType      string
	Content        []byte
	IdempotencyKey string
	CommandType    string
	DefaultName    string
}

type createIDs struct {
	captureID  foundation.ID
	sourceID   foundation.ID
	artifactID foundation.ID
	versionID  foundation.ID
	outboxID   foundation.ID
}

func (service *Service) createMaterialized(ctx context.Context, command materializedCommand) (CreateResult, error) {
	if err := contextDone(ctx); err != nil {
		return CreateResult{}, err
	}
	if !validID(command.WorkspaceID) || !command.Kind.Valid() {
		return CreateResult{}, invalid("CAPTURE_REQUEST_INVALID", "capture request identity is invalid")
	}
	if len(command.Content) == 0 || int64(len(command.Content)) > workspacedomain.MaxCommittedSourceBytes {
		return CreateResult{}, invalid("CAPTURE_CONTENT_SIZE_INVALID", "capture content size is invalid")
	}
	key, err := normalizeIdempotencyKey(command.IdempotencyKey)
	if err != nil {
		return CreateResult{}, err
	}
	displayName, err := normalizeDisplayName(command.DisplayName, command.DefaultName)
	if err != nil {
		return CreateResult{}, err
	}
	digest := sha256.Sum256(command.Content)
	contentHash := hex.EncodeToString(digest[:])
	binding, err := createBinding(command.WorkspaceID, key, command.CommandType, createHashPayload{
		Kind: command.Kind, DisplayName: displayName, ContentHash: contentHash, MediaType: command.MediaType,
	})
	if err != nil {
		return CreateResult{}, err
	}
	if replay, found, replayErr := service.dependencies.Repository.ReplayCreate(ctx, binding); replayErr != nil {
		return CreateResult{}, replayErr
	} else if found {
		validated, validateErr := validateResult(replay, command.WorkspaceID, command.Kind, true)
		if validateErr != nil {
			return CreateResult{}, validateErr
		}
		if validated.Capture.OriginalInputHash != contentHash || validated.Capture.LatestSourceVersionID == "" {
			return CreateResult{}, inconsistent("CAPTURE_RESULT_INVALID", "capture replay returned a different content binding")
		}
		stage, stageErr := service.stageContent(ctx, command.WorkspaceID, materializedStageRef(binding), command.Content, contentHash)
		if stageErr != nil {
			return CreateResult{}, stageErr
		}
		if publishErr := service.publishConfirmedContent(ctx, command.WorkspaceID, stage); publishErr != nil {
			return CreateResult{}, publishErr
		}
		return validated, nil
	}
	ids, err := service.newCreateIDs(true)
	if err != nil {
		return CreateResult{}, err
	}
	now, err := service.now()
	if err != nil {
		return CreateResult{}, err
	}
	location := sourceLocation(ids.captureID)
	stage, err := service.stageContent(ctx, command.WorkspaceID, materializedStageRef(binding), command.Content, contentHash)
	if err != nil {
		return CreateResult{}, err
	}
	capture := domain.Capture{
		ID: ids.captureID, WorkspaceID: command.WorkspaceID, Kind: command.Kind, DisplayName: displayName,
		OriginalLocation: location, OriginalInputHash: contentHash, SourceID: ids.sourceID, LatestSourceVersionID: ids.versionID,
		Status: domain.StatusSourceSaved, FetchStatus: domain.StageNotApplicable, IngestionStatus: domain.StagePending,
		IndexStatus: domain.StagePending, ProfileStatus: domain.StagePending,
		Version: 1, CapturedAt: now, UpdatedAt: now,
	}
	source := sourceFor(capture, now)
	registration := workspacedomain.SourceRegistration{
		Source: source,
		Artifact: workspacedomain.ContentArtifact{
			ID: ids.artifactID, WorkspaceID: command.WorkspaceID, ContentHash: contentHash,
			ByteSize: int64(len(command.Content)), ManagedLocation: stage.ManagedLocation, CreatedAt: now,
		},
		Version: workspacedomain.SourceVersion{
			ID: ids.versionID, SourceID: ids.sourceID, ContentArtifactID: ids.artifactID,
			ContentHash: contentHash, ByteSize: int64(len(command.Content)), MediaType: command.MediaType,
			OriginalContentLocation: location, SecurityStatus: "pending", CapturedAt: now,
		},
	}
	result, err := service.dependencies.Repository.Create(ctx, CreateRecord{
		Binding: binding, Capture: capture, Source: source, Registration: &registration,
		OutboxID: ids.outboxID, EventKey: "capture.process:v1:" + string(ids.captureID), CreatedAt: now,
	})
	if err != nil {
		recoveryCtx, cancel := contentRecoveryContext(ctx)
		replay, found, replayErr := service.dependencies.Repository.ReplayCreate(recoveryCtx, binding)
		if replayErr != nil {
			if captureErrorCode(replayErr) == "CAPTURE_IDEMPOTENCY_CONFLICT" {
				discardErr := service.dependencies.Content.DiscardManagedBytes(recoveryCtx, command.WorkspaceID, stage)
				cancel()
				return CreateResult{}, errors.Join(replayErr, discardErr)
			}
			cancel()
			return CreateResult{}, contentFinalizationUnknown(errors.Join(err, replayErr))
		}
		if !found {
			if captureErrorCode(err) == "CAPTURE_COMMIT_FAILED" || captureErrorCode(err) == "" {
				cancel()
				return CreateResult{}, contentFinalizationUnknown(err)
			}
			discardErr := service.dependencies.Content.DiscardManagedBytes(recoveryCtx, command.WorkspaceID, stage)
			cancel()
			return CreateResult{}, errors.Join(err, discardErr)
		}
		validated, validateErr := validateResult(replay, command.WorkspaceID, command.Kind, true)
		if validateErr != nil {
			cancel()
			return CreateResult{}, validateErr
		}
		if validated.Capture.OriginalInputHash != contentHash || validated.Capture.LatestSourceVersionID == "" {
			cancel()
			return CreateResult{}, inconsistent("CAPTURE_RESULT_INVALID", "capture replay returned a different content binding")
		}
		cancel()
		result = validated
	}
	if publishErr := service.publishConfirmedContent(ctx, command.WorkspaceID, stage); publishErr != nil {
		return CreateResult{}, publishErr
	}
	return validateResult(result, command.WorkspaceID, command.Kind, result.Replayed)
}

func (service *Service) stageContent(
	ctx context.Context,
	workspaceID foundation.ID,
	sourceRef string,
	content []byte,
	contentHash string,
) (workspacedomain.ManagedContentStage, error) {
	stage, err := service.dependencies.Content.StageManagedBytes(ctx, workspaceID, sourceRef, content, contentHash)
	if err != nil {
		return workspacedomain.ManagedContentStage{}, err
	}
	if stage.ContentHash != contentHash || stage.ByteSize != int64(len(content)) || strings.TrimSpace(stage.ManagedLocation) == "" {
		return workspacedomain.ManagedContentStage{}, inconsistent("CAPTURE_CONTENT_BINDING_INVALID", "managed content stage differs from the requested bytes")
	}
	return stage, nil
}

func (service *Service) publishConfirmedContent(ctx context.Context, workspaceID foundation.ID, stage workspacedomain.ManagedContentStage) error {
	recoveryCtx, cancel := contentRecoveryContext(ctx)
	published, err := service.dependencies.Content.PublishManagedBytes(recoveryCtx, workspaceID, stage)
	cancel()
	if err != nil {
		return contentFinalizationUnknown(err)
	}
	if published.ContentHash != stage.ContentHash || published.ByteSize != stage.ByteSize || published.ManagedLocation != stage.ManagedLocation {
		return contentFinalizationUnknown(errors.New("managed content publish returned a different binding"))
	}
	return nil
}

func contentRecoveryContext(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.WithoutCancel(ctx), contentFinalizeWait)
}

func contentFinalizationUnknown(cause error) error {
	return foundation.NewError(foundation.ErrorDependencyUnavailable, "CAPTURE_CONTENT_FINALIZATION_UNKNOWN", true, cause)
}

func captureErrorCode(err error) string {
	var classified *foundation.Error
	if errors.As(err, &classified) {
		return classified.Code
	}
	return ""
}

type createHashPayload struct {
	Kind        domain.Kind `json:"kind"`
	DisplayName string      `json:"display_name"`
	URL         string      `json:"url,omitempty"`
	ContentHash string      `json:"content_hash,omitempty"`
	MediaType   string      `json:"media_type,omitempty"`
}

func createBinding(workspaceID foundation.ID, key, commandType string, payload createHashPayload) (CommandBinding, error) {
	encoded, err := json.Marshal(struct {
		Schema      string            `json:"schema"`
		WorkspaceID foundation.ID     `json:"workspace_id"`
		CommandType string            `json:"command_type"`
		Payload     createHashPayload `json:"payload"`
	}{Schema: "capture-create-command/v1", WorkspaceID: workspaceID, CommandType: commandType, Payload: payload})
	if err != nil {
		return CommandBinding{}, inconsistent("CAPTURE_REQUEST_ENCODING_FAILED", "capture request could not be encoded")
	}
	digest := sha256.Sum256(encoded)
	return CommandBinding{WorkspaceID: workspaceID, IdempotencyKey: key, RequestHash: hex.EncodeToString(digest[:]), CommandType: commandType}, nil
}

func materializedStageRef(binding CommandBinding) string {
	digest := sha256.Sum256([]byte(string(binding.WorkspaceID) + "\x00" + binding.IdempotencyKey))
	return "capture-commands/" + hex.EncodeToString(digest[:])
}

func (service *Service) newCreateIDs(materialized bool) (createIDs, error) {
	var result createIDs
	fields := []*foundation.ID{&result.captureID, &result.sourceID}
	if materialized {
		fields = append(fields, &result.artifactID, &result.versionID)
	}
	fields = append(fields, &result.outboxID)
	for _, field := range fields {
		id, err := service.dependencies.IDs.New()
		if err != nil {
			return createIDs{}, err
		}
		*field = id
	}
	return result, nil
}

func sourceFor(capture domain.Capture, now time.Time) workspacedomain.Source {
	return workspacedomain.Source{
		ID: capture.SourceID, WorkspaceID: capture.WorkspaceID, Type: "quick_capture_" + strings.ToLower(string(capture.Kind)),
		LogicalName: capture.DisplayName, OriginalLocation: capture.OriginalLocation, CreatedAt: now,
	}
}

func sourceLocation(captureID foundation.ID) string {
	return "captures/" + string(captureID) + "/input"
}

func normalizeDisplayName(value, fallback string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		value = strings.TrimSpace(fallback)
	}
	if value == "" || !utf8.ValidString(value) || len(value) > 512 || strings.ContainsAny(value, "\r\n\x00") {
		return "", invalid("CAPTURE_DISPLAY_NAME_INVALID", "capture display name is invalid")
	}
	return value, nil
}

func normalizeIdempotencyKey(value string) (string, error) {
	if value == "" || value != strings.TrimSpace(value) || len(value) > maxIdempotencyBytes || strings.ContainsAny(value, "\r\n\x00") {
		return "", invalid("CAPTURE_IDEMPOTENCY_KEY_INVALID", "capture idempotency key is invalid")
	}
	return value, nil
}

func normalizeURL(value string) (string, *url.URL, error) {
	value = strings.TrimSpace(value)
	if value == "" || len(value) > 8192 || strings.ContainsAny(value, "\r\n\x00") {
		return "", nil, invalid("CAPTURE_URL_INVALID", "capture URL is invalid")
	}
	parsed, err := url.Parse(value)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" || parsed.User != nil {
		return "", nil, invalid("CAPTURE_URL_INVALID", "capture URL must be an HTTP(S) URL without credentials")
	}
	return value, parsed, nil
}

func normalizeMediaType(kind domain.Kind, value string) (string, error) {
	parsed, _, err := mime.ParseMediaType(strings.TrimSpace(value))
	if err != nil {
		return "", invalid("CAPTURE_MEDIA_TYPE_INVALID", "capture media type is invalid")
	}
	parsed = strings.ToLower(parsed)
	allowedFiles := map[string]struct{}{
		"text/plain": {}, "text/markdown": {}, "text/html": {}, "application/pdf": {},
	}
	allowedImages := map[string]struct{}{
		"image/png": {}, "image/jpeg": {}, "image/webp": {}, "image/gif": {},
	}
	allowed := allowedFiles
	if kind == domain.KindImage {
		allowed = allowedImages
	}
	if _, found := allowed[parsed]; !found {
		return "", invalid("CAPTURE_MEDIA_TYPE_UNSUPPORTED", "capture media type is unsupported")
	}
	return parsed, nil
}

func safeFileName(value string) string {
	value = strings.TrimSpace(value)
	if value == "" || !utf8.ValidString(value) || strings.ContainsAny(value, "\r\n\x00") {
		return ""
	}
	value = filepath.Base(strings.ReplaceAll(value, "\\", "/"))
	if value == "." || value == ".." || len(value) > 512 {
		return ""
	}
	return value
}

func validateResult(result CreateResult, workspaceID foundation.ID, kind domain.Kind, replayed bool) (CreateResult, error) {
	if err := result.Capture.Validate(); err != nil || result.Capture.WorkspaceID != workspaceID || result.Capture.Kind != kind || result.Replayed != replayed {
		return CreateResult{}, inconsistent("CAPTURE_RESULT_INVALID", "capture repository returned an invalid result")
	}
	return result, nil
}

func (service *Service) now() (time.Time, error) {
	now := service.dependencies.Clock.Now().UTC()
	if now.IsZero() {
		return time.Time{}, inconsistent("CAPTURE_CLOCK_INVALID", "capture clock returned zero time")
	}
	return now, nil
}

func validID(value foundation.ID) bool {
	_, err := foundation.ParseID(string(value))
	return err == nil
}

func contextDone(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return foundation.NewError(foundation.ErrorDependencyUnavailable, "CAPTURE_CONTEXT_DONE", false, err)
	}
	return nil
}

func invalid(code, message string) error {
	return foundation.NewError(foundation.ErrorInvalidInput, code, false, errors.New(message))
}

func unavailable(message string) error {
	return foundation.NewError(foundation.ErrorDependencyUnavailable, "CAPTURE_SERVICE_UNAVAILABLE", true, errors.New(message))
}

func inconsistent(code, message string) error {
	return foundation.NewError(foundation.ErrorConsistencyViolation, code, false, errors.New(message))
}
