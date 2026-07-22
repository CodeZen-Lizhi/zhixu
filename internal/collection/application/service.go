package application

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/CodeZen-Lizhi/zhixu/internal/collection/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	"golang.org/x/text/cases"
	"golang.org/x/text/unicode/norm"
)

const (
	maxCollectionNameBytes        = 256
	maxCollectionDescriptionBytes = 4096
	maxIdempotencyKeyBytes        = 128
	maxListLimit                  = 100
	defaultListLimit              = 50
	defaultResultLimit            = 25
)

var collectionFold = cases.Fold()

// Dependencies 是 Collection Service 的显式依赖集合。
type Dependencies struct {
	Repository Repository
	IDs        foundation.IDGenerator
	Clock      foundation.Clock
}

// Service 编排 Collection 规范化、幂等和持久化边界。
type Service struct{ dependencies Dependencies }

// NewService 创建 fail-closed 的 Collection application service。
func NewService(dependencies Dependencies) (*Service, error) {
	if dependencies.Repository == nil || dependencies.IDs == nil || dependencies.Clock == nil {
		return nil, foundation.NewError(foundation.ErrorDependencyUnavailable, ErrorCodeDependencyUnavailable, false, errors.New("collection dependencies are incomplete"))
	}
	return &Service{dependencies: dependencies}, nil
}

// Create 规范化并幂等创建集合。
func (s *Service) Create(ctx context.Context, command CreateCommand) (CommandResult, error) {
	if err := validateContext(ctx); err != nil {
		return CommandResult{}, err
	}
	name, normalized, description, query, viewType, viewConfig, key, err := canonicalDefinition(command.WorkspaceID, command.Name, command.Description, command.Query, command.ViewType, command.ViewConfig, command.IdempotencyKey)
	if err != nil {
		return CommandResult{}, err
	}
	id, err := s.dependencies.IDs.New()
	if err != nil {
		return CommandResult{}, err
	}
	now := s.dependencies.Clock.Now().UTC()
	if now.IsZero() {
		return CommandResult{}, requestInvalid("clock returned zero time")
	}
	collection := Collection{ID: id, WorkspaceID: command.WorkspaceID, Name: name, NormalizedName: normalized, Description: description, QuerySchemaVersion: domain.QuerySchemaVersionV1, QueryVersion: 1, Query: query, QueryHash: mustQueryHash(query), ViewType: viewType, ViewConfig: viewConfig, Status: CollectionStatusActive, Version: 1, CreatedAt: now, UpdatedAt: now}
	hash, err := requestHash("CREATE", command.WorkspaceID, "", 0, &collection)
	if err != nil {
		return CommandResult{}, err
	}
	result, err := s.dependencies.Repository.CreateCollection(ctx, CreateRecord{Collection: collection, IdempotencyKey: key, RequestHash: hash})
	if err != nil {
		return CommandResult{}, err
	}
	if result.Replayed {
		if err := validateReplayResult(result, command.WorkspaceID, "", hash, "CREATE"); err != nil {
			return CommandResult{}, err
		}
	} else if err := validateCommandResult(result, command.WorkspaceID, id, 1, hash, "CREATE", false); err != nil {
		return CommandResult{}, err
	}
	return result, nil
}

// Update 规范化并使用 expected version 更新集合。
func (s *Service) Update(ctx context.Context, command UpdateCommand) (CommandResult, error) {
	if err := validateContext(ctx); err != nil {
		return CommandResult{}, err
	}
	if !validID(command.CollectionID) || command.ExpectedVersion < 1 {
		return CommandResult{}, requestInvalid("collection update identity is invalid")
	}
	name, normalized, description, query, viewType, viewConfig, key, err := canonicalDefinition(command.WorkspaceID, command.Name, command.Description, command.Query, command.ViewType, command.ViewConfig, command.IdempotencyKey)
	if err != nil {
		return CommandResult{}, err
	}
	collection := Collection{ID: command.CollectionID, WorkspaceID: command.WorkspaceID, Name: name, NormalizedName: normalized, Description: description, QuerySchemaVersion: domain.QuerySchemaVersionV1, QueryVersion: 1, Query: query, QueryHash: mustQueryHash(query), ViewType: viewType, ViewConfig: viewConfig, Status: CollectionStatusActive, Version: command.ExpectedVersion + 1}
	hash, err := requestHash("UPDATE", command.WorkspaceID, command.CollectionID, command.ExpectedVersion, &collection)
	if err != nil {
		return CommandResult{}, err
	}
	now := s.dependencies.Clock.Now().UTC()
	if now.IsZero() {
		return CommandResult{}, requestInvalid("clock returned zero time")
	}
	result, err := s.dependencies.Repository.UpdateCollection(ctx, UpdateRecord{WorkspaceID: command.WorkspaceID, CollectionID: command.CollectionID, ExpectedVersion: command.ExpectedVersion, Collection: collection, IdempotencyKey: key, RequestHash: hash, At: now})
	if err != nil {
		return CommandResult{}, err
	}
	if result.Replayed {
		if err := validateReplayResult(result, command.WorkspaceID, command.CollectionID, hash, "UPDATE"); err != nil {
			return CommandResult{}, err
		}
	} else if err := validateCommandResult(result, command.WorkspaceID, command.CollectionID, command.ExpectedVersion+1, hash, "UPDATE", false); err != nil {
		return CommandResult{}, err
	}
	return result, nil
}

// Archive 规范化并使用 expected version 归档集合。
func (s *Service) Archive(ctx context.Context, command ArchiveCommand) (CommandResult, error) {
	if err := validateContext(ctx); err != nil {
		return CommandResult{}, err
	}
	if !validID(command.WorkspaceID) || !validID(command.CollectionID) || command.ExpectedVersion < 1 {
		return CommandResult{}, requestInvalid("collection archive identity is invalid")
	}
	key, err := normalizeIdempotencyKey(command.IdempotencyKey)
	if err != nil {
		return CommandResult{}, err
	}
	hash, err := requestHash("ARCHIVE", command.WorkspaceID, command.CollectionID, command.ExpectedVersion, nil)
	if err != nil {
		return CommandResult{}, err
	}
	now := s.dependencies.Clock.Now().UTC()
	if now.IsZero() {
		return CommandResult{}, requestInvalid("clock returned zero time")
	}
	result, err := s.dependencies.Repository.ArchiveCollection(ctx, ArchiveRecord{WorkspaceID: command.WorkspaceID, CollectionID: command.CollectionID, ExpectedVersion: command.ExpectedVersion, IdempotencyKey: key, RequestHash: hash, At: now})
	if err != nil {
		return CommandResult{}, err
	}
	if result.Replayed {
		if err := validateReplayResult(result, command.WorkspaceID, command.CollectionID, hash, "ARCHIVE"); err != nil {
			return CommandResult{}, err
		}
	} else if err := validateCommandResult(result, command.WorkspaceID, command.CollectionID, command.ExpectedVersion+1, hash, "ARCHIVE", false); err != nil {
		return CommandResult{}, err
	}
	return result, nil
}

// Get 按 Workspace 隔离读取集合。
func (s *Service) Get(ctx context.Context, workspaceID, collectionID foundation.ID) (Collection, error) {
	if err := validateContext(ctx); err != nil {
		return Collection{}, err
	}
	if !validID(workspaceID) || !validID(collectionID) {
		return Collection{}, requestInvalid("collection lookup identity is invalid")
	}
	return s.dependencies.Repository.GetCollection(ctx, workspaceID, collectionID)
}

// List 返回 Workspace 内稳定排序的有界集合列表。
func (s *Service) List(ctx context.Context, query ListQuery) (CollectionListPage, error) {
	if err := validateContext(ctx); err != nil {
		return CollectionListPage{}, err
	}
	if !validID(query.WorkspaceID) {
		return CollectionListPage{}, requestInvalid("collection list workspace is invalid")
	}
	if query.Limit == 0 {
		query.Limit = defaultListLimit
	}
	if query.Limit < 1 || query.Limit > maxListLimit {
		return CollectionListPage{}, requestInvalid("collection list limit is invalid")
	}
	if len(query.Cursor) > collectionCursorMaxSize {
		return CollectionListPage{}, cursorInvalid("collection list cursor is invalid")
	}
	for _, status := range query.Statuses {
		if status != CollectionStatusActive && status != CollectionStatusArchived {
			return CollectionListPage{}, requestInvalid("collection list status is invalid")
		}
	}
	return s.dependencies.Repository.ListCollections(ctx, query)
}

// Results 执行保存集合的统一 read model；Repository 不可执行时显式返回能力不可用。
func (s *Service) Results(ctx context.Context, query ResultsQuery) (ResultPage, error) {
	if err := validateContext(ctx); err != nil {
		return ResultPage{}, err
	}
	if query.Limit == 0 {
		query.Limit = defaultResultLimit
	}
	if !validID(query.WorkspaceID) || !validID(query.CollectionID) || query.Limit < 1 || query.Limit > maxListLimit {
		return ResultPage{}, requestInvalid("collection result request is invalid")
	}
	reader, ok := s.dependencies.Repository.(QueryRepository)
	if !ok || reader == nil {
		return ResultPage{}, foundation.NewError(foundation.ErrorDependencyUnavailable, ErrorCodeDependencyUnavailable, true, errors.New("collection query repository is unavailable"))
	}
	return reader.ExecuteQuery(ctx, query)
}

// Preview 对未保存 Query 执行与 saved results 相同的 compiler、revision、count 和 keyset page。
func (s *Service) Preview(ctx context.Context, query PreviewQuery) (ResultPage, error) {
	if err := validateContext(ctx); err != nil {
		return ResultPage{}, err
	}
	if !validID(query.WorkspaceID) {
		return ResultPage{}, requestInvalid("collection preview workspace is invalid")
	}
	if query.Limit == 0 {
		query.Limit = defaultResultLimit
	}
	if query.Limit < 1 || query.Limit > maxListLimit {
		return ResultPage{}, requestInvalid("collection preview limit is invalid")
	}
	canonical, err := domain.CanonicalizeQuery(query.Query)
	if err != nil {
		return ResultPage{}, err
	}
	query.Query = canonical.Definition
	reader, ok := s.dependencies.Repository.(PreviewRepository)
	if !ok || reader == nil {
		return ResultPage{}, foundation.NewError(foundation.ErrorDependencyUnavailable, ErrorCodeDependencyUnavailable, true, errors.New("collection preview repository is unavailable"))
	}
	return reader.ExecutePreview(ctx, query)
}

// PlanDurableScan 冻结 Collection version、query hash、read-model revision 与精确成员数。
func (s *Service) PlanDurableScan(ctx context.Context, workspaceID, collectionID foundation.ID) (DurableScanBinding, error) {
	if err := validateContext(ctx); err != nil {
		return DurableScanBinding{}, err
	}
	if !validID(workspaceID) || !validID(collectionID) {
		return DurableScanBinding{}, requestInvalid("collection durable scan identity is invalid")
	}
	reader, ok := s.dependencies.Repository.(DurableScanRepository)
	if !ok || reader == nil {
		return DurableScanBinding{}, foundation.NewError(foundation.ErrorDependencyUnavailable, ErrorCodeDependencyUnavailable, true, errors.New("collection durable scan repository is unavailable"))
	}
	binding, err := reader.PlanDurableScan(ctx, workspaceID, collectionID)
	if err != nil {
		return DurableScanBinding{}, err
	}
	if !validDurableScanBinding(binding) || binding.WorkspaceID != workspaceID || binding.CollectionID != collectionID {
		return DurableScanBinding{}, foundation.NewError(foundation.ErrorConsistencyViolation, ErrorCodeResultInconsistent, false, errors.New("collection durable scan binding is inconsistent"))
	}
	return binding, nil
}

// ReadDurableScanPage 读取固定 binding 下的有界 `(object_type,id)` 页面。
func (s *Service) ReadDurableScanPage(ctx context.Context, request DurableScanPageRequest) (DurableScanPage, error) {
	if err := validateContext(ctx); err != nil {
		return DurableScanPage{}, err
	}
	if !validDurableScanBinding(request.Binding) || request.Limit < 1 || request.Limit > maxListLimit || request.PairTargetLimit < 0 || request.PairTargetLimit > maxListLimit || (request.After != nil && !validDurableScanKey(*request.After)) {
		return DurableScanPage{}, requestInvalid("collection durable scan page request is invalid")
	}
	reader, ok := s.dependencies.Repository.(DurableScanRepository)
	if !ok || reader == nil {
		return DurableScanPage{}, foundation.NewError(foundation.ErrorDependencyUnavailable, ErrorCodeDependencyUnavailable, true, errors.New("collection durable scan repository is unavailable"))
	}
	page, err := reader.ReadDurableScanPage(ctx, request)
	if err != nil {
		return DurableScanPage{}, err
	}
	if err := validateDurableScanPage(request, page); err != nil {
		return DurableScanPage{}, err
	}
	return page, nil
}

func validateDurableScanPage(request DurableScanPageRequest, page DurableScanPage) error {
	if page.Binding != request.Binding || len(page.Items) > request.Limit || len(page.Pairs) > len(page.Items)*request.PairTargetLimit || page.Complete != (page.Next == nil) {
		return foundation.NewError(foundation.ErrorConsistencyViolation, ErrorCodeResultInconsistent, false, errors.New("collection durable scan page is not request-bound"))
	}
	var previous *DurableScanKey
	if request.After != nil {
		value := *request.After
		previous = &value
	}
	sources := make(map[DurableScanKey]struct{}, len(page.Items))
	for _, item := range page.Items {
		key := DurableScanKey{ObjectType: item.ObjectType, ID: item.ID}
		if !validDurableScanKey(key) || (previous != nil && !durableScanKeyLess(*previous, key)) {
			return foundation.NewError(foundation.ErrorConsistencyViolation, ErrorCodeResultInconsistent, false, errors.New("collection durable scan items are not a strict keyset page"))
		}
		sources[key] = struct{}{}
		value := key
		previous = &value
	}
	if page.Next != nil {
		if len(page.Items) == 0 || previous == nil || *page.Next != *previous {
			return foundation.NewError(foundation.ErrorConsistencyViolation, ErrorCodeResultInconsistent, false, errors.New("collection durable scan next key is inconsistent"))
		}
	}
	seenPairs := make(map[DurableScanPair]struct{}, len(page.Pairs))
	requiredNodes := make(map[DurableScanKey]struct{}, len(page.Pairs)*2)
	pairCounts := make(map[DurableScanKey]int, len(page.Items))
	var previousPair *DurableScanPair
	for _, pair := range page.Pairs {
		if !validDurableScanKey(pair.Source) || !validDurableScanKey(pair.Target) || !durableScanKeyLess(pair.Source, pair.Target) {
			return foundation.NewError(foundation.ErrorConsistencyViolation, ErrorCodeResultInconsistent, false, errors.New("collection durable scan pair is invalid"))
		}
		if _, ok := sources[pair.Source]; !ok {
			return foundation.NewError(foundation.ErrorConsistencyViolation, ErrorCodeResultInconsistent, false, errors.New("collection durable scan pair source is outside the page"))
		}
		if _, duplicate := seenPairs[pair]; duplicate {
			return foundation.NewError(foundation.ErrorConsistencyViolation, ErrorCodeResultInconsistent, false, errors.New("collection durable scan pair is duplicated"))
		}
		if previousPair != nil && !durableScanPairLess(*previousPair, pair) {
			return foundation.NewError(foundation.ErrorConsistencyViolation, ErrorCodeResultInconsistent, false, errors.New("collection durable scan pairs are not strictly ordered"))
		}
		pairCounts[pair.Source]++
		if pairCounts[pair.Source] > request.PairTargetLimit {
			return foundation.NewError(foundation.ErrorConsistencyViolation, ErrorCodeResultInconsistent, false, errors.New("collection durable scan pair source exceeds target limit"))
		}
		seenPairs[pair] = struct{}{}
		requiredNodes[pair.Source] = struct{}{}
		requiredNodes[pair.Target] = struct{}{}
		value := pair
		previousPair = &value
	}
	seenNodes := make(map[DurableScanKey]struct{}, len(page.Nodes))
	var previousNode *DurableScanKey
	for _, node := range page.Nodes {
		if !validDurableScanKey(node.Key) || node.Version < 1 || strings.TrimSpace(node.Status) == "" || strings.TrimSpace(node.Title) == "" || strings.TrimSpace(node.Summary) == "" {
			return foundation.NewError(foundation.ErrorConsistencyViolation, ErrorCodeResultInconsistent, false, errors.New("collection durable scan node is invalid"))
		}
		if previousNode != nil && !durableScanKeyLess(*previousNode, node.Key) {
			return foundation.NewError(foundation.ErrorConsistencyViolation, ErrorCodeResultInconsistent, false, errors.New("collection durable scan nodes are not strictly ordered"))
		}
		if _, duplicate := seenNodes[node.Key]; duplicate {
			return foundation.NewError(foundation.ErrorConsistencyViolation, ErrorCodeResultInconsistent, false, errors.New("collection durable scan node is duplicated"))
		}
		seenNodes[node.Key] = struct{}{}
		value := node.Key
		previousNode = &value
	}
	if len(seenNodes) != len(requiredNodes) {
		return foundation.NewError(foundation.ErrorConsistencyViolation, ErrorCodeResultInconsistent, false, errors.New("collection durable scan node set is incomplete"))
	}
	for key := range requiredNodes {
		if _, found := seenNodes[key]; !found {
			return foundation.NewError(foundation.ErrorConsistencyViolation, ErrorCodeResultInconsistent, false, errors.New("collection durable scan pair node is missing"))
		}
	}
	return nil
}

func validDurableScanBinding(binding DurableScanBinding) bool {
	return validID(binding.WorkspaceID) && validID(binding.CollectionID) && binding.CollectionVersion > 0 && isHexHash(binding.QueryHash) && isHexHash(binding.ReadModelRevision) && binding.ExactCount >= 0
}

func validDurableScanKey(key DurableScanKey) bool {
	return (key.ObjectType == "CLAIM" || key.ObjectType == "TOPIC") && validID(key.ID)
}

func durableScanKeyLess(left, right DurableScanKey) bool {
	return left.ObjectType < right.ObjectType || (left.ObjectType == right.ObjectType && left.ID < right.ID)
}

func durableScanPairLess(left, right DurableScanPair) bool {
	return durableScanKeyLess(left.Source, right.Source) || (left.Source == right.Source && durableScanKeyLess(left.Target, right.Target))
}

func canonicalDefinition(workspaceID foundation.ID, name, description string, query domain.Query, viewType domain.ViewType, viewConfig domain.ViewConfig, key string) (string, string, string, domain.Query, domain.ViewType, domain.ViewConfig, string, error) {
	if !validID(workspaceID) {
		return "", "", "", domain.Query{}, "", domain.ViewConfig{}, "", requestInvalid("collection workspace is invalid")
	}
	key, err := normalizeIdempotencyKey(key)
	if err != nil {
		return "", "", "", domain.Query{}, "", domain.ViewConfig{}, "", err
	}
	display, normalized, err := normalizeName(name)
	if err != nil {
		return "", "", "", domain.Query{}, "", domain.ViewConfig{}, "", err
	}
	description, err = normalizeDescription(description)
	if err != nil {
		return "", "", "", domain.Query{}, "", domain.ViewConfig{}, "", err
	}
	canonicalQuery, err := domain.CanonicalizeQuery(query)
	if err != nil {
		return "", "", "", domain.Query{}, "", domain.ViewConfig{}, "", err
	}
	canonicalView, err := domain.CanonicalizeViewConfig(viewType, viewConfig)
	if err != nil {
		return "", "", "", domain.Query{}, "", domain.ViewConfig{}, "", err
	}
	encodedView, err := json.Marshal(canonicalView)
	if err != nil || len(encodedView) > 16*1024 {
		return "", "", "", domain.Query{}, "", domain.ViewConfig{}, "", requestInvalid("collection view config exceeds size limit")
	}
	canonicalType := domain.ViewType(strings.ToUpper(strings.TrimSpace(string(viewType))))
	return display, normalized, description, canonicalQuery.Definition, canonicalType, canonicalView, key, nil
}

func normalizeName(value string) (string, string, error) {
	display, err := normalizeText(value, maxCollectionNameBytes, false)
	if err != nil {
		return "", "", err
	}
	normalized := norm.NFC.String(collectionFold.String(display))
	if len(normalized) > maxCollectionNameBytes {
		return "", "", requestInvalid("collection name exceeds byte limit")
	}
	return display, normalized, nil
}

func normalizeDescription(value string) (string, error) {
	return normalizeText(value, maxCollectionDescriptionBytes, true)
}

func normalizeText(value string, maxBytes int, allowEmpty bool) (string, error) {
	if !utf8.ValidString(value) {
		return "", requestInvalid("collection text is not valid utf-8")
	}
	value = norm.NFC.String(value)
	var b strings.Builder
	started := false
	pending := false
	for _, r := range value {
		if unicode.IsSpace(r) {
			if started {
				pending = true
			}
			continue
		}
		if unicode.IsControl(r) {
			return "", requestInvalid("collection text contains control character")
		}
		if pending {
			b.WriteByte(' ')
			pending = false
		}
		b.WriteRune(r)
		started = true
		if b.Len() > maxBytes {
			return "", requestInvalid("collection text exceeds byte limit")
		}
	}
	result := b.String()
	if result == "" && !allowEmpty {
		return "", requestInvalid("collection name is required")
	}
	return result, nil
}

func normalizeIdempotencyKey(value string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" || len(value) > maxIdempotencyKeyBytes || strings.ContainsAny(value, "\r\n") {
		return "", requestInvalid("idempotency key is invalid")
	}
	return value, nil
}
func validateContext(ctx context.Context) error {
	if ctx == nil {
		return requestInvalid("context is nil")
	}
	return nil
}
func validID(value foundation.ID) bool {
	_, err := foundation.ParseID(string(value))
	return err == nil
}
func mustQueryHash(query domain.Query) string {
	canonical, _ := domain.CanonicalizeQuery(query)
	return canonical.Hash
}

func requestHash(operation string, workspaceID, collectionID foundation.ID, expected int64, collection *Collection) (string, error) {
	var definition *requestDefinition
	if collection != nil {
		definition = &requestDefinition{
			Name:           collection.Name,
			NormalizedName: collection.NormalizedName,
			Description:    collection.Description,
			Query:          collection.Query,
			QueryHash:      collection.QueryHash,
			ViewType:       collection.ViewType,
			ViewConfig:     collection.ViewConfig,
		}
	}
	payload := struct {
		SchemaVersion   string             `json:"schema_version"`
		Operation       string             `json:"operation"`
		WorkspaceID     foundation.ID      `json:"workspace_id"`
		CollectionID    foundation.ID      `json:"collection_id,omitempty"`
		ExpectedVersion int64              `json:"expected_version,omitempty"`
		Collection      *requestDefinition `json:"collection,omitempty"`
	}{"collection-command/v1", operation, workspaceID, collectionID, expected, definition}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return "", requestInvalid("collection request cannot be encoded")
	}
	digest := sha256.Sum256(encoded)
	return hex.EncodeToString(digest[:]), nil
}

type requestDefinition struct {
	Name           string            `json:"name"`
	NormalizedName string            `json:"normalized_name"`
	Description    string            `json:"description"`
	Query          domain.Query      `json:"query"`
	QueryHash      string            `json:"query_hash"`
	ViewType       domain.ViewType   `json:"view_type"`
	ViewConfig     domain.ViewConfig `json:"view_config"`
}

func validateCommandResult(result CommandResult, workspaceID, collectionID foundation.ID, expectedVersion int64, requestHash, commandType string, replayed bool) error {
	if result.Collection.WorkspaceID != workspaceID || result.Collection.ID != collectionID || result.CommandVersion != expectedVersion || result.RequestHash != requestHash || result.CommandType != commandType || result.Replayed != replayed {
		return resultInconsistent("collection command result is not bound to request")
	}
	return nil
}

func validateReplayResult(result CommandResult, workspaceID, collectionID foundation.ID, requestHash, commandType string) error {
	if result.Collection.WorkspaceID != workspaceID || (collectionID != "" && result.Collection.ID != collectionID) || !validID(result.Collection.ID) || result.CommandVersion < 1 || result.Collection.Version != result.CommandVersion || result.RequestHash != requestHash || result.CommandType != commandType || !result.Replayed {
		return resultInconsistent("collection replay result is not bound to receipt")
	}
	return nil
}

// ComputeRequestHash 暴露稳定请求摘要，供 adapter 测试和下游 receipt 绑定使用。
func ComputeRequestHash(operation string, workspaceID, collectionID foundation.ID, expected int64, collection *Collection) (string, error) {
	return requestHash(operation, workspaceID, collectionID, expected, collection)
}

var _ = time.Time{}
