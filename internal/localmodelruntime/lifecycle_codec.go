package localmodelruntime

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
)

const runtimeColumns = `mode,owner_id::text,owner_epoch,heartbeat_at,lease_expires_at,requirement_version,requirement_hash,required_models,observed_phase,ready_requirement_hash,child_epoch,last_error_code,last_error_retryable,version,updated_at`
const runtimeSelect = `SELECT ` + runtimeColumns + ` FROM ops.managed_ollama_runtime`
const holdColumns = `hold_id::text,owner_kind,owner_id::text,owner_epoch,role,instance_id::text,revision,rollout_id::text,operation_id::text,requirement_hash,models,lease_expires_at,version,released_at,created_at,updated_at`
const operationColumns = `operation_id::text,kind,idempotency_key,request_hash,target_revision,rollout_id::text,requirement_hash,required_models,phase,claim_owner_id::text,claim_owner_epoch,claim_expires_at,version,completed_models,resolved_models,completed_bytes,total_bytes,progress_known,attempt_no,last_progress_at,error_code,error_retryable,created_at,updated_at,terminal_at`

func scanRuntime(row interface{ Scan(...any) error }) (RuntimeRecord, error) {
	var r RuntimeRecord
	var owner, errCode *string
	var heartbeatAt, expiryAt *time.Time
	var rawModels []byte
	if err := row.Scan(&r.Mode, &owner, &r.OwnerEpoch, &heartbeatAt, &expiryAt,
		&r.RequirementVersion, &r.Requirement.Hash, &rawModels, &r.Phase, &r.ReadyHash,
		&r.ChildEpoch, &errCode, &r.Retryable, &r.Version, &r.UpdatedAt); err != nil {
		return RuntimeRecord{}, err
	}
	var refs []ModelRef
	if err := json.Unmarshal(rawModels, &refs); err != nil {
		return RuntimeRecord{}, err
	}
	requirement, err := ParseRequirement(refs, r.Requirement.Hash)
	if err != nil {
		return RuntimeRecord{}, err
	}
	r.Requirement = requirement
	if owner != nil {
		id, parseErr := foundation.ParseID(*owner)
		if parseErr != nil {
			return RuntimeRecord{}, parseErr
		}
		r.OwnerID = &id
	}
	r.HeartbeatAt = heartbeatAt
	r.LeaseExpiresAt = expiryAt
	if errCode != nil {
		r.ErrorCode = *errCode
	}
	return r, nil
}

func scanManagerLease(row interface{ Scan(...any) error }) (ManagerLease, error) {
	var l ManagerLease
	var owner string
	if err := row.Scan(&owner, &l.OwnerEpoch, &l.RequirementVersion, &l.Version, &l.HeartbeatAt, &l.LeaseExpiresAt, &l.Mode); err != nil {
		return ManagerLease{}, err
	}
	id, err := foundation.ParseID(owner)
	if err != nil {
		return ManagerLease{}, err
	}
	l.OwnerID = id
	return l, nil
}

func scanHold(row interface{ Scan(...any) error }) (HoldRecord, error) {
	var h HoldRecord
	var id, owner string
	var instance, rollout, operation, role *string
	var models []byte
	var released *time.Time
	var revision *int64
	if err := row.Scan(&id, &h.Kind, &owner, &h.OwnerEpoch, &role, &instance, &revision, &rollout, &operation, &h.Requirement.Hash, &models, &h.LeaseExpiresAt, &h.Version, &released, &h.CreatedAt, &h.UpdatedAt); err != nil {
		return HoldRecord{}, err
	}
	parsed, err := foundation.ParseID(id)
	if err != nil {
		return HoldRecord{}, err
	}
	h.HoldID = parsed
	ownerID, err := foundation.ParseID(owner)
	if err != nil {
		return HoldRecord{}, err
	}
	h.OwnerID = ownerID
	if role != nil {
		h.Role = *role
	}
	h.Revision = revision
	h.InstanceID = parseOptionalID(instance)
	h.RolloutID = parseOptionalID(rollout)
	h.OperationID = parseOptionalID(operation)
	var refs []ModelRef
	if err := json.Unmarshal(models, &refs); err != nil {
		return HoldRecord{}, err
	}
	h.Requirement, err = ParseRequirement(refs, h.Requirement.Hash)
	if err != nil {
		return HoldRecord{}, err
	}
	h.ReleasedAt = released
	return h, nil
}

func scanOperation(row interface{ Scan(...any) error }) (OperationRecord, error) {
	return scanOperationRow(row, nil)
}

func scanPullAttempt(row interface{ Scan(...any) error }) (PullAttemptResult, error) {
	var remainingMicros int64
	operation, err := scanOperationRow(row, &remainingMicros)
	if err != nil {
		return PullAttemptResult{}, err
	}
	return PullAttemptResult{
		Operation: operation,
		Remaining: time.Duration(remainingMicros) * time.Microsecond,
	}, nil
}

func scanOperationRow(row interface{ Scan(...any) error }, remaining *int64) (OperationRecord, error) {
	var o OperationRecord
	var id string
	var rollout, owner, errorCode *string
	var claimOwnerEpoch *int64
	var models, completed, resolved []byte
	var claimExpiry, lastProgress, terminal *time.Time
	var target *int64
	args := []any{&id, &o.Kind, &o.IdempotencyKey, &o.RequestHash, &target, &rollout,
		&o.Requirement.Hash, &models, &o.Phase, &owner, &claimOwnerEpoch, &claimExpiry,
		&o.Version, &completed, &resolved, &o.CompletedBytes, &o.TotalBytes, &o.ProgressKnown,
		&o.AttemptNo, &lastProgress, &errorCode, &o.Retryable, &o.CreatedAt, &o.UpdatedAt, &terminal}
	if remaining != nil {
		args = append(args, remaining)
	}
	if err := row.Scan(args...); err != nil {
		return OperationRecord{}, err
	}
	parsed, err := foundation.ParseID(id)
	if err != nil {
		return OperationRecord{}, err
	}
	o.OperationID = parsed
	o.TargetRevision = target
	o.RolloutID = parseOptionalID(rollout)
	o.ClaimOwnerID = parseOptionalID(owner)
	if claimOwnerEpoch != nil {
		o.ClaimOwnerEpoch = *claimOwnerEpoch
	}
	if errorCode != nil {
		o.ErrorCode = *errorCode
	}
	o.ClaimExpiresAt = claimExpiry
	o.LastProgressAt = lastProgress
	o.TerminalAt = terminal
	var refs []ModelRef
	if err := json.Unmarshal(models, &refs); err != nil {
		return OperationRecord{}, err
	}
	if err := json.Unmarshal(completed, &o.CompletedModels); err != nil {
		return OperationRecord{}, err
	}
	if err := json.Unmarshal(resolved, &o.ResolvedModels); err != nil {
		return OperationRecord{}, err
	}
	o.Requirement, err = ParseRequirement(refs, o.Requirement.Hash)
	if err != nil {
		return OperationRecord{}, err
	}
	return o, nil
}

func unionRequirements(sources []DemandSource) (Requirement, error) {
	var models []ModelRef
	for _, source := range sources {
		models = append(models, source.Requirement.Models...)
	}
	return NewRequirement(models)
}

func requirementFromRuntime(r RuntimeRecord) (Requirement, error) { return r.Requirement, nil }
func parseOptionalID(value *string) *foundation.ID {
	if value == nil || *value == "" {
		return nil
	}
	id, err := foundation.ParseID(*value)
	if err != nil {
		return nil
	}
	return &id
}

func conflict(message string) error      { return fmt.Errorf("LOCAL_MODEL_RUNTIME_CONFLICT: %s", message) }
func intervalArg(d time.Duration) string { return fmt.Sprintf("%d microseconds", d.Microseconds()) }
func nullableID(id *foundation.ID) any {
	if id == nil {
		return nil
	}
	return string(*id)
}
func nullableInt64(v *int64) any {
	if v == nil {
		return nil
	}
	return *v
}
func nullableText(v string) any {
	if strings.TrimSpace(v) == "" {
		return nil
	}
	return v
}
func nullableErrorCode(v string) any {
	if v == "" {
		return nil
	}
	return v
}

func marshalModelRefs(models []ModelRef) []byte {
	if models == nil {
		models = []ModelRef{}
	}
	encoded, _ := json.Marshal(models)
	return encoded
}

func marshalResolvedModels(models []ResolvedModel) []byte {
	if models == nil {
		models = []ResolvedModel{}
	}
	encoded, _ := json.Marshal(models)
	return encoded
}

func validateManagerClaim(c ManagerClaimCommand) error {
	if !validID(c.OwnerID) || c.LeaseDuration <= 0 || c.LeaseDuration > 10*time.Minute || c.StaleAfter < c.LeaseDuration || c.StaleAfter > 10*time.Minute {
		return errors.New("invalid manager claim")
	}
	return nil
}
func validateManagerHeartbeat(c ManagerHeartbeatCommand) error {
	if !validID(c.OwnerID) || c.OwnerEpoch <= 0 || c.ExpectedVersion <= 0 || c.LeaseDuration <= 0 || c.LeaseDuration > 10*time.Minute {
		return errors.New("invalid manager heartbeat")
	}
	return nil
}
func validateDemandCAS(c DemandCASCommand) error {
	if !validID(c.OwnerID) || c.OwnerEpoch <= 0 || c.ExpectedVersion <= 0 || c.ExpectedRequirementVersion < 0 || c.ExpectedSettingsStateVersion <= 0 {
		return errors.New("invalid demand CAS")
	}
	if _, err := ParseRequirement(c.Requirement.Models, c.Requirement.Hash); err != nil {
		return err
	}
	return nil
}
func validateOperationClaim(c OperationClaimCommand) error {
	if !validID(c.OperationID) || !ValidOperationKind(c.Kind) || !validID(c.OwnerID) || c.OwnerEpoch <= 0 || c.LeaseDuration <= 0 || c.LeaseDuration > time.Hour || strings.TrimSpace(c.IdempotencyKey) != c.IdempotencyKey || c.IdempotencyKey == "" || len(c.IdempotencyKey) > 256 || strings.IndexFunc(c.IdempotencyKey, func(r rune) bool { return r < 0x20 || r == 0x7f }) >= 0 || len(c.RequestHash) != 64 || !isLowerHex(c.RequestHash) {
		return errors.New("invalid operation claim")
	}
	if _, err := ParseRequirement(c.Requirement.Models, c.Requirement.Hash); err != nil {
		return err
	}
	if len(c.Requirement.Models) == 0 {
		return errors.New("operation requirement is empty")
	}
	return nil
}

func validatePullAttempt(c PullAttemptCommand) error {
	if !validID(c.OperationID) || !validID(c.OwnerID) || c.OwnerEpoch <= 0 || c.ExpectedVersion <= 0 || c.LeaseDuration <= 0 || c.LeaseDuration > time.Hour {
		return errors.New("invalid pull attempt")
	}
	return nil
}

func validateOperationExpirySweep(c OperationExpirySweepCommand) error {
	if !validID(c.OwnerID) || c.OwnerEpoch <= 0 {
		return errors.New("invalid operation expiry sweep")
	}
	return nil
}

func validateActiveRecovery(c ActiveRecoveryCommand) error {
	if !validID(c.OwnerID) || c.OwnerEpoch <= 0 || c.ExpectedSettingsStateVersion <= 0 || c.LeaseDuration <= 0 || c.LeaseDuration > time.Hour {
		return errors.New("invalid active recovery command")
	}
	return nil
}

func validateActiveRecoveryCheck(c ActiveRecoveryCheckCommand) error {
	if !validID(c.OperationID) || !validID(c.OwnerID) || c.OwnerEpoch <= 0 {
		return errors.New("invalid active recovery check")
	}
	return nil
}

func validateActivationPreparation(c ActivationPreparationCommand) error {
	if !validID(c.OperationID) || !validID(c.HoldID) || !validID(c.RolloutID) || !validID(c.OwnerID) || c.TargetRevision < 0 || c.OwnerEpoch <= 0 || c.LeaseDuration <= 0 || strings.TrimSpace(c.IdempotencyKey) != c.IdempotencyKey || c.IdempotencyKey == "" || len(c.IdempotencyKey) > 256 || strings.IndexFunc(c.IdempotencyKey, func(r rune) bool { return r < 0x20 || r == 0x7f }) >= 0 || len(c.RequestHash) != 64 || !isLowerHex(c.RequestHash) {
		return errors.New("invalid activation preparation")
	}
	if _, err := ParseRequirement(c.Requirement.Models, c.Requirement.Hash); err != nil {
		return err
	}
	if len(c.Requirement.Models) == 0 {
		return errors.New("activation preparation requirement is empty")
	}
	return nil
}

func validateTestPreparation(c TestPreparationCommand) error {
	if !validID(c.OperationID) || !validID(c.HoldID) || !validID(c.OwnerID) || c.OwnerEpoch <= 0 || c.LeaseDuration <= 0 || strings.TrimSpace(c.IdempotencyKey) != c.IdempotencyKey || c.IdempotencyKey == "" || len(c.IdempotencyKey) > 256 || strings.IndexFunc(c.IdempotencyKey, func(r rune) bool { return r < 0x20 || r == 0x7f }) >= 0 || len(c.RequestHash) != 64 || !isLowerHex(c.RequestHash) {
		return errors.New("invalid test preparation")
	}
	if c.TargetRevision != nil && *c.TargetRevision < 0 {
		return errors.New("invalid test preparation target revision")
	}
	if _, err := ParseRequirement(c.Requirement.Models, c.Requirement.Hash); err != nil {
		return err
	}
	if len(c.Requirement.Models) == 0 {
		return errors.New("test preparation requirement is empty")
	}
	return nil
}
func validateTestProbeClaim(c TestProbeClaimCommand) error {
	if !validID(c.OperationID) || !validID(c.OwnerID) || c.OwnerEpoch <= 0 || c.ExpectedVersion <= 0 || c.LeaseDuration <= 0 {
		return errors.New("invalid test probe claim")
	}
	return nil
}
func validateTestProbeCompletion(c TestProbeCompletionCommand) error {
	if !validID(c.OperationID) || !validID(c.OwnerID) || c.OwnerEpoch <= 0 || c.ExpectedVersion <= 0 {
		return errors.New("invalid test probe completion")
	}
	if c.Abandon {
		if c.ErrorCode != "" || c.Retryable {
			return errors.New("abandoned test probe cannot record a terminal error")
		}
		return nil
	}
	if c.ErrorCode == "" {
		if c.Retryable {
			return errors.New("successful test probe cannot be retryable")
		}
		return nil
	}
	if len(c.ErrorCode) > 128 || strings.IndexFunc(c.ErrorCode, func(r rune) bool {
		return !(r >= 'A' && r <= 'Z') && !(r >= '0' && r <= '9') && r != '_'
	}) >= 0 {
		return errors.New("invalid test probe error code")
	}
	return nil
}

func equalOptionalInt64(left, right *int64) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return *left == *right
}
func validateOperationProgress(c OperationProgressCommand) error {
	if !validID(c.OperationID) || !validID(c.OwnerID) || c.OwnerEpoch <= 0 || c.ExpectedVersion <= 0 || !ValidOperationPhase(c.ExpectedPhase) || !ValidOperationPhase(c.NextPhase) || c.LeaseDuration <= 0 || c.LeaseDuration > time.Hour || c.CompletedBytes < 0 {
		return errors.New("invalid operation progress")
	}
	if c.TotalBytes != nil && *c.TotalBytes < c.CompletedBytes {
		return errors.New("operation progress exceeds total")
	}
	return nil
}
func validateOperationTerminal(c OperationTerminalCommand) error {
	if !validID(c.OperationID) || !validID(c.OwnerID) || c.OwnerEpoch <= 0 || c.ExpectedVersion <= 0 || (c.Phase != OperationPhaseReady && c.Phase != OperationPhaseFailed) || c.LeaseDuration < 0 || c.CompletedBytes < 0 {
		return errors.New("invalid operation terminal")
	}
	if c.TotalBytes != nil && *c.TotalBytes < c.CompletedBytes {
		return errors.New("operation terminal exceeds total")
	}
	return nil
}
func validateHoldAcquire(c HoldAcquireCommand) error {
	if !validID(c.HoldID) || !validID(c.OwnerID) || c.OwnerEpoch <= 0 || !ValidHoldKind(c.Kind) || c.LeaseDuration <= 0 {
		return errors.New("invalid hold acquire")
	}
	if _, err := ParseRequirement(c.Requirement.Models, c.Requirement.Hash); err != nil {
		return err
	}
	if len(c.Requirement.Models) == 0 {
		return errors.New("hold requirement is empty")
	}
	return nil
}
func validateHoldRenew(c HoldRenewCommand) error {
	if !validID(c.HoldID) || !validID(c.OwnerID) || c.OwnerEpoch <= 0 || c.ExpectedVersion <= 0 || c.LeaseDuration <= 0 {
		return errors.New("invalid hold renewal")
	}
	return nil
}
func validateRuntimeCommand(c RuntimePhaseCommand) error {
	if !validID(c.OwnerID) || c.OwnerEpoch <= 0 || c.ExpectedVersion <= 0 || c.ExpectedSettingsStateVersion <= 0 || c.ChildEpoch < 0 || !ValidRuntimePhase(c.ExpectedPhase) || !ValidRuntimePhase(c.NextPhase) {
		return errors.New("invalid runtime phase command")
	}
	if c.RequirementHash != "" && !isLowerHex(c.RequirementHash) {
		return errors.New("invalid runtime requirement hash")
	}
	if c.ReadyHash != "" && !isLowerHex(c.ReadyHash) {
		return errors.New("invalid runtime ready hash")
	}
	if c.NextPhase == RuntimePhaseReady && (c.RequirementHash == "" || c.ReadyHash != c.RequirementHash) {
		return errors.New("ready runtime phase must bind requirement")
	}
	if c.NextPhase != RuntimePhaseReady && c.ReadyHash != "" {
		return errors.New("non-ready runtime phase cannot retain ready hash")
	}
	return nil
}

func isLowerHex(value string) bool {
	if len(value) != 64 {
		return false
	}
	for _, r := range value {
		if !(r >= '0' && r <= '9') && !(r >= 'a' && r <= 'f') {
			return false
		}
	}
	return true
}
