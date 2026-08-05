package domain

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
)

const (
	maxArtifactTypeBytes  = 128
	maxArtifactTitleBytes = 512
	maxScopeBytes         = 16 * 1024
	maxSectionKeyBytes    = 128
	maxSectionTitleBytes  = 512
	maxSectionContent     = 256 * 1024
	maxCitationExcerpt    = 16 * 1024
	maxDocumentSources    = 64
	maxGapCodeBytes       = 128
	maxGapDescription     = 4 * 1024
)

// Status 是 Artifact 的受控生命周期状态。
type Status string

const (
	// StatusPlanning 表示 Artifact 仅完成规划，尚未提交大纲。
	StatusPlanning Status = "PLANNING"
	// StatusOutlineReview 表示大纲等待人工审批。
	StatusOutlineReview Status = "OUTLINE_REVIEW"
	// StatusGenerating 表示可以基于已验证内容生成或修订章节。
	StatusGenerating Status = "GENERATING"
	// StatusDraft 表示所有已记录章节形成待审批草稿。
	StatusDraft Status = "DRAFT"
	// StatusApproved 表示 Artifact 草稿已获人工批准。
	StatusApproved Status = "APPROVED"
	// StatusExported 表示已导出追踪副本，仍不是正式知识。
	StatusExported Status = "EXPORTED"
	// StatusPublishProposed 表示仅创建了正式知识写入 Proposal 请求。
	StatusPublishProposed Status = "PUBLISH_PROPOSED"
	// StatusPublished 表示对应 Publish Proposal 已写回正式知识。
	StatusPublished Status = "PUBLISHED"
	// StatusArchived 表示 Artifact 只读保留。
	StatusArchived Status = "ARCHIVED"
)

// CreatorType 标识不可变 Revision 的创建来源，不赋予写正式知识权限。
type CreatorType string

const (
	// CreatorHuman 表示 Revision 来自人工编辑或审批操作。
	CreatorHuman CreatorType = "HUMAN"
	// CreatorAgent 表示 Revision 由 Agent 生成，但仍只属于 Artifact 隔离区。
	CreatorAgent CreatorType = "AGENT"
)

// CoverageStatus 描述一个章节计划由已验证内容覆盖的程度。
type CoverageStatus string

const (
	// CoverageCovered 表示章节有完整的已验证引用且无知识缺口。
	CoverageCovered CoverageStatus = "COVERED"
	// CoveragePartial 表示章节有已验证引用，但保留了显式知识缺口。
	CoveragePartial CoverageStatus = "PARTIAL"
	// CoverageGap 表示缺少可用的已验证知识，章节正文必须为空。
	CoverageGap CoverageStatus = "GAP"
)

// OutlineSection 是审批与 Revision 一起冻结的章节计划。
type OutlineSection struct {
	Key   string
	Title string
}

// Citation 只引用已验证 Source Span，不能将 Agent 输出伪装成知识来源。
type Citation struct {
	SourceVersionID     foundation.ID
	SourceSpanID        foundation.ID
	VerifiedContentHash string
	Excerpt             string
	Verified            bool
}

// DocumentSource 引用 Workspace 内一份经过服务端复核的不可变文章 Revision。
// 它与正式知识 Citation 分开表达，不能伪装成 Source Span。
type DocumentSource struct {
	DocumentID          foundation.ID
	ArticleRevisionID   foundation.ID
	RevisionNo          int64
	VerifiedContentHash string
	Verified            bool
}

// Gap 明确记录无法由已验证知识支持的部分。
type Gap struct {
	Code        string
	Description string
}

// Coverage 是单个章节的来源覆盖结论。
type Coverage struct {
	SectionKey string
	Status     CoverageStatus
	Gaps       []Gap
}

// Section 是 Revision 中不可变的单章正文及其验证引用。
type Section struct {
	Key             string
	Title           string
	Content         string
	Citations       []Citation
	DocumentSources []DocumentSource `json:"DocumentSources,omitempty"`
	Coverage        Coverage
}

// GenerationMetadata 保存 Agent 生成的可追踪版本信息；人工 Revision 必须为空。
type GenerationMetadata struct {
	PromptVersion             string
	ModelVersion              string
	WorkflowDefinitionVersion string
	SchemaVersion             string
}

// Revision 是 Artifact 的不可变快照；任何大纲或正文变化都必须创建新的 Revision。
type Revision struct {
	ID          foundation.ID
	ArtifactID  foundation.ID
	RevisionNo  int64
	Outline     []OutlineSection
	Sections    []Section
	CreatedBy   CreatorType
	Metadata    *GenerationMetadata
	ContentHash string
	CreatedAt   time.Time
}

// Artifact 是 Workspace 隔离的学习或面试产物，不是正式 RAG 知识。
type Artifact struct {
	ID                foundation.ID
	WorkspaceID       foundation.ID
	Type              string
	Title             string
	Status            Status
	ScopeDefinition   string
	SourceCoverage    []Coverage
	CurrentRevisionID foundation.ID
	Version           int64
	CreatedAt         time.Time
	UpdatedAt         time.Time
}

// PlanInput 是创建 Artifact 及其首个空 Revision 所需的纯领域输入。
type PlanInput struct {
	ArtifactID        foundation.ID
	InitialRevisionID foundation.ID
	WorkspaceID       foundation.ID
	Type              string
	Title             string
	ScopeDefinition   string
	CreatedAt         time.Time
}

// PublicationRequest 只表达将已批准 Artifact 交给 Change Control 的领域请求，不创建正式 Document。
type PublicationRequest struct {
	ProposalType    string
	ArtifactID      foundation.ID
	WorkspaceID     foundation.ID
	RevisionID      foundation.ID
	RevisionNo      int64
	ArtifactVersion int64
	ContentHash     string
	// SourceCoverage freezes the verified coverage conclusion for every section.
	// Change Control persists this exact snapshot with the publication Proposal.
	SourceCoverage []Coverage
	RequestedAt    time.Time
}

const (
	// PublishArtifactProposalType 是 Artifact 请求正式知识写回时唯一允许的 Proposal 类型。
	PublishArtifactProposalType = "PUBLISH_ARTIFACT"
)

// PublicationConfirmation 仅记录外部 Publish Proposal 已成功写回的绑定事实。
type PublicationConfirmation struct {
	ProposalID       foundation.ID
	FormalDocumentID foundation.ID
	ConfirmedAt      time.Time
}

// PlanArtifact 创建 PLANNING Artifact 与 revision 1 的空不可变快照。
func PlanArtifact(input PlanInput) (Artifact, Revision, error) {
	if !validID(input.ArtifactID) || !validID(input.InitialRevisionID) || !validID(input.WorkspaceID) ||
		input.ArtifactID == input.InitialRevisionID || input.ArtifactID == input.WorkspaceID || input.InitialRevisionID == input.WorkspaceID ||
		!canonicalText(input.Type, maxArtifactTypeBytes) || !canonicalText(input.Title, maxArtifactTitleBytes) ||
		!canonicalText(input.ScopeDefinition, maxScopeBytes) || input.CreatedAt.IsZero() {
		return Artifact{}, Revision{}, invalid(ErrorCodeArtifactInvalid, "artifact plan input is invalid")
	}
	revision := Revision{ID: input.InitialRevisionID, ArtifactID: input.ArtifactID, RevisionNo: 1, Outline: []OutlineSection{}, Sections: []Section{}, CreatedBy: CreatorHuman, CreatedAt: input.CreatedAt.UTC()}
	hash, err := ComputeRevisionContentHash(revision)
	if err != nil {
		return Artifact{}, Revision{}, err
	}
	revision.ContentHash = hash
	artifact := Artifact{ID: input.ArtifactID, WorkspaceID: input.WorkspaceID, Type: input.Type, Title: input.Title, Status: StatusPlanning, ScopeDefinition: input.ScopeDefinition, SourceCoverage: []Coverage{}, CurrentRevisionID: revision.ID, Version: 1, CreatedAt: input.CreatedAt.UTC(), UpdatedAt: input.CreatedAt.UTC()}
	if err := ValidateArtifact(artifact); err != nil {
		return Artifact{}, Revision{}, err
	}
	return artifact, revision, nil
}

// ComputeRevisionContentHash 返回由可验证正文、引用和 Coverage 构成的稳定 SHA-256。
func ComputeRevisionContentHash(revision Revision) (string, error) {
	canonical, err := canonicalRevision(revision)
	if err != nil {
		return "", err
	}
	sections := make([]canonicalSection, len(canonical.Sections))
	hasDocumentSources := false
	for index, section := range canonical.Sections {
		sections[index] = canonicalSection{Key: section.Key, Title: section.Title, Content: section.Content, Citations: section.Citations, DocumentSources: section.DocumentSources, Coverage: section.Coverage}
		hasDocumentSources = hasDocumentSources || len(section.DocumentSources) > 0
	}
	if !hasDocumentSources {
		legacySections := make([]canonicalSectionV1, len(sections))
		for index, section := range sections {
			legacySections[index] = canonicalSectionV1{Key: section.Key, Title: section.Title, Content: section.Content, Citations: section.Citations, Coverage: section.Coverage}
		}
		payload := struct {
			Schema   string               `json:"schema"`
			Outline  []OutlineSection     `json:"outline"`
			Sections []canonicalSectionV1 `json:"sections"`
			Creator  CreatorType          `json:"creator"`
			Metadata *GenerationMetadata  `json:"metadata"`
		}{Schema: RevisionSchemaV1, Outline: canonical.Outline, Sections: legacySections, Creator: canonical.CreatedBy, Metadata: canonical.Metadata}
		encoded, err := json.Marshal(payload)
		if err != nil {
			return "", inconsistent(ErrorCodeArtifactInvalid, "revision hash payload cannot be encoded")
		}
		digest := sha256.Sum256(encoded)
		return hex.EncodeToString(digest[:]), nil
	}
	payload := struct {
		Schema   string              `json:"schema"`
		Outline  []OutlineSection    `json:"outline"`
		Sections []canonicalSection  `json:"sections"`
		Creator  CreatorType         `json:"creator"`
		Metadata *GenerationMetadata `json:"metadata"`
	}{Schema: RevisionSchemaV2, Outline: canonical.Outline, Sections: sections, Creator: canonical.CreatedBy, Metadata: canonical.Metadata}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return "", inconsistent(ErrorCodeArtifactInvalid, "revision hash payload cannot be encoded")
	}
	digest := sha256.Sum256(encoded)
	return hex.EncodeToString(digest[:]), nil
}

const (
	// RevisionSchemaV1 是只包含正式 Citation 的既有 Revision 哈希契约。
	RevisionSchemaV1 = "artifact-revision/v1"
	// RevisionSchemaV2 增加独立的不可变来源文档绑定。
	RevisionSchemaV2 = "artifact-revision/v2"
)

// RevisionSchemaVersion 返回持久化当前 Revision 所需的最小 Schema 版本。
func RevisionSchemaVersion(revision Revision) (string, error) {
	canonical, err := canonicalRevision(revision)
	if err != nil {
		return "", err
	}
	for _, section := range canonical.Sections {
		if len(section.DocumentSources) > 0 {
			return RevisionSchemaV2, nil
		}
	}
	return RevisionSchemaV1, nil
}

// ValidateRevision 验证 Revision 仍保持 canonical 内容和哈希绑定。
func ValidateRevision(revision Revision) error {
	if !validID(revision.ID) || !validID(revision.ArtifactID) || revision.ID == revision.ArtifactID || revision.RevisionNo < 1 || revision.CreatedAt.IsZero() || !canonicalSHA256(revision.ContentHash) {
		return invalid(ErrorCodeArtifactInvalid, "revision identity or lifecycle is invalid")
	}
	canonical, err := canonicalRevision(revision)
	if err != nil {
		return err
	}
	hash, err := ComputeRevisionContentHash(canonical)
	if err != nil {
		return err
	}
	if hash != revision.ContentHash {
		return inconsistent(ErrorCodeArtifactInvalid, "revision content hash is inconsistent")
	}
	return nil
}

// ValidateArtifact 验证 Artifact 身份、状态和当前 Revision 指针；它不把 Artifact 作为正式知识。
func ValidateArtifact(artifact Artifact) error {
	if !validID(artifact.ID) || !validID(artifact.WorkspaceID) || !validID(artifact.CurrentRevisionID) ||
		artifact.ID == artifact.WorkspaceID || artifact.ID == artifact.CurrentRevisionID || artifact.WorkspaceID == artifact.CurrentRevisionID ||
		!canonicalText(artifact.Type, maxArtifactTypeBytes) || !canonicalText(artifact.Title, maxArtifactTitleBytes) ||
		!canonicalText(artifact.ScopeDefinition, maxScopeBytes) || !validStatus(artifact.Status) || artifact.Version < 1 ||
		artifact.CreatedAt.IsZero() || artifact.UpdatedAt.Before(artifact.CreatedAt) {
		return invalid(ErrorCodeArtifactInvalid, "artifact identity or lifecycle is invalid")
	}
	if _, err := canonicalCoverage(artifact.SourceCoverage, nil, false); err != nil {
		return err
	}
	return nil
}

// CloneRevision 返回不与输入共享 slice 或指针的 Revision 快照。
func CloneRevision(revision Revision) Revision {
	copyValue := revision
	copyValue.Outline = make([]OutlineSection, len(revision.Outline))
	copy(copyValue.Outline, revision.Outline)
	copyValue.Sections = make([]Section, len(revision.Sections))
	for index, section := range revision.Sections {
		copyValue.Sections[index] = section
		copyValue.Sections[index].Citations = make([]Citation, len(section.Citations))
		copy(copyValue.Sections[index].Citations, section.Citations)
		copyValue.Sections[index].DocumentSources = make([]DocumentSource, len(section.DocumentSources))
		copy(copyValue.Sections[index].DocumentSources, section.DocumentSources)
		copyValue.Sections[index].Coverage.Gaps = make([]Gap, len(section.Coverage.Gaps))
		copy(copyValue.Sections[index].Coverage.Gaps, section.Coverage.Gaps)
	}
	if revision.Metadata != nil {
		metadata := *revision.Metadata
		copyValue.Metadata = &metadata
	}
	return copyValue
}

// CloneArtifact 返回不与输入共享 SourceCoverage 的 Artifact 快照。
func CloneArtifact(artifact Artifact) Artifact {
	copyValue := artifact
	copyValue.SourceCoverage = cloneCoverage(artifact.SourceCoverage)
	return copyValue
}

type canonicalSection struct {
	Key             string           `json:"key"`
	Title           string           `json:"title"`
	Content         string           `json:"content"`
	Citations       []Citation       `json:"citations"`
	DocumentSources []DocumentSource `json:"document_sources"`
	Coverage        Coverage         `json:"coverage"`
}

type canonicalSectionV1 struct {
	Key       string     `json:"key"`
	Title     string     `json:"title"`
	Content   string     `json:"content"`
	Citations []Citation `json:"citations"`
	Coverage  Coverage   `json:"coverage"`
}

func canonicalRevision(revision Revision) (Revision, error) {
	if revision.CreatedBy != CreatorHuman && revision.CreatedBy != CreatorAgent {
		return Revision{}, invalid(ErrorCodeArtifactInvalid, "revision creator is unsupported")
	}
	if revision.CreatedBy == CreatorHuman && revision.Metadata != nil {
		return Revision{}, invalid(ErrorCodeArtifactInvalid, "human revision cannot carry generation metadata")
	}
	if revision.CreatedBy == CreatorAgent && !validMetadata(revision.Metadata) {
		return Revision{}, invalid(ErrorCodeArtifactInvalid, "agent revision metadata is invalid")
	}
	outline, err := canonicalOutline(revision.Outline)
	if err != nil {
		return Revision{}, err
	}
	outlineTitles := make(map[string]string, len(outline))
	for _, section := range outline {
		outlineTitles[section.Key] = section.Title
	}
	sections := make([]canonicalSection, len(revision.Sections))
	seenSections := make(map[string]struct{}, len(revision.Sections))
	for index, section := range revision.Sections {
		if _, exists := outlineTitles[section.Key]; !exists || outlineTitles[section.Key] != section.Title || !canonicalOptionalText(section.Content, maxSectionContent) {
			return Revision{}, invalid(ErrorCodeArtifactInvalid, "revision section is invalid")
		}
		if _, duplicate := seenSections[section.Key]; duplicate {
			return Revision{}, invalid(ErrorCodeArtifactInvalid, "revision section key is duplicated")
		}
		seenSections[section.Key] = struct{}{}
		citations, err := canonicalCitations(section.Citations)
		if err != nil {
			return Revision{}, err
		}
		documentSources, err := canonicalDocumentSources(section.DocumentSources)
		if err != nil {
			return Revision{}, err
		}
		coverage, err := canonicalCoverage([]Coverage{section.Coverage}, map[string]struct{}{section.Key: {}}, true)
		if err != nil {
			return Revision{}, err
		}
		if err := validateSectionEvidence(section.Content, citations, documentSources, coverage[0]); err != nil {
			return Revision{}, err
		}
		sections[index] = canonicalSection{Key: section.Key, Title: section.Title, Content: section.Content, Citations: citations, DocumentSources: documentSources, Coverage: coverage[0]}
	}
	sort.Slice(sections, func(i, j int) bool { return sections[i].Key < sections[j].Key })
	copyValue := CloneRevision(revision)
	copyValue.Outline = outline
	copyValue.Sections = make([]Section, len(sections))
	for index, section := range sections {
		copyValue.Sections[index] = Section{Key: section.Key, Title: section.Title, Content: section.Content, Citations: cloneCitations(section.Citations), DocumentSources: cloneDocumentSources(section.DocumentSources), Coverage: section.Coverage}
	}
	return copyValue, nil
}

func canonicalOutline(outline []OutlineSection) ([]OutlineSection, error) {
	if outline == nil {
		return nil, invalid(ErrorCodeArtifactInvalid, "revision outline must be explicit")
	}
	canonical := make([]OutlineSection, len(outline))
	copy(canonical, outline)
	seen := make(map[string]struct{}, len(canonical))
	for _, section := range canonical {
		if !canonicalSectionKey(section.Key) || !canonicalText(section.Title, maxSectionTitleBytes) {
			return nil, invalid(ErrorCodeArtifactInvalid, "outline section is invalid")
		}
		if _, duplicate := seen[section.Key]; duplicate {
			return nil, invalid(ErrorCodeArtifactInvalid, "outline section key is duplicated")
		}
		seen[section.Key] = struct{}{}
	}
	sort.Slice(canonical, func(i, j int) bool { return canonical[i].Key < canonical[j].Key })
	return canonical, nil
}

func canonicalCitations(citations []Citation) ([]Citation, error) {
	if citations == nil {
		return nil, invalid(ErrorCodeArtifactCoverageInvalid, "section citations must be explicit")
	}
	canonical := make([]Citation, len(citations))
	copy(canonical, citations)
	seen := make(map[string]struct{}, len(canonical))
	for _, citation := range canonical {
		if !validID(citation.SourceVersionID) || !validID(citation.SourceSpanID) || citation.SourceVersionID == citation.SourceSpanID ||
			!citation.Verified || !canonicalSHA256(citation.VerifiedContentHash) || !canonicalText(citation.Excerpt, maxCitationExcerpt) {
			return nil, invalid(ErrorCodeArtifactCoverageInvalid, "citation must bind verified source content")
		}
		key := string(citation.SourceVersionID) + "\x00" + string(citation.SourceSpanID) + "\x00" + citation.VerifiedContentHash
		if _, duplicate := seen[key]; duplicate {
			return nil, invalid(ErrorCodeArtifactCoverageInvalid, "citation identity is duplicated")
		}
		seen[key] = struct{}{}
	}
	sort.Slice(canonical, func(i, j int) bool {
		left := string(canonical[i].SourceVersionID) + "\x00" + string(canonical[i].SourceSpanID) + "\x00" + canonical[i].VerifiedContentHash
		right := string(canonical[j].SourceVersionID) + "\x00" + string(canonical[j].SourceSpanID) + "\x00" + canonical[j].VerifiedContentHash
		return left < right
	})
	return canonical, nil
}

func canonicalDocumentSources(values []DocumentSource) ([]DocumentSource, error) {
	if values == nil {
		values = []DocumentSource{}
	}
	if len(values) > maxDocumentSources {
		return nil, invalid(ErrorCodeArtifactCoverageInvalid, "section document sources exceed the bounded limit")
	}
	canonical := cloneDocumentSources(values)
	seen := make(map[string]struct{}, len(canonical))
	for _, source := range canonical {
		if !validID(source.DocumentID) || !validID(source.ArticleRevisionID) || source.DocumentID == source.ArticleRevisionID ||
			source.RevisionNo < 1 || !source.Verified || !canonicalSHA256(source.VerifiedContentHash) {
			return nil, invalid(ErrorCodeArtifactCoverageInvalid, "document source must bind a verified article revision")
		}
		key := string(source.DocumentID) + "\x00" + string(source.ArticleRevisionID) + "\x00" + source.VerifiedContentHash
		if _, duplicate := seen[key]; duplicate {
			return nil, invalid(ErrorCodeArtifactCoverageInvalid, "document source identity is duplicated")
		}
		seen[key] = struct{}{}
	}
	sort.Slice(canonical, func(i, j int) bool {
		left := string(canonical[i].DocumentID) + "\x00" + string(canonical[i].ArticleRevisionID) + "\x00" + canonical[i].VerifiedContentHash
		right := string(canonical[j].DocumentID) + "\x00" + string(canonical[j].ArticleRevisionID) + "\x00" + canonical[j].VerifiedContentHash
		return left < right
	})
	return canonical, nil
}

func canonicalCoverage(coverage []Coverage, expected map[string]struct{}, requireComplete bool) ([]Coverage, error) {
	if coverage == nil {
		return nil, invalid(ErrorCodeArtifactCoverageInvalid, "coverage must be explicit")
	}
	canonical := cloneCoverage(coverage)
	seen := make(map[string]struct{}, len(canonical))
	for index := range canonical {
		item := &canonical[index]
		if !canonicalSectionKey(item.SectionKey) || !validCoverageStatus(item.Status) {
			return nil, invalid(ErrorCodeArtifactCoverageInvalid, "coverage identity or status is invalid")
		}
		if expected != nil {
			if _, exists := expected[item.SectionKey]; !exists {
				return nil, invalid(ErrorCodeArtifactCoverageInvalid, "coverage does not belong to outline")
			}
		}
		if _, duplicate := seen[item.SectionKey]; duplicate {
			return nil, invalid(ErrorCodeArtifactCoverageInvalid, "coverage section key is duplicated")
		}
		seen[item.SectionKey] = struct{}{}
		if item.Gaps == nil {
			return nil, invalid(ErrorCodeArtifactCoverageInvalid, "coverage gaps must be explicit")
		}
		gapSeen := make(map[string]struct{}, len(item.Gaps))
		for _, gap := range item.Gaps {
			if !canonicalText(gap.Code, maxGapCodeBytes) || !canonicalText(gap.Description, maxGapDescription) {
				return nil, invalid(ErrorCodeArtifactCoverageInvalid, "knowledge gap is invalid")
			}
			if _, duplicate := gapSeen[gap.Code]; duplicate {
				return nil, invalid(ErrorCodeArtifactCoverageInvalid, "knowledge gap code is duplicated")
			}
			gapSeen[gap.Code] = struct{}{}
		}
		sort.Slice(item.Gaps, func(i, j int) bool { return item.Gaps[i].Code < item.Gaps[j].Code })
		switch item.Status {
		case CoverageCovered:
			if len(item.Gaps) != 0 {
				return nil, invalid(ErrorCodeArtifactCoverageInvalid, "covered section cannot retain gaps")
			}
		case CoveragePartial, CoverageGap:
			if len(item.Gaps) == 0 {
				return nil, invalid(ErrorCodeArtifactCoverageInvalid, "partial or missing section requires explicit gaps")
			}
		}
	}
	if requireComplete && len(expected) != len(canonical) {
		return nil, invalid(ErrorCodeArtifactCoverageInvalid, "coverage does not include every outlined section")
	}
	sort.Slice(canonical, func(i, j int) bool { return canonical[i].SectionKey < canonical[j].SectionKey })
	return canonical, nil
}

func validateSectionEvidence(content string, citations []Citation, documentSources []DocumentSource, coverage Coverage) error {
	supportCount := len(citations) + len(documentSources)
	switch coverage.Status {
	case CoverageCovered:
		if content == "" || supportCount == 0 {
			return invalid(ErrorCodeArtifactCoverageInvalid, "covered section requires body and verified sources")
		}
	case CoveragePartial:
		if content == "" || supportCount == 0 {
			return invalid(ErrorCodeArtifactCoverageInvalid, "partial section requires body and verified sources")
		}
	case CoverageGap:
		if content != "" || supportCount != 0 {
			return invalid(ErrorCodeArtifactCoverageInvalid, "missing knowledge cannot be replaced with generated content")
		}
	}
	return nil
}

func validMetadata(metadata *GenerationMetadata) bool {
	return metadata != nil && canonicalText(metadata.PromptVersion, 256) && canonicalText(metadata.ModelVersion, 256) &&
		canonicalText(metadata.WorkflowDefinitionVersion, 256) && canonicalText(metadata.SchemaVersion, 256)
}

func validID(value foundation.ID) bool {
	parsed, err := foundation.ParseID(string(value))
	return err == nil && parsed == value
}

func validStatus(status Status) bool {
	switch status {
	case StatusPlanning, StatusOutlineReview, StatusGenerating, StatusDraft, StatusApproved, StatusExported, StatusPublishProposed, StatusPublished, StatusArchived:
		return true
	default:
		return false
	}
}

func validCoverageStatus(status CoverageStatus) bool {
	return status == CoverageCovered || status == CoveragePartial || status == CoverageGap
}

func canonicalText(value string, maxBytes int) bool {
	return value != "" && value == strings.TrimSpace(value) && len(value) <= maxBytes && utf8.ValidString(value)
}

func canonicalOptionalText(value string, maxBytes int) bool {
	return value == strings.TrimSpace(value) && len(value) <= maxBytes && utf8.ValidString(value)
}

func canonicalSectionKey(value string) bool {
	if !canonicalText(value, maxSectionKeyBytes) {
		return false
	}
	for _, runeValue := range value {
		if !(runeValue >= 'a' && runeValue <= 'z') && !(runeValue >= '0' && runeValue <= '9') && runeValue != '-' {
			return false
		}
	}
	return true
}

func canonicalSHA256(value string) bool {
	if len(value) != 64 || value != strings.ToLower(value) {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}

func cloneCoverage(coverage []Coverage) []Coverage {
	result := make([]Coverage, len(coverage))
	for index, item := range coverage {
		result[index] = item
		result[index].Gaps = make([]Gap, len(item.Gaps))
		copy(result[index].Gaps, item.Gaps)
	}
	return result
}

func cloneCitations(citations []Citation) []Citation {
	result := make([]Citation, len(citations))
	copy(result, citations)
	return result
}

func cloneDocumentSources(values []DocumentSource) []DocumentSource {
	result := make([]DocumentSource, len(values))
	copy(result, values)
	return result
}
