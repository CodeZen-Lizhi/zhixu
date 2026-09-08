package postgres

import (
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	"time"
)

func nullableTime(value time.Time) any {
	if value.IsZero() {
		return nil
	}
	return value.UTC()
}

func nullableID(value foundation.ID) any {
	if value == "" {
		return nil
	}
	return string(value)
}
