package application

import (
	"context"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	"github.com/CodeZen-Lizhi/zhixu/internal/workspace/domain"
)

// DiscoverWorkspaceSources 通过检查点持续遍历有效目录。
// 每个文件与手动扫描一样，通过同一不可变采集/注册路径
// 提交；中断的文件会在下一轮完整遍历时重访。
func (s *Service) DiscoverWorkspaceSources(ctx context.Context, workspaceID foundation.ID, after string, limit int) (domain.SourceDiscoveryPage, error) {
	result := domain.SourceDiscoveryPage{After: after}
	if s == nil || ctx == nil || s.dependencies.Repository == nil || s.dependencies.IDs == nil || s.dependencies.Clock == nil {
		return result, dependencyError("WORKSPACE_SERVICE_UNAVAILABLE")
	}
	failures, ok := s.dependencies.Repository.(domain.DiscoveryFailureRepository)
	if !ok {
		return result, dependencyError("WORKSPACE_DISCOVERY_FAILURE_STORE_UNAVAILABLE")
	}
	binder, ok := s.dependencies.Files.(domain.SourceDiscoveryBinder)
	if !ok {
		return result, dependencyError("WORKSPACE_SOURCE_DISCOVERY_UNAVAILABLE")
	}
	workspace, err := s.dependencies.Repository.GetWorkspaceByID(ctx, workspaceID)
	if err != nil {
		return result, err
	}
	if workspace.Status != domain.WorkspaceStatusActive {
		return result, dependencyError("WORKSPACE_SOURCE_DISCOVERY_INACTIVE")
	}
	validate := func(checkCtx context.Context) error {
		current, err := s.dependencies.Repository.GetWorkspaceByID(checkCtx, workspaceID)
		if err != nil {
			return err
		}
		if current.Status != domain.WorkspaceStatusActive || current.RootPath != workspace.RootPath || current.RootFingerprint != workspace.RootFingerprint || current.BindingVersion != workspace.BindingVersion {
			return dependencyError("WORKSPACE_SOURCE_DISCOVERY_INACTIVE")
		}
		return nil
	}
	files, err := binder.BindSourceDiscovery(ctx, workspace.RootPath, validate)
	if err != nil {
		return result, err
	}
	defer files.Close()
	boundService := *s
	boundService.dependencies.Files = files
	page, err := files.ScanPage(ctx, workspace.RootPath, after, limit)
	if err != nil {
		return result, err
	}
	record := func(observations []domain.DiscoveryObservation) error {
		if err := files.Revalidate(ctx); err != nil {
			return err
		}
		err := failures.RecordDiscoveryObservations(ctx, workspace, observations)
		if err != nil {
			result.After = after
		}
		return err
	}
	observed := append([]domain.DiscoveryObservation{}, page.Failures...)
	for _, relative := range page.ReadDirectories {
		observed = append(observed, domain.DiscoveryObservation{Path: relative, Stage: "WALK"})
	}
	if len(observed) > 0 {
		if err := record(observed); err != nil {
			return result, err
		}
	}
	result.Failed = page.Failed
	for _, relative := range page.Paths {
		if err := ctx.Err(); err != nil {
			return result, err
		}
		// 每次文件操作前重新校验进程的 RootGrant。
		current, err := s.dependencies.Repository.GetWorkspaceByID(ctx, workspaceID)
		if err != nil {
			return result, err
		}
		if current.Status != domain.WorkspaceStatusActive || current.RootPath != workspace.RootPath || current.RootFingerprint != workspace.RootFingerprint || current.BindingVersion != workspace.BindingVersion {
			return result, dependencyError("WORKSPACE_SOURCE_DISCOVERY_INACTIVE")
		}
		file, err := files.ObserveSource(ctx, workspace.RootPath, relative)
		if err != nil {
			if ctx.Err() != nil {
				result.After = relative
				return result, ctx.Err()
			}
			if err := record([]domain.DiscoveryObservation{{Path: relative, Stage: "OBSERVE", Code: "FILE_OBSERVATION_FAILED"}}); err != nil {
				return result, err
			}
			result.Failed++
			result.After = relative
			continue
		}
		registered, err := boundService.registerScannedFiles(ctx, workspace, []domain.ScannedFile{file})
		if err != nil {
			if ctx.Err() != nil {
				result.After = relative
				return result, ctx.Err()
			}
			if err := record([]domain.DiscoveryObservation{{Path: relative, Stage: "REGISTER", Code: "SOURCE_REGISTRATION_FAILED"}}); err != nil {
				return result, err
			}
			result.Failed++
			result.After = relative
			continue
		}
		if err := record([]domain.DiscoveryObservation{{Path: relative, Stage: "REGISTER"}}); err != nil {
			return result, err
		}
		result.Files = append(result.Files, registered...)
		result.After = relative
	}
	result.After = page.After
	result.Done = page.Done
	return result, nil
}

func (s *Service) ListDiscoveryFailures(ctx context.Context, id foundation.ID, after string, limit int) (domain.DiscoveryFailurePage, error) {
	if limit < 1 || limit > 100 || (after != "" && !domain.ValidDiscoveryPath(after)) {
		return domain.DiscoveryFailurePage{}, invalidError("DISCOVERY_FAILURE_QUERY_INVALID")
	}
	repository, ok := s.dependencies.Repository.(domain.DiscoveryFailureRepository)
	if !ok {
		return domain.DiscoveryFailurePage{}, dependencyError("WORKSPACE_DISCOVERY_FAILURE_STORE_UNAVAILABLE")
	}
	return repository.ListDiscoveryFailures(ctx, id, after, limit)
}
