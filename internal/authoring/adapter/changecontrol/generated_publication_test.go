package changecontrol

import (
	"context"
	"errors"
	"strings"
	"testing"

	authoringapp "github.com/CodeZen-Lizhi/zhixu/internal/authoring/application"
	authoringdomain "github.com/CodeZen-Lizhi/zhixu/internal/authoring/domain"
	changecontroldomain "github.com/CodeZen-Lizhi/zhixu/internal/changecontrol/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
)

func TestGeneratedPublicationEvidenceUsesRealRevisionProvenance(t *testing.T) {
	for _, actor := range []string{"USER", "AGENT", "SYSTEM"} {
		t.Run(actor, func(t *testing.T) {
			request := proposalRequestFixture(t)
			request.CreatedByType = actor
			service := &proposalServiceFake{}
			creator, err := NewProposalCreator(service, &proposalTargetReaderFake{})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := creator.CreatePublicationProposal(t.Context(), request); err != nil {
				t.Fatal(err)
			}
			evidence := service.createOnlyCommand.EvidenceSummary
			if actor == "USER" {
				if evidence != publicationEvidence+" content_sha256="+request.ContentHash {
					t.Fatalf("existing USER request hash input changed: %q", evidence)
				}
			} else if !strings.Contains(evidence, actor) || strings.Contains(evidence, "explicitly submitted by the user") {
				t.Fatalf("generated content masquerades as a user submission: %q", evidence)
			}
		})
	}
	request := proposalRequestFixture(t)
	request.CreatedByType = "unknown"
	service := &proposalServiceFake{}
	creator, err := NewProposalCreator(service, &proposalTargetReaderFake{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := creator.CreatePublicationProposal(t.Context(), request); err == nil || service.createOnlyCommand.WorkspaceID != "" {
		t.Fatalf("unknown provenance reached Proposal creation: %v", err)
	}
}

func TestGeneratedPublicationRetirerKeepsScopeAndBusyClassification(t *testing.T) {
	scope := &generatedAdapterScope{}
	cause := foundation.NewError(foundation.ErrorVersionConflict, changecontroldomain.ErrorCodeGeneratedPublicationBusy, false, errors.New("approved"))
	repository := &generatedRetirementRepositoryFake{err: cause}
	retirer, err := NewGeneratedPublicationRetirer(repository)
	if err != nil {
		t.Fatal(err)
	}
	request := authoringapp.GeneratedProposalRetirement{WorkspaceID: authoringAdapterID(1), ProposalID: authoringAdapterID(2),
		ProposalRevisionID: authoringAdapterID(3), ExpectedProposalVersion: 1, ContentHash: strings.Repeat("a", 64)}
	err = retirer.RetireGeneratedProposalScoped(t.Context(), scope, request)
	var classified *foundation.Error
	if !errors.As(err, &classified) || classified.Code != authoringdomain.ErrorCodeGeneratedPublicationBusy || !errors.Is(err, cause) ||
		repository.scope != scope || repository.request.WorkspaceID != request.WorkspaceID || repository.request.ProposalRevisionID != request.ProposalRevisionID {
		t.Fatalf("retirement scope or error changed: error=%v scope=%v", err, repository.scope)
	}
}

type generatedAdapterScope struct{}

func (*generatedAdapterScope) TransactionScope() {}

type generatedRetirementRepositoryFake struct {
	scope   foundation.TransactionScope
	request changecontroldomain.GeneratedPublicationRetirement
	err     error
}

func (fake *generatedRetirementRepositoryFake) RetireGeneratedPublicationScoped(_ context.Context, scope foundation.TransactionScope, request changecontroldomain.GeneratedPublicationRetirement) error {
	fake.scope, fake.request = scope, request
	return fake.err
}
