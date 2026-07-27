package application

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/CodeZen-Lizhi/zhixu/internal/export/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation/redaction"
)

const maxRenderedExportBytes = 32 * 1024 * 1024

type exportEnvelope struct {
	SchemaVersion     string                 `json:"schema_version"`
	WorkspaceID       string                 `json:"workspace_id"`
	CollectionID      *string                `json:"collection_id"`
	CollectionVersion *int64                 `json:"collection_version"`
	QueryHash         string                 `json:"query_hash"`
	ReadModelRevision string                 `json:"read_model_revision"`
	Kind              domain.Kind            `json:"kind"`
	RedactionPolicy   domain.RedactionPolicy `json:"redaction_policy"`
	Fields            []domain.Field         `json:"fields"`
	ExactCount        int64                  `json:"exact_count"`
	ExportedCount     int                    `json:"exported_count"`
	Items             []map[string]any       `json:"items"`
}

// Render 将稳定 Collection 快照编码为 Markdown 或 JSON；任何 Secret 都会强制脱敏。
func Render(snapshot CollectionSnapshot, job domain.Job) ([]byte, string, error) {
	if snapshot.WorkspaceID != job.WorkspaceID || snapshot.ExactCount < int64(len(snapshot.Items)) || len(snapshot.Items) > MaxSnapshotItems {
		return nil, "", resultInvalid(errors.New("export snapshot is invalid"))
	}
	items := make([]map[string]any, 0, len(snapshot.Items))
	for _, item := range snapshot.Items {
		projected, err := projectItem(item, job.Fields, job.Redaction, job.IncludeSensitive)
		if err != nil {
			return nil, "", err
		}
		items = append(items, projected)
	}
	collectionID := (*string)(nil)
	if snapshot.CollectionID != nil {
		value := string(*snapshot.CollectionID)
		collectionID = &value
	}
	envelope := exportEnvelope{SchemaVersion: job.SchemaVersion, WorkspaceID: string(job.WorkspaceID), CollectionID: collectionID, CollectionVersion: snapshot.CollectionVersion, QueryHash: snapshot.QueryHash, ReadModelRevision: snapshot.ReadModelRevision, Kind: job.Kind, RedactionPolicy: job.Redaction, Fields: append([]domain.Field(nil), job.Fields...), ExactCount: snapshot.ExactCount, ExportedCount: len(items), Items: items}
	var payload []byte
	var extension string
	var err error
	if job.Kind == domain.KindMarkdown {
		payload, err = renderMarkdown(snapshot, envelope)
		extension = "md"
	} else {
		payload, err = json.MarshalIndent(envelope, "", "  ")
		if err == nil {
			payload = append(payload, '\n')
		}
		extension = "json"
	}
	if err != nil {
		return nil, "", resultInvalid(err)
	}
	if len(payload) > maxRenderedExportBytes {
		return nil, "", invalid(errors.New("rendered export exceeds size limit"))
	}
	if !utf8.Valid(payload) {
		return nil, "", resultInvalid(errors.New("rendered export is not UTF-8"))
	}
	return payload, extension, nil
}

func renderMarkdown(snapshot CollectionSnapshot, envelope exportEnvelope) ([]byte, error) {
	var buffer bytes.Buffer
	fmt.Fprintf(&buffer, "# %s\n\n", markdownText(snapshot.Name, domain.RedactionMasked, false))
	fmt.Fprintf(&buffer, "- schema_version: `%s`\n", envelope.SchemaVersion)
	fmt.Fprintf(&buffer, "- workspace_id: `%s`\n", envelope.WorkspaceID)
	if envelope.CollectionID != nil {
		fmt.Fprintf(&buffer, "- collection_id: `%s`\n", *envelope.CollectionID)
	}
	if envelope.CollectionVersion != nil {
		fmt.Fprintf(&buffer, "- collection_version: `%d`\n", *envelope.CollectionVersion)
	}
	fmt.Fprintf(&buffer, "- query_hash: `%s`\n- read_model_revision: `%s`\n- redaction_policy: `%s`\n- exact_count: `%d`\n- exported_count: `%d`\n\n", envelope.QueryHash, envelope.ReadModelRevision, envelope.RedactionPolicy, envelope.ExactCount, envelope.ExportedCount)
	for index, item := range envelope.Items {
		title, _ := item[string(domain.FieldTitle)].(string)
		if strings.TrimSpace(title) == "" {
			title = fmt.Sprintf("Item %d", index+1)
		}
		fmt.Fprintf(&buffer, "## %s\n\n", markdownText(title, domain.RedactionMasked, false))
		keys := make([]string, 0, len(item))
		for key := range item {
			if key != string(domain.FieldTitle) {
				keys = append(keys, key)
			}
		}
		sort.Strings(keys)
		for _, key := range keys {
			encoded, err := json.Marshal(item[key])
			if err != nil {
				return nil, err
			}
			fmt.Fprintf(&buffer, "- **%s**: %s\n", markdownText(key, domain.RedactionMasked, false), markdownText(string(encoded), domain.RedactionMasked, false))
		}
		buffer.WriteByte('\n')
	}
	return buffer.Bytes(), nil
}

func projectItem(item Item, fields []domain.Field, policy domain.RedactionPolicy, includeSensitive bool) (map[string]any, error) {
	if item.ID == "" || strings.TrimSpace(item.ObjectType) == "" || item.CreatedAt.IsZero() || item.UpdatedAt.IsZero() {
		return nil, resultInvalid(errors.New("export item is invalid"))
	}
	result := make(map[string]any, len(fields))
	for _, field := range fields {
		switch field {
		case domain.FieldObjectType:
			result[string(field)] = item.ObjectType
		case domain.FieldID:
			result[string(field)] = string(item.ID)
		case domain.FieldTitle:
			result[string(field)] = safeText(item.Title, policy, includeSensitive)
		case domain.FieldSummary:
			result[string(field)] = safeText(item.Summary, policy, includeSensitive)
		case domain.FieldStatus:
			result[string(field)] = item.Status
		case domain.FieldTopic:
			if item.TopicID == nil {
				result[string(field)] = nil
			} else {
				result[string(field)] = string(*item.TopicID)
			}
		case domain.FieldSource:
			sources := make([]map[string]any, 0, len(item.Sources))
			for _, source := range item.Sources {
				path := source.Path
				if redaction.ContainsAbsolutePath(path) {
					path = "<redacted-path>"
				}
				sources = append(sources, map[string]any{"source_type": safeText(source.Type, policy, includeSensitive), "file_path": safeText(path, policy, includeSensitive), "support_type": source.Support, "created_at": source.CreatedAt.UTC().Format(time.RFC3339Nano)})
			}
			result[string(field)] = sources
		case domain.FieldRelations:
			result[string(field)] = append([]string(nil), item.Relations...)
		case domain.FieldHealth:
			if item.Health == nil {
				result[string(field)] = nil
			} else {
				result[string(field)] = map[string]any{"count": item.Health.Count, "max_severity": item.Health.MaxSeverity, "issue_types": append([]string(nil), item.Health.IssueTypes...), "summary": safeText(item.Health.Summary, policy, includeSensitive)}
			}
		case domain.FieldConfidence:
			result[string(field)] = item.Confidence
		case domain.FieldCreatedAt:
			result[string(field)] = item.CreatedAt.UTC().Format(time.RFC3339Nano)
		case domain.FieldUpdatedAt:
			result[string(field)] = item.UpdatedAt.UTC().Format(time.RFC3339Nano)
		case domain.FieldApplicability:
			if len(item.Applicability) == 0 {
				result[string(field)] = nil
			} else {
				var decoded any
				if err := json.Unmarshal(item.Applicability, &decoded); err != nil {
					return nil, resultInvalid(err)
				}
				sanitized, err := sanitizeJSONValue(decoded, policy, includeSensitive)
				if err != nil {
					return nil, err
				}
				result[string(field)] = map[string]any{"schema_version": item.ApplicabilitySchemaVersion, "hash": item.ApplicabilityHash, "value": sanitized}
			}
		default:
			return nil, invalid(errors.New("unknown export field"))
		}
	}
	return result, nil
}

func sanitizeJSONValue(value any, policy domain.RedactionPolicy, includeSensitive bool) (any, error) {
	switch typed := value.(type) {
	case nil, bool, float64:
		return typed, nil
	case string:
		return safeText(typed, policy, includeSensitive), nil
	case []any:
		result := make([]any, len(typed))
		for index, item := range typed {
			sanitized, err := sanitizeJSONValue(item, policy, includeSensitive)
			if err != nil {
				return nil, err
			}
			result[index] = sanitized
		}
		return result, nil
	case map[string]any:
		result := make(map[string]any, len(typed))
		for key, item := range typed {
			sanitizedKey := safeText(key, policy, includeSensitive)
			if _, exists := result[sanitizedKey]; exists {
				return nil, resultInvalid(errors.New("export redaction produced duplicate JSON keys"))
			}
			sanitized, err := sanitizeJSONValue(item, policy, includeSensitive)
			if err != nil {
				return nil, err
			}
			result[sanitizedKey] = sanitized
		}
		return result, nil
	default:
		return nil, resultInvalid(fmt.Errorf("unsupported export JSON value %T", value))
	}
}

func safeText(value string, _ domain.RedactionPolicy, _ bool) string {
	value = redaction.RedactSecrets(value)
	if redaction.ContainsAbsolutePath(value) {
		return "<redacted-path>"
	}
	return neutralizeSpreadsheetFormula(value)
}

func neutralizeSpreadsheetFormula(value string) string {
	trimmed := strings.TrimLeft(value, " \t\r\n")
	if trimmed == "" || !strings.ContainsRune("=+-@", rune(trimmed[0])) {
		return value
	}
	return "'" + value
}

func markdownText(value string, policy domain.RedactionPolicy, includeSensitive bool) string {
	value = safeText(value, policy, includeSensitive)
	value = strings.ReplaceAll(value, "\r", " ")
	value = strings.ReplaceAll(value, "\n", " ")
	return strings.ReplaceAll(value, "`", "\\`")
}
