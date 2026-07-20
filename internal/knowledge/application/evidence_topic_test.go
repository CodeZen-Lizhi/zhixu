package application

import (
	"context"
	"testing"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	"github.com/CodeZen-Lizhi/zhixu/internal/knowledge/domain"
)

type fakeEvidenceTopicRepository struct {
	result      []domain.EvidenceTopicBinding
	workspaceID foundation.ID
	provenance  []domain.ProvenanceRef
}

func (f *fakeEvidenceTopicRepository) ResolveEvidenceTopics(_ context.Context, workspaceID foundation.ID, provenance []domain.ProvenanceRef) ([]domain.EvidenceTopicBinding, error) {
	f.workspaceID = workspaceID
	f.provenance = provenance
	return f.result, nil
}

func TestEvidenceTopicServiceCanonicalizesAndValidatesRepositoryResult(t *testing.T) {
	workspace := testID(1)
	second := domain.ProvenanceRef{WorkspaceID: workspace, SourceVersionID: testID(4), SourceSpanID: testID(5)}
	first := domain.ProvenanceRef{WorkspaceID: workspace, SourceVersionID: testID(2), SourceSpanID: testID(3)}
	repository := &fakeEvidenceTopicRepository{result: []domain.EvidenceTopicBinding{
		{TopicID: testID(10), TopicName: "主题", Provenance: first},
	}}
	service, err := NewEvidenceTopicService(repository)
	if err != nil {
		t.Fatal(err)
	}
	result, err := service.ResolveEvidenceTopics(context.Background(), ResolveEvidenceTopicsQuery{WorkspaceID: workspace, Provenance: []domain.ProvenanceRef{second, first}})
	if err != nil || len(result) != 1 || repository.provenance[0] != first {
		t.Fatalf("result=%#v provenance=%#v err=%v", result, repository.provenance, err)
	}
}

func TestEvidenceTopicServiceFailsClosedOnDamagedRepositoryResults(t *testing.T) {
	workspace := testID(1)
	ref := domain.ProvenanceRef{WorkspaceID: workspace, SourceVersionID: testID(2), SourceSpanID: testID(3)}
	valid := domain.EvidenceTopicBinding{TopicID: testID(10), TopicName: "主题", Provenance: ref}
	cases := [][]domain.EvidenceTopicBinding{
		{valid, valid},
		{{TopicID: testID(10), TopicName: " 非规范 ", Provenance: ref}},
		{{TopicID: testID(10), TopicName: "主题", Provenance: domain.ProvenanceRef{WorkspaceID: workspace, SourceVersionID: testID(8), SourceSpanID: testID(9)}}},
		{{TopicID: testID(11), TopicName: "乙", Provenance: ref}, {TopicID: testID(10), TopicName: "甲", Provenance: ref}},
	}
	for _, result := range cases {
		service, err := NewEvidenceTopicService(&fakeEvidenceTopicRepository{result: result})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := service.ResolveEvidenceTopics(context.Background(), ResolveEvidenceTopicsQuery{WorkspaceID: workspace, Provenance: []domain.ProvenanceRef{ref}}); errorCode(err) != errorCodeResultConsistency {
			t.Fatalf("result=%#v err=%v", result, err)
		}
	}
}
