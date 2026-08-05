package workflow

import (
	"strings"
	"unicode"

	artifactapp "github.com/CodeZen-Lizhi/zhixu/internal/artifact/application"
	artifactdomain "github.com/CodeZen-Lizhi/zhixu/internal/artifact/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	organizingdomain "github.com/CodeZen-Lizhi/zhixu/internal/organizing/domain"
)

const (
	mergeCategoryDuplicate     = "DUPLICATE"
	mergeCategoryComplementary = "COMPLEMENTARY"
	mergeCategoryConflict      = "CONFLICT"
	mergeCategoryUnique        = "UNIQUE"
	mergePreviewLimit          = 64

	semanticGapCode   = "NO_TEMPLATE_SEMANTIC_EVIDENCE"
	semanticGapDetail = "冻结证据中没有可证明本章节语义的内容。"
)

type mergeCategoryCount struct {
	Category string `json:"category"`
	Count    int    `json:"count"`
}

type mergeComparisonEntry struct {
	Category          string                        `json:"category"`
	Kind              organizingdomain.MaterialKind `json:"kind"`
	SourceVersionID   foundation.ID                 `json:"source_version_id,omitempty"`
	SourceSpanID      foundation.ID                 `json:"source_span_id,omitempty"`
	DocumentID        foundation.ID                 `json:"document_id,omitempty"`
	ArticleRevisionID foundation.ID                 `json:"article_revision_id,omitempty"`
	RevisionNo        int64                         `json:"revision_no,omitempty"`
	ContentHash       string                        `json:"content_hash"`
	ExcerptHash       string                        `json:"excerpt_hash,omitempty"`
}

// mergeComparison is a bounded review projection. The Artifact still carries
// every rendered citation; this projection exposes a source-addressable preview
// and complete category counts to the human confirmation node.
type mergeComparison struct {
	EvidenceCount int                    `json:"evidence_count"`
	DocumentCount int                    `json:"document_count"`
	Categories    []mergeCategoryCount   `json:"categories"`
	Preview       []mergeComparisonEntry `json:"comparison"`
	byCategory    map[string][]verifiedEvidence
}

func renderTemplateSections(revision organizingdomain.TemplateRevision, evidence []verifiedEvidence, capacityLimited bool) []artifactapp.SectionInput {
	switch revision.Declaration.Kind {
	case organizingdomain.TemplateMergeDocuments:
		comparison := classifyMergeEvidence(evidence)
		return renderMergeSections(revision, evidence, comparison, capacityLimited)
	case organizingdomain.TemplateTopicArticle:
		return renderTopicArticleSections(revision, evidence, capacityLimited)
	case organizingdomain.TemplateKnowledgeReport:
		return renderKnowledgeReportSections(revision, evidence, capacityLimited)
	case organizingdomain.TemplateInterviewReview:
		return renderInterviewReviewSections(revision, evidence, capacityLimited)
	default:
		return renderGenericSections(revision, evidence, capacityLimited)
	}
}

func renderGenericSections(revision organizingdomain.TemplateRevision, evidence []verifiedEvidence, capacityLimited bool) []artifactapp.SectionInput {
	buckets := make([][]verifiedEvidence, len(revision.Declaration.Sections))
	for index, item := range evidence {
		bucket := index % len(buckets)
		buckets[bucket] = append(buckets[bucket], item)
	}
	sections := make([]artifactapp.SectionInput, len(revision.Declaration.Sections))
	for index, declaration := range revision.Declaration.Sections {
		sections[index] = evidenceSection(declaration, buckets[index], capacityLimited, evidenceUnavailableGap, evidenceUnavailableDetail)
	}
	return sections
}

func renderTopicArticleSections(revision organizingdomain.TemplateRevision, evidence []verifiedEvidence, capacityLimited bool) []artifactapp.SectionInput {
	comparison := classifyMergeEvidence(evidence)
	sections := make([]artifactapp.SectionInput, len(revision.Declaration.Sections))
	for index, declaration := range revision.Declaration.Sections {
		var selected []verifiedEvidence
		gapCode, gapDetail := semanticGapCode, semanticGapDetail
		switch declaration.Key {
		case "overview":
			selected = firstEvidence(evidence, 8)
		case "core-concepts", "details":
			selected = evidenceWithAnyMarker(evidence, conceptMarkers)
			if len(selected) == 0 {
				selected = firstEvidence(evidence, maxCitationsPerSection)
			}
		case "examples":
			selected = evidenceWithAnyMarker(evidence, exampleMarkers)
		case "conflicts":
			selected = comparison.byCategory[mergeCategoryConflict]
		case "gaps":
			selected = evidenceWithAnyMarker(evidence, gapMarkers)
		case "sources":
			selected = evidence
		default:
			selected = firstEvidence(evidence, maxCitationsPerSection)
			gapCode, gapDetail = evidenceUnavailableGap, evidenceUnavailableDetail
		}
		sections[index] = evidenceSection(declaration, selected, capacityLimited, gapCode, gapDetail)
	}
	return sections
}

func renderKnowledgeReportSections(revision organizingdomain.TemplateRevision, evidence []verifiedEvidence, capacityLimited bool) []artifactapp.SectionInput {
	comparison := classifyMergeEvidence(evidence)
	sections := make([]artifactapp.SectionInput, len(revision.Declaration.Sections))
	for index, declaration := range revision.Declaration.Sections {
		var selected []verifiedEvidence
		gapCode, gapDetail := semanticGapCode, semanticGapDetail
		switch declaration.Key {
		case "summary", "coverage", "findings", "sources":
			selected = evidence
		case "conflicts":
			selected = comparison.byCategory[mergeCategoryConflict]
		case "gaps":
			selected = evidenceWithAnyMarker(evidence, gapMarkers)
		default:
			selected = firstEvidence(evidence, maxCitationsPerSection)
			gapCode, gapDetail = evidenceUnavailableGap, evidenceUnavailableDetail
		}
		sections[index] = evidenceSection(declaration, selected, capacityLimited, gapCode, gapDetail)
	}
	return sections
}

func renderInterviewReviewSections(revision organizingdomain.TemplateRevision, evidence []verifiedEvidence, capacityLimited bool) []artifactapp.SectionInput {
	sections := make([]artifactapp.SectionInput, len(revision.Declaration.Sections))
	for index, declaration := range revision.Declaration.Sections {
		var selected []verifiedEvidence
		gapCode, gapDetail := semanticGapCode, semanticGapDetail
		switch declaration.Key {
		case "core-concepts":
			selected = evidenceWithAnyMarker(evidence, conceptMarkers)
			if len(selected) == 0 {
				selected = firstEvidence(evidence, maxCitationsPerSection)
			}
		case "questions":
			selected = evidenceWithAnyMarker(evidence, questionMarkers)
		case "follow-ups":
			selected = evidenceWithAnyMarker(evidence, followUpMarkers)
		case "code-examples":
			selected = evidenceWithAnyMarker(evidence, codeMarkers)
		case "gaps":
			selected = evidenceWithAnyMarker(evidence, gapMarkers)
		case "sources":
			selected = evidence
		default:
			selected = firstEvidence(evidence, maxCitationsPerSection)
			gapCode, gapDetail = evidenceUnavailableGap, evidenceUnavailableDetail
		}
		sections[index] = evidenceSection(declaration, selected, capacityLimited, gapCode, gapDetail)
	}
	return sections
}

func renderMergeSections(revision organizingdomain.TemplateRevision, evidence []verifiedEvidence, comparison mergeComparison, capacityLimited bool) []artifactapp.SectionInput {
	sections := make([]artifactapp.SectionInput, len(revision.Declaration.Sections))
	for index, declaration := range revision.Declaration.Sections {
		var selected []verifiedEvidence
		gapCode, gapDetail := semanticGapCode, semanticGapDetail
		switch declaration.Key {
		case "summary", "conclusion", "sources":
			selected = evidence
		case "common":
			selected = comparison.byCategory[mergeCategoryDuplicate]
		case "complementary":
			selected = comparison.byCategory[mergeCategoryComplementary]
		case "conflicts":
			selected = comparison.byCategory[mergeCategoryConflict]
		case "unique":
			selected = comparison.byCategory[mergeCategoryUnique]
		default:
			selected = firstEvidence(evidence, maxCitationsPerSection)
			gapCode, gapDetail = evidenceUnavailableGap, evidenceUnavailableDetail
		}
		sectionCapacityLimited := capacityLimited
		if len(selected) > maxCitationsPerSection {
			sectionCapacityLimited = true
		}
		sections[index] = evidenceSection(declaration, selected, sectionCapacityLimited, gapCode, gapDetail)
	}
	return sections
}

func evidenceSection(declaration organizingdomain.TemplateSection, evidence []verifiedEvidence, capacityLimited bool, missingCode, missingDetail string) artifactapp.SectionInput {
	section := artifactapp.SectionInput{
		Key: declaration.Key, Title: declaration.Title,
		Coverage: artifactdomain.Coverage{SectionKey: declaration.Key, Gaps: []artifactdomain.Gap{}},
	}
	if len(evidence) == 0 {
		section.Citations = []artifactapp.CitationInput{}
		section.Coverage.Status = artifactdomain.CoverageGap
		section.Coverage.Gaps = []artifactdomain.Gap{{Code: missingCode, Description: missingDetail}}
		return section
	}
	if len(evidence) > maxCitationsPerSection {
		evidence = evidence[:maxCitationsPerSection]
		capacityLimited = true
	}
	section.Citations = make([]artifactapp.CitationInput, len(evidence))
	var content strings.Builder
	for index, item := range evidence {
		section.Citations[index] = item.input
		if index > 0 {
			content.WriteByte('\n')
		}
		content.WriteString("- ")
		content.WriteString(indentExcerpt(boundedUTF8(item.citation.Excerpt, maxRenderedExcerptBytes)))
	}
	section.Content = content.String()
	section.Coverage.Status = artifactdomain.CoverageCovered
	if capacityLimited {
		section.Coverage.Status = artifactdomain.CoveragePartial
		section.Coverage.Gaps = []artifactdomain.Gap{{Code: evidenceCapacityGap, Description: evidenceCapacityDetail}}
	}
	return section
}

func classifyMergeEvidence(evidence []verifiedEvidence) mergeComparison {
	categories := make([]string, len(evidence))
	for index := range categories {
		categories[index] = mergeCategoryUnique
	}
	for left := 0; left < len(evidence); left++ {
		for right := left + 1; right < len(evidence); right++ {
			if evidence[left].citation.SourceVersionID == evidence[right].citation.SourceVersionID {
				continue
			}
			leftText := canonicalEvidenceText(evidence[left].citation.Excerpt)
			rightText := canonicalEvidenceText(evidence[right].citation.Excerpt)
			if leftText == "" || rightText == "" {
				continue
			}
			if leftText == rightText {
				setMergeCategory(categories, left, mergeCategoryDuplicate)
				setMergeCategory(categories, right, mergeCategoryDuplicate)
				continue
			}
			if !sharesEvidenceTerms(leftText, rightText) {
				continue
			}
			if evidencePolarity(leftText) != evidencePolarity(rightText) {
				setMergeCategory(categories, left, mergeCategoryConflict)
				setMergeCategory(categories, right, mergeCategoryConflict)
				continue
			}
			setMergeCategory(categories, left, mergeCategoryComplementary)
			setMergeCategory(categories, right, mergeCategoryComplementary)
		}
	}
	comparison := mergeComparison{
		EvidenceCount: len(evidence),
		Categories: []mergeCategoryCount{
			{Category: mergeCategoryDuplicate}, {Category: mergeCategoryComplementary},
			{Category: mergeCategoryConflict}, {Category: mergeCategoryUnique},
		},
		Preview:    []mergeComparisonEntry{},
		byCategory: map[string][]verifiedEvidence{mergeCategoryDuplicate: {}, mergeCategoryComplementary: {}, mergeCategoryConflict: {}, mergeCategoryUnique: {}},
	}
	for index, category := range categories {
		comparison.byCategory[category] = append(comparison.byCategory[category], evidence[index])
		for countIndex := range comparison.Categories {
			if comparison.Categories[countIndex].Category == category {
				comparison.Categories[countIndex].Count++
			}
		}
		if len(comparison.Preview) < mergePreviewLimit {
			item := evidence[index]
			comparison.Preview = append(comparison.Preview, mergeComparisonEntry{
				Category: category, Kind: organizingdomain.MaterialSourceVersion, SourceVersionID: item.citation.SourceVersionID, SourceSpanID: item.citation.SourceSpanID,
				ContentHash: item.citation.VerifiedContentHash, ExcerptHash: hashText(item.citation.Excerpt),
			})
		}
	}
	return comparison
}

func mergeConflictCount(comparison mergeComparison) int {
	for _, category := range comparison.Categories {
		if category.Category == mergeCategoryConflict {
			return category.Count
		}
	}
	return 0
}

func validMergeComparisonReceipt(receipt mergeStageReceipt) bool {
	expected := []string{mergeCategoryDuplicate, mergeCategoryComplementary, mergeCategoryConflict, mergeCategoryUnique}
	if len(receipt.Categories) != len(expected) || len(receipt.Comparison) != min(receipt.EvidenceCount+receipt.DocumentCount, mergePreviewLimit) {
		return false
	}
	total, conflicts := 0, 0
	categoryCounts := make(map[string]int, len(expected))
	for index, category := range receipt.Categories {
		if category.Category != expected[index] || category.Count < 0 {
			return false
		}
		total += category.Count
		categoryCounts[category.Category] = category.Count
		if category.Category == mergeCategoryConflict {
			conflicts = category.Count
		}
	}
	if total != receipt.EvidenceCount+receipt.DocumentCount || conflicts != receipt.ConflictCount {
		return false
	}
	seen := make(map[string]struct{}, len(receipt.Comparison))
	previewCounts := make(map[string]int, len(expected))
	for _, item := range receipt.Comparison {
		if !validMergeCategory(item.Category) || !validMergeComparisonEntry(item) {
			return false
		}
		identity := mergeComparisonIdentity(item)
		if _, duplicate := seen[identity]; duplicate {
			return false
		}
		seen[identity] = struct{}{}
		previewCounts[item.Category]++
		if previewCounts[item.Category] > categoryCounts[item.Category] {
			return false
		}
	}
	return true
}

func validMergeComparisonEntry(item mergeComparisonEntry) bool {
	return validOutlineSupport(outlineSupport{
		Kind: item.Kind, SourceVersionID: item.SourceVersionID, SourceSpanID: item.SourceSpanID,
		DocumentID: item.DocumentID, ArticleRevisionID: item.ArticleRevisionID, RevisionNo: item.RevisionNo,
		ContentHash: item.ContentHash, ExcerptHash: item.ExcerptHash,
	})
}

func mergeComparisonIdentity(item mergeComparisonEntry) string {
	return outlineSupportIdentity(outlineSupport{
		Kind: item.Kind, SourceVersionID: item.SourceVersionID, SourceSpanID: item.SourceSpanID,
		DocumentID: item.DocumentID, ArticleRevisionID: item.ArticleRevisionID,
	})
}

func validMergeCategory(category string) bool {
	return category == mergeCategoryDuplicate || category == mergeCategoryComplementary ||
		category == mergeCategoryConflict || category == mergeCategoryUnique
}

func frozenOutlineSupports(snapshot organizingdomain.Snapshot) []outlineSupport {
	result := make([]outlineSupport, 0)
	seen := make(map[string]struct{})
	for _, material := range snapshot.Materials {
		if material.Kind == organizingdomain.MaterialDocumentRevision {
			identity := string(material.Kind) + "\x00" + string(material.DocumentID) + "\x00" + string(material.ArticleRevisionID)
			if _, duplicate := seen[identity]; !duplicate {
				seen[identity] = struct{}{}
				result = append(result, outlineSupport{Kind: material.Kind, DocumentID: material.DocumentID, ArticleRevisionID: material.ArticleRevisionID, RevisionNo: material.Version, ContentHash: material.ContentHash})
			}
		}
		for _, evidence := range material.Evidence {
			identity := string(organizingdomain.MaterialSourceVersion) + "\x00" + string(evidence.SourceVersionID) + "\x00" + string(evidence.SourceSpanID)
			if _, duplicate := seen[identity]; duplicate {
				continue
			}
			seen[identity] = struct{}{}
			result = append(result, outlineSupport{
				Kind:            organizingdomain.MaterialSourceVersion,
				SourceVersionID: evidence.SourceVersionID, SourceSpanID: evidence.SourceSpanID,
				ContentHash: evidence.ContentHash, ExcerptHash: evidence.ExcerptHash,
			})
		}
	}
	return result
}

func setMergeCategory(categories []string, index int, next string) {
	if mergeCategoryPriority(next) > mergeCategoryPriority(categories[index]) {
		categories[index] = next
	}
}

func mergeCategoryPriority(category string) int {
	switch category {
	case mergeCategoryConflict:
		return 3
	case mergeCategoryDuplicate:
		return 2
	case mergeCategoryComplementary:
		return 1
	default:
		return 0
	}
}

func canonicalEvidenceText(value string) string {
	var result strings.Builder
	for _, character := range strings.ToLower(value) {
		if unicode.IsLetter(character) || unicode.IsNumber(character) {
			result.WriteRune(character)
		} else {
			result.WriteByte(' ')
		}
	}
	return strings.Join(strings.Fields(result.String()), " ")
}

func sharesEvidenceTerms(left, right string) bool {
	leftTerms := evidenceTerms(left)
	rightTerms := evidenceTerms(right)
	for term := range leftTerms {
		if _, found := rightTerms[term]; found {
			return true
		}
	}
	return false
}

func evidenceTerms(value string) map[string]struct{} {
	terms := make(map[string]struct{})
	for _, word := range strings.Fields(value) {
		characters := []rune(word)
		if len(characters) >= 3 {
			terms[word] = struct{}{}
		}
		if containsHan(characters) {
			for index := 0; index+1 < len(characters); index++ {
				terms[string(characters[index:index+2])] = struct{}{}
			}
		}
	}
	return terms
}

func containsHan(characters []rune) bool {
	for _, character := range characters {
		if unicode.Is(unicode.Han, character) {
			return true
		}
	}
	return false
}

func evidencePolarity(value string) bool {
	for _, marker := range negativeMarkers {
		if strings.Contains(value, marker) {
			return true
		}
	}
	return false
}

func evidenceWithAnyMarker(evidence []verifiedEvidence, markers []string) []verifiedEvidence {
	result := make([]verifiedEvidence, 0, len(evidence))
	for _, item := range evidence {
		text := strings.ToLower(item.citation.Excerpt)
		for _, marker := range markers {
			if strings.Contains(text, marker) {
				result = append(result, item)
				break
			}
		}
	}
	return result
}

func firstEvidence(evidence []verifiedEvidence, limit int) []verifiedEvidence {
	if len(evidence) <= limit {
		return evidence
	}
	return evidence[:limit]
}

var (
	negativeMarkers = []string{"not", "never", "without", "disabled", "false", "禁止", "不得", "不能", "无法", "未", "无", "不"}
	conceptMarkers  = []string{"概念", "定义", "原理", "concept", "definition", "is", "are"}
	exampleMarkers  = []string{"示例", "例如", "example", "for example"}
	gapMarkers      = []string{"缺口", "缺少", "未知", "不确定", "待验证", "unknown", "uncertain", "todo"}
	questionMarkers = []string{"?", "？", "什么", "如何", "为什么", "是否", "which", "what", "how", "why", "when"}
	followUpMarkers = []string{"为什么", "如何", "边界", "权衡", "限制", "风险", "异常", "why", "how", "tradeoff", "limit", "risk", "edge"}
	codeMarkers     = []string{"```", "func ", "function ", "class ", "interface ", "package ", "import ", "代码", "code"}
)
