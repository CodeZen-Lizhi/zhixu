package application

import "github.com/CodeZen-Lizhi/zhixu/internal/review/interview/domain"

// ErrorCodeAPIVersionUnsupported identifies a source that the caller's contract cannot represent.
const ErrorCodeAPIVersionUnsupported = "INTERVIEW_API_VERSION_UNSUPPORTED"

// RequireClaimSessionSource checks the immutable source selection, including sessions without turns.
func RequireClaimSessionSource(session domain.Session) error {
	if session.Config.Scope.NoteRevision != nil {
		return unsupportedSourceError()
	}
	return nil
}

// RequireClaimSources rejects every NOTE projection before it reaches a Claim-only consumer.
// It checks source membership; normal domain and identity validation remains the caller's responsibility.
func RequireClaimSources(snapshot Snapshot) error {
	if err := RequireClaimSessionSource(snapshot.Session); err != nil {
		return err
	}
	for _, question := range snapshot.Questions {
		if question.SourceKind == domain.QuestionSourceNoteRevision || question.NoteSource != nil {
			return unsupportedSourceError()
		}
	}
	for _, turn := range snapshot.Turns {
		if turn.Score.NoteSource != nil {
			return unsupportedSourceError()
		}
	}
	if snapshot.Report != nil {
		if len(snapshot.Report.NoteSources) != 0 {
			return unsupportedSourceError()
		}
		for _, findings := range [][]domain.Finding{snapshot.Report.Strengths, snapshot.Report.Gaps, snapshot.Report.Expression} {
			for _, finding := range findings {
				if finding.SourceKind == domain.QuestionSourceNoteRevision || finding.NoteSource != nil {
					return unsupportedSourceError()
				}
			}
		}
	}
	return RequireClaimPathSources(PathSnapshot{Steps: snapshot.Steps})
}

// RequireClaimPathSources checks the frozen provenance, independently of mutable step progress.
func RequireClaimPathSources(snapshot PathSnapshot) error {
	for _, step := range snapshot.Steps {
		if step.SourceKind == domain.QuestionSourceNoteRevision || step.NoteSource != nil {
			return unsupportedSourceError()
		}
	}
	return nil
}

func unsupportedSourceError() error {
	return domain.ConflictError(ErrorCodeAPIVersionUnsupported, "the requested source requires the newer interview contract")
}
