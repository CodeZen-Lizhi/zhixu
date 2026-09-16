package synthesispostgres

import (
	"context"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	organizingworkflow "github.com/CodeZen-Lizhi/zhixu/internal/organizing/workflow"
)

func (s *Store) SynthesisRetryDefinitionVersionScoped(ctx context.Context, scope foundation.TransactionScope, workspace, run foundation.ID) (int64, error) {
	tx, err := s.transaction(ctx, scope)
	if err != nil {
		return 0, err
	}
	var version int64
	result := tx.Raw(`SELECT d.version FROM workflow.run r JOIN workflow.definition d ON d.id=r.definition_id AND d.workspace_id=r.workspace_id WHERE r.workspace_id=? AND r.id=? AND d.key=?`, string(workspace), string(run), organizingworkflow.SynthesisDefinitionKey).Scan(&version)
	if result.Error != nil {
		return 0, classify(ctx, result.Error)
	}
	if result.RowsAffected != 1 || version != organizingworkflow.SynthesisDefinitionVersion && version != organizingworkflow.SynthesisManuscriptDefinitionVersion {
		return 0, invalid("synthesis retry has an unknown original definition")
	}
	return version, nil
}
