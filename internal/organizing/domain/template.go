package domain

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"path"
	"strings"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
)

const (
	// TemplateSchemaVersion 是首版受限模板声明版本。
	TemplateSchemaVersion        = "organizing-template/v1"
	maxTemplateNameBytes         = 128
	maxTemplateDescriptionBytes  = 2048
	maxTemplateInstructionsBytes = 8192
	maxTemplateSections          = 24
)

var mandatoryGovernanceSections = []string{"conflicts", "gaps", "sources"}

// TemplateKind 是服务器注册的四类整理结果。
type TemplateKind string

const (
	// TemplateTopicArticle 生成先审大纲的专题文章。
	TemplateTopicArticle TemplateKind = "TOPIC_ARTICLE"
	// TemplateMergeDocuments 生成保留冲突的多文档合并草稿。
	TemplateMergeDocuments TemplateKind = "MERGE_DOCUMENTS"
	// TemplateKnowledgeReport 生成默认留在 Artifact 的总结报告。
	TemplateKnowledgeReport TemplateKind = "KNOWLEDGE_REPORT"
	// TemplateInterviewReview 生成默认留在 Artifact 的面试复习文档。
	TemplateInterviewReview TemplateKind = "INTERVIEW_REVIEW"
)

// TemplateOwner 表示只读内置模板或 Workspace 自定义模板。
type TemplateOwner string

const (
	// TemplateBuiltIn 是服务器发布的只读模板。
	TemplateBuiltIn TemplateOwner = "BUILT_IN"
	// TemplateCustom 是 Workspace 内可追加 Revision 的模板。
	TemplateCustom TemplateOwner = "CUSTOM"
)

// ResultKind 表示固定 Workflow 的受控结果 owner。
type ResultKind string

const (
	// ResultMergeProposal 表示结果只能创建不覆盖输入的合并 Proposal。
	ResultMergeProposal ResultKind = "MERGE_PROPOSAL"
	// ResultArtifact 表示结果默认保留为 Artifact。
	ResultArtifact ResultKind = "ARTIFACT"
)

// LengthPreset 是模板允许的离散篇幅配置。
type LengthPreset string

const (
	// LengthShort 表示短篇输出。
	LengthShort LengthPreset = "SHORT"
	// LengthMedium 表示中等篇幅输出。
	LengthMedium LengthPreset = "MEDIUM"
	// LengthLong 表示长篇输出。
	LengthLong LengthPreset = "LONG"
)

// TemplateSection 是模板中可排序、可选或必选的章节声明。
type TemplateSection struct {
	Key      string `json:"key"`      // Key 是固定章节标识。
	Title    string `json:"title"`    // Title 是用户可读章节名。
	Required bool   `json:"required"` // Required 要求无证据时保留 GAP。
}

// MaterialPolicy 限制模板可消费的材料类型与数量。
type MaterialPolicy struct {
	AllowedKinds []MaterialKind `json:"allowed_kinds"` // AllowedKinds 是材料类型白名单。
	MinMaterials int            `json:"min_materials"` // MinMaterials 是确认下限。
	MaxMaterials int            `json:"max_materials"` // MaxMaterials 是展开后上限。
}

// PresentationPolicy 只影响表达，不改变证据或执行权限。
type PresentationPolicy struct {
	Audience        string       `json:"audience"`         // Audience 是目标读者。
	Language        string       `json:"language"`         // Language 是输出语言。
	Tone            string       `json:"tone"`             // Tone 是受限表达风格。
	Length          LengthPreset `json:"length"`           // Length 是离散篇幅。
	IncludeCode     bool         `json:"include_code"`     // IncludeCode 控制代码内容。
	IncludeExamples bool         `json:"include_examples"` // IncludeExamples 控制示例。
	IncludeFAQ      bool         `json:"include_faq"`      // IncludeFAQ 控制 FAQ。
}

// OutputDefaults 提供受控的相对目录和 Markdown 文件名模式。
type OutputDefaults struct {
	Directory       string `json:"directory"`        // Directory 是 Workspace 相对目录。
	FilenamePattern string `json:"filename_pattern"` // FilenamePattern 是受控 Markdown 文件名模式。
}

// TemplateDeclaration 是允许用户配置的完整封闭声明，不包含 Prompt、Tool、Permission 或 Workflow 节点。
type TemplateDeclaration struct {
	SchemaVersion          string             `json:"schema_version"`          // SchemaVersion 冻结解析契约。
	Kind                   TemplateKind       `json:"kind"`                    // Kind 选择固定 Workflow。
	Name                   string             `json:"name"`                    // Name 是模板名称。
	Description            string             `json:"description"`             // Description 说明模板用途。
	Materials              MaterialPolicy     `json:"materials"`               // Materials 约束确认输入。
	Sections               []TemplateSection  `json:"sections"`                // Sections 是有序章节。
	Presentation           PresentationPolicy `json:"presentation"`            // Presentation 只控制表达。
	Output                 OutputDefaults     `json:"output"`                  // Output 提供受控默认路径。
	AdditionalInstructions string             `json:"additional_instructions"` // AdditionalInstructions 按不可信内容处理。
}

// Template 是可更新 current pointer 的稳定模板身份。
type Template struct {
	ID                foundation.ID `json:"id"`                     // ID 是稳定模板身份。
	WorkspaceID       foundation.ID `json:"workspace_id,omitempty"` // WorkspaceID 对内置模板为空。
	Owner             TemplateOwner `json:"owner"`                  // Owner 区分内置与自定义。
	Kind              TemplateKind  `json:"kind"`                   // Kind 固定整理类型。
	CurrentRevisionID foundation.ID `json:"current_revision_id"`    // CurrentRevisionID 指向最新声明。
	Version           int64         `json:"version"`                // Version 用于自定义模板 CAS。
	CreatedAt         time.Time     `json:"created_at"`             // CreatedAt 是创建时间。
	UpdatedAt         time.Time     `json:"updated_at"`             // UpdatedAt 是 current pointer 更新时间。
}

// TemplateRevision 是 append-only 的受限模板声明。
type TemplateRevision struct {
	ID              foundation.ID       `json:"id"`                     // ID 是不可变 Revision 身份。
	TemplateID      foundation.ID       `json:"template_id"`            // TemplateID 绑定稳定模板。
	WorkspaceID     foundation.ID       `json:"workspace_id,omitempty"` // WorkspaceID 对内置模板为空。
	RevisionNo      int64               `json:"revision_no"`            // RevisionNo 是单调版本号。
	Declaration     TemplateDeclaration `json:"declaration"`            // Declaration 是规范化声明。
	DeclarationHash string              `json:"declaration_hash"`       // DeclarationHash 是声明摘要。
	CreatedAt       time.Time           `json:"created_at"`             // CreatedAt 是 Revision 创建时间。
}

// CompiledTemplate 是模板编译器可交给 Workflow 的唯一输出。
type CompiledTemplate struct {
	DefinitionKey              string            // DefinitionKey 指向注册 Workflow。
	DefinitionVersion          int64             // DefinitionVersion 是固定版本。
	ResultKind                 ResultKind        // ResultKind 指定结果 owner。
	RequiresOutlineApproval    bool              // RequiresOutlineApproval 强制先审大纲。
	RequiresResultConfirmation bool              // RequiresResultConfirmation 强制确认合并结果。
	EvidencePolicy             string            // EvidencePolicy 固定要求证据。
	ConflictPolicy             string            // ConflictPolicy 固定保留冲突。
	GapPolicy                  string            // GapPolicy 固定显式 GAP。
	DeclarationHash            string            // DeclarationHash 绑定运行模板。
	Sections                   []TemplateSection // Sections 是编译后章节。
}

// BuiltInTemplates 返回四个不可变、服务器拥有的模板 Revision。
func BuiltInTemplates(createdAt time.Time) ([]Template, []TemplateRevision, error) {
	createdAt = canonicalTime(createdAt)
	declarations := []TemplateDeclaration{
		builtInDeclaration(TemplateTopicArticle, "专题知识文章", "将已确认材料整理为先审大纲、再分章生成的专题文章。", []TemplateSection{{"overview", "概览", true}, {"core-concepts", "核心概念", true}, {"details", "知识详解", true}, {"examples", "示例", false}, {"conflicts", "冲突与适用条件", true}, {"gaps", "知识缺口", true}, {"sources", "来源", true}}),
		builtInDeclaration(TemplateMergeDocuments, "多文档合并整理", "比较重复、互补、冲突与独特内容，并生成不覆盖原文的合并草稿。", []TemplateSection{{"summary", "合并摘要", true}, {"common", "共同内容", true}, {"complementary", "互补内容", true}, {"conflicts", "冲突与差异", true}, {"unique", "独特内容", true}, {"conclusion", "整理结论", true}, {"gaps", "知识缺口", true}, {"sources", "来源", true}}),
		builtInDeclaration(TemplateKnowledgeReport, "知识总结报告", "生成覆盖、结论、冲突和缺口可追溯的知识报告。", []TemplateSection{{"summary", "总结", true}, {"coverage", "覆盖范围", true}, {"findings", "主要结论", true}, {"conflicts", "冲突", true}, {"gaps", "知识缺口", true}, {"sources", "来源", true}}),
		builtInDeclaration(TemplateInterviewReview, "面试复习文档", "生成核心概念、问题、追问、代码示例和来源。", []TemplateSection{{"core-concepts", "核心概念", true}, {"questions", "面试问题", true}, {"follow-ups", "追问", true}, {"code-examples", "代码示例", false}, {"conflicts", "冲突与适用条件", true}, {"gaps", "薄弱点", true}, {"sources", "来源", true}}),
	}
	templates := make([]Template, 0, len(declarations))
	revisions := make([]TemplateRevision, 0, len(declarations))
	templateIDs := []foundation.ID{"b7100000-0000-4000-8000-000000000001", "b7100000-0000-4000-8000-000000000002", "b7100000-0000-4000-8000-000000000003", "b7100000-0000-4000-8000-000000000004"}
	revisionIDs := []foundation.ID{"b7200000-0000-4000-8000-000000000001", "b7200000-0000-4000-8000-000000000002", "b7200000-0000-4000-8000-000000000003", "b7200000-0000-4000-8000-000000000004"}
	for i, declaration := range declarations {
		templateID := templateIDs[i]
		revisionID := revisionIDs[i]
		template, revision, err := newTemplateRevision(templateID, revisionID, "", TemplateBuiltIn, 1, 1, declaration, createdAt)
		if err != nil {
			return nil, nil, err
		}
		templates = append(templates, template)
		revisions = append(revisions, revision)
	}
	return templates, revisions, nil
}

// NewCustomTemplate 创建 Workspace 模板及不可变 Revision 1。
func NewCustomTemplate(templateID, revisionID, workspaceID foundation.ID, declaration TemplateDeclaration, createdAt time.Time) (Template, TemplateRevision, error) {
	if !validID(workspaceID) {
		return Template{}, TemplateRevision{}, invalid(ErrorCodeTemplateInvalid, "custom template workspace is invalid")
	}
	return newTemplateRevision(templateID, revisionID, workspaceID, TemplateCustom, 1, 1, declaration, createdAt)
}

// CanonicalTemplateDeclaration 返回可持久化的规范声明及其稳定摘要。
func CanonicalTemplateDeclaration(declaration TemplateDeclaration) (TemplateDeclaration, string, error) {
	return canonicalDeclaration(declaration)
}

// ReviseCustomTemplate 在 expected-version CAS 下追加声明 Revision。
func ReviseCustomTemplate(current Template, expectedVersion int64, revisionID foundation.ID, declaration TemplateDeclaration, createdAt time.Time) (Template, TemplateRevision, error) {
	if err := current.Validate(); err != nil {
		return Template{}, TemplateRevision{}, err
	}
	if current.Owner != TemplateCustom || current.Version != expectedVersion {
		return Template{}, TemplateRevision{}, conflict("custom template expected version is stale or template is read-only")
	}
	if declaration.Kind != current.Kind {
		return Template{}, TemplateRevision{}, invalid(ErrorCodeTemplateInvalid, "custom template kind cannot change across revisions")
	}
	next, revision, err := newTemplateRevision(current.ID, revisionID, current.WorkspaceID, TemplateCustom, current.Version+1, current.Version+1, declaration, createdAt)
	if err != nil {
		return Template{}, TemplateRevision{}, err
	}
	next.CreatedAt = current.CreatedAt
	return next, revision, nil
}

// CloneBuiltIn 从内置声明创建独立 Workspace 模板。
func CloneBuiltIn(source TemplateRevision, templateID, revisionID, workspaceID foundation.ID, name string, createdAt time.Time) (Template, TemplateRevision, error) {
	if err := source.Validate(TemplateBuiltIn); err != nil {
		return Template{}, TemplateRevision{}, err
	}
	declaration := cloneDeclaration(source.Declaration)
	declaration.Name = name
	return NewCustomTemplate(templateID, revisionID, workspaceID, declaration, createdAt)
}

// Compile 只把声明映射到已注册 Workflow 和固定治理策略。
func Compile(revision TemplateRevision) (CompiledTemplate, error) {
	owner := TemplateCustom
	if revision.WorkspaceID == "" {
		owner = TemplateBuiltIn
	}
	if err := revision.Validate(owner); err != nil {
		return CompiledTemplate{}, err
	}
	compiled := CompiledTemplate{
		DefinitionVersion: 1, EvidencePolicy: "REQUIRED", ConflictPolicy: "PRESERVE", GapPolicy: "EXPLICIT",
		DeclarationHash: revision.DeclarationHash, Sections: append([]TemplateSection(nil), revision.Declaration.Sections...),
	}
	switch revision.Declaration.Kind {
	case TemplateTopicArticle:
		compiled.DefinitionKey, compiled.ResultKind, compiled.RequiresOutlineApproval = "organizing.topic-article", ResultArtifact, true
	case TemplateMergeDocuments:
		compiled.DefinitionKey, compiled.ResultKind, compiled.RequiresResultConfirmation = "organizing.merge-documents", ResultMergeProposal, true
	case TemplateKnowledgeReport:
		compiled.DefinitionKey, compiled.ResultKind = "organizing.knowledge-report", ResultArtifact
	case TemplateInterviewReview:
		compiled.DefinitionKey, compiled.ResultKind = "organizing.interview-review", ResultArtifact
	default:
		return CompiledTemplate{}, invalid(ErrorCodeTemplateInvalid, "template kind is not registered")
	}
	return compiled, nil
}

// Validate 校验持久模板身份与 current pointer。
func (template Template) Validate() error {
	workspaceValid := (template.Owner == TemplateBuiltIn && template.WorkspaceID == "") || (template.Owner == TemplateCustom && validID(template.WorkspaceID))
	if !validID(template.ID) || !validID(template.CurrentRevisionID) || !workspaceValid || !template.Owner.Valid() || !template.Kind.Valid() || template.Version < 1 ||
		template.CreatedAt.IsZero() || canonicalTime(template.CreatedAt) != template.CreatedAt || canonicalTime(template.UpdatedAt) != template.UpdatedAt || template.UpdatedAt.Before(template.CreatedAt) {
		return invalid(ErrorCodeTemplateInvalid, "template identity is invalid")
	}
	return nil
}

// Validate 校验 append-only Template Revision 及声明摘要。
func (revision TemplateRevision) Validate(owner TemplateOwner) error {
	workspaceValid := (owner == TemplateBuiltIn && revision.WorkspaceID == "") || (owner == TemplateCustom && validID(revision.WorkspaceID))
	if !validID(revision.ID) || !validID(revision.TemplateID) || !workspaceValid || revision.RevisionNo < 1 || revision.CreatedAt.IsZero() || canonicalTime(revision.CreatedAt) != revision.CreatedAt {
		return invalid(ErrorCodeTemplateInvalid, "template revision identity is invalid")
	}
	canonical, digest, err := canonicalDeclaration(revision.Declaration)
	if err != nil || digest != revision.DeclarationHash || !declarationsEqual(canonical, revision.Declaration) {
		return invalid(ErrorCodeTemplateInvalid, "template revision declaration or hash is invalid")
	}
	return nil
}

func newTemplateRevision(templateID, revisionID, workspaceID foundation.ID, owner TemplateOwner, templateVersion, revisionNo int64, declaration TemplateDeclaration, createdAt time.Time) (Template, TemplateRevision, error) {
	createdAt = canonicalTime(createdAt)
	canonical, digest, err := canonicalDeclaration(declaration)
	if err != nil {
		return Template{}, TemplateRevision{}, err
	}
	template := Template{ID: templateID, WorkspaceID: workspaceID, Owner: owner, Kind: canonical.Kind, CurrentRevisionID: revisionID, Version: templateVersion, CreatedAt: createdAt, UpdatedAt: createdAt}
	revision := TemplateRevision{ID: revisionID, TemplateID: templateID, WorkspaceID: workspaceID, RevisionNo: revisionNo, Declaration: canonical, DeclarationHash: digest, CreatedAt: createdAt}
	if err := template.Validate(); err != nil {
		return Template{}, TemplateRevision{}, err
	}
	if err := revision.Validate(owner); err != nil {
		return Template{}, TemplateRevision{}, err
	}
	return template, revision, nil
}

func canonicalDeclaration(input TemplateDeclaration) (TemplateDeclaration, string, error) {
	result := cloneDeclaration(input)
	result.SchemaVersion = strings.TrimSpace(result.SchemaVersion)
	name, nameOK := canonicalText(result.Name, maxTemplateNameBytes, false)
	description, descriptionOK := canonicalText(result.Description, maxTemplateDescriptionBytes, false)
	instructions, instructionsOK := canonicalText(result.AdditionalInstructions, maxTemplateInstructionsBytes, true)
	result.Name, result.Description, result.AdditionalInstructions = name, description, instructions
	audience, audienceOK := canonicalText(result.Presentation.Audience, 512, true)
	language, languageOK := canonicalText(result.Presentation.Language, 64, false)
	tone, toneOK := canonicalText(result.Presentation.Tone, 128, false)
	result.Presentation.Audience, result.Presentation.Language, result.Presentation.Tone = audience, language, tone
	result.Output.Directory = strings.TrimSpace(result.Output.Directory)
	result.Output.FilenamePattern = strings.TrimSpace(result.Output.FilenamePattern)
	if result.SchemaVersion != TemplateSchemaVersion || !result.Kind.Valid() || !nameOK || !descriptionOK || !instructionsOK || !result.Presentation.Length.Valid() ||
		!audienceOK || !languageOK || !toneOK || len(result.Sections) == 0 || len(result.Sections) > maxTemplateSections ||
		result.Materials.MinMaterials < 1 || result.Materials.MaxMaterials < result.Materials.MinMaterials || result.Materials.MaxMaterials > MaxSnapshotMaterials ||
		len(result.Materials.AllowedKinds) == 0 || !validOutputDefaults(result.Output) {
		return TemplateDeclaration{}, "", invalid(ErrorCodeTemplateInvalid, "template declaration fields are invalid")
	}
	seenKinds := map[MaterialKind]struct{}{}
	for _, kind := range result.Materials.AllowedKinds {
		if !kind.Valid() {
			return TemplateDeclaration{}, "", invalid(ErrorCodeTemplateInvalid, "template material kind is invalid")
		}
		if _, ok := seenKinds[kind]; ok {
			return TemplateDeclaration{}, "", invalid(ErrorCodeTemplateInvalid, "template material kind is duplicated")
		}
		seenKinds[kind] = struct{}{}
	}
	seenSections := map[string]int{}
	for i := range result.Sections {
		result.Sections[i].Key = strings.TrimSpace(result.Sections[i].Key)
		title, ok := canonicalText(result.Sections[i].Title, 256, false)
		result.Sections[i].Title = title
		if !ok || !validSectionKey(result.Sections[i].Key) {
			return TemplateDeclaration{}, "", invalid(ErrorCodeTemplateInvalid, "template section is invalid")
		}
		if _, exists := seenSections[result.Sections[i].Key]; exists {
			return TemplateDeclaration{}, "", invalid(ErrorCodeTemplateInvalid, "template section key is duplicated")
		}
		seenSections[result.Sections[i].Key] = i
	}
	for _, key := range mandatoryGovernanceSections {
		index, exists := seenSections[key]
		if !exists {
			return TemplateDeclaration{}, "", invalid(ErrorCodeTemplateInvalid, "template must retain all governance sections")
		}
		if !result.Sections[index].Required {
			return TemplateDeclaration{}, "", invalid(ErrorCodeTemplateInvalid, "template governance sections must remain required")
		}
	}
	raw, err := json.Marshal(result)
	if err != nil {
		return TemplateDeclaration{}, "", invalid(ErrorCodeTemplateInvalid, "template declaration encoding failed")
	}
	digest := sha256.Sum256(raw)
	return result, hex.EncodeToString(digest[:]), nil
}

func builtInDeclaration(kind TemplateKind, name, description string, sections []TemplateSection) TemplateDeclaration {
	allowedKinds := []MaterialKind{MaterialSourceVersion, MaterialDocumentRevision, MaterialClaim, MaterialSmartCollection}
	return TemplateDeclaration{
		SchemaVersion: TemplateSchemaVersion, Kind: kind, Name: name, Description: description,
		Materials: MaterialPolicy{AllowedKinds: allowedKinds, MinMaterials: 1, MaxMaterials: MaxSnapshotMaterials}, Sections: sections,
		Presentation: PresentationPolicy{Audience: "", Language: "zh-CN", Tone: "清晰、准确", Length: LengthMedium, IncludeCode: true, IncludeExamples: true},
		Output:       OutputDefaults{Directory: "organized", FilenamePattern: "{slug}.md"}, AdditionalInstructions: "",
	}
}

func validOutputDefaults(output OutputDefaults) bool {
	directory := strings.TrimSpace(output.Directory)
	filename := strings.TrimSpace(output.FilenamePattern)
	firstDirectory, _, _ := strings.Cut(directory, "/")
	if directory == "" || filename == "" || strings.ContainsAny(directory+filename, "\\\x00") || strings.HasPrefix(directory, "/") || strings.Contains(filename, "/") ||
		path.Clean(directory) != directory || directory == "." || strings.HasPrefix(directory, "../") || strings.Contains(filename, "..") || !strings.HasSuffix(strings.ToLower(filename), ".md") {
		return false
	}
	if firstDirectory == ".git" || firstDirectory == ".knowledge" {
		return false
	}
	for _, token := range []string{"{slug}", "{date}"} {
		filename = strings.ReplaceAll(filename, token, "x")
	}
	return !strings.ContainsAny(filename, "{}") && len(directory) <= 1024 && len(filename) <= 256
}

func validSectionKey(value string) bool {
	if value == "" || len(value) > 64 {
		return false
	}
	for i, current := range value {
		if (current >= 'a' && current <= 'z') || (current >= '0' && current <= '9' && i > 0) || (current == '-' && i > 0 && i < len(value)-1) {
			continue
		}
		return false
	}
	return true
}

func cloneDeclaration(input TemplateDeclaration) TemplateDeclaration {
	input.Materials.AllowedKinds = append([]MaterialKind(nil), input.Materials.AllowedKinds...)
	input.Sections = append([]TemplateSection(nil), input.Sections...)
	return input
}

func declarationsEqual(left, right TemplateDeclaration) bool {
	leftRaw, leftErr := json.Marshal(left)
	rightRaw, rightErr := json.Marshal(right)
	return leftErr == nil && rightErr == nil && string(leftRaw) == string(rightRaw)
}

func (kind TemplateKind) Valid() bool {
	return kind == TemplateTopicArticle || kind == TemplateMergeDocuments || kind == TemplateKnowledgeReport || kind == TemplateInterviewReview
}
func (owner TemplateOwner) Valid() bool { return owner == TemplateBuiltIn || owner == TemplateCustom }
func (kind ResultKind) Valid() bool {
	return kind == ResultMergeProposal || kind == ResultArtifact
}
func (preset LengthPreset) Valid() bool {
	return preset == LengthShort || preset == LengthMedium || preset == LengthLong
}
