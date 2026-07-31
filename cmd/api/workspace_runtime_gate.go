package main

import (
	"context"
	"errors"
	"net/http"
	"sync/atomic"

	"github.com/CodeZen-Lizhi/zhixu/internal/httpapi"
	workspaceruntimegrant "github.com/CodeZen-Lizhi/zhixu/internal/workspace/runtimegrant"
)

type apiWorkspaceRuntimeGate struct {
	accepting atomic.Bool
	inFlight  atomic.Int64
}

func newAPIWorkspaceRuntimeGate() *apiWorkspaceRuntimeGate {
	gate := &apiWorkspaceRuntimeGate{}
	gate.accepting.Store(true)
	return gate
}

func (gate *apiWorkspaceRuntimeGate) Hooks() workspaceruntimegrant.QuiescenceHooks {
	return workspaceruntimegrant.QuiescenceHooks{
		Begin: func(ctx context.Context) error {
			if gate == nil || ctx == nil {
				return errors.New("api Workspace runtime gate is unavailable")
			}
			gate.accepting.Store(false)
			return nil
		},
		IsQuiesced: func(ctx context.Context) (bool, error) {
			if gate == nil || ctx == nil {
				return false, errors.New("api Workspace runtime gate is unavailable")
			}
			return gate.inFlight.Load() == 0, nil
		},
		Resume: func(ctx context.Context) error {
			if gate == nil || ctx == nil {
				return errors.New("api Workspace runtime gate is unavailable")
			}
			gate.accepting.Store(true)
			return nil
		},
	}
}

func (gate *apiWorkspaceRuntimeGate) Wrap(next http.Handler) http.Handler {
	return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if !tracksWorkspaceRuntimeWork(request) {
			next.ServeHTTP(writer, request)
			return
		}
		if gate == nil || !gate.accepting.Load() {
			writeWorkspaceQuiescing(writer)
			return
		}
		gate.inFlight.Add(1)
		defer gate.inFlight.Add(-1)
		if !gate.accepting.Load() {
			writeWorkspaceQuiescing(writer)
			return
		}
		next.ServeHTTP(writer, request)
	})
}

func tracksWorkspaceRuntimeWork(request *http.Request) bool {
	if request == nil {
		return true
	}
	// The event stream is database-backed and intentionally long-lived. It is
	// closed when the old API container is revoked, but it must not block Root I/O drain.
	return request.Method != http.MethodGet || request.URL.Path != "/api/v1/events"
}

func writeWorkspaceQuiescing(writer http.ResponseWriter) {
	writer.Header().Set("Cache-Control", "no-store")
	httpapi.WriteProblem(writer, http.StatusServiceUnavailable, "WORKSPACE_SWITCH_QUIESCING", "Workspace 正在安全切换，请稍后重试", true, nil)
}
