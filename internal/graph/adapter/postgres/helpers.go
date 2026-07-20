package postgres

import (
	"fmt"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
)

func idValue(value string) foundation.ID { return foundation.ID(value) }

func ids[T ~string](values []T) []string {
	result := make([]string, len(values))
	for index, value := range values {
		result[index] = string(value)
	}
	return result
}

func stringsOf[T ~string](values []T) []string { return ids(values) }

func nodeEvidenceHref(relationID foundation.ID) string {
	return fmt.Sprintf("/api/v1/graph/relations/%s/evidence", relationID)
}
