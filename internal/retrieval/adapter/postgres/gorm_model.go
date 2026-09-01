package postgres

import (
	"database/sql/driver"
	"encoding/json"
	"errors"
	"fmt"
	"slices"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	"github.com/CodeZen-Lizhi/zhixu/internal/retrieval/domain"
	"github.com/pgvector/pgvector-go"
)

// gormRetrievalJSONB binds validated JSON as text so database/sql does not
// infer bytea for PostgreSQL JSONB parameters.
type gormRetrievalJSONB []byte

func (value gormRetrievalJSONB) Value() (driver.Value, error) {
	if len(value) == 0 || !json.Valid(value) {
		return nil, errors.New("retrieval JSONB value is invalid")
	}
	return string(value), nil
}

func (value *gormRetrievalJSONB) Scan(source any) error {
	if value == nil {
		return errors.New("retrieval JSONB destination is nil")
	}
	var raw []byte
	switch typed := source.(type) {
	case string:
		raw = []byte(typed)
	case []byte:
		raw = append([]byte(nil), typed...)
	default:
		return fmt.Errorf("retrieval JSONB source has unsupported type %T", source)
	}
	if len(raw) == 0 || !json.Valid(raw) {
		return errors.New("persisted retrieval JSONB value is invalid")
	}
	*value = gormRetrievalJSONB(raw)
	return nil
}

type gormRetrievalNullableVector struct {
	values []float32
	valid  bool
}

func (value *gormRetrievalNullableVector) Scan(source any) error {
	if value == nil {
		return errors.New("retrieval vector destination is nil")
	}
	if source == nil {
		value.values = nil
		value.valid = false
		return nil
	}
	var vector pgvector.Vector
	if err := vector.Scan(source); err != nil {
		return err
	}
	value.values = append(value.values[:0], vector.Slice()...)
	value.valid = true
	return nil
}

func scanGORMRetrievalEmbedding(row retrievalRowScanner) (domain.EmbeddingVersion, error) {
	value, err := scanEmbedding(row)
	if err != nil {
		return domain.EmbeddingVersion{}, err
	}
	value.CreatedAt = value.CreatedAt.UTC()
	if err := domain.ValidateEmbeddingVersion(value); err != nil {
		return domain.EmbeddingVersion{}, consistency("RETRIEVAL_EMBEDDING_DATA_INVALID", err)
	}
	return value, nil
}

func scanGORMRetrievalIndex(row retrievalRowScanner) (domain.IndexVersion, error) {
	value, err := scanIndex(row)
	if err != nil {
		return domain.IndexVersion{}, err
	}
	value.FusionConfig = append(json.RawMessage(nil), value.FusionConfig...)
	value.DegradedCapabilities = append([]domain.DegradedCapability(nil), value.DegradedCapabilities...)
	value.CreatedAt = value.CreatedAt.UTC()
	value.UpdatedAt = value.UpdatedAt.UTC()
	if err := domain.ValidateIndexVersion(value); err != nil {
		return domain.IndexVersion{}, consistency("RETRIEVAL_INDEX_DATA_INVALID", err)
	}
	return value, nil
}

func scanGORMRetrievalActivation(row retrievalRowScanner) (domain.Activation, error) {
	var value domain.Activation
	var previousID *string
	err := row.Scan(
		&value.ID, &value.Kind, &value.WorkspaceID, &value.TargetIndexVersionID, &previousID,
		&value.TargetVersion, &value.PreviousVersion, &value.IdempotencyKey, &value.ReasonCode, &value.CreatedAt,
	)
	if err != nil {
		return domain.Activation{}, err
	}
	if previousID != nil {
		id := foundation.ID(*previousID)
		value.PreviousIndexVersionID = &id
	}
	value.CreatedAt = value.CreatedAt.UTC()
	if err := validateGORMRetrievalActivation(value); err != nil {
		return domain.Activation{}, err
	}
	return value, nil
}

func validateGORMRetrievalActivation(value domain.Activation) error {
	if (value.PreviousIndexVersionID == nil) != (value.PreviousVersion == nil) {
		return consistency("RETRIEVAL_ACTIVATION_DATA_INVALID", errors.New("persisted activation previous binding is incomplete"))
	}
	if value.TargetVersion <= 1 || value.PreviousVersion != nil && *value.PreviousVersion <= 1 {
		return consistency("RETRIEVAL_ACTIVATION_DATA_INVALID", errors.New("persisted activation versions are invalid"))
	}
	expectedTargetVersion := value.TargetVersion - 1
	switch value.Kind {
	case domain.ActivationKindActivate:
		command := domain.ActivationCommand{
			ActivationID: value.ID, WorkspaceID: value.WorkspaceID, TargetIndexVersionID: value.TargetIndexVersionID,
			ExpectedTargetVersion: expectedTargetVersion, ExpectedCurrentIndexVersionID: value.PreviousIndexVersionID,
			IdempotencyKey: value.IdempotencyKey, ReasonCode: value.ReasonCode, At: value.CreatedAt,
		}
		if value.PreviousVersion != nil {
			expectedPreviousVersion := *value.PreviousVersion - 1
			command.ExpectedCurrentVersion = &expectedPreviousVersion
		}
		if err := domain.ValidateActivationCommandInput(command); err != nil {
			return consistency("RETRIEVAL_ACTIVATION_DATA_INVALID", err)
		}
		if value.PreviousIndexVersionID != nil && *value.PreviousIndexVersionID == value.TargetIndexVersionID {
			return consistency("RETRIEVAL_ACTIVATION_DATA_INVALID", errors.New("persisted activation binding is invalid"))
		}
	case domain.ActivationKindRollback:
		if value.PreviousIndexVersionID == nil || value.PreviousVersion == nil {
			return consistency("RETRIEVAL_ACTIVATION_DATA_INVALID", errors.New("persisted rollback binding is incomplete"))
		}
		if err := domain.ValidateRollbackActivationCommandInput(domain.RollbackActivationCommand{
			ActivationID: value.ID, WorkspaceID: value.WorkspaceID, TargetIndexVersionID: value.TargetIndexVersionID,
			ExpectedTargetVersion: expectedTargetVersion, ExpectedCurrentIndexVersionID: *value.PreviousIndexVersionID,
			ExpectedCurrentVersion: *value.PreviousVersion - 1, IdempotencyKey: value.IdempotencyKey,
			ReasonCode: value.ReasonCode, At: value.CreatedAt,
		}); err != nil {
			return consistency("RETRIEVAL_ACTIVATION_DATA_INVALID", err)
		}
	default:
		return consistency("RETRIEVAL_ACTIVATION_DATA_INVALID", errors.New("persisted activation kind is invalid"))
	}
	return nil
}

func scanGORMRetrievalBuildStatus(row retrievalRowScanner, invalidCode string) (domain.BuildStatus, error) {
	var status domain.BuildStatus
	var sourceManifestHash *string
	var expectedSourceCount *int64
	var degraded gormRetrievalJSONB
	err := row.Scan(
		&status.WorkspaceID, &status.IndexVersionID, &status.IndexStatus, &status.IndexVersion, &status.ExpectedChunkCount,
		&sourceManifestHash, &expectedSourceCount, &degraded, &status.ManifestChunkCount,
		&status.SourceManifestCount, &status.IncludedSourceCount, &status.ExcludedSourceCount,
		&status.ProjectionCount, &status.LexicalPendingCount, &status.LexicalReadyCount, &status.LexicalFailedCount,
		&status.VectorDisabledCount, &status.VectorPendingCount, &status.VectorReadyCount,
		&status.VectorSkippedOversizedCount, &status.VectorFailedCount,
	)
	if err != nil {
		return domain.BuildStatus{}, err
	}
	if sourceManifestHash != nil {
		status.SourceManifestHash = *sourceManifestHash
	}
	status.ExpectedSourceCount = expectedSourceCount
	if err := json.Unmarshal(degraded, &status.DegradedCapabilities); err != nil {
		return domain.BuildStatus{}, consistency(invalidCode, err)
	}
	normalized, err := domain.NormalizeDegradedCapabilities(status.DegradedCapabilities)
	if err != nil || !slices.Equal(normalized, status.DegradedCapabilities) || !validGORMRetrievalBuildStatus(status) {
		return domain.BuildStatus{}, consistency(invalidCode, errors.New("persisted retrieval build status is invalid"))
	}
	return status, nil
}

func validGORMRetrievalBuildStatus(status domain.BuildStatus) bool {
	counts := []int64{
		status.ExpectedChunkCount, status.ManifestChunkCount, status.SourceManifestCount,
		status.IncludedSourceCount, status.ExcludedSourceCount, status.ProjectionCount,
		status.LexicalPendingCount, status.LexicalReadyCount, status.LexicalFailedCount,
		status.VectorDisabledCount, status.VectorPendingCount, status.VectorReadyCount,
		status.VectorSkippedOversizedCount, status.VectorFailedCount,
	}
	for _, count := range counts {
		if count < 0 {
			return false
		}
	}
	return status.WorkspaceID != "" && status.IndexVersionID != "" &&
		domain.IsValidIndexStatus(status.IndexStatus) && status.IndexVersion > 0 &&
		(status.SourceManifestHash == "") == (status.ExpectedSourceCount == nil) &&
		(status.ExpectedSourceCount == nil || *status.ExpectedSourceCount > 0)
}

func marshalGORMRetrievalCapabilities(capabilities []domain.DegradedCapability) (gormRetrievalJSONB, error) {
	if len(capabilities) == 0 {
		return gormRetrievalJSONB("[]"), nil
	}
	encoded, err := json.Marshal(capabilities)
	if err != nil {
		return nil, err
	}
	return gormRetrievalJSONB(encoded), nil
}

func gormRetrievalNullableInt64(value *int64) any {
	if value == nil {
		return nil
	}
	return *value
}

func gormRetrievalNullableString(value string) any {
	if value == "" {
		return nil
	}
	return value
}

func gormRetrievalOptionalID(value *foundation.ID) any {
	if value == nil {
		return nil
	}
	return string(*value)
}

func gormRetrievalSameOptionalID(left, right *foundation.ID) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return *left == *right
}

func gormRetrievalSameOptionalInt64(left, right *int64) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return *left == *right
}
