package application_test

import (
	"context"

	changecontrolapplication "github.com/CodeZen-Lizhi/zhixu/internal/changecontrol/application"
	"github.com/CodeZen-Lizhi/zhixu/internal/changecontrol/domain"
)

type testWritebackAuditRecorder struct{}

func (*testWritebackAuditRecorder) EnsureStarted(context.Context, changecontrolapplication.WritebackResumeIdentity, domain.WritebackExecution, changecontrolapplication.WritebackAuditStep) error {
	return nil
}

func (*testWritebackAuditRecorder) RequireStarted(context.Context, changecontrolapplication.WritebackResumeIdentity, domain.WritebackExecution, changecontrolapplication.WritebackAuditStep) error {
	return nil
}

func (*testWritebackAuditRecorder) RecordSucceeded(context.Context, changecontrolapplication.WritebackResumeIdentity, domain.WritebackExecution, changecontrolapplication.WritebackAuditStep) error {
	return nil
}
