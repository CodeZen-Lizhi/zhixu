package postgres

import (
	"errors"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	"github.com/CodeZen-Lizhi/zhixu/internal/retrieval/domain"
)

func snapshotRegressionFailureCode(regressionCode string) string {
	if regressionCode == domain.SnapshotStructureRegressionV2 {
		return domain.ErrorCodeSnapshotStructureV2RegressionFailed
	}
	return domain.ErrorCodeSnapshotStructureRegressionFailed
}

func optionalRegressionID(value *string) foundation.ID {
	if value == nil {
		return ""
	}
	return foundation.ID(*value)
}

func isSnapshotRegressionFailure(err error) bool {
	var classified *foundation.Error
	return errors.As(err, &classified) &&
		(classified.Code == domain.ErrorCodeSnapshotStructureRegressionFailed || classified.Code == domain.ErrorCodeSnapshotStructureV2RegressionFailed)
}
