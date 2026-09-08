package postgres

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"strconv"
	"strings"
	"time"

	collectionapp "github.com/CodeZen-Lizhi/zhixu/internal/collection/application"
	"github.com/CodeZen-Lizhi/zhixu/internal/collection/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	"github.com/lib/pq"
	"gorm.io/gorm"
)

const collectionItemColumns = `item.id,item.workspace_id,item.object_type,item.topic_id,
item.title,item.summary,item.status,item.confidence,item.relation_type,item.health_issue_type,
item.source_type,item.file_path,item.search_text,item.created_at,item.updated_at`

const defaultCollectionStatementTimeout = 1500 * time.Millisecond

const setLocalCollectionStatementTimeoutSQL = `SELECT pg_catalog.set_config('statement_timeout',?,true)`

const unifiedItemCTE = `WITH item AS (
    SELECT t.id::text AS id, t.workspace_id::text AS workspace_id, 'TOPIC'::text AS object_type,
           t.id::text AS topic_id, t.name AS title, t.description AS summary, t.status,
           NULL::double precision AS confidence, relation.relation_type,
           hi.issue_type AS health_issue_type, NULL::text AS source_type, NULL::text AS file_path,
	       concat_ws(' ',t.normalized_name,lower(t.description),aliases.aliases) AS search_text, t.created_at, t.updated_at
    FROM core.topic t
	LEFT JOIN LATERAL (
		SELECT string_agg(a.normalized_alias,' ' ORDER BY a.normalized_alias,a.id) AS aliases
		FROM core.topic_alias a
		WHERE a.workspace_id=t.workspace_id AND a.topic_id=t.id
	) aliases ON true
	LEFT JOIN LATERAL (
		SELECT related.relation_type
		FROM (
			SELECT r.relation_type, r.updated_at, r.id
			FROM core.relation r
			WHERE r.workspace_id=t.workspace_id AND r.source_node_type='TOPIC'
			  AND r.source_node_id=t.id AND r.status='CONFIRMED'
			UNION ALL
			SELECT r.relation_type, r.updated_at, r.id
			FROM core.relation r
			WHERE r.workspace_id=t.workspace_id AND r.target_node_type='TOPIC'
			  AND r.target_node_id=t.id AND r.status='CONFIRMED'
		) related
		ORDER BY related.updated_at DESC, related.id LIMIT 1
	) relation ON true
    LEFT JOIN LATERAL (
		SELECT i.type AS issue_type FROM ops.health_issue i
        WHERE i.workspace_id=t.workspace_id AND i.target_type='TOPIC' AND i.target_id=t.id
          AND i.status NOT IN ('RESOLVED','IGNORED','FALSE_POSITIVE')
		ORDER BY i.updated_at DESC, i.id LIMIT 1
    ) hi ON true
    WHERE t.workspace_id=@workspace
    UNION ALL
    SELECT c.id::text, c.workspace_id::text, 'CLAIM'::text,
           membership.topic_id::text, c.statement, c.statement, c.status,
           c.confidence_score, relation.relation_type, hi.issue_type,
           source_info.source_type, source_info.file_path,
	       c.normalized_statement, c.created_at, c.updated_at
    FROM core.claim c
    LEFT JOIN LATERAL (
        SELECT r.target_node_id AS topic_id FROM core.relation r
        WHERE r.workspace_id=c.workspace_id AND r.source_node_type='CLAIM' AND r.source_node_id=c.id
          AND r.target_node_type='TOPIC' AND r.relation_type='BELONGS_TO' AND r.status='CONFIRMED'
        ORDER BY r.target_node_id LIMIT 1
    ) membership ON true
    LEFT JOIN LATERAL (
		SELECT related.relation_type
		FROM (
			SELECT r.relation_type, r.updated_at, r.id
			FROM core.relation r
			WHERE r.workspace_id=c.workspace_id AND r.source_node_type='CLAIM'
			  AND r.source_node_id=c.id AND r.status='CONFIRMED'
			UNION ALL
			SELECT r.relation_type, r.updated_at, r.id
			FROM core.relation r
			WHERE r.workspace_id=c.workspace_id AND r.target_node_type='CLAIM'
			  AND r.target_node_id=c.id AND r.status='CONFIRMED'
		) related
		ORDER BY related.updated_at DESC, related.id LIMIT 1
	) relation ON true
    LEFT JOIN LATERAL (
		SELECT i.type AS issue_type FROM ops.health_issue i
        WHERE i.workspace_id=c.workspace_id AND i.target_type='CLAIM' AND i.target_id=c.id
          AND i.status NOT IN ('RESOLVED','IGNORED','FALSE_POSITIVE')
		ORDER BY i.updated_at DESC, i.id LIMIT 1
    ) hi ON true
    LEFT JOIN LATERAL (
		SELECT s.type AS source_type, s.original_location AS file_path
        FROM core.claim_source cs
        JOIN core.source_version sv ON sv.id=cs.source_version_id
        JOIN core.source s ON s.id=sv.source_id AND s.workspace_id=c.workspace_id
        WHERE cs.workspace_id=c.workspace_id AND cs.claim_id=c.id
		ORDER BY cs.created_at DESC, cs.id LIMIT 1
    ) source_info ON true
    WHERE c.workspace_id=@workspace
) `

const hydrateCollectionItemsSQL = `WITH requested AS (
    SELECT input.object_type,input.id,input.ordinality
    FROM unnest((@object_types)::text[],(@ids)::uuid[]) WITH ORDINALITY AS input(object_type,id,ordinality)
), topic_aliases AS (
    SELECT a.topic_id AS id,jsonb_agg(a.alias ORDER BY a.normalized_alias,a.id) AS aliases
    FROM core.topic_alias a
    JOIN requested requested_topic ON requested_topic.object_type='TOPIC' AND requested_topic.id=a.topic_id
    WHERE a.workspace_id=@workspace
    GROUP BY a.topic_id
), claim_details AS (
    SELECT c.id,c.applicability,c.applicability_schema_version,c.applicability_hash
    FROM core.claim c
    JOIN requested requested_claim ON requested_claim.object_type='CLAIM' AND requested_claim.id=c.id
    WHERE c.workspace_id=@workspace
	), claim_sources_ranked AS (
	SELECT cs.claim_id AS id,s.type,s.original_location,cs.support_type,cs.created_at,cs.id AS source_id,
		row_number() OVER (PARTITION BY cs.claim_id ORDER BY cs.created_at DESC,cs.id) AS row_number
	FROM core.claim_source cs
	JOIN requested requested_claim ON requested_claim.object_type='CLAIM' AND requested_claim.id=cs.claim_id
	JOIN core.source_version sv ON sv.id=cs.source_version_id
	JOIN core.source s ON s.id=sv.source_id AND s.workspace_id=@workspace
	WHERE cs.workspace_id=@workspace
	), claim_sources AS (
	SELECT id,jsonb_agg(jsonb_build_object(
		'source_type',type,'file_path',original_location,
		'support_type',support_type,'created_at',created_at
	) ORDER BY created_at DESC,source_id) AS source_summaries
	FROM claim_sources_ranked
	WHERE row_number <= 32
	GROUP BY id
), relation_edges AS (
    SELECT r.source_node_type AS object_type,r.source_node_id AS id,r.relation_type
    FROM core.relation r
    JOIN requested source_item ON source_item.object_type=r.source_node_type AND source_item.id=r.source_node_id
    WHERE r.workspace_id=@workspace AND r.status='CONFIRMED'
    UNION ALL
    SELECT r.target_node_type,r.target_node_id,r.relation_type
    FROM core.relation r
    JOIN requested target_item ON target_item.object_type=r.target_node_type AND target_item.id=r.target_node_id
    WHERE r.workspace_id=@workspace AND r.status='CONFIRMED'
), relation_types AS (
    SELECT object_type,id,to_jsonb(array_agg(DISTINCT relation_type ORDER BY relation_type)) AS types
    FROM relation_edges
    GROUP BY object_type,id
), health_summary AS (
    SELECT i.target_type AS object_type,i.target_id AS id,count(*) AS issue_count,
           (array_agg(i.severity ORDER BY CASE i.severity WHEN 'CRITICAL' THEN 4 WHEN 'HIGH' THEN 3 WHEN 'MEDIUM' THEN 2 ELSE 1 END DESC,i.updated_at DESC,i.id))[1] AS max_severity,
           to_jsonb(array_agg(DISTINCT i.type ORDER BY i.type)) AS issue_types,
           left(string_agg(i.evidence_summary,'; ' ORDER BY i.updated_at DESC,i.id),4096) AS evidence_summary
    FROM ops.health_issue i
    JOIN requested target_item ON target_item.object_type=i.target_type AND target_item.id=i.target_id
    WHERE i.workspace_id=@workspace AND i.status NOT IN ('RESOLVED','IGNORED','FALSE_POSITIVE')
    GROUP BY i.target_type,i.target_id
)
SELECT requested.object_type,requested.id::text,
       COALESCE(topic_aliases.aliases,'[]'::jsonb),claim_details.applicability,
       claim_details.applicability_schema_version,claim_details.applicability_hash,
       COALESCE(claim_sources.source_summaries,'[]'::jsonb),
       COALESCE(relation_types.types,'[]'::jsonb),COALESCE(health_summary.issue_count,0)::bigint,
       health_summary.max_severity,COALESCE(health_summary.issue_types,'[]'::jsonb),health_summary.evidence_summary
FROM requested
LEFT JOIN topic_aliases ON requested.object_type='TOPIC' AND topic_aliases.id=requested.id
LEFT JOIN claim_details ON requested.object_type='CLAIM' AND claim_details.id=requested.id
LEFT JOIN claim_sources ON requested.object_type='CLAIM' AND claim_sources.id=requested.id
LEFT JOIN relation_types ON relation_types.object_type=requested.object_type AND relation_types.id=requested.id
LEFT JOIN health_summary ON health_summary.object_type=requested.object_type AND health_summary.id=requested.id
ORDER BY requested.ordinality`

type queryExecution struct {
	workspaceID       foundation.ID
	collectionID      foundation.ID
	collectionVersion int64
	queryHash         string
	query             domain.Query
	limit             int
	cursor            string
	scope             collectionapp.CursorScope
}

func (r *GORMRepository) executeQuerySnapshot(ctx context.Context, db *gorm.DB, request collectionapp.ResultsQuery) (collectionapp.ResultPage, error) {
	collection, err := loadCollection(ctx, db, request.WorkspaceID, request.CollectionID, false)
	if err != nil {
		return collectionapp.ResultPage{}, err
	}
	return r.executePlanSnapshot(ctx, db, queryExecution{
		workspaceID: request.WorkspaceID, collectionID: request.CollectionID,
		collectionVersion: collection.Version, queryHash: collection.QueryHash, query: collection.Query,
		limit: request.Limit, cursor: request.Cursor, scope: collectionapp.CursorScopeSaved,
	})
}

func (r *GORMRepository) executePlanSnapshot(ctx context.Context, db *gorm.DB, execution queryExecution) (collectionapp.ResultPage, error) {
	plan, err := collectionapp.CompileQuery(execution.query)
	if err != nil {
		return collectionapp.ResultPage{}, err
	}
	if plan.Canonical.Hash != execution.queryHash {
		return collectionapp.ResultPage{}, inconsistent(errors.New("collection query hash is inconsistent"))
	}
	// 结果页总是 hydrate active Health 摘要，因此游标必须绑定 health revision；
	// durable scan revision 则只绑定会改变成员集合的事实，避免 Health scan 被自己的输出打断。
	revision, scanRevision, err := readCollectionResultRevisions(ctx, db, execution.workspaceID, plan)
	if err != nil {
		return collectionapp.ResultPage{}, err
	}
	where, args := plan.Where, collectionQueryArguments(execution.workspaceID, plan.Args)
	countWhere := where
	countArgs := maps.Clone(args)
	sortBytes, _ := json.Marshal(plan.Sort)
	sortDigest := sha256.Sum256(sortBytes)
	sortHash := hex.EncodeToString(sortDigest[:])
	if execution.cursor != "" {
		cursor, decodeErr := r.cursor.Decode(execution.cursor, collectionapp.CursorBinding{
			Scope: execution.scope, WorkspaceID: execution.workspaceID, CollectionID: execution.collectionID,
			CollectionVersion: execution.collectionVersion, QueryHash: execution.queryHash,
			SortHash: sortHash, Limit: execution.limit, RevisionHash: revision,
		})
		if decodeErr != nil {
			return collectionapp.ResultPage{}, decodeErr
		}
		keyset, keysetArgs, keysetErr := buildKeysetPredicate(plan.Sort, cursor)
		if keysetErr != nil {
			return collectionapp.ResultPage{}, keysetErr
		}
		where += " AND " + keyset
		maps.Copy(args, keysetArgs)
	}
	countSQL := "SELECT count(*) FROM (" + unifiedItemCTE + "SELECT 1 FROM item WHERE " + countWhere + ") counted"
	var count int64
	if err := db.WithContext(ctx).Raw(countSQL, countArgs).Row().Scan(&count); err != nil {
		return collectionapp.ResultPage{}, classify(err)
	}
	pageSQL, pageArgs := buildCollectionPageQuery(plan, where, args, execution.limit+1)
	rows, err := db.WithContext(ctx).Raw(pageSQL, pageArgs).Rows()
	if err != nil {
		return collectionapp.ResultPage{}, classify(err)
	}
	defer rows.Close()
	items := make([]collectionapp.CollectionItem, 0, execution.limit)
	for rows.Next() {
		item, scanErr := scanItem(rows)
		if scanErr != nil {
			return collectionapp.ResultPage{}, scanErr
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return collectionapp.ResultPage{}, classify(err)
	}
	page := collectionapp.ResultPage{ExactCount: count, QueryHash: execution.queryHash, RevisionHash: revision, ScanRevisionHash: scanRevision, Items: items}
	if len(items) > execution.limit {
		last := items[execution.limit-1]
		items = items[:execution.limit]
		page.Items = items
		cursor, cursorErr := r.cursor.Encode(collectionapp.ResultCursor{
			Scope: execution.scope, WorkspaceID: execution.workspaceID, CollectionID: execution.collectionID,
			CollectionVersion: execution.collectionVersion, QueryHash: execution.queryHash,
			SortHash: sortHash, Limit: execution.limit, RevisionHash: revision,
			LastObjectType: last.ObjectType, LastID: last.ID, LastSortValues: itemSortValues(last, plan.Sort),
		})
		if cursorErr != nil {
			return collectionapp.ResultPage{}, cursorErr
		}
		page.NextCursor = cursor
	}
	if err := hydrateCollectionItems(ctx, db, execution.workspaceID, page.Items); err != nil {
		return collectionapp.ResultPage{}, err
	}
	return page, nil
}

func buildCollectionPageQuery(plan collectionapp.QueryPlan, where string, args map[string]any, limit int) (string, map[string]any) {
	sortSQL := make([]string, 0, len(plan.Sort))
	for _, sortTerm := range plan.Sort {
		sortSQL = append(sortSQL, sortTerm.Column+" "+sortTerm.Direction+" NULLS LAST")
	}
	pageSQL := unifiedItemCTE + "SELECT " + collectionItemColumns + " FROM item WHERE " + where + " ORDER BY " + strings.Join(sortSQL, ", ") + " LIMIT @limit"
	pageArgs := maps.Clone(args)
	pageArgs["limit"] = limit
	return pageSQL, pageArgs
}

type revisionOptions struct {
	includeConflicts    bool
	includeHealthIssues bool
}

type readModelRevisionVector struct {
	knowledge int64
	conflict  int64
	health    int64
}

func loadReadModelRevisionVector(ctx context.Context, db *gorm.DB, workspaceID foundation.ID) (readModelRevisionVector, error) {
	var vector readModelRevisionVector
	const revisionSQL = `SELECT
 COALESCE(revision.knowledge_revision,0),
 COALESCE(revision.conflict_revision,0),
 COALESCE(revision.health_revision,0)
FROM core.workspace workspace
LEFT JOIN core.workspace_read_model_revision revision ON revision.workspace_id=workspace.id
WHERE workspace.id=@workspace`
	if err := db.WithContext(ctx).Raw(revisionSQL, map[string]any{"workspace": string(workspaceID)}).Row().Scan(&vector.knowledge, &vector.conflict, &vector.health); err != nil {
		return readModelRevisionVector{}, classify(err)
	}
	return vector, nil
}

func hashReadModelRevision(vector readModelRevisionVector, options revisionOptions) string {
	conflictRevision, healthRevision := int64(0), int64(0)
	if options.includeConflicts {
		conflictRevision = vector.conflict
	}
	if options.includeHealthIssues {
		healthRevision = vector.health
	}
	value := fmt.Sprintf("collection-revision/v5|conflicts=%t|health=%t|knowledge=%d|conflict=%d|health_issue=%d", options.includeConflicts, options.includeHealthIssues, vector.knowledge, conflictRevision, healthRevision)
	digest := sha256.Sum256([]byte(value))
	return hex.EncodeToString(digest[:])
}

func readModelRevisionWithOptions(ctx context.Context, db *gorm.DB, workspaceID foundation.ID, options revisionOptions) (string, error) {
	vector, err := loadReadModelRevisionVector(ctx, db, workspaceID)
	if err != nil {
		return "", err
	}
	return hashReadModelRevision(vector, options), nil
}

func readCollectionResultRevisions(ctx context.Context, db *gorm.DB, workspaceID foundation.ID, plan collectionapp.QueryPlan) (string, string, error) {
	vector, err := loadReadModelRevisionVector(ctx, db, workspaceID)
	if err != nil {
		return "", "", err
	}
	resultRevision := hashReadModelRevision(vector, revisionOptions{includeHealthIssues: true})
	scanRevision := hashReadModelRevision(vector, revisionOptions{includeHealthIssues: queryPlanReferencesField(plan, "health_issue_type")})
	return resultRevision, scanRevision, nil
}

func configureCollectionStatementTimeout(ctx context.Context, db *gorm.DB, timeout time.Duration) error {
	if timeout < time.Millisecond {
		return errors.New("collection statement timeout must be at least one millisecond")
	}
	return db.WithContext(ctx).Exec(setLocalCollectionStatementTimeoutSQL, strconv.FormatInt(timeout.Milliseconds(), 10)+"ms").Error
}

// collectionQueryArguments binds the compiler's named values without rewriting SQL.
func collectionQueryArguments(workspaceID foundation.ID, values map[string]any) map[string]any {
	args := make(map[string]any, len(values)+1)
	for name, value := range values {
		if array, ok := value.([]string); ok {
			value = pq.Array(array)
		}
		args[name] = value
	}
	args["workspace"] = string(workspaceID)
	return args
}

func scanItem(row interface{ Scan(...any) error }) (collectionapp.CollectionItem, error) {
	var item collectionapp.CollectionItem
	var id, workspace, objectType string
	var topicID, relationType, healthType, sourceType, filePath *string
	if err := row.Scan(&id, &workspace, &objectType, &topicID, &item.Title, &item.Summary, &item.Status, &item.Confidence, &relationType, &healthType, &sourceType, &filePath, &item.SortTextKey, &item.CreatedAt, &item.UpdatedAt); err != nil {
		return collectionapp.CollectionItem{}, classify(err)
	}
	if !validID(foundation.ID(workspace)) || !validID(foundation.ID(id)) ||
		(objectType != "TOPIC" && objectType != "CLAIM") || item.Title == "" || item.Status == "" ||
		item.CreatedAt.IsZero() || item.UpdatedAt.Before(item.CreatedAt) {
		return collectionapp.CollectionItem{}, inconsistent(errors.New("collection result identity or shape is invalid"))
	}
	item.ID, item.ObjectType = foundation.ID(id), objectType
	item.CreatedAt = item.CreatedAt.UTC()
	item.UpdatedAt = item.UpdatedAt.UTC()
	item.RelationType, item.HealthType, item.SourceType, item.FilePath = relationType, healthType, sourceType, filePath
	if topicID != nil && *topicID != "" {
		parsed := foundation.ID(*topicID)
		if !validID(parsed) {
			return collectionapp.CollectionItem{}, inconsistent(errors.New("collection result topic reference is invalid"))
		}
		item.TopicID = &parsed
	}
	return item, nil
}

func hydrateCollectionItems(ctx context.Context, db *gorm.DB, workspaceID foundation.ID, items []collectionapp.CollectionItem) error {
	objectTypes := make([]string, len(items))
	ids := make([]string, len(items))
	positions := make(map[string]int, len(items))
	for index, item := range items {
		if (item.ObjectType != "TOPIC" && item.ObjectType != "CLAIM") || !validID(item.ID) {
			return inconsistent(errors.New("collection hydration identity is invalid"))
		}
		key := item.ObjectType + "\x00" + string(item.ID)
		if _, exists := positions[key]; exists {
			return inconsistent(errors.New("collection hydration identity is duplicated"))
		}
		objectTypes[index], ids[index], positions[key] = item.ObjectType, string(item.ID), index
	}
	rows, err := db.WithContext(ctx).Raw(hydrateCollectionItemsSQL, map[string]any{
		"workspace": string(workspaceID), "object_types": pq.Array(objectTypes), "ids": pq.Array(ids),
	}).Rows()
	if err != nil {
		return classify(err)
	}
	defer rows.Close()
	seen := make(map[string]struct{}, len(items))
	for rows.Next() {
		var objectType, id string
		var aliasesRaw, sourceSummariesRaw, relationTypesRaw, healthIssueTypesRaw collectionJSONB
		var applicability *collectionJSONB
		var applicabilitySchemaVersion, applicabilityHash, maxSeverity, healthSummary *string
		var healthIssueCount int64
		if err := rows.Scan(&objectType, &id, &aliasesRaw, &applicability, &applicabilitySchemaVersion, &applicabilityHash, &sourceSummariesRaw, &relationTypesRaw, &healthIssueCount, &maxSeverity, &healthIssueTypesRaw, &healthSummary); err != nil {
			return classify(err)
		}
		key := objectType + "\x00" + id
		index, exists := positions[key]
		if !exists {
			return inconsistent(errors.New("collection hydration crossed page boundary"))
		}
		if _, duplicate := seen[key]; duplicate {
			return inconsistent(errors.New("collection hydration returned duplicate result"))
		}
		seen[key] = struct{}{}
		item := &items[index]
		item.Applicability = nil
		if applicability != nil {
			item.Applicability = append(item.Applicability, (*applicability)...)
		}
		if applicabilitySchemaVersion != nil {
			item.ApplicabilitySchemaVersion = *applicabilitySchemaVersion
		}
		if applicabilityHash != nil {
			item.ApplicabilityHash = *applicabilityHash
		}
		if err := json.Unmarshal(aliasesRaw, &item.Aliases); err != nil {
			return inconsistent(errors.New("collection topic aliases are invalid"))
		}
		if err := json.Unmarshal(sourceSummariesRaw, &item.SourceSummaries); err != nil {
			return inconsistent(errors.New("collection source summaries are invalid"))
		}
		if err := json.Unmarshal(relationTypesRaw, &item.RelationTypes); err != nil {
			return inconsistent(errors.New("collection relation types are invalid"))
		}
		var healthIssueTypes []string
		if err := json.Unmarshal(healthIssueTypesRaw, &healthIssueTypes); err != nil {
			return inconsistent(errors.New("collection health issue types are invalid"))
		}
		if healthIssueCount > 0 {
			item.HealthSummary = &collectionapp.CollectionHealthSummary{Count: int(healthIssueCount), IssueTypes: healthIssueTypes}
			if maxSeverity != nil {
				item.HealthSummary.MaxSeverity = *maxSeverity
			}
			if healthSummary != nil {
				item.HealthSummary.Summary = *healthSummary
			}
		}
	}
	if err := rows.Err(); err != nil {
		return classify(err)
	}
	if len(seen) != len(items) {
		return inconsistent(errors.New("collection hydration result is incomplete"))
	}
	return nil
}

func buildKeysetPredicate(sortTerms []collectionapp.SortExpression, cursor collectionapp.ResultCursor) (string, map[string]any, error) {
	values := cursor.LastSortValues
	if len(values) == 0 {
		values = []*string{stringPointer(cursor.LastObjectType), stringPointer(string(cursor.LastID))}
		sortTerms = []collectionapp.SortExpression{{Column: "item.object_type", Direction: "ASC"}, {Column: "item.id", Direction: "ASC"}}
	}
	if len(values) != len(sortTerms) {
		return "", nil, requestInvalid(errors.New("collection cursor sort key is invalid"))
	}
	args := make(map[string]any, len(values))
	for index, value := range values {
		name := "cursor" + strconv.Itoa(index+1)
		args[name] = nil
		if value != nil {
			args[name] = *value
		}
	}
	branches := make([]string, 0, len(sortTerms))
	for index, term := range sortTerms {
		parts := make([]string, 0, index+1)
		for prefix := 0; prefix < index; prefix++ {
			parts = append(parts, sortTerms[prefix].Column+" IS NOT DISTINCT FROM "+typedPlaceholder(sortTerms[prefix].Column, prefix+1))
		}
		operator := ">"
		if term.Direction == "DESC" {
			operator = "<"
		}
		placeholder := typedPlaceholder(term.Column, index+1)
		parts = append(parts, "(("+term.Column+" "+operator+" "+placeholder+") OR ("+placeholder+" IS NOT NULL AND "+term.Column+" IS NULL))")
		branches = append(branches, "("+strings.Join(parts, " AND ")+")")
	}
	return "(" + strings.Join(branches, " OR ") + ")", args, nil
}

func typedPlaceholder(column string, position int) string {
	placeholder := fmt.Sprintf("(@cursor%d)", position)
	switch column {
	case "item.created_at", "item.updated_at":
		return placeholder + "::timestamptz"
	case "item.confidence":
		return placeholder + "::double precision"
	default:
		return placeholder + "::text"
	}
}

func itemSortValues(item collectionapp.CollectionItem, sortTerms []collectionapp.SortExpression) []*string {
	values := make([]*string, 0, len(sortTerms))
	for _, term := range sortTerms {
		var value *string
		switch term.Column {
		case "item.id":
			value = stringPointer(string(item.ID))
		case "item.object_type":
			value = stringPointer(item.ObjectType)
		case "item.topic_id":
			if item.TopicID != nil {
				value = stringPointer(string(*item.TopicID))
			}
		case "item.status":
			value = stringPointer(item.Status)
		case "item.created_at":
			value = stringPointer(item.CreatedAt.UTC().Format(time.RFC3339Nano))
		case "item.updated_at":
			value = stringPointer(item.UpdatedAt.UTC().Format(time.RFC3339Nano))
		case "item.confidence":
			if item.Confidence != nil {
				value = stringPointer(fmt.Sprintf("%.17g", *item.Confidence))
			}
		case "item.relation_type":
			value = cloneStringPointer(item.RelationType)
		case "item.health_issue_type":
			value = cloneStringPointer(item.HealthType)
		case "item.source_type":
			value = cloneStringPointer(item.SourceType)
		case "item.file_path":
			value = cloneStringPointer(item.FilePath)
		case "item.search_text", "lower(item.search_text)":
			value = stringPointer(strings.ToLower(item.SortTextKey))
		}
		values = append(values, value)
	}
	return values
}

func stringPointer(value string) *string { return &value }

func cloneStringPointer(value *string) *string {
	if value == nil {
		return nil
	}
	return stringPointer(*value)
}
