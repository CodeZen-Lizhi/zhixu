package postgres

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"

	collectionapp "github.com/CodeZen-Lizhi/zhixu/internal/collection/application"
	collectiondomain "github.com/CodeZen-Lizhi/zhixu/internal/collection/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	"github.com/jackc/pgx/v5"
)

var _ collectionapp.DurableScanRepository = (*Repository)(nil)

// PlanDurableScan 在一个只读快照中冻结 Collection 定义、read model revision 与精确成员数。
func (r *Repository) PlanDurableScan(ctx context.Context, workspaceID, collectionID foundation.ID) (collectionapp.DurableScanBinding, error) {
	if r == nil || r.db == nil || r.beginner == nil {
		return collectionapp.DurableScanBinding{}, unavailable(errors.New("collection durable scan repository is unavailable"))
	}
	if ctx == nil || !validID(workspaceID) || !validID(collectionID) {
		return collectionapp.DurableScanBinding{}, requestInvalid(errors.New("collection durable scan identity is invalid"))
	}
	tx, err := r.beginner.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return collectionapp.DurableScanBinding{}, classify(err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	if err := configureCollectionStatementTimeout(ctx, tx, defaultCollectionStatementTimeout); err != nil {
		return collectionapp.DurableScanBinding{}, classify(err)
	}
	binding, err := planDurableScanSnapshot(ctx, tx, workspaceID, collectionID)
	if err != nil {
		return collectionapp.DurableScanBinding{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return collectionapp.DurableScanBinding{}, classify(err)
	}
	return binding, nil
}

func planDurableScanSnapshot(ctx context.Context, db DB, workspaceID, collectionID foundation.ID) (collectionapp.DurableScanBinding, error) {
	collection, plan, where, args, err := loadDurableScanPlan(ctx, db, workspaceID, collectionID)
	if err != nil {
		return collectionapp.DurableScanBinding{}, err
	}
	if collection.Status != collectionapp.CollectionStatusActive {
		return collectionapp.DurableScanBinding{}, notFound(errors.New("active collection not found"))
	}
	revision, err := readDurableScanRevision(ctx, db, workspaceID, plan)
	if err != nil {
		return collectionapp.DurableScanBinding{}, err
	}
	count, err := countDurableScanMembers(ctx, db, where, args)
	if err != nil {
		return collectionapp.DurableScanBinding{}, err
	}
	return collectionapp.DurableScanBinding{
		WorkspaceID:       workspaceID,
		CollectionID:      collectionID,
		CollectionVersion: collection.Version,
		QueryHash:         plan.Canonical.Hash,
		ReadModelRevision: revision,
		ExactCount:        count,
	}, nil
}

// ReadDurableScanPage 在固定 binding 下读取一个结构化 keyset 页面和有界后继 pair。
func (r *Repository) ReadDurableScanPage(ctx context.Context, request collectionapp.DurableScanPageRequest) (collectionapp.DurableScanPage, error) {
	if r == nil || r.db == nil || r.beginner == nil {
		return collectionapp.DurableScanPage{}, unavailable(errors.New("collection durable scan repository is unavailable"))
	}
	if ctx == nil || !validDurableScanRequest(request) {
		return collectionapp.DurableScanPage{}, requestInvalid(errors.New("collection durable scan page request is invalid"))
	}
	tx, err := r.beginner.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return collectionapp.DurableScanPage{}, classify(err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	if err := configureCollectionStatementTimeout(ctx, tx, defaultCollectionStatementTimeout); err != nil {
		return collectionapp.DurableScanPage{}, classify(err)
	}
	page, err := readDurableScanPageSnapshot(ctx, tx, request)
	if err != nil {
		return collectionapp.DurableScanPage{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return collectionapp.DurableScanPage{}, classify(err)
	}
	return page, nil
}

// VerifyDurableScanRevision 复核 Collection 定义与 read-model revision。
// exact_count 已在 durable binding 规划/start 事务中校验；跨页不重复扫描成员集合计数。
func (r *Repository) VerifyDurableScanRevision(ctx context.Context, binding collectionapp.DurableScanBinding) error {
	if r == nil || r.db == nil || r.beginner == nil {
		return unavailable(errors.New("collection durable scan repository is unavailable"))
	}
	if ctx == nil || !validDurableScanBinding(binding) {
		return requestInvalid(errors.New("collection durable scan binding is invalid"))
	}
	tx, err := r.beginner.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return classify(err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	if err := configureCollectionStatementTimeout(ctx, tx, defaultCollectionStatementTimeout); err != nil {
		return classify(err)
	}
	collection, plan, _, _, err := loadDurableScanPlan(ctx, tx, binding.WorkspaceID, binding.CollectionID)
	if err != nil {
		return err
	}
	return validateDurableScanRevision(ctx, tx, binding, collection, plan)
}

func validDurableScanRequest(request collectionapp.DurableScanPageRequest) bool {
	binding := request.Binding
	if !validID(binding.WorkspaceID) || !validID(binding.CollectionID) || binding.CollectionVersion < 1 || !isHash(binding.QueryHash) || !isHash(binding.ReadModelRevision) || binding.ExactCount < 0 || request.Limit < 1 || request.Limit > 100 || request.PairTargetLimit < 0 || request.PairTargetLimit > 100 {
		return false
	}
	return request.After == nil || ((request.After.ObjectType == "CLAIM" || request.After.ObjectType == "TOPIC") && validID(request.After.ID))
}

func readDurableScanPageSnapshot(ctx context.Context, db DB, request collectionapp.DurableScanPageRequest) (collectionapp.DurableScanPage, error) {
	binding := request.Binding
	var where string
	var args []any
	var err error
	if request.After == nil {
		_, _, where, args, err = verifyDurableScanBindingSnapshot(ctx, db, binding, false)
	} else {
		_, _, where, args, err = verifyDurableScanRevisionSnapshot(ctx, db, binding)
	}
	if err != nil {
		return collectionapp.DurableScanPage{}, err
	}
	if request.After != nil {
		member, err := durableScanMemberExists(ctx, db, where, args, *request.After)
		if err != nil {
			return collectionapp.DurableScanPage{}, err
		}
		if !member {
			return collectionapp.DurableScanPage{}, durableScanStale("collection durable scan checkpoint is no longer a member")
		}
	}
	items, hasMore, err := loadDurableScanItems(ctx, db, binding.WorkspaceID, where, args, request.After, request.Limit)
	if err != nil {
		return collectionapp.DurableScanPage{}, err
	}
	pairs, err := loadDurableScanPairs(ctx, db, where, args, items, request.PairTargetLimit)
	if err != nil {
		return collectionapp.DurableScanPage{}, err
	}
	nodes, err := loadDurableScanNodes(ctx, db, binding.WorkspaceID, pairs)
	if err != nil {
		return collectionapp.DurableScanPage{}, err
	}
	page := collectionapp.DurableScanPage{
		Binding:  binding,
		Items:    items,
		Pairs:    pairs,
		Nodes:    nodes,
		Complete: !hasMore,
	}
	if hasMore {
		last := items[len(items)-1]
		page.Next = &collectionapp.DurableScanKey{ObjectType: last.ObjectType, ID: last.ID}
	}
	return page, nil
}

// VerifyDurableScanBinding 在调用方拥有的 start transaction 中原子复核冻结 binding。
// 调用方必须使用 repeatable-read，并在同一事务中紧接着写入 scan/runtime 事实。
func VerifyDurableScanBinding(ctx context.Context, tx pgx.Tx, binding collectionapp.DurableScanBinding) error {
	if ctx == nil || tx == nil || !validDurableScanBinding(binding) {
		return requestInvalid(errors.New("collection durable scan binding verification request is invalid"))
	}
	_, _, _, _, err := verifyDurableScanBindingSnapshot(ctx, tx, binding, true)
	return err
}

func verifyDurableScanBindingSnapshot(ctx context.Context, db DB, binding collectionapp.DurableScanBinding, lockCollection bool) (collectionapp.Collection, collectionapp.QueryPlan, string, []any, error) {
	collection, plan, where, args, err := loadDurableScanPlanWithLock(ctx, db, binding.WorkspaceID, binding.CollectionID, lockCollection)
	if err != nil {
		return collectionapp.Collection{}, collectionapp.QueryPlan{}, "", nil, err
	}
	if err := validateDurableScanRevision(ctx, db, binding, collection, plan); err != nil {
		return collectionapp.Collection{}, collectionapp.QueryPlan{}, "", nil, err
	}
	count, err := countDurableScanMembers(ctx, db, where, args)
	if err != nil {
		return collectionapp.Collection{}, collectionapp.QueryPlan{}, "", nil, err
	}
	if count != binding.ExactCount {
		return collectionapp.Collection{}, collectionapp.QueryPlan{}, "", nil, durableScanStale("collection durable scan member count changed")
	}
	return collection, plan, where, args, nil
}

func verifyDurableScanRevisionSnapshot(ctx context.Context, db DB, binding collectionapp.DurableScanBinding) (collectionapp.Collection, collectionapp.QueryPlan, string, []any, error) {
	collection, plan, where, args, err := loadDurableScanDefinitionSnapshot(ctx, db, binding)
	if err != nil {
		return collectionapp.Collection{}, collectionapp.QueryPlan{}, "", nil, err
	}
	revision, err := readDurableScanRevision(ctx, db, binding.WorkspaceID, plan)
	if err != nil {
		return collectionapp.Collection{}, collectionapp.QueryPlan{}, "", nil, err
	}
	if revision != binding.ReadModelRevision {
		return collectionapp.Collection{}, collectionapp.QueryPlan{}, "", nil, durableScanStale("collection durable scan read model changed")
	}
	return collection, plan, where, args, nil
}

func loadDurableScanDefinitionSnapshot(ctx context.Context, db DB, binding collectionapp.DurableScanBinding) (collectionapp.Collection, collectionapp.QueryPlan, string, []any, error) {
	collection, plan, where, args, err := loadDurableScanPlan(ctx, db, binding.WorkspaceID, binding.CollectionID)
	if err != nil {
		return collectionapp.Collection{}, collectionapp.QueryPlan{}, "", nil, err
	}
	if collection.Status != collectionapp.CollectionStatusActive || collection.Version != binding.CollectionVersion || collection.QueryHash != binding.QueryHash || plan.Canonical.Hash != binding.QueryHash {
		return collectionapp.Collection{}, collectionapp.QueryPlan{}, "", nil, durableScanStale("collection durable scan definition changed")
	}
	return collection, plan, where, args, nil
}

func validateDurableScanRevision(ctx context.Context, db DB, binding collectionapp.DurableScanBinding, collection collectionapp.Collection, plan collectionapp.QueryPlan) error {
	if collection.Status != collectionapp.CollectionStatusActive || collection.Version != binding.CollectionVersion || collection.QueryHash != binding.QueryHash || plan.Canonical.Hash != binding.QueryHash {
		return durableScanStale("collection durable scan definition changed")
	}
	revision, err := readDurableScanRevision(ctx, db, binding.WorkspaceID, plan)
	if err != nil {
		return err
	}
	if revision != binding.ReadModelRevision {
		return durableScanStale("collection durable scan read model changed")
	}
	return nil
}

func loadDurableScanPlan(ctx context.Context, db DB, workspaceID, collectionID foundation.ID) (collectionapp.Collection, collectionapp.QueryPlan, string, []any, error) {
	return loadDurableScanPlanWithLock(ctx, db, workspaceID, collectionID, false)
}

func loadDurableScanPlanWithLock(ctx context.Context, db DB, workspaceID, collectionID foundation.ID, lockCollection bool) (collectionapp.Collection, collectionapp.QueryPlan, string, []any, error) {
	var collection collectionapp.Collection
	var err error
	if lockCollection {
		query := collectionSelect + ` WHERE workspace_id=$1 AND id=$2 FOR SHARE`
		var row pgx.Row
		if database, ok := db.(*gormDB); ok {
			row = database.collectionRow(ctx, query, string(workspaceID), string(collectionID))
		} else {
			row = db.QueryRow(ctx, query, string(workspaceID), string(collectionID))
		}
		collection, err = scanCollection(row)
		if errors.Is(err, pgx.ErrNoRows) {
			err = notFound(errors.New("collection not found"))
		}
	} else {
		collection, err = loadCollection(ctx, db, workspaceID, collectionID, false)
	}
	if err != nil {
		return collectionapp.Collection{}, collectionapp.QueryPlan{}, "", nil, err
	}
	plan, err := collectionapp.CompileQuery(collection.Query)
	if err != nil {
		return collectionapp.Collection{}, collectionapp.QueryPlan{}, "", nil, err
	}
	if plan.Canonical.Hash != collection.QueryHash {
		return collectionapp.Collection{}, collectionapp.QueryPlan{}, "", nil, inconsistent(errors.New("collection durable scan query hash is inconsistent"))
	}
	where, args := shiftWhere(plan.Where, append([]any{string(workspaceID)}, plan.Args...))
	return collection, plan, where, args, nil
}

func validDurableScanBinding(binding collectionapp.DurableScanBinding) bool {
	return validID(binding.WorkspaceID) && validID(binding.CollectionID) && binding.CollectionVersion > 0 && isHash(binding.QueryHash) && isHash(binding.ReadModelRevision) && binding.ExactCount >= 0
}

func readDurableScanRevision(ctx context.Context, db DB, workspaceID foundation.ID, plan collectionapp.QueryPlan) (string, error) {
	return readModelRevisionWithOptions(ctx, db, workspaceID, revisionOptions{
		includeHealthIssues: queryPlanReferencesField(plan, "health_issue_type"),
	})
}

func queryPlanReferencesField(plan collectionapp.QueryPlan, field string) bool {
	if queryReferencesField(plan.Canonical.Definition.Root, field) {
		return true
	}
	for _, term := range plan.Canonical.Definition.Sort {
		if strings.EqualFold(strings.TrimSpace(term.Field), field) {
			return true
		}
	}
	return false
}

func queryReferencesField(clause collectiondomain.Clause, field string) bool {
	if clause.Kind == collectiondomain.ClauseKindPredicate {
		return strings.EqualFold(strings.TrimSpace(clause.Field), field)
	}
	for _, child := range clause.Clauses {
		if queryReferencesField(child, field) {
			return true
		}
	}
	return false
}

func countDurableScanMembers(ctx context.Context, db DB, where string, args []any) (int64, error) {
	query := "SELECT count(*) FROM (" + unifiedItemCTE + "SELECT 1 FROM item WHERE " + where + ") counted"
	var count int64
	if err := db.QueryRow(ctx, query, args...).Scan(&count); err != nil {
		return 0, classify(err)
	}
	return count, nil
}

func durableScanMemberExists(ctx context.Context, db DB, where string, args []any, key collectionapp.DurableScanKey) (bool, error) {
	objectTypeArg := len(args) + 1
	idArg := objectTypeArg + 1
	query := unifiedItemCTE + "SELECT EXISTS(SELECT 1 FROM item WHERE " + where + fmt.Sprintf(" AND item.object_type=$%d AND item.id=$%d)", objectTypeArg, idArg)
	queryArgs := append(append([]any(nil), args...), key.ObjectType, string(key.ID))
	var exists bool
	if err := db.QueryRow(ctx, query, queryArgs...).Scan(&exists); err != nil {
		return false, classify(err)
	}
	return exists, nil
}

func loadDurableScanItems(ctx context.Context, db DB, workspaceID foundation.ID, where string, args []any, after *collectionapp.DurableScanKey, limit int) ([]collectionapp.CollectionItem, bool, error) {
	pageWhere := where
	pageArgs := append([]any(nil), args...)
	if after != nil {
		objectTypeArg := len(pageArgs) + 1
		idArg := objectTypeArg + 1
		pageWhere += fmt.Sprintf(" AND (item.object_type,item.id)>($%d::text,$%d::text)", objectTypeArg, idArg)
		pageArgs = append(pageArgs, after.ObjectType, string(after.ID))
	}
	limitArg := len(pageArgs) + 1
	query := unifiedItemCTE + "SELECT * FROM item WHERE " + pageWhere + fmt.Sprintf(" ORDER BY item.object_type,item.id LIMIT $%d", limitArg)
	pageArgs = append(pageArgs, limit+1)
	rows, err := db.Query(ctx, query, pageArgs...)
	if err != nil {
		return nil, false, classify(err)
	}
	defer rows.Close()
	items := make([]collectionapp.CollectionItem, 0, limit+1)
	for rows.Next() {
		item, scanErr := scanItem(rows)
		if scanErr != nil {
			return nil, false, scanErr
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, false, classify(err)
	}
	hasMore := len(items) > limit
	if hasMore {
		items = items[:limit]
	}
	if err := hydrateCollectionItems(ctx, db, workspaceID, items); err != nil {
		return nil, false, err
	}
	return items, hasMore, nil
}

func loadDurableScanPairs(ctx context.Context, db DB, where string, args []any, sources []collectionapp.CollectionItem, targetLimit int) ([]collectionapp.DurableScanPair, error) {
	if len(sources) == 0 || targetLimit == 0 {
		return nil, nil
	}
	objectTypes := make([]string, len(sources))
	ids := make([]string, len(sources))
	for index, source := range sources {
		objectTypes[index], ids[index] = source.ObjectType, string(source.ID)
	}
	objectTypesArg := len(args) + 1
	idsArg := objectTypesArg + 1
	limitArg := idsArg + 1
	query := unifiedItemCTE + `, filtered AS (
	SELECT item.object_type,item.id FROM item WHERE ` + where + `
), source AS (
	SELECT input.object_type,input.id,input.ordinality
	FROM unnest($` + fmt.Sprint(objectTypesArg) + `::text[],$` + fmt.Sprint(idsArg) + `::uuid[]) WITH ORDINALITY AS input(object_type,id,ordinality)
)
SELECT source.object_type,source.id::text,target.object_type,target.id
FROM source
CROSS JOIN LATERAL (
	SELECT filtered.object_type,filtered.id
	FROM filtered
	WHERE (filtered.object_type,filtered.id)>(source.object_type,source.id::text)
	ORDER BY filtered.object_type,filtered.id
	LIMIT $` + fmt.Sprint(limitArg) + `
) target
ORDER BY source.ordinality,target.object_type,target.id`
	queryArgs := append(append([]any(nil), args...), objectTypes, ids, targetLimit)
	rows, err := db.Query(ctx, query, queryArgs...)
	if err != nil {
		return nil, classify(err)
	}
	defer rows.Close()
	pairs := make([]collectionapp.DurableScanPair, 0, len(sources)*targetLimit)
	sourceSet := make(map[collectionapp.DurableScanKey]struct{}, len(sources))
	for _, source := range sources {
		sourceSet[collectionapp.DurableScanKey{ObjectType: source.ObjectType, ID: source.ID}] = struct{}{}
	}
	for rows.Next() {
		var sourceType, sourceID, targetType, targetID string
		if err := rows.Scan(&sourceType, &sourceID, &targetType, &targetID); err != nil {
			return nil, classify(err)
		}
		pair := collectionapp.DurableScanPair{
			Source: collectionapp.DurableScanKey{ObjectType: sourceType, ID: foundation.ID(sourceID)},
			Target: collectionapp.DurableScanKey{ObjectType: targetType, ID: foundation.ID(targetID)},
		}
		if !validDurableScanKey(pair.Source) || !validDurableScanKey(pair.Target) ||
			pair.Target.ObjectType < pair.Source.ObjectType ||
			(pair.Target.ObjectType == pair.Source.ObjectType && pair.Target.ID <= pair.Source.ID) {
			return nil, inconsistent(errors.New("collection durable scan pair is invalid"))
		}
		if _, exists := sourceSet[pair.Source]; !exists {
			return nil, inconsistent(errors.New("collection durable scan pair crossed source page"))
		}
		pairs = append(pairs, pair)
	}
	if err := rows.Err(); err != nil {
		return nil, classify(err)
	}
	return pairs, nil
}

const durableScanNodeSQL = `WITH requested AS (
	SELECT input.object_type,input.id,input.ordinality
	FROM unnest($2::text[],$3::uuid[]) WITH ORDINALITY AS input(object_type,id,ordinality)
), topic_aliases_ranked AS (
	SELECT a.topic_id AS id,a.alias,a.normalized_alias,a.id AS alias_id,
		row_number() OVER (PARTITION BY a.topic_id ORDER BY a.normalized_alias,a.id) AS row_number
	FROM core.topic_alias a
	JOIN requested q ON q.object_type='TOPIC' AND q.id=a.topic_id
	WHERE a.workspace_id=$1
), topic_aliases AS (
	SELECT id,COALESCE(array_agg(alias ORDER BY normalized_alias,alias_id),'{}'::text[]) AS aliases
	FROM topic_aliases_ranked
	WHERE row_number <= 32
	GROUP BY id
), claim_topics AS (
	SELECT r.source_node_id AS id,
		COALESCE(array_agg(DISTINCT r.target_node_id::text ORDER BY r.target_node_id::text),'{}'::text[]) AS topic_ids
	FROM core.relation r
	JOIN requested q ON q.object_type='CLAIM' AND q.id=r.source_node_id
	WHERE r.workspace_id=$1 AND r.source_node_type='CLAIM' AND r.target_node_type='TOPIC'
	  AND r.relation_type='BELONGS_TO' AND r.status='CONFIRMED'
	GROUP BY r.source_node_id
), claim_sources AS (
	SELECT cs.claim_id AS id,
		COALESCE(array_agg(DISTINCT cs.source_version_id::text ORDER BY cs.source_version_id::text),'{}'::text[]) AS source_version_ids
	FROM core.claim_source cs
	JOIN requested q ON q.object_type='CLAIM' AND q.id=cs.claim_id
	WHERE cs.workspace_id=$1
	GROUP BY cs.claim_id
)
SELECT q.object_type,q.id::text,
	CASE WHEN q.object_type='TOPIC' THEN t.version ELSE c.version END AS version,
	CASE WHEN q.object_type='TOPIC' THEN t.status ELSE c.status END AS status,
	CASE WHEN q.object_type='TOPIC' THEN t.name ELSE c.statement END AS title,
	CASE WHEN q.object_type='TOPIC' THEN COALESCE(NULLIF(t.description,''),t.name) ELSE c.statement END AS summary,
	COALESCE(topic_aliases.aliases,'{}'::text[]),
	COALESCE(claim_topics.topic_ids,'{}'::text[]),
	COALESCE(claim_sources.source_version_ids,'{}'::text[])
FROM requested q
LEFT JOIN core.topic t ON q.object_type='TOPIC' AND t.workspace_id=$1 AND t.id=q.id
LEFT JOIN core.claim c ON q.object_type='CLAIM' AND c.workspace_id=$1 AND c.id=q.id
LEFT JOIN topic_aliases ON topic_aliases.id=q.id
LEFT JOIN claim_topics ON claim_topics.id=q.id
LEFT JOIN claim_sources ON claim_sources.id=q.id
ORDER BY q.object_type,q.id`

func loadDurableScanNodes(ctx context.Context, db DB, workspaceID foundation.ID, pairs []collectionapp.DurableScanPair) ([]collectionapp.DurableScanNode, error) {
	if len(pairs) == 0 {
		return nil, nil
	}
	set := make(map[collectionapp.DurableScanKey]struct{}, len(pairs)*2)
	for _, pair := range pairs {
		set[pair.Source] = struct{}{}
		set[pair.Target] = struct{}{}
	}
	keys := make([]collectionapp.DurableScanKey, 0, len(set))
	for key := range set {
		keys = append(keys, key)
	}
	sort.Slice(keys, func(left, right int) bool {
		return keys[left].ObjectType < keys[right].ObjectType || (keys[left].ObjectType == keys[right].ObjectType && keys[left].ID < keys[right].ID)
	})
	objectTypes := make([]string, len(keys))
	ids := make([]string, len(keys))
	for index, key := range keys {
		objectTypes[index], ids[index] = key.ObjectType, string(key.ID)
	}
	var rows pgx.Rows
	var err error
	if database, ok := db.(*gormDB); ok {
		rows, err = database.durableNodeRows(ctx, durableScanNodeSQL, string(workspaceID), objectTypes, ids)
	} else {
		rows, err = db.Query(ctx, durableScanNodeSQL, string(workspaceID), objectTypes, ids)
	}
	if err != nil {
		return nil, classify(err)
	}
	defer rows.Close()
	nodes := make([]collectionapp.DurableScanNode, 0, len(keys))
	seen := make(map[collectionapp.DurableScanKey]struct{}, len(keys))
	for rows.Next() {
		var objectType, id string
		var version *int64
		var status, title, summary *string
		var aliases, topicIDs, sourceVersionIDs []string
		if err := rows.Scan(&objectType, &id, &version, &status, &title, &summary, &aliases, &topicIDs, &sourceVersionIDs); err != nil {
			return nil, classify(err)
		}
		key := collectionapp.DurableScanKey{ObjectType: objectType, ID: foundation.ID(id)}
		if !validDurableScanKey(key) || version == nil || status == nil || title == nil || summary == nil || *version < 1 || *status == "" || *title == "" {
			return nil, inconsistent(errors.New("collection durable scan node is missing"))
		}
		if _, expected := set[key]; !expected {
			return nil, inconsistent(errors.New("collection durable scan node crossed requested boundary"))
		}
		if _, duplicate := seen[key]; duplicate {
			return nil, inconsistent(errors.New("collection durable scan node is duplicated"))
		}
		seen[key] = struct{}{}
		node := collectionapp.DurableScanNode{
			Key:     key,
			Version: *version, Status: *status, Title: *title, Summary: *summary,
			Aliases: append([]string(nil), aliases...),
		}
		for _, value := range topicIDs {
			parsed := foundation.ID(value)
			if !validID(parsed) {
				return nil, inconsistent(errors.New("collection durable scan topic reference is invalid"))
			}
			node.TopicIDs = append(node.TopicIDs, parsed)
		}
		for _, value := range sourceVersionIDs {
			parsed := foundation.ID(value)
			if !validID(parsed) {
				return nil, inconsistent(errors.New("collection durable scan source reference is invalid"))
			}
			node.SourceVersionIDs = append(node.SourceVersionIDs, parsed)
		}
		nodes = append(nodes, node)
	}
	if err := rows.Err(); err != nil {
		return nil, classify(err)
	}
	if len(seen) != len(keys) {
		return nil, inconsistent(errors.New("collection durable scan node hydration is incomplete"))
	}
	return nodes, nil
}

func durableScanStale(message string) error {
	return versionConflict(collectionapp.ErrorCodeCursorStale, errors.New(message))
}

func validDurableScanKey(key collectionapp.DurableScanKey) bool {
	return (key.ObjectType == "CLAIM" || key.ObjectType == "TOPIC") && validID(key.ID)
}
