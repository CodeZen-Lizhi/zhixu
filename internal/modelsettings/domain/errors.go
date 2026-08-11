// Package domain defines the model settings revision and rollout contracts.
package domain

const (
	ErrorCodeInvalid                   = "MODEL_SETTINGS_INVALID"
	ErrorCodeUnavailable               = "MODEL_SETTINGS_UNAVAILABLE"
	ErrorCodeCorrupt                   = "MODEL_SETTINGS_CORRUPT"
	ErrorCodeRevisionConflict          = "MODEL_SETTINGS_REVISION_CONFLICT"
	ErrorCodeRolloutInProgress         = "MODEL_SETTINGS_ROLLOUT_IN_PROGRESS"
	ErrorCodeRolloutConflict           = "MODEL_SETTINGS_ROLLOUT_CONFLICT"
	ErrorCodeRolloutLeaseExpired       = "MODEL_SETTINGS_ROLLOUT_LEASE_EXPIRED"
	ErrorCodeRolloutWaitTimeout        = "MODEL_SETTINGS_ROLLOUT_WAIT_TIMEOUT"
	ErrorCodeRuntimeConflict           = "MODEL_SETTINGS_RUNTIME_CONFLICT"
	ErrorCodeRuntimeNotPrepared        = "MODEL_SETTINGS_RUNTIME_NOT_PREPARED"
	ErrorCodeActivationConflict        = "MODEL_SETTINGS_ACTIVATION_CONFLICT"
	ErrorCodeActivationPrepareFailed   = "MODEL_SETTINGS_ACTIVATION_PREPARE_FAILED"
	ErrorCodeActivationLeaseExpired    = "MODEL_SETTINGS_ACTIVATION_LEASE_EXPIRED"
	ErrorCodeParticipantConflict       = "MODEL_SETTINGS_PARTICIPANT_CONFLICT"
	ErrorCodeRuntimeOwnershipLost      = "MODEL_RUNTIME_OWNERSHIP_LOST"
	ErrorCodeRuntimeNotReady           = "MODEL_RUNTIME_NOT_READY"
	ErrorCodeSecretUnavailable         = "MODEL_SETTINGS_SECRET_UNAVAILABLE"
	ErrorCodeSecretTargetChanged       = "MODEL_SETTINGS_SECRET_TARGET_CHANGED"
	ErrorCodeSecretActionInvalid       = "MODEL_SETTINGS_SECRET_ACTION_INVALID"
	ErrorCodeEnqueuePaused             = "MODEL_SETTINGS_ENQUEUE_PAUSED"
	ErrorCodeActiveRevisionUnavailable = "MODEL_SETTINGS_ACTIVE_REVISION_UNAVAILABLE"
	ErrorCodeCommittedQueuePaused      = "MODEL_SETTINGS_COMMITTED_QUEUE_PAUSED"
)
