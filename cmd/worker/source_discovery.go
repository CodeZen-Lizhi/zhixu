package main

import (
	"context"
	"log/slog"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	workspaceapp "github.com/CodeZen-Lizhi/zhixu/internal/workspace/application"
	"github.com/CodeZen-Lizhi/zhixu/internal/workspace/domain"
)

// 串行生产者拥有游标。重启可安全地重复发现；
// 来源/版本唯一性及 Capture 回执负责持久去重。
type localSourceDiscovery struct {
	repository domain.ActiveWorkspaceRepository
	service    *workspaceapp.Service
	workspace  foundation.ID
	after      string
	nextPass   time.Time
}

func (d *localSourceDiscovery) DiscoverBatch(ctx context.Context) (domain.SourceDiscoveryPage, error) {
	var result domain.SourceDiscoveryPage
	workspace, err := d.repository.GetActiveWorkspace(ctx)
	if err != nil {
		return result, err
	}
	if workspace.ID != d.workspace {
		d.workspace = workspace.ID
		d.after = ""
		d.nextPass = time.Time{}
	}
	if time.Now().Before(d.nextPass) {
		return result, nil
	}
	result, err = d.service.DiscoverWorkspaceSources(ctx, workspace.ID, d.after, 100)
	if err != nil {
		return result, err
	}
	d.after = result.After
	if result.Done {
		d.after = ""
		d.nextPass = time.Now().Add(30 * time.Second)
	}
	return result, err
}

func discoverLocalSources(ctx context.Context, logger *slog.Logger, discovery *localSourceDiscovery, timeout time.Duration) {
	if discovery == nil || discovery.repository == nil || discovery.service == nil {
		return
	}
	bounded, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	result, err := discovery.DiscoverBatch(bounded)
	if err != nil {
		code, retryable := captureDispatchFailure(err)
		logger.Warn("local source discovery interrupted", "error_code", code, "retryable", retryable, "registered", len(result.Files), "failed", result.Failed)
	} else if result.Failed > 0 {
		logger.Warn("local source discovery found unreadable entries", "error_code", "SOURCE_DISCOVERY_ENTRY_FAILED", "failed", result.Failed, "registered", len(result.Files))
	}
}
