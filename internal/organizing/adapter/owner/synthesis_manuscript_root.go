package owner

import (
	"context"
	"errors"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	app "github.com/CodeZen-Lizhi/zhixu/internal/organizing/application"
	platformpostgres "github.com/CodeZen-Lizhi/zhixu/internal/platform/postgres"
	"github.com/CodeZen-Lizhi/zhixu/internal/platform/rootgrant"
)

// SynthesisManuscriptRoot 记录已有进程能力。账本 UUID 仅作审计身份；每次调用仍须解析实时根目录权限。
type SynthesisManuscriptRoot struct {
	resolver *rootgrant.RootGrantResolver
	files    app.SynthesisManuscriptFileReader
}

func NewSynthesisManuscriptRoot(resolver *rootgrant.RootGrantResolver, files app.SynthesisManuscriptFileReader) (*SynthesisManuscriptRoot, error) {
	if resolver == nil || files == nil {
		return nil, manuscriptRootDenied()
	}
	return &SynthesisManuscriptRoot{resolver: resolver, files: files}, nil
}

func (r *SynthesisManuscriptRoot) ReadSynthesisManuscriptRootScoped(ctx context.Context, scope foundation.TransactionScope, workspace foundation.ID) (app.SynthesisManuscriptRoot, error) {
	var out app.SynthesisManuscriptRoot
	cap, err := r.resolver.Resolve(ctx, workspace)
	if err != nil {
		return out, err
	}
	defer func() { _ = cap.Close() }()
	tx, err := platformpostgres.GORMTransaction(scope)
	if err != nil {
		return out, err
	}
	tx = tx.WithContext(ctx)
	if err := tx.Exec(`SELECT singleton FROM ops.workspace_control_state WHERE singleton=true FOR SHARE`).Error; err != nil {
		return out, err
	}
	var binding struct {
		RootPath        string
		RootFingerprint string
		BindingVersion  int64
	}
	result := tx.Raw(`SELECT root_path,root_fingerprint,binding_version FROM core.workspace WHERE id=? AND status='active' AND availability='available' AND removed_at IS NULL FOR SHARE`, string(workspace)).Scan(&binding)
	if result.Error != nil {
		return out, result.Error
	}
	if result.RowsAffected != 1 || binding.RootPath != cap.CanonicalRoot() || len(binding.RootFingerprint) != 64 || binding.BindingVersion < 1 {
		return out, manuscriptRootDenied()
	}
	if err = cap.Revalidate(ctx); err != nil {
		return out, err
	}
	result = tx.Raw(`INSERT INTO organizing.synthesis_manuscript_root_identity(workspace_id,grant_generation,root_fingerprint,binding_version)
 VALUES(?,?,?,?) ON CONFLICT(workspace_id,grant_generation,root_fingerprint,binding_version) DO NOTHING RETURNING id`, string(workspace), cap.Generation(), binding.RootFingerprint, binding.BindingVersion).Scan(&out.GrantID)
	if result.Error != nil {
		return out, result.Error
	}
	if result.RowsAffected == 0 {
		if err = tx.Raw(`SELECT id FROM organizing.synthesis_manuscript_root_identity WHERE workspace_id=? AND grant_generation=? AND root_fingerprint=? AND binding_version=?`, string(workspace), cap.Generation(), binding.RootFingerprint, binding.BindingVersion).Scan(&out.GrantID).Error; err != nil {
			return out, err
		}
	}
	out.Fingerprint = binding.RootFingerprint
	out.BindingVersion = binding.BindingVersion
	return out, cap.Revalidate(ctx)
}

func (r *SynthesisManuscriptRoot) CurrentContent(ctx context.Context, workspace foundation.ID, path string, limit int64) ([]byte, string, error) {
	cap, err := r.resolver.Resolve(ctx, workspace)
	if err != nil {
		return nil, "", err
	}
	defer func() { _ = cap.Close() }()
	content, hash, err := r.files.CurrentContent(ctx, workspace, path, limit)
	if err != nil {
		return nil, "", err
	}
	if err = cap.Revalidate(ctx); err != nil {
		return nil, "", err
	}
	return content, hash, nil
}
func (r *SynthesisManuscriptRoot) EnsureTargetAbsent(ctx context.Context, workspace foundation.ID, path, token string) error {
	cap, err := r.resolver.Resolve(ctx, workspace)
	if err != nil {
		return err
	}
	defer func() { _ = cap.Close() }()
	if err = r.files.EnsureTargetAbsent(ctx, workspace, path, token); err != nil {
		return err
	}
	return cap.Revalidate(ctx)
}
func manuscriptRootDenied() error {
	return foundation.NewError(foundation.ErrorPermissionDenied, rootgrant.ErrorCodeRootNotGranted, false, errors.New("manuscript root authority is unavailable"))
}
