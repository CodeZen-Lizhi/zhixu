package workflow

import (
	"testing"

	interviewapp "github.com/CodeZen-Lizhi/zhixu/internal/review/interview/application"
	workflowapp "github.com/CodeZen-Lizhi/zhixu/internal/workflow/application"
	workflowdomain "github.com/CodeZen-Lizhi/zhixu/internal/workflow/domain"
)

func TestNotePreparationDefinitionFreezesOneNonRetryingModelNode(t *testing.T) {
	catalog, err := workflowapp.NewValidationCatalog([]int{1}, []workflowdomain.Permission{workflowdomain.PermissionReadLocal, workflowdomain.PermissionWriteProposal})
	if err != nil {
		t.Fatal(err)
	}
	executors, err := workflowapp.NewExecutorRegistry(catalog)
	if err != nil {
		t.Fatal(err)
	}
	if err := executors.Register(interviewapp.NotePreparationNodeKind, 1, &NotePreparationExecutor{}); err != nil {
		t.Fatal(err)
	}
	if err := executors.Freeze(); err != nil {
		t.Fatal(err)
	}
	registry, err := workflowapp.NewDefinitionRegistry(catalog, executors)
	if err != nil {
		t.Fatal(err)
	}
	if err := RegisterNotePreparationDefinition(registry); err != nil {
		t.Fatal(err)
	}
	if err := registry.Freeze(); err != nil {
		t.Fatal(err)
	}
	definition, err := registry.Resolve(interviewapp.NotePreparationDefinitionKey, 1)
	if err != nil {
		t.Fatal(err)
	}
	expected, err := NotePreparationDefinition()
	if err != nil {
		t.Fatal(err)
	}
	if definition.GraphHash != expected.GraphHash || len(definition.Graph.Nodes) != 1 || definition.Graph.Nodes[0].RetryPolicy != (workflowdomain.RetryPolicy{}) {
		t.Fatal("note Workflow changed or permits automatic paid retries")
	}
	for _, raw := range []string{`{}`, `{"preparation_id":"bad"}`, `{"preparation_id":"79000000-0000-4000-8000-000000000006","preparation_id":"79000000-0000-4000-8000-000000000006"}`, `{"preparation_id":"79000000-0000-4000-8000-000000000006","snapshot":{}}`} {
		if _, err := DecodeNotePreparationInput([]byte(raw)); err == nil {
			t.Fatal("invalid Workflow payload accepted")
		}
	}
}
