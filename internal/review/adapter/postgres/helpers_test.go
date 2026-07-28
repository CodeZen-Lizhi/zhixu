package postgres

import (
	"errors"
	"testing"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	"github.com/CodeZen-Lizhi/zhixu/internal/review/domain"
)

func TestEncodeJSONReturnsPersistenceError(t *testing.T) {
	_, err := encodeJSON(map[string]any{"unsupported": make(chan int)})
	var classified *foundation.Error
	if !errors.As(err, &classified) || classified.Kind != foundation.ErrorConsistencyViolation || classified.Code != domain.ErrorCodePersistenceInvalid {
		t.Fatalf("encode error=%v classified=%+v", err, classified)
	}
}
