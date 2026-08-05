// Package domain defines mutable Working Drafts and immutable Article Revision freeze rules.
package domain

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"path"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	changecontroldomain "github.com/CodeZen-Lizhi/zhixu/internal/changecontrol/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
)

const (
	// MaxTitleBytes 是 Working Draft 与 Document 标题的 UTF-8 字节上限。
	MaxTitleBytes = 512
	// MaxTargetPathBytes 是 Working Draft 目标路径的 UTF-8 字节上限。
	MaxTargetPathBytes = 4096
	// MaxBodyBytes 与 Safe Writeback 的单文件内容上限保持一致。
	MaxBodyBytes = 10 * 1024 * 1024
	// MaxRevisionNo 与 PostgreSQL integer revision_no 的正数上限保持一致。
	MaxRevisionNo = 1<<31 - 1
)

// WorkingDraftStatus 表示可恢复编辑状态。
type WorkingDraftStatus string

const (
	// WorkingDraftEditing 表示草稿仍可通过 CAS 自动保存。
	WorkingDraftEditing WorkingDraftStatus = "EDITING"
	// WorkingDraftArchived 表示草稿只保留历史读取，不再接受编辑或冻结。
	WorkingDraftArchived WorkingDraftStatus = "ARCHIVED"
)

// DocumentLifecycle 表示正式 Document 身份的生命周期。
type DocumentLifecycle string

const (
	// DocumentDraft 尚未完成正式写回。
	DocumentDraft DocumentLifecycle = "DRAFT"
	// DocumentPublished 已拥有正式发布 Revision。
	DocumentPublished DocumentLifecycle = "PUBLISHED"
	// DocumentArchived 已从日常创作入口归档。
	DocumentArchived DocumentLifecycle = "ARCHIVED"
	// DocumentDeleted 是保留身份的逻辑删除状态。
	DocumentDeleted DocumentLifecycle = "DELETED"
)

// RevisionStatus 表示 Article Revision 的治理状态。
type RevisionStatus string

const (
	// RevisionDraft 是 Freeze 新增的待发布不可变内容版本。
	RevisionDraft RevisionStatus = "DRAFT"
	// RevisionReview 表示 Revision 正在审阅。
	RevisionReview RevisionStatus = "REVIEW"
	// RevisionApproved 表示 Revision 已批准但尚未正式写回。
	RevisionApproved RevisionStatus = "APPROVED"
	// RevisionPublished 表示 Revision 已绑定成功的 Git Commit。
	RevisionPublished RevisionStatus = "PUBLISHED"
	// RevisionSuperseded 表示已有更新版本替代该 Revision。
	RevisionSuperseded RevisionStatus = "SUPERSEDED"
	// RevisionArchived 表示 Revision 已归档。
	RevisionArchived RevisionStatus = "ARCHIVED"
)

// PublicationStatus 表示冻结 Revision 到正式 Git Commit 的恢复状态。
type PublicationStatus string

const (
	// PublicationPending 表示 Proposal 已绑定但尚无权威 Commit mapping。
	PublicationPending PublicationStatus = "PENDING"
	// PublicationPublished 表示 Revision 与 Document 已由 Commit mapping 完成发布。
	PublicationPublished PublicationStatus = "PUBLISHED"
	// PublicationRecoveryRequired 表示 Commit mapping 与冻结身份存在可诊断冲突。
	PublicationRecoveryRequired PublicationStatus = "RECOVERY_REQUIRED"
	// PublicationClosed 表示 Proposal 已拒绝、要求修订或取消，不再阻塞后续发布。
	PublicationClosed PublicationStatus = "CLOSED"
)

// ProposalTargetMode 复用 Change Control 的目标存在性契约。
type ProposalTargetMode = changecontroldomain.TargetMode

const (
	// ProposalTargetCreateOnly 要求目标在审批和应用时均不存在。
	ProposalTargetCreateOnly = changecontroldomain.TargetModeCreateOnly
	// ProposalTargetReplace 要求目标是已发布 Document 的既有文件。
	ProposalTargetReplace = changecontroldomain.TargetModeReplace
)

// WorkingDraft 是服务端持久的可变编辑恢复状态。
type WorkingDraft struct {
	ID          foundation.ID
	WorkspaceID foundation.ID
	DocumentID  foundation.ID
	Title       string
	TargetPath  string
	Body        string
	Status      WorkingDraftStatus
	Version     int64
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

// Document 是可发布文章的稳定身份。
type Document struct {
	ID                         foundation.ID
	WorkspaceID                foundation.ID
	CanonicalPath              string
	Title                      string
	Lifecycle                  DocumentLifecycle
	CurrentPublishedRevisionID foundation.ID
	Version                    int64
	CreatedAt                  time.Time
	UpdatedAt                  time.Time
}

// ArticleRevision 是一次显式 Freeze 产生的不可变 Markdown 内容版本。
type ArticleRevision struct {
	ID               foundation.ID
	WorkspaceID      foundation.ID
	DocumentID       foundation.ID
	SourceVersionID  foundation.ID
	ParentRevisionID foundation.ID
	RevisionNo       int
	Content          string
	ContentHash      string
	Status           RevisionStatus
	OptimizationMode string
	GitCommit        string
	CreatedByType    string
	CreatedAt        time.Time
}

// PublicationBinding 是 Revision、Proposal 与最终 Commit 之间的持久恢复身份。
type PublicationBinding struct {
	ID                 foundation.ID
	ReservationID      foundation.ID
	WorkspaceID        foundation.ID
	DocumentID         foundation.ID
	ArticleRevisionID  foundation.ID
	ProposalID         foundation.ID
	ProposalRevisionID foundation.ID
	TargetPath         string
	ContentHash        string
	TargetMode         ProposalTargetMode
	AbsenceToken       string
	Status             PublicationStatus
	GitCommit          string
	ErrorCode          string
	Version            int64
	CreatedAt          time.Time
	UpdatedAt          time.Time
	PublishedAt        *time.Time
}

// NewBlankWorkingDraft 创建允许空标题、空路径和空正文的 v1 编辑状态。
func NewBlankWorkingDraft(id, workspaceID foundation.ID, now time.Time) (WorkingDraft, error) {
	now = canonicalTime(now)
	draft := WorkingDraft{
		ID: id, WorkspaceID: workspaceID, Status: WorkingDraftEditing,
		Version: 1, CreatedAt: now, UpdatedAt: now,
	}
	if err := draft.Validate(); err != nil {
		return WorkingDraft{}, err
	}
	return draft, nil
}

// Validate 检查持久 Working Draft 的本地不变量，允许尚不可冻结的空白或临时路径。
func (draft WorkingDraft) Validate() error {
	if !validID(draft.ID) || !validID(draft.WorkspaceID) ||
		(draft.DocumentID != "" && !validID(draft.DocumentID)) ||
		!validWorkingTitle(draft.Title) || !validWorkingPath(draft.TargetPath) ||
		!validBody(draft.Body, true) || !draft.Status.Valid() || draft.Version < 1 ||
		draft.CreatedAt.IsZero() || draft.UpdatedAt.Before(draft.CreatedAt) {
		return invalid(ErrorCodeDraftInvalid, "working draft fields are invalid")
	}
	return nil
}

// ApplyUpdate 以 expected version 生成下一版 Working Draft，不创建 Article Revision。
func ApplyUpdate(current WorkingDraft, expectedVersion int64, title, targetPath, body string, now time.Time) (WorkingDraft, error) {
	if err := current.Validate(); err != nil {
		return WorkingDraft{}, err
	}
	if current.Status != WorkingDraftEditing {
		return WorkingDraft{}, conflict("archived working draft cannot be updated")
	}
	if expectedVersion != current.Version {
		return WorkingDraft{}, conflict("working draft expected version is stale")
	}
	if !validWorkingTitle(title) || !validWorkingPath(targetPath) || !validBody(body, true) {
		return WorkingDraft{}, invalid(ErrorCodeDraftInvalid, "working draft update fields are invalid")
	}
	now = canonicalTime(now)
	if now.IsZero() || now.Before(current.UpdatedAt) {
		return WorkingDraft{}, invalid(ErrorCodeDraftInvalid, "working draft update time is invalid")
	}
	next := current
	next.Title = title
	next.TargetPath = targetPath
	next.Body = body
	next.Version++
	next.UpdatedAt = now
	return next, nil
}

// PrepareFreeze validates publishable fields and builds the next Document and immutable Revision.
func PrepareFreeze(
	current WorkingDraft,
	expectedVersion int64,
	documentID foundation.ID,
	revisionID foundation.ID,
	existing *Document,
	parent *ArticleRevision,
	now time.Time,
) (WorkingDraft, Document, ArticleRevision, error) {
	if err := current.Validate(); err != nil {
		return WorkingDraft{}, Document{}, ArticleRevision{}, err
	}
	if current.Status != WorkingDraftEditing || expectedVersion != current.Version {
		return WorkingDraft{}, Document{}, ArticleRevision{}, conflict("working draft cannot be frozen from the requested version")
	}
	title, err := CanonicalizeTitle(current.Title)
	if err != nil {
		return WorkingDraft{}, Document{}, ArticleRevision{}, err
	}
	targetPath, err := CanonicalizeTargetPath(current.TargetPath)
	if err != nil {
		return WorkingDraft{}, Document{}, ArticleRevision{}, err
	}
	content := CanonicalizeMarkdown(current.Body)
	if !validBody(content, false) {
		return WorkingDraft{}, Document{}, ArticleRevision{}, invalid(ErrorCodeFreezeInvalid, "article revision content must be non-empty Markdown")
	}
	if !validID(documentID) || !validID(revisionID) {
		return WorkingDraft{}, Document{}, ArticleRevision{}, invalid(ErrorCodeFreezeInvalid, "freeze identities are invalid")
	}
	now = canonicalTime(now)
	if now.IsZero() || now.Before(current.UpdatedAt) {
		return WorkingDraft{}, Document{}, ArticleRevision{}, invalid(ErrorCodeFreezeInvalid, "freeze time is invalid")
	}

	var document Document
	var parentID foundation.ID
	revisionNo := 1
	if current.DocumentID == "" {
		if existing != nil || parent != nil {
			return WorkingDraft{}, Document{}, ArticleRevision{}, invalid(ErrorCodeFreezeInvalid, "unbound draft cannot reuse a document revision")
		}
		document = Document{
			ID: documentID, WorkspaceID: current.WorkspaceID, CanonicalPath: targetPath,
			Title: title, Lifecycle: DocumentDraft, Version: 1, CreatedAt: now, UpdatedAt: now,
		}
	} else {
		if existing == nil || parent == nil || current.DocumentID != documentID ||
			existing.ID != documentID || existing.WorkspaceID != current.WorkspaceID ||
			parent.DocumentID != documentID || parent.WorkspaceID != current.WorkspaceID {
			return WorkingDraft{}, Document{}, ArticleRevision{}, invalid(ErrorCodeFreezeInvalid, "bound draft document history is inconsistent")
		}
		if err := existing.Validate(); err != nil {
			return WorkingDraft{}, Document{}, ArticleRevision{}, err
		}
		if err := parent.Validate(); err != nil {
			return WorkingDraft{}, Document{}, ArticleRevision{}, err
		}
		document = *existing
		if document.Lifecycle == DocumentArchived || document.Lifecycle == DocumentDeleted {
			return WorkingDraft{}, Document{}, ArticleRevision{}, conflict("archived or deleted document cannot accept a new authoring revision")
		}
		if document.Lifecycle == DocumentPublished && targetPath != document.CanonicalPath {
			return WorkingDraft{}, Document{}, ArticleRevision{}, conflict("published document path cannot change through authoring freeze")
		}
		document.Title = title
		document.CanonicalPath = targetPath
		document.Version++
		document.UpdatedAt = now
		parentID = parent.ID
		revisionNo = parent.RevisionNo + 1
	}

	revision := ArticleRevision{
		ID: revisionID, WorkspaceID: current.WorkspaceID, DocumentID: document.ID,
		ParentRevisionID: parentID, RevisionNo: revisionNo, Content: content,
		ContentHash: ComputeContentHash(content), Status: RevisionDraft,
		OptimizationMode: "NONE", CreatedByType: "USER", CreatedAt: now,
	}
	next := current
	next.DocumentID = document.ID
	next.Title = title
	next.TargetPath = targetPath
	next.Body = content
	next.Version++
	next.UpdatedAt = now
	if err := document.Validate(); err != nil {
		return WorkingDraft{}, Document{}, ArticleRevision{}, err
	}
	if err := revision.Validate(); err != nil {
		return WorkingDraft{}, Document{}, ArticleRevision{}, err
	}
	return next, document, revision, nil
}

// Validate checks a Document snapshot returned by the repository.
func (document Document) Validate() error {
	canonicalTitle, titleErr := CanonicalizeTitle(document.Title)
	canonicalPath, pathErr := CanonicalizeTargetPath(document.CanonicalPath)
	if !validID(document.ID) || !validID(document.WorkspaceID) ||
		(document.CurrentPublishedRevisionID != "" && !validID(document.CurrentPublishedRevisionID)) ||
		titleErr != nil || canonicalTitle != document.Title || pathErr != nil || canonicalPath != document.CanonicalPath ||
		!document.Lifecycle.Valid() || document.Version < 1 || document.CreatedAt.IsZero() ||
		document.UpdatedAt.Before(document.CreatedAt) {
		return invalid(ErrorCodeFreezeInvalid, "document fields are invalid")
	}
	if (document.Lifecycle == DocumentDraft && document.CurrentPublishedRevisionID != "") ||
		(document.Lifecycle == DocumentPublished && document.CurrentPublishedRevisionID == "") {
		return invalid(ErrorCodeFreezeInvalid, "document lifecycle and published revision are inconsistent")
	}
	return nil
}

// Validate checks an immutable Article Revision snapshot.
func (revision ArticleRevision) Validate() error {
	if !validID(revision.ID) || !validID(revision.WorkspaceID) || !validID(revision.DocumentID) ||
		(revision.SourceVersionID != "" && !validID(revision.SourceVersionID)) ||
		(revision.ParentRevisionID != "" && !validID(revision.ParentRevisionID)) ||
		revision.RevisionNo < 1 || revision.RevisionNo > MaxRevisionNo || !validBody(revision.Content, false) ||
		revision.ContentHash != ComputeContentHash(revision.Content) || !revision.Status.Valid() ||
		!validOptimizationMode(revision.OptimizationMode) || !validCreatedByType(revision.CreatedByType) || revision.CreatedAt.IsZero() ||
		(revision.RevisionNo == 1 && revision.ParentRevisionID != "") ||
		(revision.RevisionNo > 1 && revision.ParentRevisionID == "") ||
		(revision.Status == RevisionPublished && !validGitCommit(revision.GitCommit)) ||
		(revision.GitCommit != "" && !validGitCommit(revision.GitCommit)) {
		return invalid(ErrorCodeFreezeInvalid, "article revision fields are invalid")
	}
	return nil
}

// Validate checks an immutable publication binding and its state shape.
func (binding PublicationBinding) Validate() error {
	canonicalPath, pathErr := CanonicalizeTargetPath(binding.TargetPath)
	if !validID(binding.ID) || !validID(binding.ReservationID) || !validID(binding.WorkspaceID) || !validID(binding.DocumentID) ||
		!validID(binding.ArticleRevisionID) || !validID(binding.ProposalID) ||
		!validID(binding.ProposalRevisionID) || pathErr != nil || canonicalPath != binding.TargetPath ||
		!validHash(binding.ContentHash) || !validTargetMode(binding.TargetMode) || !binding.Status.Valid() ||
		binding.Version < 1 || binding.CreatedAt.IsZero() || binding.UpdatedAt.Before(binding.CreatedAt) {
		return invalid(ErrorCodePublicationInvalid, "publication binding fields are invalid")
	}
	if binding.TargetMode == ProposalTargetCreateOnly {
		if !validAbsenceToken(binding.AbsenceToken) {
			return invalid(ErrorCodePublicationInvalid, "create-only publication absence token is invalid")
		}
	} else if binding.AbsenceToken != "" {
		return invalid(ErrorCodePublicationInvalid, "replace publication cannot carry an absence token")
	}
	switch binding.Status {
	case PublicationPending:
		if binding.GitCommit != "" || binding.ErrorCode != "" || binding.PublishedAt != nil {
			return invalid(ErrorCodePublicationInvalid, "pending publication state is invalid")
		}
	case PublicationPublished:
		if !validGitCommit(binding.GitCommit) || binding.ErrorCode != "" || binding.PublishedAt == nil ||
			binding.PublishedAt.Before(binding.CreatedAt) {
			return invalid(ErrorCodePublicationInvalid, "published publication state is invalid")
		}
	case PublicationRecoveryRequired, PublicationClosed:
		if binding.GitCommit != "" || !validErrorCode(binding.ErrorCode) || binding.PublishedAt != nil {
			return invalid(ErrorCodePublicationInvalid, "non-published terminal publication state is invalid")
		}
	}
	return nil
}

// CanonicalizeTitle trims surrounding whitespace and rejects blank or multiline titles.
func CanonicalizeTitle(value string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" || !utf8.ValidString(value) || len(value) > MaxTitleBytes || strings.ContainsAny(value, "\r\n\x00") {
		return "", invalid(ErrorCodeFreezeInvalid, "document title is invalid")
	}
	return value, nil
}

// CanonicalizeTargetPath returns one safe relative POSIX .md path.
func CanonicalizeTargetPath(value string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" || !utf8.ValidString(value) || len(value) > MaxTargetPathBytes ||
		strings.Contains(value, "\\") || strings.HasPrefix(value, "/") || hasControl(value) {
		return "", invalid(ErrorCodeTargetPathInvalid, "target path must be a relative POSIX Markdown path")
	}
	for _, part := range strings.Split(value, "/") {
		if part == ".." {
			return "", invalid(ErrorCodeTargetPathInvalid, "target path cannot traverse outside the workspace")
		}
	}
	clean := path.Clean(value)
	if clean == "." || clean == ".." || strings.HasPrefix(clean, "../") {
		return "", invalid(ErrorCodeTargetPathInvalid, "target path cannot traverse outside the workspace")
	}
	first, _, _ := strings.Cut(clean, "/")
	if strings.EqualFold(first, ".git") || strings.EqualFold(first, ".knowledge") {
		return "", invalid(ErrorCodeTargetPathInvalid, "target path uses a reserved workspace directory")
	}
	if !strings.EqualFold(path.Ext(clean), ".md") {
		return "", invalid(ErrorCodeTargetPathInvalid, "target path must use the .md extension")
	}
	return clean, nil
}

// ComputeContentHash returns the lower-case SHA-256 of exact Markdown bytes.
func ComputeContentHash(content string) string {
	digest := sha256.Sum256([]byte(content))
	return hex.EncodeToString(digest[:])
}

// CanonicalizeMarkdown normalizes line endings once before a Revision is frozen.
func CanonicalizeMarkdown(content string) string {
	content = strings.ReplaceAll(content, "\r\n", "\n")
	return strings.ReplaceAll(content, "\r", "\n")
}

// ComputeCreateRequestHash binds an empty Working Draft create command to its Workspace.
func ComputeCreateRequestHash(workspaceID foundation.ID) (string, error) {
	if !validID(workspaceID) {
		return "", invalid(ErrorCodeDraftInvalid, "create workspace identity is invalid")
	}
	return requestHash("working-draft-create/v1", struct {
		WorkspaceID foundation.ID `json:"workspace_id"`
	}{workspaceID})
}

// ComputeUpdateRequestHash binds exact autosave bytes and expected version.
func ComputeUpdateRequestHash(draft WorkingDraft, expectedVersion int64, title, targetPath, body string) (string, error) {
	if !validID(draft.WorkspaceID) || !validID(draft.ID) || expectedVersion < 1 ||
		!validWorkingTitle(title) || !validWorkingPath(targetPath) || !validBody(body, true) {
		return "", invalid(ErrorCodeDraftInvalid, "update request is invalid")
	}
	return requestHash("working-draft-update/v1", struct {
		WorkspaceID     foundation.ID `json:"workspace_id"`
		DraftID         foundation.ID `json:"draft_id"`
		ExpectedVersion int64         `json:"expected_version"`
		Title           string        `json:"title"`
		TargetPath      string        `json:"target_path"`
		BodyHash        string        `json:"body_hash"`
		BodyBytes       int           `json:"body_bytes"`
	}{draft.WorkspaceID, draft.ID, expectedVersion, title, targetPath, ComputeContentHash(body), len(body)})
}

// ComputeFreezeRequestHash binds Freeze to one Workspace, Draft and expected version.
func ComputeFreezeRequestHash(workspaceID, draftID foundation.ID, expectedVersion int64) (string, error) {
	if !validID(workspaceID) || !validID(draftID) || expectedVersion < 1 {
		return "", invalid(ErrorCodeFreezeInvalid, "freeze request identity is invalid")
	}
	return requestHash("working-draft-freeze/v1", struct {
		WorkspaceID     foundation.ID `json:"workspace_id"`
		DraftID         foundation.ID `json:"draft_id"`
		ExpectedVersion int64         `json:"expected_version"`
	}{workspaceID, draftID, expectedVersion})
}

// ComputePublishRequestHash binds a publish command to one frozen Article Revision.
func ComputePublishRequestHash(workspaceID, documentID, revisionID foundation.ID) (string, error) {
	if !validID(workspaceID) || !validID(documentID) || !validID(revisionID) {
		return "", invalid(ErrorCodePublicationInvalid, "publication request identity is invalid")
	}
	return requestHash("document-publication/v1", struct {
		WorkspaceID foundation.ID `json:"workspace_id"`
		DocumentID  foundation.ID `json:"document_id"`
		RevisionID  foundation.ID `json:"revision_id"`
	}{workspaceID, documentID, revisionID})
}

// ComputeAbsenceToken derives a versioned proof identity for one absent target path.
func ComputeAbsenceToken(workspaceID foundation.ID, targetPath string) (string, error) {
	canonical, err := CanonicalizeTargetPath(targetPath)
	if !validID(workspaceID) || err != nil || canonical != targetPath {
		return "", invalid(ErrorCodePublicationInvalid, "absence token target is invalid")
	}
	token, err := changecontroldomain.ComputeAbsenceToken(workspaceID, targetPath)
	if err != nil {
		return "", invalid(ErrorCodePublicationInvalid, "absence token target is invalid")
	}
	return token, nil
}

// ComputeProposalIdempotencyKey binds the external Proposal to frozen server facts.
func ComputeProposalIdempotencyKey(
	workspaceID, documentID, revisionID foundation.ID,
	targetPath, contentHash string,
	targetMode ProposalTargetMode,
	absenceToken string,
) (string, error) {
	canonicalPath, pathErr := CanonicalizeTargetPath(targetPath)
	if !validID(workspaceID) || !validID(documentID) || !validID(revisionID) || pathErr != nil ||
		canonicalPath != targetPath || !validHash(contentHash) || !validTargetMode(targetMode) ||
		(targetMode == ProposalTargetCreateOnly && !validAbsenceToken(absenceToken)) ||
		(targetMode == ProposalTargetReplace && absenceToken != "") {
		return "", invalid(ErrorCodePublicationInvalid, "publication proposal identity is invalid")
	}
	digest, err := requestHash("document-publication-proposal/v1", struct {
		WorkspaceID       foundation.ID      `json:"workspace_id"`
		DocumentID        foundation.ID      `json:"document_id"`
		ArticleRevisionID foundation.ID      `json:"article_revision_id"`
		TargetPath        string             `json:"target_path"`
		ContentHash       string             `json:"content_hash"`
		TargetMode        ProposalTargetMode `json:"target_mode"`
		AbsenceToken      string             `json:"absence_token,omitempty"`
	}{workspaceID, documentID, revisionID, targetPath, contentHash, targetMode, absenceToken})
	if err != nil {
		return "", err
	}
	return "authoring-publication/v1:" + digest, nil
}

// Valid reports whether a Working Draft status is supported.
func (status WorkingDraftStatus) Valid() bool {
	return status == WorkingDraftEditing || status == WorkingDraftArchived
}

// Valid reports whether a Document lifecycle is supported.
func (status DocumentLifecycle) Valid() bool {
	switch status {
	case DocumentDraft, DocumentPublished, DocumentArchived, DocumentDeleted:
		return true
	default:
		return false
	}
}

// Valid reports whether an Article Revision status is supported.
func (status RevisionStatus) Valid() bool {
	switch status {
	case RevisionDraft, RevisionReview, RevisionApproved, RevisionPublished, RevisionSuperseded, RevisionArchived:
		return true
	default:
		return false
	}
}

// Valid reports whether a publication status is supported.
func (status PublicationStatus) Valid() bool {
	return status == PublicationPending || status == PublicationPublished ||
		status == PublicationRecoveryRequired || status == PublicationClosed
}

func requestHash(schema string, payload any) (string, error) {
	encoded, err := json.Marshal(struct {
		Schema  string `json:"schema"`
		Payload any    `json:"payload"`
	}{schema, payload})
	if err != nil {
		return "", invalid(ErrorCodeDraftInvalid, "authoring request cannot be encoded")
	}
	digest := sha256.Sum256(encoded)
	return hex.EncodeToString(digest[:]), nil
}

func validWorkingTitle(value string) bool {
	return utf8.ValidString(value) && len(value) <= MaxTitleBytes && !strings.ContainsAny(value, "\r\n\x00")
}

func validWorkingPath(value string) bool {
	return utf8.ValidString(value) && len(value) <= MaxTargetPathBytes && !strings.ContainsAny(value, "\r\n\x00")
}

func validBody(value string, allowBlank bool) bool {
	return utf8.ValidString(value) && len(value) <= MaxBodyBytes && !strings.ContainsRune(value, '\x00') &&
		(allowBlank || strings.TrimSpace(value) != "")
}

func hasControl(value string) bool {
	for _, char := range value {
		if unicode.IsControl(char) {
			return true
		}
	}
	return false
}

func validID(value foundation.ID) bool {
	parsed, err := foundation.ParseID(string(value))
	return err == nil && parsed == value
}

func validHash(value string) bool {
	return len(value) == 64 && validLowerHex(value)
}

func validGitCommit(value string) bool {
	return (len(value) == 40 || len(value) == 64) && validLowerHex(value)
}

func validOptimizationMode(value string) bool {
	switch value {
	case "NONE", "CLARITY", "STRUCTURE", "COMPLETENESS":
		return true
	default:
		return false
	}
}

func validCreatedByType(value string) bool {
	return value == "USER" || value == "AGENT" || value == "SYSTEM"
}

func canonicalTime(value time.Time) time.Time {
	return value.UTC().Truncate(time.Microsecond)
}

func validLowerHex(value string) bool {
	for _, character := range value {
		if (character < '0' || character > '9') && (character < 'a' || character > 'f') {
			return false
		}
	}
	return true
}

func validAbsenceToken(value string) bool {
	return changecontroldomain.ValidAbsenceToken(value)
}

func validTargetMode(mode ProposalTargetMode) bool {
	return mode == ProposalTargetCreateOnly || mode == ProposalTargetReplace
}

func validErrorCode(value string) bool {
	if value == "" || len(value) > 128 || value[0] < 'A' || value[0] > 'Z' {
		return false
	}
	for _, character := range value {
		if (character < 'A' || character > 'Z') && (character < '0' || character > '9') && character != '_' {
			return false
		}
	}
	return true
}
