package workflow

import (
	"testing"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	organizingdomain "github.com/CodeZen-Lizhi/zhixu/internal/organizing/domain"
	workflowapp "github.com/CodeZen-Lizhi/zhixu/internal/workflow/application"
)

func TestResultReceiptCarriesImmutableTerminalBinding(t *testing.T) {
	resultID := terminalTestID(9)
	bindingID := terminalTestID(4)
	snapshotID := terminalTestID(3)
	referenceID := terminalTestID(6)
	tests := []organizingdomain.ResultKind{organizingdomain.ResultArtifact, organizingdomain.ResultMergeProposal}
	for _, kind := range tests {
		t.Run(string(kind), func(t *testing.T) {
			executor := &Executor{dependencies: ExecutorDependencies{IDs: fixedReceiptIDGenerator{id: resultID}}}
			output, err := executor.resultReceipt(workflowapp.ExecutionContext{}, runContext{
				binding:  organizingdomain.RunBinding{ID: bindingID},
				snapshot: organizingdomain.Snapshot{ID: snapshotID},
				compiled: organizingdomain.CompiledTemplate{ResultKind: kind},
			}, kind, referenceID, terminalTestHash("f"))
			if err != nil {
				t.Fatal(err)
			}
			receipt, err := decodeFinalReceipt(output.Output)
			if err != nil {
				t.Fatal(err)
			}
			if receipt.SchemaVersion != receiptSchemaV1 || receipt.ResultID != resultID ||
				receipt.RunBindingID != bindingID || receipt.SnapshotID != snapshotID || receipt.Kind != kind ||
				receipt.ResultRef != referenceID || receipt.ResultHash != terminalTestHash("f") {
				t.Fatalf("receipt=%+v", receipt)
			}
		})
	}
}

func TestTerminalContractCoversEveryFinalExecutorNode(t *testing.T) {
	tests := []struct {
		node       string
		definition string
		kind       organizingdomain.ResultKind
	}{
		{TopicArtifactNodeKind, TopicArticleDefinitionKey, organizingdomain.ResultArtifact},
		{MergeProposalNodeKind, MergeDocumentsDefinitionKey, organizingdomain.ResultMergeProposal},
		{KnowledgeReportNodeKind, KnowledgeReportDefinitionKey, organizingdomain.ResultArtifact},
		{InterviewReviewNodeKind, InterviewReviewDefinitionKey, organizingdomain.ResultArtifact},
	}
	for _, test := range tests {
		definition, kind, final := terminalContract(test.node)
		if !final || definition != test.definition || kind != test.kind {
			t.Fatalf("node=%s definition=%s kind=%s final=%v", test.node, definition, kind, final)
		}
	}
}

type fixedReceiptIDGenerator struct {
	id  foundation.ID
	err error
}

func (generator fixedReceiptIDGenerator) New() (foundation.ID, error) {
	return generator.id, generator.err
}
