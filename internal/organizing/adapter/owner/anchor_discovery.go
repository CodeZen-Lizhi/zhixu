package owner

import (
	"context"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	app "github.com/CodeZen-Lizhi/zhixu/internal/organizing/application"
	"github.com/CodeZen-Lizhi/zhixu/internal/organizing/domain"
)

var _ app.AnchorDiscoverySourceResolver = (*GORMSynthesisSourceReader)(nil)

func (reader *GORMSynthesisSourceReader) ResolveAnchorDiscoverySources(ctx context.Context, source domain.SynthesisSourceVersion, ids []foundation.ID) ([]domain.SynthesisSourceRef, error) {
	if err := reader.ready(ctx); err != nil {
		return nil, err
	}
	if source.Validate() != nil || len(ids) < 1 || len(ids) > domain.MaxSynthesisSources {
		return nil, synthesisSourceError(foundation.ErrorInvalidInput, "ANCHOR_DISCOVERY_SOURCE_INVALID", false)
	}
	seen := make(map[foundation.ID]bool, len(ids))
	for _, id := range ids {
		if !validID(id) || seen[id] {
			return nil, synthesisSourceError(foundation.ErrorInvalidInput, "ANCHOR_DISCOVERY_SOURCE_INVALID", false)
		}
		seen[id] = true
	}
	state, found, err := reader.state(ctx, reader.database, source)
	if err != nil {
		return nil, err
	}
	if !found || state.RemovedAt != nil {
		return nil, synthesisSourceError(foundation.ErrorNotFound, "SYNTHESIS_SOURCE_NOT_FOUND", false)
	}
	if !state.Current || state.Derived {
		return nil, synthesisSourceError(foundation.ErrorVersionConflict, "SYNTHESIS_SOURCE_STALE", false)
	}
	var rows []synthesisSpanRow
	err = reader.database.WithContext(ctx).Table("ingestion.source_span sp").Select("sp.id,sp.excerpt_hash").
		Joins("JOIN ingestion.parse_projection pp ON pp.id=sp.parse_projection_id AND pp.workspace_id=sp.workspace_id AND pp.content_artifact_id=sp.content_artifact_id AND pp.parser_version=sp.parser_version AND pp.schema_version=sp.schema_version").
		Where("sp.workspace_id=? AND sp.content_artifact_id=? AND sp.parse_projection_id=? AND sp.id IN ?", string(source.WorkspaceID), string(source.ContentArtifactID), string(source.ParseProjectionID), ids).Order("sp.id").Limit(len(ids)).Find(&rows).Error
	if err != nil {
		return nil, synthesisSourceDBError(ctx, err)
	}
	if len(rows) != len(ids) {
		return nil, synthesisSourceError(foundation.ErrorVersionConflict, "ANCHOR_DISCOVERY_SPAN_STALE", false)
	}
	refs := make([]domain.SynthesisSourceRef, len(rows))
	for i, row := range rows {
		refs[i] = domain.SynthesisSourceRef{Source: source, SourceSpanID: foundation.ID(row.ID), ExcerptHash: row.ExcerptHash, Title: state.Title}
		if refs[i].Validate() != nil {
			return nil, synthesisSourceError(foundation.ErrorConsistencyViolation, "ANCHOR_DISCOVERY_SPAN_INVALID", false)
		}
	}
	return refs, nil
}
