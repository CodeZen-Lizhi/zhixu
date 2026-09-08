package main

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	authoringchangecontrol "github.com/CodeZen-Lizhi/zhixu/internal/authoring/adapter/changecontrol"
	changecontrolapp "github.com/CodeZen-Lizhi/zhixu/internal/changecontrol/application"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	platformpostgres "github.com/CodeZen-Lizhi/zhixu/internal/platform/postgres"
)

func TestNewAuthoringHandlerRequiresEveryProductionDependency(t *testing.T) {
	proposals := authoringProposalServiceFake{}
	targets := authoringTargetReaderFake{}
	for _, test := range []struct {
		name      string
		pool      *platformpostgres.Pool
		proposals authoringchangecontrol.ProposalService
		targets   authoringchangecontrol.TargetReader
	}{
		{name: "database", proposals: proposals, targets: &targets},
		{name: "proposal service", pool: apiConstructorPool(t), targets: &targets},
		{name: "target reader", pool: apiConstructorPool(t), proposals: proposals},
	} {
		t.Run(test.name, func(t *testing.T) {
			if handler, err := newAuthoringHandler(test.pool, test.proposals, test.targets, time.Second); err == nil || handler != nil {
				t.Fatalf("handler=%#v err=%v", handler, err)
			}
		})
	}
}

func TestNewAuthoringHandlerComposesProductionBoundaries(t *testing.T) {
	handler, err := newAuthoringHandler(
		apiConstructorPool(t), authoringProposalServiceFake{}, &authoringTargetReaderFake{}, time.Second,
	)
	if err != nil || handler == nil || !handler.Available() {
		t.Fatalf("handler=%#v err=%v", handler, err)
	}
}

type authoringProposalServiceFake struct{}

func (authoringProposalServiceFake) CreateProposal(context.Context, changecontrolapp.CreateCommand) (changecontrolapp.CreateResult, error) {
	return changecontrolapp.CreateResult{}, nil
}

func (authoringProposalServiceFake) CreateCreateOnlyFileProposal(context.Context, changecontrolapp.CreateCreateOnlyFileProposalCommand) (changecontrolapp.CreateResult, error) {
	return changecontrolapp.CreateResult{}, nil
}

type authoringTargetReaderFake struct {
	err error
}

func (reader *authoringTargetReaderFake) CurrentHash(context.Context, foundation.ID, string) (string, error) {
	if reader == nil {
		return "", errors.New("target reader is nil")
	}
	if reader.err != nil {
		return "", reader.err
	}
	return strings.Repeat("a", 64), nil
}
