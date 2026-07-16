package application

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	"github.com/CodeZen-Lizhi/zhixu/internal/ingestion/domain"
)

func TestServiceProcessPersistsChunkedProjection(t *testing.T) {
	workspaceID := foundation.ID("10000000-0000-4000-8000-000000000001")
	versionID := foundation.ID("10000000-0000-4000-8000-000000000002")
	artifactID := foundation.ID("10000000-0000-4000-8000-000000000003")
	repository := &fakeIngestionRepository{}
	service := newIngestionTestService(t, repository, SourceContent{
		WorkspaceID: workspaceID, SourceVersionID: versionID, ContentArtifactID: artifactID,
		MediaType: "text/markdown", Bytes: []byte("# Title\n"),
	})

	result, err := service.Process(context.Background(), ProcessRequest{SourceVersionID: versionID, IdempotencyKey: "scan:one", AttemptNumber: 1})
	if err != nil {
		t.Fatal(err)
	}
	if result.Attempt.Status != domain.AttemptChunked || result.Attempt.SecurityStatus != domain.SecurityPassed || result.Attempt.ParseProjectionID == nil {
		t.Fatalf("attempt = %#v", result.Attempt)
	}
	if len(repository.transitions) != 4 || repository.transitions[0].Status != domain.AttemptParsing || repository.transitions[3].Status != domain.AttemptChunked {
		t.Fatalf("transitions = %#v", repository.transitions)
	}
	if len(repository.saved.Spans) != 1 || len(repository.saved.Chunks) != 1 {
		t.Fatalf("projection write = %#v", repository.saved)
	}
	if repository.saved.Spans[0].StartByte != 0 || repository.saved.Spans[0].EndByte != 8 || repository.saved.Chunks[0].Content != "# Title\n" {
		t.Fatalf("span/chunk = %#v / %#v", repository.saved.Spans, repository.saved.Chunks)
	}
}

func TestServiceProcessQuarantinesNULWithoutCallingParser(t *testing.T) {
	versionID := foundation.ID("20000000-0000-4000-8000-000000000002")
	parser := &fakeApplicationParser{}
	repository := &fakeIngestionRepository{}
	service := newIngestionTestServiceWithParser(t, repository, SourceContent{
		WorkspaceID: "20000000-0000-4000-8000-000000000001", SourceVersionID: versionID,
		ContentArtifactID: "20000000-0000-4000-8000-000000000003", MediaType: "text/plain", Bytes: []byte{'a', 0, 'b'},
	}, parser)

	result, err := service.Process(context.Background(), ProcessRequest{SourceVersionID: versionID, IdempotencyKey: "scan:nul", AttemptNumber: 1})
	var classified *foundation.Error
	if !errors.As(err, &classified) || classified.Code != "SOURCE_BINARY_CONTENT" || classified.Kind != foundation.ErrorPermissionDenied {
		t.Fatalf("error = %#v", err)
	}
	if parser.calls != 0 || result.Attempt.Status != domain.AttemptValidating || result.Attempt.SecurityStatus != domain.SecurityQuarantined || result.Attempt.CompletedAt == nil {
		t.Fatalf("quarantine result = %#v, parser calls=%d", result, parser.calls)
	}
}

func TestServiceProcessQuarantinesBinaryControlWithoutCallingParser(t *testing.T) {
	versionID := foundation.ID("20000000-0000-4000-8000-000000000012")
	parser := &fakeApplicationParser{}
	repository := &fakeIngestionRepository{}
	service := newIngestionTestServiceWithParser(t, repository, SourceContent{
		WorkspaceID: "20000000-0000-4000-8000-000000000011", SourceVersionID: versionID,
		ContentArtifactID: "20000000-0000-4000-8000-000000000013", MediaType: "text/plain", Bytes: []byte{'a', 0x01, 'b'},
	}, parser)

	result, err := service.Process(context.Background(), ProcessRequest{SourceVersionID: versionID, IdempotencyKey: "scan:control", AttemptNumber: 1})
	var classified *foundation.Error
	if !errors.As(err, &classified) || classified.Code != "SOURCE_BINARY_CONTENT" || classified.Kind != foundation.ErrorPermissionDenied {
		t.Fatalf("error = %#v", err)
	}
	if parser.calls != 0 || result.Attempt.Status != domain.AttemptValidating || result.Attempt.SecurityStatus != domain.SecurityQuarantined {
		t.Fatalf("quarantine result = %#v, parser calls=%d", result, parser.calls)
	}
}

func TestServiceReplaysParsedAndChunkingAttempts(t *testing.T) {
	for _, status := range []domain.AttemptStatus{domain.AttemptParsed, domain.AttemptChunking} {
		t.Run(string(status), func(t *testing.T) {
			versionID := foundation.ID("21000000-0000-4000-8000-000000000012")
			projectionID := foundation.ID("21000000-0000-4000-8000-000000000014")
			repository := &fakeIngestionRepository{existing: true, attempt: domain.AttemptRecord{Attempt: domain.Attempt{
				ID: "21000000-0000-4000-8000-000000000011", WorkspaceID: "21000000-0000-4000-8000-000000000013", SourceVersionID: versionID,
				Status: status, SecurityStatus: domain.SecurityPassed, ParserID: "fake", ParserVersion: "fake-1", ParserConfigHash: strings.Repeat("a", 64), ChunkStrategyVersion: "structure-v1", SchemaVersion: "parse-v1", IdempotencyKey: "replay-checkpoint", AttemptNumber: 1,
			}, ParseProjectionID: &projectionID, Version: 2}, projection: domain.ProjectionResult{Projection: domain.ParseProjection{ID: projectionID}, Chunks: []domain.CanonicalChunk{{}}}}
			parser := &fakeApplicationParser{}
			service := newIngestionTestServiceWithParser(t, repository, SourceContent{WorkspaceID: "21000000-0000-4000-8000-000000000013", SourceVersionID: versionID, ContentArtifactID: "21000000-0000-4000-8000-000000000015", MediaType: "text/plain", Bytes: []byte("body")}, parser)
			result, err := service.Process(context.Background(), ProcessRequest{SourceVersionID: versionID, IdempotencyKey: "replay-checkpoint", AttemptNumber: 1})
			if err != nil || parser.calls != 0 || result.Attempt.Status != domain.AttemptChunked || len(result.Projection.Chunks) != 1 {
				t.Fatalf("replay = %#v, parser calls=%d, err=%v", result, parser.calls, err)
			}
		})
	}
}

func TestServicePersistsCancellation(t *testing.T) {
	versionID := foundation.ID("22000000-0000-4000-8000-000000000012")
	repository := &fakeIngestionRepository{}
	parser := &fakeApplicationParser{parseErr: context.Canceled}
	service := newIngestionTestServiceWithParser(t, repository, SourceContent{WorkspaceID: "22000000-0000-4000-8000-000000000011", SourceVersionID: versionID, ContentArtifactID: "22000000-0000-4000-8000-000000000013", MediaType: "text/plain", Bytes: []byte("body")}, parser)
	result, err := service.Process(context.Background(), ProcessRequest{SourceVersionID: versionID, IdempotencyKey: "cancelled", AttemptNumber: 1})
	var classified *foundation.Error
	if !errors.As(err, &classified) || classified.Code != "INGESTION_CANCELLED" || classified.Retryable || result.Attempt.Status != domain.AttemptCancelled {
		t.Fatalf("cancel = %#v, attempt=%#v", err, result.Attempt)
	}
}

func TestServicePersistsReadCancellationAfterAttemptCreation(t *testing.T) {
	versionID := foundation.ID("22000000-0000-4000-8000-000000000022")
	repository := &fakeIngestionRepository{}
	parser := &fakeApplicationParser{}
	reader := &fakeSourceContentReader{content: SourceContent{WorkspaceID: "22000000-0000-4000-8000-000000000021", SourceVersionID: versionID, ContentArtifactID: "22000000-0000-4000-8000-000000000023", MediaType: "text/plain", Bytes: []byte("body")}, readErr: context.Canceled}
	service := newIngestionTestServiceWithSource(t, repository, reader, fakeParserRegistry{parser: parser})
	result, err := service.Process(context.Background(), ProcessRequest{SourceVersionID: versionID, IdempotencyKey: "read-cancelled", AttemptNumber: 1})
	var classified *foundation.Error
	if !errors.As(err, &classified) || classified.Code != "INGESTION_CANCELLED" || classified.Retryable || result.Attempt.Status != domain.AttemptCancelled || result.Attempt.FailureStage != "read" || parser.calls != 0 {
		t.Fatalf("read cancel = %#v, attempt=%#v, parser calls=%d", err, result.Attempt, parser.calls)
	}
}

func TestServicePersistsOversizeBeforeFilesystemRead(t *testing.T) {
	versionID := foundation.ID("22000000-0000-4000-8000-000000000032")
	repository := &fakeIngestionRepository{}
	parser := &fakeApplicationParser{}
	reader := &fakeSourceContentReader{content: SourceContent{WorkspaceID: "22000000-0000-4000-8000-000000000031", SourceVersionID: versionID, ContentArtifactID: "22000000-0000-4000-8000-000000000033", MediaType: "text/plain", Bytes: make([]byte, 1025)}}
	service := newIngestionTestServiceWithSource(t, repository, reader, fakeParserRegistry{parser: parser})
	result, err := service.Process(context.Background(), ProcessRequest{SourceVersionID: versionID, IdempotencyKey: "oversize", AttemptNumber: 1})
	var classified *foundation.Error
	if !errors.As(err, &classified) || classified.Code != "SOURCE_FILE_TOO_LARGE" || result.Attempt.Status != domain.AttemptParseFailed || result.Attempt.FailureStage != "validation" || reader.readCalls != 0 || parser.calls != 0 {
		t.Fatalf("oversize = %#v, attempt=%#v, reads=%d, parser calls=%d", err, result.Attempt, reader.readCalls, parser.calls)
	}
}

func TestServicePersistsReadDependencyFailure(t *testing.T) {
	versionID := foundation.ID("22000000-0000-4000-8000-000000000042")
	repository := &fakeIngestionRepository{}
	reader := &fakeSourceContentReader{content: SourceContent{WorkspaceID: "22000000-0000-4000-8000-000000000041", SourceVersionID: versionID, ContentArtifactID: "22000000-0000-4000-8000-000000000043", MediaType: "text/plain", Bytes: []byte("body")}, readErr: foundation.NewError(foundation.ErrorRetryableFailure, "CONTENT_ARTIFACT_READ_FAILED", true, errors.New("temporary filesystem failure"))}
	service := newIngestionTestServiceWithSource(t, repository, reader, fakeParserRegistry{parser: &fakeApplicationParser{}})
	result, err := service.Process(context.Background(), ProcessRequest{SourceVersionID: versionID, IdempotencyKey: "read-failure", AttemptNumber: 1})
	var classified *foundation.Error
	if !errors.As(err, &classified) || classified.Code != "CONTENT_ARTIFACT_READ_FAILED" || !classified.Retryable || result.Attempt.Status != domain.AttemptParseFailed || result.Attempt.FailureStage != "read" || !result.Attempt.Retryable {
		t.Fatalf("read failure = %#v, attempt=%#v", err, result.Attempt)
	}
}

func TestServiceCreatesFailedAttemptForUnsupportedMIME(t *testing.T) {
	versionID := foundation.ID("23000000-0000-4000-8000-000000000012")
	repository := &fakeIngestionRepository{}
	service := newIngestionTestServiceWithRegistry(t, repository, SourceContent{WorkspaceID: "23000000-0000-4000-8000-000000000011", SourceVersionID: versionID, ContentArtifactID: "23000000-0000-4000-8000-000000000013", MediaType: "application/pdf", Bytes: []byte("body")}, &fakeApplicationParser{}, fakeParserRegistry{err: &domain.ParserError{Code: "PARSER_MEDIA_TYPE_UNSUPPORTED", Cause: domain.ErrUnsupportedMediaType}})
	result, err := service.Process(context.Background(), ProcessRequest{SourceVersionID: versionID, IdempotencyKey: "unsupported", AttemptNumber: 1})
	var classified *foundation.Error
	if !errors.As(err, &classified) || classified.Kind != foundation.ErrorInvalidInput || classified.Code != "PARSER_MEDIA_TYPE_UNSUPPORTED" || result.Attempt.Status != domain.AttemptParseFailed || result.Attempt.ErrorCode != "PARSER_MEDIA_TYPE_UNSUPPORTED" {
		t.Fatalf("unsupported = %#v, attempt=%#v", err, result.Attempt)
	}
}

func TestServiceReplaysChunkedAttemptWithoutReparsing(t *testing.T) {
	versionID := foundation.ID("21000000-0000-4000-8000-000000000002")
	projectionID := foundation.ID("21000000-0000-4000-8000-000000000004")
	repository := &fakeIngestionRepository{
		existing: true,
		attempt: domain.AttemptRecord{Attempt: domain.Attempt{
			ID: "21000000-0000-4000-8000-000000000001", WorkspaceID: "21000000-0000-4000-8000-000000000003", SourceVersionID: versionID,
			Status: domain.AttemptChunked, SecurityStatus: domain.SecurityPassed, ParserID: "fake", ParserVersion: "fake-1", ParserConfigHash: strings.Repeat("a", 64), ChunkStrategyVersion: "structure-v1", SchemaVersion: "parse-v1", IdempotencyKey: "replay", AttemptNumber: 1,
		}, ParseProjectionID: &projectionID, Version: 4},
		projection: domain.ProjectionResult{Projection: domain.ParseProjection{ID: projectionID}, Chunks: []domain.CanonicalChunk{{}}},
	}
	parser := &fakeApplicationParser{}
	service := newIngestionTestServiceWithParser(t, repository, SourceContent{WorkspaceID: "21000000-0000-4000-8000-000000000003", SourceVersionID: versionID, ContentArtifactID: "21000000-0000-4000-8000-000000000005", MediaType: "text/plain", Bytes: []byte("body")}, parser)
	result, err := service.Process(context.Background(), ProcessRequest{SourceVersionID: versionID, IdempotencyKey: "replay", AttemptNumber: 1})
	if err != nil || parser.calls != 0 || result.Attempt.Status != domain.AttemptChunked || len(result.Projection.Chunks) != 1 {
		t.Fatalf("replay = %#v, parser calls=%d, err=%v", result, parser.calls, err)
	}
}

func TestServicePreservesRetryableProjectionFailure(t *testing.T) {
	repository := &fakeIngestionRepository{saveErr: foundation.NewError(foundation.ErrorRetryableFailure, "INGESTION_PROJECTION_COMMIT_FAILED", true, errors.New("serialization failure"))}
	versionID := foundation.ID("22000000-0000-4000-8000-000000000002")
	service := newIngestionTestService(t, repository, SourceContent{WorkspaceID: "22000000-0000-4000-8000-000000000001", SourceVersionID: versionID, ContentArtifactID: "22000000-0000-4000-8000-000000000003", MediaType: "text/markdown", Bytes: []byte("# body\n")})
	result, err := service.Process(context.Background(), ProcessRequest{SourceVersionID: versionID, IdempotencyKey: "retryable", AttemptNumber: 1})
	var classified *foundation.Error
	if !errors.As(err, &classified) || classified.Kind != foundation.ErrorRetryableFailure || !classified.Retryable || !result.Attempt.Retryable || result.Attempt.Status != domain.AttemptParseFailed {
		t.Fatalf("failure = %#v, attempt=%#v", err, result.Attempt)
	}
}

func TestServiceDeduplicatesWarningsFromValidationAndParser(t *testing.T) {
	versionID := foundation.ID("23000000-0000-4000-8000-000000000002")
	repository := &fakeIngestionRepository{}
	parser := &fakeApplicationParser{emitBOMWarning: true}
	service := newIngestionTestServiceWithParser(t, repository, SourceContent{WorkspaceID: "23000000-0000-4000-8000-000000000001", SourceVersionID: versionID, ContentArtifactID: "23000000-0000-4000-8000-000000000003", MediaType: "text/plain", Bytes: []byte("\xef\xbb\xbfbody")}, parser)
	result, err := service.Process(context.Background(), ProcessRequest{SourceVersionID: versionID, IdempotencyKey: "warnings", AttemptNumber: 1})
	if err != nil || len(result.Attempt.Warnings) != 1 || result.Attempt.Warnings[0].Code != "UTF8_BOM_PRESENT" {
		t.Fatalf("warnings = %#v, err=%v", result.Attempt.Warnings, err)
	}
}

func newIngestionTestService(t *testing.T, repository *fakeIngestionRepository, content SourceContent) *Service {
	t.Helper()
	return newIngestionTestServiceWithParser(t, repository, content, &fakeApplicationParser{})
}

func newIngestionTestServiceWithParser(t *testing.T, repository *fakeIngestionRepository, content SourceContent, parser *fakeApplicationParser) *Service {
	t.Helper()
	return newIngestionTestServiceWithRegistry(t, repository, content, parser, fakeParserRegistry{parser: parser})
}

func newIngestionTestServiceWithRegistry(t *testing.T, repository *fakeIngestionRepository, content SourceContent, parser *fakeApplicationParser, registry fakeParserRegistry) *Service {
	t.Helper()
	return newIngestionTestServiceWithSource(t, repository, &fakeSourceContentReader{content: content}, registry)
}

func newIngestionTestServiceWithSource(t *testing.T, repository *fakeIngestionRepository, sources SourceContentReader, registry fakeParserRegistry) *Service {
	t.Helper()
	ids := []foundation.ID{
		"30000000-0000-4000-8000-000000000001", "30000000-0000-4000-8000-000000000002",
		"30000000-0000-4000-8000-000000000003", "30000000-0000-4000-8000-000000000004",
	}
	service, err := NewService(Dependencies{
		Repository:    repository,
		Sources:       sources,
		Parsers:       registry,
		IDs:           &sequenceIngestionIDs{ids: ids},
		Clock:         foundation.FixedClock{Value: time.Date(2026, 7, 17, 0, 0, 0, 0, time.UTC)},
		ContentPolicy: ContentPolicy{MaxBytes: 1024},
		ChunkOptions:  domain.ChunkOptions{StrategyVersion: "structure-v1", SchemaVersion: "parse-v1", SoftMaxBytes: 1024},
	})
	if err != nil {
		t.Fatal(err)
	}
	return service
}

type fakeSourceContentReader struct {
	content     SourceContent
	metadataErr error
	readErr     error
	readCalls   int
}

func (f *fakeSourceContentReader) GetSourceMetadata(_ context.Context, _ foundation.ID) (SourceMetadata, error) {
	if f.metadataErr != nil {
		return SourceMetadata{}, f.metadataErr
	}
	return SourceMetadata{
		WorkspaceID: f.content.WorkspaceID, SourceVersionID: f.content.SourceVersionID,
		ContentArtifactID: f.content.ContentArtifactID, MediaType: f.content.MediaType, ByteSize: int64(len(f.content.Bytes)),
	}, nil
}

func (f *fakeSourceContentReader) ReadSourceContent(_ context.Context, _ SourceMetadata) ([]byte, error) {
	f.readCalls++
	return f.content.Bytes, f.readErr
}

type fakeParserRegistry struct {
	parser domain.Parser
	err    error
}

func (f fakeParserRegistry) ParserFor(string) (domain.Parser, error) { return f.parser, f.err }

type fakeApplicationParser struct {
	calls          int
	emitBOMWarning bool
	parseErr       error
}

func (p *fakeApplicationParser) Supports(mediaType string) bool {
	return mediaType == "text/markdown" || mediaType == "text/plain"
}
func (p *fakeApplicationParser) Version() string { return "fake-1" }
func (p *fakeApplicationParser) Descriptor() domain.ParserDescriptor {
	return domain.ParserDescriptor{ID: "fake", Version: "fake-1", ConfigHash: strings.Repeat("a", 64), SchemaVersion: "parse-v1"}
}
func (p *fakeApplicationParser) Parse(_ context.Context, input domain.SourceInput) (domain.ParsedDocument, error) {
	p.calls++
	if p.parseErr != nil {
		return domain.ParsedDocument{}, p.parseErr
	}
	parsed := domain.ParsedDocument{
		ParserID: "fake", ParserVersion: "fake-1", ParserConfigHash: strings.Repeat("a", 64), SchemaVersion: "parse-v1",
		NormalizedContentHash: strings.Repeat("b", 64),
		Blocks:                []domain.ParsedBlock{{SpanType: "heading", StartLine: 1, EndLine: 1, StartByte: 0, EndByte: int64(len(input.ImmutableBytes)), Content: string(input.ImmutableBytes)}},
	}
	if p.emitBOMWarning {
		parsed.Warnings = []domain.Warning{{Code: "UTF8_BOM_PRESENT", Message: "parser detected UTF-8 BOM", StartByte: 0, EndByte: 3}}
	}
	return parsed, nil
}

type sequenceIngestionIDs struct {
	ids   []foundation.ID
	index int
}

func (s *sequenceIngestionIDs) New() (foundation.ID, error) {
	if s.index >= len(s.ids) {
		return "", errors.New("no more IDs")
	}
	id := s.ids[s.index]
	s.index++
	return id, nil
}

type fakeIngestionRepository struct {
	attempt     domain.AttemptRecord
	existing    bool
	projection  domain.ProjectionResult
	transitions []domain.AttemptTransition
	saved       domain.ProjectionWrite
	saveErr     error
}

func (f *fakeIngestionRepository) CreateAttempt(_ context.Context, attempt domain.AttemptRecord) (domain.AttemptResult, error) {
	if f.existing {
		return domain.AttemptResult{Attempt: f.attempt, Created: false}, nil
	}
	f.attempt = attempt
	return domain.AttemptResult{Attempt: attempt, Created: true}, nil
}
func (f *fakeIngestionRepository) GetAttempt(context.Context, foundation.ID) (domain.AttemptRecord, error) {
	return f.attempt, nil
}
func (f *fakeIngestionRepository) TransitionAttempt(_ context.Context, transition domain.AttemptTransition) (domain.AttemptRecord, error) {
	f.transitions = append(f.transitions, transition)
	f.attempt.Status = transition.Status
	f.attempt.SecurityStatus = transition.SecurityStatus
	f.attempt.ParseProjectionID = transition.ParseProjectionID
	f.attempt.FailureStage = transition.FailureStage
	f.attempt.ErrorCode = transition.ErrorCode
	f.attempt.Retryable = transition.Retryable
	f.attempt.Warnings = append([]domain.Warning(nil), transition.Warnings...)
	f.attempt.CompletedAt = transition.CompletedAt
	f.attempt.Version++
	return f.attempt, nil
}
func (f *fakeIngestionRepository) GetProjection(_ context.Context, _ foundation.ID, _, _ string) (domain.ProjectionResult, error) {
	if f.projection.Projection.ID != "" {
		return f.projection, nil
	}
	return domain.ProjectionResult{Projection: f.saved.Projection, Spans: f.saved.Spans, Chunks: f.saved.Chunks, Created: false}, nil
}
func (f *fakeIngestionRepository) SaveProjection(_ context.Context, write domain.ProjectionWrite) (domain.ProjectionResult, error) {
	if f.saveErr != nil {
		return domain.ProjectionResult{}, f.saveErr
	}
	f.saved = write
	return domain.ProjectionResult{Projection: write.Projection, Spans: write.Spans, Chunks: write.Chunks, Created: true}, nil
}
