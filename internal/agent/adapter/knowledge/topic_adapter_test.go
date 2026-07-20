package knowledge

import (
	"context"
	"errors"
	"testing"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	knowledgeapplication "github.com/CodeZen-Lizhi/zhixu/internal/knowledge/application"
	knowledgedomain "github.com/CodeZen-Lizhi/zhixu/internal/knowledge/domain"
)

func TestTopicAdapterDelegatesEvidenceTopicResolution(t *testing.T) {
	workspace := adapterTestID(20)
	provenance := knowledgedomain.ProvenanceRef{
		WorkspaceID: workspace, SourceVersionID: adapterTestID(21), SourceSpanID: adapterTestID(22),
	}
	want := []knowledgedomain.EvidenceTopicBinding{{
		Provenance: provenance, TopicID: adapterTestID(23), TopicName: "检索主题",
	}}
	service := &evidenceTopicServiceFake{result: want}
	adapter, err := NewTopicAdapter(service)
	if err != nil {
		t.Fatal(err)
	}
	query := knowledgeapplication.ResolveEvidenceTopicsQuery{WorkspaceID: workspace, Provenance: []knowledgedomain.ProvenanceRef{provenance}}
	got, err := adapter.ResolveEvidenceTopics(context.Background(), query)
	if err != nil {
		t.Fatal(err)
	}
	if service.calls != 1 || service.query.WorkspaceID != workspace || len(service.query.Provenance) != 1 ||
		len(got) != 1 || got[0].TopicID != want[0].TopicID || got[0].TopicName != want[0].TopicName {
		t.Fatalf("topic delegation service=%#v got=%#v", service, got)
	}
	got, err = adapter.ResolveRAGTopics(context.Background(), workspace, []knowledgedomain.ProvenanceRef{provenance})
	if err != nil || service.calls != 2 || service.query.WorkspaceID != workspace || len(got) != 1 {
		t.Fatalf("rag topic delegation service=%#v got=%#v err=%v", service, got, err)
	}
}

func TestTopicAdapterRejectsMissingServiceAndPreservesApplicationError(t *testing.T) {
	if _, err := NewTopicAdapter(nil); adapterTestErrorCode(err) != errorCodeAdapterUnavailable {
		t.Fatalf("nil service code = %q, err=%v", adapterTestErrorCode(err), err)
	}
	var typedNil *evidenceTopicServiceFake
	if _, err := NewTopicAdapter(typedNil); adapterTestErrorCode(err) != errorCodeAdapterUnavailable {
		t.Fatalf("typed nil service code = %q, err=%v", adapterTestErrorCode(err), err)
	}

	dependencyErr := foundation.NewError(foundation.ErrorRetryableFailure, "TOPIC_READ_FAILED", true, errors.New("down"))
	adapter, err := NewTopicAdapter(&evidenceTopicServiceFake{err: dependencyErr})
	if err != nil {
		t.Fatal(err)
	}
	_, actual := adapter.ResolveEvidenceTopics(context.Background(), knowledgeapplication.ResolveEvidenceTopicsQuery{})
	if !errors.Is(actual, dependencyErr) {
		t.Fatalf("error=%v", actual)
	}

	var unavailable *TopicAdapter
	if _, err := unavailable.ResolveEvidenceTopics(context.Background(), knowledgeapplication.ResolveEvidenceTopicsQuery{}); adapterTestErrorCode(err) != errorCodeAdapterUnavailable {
		t.Fatalf("nil adapter code = %q, err=%v", adapterTestErrorCode(err), err)
	}
}

type evidenceTopicServiceFake struct {
	calls  int
	query  knowledgeapplication.ResolveEvidenceTopicsQuery
	result []knowledgedomain.EvidenceTopicBinding
	err    error
}

func (fake *evidenceTopicServiceFake) ResolveEvidenceTopics(_ context.Context, query knowledgeapplication.ResolveEvidenceTopicsQuery) ([]knowledgedomain.EvidenceTopicBinding, error) {
	fake.calls++
	fake.query = query
	return fake.result, fake.err
}
