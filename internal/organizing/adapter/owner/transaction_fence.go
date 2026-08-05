package owner

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"

	authoringdomain "github.com/CodeZen-Lizhi/zhixu/internal/authoring/domain"
	collectionapp "github.com/CodeZen-Lizhi/zhixu/internal/collection/application"
	collectiondomain "github.com/CodeZen-Lizhi/zhixu/internal/collection/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	knowledgepostgres "github.com/CodeZen-Lizhi/zhixu/internal/knowledge/adapter/postgres"
	knowledgedomain "github.com/CodeZen-Lizhi/zhixu/internal/knowledge/domain"
	organizingapp "github.com/CodeZen-Lizhi/zhixu/internal/organizing/application"
	organizingdomain "github.com/CodeZen-Lizhi/zhixu/internal/organizing/domain"
	retrievalpostgres "github.com/CodeZen-Lizhi/zhixu/internal/retrieval/adapter/postgres"
	retrievalapp "github.com/CodeZen-Lizhi/zhixu/internal/retrieval/application"
	retrievaldomain "github.com/CodeZen-Lizhi/zhixu/internal/retrieval/domain"
	"github.com/jackc/pgx/v5"
)

var _ organizingapp.FrozenMaterialFence = (*Adapter)(nil)

// VerifyFrozen locks and revalidates every frozen owner fact in the caller's
// confirmation transaction. It deliberately accepts an opaque transaction so
// pgx remains outside the application contract.
func (adapter *Adapter) VerifyFrozen(
	ctx context.Context,
	transaction any,
	workspaceID foundation.ID,
	references []organizingdomain.MaterialRef,
) error {
	tx, ok := transaction.(pgx.Tx)
	if adapter == nil || ctx == nil || !ok || nilDependency(tx) || !validID(workspaceID) ||
		len(references) == 0 || len(references) > organizingdomain.MaxSnapshotMaterials {
		return invalid("organizing material transaction fence request is invalid")
	}
	canonical := make([]organizingdomain.MaterialRef, len(references))
	for index, reference := range references {
		value, err := organizingdomain.CanonicalMaterialRef(reference)
		if err != nil {
			return err
		}
		canonical[index] = value
	}
	if err := verifyFrozenSourceFacts(ctx, tx, workspaceID, canonical); err != nil {
		return err
	}
	if err := verifyFrozenDocumentFacts(ctx, tx, workspaceID, canonical); err != nil {
		return err
	}
	if err := verifyFrozenClaimFacts(ctx, tx, workspaceID, canonical); err != nil {
		return err
	}
	if err := verifyFrozenCollectionFacts(ctx, tx, workspaceID, canonical); err != nil {
		return err
	}
	return verifyFrozenEvidenceFacts(ctx, tx, workspaceID, canonical)
}

func verifyFrozenSourceFacts(ctx context.Context, tx pgx.Tx, workspaceID foundation.ID, references []organizingdomain.MaterialRef) error {
	expected := make(map[foundation.ID]organizingdomain.MaterialRef)
	for _, reference := range references {
		if reference.Kind == organizingdomain.MaterialSourceVersion {
			expected[reference.SourceVersionID] = reference
		}
	}
	if len(expected) == 0 {
		return nil
	}
	ids := sortedFoundationIDs(expected)
	idStrings := idsToStrings(ids)
	rows, err := tx.Query(ctx, `SELECT sv.id::text,s.id::text
		FROM core.source_version AS sv
		JOIN core.source AS s ON s.id=sv.source_id AND s.workspace_id=$1 AND s.removed_at IS NULL
		JOIN core.content_artifact AS ca ON ca.id=sv.content_artifact_id AND ca.workspace_id=s.workspace_id
		WHERE sv.id=ANY($2::uuid[])
		ORDER BY sv.id
		FOR SHARE OF sv,s,ca`, string(workspaceID), idStrings)
	if err != nil {
		return fenceStorageFailure(err)
	}
	sourceIDs := make([]string, 0, len(ids))
	for rows.Next() {
		var sourceVersionID, sourceID string
		if err := rows.Scan(&sourceVersionID, &sourceID); err != nil {
			rows.Close()
			return fenceStorageFailure(err)
		}
		sourceIDs = append(sourceIDs, sourceID)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return fenceStorageFailure(err)
	}
	rows.Close()
	if len(sourceIDs) != len(ids) {
		return stale("source material is no longer available")
	}
	if err := lockLatestIngestionAttempts(ctx, tx, workspaceID, idStrings); err != nil {
		return err
	}
	if err := lockActiveSourceManifests(ctx, tx, workspaceID, sourceIDs); err != nil {
		return err
	}
	store, err := retrievalpostgres.NewSearchRepository(tx)
	if err != nil {
		return fenceOwnerReadFailure(err, "source material is no longer available")
	}
	for start := 0; start < len(ids); start += retrievalapp.MaxSourceVersionBatchSize {
		end := start + retrievalapp.MaxSourceVersionBatchSize
		if end > len(ids) {
			end = len(ids)
		}
		items, loadErr := store.LoadSourceVersionReferences(ctx, workspaceID, ids[start:end])
		if loadErr != nil {
			return fenceOwnerReadFailure(loadErr, "source material is no longer available")
		}
		if len(items) != end-start {
			return stale("source material is no longer available")
		}
		for index, item := range items {
			want := expected[ids[start+index]]
			if item.SourceVersionID != want.SourceVersionID || item.WorkspaceID != workspaceID ||
				item.ContentHash != want.ContentHash || sourceAvailability(item) != organizingdomain.MaterialAvailable {
				return stale("source material changed before confirmation")
			}
		}
	}
	return verifyFrozenProfiles(ctx, tx, workspaceID, expected, idStrings)
}

func lockLatestIngestionAttempts(ctx context.Context, tx pgx.Tx, workspaceID foundation.ID, sourceVersionIDs []string) error {
	rows, err := tx.Query(ctx, `WITH latest AS (
		SELECT DISTINCT ON (source_version_id) id
		FROM ingestion.attempt
		WHERE workspace_id=$1 AND source_version_id=ANY($2::uuid[])
		ORDER BY source_version_id,started_at DESC,id DESC
	)
	SELECT attempt.id::text
	FROM latest JOIN ingestion.attempt AS attempt ON attempt.id=latest.id
	ORDER BY attempt.id
	FOR SHARE OF attempt`, string(workspaceID), sourceVersionIDs)
	return consumeFenceRows(rows, err)
}

func lockActiveSourceManifests(ctx context.Context, tx pgx.Tx, workspaceID foundation.ID, sourceIDs []string) error {
	rows, err := tx.Query(ctx, `SELECT index_version.id::text
		FROM retrieval.index_version AS index_version
		WHERE index_version.workspace_id=$1 AND index_version.status='active'
		ORDER BY index_version.id
		FOR SHARE OF index_version`, string(workspaceID))
	if err := consumeFenceRows(rows, err); err != nil {
		return err
	}
	rows, err = tx.Query(ctx, `SELECT manifest.index_version_id::text
		FROM retrieval.index_manifest_source AS manifest
		JOIN retrieval.index_version AS index_version
		  ON index_version.id=manifest.index_version_id
		 AND index_version.workspace_id=manifest.workspace_id
		 AND index_version.status='active'
		WHERE manifest.workspace_id=$1 AND manifest.source_id=ANY($2::uuid[])
		ORDER BY manifest.index_version_id,manifest.source_id
		FOR SHARE OF manifest,index_version`, string(workspaceID), sourceIDs)
	return consumeFenceRows(rows, err)
}

func verifyFrozenProfiles(
	ctx context.Context,
	tx pgx.Tx,
	workspaceID foundation.ID,
	expected map[foundation.ID]organizingdomain.MaterialRef,
	sourceVersionIDs []string,
) error {
	rows, err := tx.Query(ctx, `SELECT source_version_id::text,COALESCE(current_revision_id::text,''),status
		FROM learning.document_knowledge_profile
		WHERE workspace_id=$1 AND source_version_id=ANY($2::uuid[])
		ORDER BY source_version_id
		FOR SHARE`, string(workspaceID), sourceVersionIDs)
	if err != nil {
		return fenceStorageFailure(err)
	}
	type profileFact struct {
		revisionID foundation.ID
		status     string
	}
	actual := make(map[foundation.ID]profileFact, len(expected))
	for rows.Next() {
		var sourceVersionID, revisionID, status string
		if err := rows.Scan(&sourceVersionID, &revisionID, &status); err != nil {
			rows.Close()
			return fenceStorageFailure(err)
		}
		actual[foundation.ID(sourceVersionID)] = profileFact{revisionID: foundation.ID(revisionID), status: status}
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return fenceStorageFailure(err)
	}
	rows.Close()
	for sourceVersionID, want := range expected {
		fact := actual[sourceVersionID]
		if fact.revisionID != want.ProfileRevisionID || fact.status == "STALE" {
			return stale("source profile changed before confirmation")
		}
	}
	return nil
}

func verifyFrozenDocumentFacts(ctx context.Context, tx pgx.Tx, workspaceID foundation.ID, references []organizingdomain.MaterialRef) error {
	items := make([]organizingdomain.MaterialRef, 0)
	seen := make(map[string]struct{})
	for _, reference := range references {
		if reference.Kind != organizingdomain.MaterialDocumentRevision {
			continue
		}
		identity, _ := reference.IdentityKey()
		if _, duplicate := seen[identity]; duplicate {
			continue
		}
		seen[identity] = struct{}{}
		items = append(items, reference)
	}
	if len(items) == 0 {
		return nil
	}
	sort.Slice(items, func(i, j int) bool {
		if items[i].DocumentID != items[j].DocumentID {
			return items[i].DocumentID < items[j].DocumentID
		}
		return items[i].ArticleRevisionID < items[j].ArticleRevisionID
	})
	documentIDs, revisionIDs := make([]string, len(items)), make([]string, len(items))
	for index, item := range items {
		documentIDs[index], revisionIDs[index] = string(item.DocumentID), string(item.ArticleRevisionID)
	}
	rows, err := tx.Query(ctx, `WITH requested(document_id,revision_id,ordinality) AS (
		SELECT document_id,revision_id,ordinality
		FROM unnest($2::uuid[],$3::uuid[]) WITH ORDINALITY AS input(document_id,revision_id,ordinality)
	)
	SELECT requested.ordinality,d.id::text,r.id::text,d.lifecycle_status,r.revision_no,r.content_hash
	FROM requested
	JOIN core.document AS d ON d.id=requested.document_id AND d.workspace_id=$1
	JOIN core.article_revision AS r
	  ON r.id=requested.revision_id AND r.document_id=d.id AND r.workspace_id=d.workspace_id
	ORDER BY requested.ordinality
	FOR SHARE OF d,r`, string(workspaceID), documentIDs, revisionIDs)
	if err != nil {
		return fenceStorageFailure(err)
	}
	count := 0
	for rows.Next() {
		var ordinal int64
		var documentID, revisionID, lifecycle, contentHash string
		var revisionNo int64
		if err := rows.Scan(&ordinal, &documentID, &revisionID, &lifecycle, &revisionNo, &contentHash); err != nil {
			rows.Close()
			return fenceStorageFailure(err)
		}
		if ordinal < 1 || ordinal > int64(len(items)) {
			rows.Close()
			return inconsistent("authoring returned an invalid frozen revision ordinal")
		}
		want := items[ordinal-1]
		if foundation.ID(documentID) != want.DocumentID || foundation.ID(revisionID) != want.ArticleRevisionID ||
			authoringdomain.DocumentLifecycle(lifecycle) == authoringdomain.DocumentDeleted ||
			revisionNo != want.Version || contentHash != want.ContentHash {
			rows.Close()
			return stale("document material changed before confirmation")
		}
		count++
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return fenceStorageFailure(err)
	}
	rows.Close()
	if count != len(items) {
		return stale("document material is no longer available")
	}
	return nil
}

func verifyFrozenClaimFacts(ctx context.Context, tx pgx.Tx, workspaceID foundation.ID, references []organizingdomain.MaterialRef) error {
	expected := make(map[foundation.ID]organizingdomain.MaterialRef)
	for _, reference := range references {
		if reference.Kind == organizingdomain.MaterialClaim {
			expected[reference.ClaimID] = reference
		}
	}
	if len(expected) == 0 {
		return nil
	}
	ids := sortedFoundationIDs(expected)
	idStrings := idsToStrings(ids)
	rows, err := tx.Query(ctx, `SELECT id::text FROM core.claim
		WHERE workspace_id=$1 AND id=ANY($2::uuid[])
		ORDER BY id FOR SHARE`, string(workspaceID), idStrings)
	if err := consumeFenceRows(rows, err); err != nil {
		return err
	}
	rows, err = tx.Query(ctx, `SELECT id::text FROM core.claim_source
		WHERE workspace_id=$1 AND claim_id=ANY($2::uuid[])
		ORDER BY claim_id,id FOR SHARE`, string(workspaceID), idStrings)
	if err := consumeFenceRows(rows, err); err != nil {
		return err
	}
	repository, err := knowledgepostgres.NewRepository(tx)
	if err != nil {
		return fenceOwnerReadFailure(err, "claim material is no longer available")
	}
	claims, err := repository.BatchGetClaims(ctx, knowledgedomain.BatchGetClaimsQuery{
		WorkspaceID: workspaceID, IDs: ids, Limit: len(ids),
	})
	if err != nil {
		return fenceOwnerReadFailure(err, "claim material is no longer available")
	}
	actual := make(map[foundation.ID]claimState, len(claims))
	for _, item := range claims {
		provenance := make(map[provenanceKey]struct{}, len(item.Sources))
		for _, source := range item.Sources {
			provenance[provenanceKey{source.Provenance.SourceVersionID, source.Provenance.SourceSpanID}] = struct{}{}
		}
		actual[item.Claim.ID] = claimState{claim: item.Claim, provenance: provenance}
	}
	if len(actual) != len(expected) {
		return stale("claim material is no longer available")
	}
	for claimID, want := range expected {
		fact, found := actual[claimID]
		if !found || !formalClaim(fact.claim.Status) || fact.claim.Version != want.Version || fact.claim.Fingerprint != want.ContentHash {
			return stale("formal claim changed before confirmation")
		}
		for _, evidence := range want.Evidence {
			if _, found := fact.provenance[provenanceKey{evidence.SourceVersionID, evidence.SourceSpanID}]; !found {
				return stale("claim evidence is no longer owned by the claim")
			}
		}
	}
	return nil
}

func verifyFrozenCollectionFacts(ctx context.Context, tx pgx.Tx, workspaceID foundation.ID, references []organizingdomain.MaterialRef) error {
	expected := make(map[foundation.ID]organizingdomain.MaterialRef)
	for _, reference := range references {
		if reference.Kind == organizingdomain.MaterialSmartCollection {
			expected[reference.CollectionID] = reference
		}
	}
	if len(expected) == 0 {
		return nil
	}
	if len(expected) > maxSerialOwnerLookups {
		return capabilityUnavailable("owner transaction fence exceeds the bounded collection lookup limit")
	}
	ids := sortedFoundationIDs(expected)
	rows, err := tx.Query(ctx, `SELECT id::text,status,version,query_hash,query_definition
		FROM learning.smart_collection
		WHERE workspace_id=$1 AND id=ANY($2::uuid[])
		ORDER BY id FOR SHARE`, string(workspaceID), idsToStrings(ids))
	if err != nil {
		return fenceStorageFailure(err)
	}
	count := 0
	healthSensitive := make(map[foundation.ID]bool, len(expected))
	for rows.Next() {
		var id, status, queryHash string
		var version int64
		var queryRaw []byte
		if err := rows.Scan(&id, &status, &version, &queryHash, &queryRaw); err != nil {
			rows.Close()
			return fenceStorageFailure(err)
		}
		want, found := expected[foundation.ID(id)]
		if !found {
			rows.Close()
			return inconsistent("collection fence returned an out-of-scope aggregate")
		}
		if collectionapp.CollectionStatus(status) != collectionapp.CollectionStatusActive || version != want.Version || queryHash != want.QueryHash {
			rows.Close()
			return stale("smart collection changed before confirmation")
		}
		var query collectiondomain.Query
		if err := json.Unmarshal(queryRaw, &query); err != nil {
			rows.Close()
			return inconsistent("smart collection query cannot be decoded")
		}
		plan, err := collectionapp.CompileQuery(query)
		if err != nil || plan.Canonical.Hash != want.QueryHash {
			rows.Close()
			return inconsistent("smart collection query binding is inconsistent")
		}
		healthSensitive[foundation.ID(id)] = collectionQueryReferencesField(plan.Canonical.Definition, "health_issue_type")
		count++
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return fenceStorageFailure(err)
	}
	rows.Close()
	if count != len(expected) {
		return stale("smart collection is no longer available")
	}
	knowledgeRevision, healthRevision, err := lockCollectionReadModelRevision(ctx, tx, workspaceID)
	if err != nil {
		return err
	}
	for _, collectionID := range ids {
		want := expected[collectionID]
		if collectionReadModelRevision(knowledgeRevision, healthRevision, healthSensitive[collectionID]) != want.ReadModelRevision {
			return stale("smart collection changed before confirmation")
		}
	}
	return nil
}

func lockCollectionReadModelRevision(ctx context.Context, tx pgx.Tx, workspaceID foundation.ID) (int64, int64, error) {
	var lockedWorkspace string
	if err := tx.QueryRow(ctx, `SELECT id::text FROM core.workspace WHERE id=$1 FOR SHARE`, string(workspaceID)).Scan(&lockedWorkspace); err != nil {
		return 0, 0, fenceStorageFailure(err)
	}
	var knowledgeRevision, healthRevision int64
	err := tx.QueryRow(ctx, `SELECT knowledge_revision,health_revision
		FROM core.workspace_read_model_revision WHERE workspace_id=$1 FOR SHARE`, string(workspaceID)).Scan(
		&knowledgeRevision, &healthRevision,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, 0, nil
	}
	if err != nil {
		return 0, 0, fenceStorageFailure(err)
	}
	return knowledgeRevision, healthRevision, nil
}

func collectionReadModelRevision(knowledgeRevision, healthRevision int64, includeHealth bool) string {
	if !includeHealth {
		healthRevision = 0
	}
	// This is the persisted Collection durable-scan revision contract.
	value := fmt.Sprintf("collection-revision/v5|conflicts=false|health=%t|knowledge=%d|conflict=0|health_issue=%d",
		includeHealth, knowledgeRevision, healthRevision)
	digest := sha256.Sum256([]byte(value))
	return hex.EncodeToString(digest[:])
}

func collectionQueryReferencesField(query collectiondomain.Query, field string) bool {
	var references func(collectiondomain.Clause) bool
	references = func(clause collectiondomain.Clause) bool {
		if clause.Kind == collectiondomain.ClauseKindPredicate {
			return strings.EqualFold(strings.TrimSpace(clause.Field), field)
		}
		for _, child := range clause.Clauses {
			if references(child) {
				return true
			}
		}
		return false
	}
	if references(query.Root) {
		return true
	}
	for _, term := range query.Sort {
		if strings.EqualFold(strings.TrimSpace(term.Field), field) {
			return true
		}
	}
	return false
}

func verifyFrozenEvidenceFacts(ctx context.Context, tx pgx.Tx, workspaceID foundation.ID, references []organizingdomain.MaterialRef) error {
	expected := make(map[citationKey]organizingdomain.EvidenceRef)
	for _, reference := range references {
		for _, evidence := range reference.Evidence {
			query := citationQuery(workspaceID, evidence)
			key := citationKeyFor(query)
			if prior, exists := expected[key]; exists && prior != evidence {
				return inconsistent("organizing materials disagree on one frozen citation")
			}
			expected[key] = evidence
		}
	}
	if len(expected) == 0 {
		return nil
	}
	byIndex := make(map[foundation.ID][]retrievaldomain.CitationReferenceQuery)
	for key := range expected {
		query := retrievaldomain.CitationReferenceQuery{WorkspaceID: workspaceID, IndexVersionID: key.indexVersionID,
			ChunkID: key.chunkID, SourceVersionID: key.sourceVersionID, SourceSpanID: key.sourceSpanID}
		byIndex[key.indexVersionID] = append(byIndex[key.indexVersionID], query)
	}
	indexIDs := make([]foundation.ID, 0, len(byIndex))
	for indexID := range byIndex {
		indexIDs = append(indexIDs, indexID)
	}
	sort.Slice(indexIDs, func(i, j int) bool { return indexIDs[i] < indexIDs[j] })
	for _, indexID := range indexIDs {
		queries := byIndex[indexID]
		sort.Slice(queries, func(i, j int) bool {
			left, right := citationKeyFor(queries[i]), citationKeyFor(queries[j])
			if left.chunkID != right.chunkID {
				return left.chunkID < right.chunkID
			}
			if left.sourceVersionID != right.sourceVersionID {
				return left.sourceVersionID < right.sourceVersionID
			}
			return left.sourceSpanID < right.sourceSpanID
		})
		for start := 0; start < len(queries); start += maxCitationBatchSize {
			end := start + maxCitationBatchSize
			if end > len(queries) {
				end = len(queries)
			}
			if err := verifyFrozenEvidenceBatch(ctx, tx, expected, queries[start:end]); err != nil {
				return err
			}
		}
	}
	return nil
}

func verifyFrozenEvidenceBatch(
	ctx context.Context,
	tx pgx.Tx,
	expected map[citationKey]organizingdomain.EvidenceRef,
	queries []retrievaldomain.CitationReferenceQuery,
) error {
	workspaceIDs, indexIDs, chunkIDs := make([]string, len(queries)), make([]string, len(queries)), make([]string, len(queries))
	sourceVersionIDs, sourceSpanIDs := make([]string, len(queries)), make([]string, len(queries))
	for index, query := range queries {
		workspaceIDs[index], indexIDs[index], chunkIDs[index] = string(query.WorkspaceID), string(query.IndexVersionID), string(query.ChunkID)
		sourceVersionIDs[index], sourceSpanIDs[index] = string(query.SourceVersionID), string(query.SourceSpanID)
	}
	rows, err := tx.Query(ctx, `WITH requested AS MATERIALIZED (
		SELECT workspace_id,index_version_id,chunk_id,source_version_id,source_span_id,ordinality
		FROM unnest($1::uuid[],$2::uuid[],$3::uuid[],$4::uuid[],$5::uuid[])
		WITH ORDINALITY AS value(workspace_id,index_version_id,chunk_id,source_version_id,source_span_id,ordinality)
	)
	SELECT requested.ordinality,sv.content_hash,sp.excerpt_hash
	FROM requested
	JOIN retrieval.index_version AS idx
	  ON idx.id=requested.index_version_id AND idx.workspace_id=requested.workspace_id AND idx.status='active'
	JOIN retrieval.index_manifest_chunk AS manifest
	  ON manifest.index_version_id=idx.id AND manifest.workspace_id=idx.workspace_id AND manifest.chunk_id=requested.chunk_id
	JOIN ingestion.canonical_chunk AS chunk
	  ON chunk.id=manifest.chunk_id AND chunk.workspace_id=manifest.workspace_id AND chunk.content_hash=manifest.content_hash
	JOIN retrieval.index_manifest_source AS source_manifest
	  ON source_manifest.index_version_id=idx.id AND source_manifest.workspace_id=idx.workspace_id
	 AND source_manifest.source_version_id=requested.source_version_id
	 AND source_manifest.parse_projection_id=chunk.parse_projection_id AND source_manifest.selection_status='included'
	JOIN core.source_version AS sv
	  ON sv.id=source_manifest.source_version_id AND sv.source_id=source_manifest.source_id
	JOIN core.source AS source
	  ON source.id=source_manifest.source_id AND source.workspace_id=idx.workspace_id
	 AND source.removed_at IS NULL
	JOIN core.content_artifact AS artifact
	  ON artifact.id=sv.content_artifact_id AND artifact.workspace_id=idx.workspace_id
	 AND artifact.content_hash=sv.content_hash AND artifact.byte_size=sv.byte_size
	JOIN ingestion.parse_projection AS projection
	  ON projection.id=source_manifest.parse_projection_id AND projection.workspace_id=idx.workspace_id
	 AND projection.content_artifact_id=artifact.id
	JOIN ingestion.source_span AS sp
	  ON sp.id=chunk.source_span_id AND sp.id=requested.source_span_id AND sp.workspace_id=idx.workspace_id
	 AND sp.content_artifact_id=artifact.id AND sp.parse_projection_id=projection.id
	 AND sp.parser_version=projection.parser_version AND sp.schema_version=projection.schema_version
	ORDER BY requested.ordinality
	FOR SHARE OF idx,manifest,chunk,source_manifest,sv,source,artifact,projection,sp`,
		workspaceIDs, indexIDs, chunkIDs, sourceVersionIDs, sourceSpanIDs)
	if err != nil {
		return fenceStorageFailure(err)
	}
	count := 0
	for rows.Next() {
		var ordinal int64
		var contentHash, excerptHash string
		if err := rows.Scan(&ordinal, &contentHash, &excerptHash); err != nil {
			rows.Close()
			return fenceStorageFailure(err)
		}
		if ordinal < 1 || ordinal > int64(len(queries)) {
			rows.Close()
			return inconsistent("citation fence returned an invalid ordinal")
		}
		want := expected[citationKeyFor(queries[ordinal-1])]
		if contentHash != want.ContentHash || excerptHash != want.ExcerptHash {
			rows.Close()
			return stale("organizing citation changed before confirmation")
		}
		count++
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return fenceStorageFailure(err)
	}
	rows.Close()
	if count != len(queries) {
		return stale("organizing citation is no longer available")
	}
	return nil
}

func sortedFoundationIDs[T any](values map[foundation.ID]T) []foundation.ID {
	result := make([]foundation.ID, 0, len(values))
	for id := range values {
		result = append(result, id)
	}
	sort.Slice(result, func(i, j int) bool { return result[i] < result[j] })
	return result
}

func idsToStrings(ids []foundation.ID) []string {
	result := make([]string, len(ids))
	for index, id := range ids {
		result[index] = string(id)
	}
	return result
}

func consumeFenceRows(rows pgx.Rows, err error) error {
	if err != nil {
		return fenceStorageFailure(err)
	}
	defer rows.Close()
	for rows.Next() {
		var ignored string
		if err := rows.Scan(&ignored); err != nil {
			return fenceStorageFailure(err)
		}
	}
	if err := rows.Err(); err != nil {
		return fenceStorageFailure(err)
	}
	return nil
}

func fenceStorageFailure(err error) error {
	return foundation.NewError(foundation.ErrorDependencyUnavailable, ErrorCodeOwnerDependencyUnavailable, true, err)
}

func fenceOwnerReadFailure(err error, staleMessage string) error {
	var classified *foundation.Error
	if errors.As(err, &classified) {
		switch classified.Kind {
		case foundation.ErrorNotFound, foundation.ErrorVersionConflict:
			return stale(staleMessage)
		case foundation.ErrorConsistencyViolation:
			return inconsistent("owner transaction fence returned inconsistent facts")
		case foundation.ErrorInvalidInput:
			return inconsistent("owner transaction fence rejected a canonical batch")
		default:
			return fenceStorageFailure(err)
		}
	}
	return fenceStorageFailure(err)
}
