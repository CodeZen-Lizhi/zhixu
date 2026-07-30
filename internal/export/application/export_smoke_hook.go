//go:build !exportsmoke

package application

import (
	"context"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
)

// AttachmentExportSmokeAfterScan is inert outside the dedicated export smoke build.
func AttachmentExportSmokeAfterScan(context.Context, foundation.ID) error { return nil }
