package main

import (
	"context"
	"errors"
	"net/http"
	"sync/atomic"

	"github.com/CodeZen-Lizhi/zhixu/internal/httpapi"
	modelsettingsdomain "github.com/CodeZen-Lizhi/zhixu/internal/modelsettings/domain"
	modelsettingsruntime "github.com/CodeZen-Lizhi/zhixu/internal/modelsettings/runtime"
)

// apiModelRuntimeController 是 API 启动前必须完成登记的运行时边界。
type apiModelRuntimeController interface {
	Active() <-chan struct{}
	Run(context.Context) error
}

// apiProducerGate 在 rollout 排空期间快速拒绝新的 API mutation。
type apiProducerGate struct {
	// enabled 是 HTTP middleware 与 watcher 共享的无锁 gate 状态。
	enabled atomic.Bool
}

// newAPIProducerGate 创建初始允许 mutation 的本地 gate。
func newAPIProducerGate() *apiProducerGate {
	gate := &apiProducerGate{}
	gate.enabled.Store(true)
	return gate
}

// Hooks 返回 Model Runtime Controller 使用的排空边界。
func (gate *apiProducerGate) Hooks() modelsettingsruntime.DrainHooks {
	return modelsettingsruntime.DrainHooks{
		Begin: func(ctx context.Context) error {
			if gate == nil || ctx == nil {
				return errors.New("api producer gate is unavailable")
			}
			gate.enabled.Store(false)
			return nil
		},
		IsQuiesced: func(ctx context.Context) (bool, error) {
			if gate == nil || ctx == nil {
				return false, errors.New("api producer gate is unavailable")
			}
			return !gate.enabled.Load(), nil
		},
		Resume: func(ctx context.Context) error {
			if gate == nil || ctx == nil {
				return errors.New("api producer gate is unavailable")
			}
			gate.enabled.Store(true)
			return nil
		},
	}
}

// Wrap 保留只读请求，并在排空期间以稳定、无缓存 Problem 拒绝 mutation。
func (gate *apiProducerGate) Wrap(next http.Handler) http.Handler {
	return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if gate != nil && !gate.enabled.Load() && mutationMethod(request.Method) {
			writer.Header().Set("Cache-Control", "no-store")
			httpapi.WriteProblem(
				writer,
				http.StatusServiceUnavailable,
				modelsettingsdomain.ErrorCodeEnqueuePaused,
				"模型配置切换中，暂不接受写请求",
				true,
				nil,
			)
			return
		}
		next.ServeHTTP(writer, request)
	})
}

func mutationMethod(method string) bool {
	switch method {
	case http.MethodGet, http.MethodHead, http.MethodOptions:
		return false
	default:
		return true
	}
}

// startAPIModelRuntime 启动 watcher；调用方必须等待 Active 后才能监听端口。
func startAPIModelRuntime(ctx context.Context, controller apiModelRuntimeController) <-chan error {
	errorsChannel := make(chan error, 1)
	go func() {
		err := controller.Run(ctx)
		if err != nil {
			errorsChannel <- err
			return
		}
		select {
		case <-controller.Active():
		default:
			errorsChannel <- errors.New("api model runtime stopped before activation")
		}
	}()
	return errorsChannel
}
