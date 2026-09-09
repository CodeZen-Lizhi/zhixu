package postgres

import "github.com/CodeZen-Lizhi/zhixu/internal/review/interview/domain"

func encodeNoteSource(source *domain.NoteQuestionSource) (any, error) {
	if source == nil {
		return nil, nil
	}
	encoded, err := encodeJSON(source)
	if err != nil {
		return nil, err
	}
	return interviewJSONB(encoded), nil
}

func nullableNoteString(value string) any {
	if value == "" {
		return nil
	}
	return value
}

func persistedSourceKind(value domain.QuestionSourceKind) string {
	if value == "" {
		return string(domain.QuestionSourceClaim)
	}
	return string(value)
}
