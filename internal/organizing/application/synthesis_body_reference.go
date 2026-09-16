package application

import "github.com/CodeZen-Lizhi/zhixu/internal/foundation"

// SynthesisPublicationBinding 在冻结模型输入前依据历史 Authoring 提交证据核验，
// 应用结果前还会再次核验。
type SynthesisPublicationBinding struct {
	WorkspaceID    foundation.ID `json:"workspace_id"`
	NoteID         foundation.ID `json:"note_id"`
	RevisionID     foundation.ID `json:"revision_id"`
	PublicationID  foundation.ID `json:"publication_id"`
	ProjectionHash string        `json:"projection_hash"`
}
