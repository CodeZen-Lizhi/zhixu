package postgres

import (
	"database/sql/driver"
	"fmt"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/ingestion/domain"
)

// projectionBatchSize bounds the number of rows in each immutable child write.
// The caller always places all batches inside one explicit GORM transaction.
const projectionBatchSize = 500

// jsonbValue is an explicit JSONB carrier. Returning a string makes the
// PostgreSQL dialect infer jsonb from the target column instead of treating a
// raw []byte as bytea.
type jsonbValue string

func (value jsonbValue) Value() (driver.Value, error) {
	if value == "" {
		return "{}", nil
	}
	return string(value), nil
}

func (value *jsonbValue) Scan(src any) error {
	switch typed := src.(type) {
	case nil:
		*value = ""
	case []byte:
		*value = jsonbValue(typed)
	case string:
		*value = jsonbValue(typed)
	default:
		return fmt.Errorf("unsupported JSONB value %T", src)
	}
	return nil
}

type provenanceGORMRecord struct {
	SourceVersionID   string    `gorm:"column:source_version_id;type:uuid;primaryKey"`
	ParseProjectionID string    `gorm:"column:parse_projection_id;type:uuid;primaryKey"`
	WorkspaceID       string    `gorm:"column:workspace_id;type:uuid"`
	SourceRevisionID  *string   `gorm:"column:source_revision_id;type:uuid"`
	CreatedAt         time.Time `gorm:"column:created_at;autoCreateTime:false"`
}

func (provenanceGORMRecord) TableName() string { return "ingestion.source_version_projection" }

type spanGORMRecord struct {
	ID                string     `gorm:"column:id;type:uuid;primaryKey"`
	WorkspaceID       string     `gorm:"column:workspace_id;type:uuid"`
	ContentArtifactID string     `gorm:"column:content_artifact_id;type:uuid"`
	ParseProjectionID string     `gorm:"column:parse_projection_id;type:uuid"`
	SpanType          string     `gorm:"column:span_type"`
	StartLine         int32      `gorm:"column:start_line"`
	EndLine           int32      `gorm:"column:end_line"`
	StartByte         int64      `gorm:"column:start_byte"`
	EndByte           int64      `gorm:"column:end_byte"`
	Selector          jsonbValue `gorm:"column:selector;type:jsonb"`
	ExcerptHash       string     `gorm:"column:excerpt_hash"`
	EvidenceKind      string     `gorm:"column:evidence_kind"`
	DerivedExcerpt    string     `gorm:"column:derived_excerpt"`
	ParserVersion     string     `gorm:"column:parser_version"`
	SchemaVersion     string     `gorm:"column:schema_version"`
	CreatedAt         time.Time  `gorm:"column:created_at;autoCreateTime:false"`
}

func (spanGORMRecord) TableName() string { return "ingestion.source_span" }

type chunkGORMRecord struct {
	ID                   string     `gorm:"column:id;type:uuid;primaryKey"`
	WorkspaceID          string     `gorm:"column:workspace_id;type:uuid"`
	ParseProjectionID    string     `gorm:"column:parse_projection_id;type:uuid"`
	Sequence             int32      `gorm:"column:sequence"`
	HeadingPath          jsonbValue `gorm:"column:heading_path;type:jsonb"`
	Content              string     `gorm:"column:content"`
	ContentHash          string     `gorm:"column:content_hash"`
	SourceSpanID         string     `gorm:"column:source_span_id;type:uuid"`
	ByteCount            int64      `gorm:"column:byte_count"`
	RuneCount            int64      `gorm:"column:rune_count"`
	ParserVersion        string     `gorm:"column:parser_version"`
	ChunkStrategyVersion string     `gorm:"column:chunk_strategy_version"`
	SchemaVersion        string     `gorm:"column:schema_version"`
	AtomicOversized      bool       `gorm:"column:atomic_oversized"`
	Status               string     `gorm:"column:status"`
	CreatedAt            time.Time  `gorm:"column:created_at;autoCreateTime:false"`
}

func (chunkGORMRecord) TableName() string { return "ingestion.canonical_chunk" }

func jsonbWarnings(value []domain.Warning) (jsonbValue, error) {
	encoded, err := marshalWarnings(value)
	return jsonbValue(encoded), err
}

func jsonbSelector(value map[string]string) (jsonbValue, error) {
	encoded, err := marshalObject(value)
	return jsonbValue(encoded), err
}

func jsonbHeading(value []string) (jsonbValue, error) {
	encoded, err := marshalStrings(value)
	return jsonbValue(encoded), err
}
