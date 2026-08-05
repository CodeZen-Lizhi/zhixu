package workflow

import (
	"path"
	"strings"
	"unicode"
	"unicode/utf8"

	changecontroldomain "github.com/CodeZen-Lizhi/zhixu/internal/changecontrol/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	organizingdomain "github.com/CodeZen-Lizhi/zhixu/internal/organizing/domain"
)

const maxOutputSlugBytes = 96

func defaultOutputPath(snapshot organizingdomain.Snapshot, revision organizingdomain.TemplateRevision) (string, error) {
	if snapshot.Validate() != nil || revision.ID != snapshot.TemplateRevisionID || revision.TemplateID != snapshot.TemplateID ||
		revision.DeclarationHash != snapshot.TemplateHash {
		return "", workflowError(foundation.ErrorConsistencyViolation, "ORGANIZING_OUTPUT_DEFAULT_INVALID", false, "organizing output default is not bound to the snapshot")
	}
	filename := strings.ReplaceAll(revision.Declaration.Output.FilenamePattern, "{slug}", outputSlug(snapshot.Intent))
	filename = strings.ReplaceAll(filename, "{date}", snapshot.CreatedAt.UTC().Format("2006-01-02"))
	target := path.Join(revision.Declaration.Output.Directory, filename)
	clean, err := changecontroldomain.ValidateTargetPath(target)
	if err != nil || clean != target || changecontroldomain.ValidateWorkspaceTarget(snapshot.WorkspaceID, target) != nil {
		return "", workflowError(foundation.ErrorInvalidInput, "ORGANIZING_OUTPUT_DEFAULT_INVALID", false, "organizing template produced an invalid Workspace Markdown path")
	}
	return target, nil
}

func validDefaultOutputPath(value string) bool {
	clean, err := changecontroldomain.ValidateTargetPath(value)
	return err == nil && clean == value && changecontroldomain.ValidateWorkspaceTarget(foundation.ID("organizing"), value) == nil
}

func outputSlug(value string) string {
	var result strings.Builder
	pendingSeparator := false
	for _, current := range strings.ToLower(strings.TrimSpace(value)) {
		if !unicode.IsLetter(current) && !unicode.IsDigit(current) {
			pendingSeparator = result.Len() > 0
			continue
		}
		separatorBytes := 0
		if pendingSeparator {
			separatorBytes = 1
		}
		if result.Len()+separatorBytes+utf8.RuneLen(current) > maxOutputSlugBytes {
			break
		}
		if pendingSeparator {
			result.WriteByte('-')
			pendingSeparator = false
		}
		result.WriteRune(current)
	}
	if result.Len() == 0 {
		return "organized-note"
	}
	return result.String()
}
