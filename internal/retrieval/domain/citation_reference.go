package domain

import "github.com/CodeZen-Lizhi/zhixu/internal/foundation"

// CitationReferenceQuery 绑定 frozen Index、Chunk、Source Version 与 Source Span 完整身份。
type CitationReferenceQuery struct {
	WorkspaceID     foundation.ID
	IndexVersionID  foundation.ID
	ChunkID         foundation.ID
	SourceVersionID foundation.ID
	SourceSpanID    foundation.ID
}

// ValidateCitationReferenceQuery 校验 Citation tuple 完整且不复用身份。
func ValidateCitationReferenceQuery(query CitationReferenceQuery) error {
	values := []foundation.ID{query.WorkspaceID, query.IndexVersionID, query.ChunkID, query.SourceVersionID, query.SourceSpanID}
	seen := make(map[foundation.ID]struct{}, len(values))
	for _, value := range values {
		parsed, err := foundation.ParseID(string(value))
		if err != nil || parsed != value {
			return invalid(ErrorCodeEvidenceReferenceInvalid, "citation reference identity is invalid")
		}
		if _, duplicate := seen[value]; duplicate {
			return invalid(ErrorCodeEvidenceReferenceInvalid, "citation reference identity is reused")
		}
		seen[value] = struct{}{}
	}
	if len(seen) != len(values) {
		return invalid(ErrorCodeEvidenceReferenceInvalid, "citation reference identity is incomplete")
	}
	return nil
}
