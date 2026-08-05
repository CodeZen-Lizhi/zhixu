package application

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strconv"
	"strings"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	"github.com/CodeZen-Lizhi/zhixu/internal/ingestion/domain"
)

// SourceMetadata 是创建 Attempt 前可从数据库可信读取的 Source 契约。
type SourceMetadata struct {
	WorkspaceID       foundation.ID
	SourceVersionID   foundation.ID
	ContentArtifactID foundation.ID
	MediaType         string
	ByteSize          int64
}

// SourceContent 是已从 Content Artifact 校验重读的 Parser 输入。
type SourceContent struct {
	WorkspaceID       foundation.ID
	SourceVersionID   foundation.ID
	ContentArtifactID foundation.ID
	MediaType         string
	Bytes             []byte
}

// SourceContentReader 隐藏 Workspace Repository 和文件系统组合读取细节。
type SourceContentReader interface {
	GetSourceMetadata(context.Context, foundation.ID) (SourceMetadata, error)
	ReadSourceContent(context.Context, SourceMetadata) ([]byte, error)
}

// Dependencies 是 Ingestion 用例需要的显式端口。
type Dependencies struct {
	Repository    domain.Repository
	Sources       SourceContentReader
	Parsers       domain.ParserRegistry
	IDs           foundation.IDGenerator
	Clock         foundation.Clock
	ContentPolicy ContentPolicy
	ChunkOptions  domain.ChunkOptions
}

// Service 编排校验、解析、分块和持久化状态机。
type Service struct{ dependencies Dependencies }

// NewService 创建 Ingestion Application Service。
func NewService(dependencies Dependencies) (*Service, error) {
	if dependencies.Repository == nil || dependencies.Sources == nil || dependencies.Parsers == nil || dependencies.IDs == nil || dependencies.Clock == nil {
		return nil, foundation.NewError(foundation.ErrorDependencyUnavailable, "INGESTION_SERVICE_UNAVAILABLE", false, errors.New("ingestion dependencies are incomplete"))
	}
	if dependencies.ChunkOptions.StrategyVersion == "" || dependencies.ChunkOptions.SchemaVersion == "" {
		return nil, foundation.NewError(foundation.ErrorInvalidInput, "INGESTION_CHUNK_POLICY_INVALID", false, errors.New("chunk versions are required"))
	}
	if dependencies.ContentPolicy.MaxBytes == 0 {
		dependencies.ContentPolicy.MaxBytes = DefaultContentMaxBytes
	}
	if dependencies.ChunkOptions.SoftMaxBytes == 0 {
		dependencies.ChunkOptions.SoftMaxBytes = domain.DefaultChunkSoftMaxBytes
	}
	return &Service{dependencies: dependencies}, nil
}

// ProcessRequest 标识一次幂等摄取尝试。
type ProcessRequest struct {
	SourceVersionID foundation.ID
	WorkflowRunID   *foundation.ID
	IdempotencyKey  string
	AttemptNumber   int32
}

// ProcessResult 返回持久化 Attempt 和共享 Projection 结果。
type ProcessResult struct {
	Attempt        domain.AttemptRecord
	Projection     domain.ProjectionResult
	AttemptCreated bool
}

// Process 处理一个 SourceVersion；失败状态会先持久化再返回错误。
func (s *Service) Process(ctx context.Context, request ProcessRequest) (ProcessResult, error) {
	if s == nil {
		return ProcessResult{}, dependencyError("INGESTION_SERVICE_UNAVAILABLE")
	}
	if request.SourceVersionID == "" || strings.TrimSpace(request.IdempotencyKey) == "" || request.AttemptNumber <= 0 {
		return ProcessResult{}, invalidError("INGESTION_REQUEST_INVALID", errors.New("source version, idempotency key and attempt number are required"))
	}
	metadata, err := s.dependencies.Sources.GetSourceMetadata(ctx, request.SourceVersionID)
	if err != nil {
		return ProcessResult{}, err
	}
	if metadata.SourceVersionID != request.SourceVersionID || metadata.WorkspaceID == "" || metadata.ContentArtifactID == "" || metadata.ByteSize < 0 {
		return ProcessResult{}, foundation.NewError(foundation.ErrorConsistencyViolation, "INGESTION_SOURCE_SCOPE_INVALID", false, errors.New("source content scope is incomplete"))
	}
	parser, parserErr := s.dependencies.Parsers.ParserFor(metadata.MediaType)
	descriptor := domain.ParserDescriptor{}
	if parserErr != nil {
		var typed *domain.ParserError
		if !errors.As(parserErr, &typed) || typed.Code != "PARSER_MEDIA_TYPE_UNSUPPORTED" {
			return ProcessResult{}, parserErr
		}
		descriptor = domain.ParserDescriptor{ID: "unsupported", Version: "none", ConfigHash: strings.Repeat("0", sha256.Size*2), SchemaVersion: s.dependencies.ChunkOptions.SchemaVersion}
	} else {
		descriptor = parser.Descriptor()
		if err := validateDescriptor(descriptor); err != nil {
			return ProcessResult{}, invalidError("PARSER_DESCRIPTOR_INVALID", err)
		}
		if descriptor.SchemaVersion != s.dependencies.ChunkOptions.SchemaVersion {
			return ProcessResult{}, invalidError("PARSER_SCHEMA_VERSION_MISMATCH", errors.New("parser and chunk schema versions differ"))
		}
	}
	attemptID, err := s.dependencies.IDs.New()
	if err != nil {
		return ProcessResult{}, err
	}
	now := s.dependencies.Clock.Now()
	created, err := s.dependencies.Repository.CreateAttempt(ctx, domain.AttemptRecord{
		Attempt: domain.Attempt{
			ID: attemptID, WorkspaceID: metadata.WorkspaceID, SourceVersionID: metadata.SourceVersionID,
			WorkflowRunID: request.WorkflowRunID, Status: domain.AttemptValidating, SecurityStatus: domain.SecurityPending,
			ParserID: descriptor.ID, ParserVersion: descriptor.Version, ParserConfigHash: descriptor.ConfigHash,
			ChunkStrategyVersion: s.dependencies.ChunkOptions.StrategyVersion, SchemaVersion: descriptor.SchemaVersion,
			IdempotencyKey: request.IdempotencyKey, AttemptNumber: request.AttemptNumber,
		},
		StartedAt: now, Version: 1,
	})
	if err != nil {
		return ProcessResult{}, err
	}
	attempt := created.Attempt
	if !created.Created {
		if err := existingAttemptError(attempt); err != nil {
			return ProcessResult{Attempt: attempt}, err
		}
		if attempt.ParseProjectionID != nil && (attempt.Status == domain.AttemptParsed || attempt.Status == domain.AttemptChunking || attempt.Status == domain.AttemptChunked) {
			projection, projectionErr := s.dependencies.Repository.GetProjection(ctx, *attempt.ParseProjectionID, s.dependencies.ChunkOptions.StrategyVersion, s.dependencies.ChunkOptions.SchemaVersion)
			if projectionErr != nil {
				return ProcessResult{Attempt: attempt}, projectionErr
			}
			if attempt.Status == domain.AttemptParsed {
				attempt, err = s.transition(ctx, attempt, domain.AttemptTransition{
					Status: domain.AttemptChunking, SecurityStatus: domain.SecurityPassed,
					ParseProjectionID: attempt.ParseProjectionID, Warnings: attempt.Warnings,
				})
				if err != nil {
					return ProcessResult{}, err
				}
			}
			if attempt.Status == domain.AttemptChunking {
				completedAt := s.dependencies.Clock.Now()
				attempt, err = s.transition(ctx, attempt, domain.AttemptTransition{
					Status: domain.AttemptChunked, SecurityStatus: domain.SecurityPassed,
					ParseProjectionID: attempt.ParseProjectionID, Warnings: attempt.Warnings, CompletedAt: &completedAt,
				})
				if err != nil {
					return ProcessResult{}, err
				}
			}
			return ProcessResult{Attempt: attempt, Projection: projection, AttemptCreated: false}, nil
		}
		// A non-terminal attempt may have been interrupted after a durable
		// transition. Re-run the deterministic parse and let the idempotent
		// projection write repair the remaining transitions instead of
		// reporting an incomplete attempt as success.
	}
	if parserErr != nil {
		result, failureErr := s.finishParseFailure(ctx, attempt, nil, parserErr)
		result.AttemptCreated = created.Created
		return result, failureErr
	}
	if s.dependencies.ContentPolicy.MaxBytes > 0 && metadata.ByteSize > s.dependencies.ContentPolicy.MaxBytes {
		result, failureErr := s.finishValidationFailure(ctx, attempt, nil, &ContentValidationError{Code: "SOURCE_FILE_TOO_LARGE", Cause: errors.New("source exceeds configured parser limit")})
		result.AttemptCreated = created.Created
		return result, failureErr
	}
	contentBytes, readErr := s.dependencies.Sources.ReadSourceContent(ctx, metadata)
	if readErr != nil {
		result, failureErr := s.finishReadFailure(ctx, attempt, readErr)
		result.AttemptCreated = created.Created
		return result, failureErr
	}
	content := SourceContent{
		WorkspaceID: metadata.WorkspaceID, SourceVersionID: metadata.SourceVersionID,
		ContentArtifactID: metadata.ContentArtifactID, MediaType: metadata.MediaType, Bytes: contentBytes,
	}
	warnings, validationErr := ValidateSourceContent(content.MediaType, content.Bytes, s.dependencies.ContentPolicy)
	if validationErr != nil {
		result, failureErr := s.finishValidationFailure(ctx, attempt, warnings, validationErr)
		result.AttemptCreated = created.Created
		return result, failureErr
	}
	if attempt.Status == domain.AttemptValidating {
		attempt, err = s.transition(ctx, attempt, domain.AttemptTransition{
			Status: domain.AttemptParsing, SecurityStatus: domain.SecurityPassed, Warnings: warnings,
		})
		if err != nil {
			return ProcessResult{}, err
		}
	} else if attempt.SecurityStatus != domain.SecurityPassed {
		return ProcessResult{}, foundation.NewError(foundation.ErrorConsistencyViolation, "INGESTION_ATTEMPT_STATE_INVALID", false, errors.New("non-validating attempt is not security passed"))
	}
	parsed, parseErr := domain.ParseContent(ctx, parser, domain.SourceInput{
		SourceVersionID: content.SourceVersionID, MediaType: content.MediaType, ImmutableBytes: content.Bytes,
	})
	if parseErr != nil {
		result, failureErr := s.finishParseFailure(ctx, attempt, warnings, parseErr)
		result.AttemptCreated = created.Created
		return result, failureErr
	}
	if parsed.ParserID != descriptor.ID || parsed.ParserVersion != descriptor.Version || parsed.ParserConfigHash != descriptor.ConfigHash || parsed.SchemaVersion != descriptor.SchemaVersion {
		result, failureErr := s.finishParseFailure(ctx, attempt, warnings, &domain.ParserError{Code: "PARSER_DESCRIPTOR_MISMATCH", Cause: errors.New("parser output descriptor differs from registered descriptor")})
		result.AttemptCreated = created.Created
		return result, failureErr
	}
	warnings = mergeWarnings(warnings, parsed.Warnings)
	projectionID, spans, buildErr := s.buildSpans(content, parsed)
	if buildErr != nil {
		result, failureErr := s.finishParseFailure(ctx, attempt, warnings, buildErr)
		result.AttemptCreated = created.Created
		return result, failureErr
	}
	chunks, chunkErr := domain.BuildCanonicalChunks(parsed, spans, s.dependencies.ChunkOptions)
	if chunkErr != nil {
		result, failureErr := s.finishParseFailure(ctx, attempt, warnings, chunkErr)
		result.AttemptCreated = created.Created
		return result, failureErr
	}
	warnings = mergeWarnings(warnings, chunks.Warnings)
	for index := range chunks.Chunks {
		id, idErr := s.dependencies.IDs.New()
		if idErr != nil {
			result, failureErr := s.finishParseFailure(ctx, attempt, warnings, idErr)
			result.AttemptCreated = created.Created
			return result, failureErr
		}
		chunks.Chunks[index].ID = id
	}
	projection, err := s.dependencies.Repository.SaveProjection(ctx, domain.ProjectionWrite{
		SourceVersionID: content.SourceVersionID,
		Projection: domain.ParseProjection{
			ID: projectionID, WorkspaceID: content.WorkspaceID, ContentArtifactID: content.ContentArtifactID,
			ParserID: parsed.ParserID, ParserVersion: parsed.ParserVersion, ParserConfigHash: parsed.ParserConfigHash,
			SchemaVersion: parsed.SchemaVersion, NormalizedContentHash: parsed.NormalizedContentHash,
			Warnings: parsed.Warnings, CreatedAt: s.dependencies.Clock.Now(),
		},
		Spans: spans, Chunks: chunks.Chunks, CreatedAt: s.dependencies.Clock.Now(),
	})
	if err != nil {
		result, failureErr := s.finishParseFailure(ctx, attempt, warnings, err)
		result.AttemptCreated = created.Created
		return result, failureErr
	}
	projectionID = projection.Projection.ID
	if attempt.ParseProjectionID != nil && *attempt.ParseProjectionID != projectionID {
		return ProcessResult{}, foundation.NewError(foundation.ErrorConsistencyViolation, "INGESTION_PROJECTION_MISMATCH", false, errors.New("attempt points to a different parse projection"))
	}
	if attempt.Status == domain.AttemptParsing {
		attempt, err = s.transition(ctx, attempt, domain.AttemptTransition{
			Status: domain.AttemptParsed, SecurityStatus: domain.SecurityPassed, ParseProjectionID: &projectionID, Warnings: warnings,
		})
		if err != nil {
			return ProcessResult{}, err
		}
	}
	if attempt.Status == domain.AttemptParsed {
		attempt, err = s.transition(ctx, attempt, domain.AttemptTransition{
			Status: domain.AttemptChunking, SecurityStatus: domain.SecurityPassed, ParseProjectionID: &projectionID, Warnings: warnings,
		})
		if err != nil {
			return ProcessResult{}, err
		}
	}
	if attempt.Status == domain.AttemptChunking {
		completedAt := s.dependencies.Clock.Now()
		attempt, err = s.transition(ctx, attempt, domain.AttemptTransition{
			Status: domain.AttemptChunked, SecurityStatus: domain.SecurityPassed, ParseProjectionID: &projectionID,
			Warnings: warnings, CompletedAt: &completedAt,
		})
		if err != nil {
			return ProcessResult{}, err
		}
	}
	if attempt.Status != domain.AttemptChunked {
		return ProcessResult{}, foundation.NewError(foundation.ErrorConsistencyViolation, "INGESTION_ATTEMPT_STATE_INVALID", false, errors.New("attempt did not reach a terminal ingestion state"))
	}
	return ProcessResult{Attempt: attempt, Projection: projection, AttemptCreated: created.Created}, nil
}

func (s *Service) finishReadFailure(ctx context.Context, attempt domain.AttemptRecord, cause error) (ProcessResult, error) {
	kind, code, retryable := classifyFailure(cause)
	status := domain.AttemptParseFailed
	if errors.Is(cause, context.Canceled) || errors.Is(cause, context.DeadlineExceeded) {
		status = domain.AttemptCancelled
		kind, code, retryable = foundation.ErrorNonRetryableFailure, "INGESTION_CANCELLED", false
	}
	completedAt := s.dependencies.Clock.Now()
	persistCtx, cancel := persistenceContext(ctx)
	defer cancel()
	updated, err := s.transition(persistCtx, attempt, domain.AttemptTransition{
		Status: status, SecurityStatus: attempt.SecurityStatus, FailureStage: "read",
		ErrorCode: code, Retryable: retryable, CompletedAt: &completedAt,
	})
	if err != nil {
		return ProcessResult{}, err
	}
	var classified *foundation.Error
	if errors.As(cause, &classified) && status != domain.AttemptCancelled {
		return ProcessResult{Attempt: updated}, classified
	}
	return ProcessResult{Attempt: updated}, foundation.NewError(kind, code, retryable, cause)
}

func (s *Service) buildSpans(content SourceContent, parsed domain.ParsedDocument) (foundation.ID, []domain.SourceSpan, error) {
	projectionID, err := s.dependencies.IDs.New()
	if err != nil {
		return "", nil, err
	}
	spans := make([]domain.SourceSpan, 0, len(parsed.Blocks))
	for _, block := range parsed.Blocks {
		if block.StartByte < 0 || block.EndByte < block.StartByte || block.EndByte > int64(len(content.Bytes)) || block.StartLine < 1 || block.EndLine < block.StartLine {
			return "", nil, foundation.NewError(foundation.ErrorConsistencyViolation, "PARSER_SOURCE_RANGE_INVALID", false, errors.New("parser returned an invalid raw range"))
		}
		id, idErr := s.dependencies.IDs.New()
		if idErr != nil {
			return "", nil, idErr
		}
		evidenceKind := block.EvidenceKind
		if evidenceKind == "" {
			evidenceKind = domain.EvidenceRawBytes
		}
		var evidence []byte
		derivedExcerpt := ""
		switch evidenceKind {
		case domain.EvidenceRawBytes:
			evidence = content.Bytes[block.StartByte:block.EndByte]
			if string(evidence) != block.Content {
				return "", nil, foundation.NewError(foundation.ErrorConsistencyViolation, "PARSER_SOURCE_CONTENT_MISMATCH", false, errors.New("parser content differs from its raw evidence range"))
			}
		case domain.EvidenceDerivedText:
			if block.Content == "" || block.StartByte != 0 || block.EndByte != int64(len(content.Bytes)) {
				return "", nil, foundation.NewError(foundation.ErrorConsistencyViolation, "PARSER_DERIVED_EVIDENCE_INVALID", false, errors.New("derived evidence must bind the complete immutable artifact and contain text"))
			}
			evidence = []byte(block.Content)
			derivedExcerpt = block.Content
		default:
			return "", nil, foundation.NewError(foundation.ErrorConsistencyViolation, "PARSER_EVIDENCE_KIND_INVALID", false, errors.New("parser returned an unknown evidence kind"))
		}
		excerpt := sha256.Sum256(evidence)
		spans = append(spans, domain.SourceSpan{
			ID: id, WorkspaceID: content.WorkspaceID, ContentArtifactID: content.ContentArtifactID,
			ParseProjectionID: projectionID, SpanType: block.SpanType, StartLine: block.StartLine, EndLine: block.EndLine,
			StartByte: block.StartByte, EndByte: block.EndByte, Selector: cloneSelector(block.Selector),
			ExcerptHash: hex.EncodeToString(excerpt[:]), EvidenceKind: evidenceKind, DerivedExcerpt: derivedExcerpt,
			ParserVersion: parsed.ParserVersion, SchemaVersion: parsed.SchemaVersion,
		})
	}
	return projectionID, spans, nil
}

func (s *Service) finishValidationFailure(ctx context.Context, attempt domain.AttemptRecord, warnings []domain.Warning, cause error) (ProcessResult, error) {
	var validation *ContentValidationError
	if !errors.As(cause, &validation) {
		return s.finishParseFailure(ctx, attempt, warnings, cause)
	}
	completedAt := s.dependencies.Clock.Now()
	status := domain.AttemptParseFailed
	security := domain.SecurityPending
	if validation.Quarantine {
		status = domain.AttemptValidating
		security = domain.SecurityQuarantined
	}
	updated, err := s.transition(ctx, attempt, domain.AttemptTransition{
		Status: status, SecurityStatus: security, FailureStage: "validation", ErrorCode: validation.Code,
		Warnings: warnings, CompletedAt: &completedAt,
	})
	if err != nil {
		return ProcessResult{}, err
	}
	kind := foundation.ErrorInvalidInput
	if validation.Quarantine {
		kind = foundation.ErrorPermissionDenied
	}
	return ProcessResult{Attempt: updated}, foundation.NewError(kind, validation.Code, false, cause)
}

func (s *Service) finishParseFailure(ctx context.Context, attempt domain.AttemptRecord, warnings []domain.Warning, cause error) (ProcessResult, error) {
	kind, code, retryable := classifyFailure(cause)
	status := domain.AttemptParseFailed
	if errors.Is(cause, context.Canceled) || errors.Is(cause, context.DeadlineExceeded) {
		status = domain.AttemptCancelled
	}
	completedAt := s.dependencies.Clock.Now()
	persistCtx, cancel := persistenceContext(ctx)
	defer cancel()
	updated, err := s.transition(persistCtx, attempt, domain.AttemptTransition{
		Status: status, SecurityStatus: attempt.SecurityStatus, FailureStage: "parse",
		ErrorCode: code, Retryable: retryable, Warnings: warnings, CompletedAt: &completedAt,
	})
	if err != nil {
		return ProcessResult{}, err
	}
	if errors.Is(cause, context.Canceled) || errors.Is(cause, context.DeadlineExceeded) {
		return ProcessResult{Attempt: updated}, foundation.NewError(foundation.ErrorNonRetryableFailure, "INGESTION_CANCELLED", false, cause)
	}
	var classified *foundation.Error
	if errors.As(cause, &classified) {
		return ProcessResult{Attempt: updated}, classified
	}
	return ProcessResult{Attempt: updated}, foundation.NewError(kind, code, retryable, cause)
}

func existingAttemptError(attempt domain.AttemptRecord) error {
	switch {
	case attempt.Status == domain.AttemptChunked:
		return nil
	case attempt.Status == domain.AttemptValidating && attempt.SecurityStatus == domain.SecurityQuarantined:
		return foundation.NewError(foundation.ErrorPermissionDenied, nonEmptyCode(attempt.ErrorCode, "SOURCE_QUARANTINED"), false, errors.New("source is quarantined"))
	case attempt.Status == domain.AttemptParseFailed:
		kind := foundation.ErrorNonRetryableFailure
		if attempt.FailureStage == "validation" {
			kind = foundation.ErrorInvalidInput
		} else if attempt.Retryable {
			kind = foundation.ErrorRetryableFailure
		}
		return foundation.NewError(kind, nonEmptyCode(attempt.ErrorCode, "INGESTION_PARSE_FAILED"), attempt.Retryable, errors.New("ingestion attempt already failed"))
	case attempt.Status == domain.AttemptCancelled:
		return foundation.NewError(foundation.ErrorNonRetryableFailure, nonEmptyCode(attempt.ErrorCode, "INGESTION_CANCELLED"), false, errors.New("ingestion attempt was cancelled"))
	default:
		return nil
	}
}

func classifyFailure(cause error) (foundation.ErrorKind, string, bool) {
	if errors.Is(cause, context.Canceled) || errors.Is(cause, context.DeadlineExceeded) {
		return foundation.ErrorNonRetryableFailure, "INGESTION_CANCELLED", false
	}
	var classified *foundation.Error
	if errors.As(cause, &classified) {
		return classified.Kind, classified.Code, classified.Retryable
	}
	var parserErr *domain.ParserError
	if errors.As(cause, &parserErr) && parserErr.Code != "" {
		if strings.HasPrefix(parserErr.Code, "SOURCE_") || parserErr.Code == "PARSER_MEDIA_TYPE_UNSUPPORTED" {
			return foundation.ErrorInvalidInput, parserErr.Code, false
		}
		return foundation.ErrorNonRetryableFailure, parserErr.Code, false
	}
	return foundation.ErrorNonRetryableFailure, "INGESTION_PARSE_FAILED", false
}

func nonEmptyCode(value, fallback string) string {
	if strings.TrimSpace(value) == "" {
		return fallback
	}
	return value
}

func persistenceContext(ctx context.Context) (context.Context, context.CancelFunc) {
	if ctx.Err() == nil {
		return ctx, func() {}
	}
	base := context.WithoutCancel(ctx)
	return context.WithTimeout(base, 2*time.Second)
}

func (s *Service) transition(ctx context.Context, attempt domain.AttemptRecord, transition domain.AttemptTransition) (domain.AttemptRecord, error) {
	transition.ID = attempt.ID
	transition.ExpectedVersion = attempt.Version
	return s.dependencies.Repository.TransitionAttempt(ctx, transition)
}

func validateDescriptor(descriptor domain.ParserDescriptor) error {
	if descriptor.ID == "" || descriptor.Version == "" || descriptor.SchemaVersion == "" || len(descriptor.ConfigHash) != sha256.Size*2 {
		return errors.New("parser descriptor fields are invalid")
	}
	if _, err := hex.DecodeString(descriptor.ConfigHash); err != nil {
		return errors.New("parser config hash is invalid")
	}
	return nil
}

func cloneSelector(source map[string]string) map[string]string {
	if source == nil {
		return map[string]string{}
	}
	clone := make(map[string]string, len(source))
	for key, value := range source {
		clone[key] = value
	}
	return clone
}

func mergeWarnings(existing, additions []domain.Warning) []domain.Warning {
	result := append([]domain.Warning(nil), existing...)
	seen := make(map[string]struct{}, len(result))
	for _, warning := range result {
		seen[warningKey(warning)] = struct{}{}
	}
	for _, warning := range additions {
		key := warningKey(warning)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		result = append(result, warning)
	}
	return result
}

func warningKey(warning domain.Warning) string {
	// Code and source range identify the warning occurrence; messages may be
	// localized or produced by different validation/parser layers.
	return warning.Code + "\x00" + fmtInt(warning.StartByte) + "\x00" + fmtInt(warning.EndByte)
}

func fmtInt(value int64) string {
	return strconv.FormatInt(value, 10)
}

func invalidError(code string, cause error) error {
	return foundation.NewError(foundation.ErrorInvalidInput, code, false, cause)
}

func dependencyError(code string) error {
	return foundation.NewError(foundation.ErrorDependencyUnavailable, code, false, errors.New("ingestion dependency is unavailable"))
}
