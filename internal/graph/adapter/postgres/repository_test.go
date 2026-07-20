package postgres

import (
	"context"
	"errors"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	graphdomain "github.com/CodeZen-Lizhi/zhixu/internal/graph/domain"
)

func TestNewRepositoryFailsClosedWithoutDatabase(t *testing.T) {
	repository, err := NewRepository(nil)
	if repository != nil {
		t.Fatal("nil database unexpectedly created repository")
	}
	var classified *foundation.Error
	if !errors.As(err, &classified) || classified.Code != graphdomain.ErrorCodeDependencyUnavailable {
		t.Fatalf("err=%v", err)
	}
}

func TestDisplaySummaryPreservesUTF8Boundary(t *testing.T) {
	value := strings.Repeat("知", 300)
	summary := displaySummary(value, 512)
	if !utf8.ValidString(summary) || len(summary) > 512 || summary == value {
		t.Fatalf("summary bytes=%d valid=%v", len(summary), utf8.ValidString(summary))
	}
}

func TestClassifyProjectionScanSeparatesProjectionDamageFromTimeout(t *testing.T) {
	for _, testCase := range []struct {
		name string
		err  error
		code string
	}{
		{name: "projection damage", err: errors.New("invalid applicability"), code: graphdomain.ErrorCodeProjectionInconsistent},
		{name: "timeout", err: context.DeadlineExceeded, code: graphdomain.ErrorCodeQueryTimeout},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			var classified *foundation.Error
			if err := classifyProjectionScan(testCase.err); !errors.As(err, &classified) || classified.Code != testCase.code {
				t.Fatalf("err=%v", err)
			}
		})
	}
}
