package application

import "context"

// SynthesisPublicationEventReconciler 在既有 Workflow outbox 中记录已证明的发布。
// 它不授予更新其他笔记的权限。
type SynthesisPublicationEventReconciler interface {
	ReconcileSynthesisPublicationEvents(context.Context, int) (int, error)
}
