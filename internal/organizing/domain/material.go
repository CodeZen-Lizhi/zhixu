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
	// MaxDraftMaterials 限制一次 Suggested Material Set 的候选数量。
	MaxDraftMaterials = 100
	// MaxSnapshotMaterials 限制 Smart Collection 展开后的冻结材料总数。
	MaxSnapshotMaterials = 500
	// MaxEvidencePerMaterial 限制单项材料可绑定的完整 Citation tuple 数量。
	MaxEvidencePerMaterial = 100
)

// MaterialKind 是 Draft 与 Snapshot 共用的封闭判别联合。
type MaterialKind string

const (
	// MaterialSourceVersion 引用不可变 Source Version。
	MaterialSourceVersion MaterialKind = "SOURCE_VERSION"
	// MaterialDocumentRevision 引用稳定 Document 下的不可变 Article Revision。
	MaterialDocumentRevision MaterialKind = "DOCUMENT_REVISION"
	// MaterialClaim 引用带版本的正式 Claim。
	MaterialClaim MaterialKind = "CLAIM"
	// MaterialSmartCollection 引用带查询快照身份的 Smart Collection。
	MaterialSmartCollection MaterialKind = "SMART_COLLECTION"
)

// EvidenceRef 与 Retrieval CitationReferenceQuery 一一对应；Workspace 由父级 Draft/Snapshot 绑定。
type EvidenceRef struct {
	IndexVersionID  foundation.ID `json:"index_version_id"`
	ChunkID         foundation.ID `json:"chunk_id"`
	SourceVersionID foundation.ID `json:"source_version_id"`
	SourceSpanID    foundation.ID `json:"source_span_id"`
	ContentHash     string        `json:"content_hash"`
	ExcerptHash     string        `json:"excerpt_hash"`
}

// MaterialRef 通过 kind 对 Source/Document/Claim/Collection 四种引用做严格互斥表达。
type MaterialRef struct {
	Kind               MaterialKind  `json:"kind"`
	SourceVersionID    foundation.ID `json:"source_version_id,omitempty"`
	DocumentID         foundation.ID `json:"document_id,omitempty"`
	ArticleRevisionID  foundation.ID `json:"article_revision_id,omitempty"`
	ClaimID            foundation.ID `json:"claim_id,omitempty"`
	CollectionID       foundation.ID `json:"collection_id,omitempty"`
	OriginCollectionID foundation.ID `json:"origin_collection_id,omitempty"`
	ProfileRevisionID  foundation.ID `json:"profile_revision_id,omitempty"`
	Version            int64         `json:"version"`
	ContentHash        string        `json:"content_hash,omitempty"`
	QueryHash          string        `json:"query_hash,omitempty"`
	ReadModelRevision  string        `json:"read_model_revision,omitempty"`
	Evidence           []EvidenceRef `json:"evidence"`
}

// SuggestionReasonCode 是可解释召回的稳定原因码。
type SuggestionReasonCode string

const (
	// ReasonHybridMatch 表示 Hybrid Search 命中。
	ReasonHybridMatch SuggestionReasonCode = "HYBRID_MATCH"
	// ReasonProfileMatch 表示 Document Knowledge Profile 命中。
	ReasonProfileMatch SuggestionReasonCode = "PROFILE_MATCH"
	// ReasonAliasMatch 表示正式 Topic 别名命中。
	ReasonAliasMatch SuggestionReasonCode = "ALIAS_MATCH"
	// ReasonFormalKnowledge 表示正式 Claim 命中。
	ReasonFormalKnowledge SuggestionReasonCode = "FORMAL_KNOWLEDGE"
	// ReasonCollectionMember 表示来自 Smart Collection 成员。
	ReasonCollectionMember SuggestionReasonCode = "COLLECTION_MEMBER"
	// ReasonUserAdded 表示用户显式补充。
	ReasonUserAdded SuggestionReasonCode = "USER_ADDED"
)

// MaterialOrigin 区分系统建议与用户显式补充。
type MaterialOrigin string

const (
	// MaterialOriginSuggested 表示由有界建议服务产生。
	MaterialOriginSuggested MaterialOrigin = "SUGGESTED"
	// MaterialOriginUser 表示用户显式添加。
	MaterialOriginUser MaterialOrigin = "USER"
)

// MaterialAvailability 是确认前 owner 复核后的可用性。
type MaterialAvailability string

const (
	// MaterialAvailable 表示当前版本与 Evidence 可访问。
	MaterialAvailable MaterialAvailability = "AVAILABLE"
	// MaterialStale 表示 owner version 已变化。
	MaterialStale MaterialAvailability = "STALE"
	// MaterialUnavailable 表示资源或 Evidence 不再可访问。
	MaterialUnavailable MaterialAvailability = "UNAVAILABLE"
)

// Valid 报告 kind 是否属于公开判别联合。
func (kind MaterialKind) Valid() bool {
	return kind == MaterialSourceVersion || kind == MaterialDocumentRevision || kind == MaterialClaim || kind == MaterialSmartCollection
}

// Validate 检查完整 Citation tuple 与双哈希，不允许只保存局部 Evidence 身份。
func (evidence EvidenceRef) Validate() error {
	identities := []foundation.ID{evidence.IndexVersionID, evidence.ChunkID, evidence.SourceVersionID, evidence.SourceSpanID}
	for _, identity := range identities {
		if !validID(identity) {
			return invalid(ErrorCodeMaterialInvalid, "material evidence citation tuple is incomplete")
		}
	}
	if !isHash(evidence.ContentHash) || !isHash(evidence.ExcerptHash) {
		return invalid(ErrorCodeMaterialInvalid, "material evidence hashes are invalid")
	}
	return nil
}

// Validate 检查 MaterialRef 恰好满足其 kind 对应的一种形状。
func (reference MaterialRef) Validate() error {
	if !reference.Kind.Valid() || reference.Version < 0 || len(reference.Evidence) > MaxEvidencePerMaterial {
		return invalid(ErrorCodeMaterialInvalid, "material reference fields are invalid")
	}
	if reference.OriginCollectionID != "" && (reference.Kind == MaterialSmartCollection || !validID(reference.OriginCollectionID)) {
		return invalid(ErrorCodeMaterialInvalid, "material collection origin is invalid")
	}
	valid := false
	switch reference.Kind {
	case MaterialSourceVersion:
		valid = validID(reference.SourceVersionID) && reference.Version == 0 && isHash(reference.ContentHash) &&
			reference.DocumentID == "" && reference.ArticleRevisionID == "" && reference.ClaimID == "" && reference.CollectionID == "" &&
			reference.QueryHash == "" && reference.ReadModelRevision == ""
	case MaterialDocumentRevision:
		valid = validID(reference.DocumentID) && validID(reference.ArticleRevisionID) && reference.Version > 0 && isHash(reference.ContentHash) &&
			reference.SourceVersionID == "" && reference.ClaimID == "" && reference.CollectionID == "" &&
			reference.ProfileRevisionID == "" && reference.QueryHash == "" && reference.ReadModelRevision == ""
	case MaterialClaim:
		valid = validID(reference.ClaimID) && reference.Version > 0 && isHash(reference.ContentHash) && len(reference.Evidence) > 0 &&
			reference.SourceVersionID == "" && reference.DocumentID == "" && reference.ArticleRevisionID == "" && reference.CollectionID == "" &&
			reference.ProfileRevisionID == "" && reference.QueryHash == "" && reference.ReadModelRevision == ""
	case MaterialSmartCollection:
		valid = validID(reference.CollectionID) && reference.Version > 0 && isHash(reference.QueryHash) && isHash(reference.ReadModelRevision) &&
			reference.SourceVersionID == "" && reference.DocumentID == "" && reference.ArticleRevisionID == "" && reference.ClaimID == "" &&
			reference.OriginCollectionID == "" && reference.ProfileRevisionID == "" && reference.ContentHash == "" && len(reference.Evidence) == 0
	}
	if !valid {
		return invalid(ErrorCodeMaterialInvalid, "material reference does not match its kind")
	}
	if reference.ProfileRevisionID != "" && !validID(reference.ProfileRevisionID) {
		return invalid(ErrorCodeMaterialInvalid, "material profile revision identity is invalid")
	}
	seenEvidence := make(map[string]struct{}, len(reference.Evidence))
	var indexVersion foundation.ID
	for _, evidence := range reference.Evidence {
		if err := evidence.Validate(); err != nil {
			return err
		}
		if indexVersion == "" {
			indexVersion = evidence.IndexVersionID
		} else if evidence.IndexVersionID != indexVersion {
			return invalid(ErrorCodeMaterialInvalid, "material evidence must use one frozen index version")
		}
		key := evidenceIdentity(evidence)
		if _, duplicate := seenEvidence[key]; duplicate {
			return invalid(ErrorCodeMaterialInvalid, "material evidence is duplicated")
		}
		seenEvidence[key] = struct{}{}
	}
	return nil
}

// IdentityKey 返回 Draft 内去重使用的稳定判别身份。
func (reference MaterialRef) IdentityKey() (string, error) {
	if err := reference.Validate(); err != nil {
		return "", err
	}
	switch reference.Kind {
	case MaterialSourceVersion:
		return string(reference.Kind) + ":" + string(reference.SourceVersionID), nil
	case MaterialDocumentRevision:
		return string(reference.Kind) + ":" + string(reference.DocumentID) + ":" + string(reference.ArticleRevisionID), nil
	case MaterialClaim:
		return string(reference.Kind) + ":" + string(reference.ClaimID), nil
	default:
		return string(reference.Kind) + ":" + string(reference.CollectionID), nil
	}
}

// CanonicalMaterialRef sorts Evidence by full tuple before persistence or hashing.
func CanonicalMaterialRef(reference MaterialRef) (MaterialRef, error) {
	evidence := make([]EvidenceRef, len(reference.Evidence))
	copy(evidence, reference.Evidence)
	reference.Evidence = evidence
	sort.Slice(reference.Evidence, func(i, j int) bool {
		return evidenceIdentity(reference.Evidence[i]) < evidenceIdentity(reference.Evidence[j])
	})
	if err := reference.Validate(); err != nil {
		return MaterialRef{}, err
	}
	return reference, nil
}

func evidenceIdentity(evidence EvidenceRef) string {
	encoded, _ := json.Marshal(evidence)
	return string(encoded)
}

func validID(value foundation.ID) bool {
	parsed, err := foundation.ParseID(string(value))
	return err == nil && parsed == value
}

func isHash(value string) bool {
	if len(value) != sha256.Size*2 || value != strings.ToLower(value) {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}

func canonicalTime(value time.Time) time.Time {
	return value.UTC().Truncate(time.Microsecond)
}

func canonicalText(value string, maxBytes int, allowEmpty bool) (string, bool) {
	value = strings.TrimSpace(value)
	return value, utf8.ValidString(value) && len(value) <= maxBytes && (allowEmpty || value != "") && !strings.ContainsRune(value, 0)
}
