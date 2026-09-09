package domain

import (
	"fmt"
	"net/url"
	"strings"
	"unicode"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
)

// RenderSynthesisMarkdown renders plain-text model fields with fixed structure
// and owner-derived source links. It never evaluates model Markdown, HTML or
// URLs. Every unaffected item renders to the same bytes across later revisions.
func RenderSynthesisMarkdown(workspaceID, noteID foundation.ID, title string, items []SynthesisItem) (string, error) {
	if !validID(noteID) || !synthesisText(title, MaxSynthesisTitleBytes, false) {
		return "", invalid(ErrorCodeSynthesisRevisionInvalid, "synthesis render identity or title is invalid")
	}
	if err := ValidateSynthesisItems(workspaceID, items); err != nil {
		return "", err
	}
	var output strings.Builder
	output.WriteString("# " + escapeSynthesisText(title) + "\n")
	sections := []struct {
		kind  SynthesisItemKind
		title string
	}{
		{SynthesisFactItem, "事实与互补"},
		{SynthesisConflictItem, "冲突与适用条件"},
		{SynthesisGapItem, "缺口与补充"},
	}
	for _, section := range sections {
		headingWritten := false
		for _, item := range items {
			if item.Kind != section.kind {
				continue
			}
			if !headingWritten {
				output.WriteString("\n## " + section.title + "\n")
				headingWritten = true
			}
			output.WriteString("\n<!-- synthesis-item:" + string(item.ID) + " -->\n")
			switch item.Kind {
			case SynthesisFactItem:
				renderSynthesisStatement(&output, workspaceID, noteID, *item.Fact, "", "")
			case SynthesisConflictItem:
				output.WriteString("- " + escapeSynthesisText(item.Conflict.Subject) + "\n")
				for index, alternative := range item.Conflict.Alternatives {
					renderSynthesisStatement(&output, workspaceID, noteID, alternative, "  ", fmt.Sprintf("观点 %d：", index+1))
				}
			case SynthesisGapItem:
				label := "待确认："
				if item.Gap.Resolution != nil {
					label = "已补充（原问题）："
				}
				output.WriteString("- " + label + escapeSynthesisText(item.Gap.Question) + "\n")
				if item.Gap.Context != "" {
					output.WriteString("  - 上下文：" + escapeSynthesisText(item.Gap.Context) + "\n")
				}
				if len(item.Gap.Sources) > 0 {
					renderSynthesisSources(&output, workspaceID, noteID, item.Gap.Sources, "  ", "上下文来源")
				}
				if item.Gap.Resolution != nil {
					renderSynthesisStatement(&output, workspaceID, noteID, *item.Gap.Resolution, "  ", "补充结论：")
				}
			}
			output.WriteString("<!-- /synthesis-item:" + string(item.ID) + " -->\n")
			if output.Len() > MaxSynthesisMarkdownBytes {
				return "", invalid(ErrorCodeSynthesisRevisionInvalid, "synthesis rendered content exceeds limit")
			}
		}
	}
	return output.String(), nil
}

func renderSynthesisStatement(output *strings.Builder, workspaceID, noteID foundation.ID, statement SynthesisStatement, indent, prefix string) {
	output.WriteString(indent + "- " + prefix + escapeSynthesisText(statement.Text) + "\n")
	applicability := statement.Applicability
	if applicability == "" {
		applicability = "来源未说明"
	}
	output.WriteString(indent + "  - 适用条件：" + escapeSynthesisText(applicability) + "\n")
	renderSynthesisSources(output, workspaceID, noteID, statement.Sources, indent+"  ", "原始依据")
}

func renderSynthesisSources(output *strings.Builder, workspaceID, noteID foundation.ID, sources []SynthesisSourceRef, indent, label string) {
	output.WriteString(indent + "- " + label + "：")
	for index, reference := range sources {
		if index > 0 {
			output.WriteString("；")
		}
		query := url.Values{
			"workspace_id":        {string(workspaceID)},
			"source_id":           {string(reference.Source.SourceID)},
			"source_version_id":   {string(reference.Source.SourceVersionID)},
			"content_artifact_id": {string(reference.Source.ContentArtifactID)},
			"parse_projection_id": {string(reference.Source.ParseProjectionID)},
			"source_span_id":      {string(reference.SourceSpanID)},
			"content_hash":        {reference.Source.ContentHash},
			"excerpt_hash":        {reference.ExcerptHash},
		}
		output.WriteString("[" + escapeSynthesisText(reference.Title) + "](/authoring/notes/" + string(noteID) + "?" + query.Encode() + ")")
	}
	output.WriteByte('\n')
}

func escapeSynthesisText(value string) string {
	var escaped strings.Builder
	for _, character := range value {
		switch character {
		case '&':
			escaped.WriteString("&amp;")
		case '<':
			escaped.WriteString("&lt;")
		case '>':
			escaped.WriteString("&gt;")
		default:
			// Escaping ASCII punctuation also prevents autolinks and Markdown
			// extension syntax from turning plain model text into navigation.
			if character < 128 && (unicode.IsPunct(character) || unicode.IsSymbol(character)) {
				escaped.WriteByte('\\')
			}
			escaped.WriteRune(character)
		}
	}
	return escaped.String()
}
