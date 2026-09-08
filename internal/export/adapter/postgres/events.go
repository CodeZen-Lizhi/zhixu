package postgres

import (
	"context"
	"errors"
	"strconv"
	"strings"

	eventsdomain "github.com/CodeZen-Lizhi/zhixu/internal/events/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/export/domain"
)

func (repository *exportRepository) appendLifecycleEvent(ctx context.Context, tx exportTransaction, job domain.Job, stage string) error {
	if isNilDependency(repository.events) {
		return nil
	}
	stage = strings.ToLower(strings.TrimSpace(stage))
	if isNilDependency(tx) || isNilDependency(tx.Scope()) {
		return unavailable(errors.New("export lifecycle transaction scope is unavailable"))
	}
	_, replayed, err := repository.events.AppendScoped(ctx, tx.Scope(), eventsdomain.AppendRequest{
		WorkspaceID: job.WorkspaceID,
		Type:        "export." + stage,
		ResourceRef: "export_job:" + string(job.ID), ResourceVersion: job.Version,
		PayloadSummary: eventsdomain.PayloadSummary{
			Status:    strings.ToLower(string(job.Status)),
			Stage:     stage,
			ScopeKind: strings.ToLower(string(job.Scope.Kind)),
		},
		SchemaVersion:  1,
		SourceEventRef: "export." + stage + ":" + string(job.ID) + ":v" + formatVersion(job.Version),
		OccurredAt:     job.UpdatedAt,
	})
	if err != nil {
		return err
	}
	if replayed {
		return resultInvalid(errors.New("export lifecycle event version was already used"))
	}
	return nil
}

func formatVersion(version int64) string {
	return strconv.FormatInt(version, 10)
}
