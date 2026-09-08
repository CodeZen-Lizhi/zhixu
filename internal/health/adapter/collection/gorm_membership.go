package collection

import (
	"context"
	"errors"
	"reflect"

	collectionapplication "github.com/CodeZen-Lizhi/zhixu/internal/collection/application"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	healthapplication "github.com/CodeZen-Lizhi/zhixu/internal/health/application"
	"github.com/CodeZen-Lizhi/zhixu/internal/health/domain"
)

// ScopedBindingVerifier 在 Health 调用方的同一事务内复核 Collection binding。
type ScopedBindingVerifier struct {
	verifier collectionapplication.ScopedDurableScanBindingVerifier
}

func NewScopedBindingVerifier(verifier collectionapplication.ScopedDurableScanBindingVerifier) (*ScopedBindingVerifier, error) {
	if isNilScopedBindingVerifier(verifier) {
		return nil, foundation.NewError(foundation.ErrorDependencyUnavailable, domain.ErrorCodeScanScopeUnavailable, false, errors.New("collection scoped binding verifier is unavailable"))
	}
	return &ScopedBindingVerifier{verifier: verifier}, nil
}

// VerifyBindingScoped validates the frozen binding inside the caller scope.
func (verifier *ScopedBindingVerifier) VerifyBindingScoped(ctx context.Context, scope foundation.TransactionScope, binding healthapplication.SmartCollectionBinding) error {
	if verifier == nil || isNilScopedBindingVerifier(verifier.verifier) || ctx == nil || scope == nil {
		return foundation.NewError(foundation.ErrorDependencyUnavailable, domain.ErrorCodeScanScopeUnavailable, false, errors.New("collection scoped binding scope is unavailable"))
	}
	return verifier.verifier.VerifyDurableScanBindingScoped(ctx, scope, toCollectionBinding(binding))
}

func isNilScopedBindingVerifier(value collectionapplication.ScopedDurableScanBindingVerifier) bool {
	if value == nil {
		return true
	}
	reflected := reflect.ValueOf(value)
	switch reflected.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return reflected.IsNil()
	default:
		return false
	}
}

var _ interface {
	VerifyBindingScoped(context.Context, foundation.TransactionScope, healthapplication.SmartCollectionBinding) error
} = (*ScopedBindingVerifier)(nil)
