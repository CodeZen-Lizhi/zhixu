// Package auth 将已认证 Principal 适配为敏感导出授权判断。
package auth

import (
	"context"
	"errors"

	authhttp "github.com/CodeZen-Lizhi/zhixu/internal/auth/http"
	"github.com/CodeZen-Lizhi/zhixu/internal/capability"
	exportapp "github.com/CodeZen-Lizhi/zhixu/internal/export/application"
	"github.com/CodeZen-Lizhi/zhixu/internal/export/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
)

// Authorizer 要求敏感导出请求来自具有 READ_LOCAL 的认证身份。
type Authorizer struct{}

var _ exportapp.Authorizer = Authorizer{}

// AuthorizeExport 对 FULL + include_sensitive 请求执行 fail-closed 授权。
func (Authorizer) AuthorizeExport(ctx context.Context, request exportapp.AuthorizationRequest) error {
	principal, ok := authhttp.PrincipalFromContext(ctx)
	if !request.IncludeSensitive || !ok || request.PermissionScope != string(capability.ReadLocal) || !principal.Has(capability.ReadLocal) {
		return foundation.NewError(foundation.ErrorPermissionDenied, domain.ErrorCodePermissionDenied, false, errors.New("sensitive export requires an authenticated READ_LOCAL principal"))
	}
	return nil
}
