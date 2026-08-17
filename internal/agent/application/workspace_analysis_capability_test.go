package application

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/agent/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
)

func TestWorkspaceAnalysisWorkerAdvertisementValidatesFrozenContract(t *testing.T) {
	valid := workspaceAnalysisCapabilityTestAdvertisement()
	if err := valid.Validate(); err != nil {
		t.Fatalf("valid advertisement rejected: %v", err)
	}

	tests := []struct {
		name   string
		mutate func(*WorkspaceAnalysisWorkerAdvertisement)
	}{
		{name: "worker instance", mutate: func(value *WorkspaceAnalysisWorkerAdvertisement) { value.WorkerInstanceID = "not-a-uuid" }},
		{name: "definition", mutate: func(value *WorkspaceAnalysisWorkerAdvertisement) { value.Contract.DefinitionKey = "rag" }},
		{name: "definition hash", mutate: func(value *WorkspaceAnalysisWorkerAdvertisement) { value.Contract.DefinitionHash = "ABC" }},
		{name: "catalog hash", mutate: func(value *WorkspaceAnalysisWorkerAdvertisement) { value.Contract.ToolCatalogHash = hashHex('A') }},
		{name: "policy", mutate: func(value *WorkspaceAnalysisWorkerAdvertisement) { value.Contract.PolicyVersion = 2 }},
		{name: "config revision", mutate: func(value *WorkspaceAnalysisWorkerAdvertisement) { value.Contract.ConfigRevision = -1 }},
		{name: "short lease", mutate: func(value *WorkspaceAnalysisWorkerAdvertisement) { value.LeaseDuration = 9 * time.Second }},
		{name: "long lease", mutate: func(value *WorkspaceAnalysisWorkerAdvertisement) { value.LeaseDuration = 61 * time.Second }},
		{name: "sub-microsecond lease", mutate: func(value *WorkspaceAnalysisWorkerAdvertisement) {
			value.LeaseDuration = 10*time.Second + time.Nanosecond
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			actual := valid
			test.mutate(&actual)
			if err := actual.Validate(); !workspaceAnalysisCapabilityHasError(err, foundation.ErrorInvalidInput, ErrorCodeWorkspaceAnalysisCapabilityInvalid) {
				t.Fatalf("Validate() error = %#v", err)
			}
		})
	}
}

func TestWorkspaceAnalysisCapabilityServiceUsesRepositoryLifecyclePorts(t *testing.T) {
	repository := &workspaceAnalysisCapabilityRepositoryFake{}
	service, err := NewWorkspaceAnalysisCapabilityService(repository)
	if err != nil {
		t.Fatal(err)
	}
	advertisement := workspaceAnalysisCapabilityTestAdvertisement()
	if _, err := service.Advertise(context.Background(), advertisement); err != nil {
		t.Fatal(err)
	}
	if _, err := service.Heartbeat(context.Background(), advertisement); err != nil {
		t.Fatal(err)
	}
	if _, err := service.Release(context.Background(), advertisement); err != nil {
		t.Fatal(err)
	}
	if repository.advertiseCalls != 1 || repository.heartbeatCalls != 1 || repository.releaseCalls != 1 ||
		repository.last.WorkerInstanceID != advertisement.WorkerInstanceID {
		t.Fatalf("repository=%#v", repository)
	}
}

func TestWorkspaceAnalysisCapabilityCheckedRunStarterChecksReadyInCallerTransaction(t *testing.T) {
	transaction := &workspaceAnalysisRunTransaction{}
	readiness := &workspaceAnalysisCapabilityRepositoryFake{}
	delegate := &workspaceAnalysisCapabilityRunStarterFake{run: domain.WorkspaceAnalysisRun{ID: workspaceAnalysisRunApplicationID(30)}}
	starter, err := NewWorkspaceAnalysisCapabilityCheckedRunStarter(readiness, delegate, workspaceAnalysisCapabilityTestAdvertisement().Contract)
	if err != nil {
		t.Fatal(err)
	}
	command := workspaceAnalysisRunStartTestCommand(false)
	run, err := starter.StartWorkspaceAnalysisRunTx(context.Background(), transaction, command)
	if err != nil || run.ID != delegate.run.ID || readiness.readyCalls != 1 || readiness.transaction != transaction ||
		delegate.calls != 1 || delegate.transaction != transaction {
		t.Fatalf("run=%#v err=%v readiness=%#v delegate=%#v", run, err, readiness, delegate)
	}

	readiness.readyErr = workspaceAnalysisCapabilityUnavailable(errors.New("not ready"))
	_, err = starter.StartWorkspaceAnalysisRunTx(context.Background(), transaction, command)
	if !workspaceAnalysisCapabilityHasError(err, foundation.ErrorDependencyUnavailable, ErrorCodeWorkspaceAnalysisCapabilityUnavailable) || delegate.calls != 1 {
		t.Fatalf("unavailable err=%#v delegate=%#v", err, delegate)
	}
}

func workspaceAnalysisCapabilityHasError(err error, kind foundation.ErrorKind, code string) bool {
	var classified *foundation.Error
	return errors.As(err, &classified) && classified.Kind == kind && classified.Code == code && !classified.Retryable
}

func workspaceAnalysisCapabilityTestAdvertisement() WorkspaceAnalysisWorkerAdvertisement {
	return WorkspaceAnalysisWorkerAdvertisement{
		WorkerInstanceID: workspaceAnalysisRunApplicationID(40),
		Contract: WorkspaceAnalysisCapabilityContract{
			DefinitionKey: "workspace-analysis", DefinitionVersion: 1,
			DefinitionHash: hashHex('a'), ToolCatalogHash: hashHex('b'), PolicyVersion: 1, ConfigRevision: 7,
		},
		LeaseDuration: DefaultWorkspaceAnalysisWorkerCapabilityLease,
	}
}

type workspaceAnalysisCapabilityRepositoryFake struct {
	advertiseCalls int
	heartbeatCalls int
	releaseCalls   int
	readyCalls     int
	last           WorkspaceAnalysisWorkerAdvertisement
	transaction    any
	readyErr       error
}

func (repository *workspaceAnalysisCapabilityRepositoryFake) AdvertiseWorkspaceAnalysisWorker(_ context.Context, advertisement WorkspaceAnalysisWorkerAdvertisement) (WorkspaceAnalysisWorkerCapability, error) {
	repository.advertiseCalls++
	repository.last = advertisement
	return WorkspaceAnalysisWorkerCapability{WorkspaceAnalysisWorkerAdvertisement: advertisement, Version: 1}, nil
}

func (repository *workspaceAnalysisCapabilityRepositoryFake) HeartbeatWorkspaceAnalysisWorker(_ context.Context, advertisement WorkspaceAnalysisWorkerAdvertisement) (WorkspaceAnalysisWorkerCapability, error) {
	repository.heartbeatCalls++
	repository.last = advertisement
	return WorkspaceAnalysisWorkerCapability{WorkspaceAnalysisWorkerAdvertisement: advertisement, Version: 2}, nil
}

func (repository *workspaceAnalysisCapabilityRepositoryFake) ReleaseWorkspaceAnalysisWorker(_ context.Context, advertisement WorkspaceAnalysisWorkerAdvertisement) (WorkspaceAnalysisWorkerCapability, error) {
	repository.releaseCalls++
	repository.last = advertisement
	return WorkspaceAnalysisWorkerCapability{WorkspaceAnalysisWorkerAdvertisement: advertisement, Version: 3}, nil
}

func (repository *workspaceAnalysisCapabilityRepositoryFake) RequireWorkspaceAnalysisWorkerReadyTx(_ context.Context, transaction any, _ WorkspaceAnalysisCapabilityContract) error {
	repository.readyCalls++
	repository.transaction = transaction
	return repository.readyErr
}

type workspaceAnalysisCapabilityRunStarterFake struct {
	run         domain.WorkspaceAnalysisRun
	calls       int
	transaction any
}

func (starter *workspaceAnalysisCapabilityRunStarterFake) StartWorkspaceAnalysisRunTx(_ context.Context, transaction any, _ WorkspaceAnalysisRunStartCommand) (domain.WorkspaceAnalysisRun, error) {
	starter.calls++
	starter.transaction = transaction
	return starter.run, nil
}
