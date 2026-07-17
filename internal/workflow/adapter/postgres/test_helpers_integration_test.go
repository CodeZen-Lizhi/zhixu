//go:build integration

package workflowpostgres

import (
	"errors"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
)

func hasCode(err error, code string) bool {
	var classified *foundation.Error
	return errors.As(err, &classified) && classified.Code == code
}
