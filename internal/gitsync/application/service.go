package application

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"unicode/utf8"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	"github.com/CodeZen-Lizhi/zhixu/internal/gitsync/domain"
)

// Service 是 HTTP 与 Worker 共享的 Git 同步应用门面。
type Service struct {
	configs ConfigStore
	runs    RunStore
	policy  URLPolicy
	remote  RemoteRepository
	ids     foundation.IDGenerator
	clock   Clock
	cursors *CursorCodec
}

// NewService 创建不自行连接数据库或执行 Git 的应用服务。
func NewService(configs ConfigStore, runs RunStore, policy URLPolicy, remote RemoteRepository, ids foundation.IDGenerator, clock Clock, cursors *CursorCodec) (*Service, error) {
	if nilInterface(configs) || nilInterface(runs) || nilInterface(policy) || nilInterface(remote) || nilInterface(ids) || nilInterface(clock) || cursors == nil {
		return nil, unavailable("Git sync service dependency is unavailable")
	}
	return &Service{configs: configs, runs: runs, policy: policy, remote: remote, ids: ids, clock: clock, cursors: cursors}, nil
}

// GetConfig 返回一个 Workspace 的非密钥配置快照。
func (service *Service) GetConfig(ctx context.Context, workspaceID foundation.ID) (domain.RemoteConfig, error) {
	if service == nil || nilInterface(service.configs) {
		return domain.RemoteConfig{}, unavailable("Git sync configuration service is unavailable")
	}
	if !validID(workspaceID) {
		return domain.RemoteConfig{}, invalid(domain.ErrorCodeInvalid, "Workspace identity is invalid")
	}
	return service.configs.GetConfig(ctx, workspaceID)
}

// SaveConfig 规范化 URL 后以 CAS 和幂等键原子保存配置与密文。
func (service *Service) SaveConfig(ctx context.Context, command SaveConfigCommand) (ConfigReceipt, error) {
	if service == nil || nilInterface(service.configs) || nilInterface(service.policy) {
		return ConfigReceipt{}, unavailable("Git sync configuration service is unavailable")
	}
	defer command.SecretAction.Value.Destroy()
	if !validID(command.WorkspaceID) || command.ExpectedRevision < 0 || !validRemoteInput(command.RemoteURL) ||
		!validText(command.IdempotencyKey, 128) || !validText(command.Actor, 256) {
		return ConfigReceipt{}, invalid(domain.ErrorCodeInvalid, "Git remote configuration command is invalid")
	}
	if err := domain.ValidateBranch(command.Branch); err != nil {
		return ConfigReceipt{}, err
	}
	if err := command.SecretAction.Validate(); err != nil {
		return ConfigReceipt{}, err
	}
	requestHash, err := hashCommand(struct {
		WorkspaceID      foundation.ID           `json:"workspace_id"`
		ExpectedRevision int64                   `json:"expected_revision"`
		RemoteURL        string                  `json:"remote_url"`
		Branch           string                  `json:"branch"`
		AutoSync         bool                    `json:"auto_sync"`
		SecretAction     domain.SecretActionKind `json:"secret_action"`
		TokenDigest      string                  `json:"token_digest"`
	}{
		WorkspaceID: command.WorkspaceID, ExpectedRevision: command.ExpectedRevision, RemoteURL: command.RemoteURL,
		Branch: command.Branch, AutoSync: command.AutoSync, SecretAction: command.SecretAction.Kind,
		TokenDigest: tokenDigest(command.SecretAction.Value),
	})
	if err != nil {
		return ConfigReceipt{}, unavailable("Git remote configuration hashing failed")
	}
	if receipt, found, replayErr := service.configs.ReplayConfig(ctx, ReplayConfigCommand{
		WorkspaceID: command.WorkspaceID, IdempotencyKey: command.IdempotencyKey,
		RequestHash: requestHash, CommandType: "SAVE",
	}); replayErr != nil || found {
		return receipt, replayErr
	}
	normalizedURL, err := service.policy.NormalizeHTTPSRemote(ctx, command.RemoteURL)
	if err != nil {
		return ConfigReceipt{}, err
	}
	return service.configs.SaveConfig(ctx, PersistConfigCommand{
		WorkspaceID: command.WorkspaceID, ExpectedRevision: command.ExpectedRevision, RemoteURL: normalizedURL,
		Branch: command.Branch, AutoSync: command.AutoSync, SecretAction: command.SecretAction,
		IdempotencyKey: command.IdempotencyKey, RequestHash: requestHash, Actor: command.Actor,
	})
}

// RemoveConfig 以 CAS 删除活动配置并清除凭据。
func (service *Service) RemoveConfig(ctx context.Context, command RemoveConfigCommand) (ConfigReceipt, error) {
	if service == nil || nilInterface(service.configs) {
		return ConfigReceipt{}, unavailable("Git sync configuration service is unavailable")
	}
	if !validID(command.WorkspaceID) || command.ExpectedRevision < 1 || !validText(command.IdempotencyKey, 128) || !validText(command.Actor, 256) {
		return ConfigReceipt{}, invalid(domain.ErrorCodeInvalid, "Git remote deletion command is invalid")
	}
	requestHash, err := hashCommand(struct {
		WorkspaceID      foundation.ID `json:"workspace_id"`
		ExpectedRevision int64         `json:"expected_revision"`
	}{WorkspaceID: command.WorkspaceID, ExpectedRevision: command.ExpectedRevision})
	if err != nil {
		return ConfigReceipt{}, unavailable("Git remote deletion hashing failed")
	}
	return service.configs.DeleteConfig(ctx, DeleteConfigCommand{
		WorkspaceID: command.WorkspaceID, ExpectedRevision: command.ExpectedRevision,
		IdempotencyKey: command.IdempotencyKey, RequestHash: requestHash, Actor: command.Actor,
	})
}

// TestConfig 解析一个不落库的配置草案并销毁短生命周期凭据。
func (service *Service) TestConfig(ctx context.Context, command TestConfigCommand) (TestConfigResult, error) {
	if service == nil || nilInterface(service.configs) || nilInterface(service.policy) || nilInterface(service.remote) {
		return TestConfigResult{}, unavailable("Git sync connection test is unavailable")
	}
	defer command.SecretAction.Value.Destroy()
	if !validID(command.WorkspaceID) || command.ExpectedRevision < 0 || !validRemoteInput(command.RemoteURL) {
		return TestConfigResult{}, invalid(domain.ErrorCodeInvalid, "Git remote test command is invalid")
	}
	if err := domain.ValidateBranch(command.Branch); err != nil {
		return TestConfigResult{}, err
	}
	if err := command.SecretAction.Validate(); err != nil {
		return TestConfigResult{}, err
	}
	normalizedURL, err := service.policy.NormalizeHTTPSRemote(ctx, command.RemoteURL)
	if err != nil {
		return TestConfigResult{}, err
	}
	current, err := service.configs.GetConfig(ctx, command.WorkspaceID)
	if err != nil {
		return TestConfigResult{}, err
	}
	if current.Revision != command.ExpectedRevision {
		return TestConfigResult{}, versionConflict(domain.ErrorCodeConfigRevisionConflict, "Git remote configuration changed")
	}
	var token domain.Token
	switch command.SecretAction.Kind {
	case domain.SecretActionKeep:
		if !current.CredentialBindingMatches(normalizedURL, command.Branch) {
			return TestConfigResult{}, versionConflict(domain.ErrorCodeConfigRevisionConflict, "a kept token cannot be rebound to another remote")
		}
		token, err = service.configs.OpenCredential(ctx, command.WorkspaceID, current.Revision)
	case domain.SecretActionReplace:
		tokenBytes := command.SecretAction.Value.Bytes()
		defer clear(tokenBytes)
		token, err = domain.TokenFromBytes(tokenBytes)
	case domain.SecretActionClear:
		return TestConfigResult{}, invalid(domain.ErrorCodeSecretActionInvalid, "connection test requires a token")
	default:
		return TestConfigResult{}, invalid(domain.ErrorCodeSecretActionInvalid, "remote token action is invalid")
	}
	if err != nil {
		return TestConfigResult{}, err
	}
	defer token.Destroy()
	testRevision := command.ExpectedRevision
	if testRevision == 0 {
		// A first-time connection test has no persisted revision yet. The remote
		// adapter still requires a positive, request-local credential binding.
		testRevision = 1
	}
	if err := service.remote.TestConnection(ctx, GitAccess{
		WorkspaceID: command.WorkspaceID, ConfigRevision: testRevision,
		RemoteURL: normalizedURL, Branch: command.Branch, Token: token,
	}); err != nil {
		return TestConfigResult{}, err
	}
	return TestConfigResult{RemoteURL: normalizedURL, Branch: command.Branch}, nil
}

// CreateRun 创建或精确重放一个持久同步运行。
func (service *Service) CreateRun(ctx context.Context, command CreateRunCommand) (RunReceipt, error) {
	if service == nil || nilInterface(service.configs) || nilInterface(service.runs) || nilInterface(service.ids) || nilInterface(service.clock) {
		return RunReceipt{}, unavailable("Git sync run service is unavailable")
	}
	if !validID(command.WorkspaceID) || !validText(command.IdempotencyKey, 128) ||
		(command.Trigger != domain.TriggerManual && command.Trigger != domain.TriggerAutomatic && command.Trigger != domain.TriggerRetry) {
		return RunReceipt{}, invalid(domain.ErrorCodeInvalid, "Git sync run command is invalid")
	}
	if command.Trigger == domain.TriggerRetry {
		if !validID(command.RetryOfRunID) {
			return RunReceipt{}, invalid(domain.ErrorCodeInvalid, "Git sync retry binding is invalid")
		}
	} else if command.RetryOfRunID != "" {
		return RunReceipt{}, invalid(domain.ErrorCodeInvalid, "non-retry run has a retry binding")
	}
	if command.Trigger == domain.TriggerAutomatic {
		if command.ExpectedConfigRevision < 1 {
			return RunReceipt{}, invalid(domain.ErrorCodeInvalid, "automatic Git sync requires an expected configuration revision")
		}
	} else if command.ExpectedConfigRevision != 0 {
		return RunReceipt{}, invalid(domain.ErrorCodeInvalid, "non-automatic Git sync has an expected configuration revision")
	}
	requestHash, err := hashCommand(struct {
		WorkspaceID            foundation.ID  `json:"workspace_id"`
		Trigger                domain.Trigger `json:"trigger"`
		RetryOfRunID           foundation.ID  `json:"retry_of_run_id,omitempty"`
		ExpectedConfigRevision int64          `json:"expected_config_revision,omitempty"`
	}{
		WorkspaceID: command.WorkspaceID, Trigger: command.Trigger, RetryOfRunID: command.RetryOfRunID,
		ExpectedConfigRevision: command.ExpectedConfigRevision,
	})
	if err != nil {
		return RunReceipt{}, unavailable("Git sync run hashing failed")
	}
	return service.createRun(ctx, command, requestHash)
}

func (service *Service) createRun(ctx context.Context, command CreateRunCommand, requestHash string) (RunReceipt, error) {
	if service == nil || nilInterface(service.configs) || nilInterface(service.runs) || nilInterface(service.ids) || nilInterface(service.clock) || len(requestHash) != 64 {
		return RunReceipt{}, unavailable("Git sync run service is unavailable")
	}
	if replayed, found, replayErr := service.runs.ReplayRun(ctx, ReplayRunCommand{
		WorkspaceID: command.WorkspaceID, IdempotencyKey: command.IdempotencyKey,
		RequestHash: requestHash, Trigger: command.Trigger, RetryOfRunID: command.RetryOfRunID,
	}); replayErr != nil || found {
		return RunReceipt{Run: replayed, Replayed: found}, replayErr
	}
	config, err := service.configs.GetConfig(ctx, command.WorkspaceID)
	if err != nil {
		return RunReceipt{}, err
	}
	if err := config.ValidateConfigured(); err != nil {
		return RunReceipt{}, err
	}
	if command.Trigger == domain.TriggerAutomatic && config.Revision != command.ExpectedConfigRevision {
		return RunReceipt{}, versionConflict(domain.ErrorCodeConfigStale, "Git remote configuration changed before automatic run creation")
	}
	if command.Trigger == domain.TriggerAutomatic && !config.AutoSync {
		return RunReceipt{}, versionConflict(domain.ErrorCodeAutoSyncDisabled, "automatic Git sync is disabled")
	}
	runID, err := service.ids.New()
	if err != nil {
		return RunReceipt{}, err
	}
	now := service.clock.Now().UTC()
	run := domain.SyncRun{
		ID: runID, WorkspaceID: command.WorkspaceID, ConfigRevision: config.Revision,
		RemoteURL: config.RemoteURL, Branch: config.Branch, Trigger: command.Trigger, RetryOfRunID: command.RetryOfRunID,
		IdempotencyKey: command.IdempotencyKey, RequestHash: requestHash, Status: domain.RunPending,
		Direction: domain.DirectionUnknown, FailureClass: domain.FailureNone, IndexStatus: domain.IndexNotRequired,
		Version: 1, CreatedAt: now, UpdatedAt: now,
	}
	created, replayed, err := service.runs.CreateRun(ctx, CreateRunRecord{Run: run})
	if err != nil {
		return RunReceipt{}, err
	}
	return RunReceipt{Run: created, Replayed: replayed}, nil
}

// RetryRun 对索引失败仅重投 follow-up，其余终态创建一个新 Git Run。
func (service *Service) RetryRun(ctx context.Context, command RetryRunCommand) (RunReceipt, error) {
	if service == nil || nilInterface(service.runs) {
		return RunReceipt{}, unavailable("Git sync retry service is unavailable")
	}
	if !validID(command.WorkspaceID) || !validID(command.RunID) || command.ExpectedVersion < 1 || !validText(command.IdempotencyKey, 128) {
		return RunReceipt{}, invalid(domain.ErrorCodeInvalid, "Git sync retry command is invalid")
	}
	requestHash, err := hashCommand(struct {
		WorkspaceID foundation.ID `json:"workspace_id"`
		RunID       foundation.ID `json:"run_id"`
		Version     int64         `json:"version"`
	}{WorkspaceID: command.WorkspaceID, RunID: command.RunID, Version: command.ExpectedVersion})
	if err != nil {
		return RunReceipt{}, unavailable("Git sync index retry hashing failed")
	}
	indexRetry := RetryIndexCommand{
		WorkspaceID: command.WorkspaceID, RunID: command.RunID, ExpectedVersion: command.ExpectedVersion,
		IdempotencyKey: command.IdempotencyKey, RequestHash: requestHash,
	}
	if replayed, found, replayErr := service.runs.ReplayIndex(ctx, indexRetry); replayErr != nil || found {
		return RunReceipt{Run: replayed, Replayed: found}, replayErr
	}
	gitRetry := ReplayRunCommand{
		WorkspaceID: command.WorkspaceID, IdempotencyKey: command.IdempotencyKey,
		RequestHash: requestHash, Trigger: domain.TriggerRetry, RetryOfRunID: command.RunID,
	}
	if replayed, found, replayErr := service.runs.ReplayRun(ctx, gitRetry); replayErr != nil || found {
		return RunReceipt{Run: replayed, Replayed: found}, replayErr
	}
	run, err := service.runs.GetRun(ctx, command.WorkspaceID, command.RunID)
	if err != nil {
		return RunReceipt{}, err
	}
	if run.Version != command.ExpectedVersion {
		return RunReceipt{}, versionConflict(domain.ErrorCodeRunTransition, "Git sync retry version changed")
	}
	if run.Status == domain.RunSucceeded && run.IndexStatus == domain.IndexFailed {
		updated, replayed, retryErr := service.runs.RetryIndex(ctx, indexRetry)
		return RunReceipt{Run: updated, Replayed: replayed}, retryErr
	}
	if run.Status == domain.RunSucceeded {
		return RunReceipt{}, versionConflict(domain.ErrorCodeRunTransition, "successful Git sync run cannot be retried")
	}
	if !run.Status.Terminal() {
		return RunReceipt{}, versionConflict(domain.ErrorCodeRunTransition, "active Git sync run cannot be retried")
	}
	return service.createRun(ctx, CreateRunCommand{
		WorkspaceID: command.WorkspaceID, Trigger: domain.TriggerRetry,
		RetryOfRunID: command.RunID, IdempotencyKey: command.IdempotencyKey,
	}, requestHash)
}

// GetRun 返回一个 Workspace-scoped 同步运行。
func (service *Service) GetRun(ctx context.Context, workspaceID, runID foundation.ID) (domain.SyncRun, error) {
	if service == nil || nilInterface(service.runs) {
		return domain.SyncRun{}, unavailable("Git sync run service is unavailable")
	}
	if !validID(workspaceID) || !validID(runID) {
		return domain.SyncRun{}, invalid(domain.ErrorCodeInvalid, "Git sync run identity is invalid")
	}
	return service.runs.GetRun(ctx, workspaceID, runID)
}

// GetStatus 返回配置和最近运行，二者都来自持久事实。
func (service *Service) GetStatus(ctx context.Context, workspaceID foundation.ID) (StatusSnapshot, error) {
	if service == nil || nilInterface(service.runs) {
		return StatusSnapshot{}, unavailable("Git sync status service is unavailable")
	}
	config, err := service.GetConfig(ctx, workspaceID)
	if err != nil {
		return StatusSnapshot{}, err
	}
	run, found, err := service.runs.GetCurrentRun(ctx, workspaceID)
	if err != nil {
		return StatusSnapshot{}, err
	}
	result := StatusSnapshot{Config: config}
	if found {
		result.CurrentRun = &run
	}
	return result, nil
}

// ListRuns 返回 Workspace-bound 有界运行页。
func (service *Service) ListRuns(ctx context.Context, command ListRunsCommand) (PublicRunPage, error) {
	if service == nil || nilInterface(service.runs) || service.cursors == nil || !validID(command.WorkspaceID) ||
		command.Limit < 1 || command.Limit > domain.MaxListLimit {
		return PublicRunPage{}, invalid(domain.ErrorCodeInvalid, "Git sync run list is invalid")
	}
	query := ListRunsQuery{WorkspaceID: command.WorkspaceID, Limit: command.Limit}
	if command.Cursor != "" {
		cursor, err := service.cursors.decode(command.Cursor)
		if err != nil {
			return PublicRunPage{}, err
		}
		if cursor.WorkspaceID != command.WorkspaceID || cursor.Limit != command.Limit {
			return PublicRunPage{}, invalid(domain.ErrorCodeInvalid, "Git sync cursor binding is invalid")
		}
		query.BeforeTime, query.BeforeID = cursor.BeforeTime, cursor.BeforeID
	}
	page, err := service.runs.ListRuns(ctx, query)
	if err != nil {
		return PublicRunPage{}, err
	}
	result := PublicRunPage{Items: page.Items}
	if !page.NextTime.IsZero() {
		result.NextCursor, err = service.cursors.encode(runCursorPayload{
			Version: runCursorVersion, WorkspaceID: command.WorkspaceID, Limit: command.Limit,
			BeforeTime: page.NextTime, BeforeID: page.NextID,
		})
		if err != nil {
			return PublicRunPage{}, err
		}
	}
	return result, nil
}

func tokenDigest(token domain.Token) string {
	if !token.Configured() {
		return ""
	}
	value := token.Bytes()
	defer clear(value)
	digest := sha256.Sum256(value)
	return hex.EncodeToString(digest[:])
}

func hashCommand(value any) (string, error) {
	encoded, err := json.Marshal(value)
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(encoded)
	return hex.EncodeToString(digest[:]), nil
}

func validID(value foundation.ID) bool {
	parsed, err := foundation.ParseID(string(value))
	return err == nil && parsed == value
}

func validRemoteInput(value string) bool {
	return value != "" && len(value) <= 2048 && utf8.ValidString(value)
}

func errorCode(err error) (string, bool) {
	var classified *foundation.Error
	if !errors.As(err, &classified) {
		return "", false
	}
	return classified.Code, true
}
