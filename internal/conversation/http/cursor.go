package conversationhttp

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/conversation/application"
	conversationdomain "github.com/CodeZen-Lizhi/zhixu/internal/conversation/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
)

const cursorSchemaVersion = 1

type cursorDocument struct {
	SchemaVersion  int    `json:"schema_version"`
	Kind           string `json:"kind"`
	WorkspaceID    string `json:"workspace_id"`
	ConversationID string `json:"conversation_id,omitempty"`
	Time           string `json:"time,omitempty"`
	Ordinal        int64  `json:"ordinal,omitempty"`
	ID             string `json:"id"`
}

// CursorCodec 编解码可跨进程重启恢复、并绑定查询范围的版本化分页游标。
type CursorCodec struct{}

// NewCursorCodec 创建 Conversation 分页游标编解码器。
func NewCursorCodec() *CursorCodec { return &CursorCodec{} }

func (codec *CursorCodec) encodeConversation(workspaceID foundation.ID, cursor conversationdomain.ConversationCursor) (string, error) {
	if codec == nil || cursor.Validate() != nil {
		return "", invalidCursor()
	}
	return encodeCursor(cursorDocument{SchemaVersion: cursorSchemaVersion, Kind: "conversation", WorkspaceID: string(workspaceID), Time: cursor.LastActivityAt.UTC().Format(time.RFC3339Nano), ID: string(cursor.ID)})
}

func (codec *CursorCodec) decodeConversation(raw string, workspaceID foundation.ID) (*conversationdomain.ConversationCursor, error) {
	if raw == "" {
		return nil, nil
	}
	document, err := decodeCursor(raw)
	if err != nil || document.SchemaVersion != cursorSchemaVersion || document.Kind != "conversation" || document.WorkspaceID != string(workspaceID) || document.ConversationID != "" || document.Ordinal != 0 {
		return nil, invalidCursor()
	}
	boundary, err := time.Parse(time.RFC3339Nano, document.Time)
	if err != nil {
		return nil, invalidCursor()
	}
	cursor := &conversationdomain.ConversationCursor{LastActivityAt: boundary.UTC(), ID: foundation.ID(document.ID)}
	if cursor.Validate() != nil {
		return nil, invalidCursor()
	}
	return cursor, nil
}

func (codec *CursorCodec) encodeTurn(workspaceID, conversationID foundation.ID, cursor conversationdomain.TurnCursor, version application.APIVersion) (string, error) {
	if codec == nil || cursor.Validate() != nil || !validTurnCursorVersion(version) {
		return "", invalidCursor()
	}
	return encodeCursor(cursorDocument{SchemaVersion: int(version), Kind: "turn", WorkspaceID: string(workspaceID), ConversationID: string(conversationID), Ordinal: cursor.Ordinal, ID: string(cursor.QuestionID)})
}

func (codec *CursorCodec) decodeTurn(raw string, workspaceID, conversationID foundation.ID, version application.APIVersion) (*conversationdomain.TurnCursor, error) {
	if codec == nil || !validTurnCursorVersion(version) {
		return nil, invalidCursor()
	}
	if raw == "" {
		return nil, nil
	}
	document, err := decodeCursor(raw)
	if err != nil || document.SchemaVersion != int(version) || document.Kind != "turn" || document.WorkspaceID != string(workspaceID) || document.ConversationID != string(conversationID) || document.Time != "" {
		return nil, invalidCursor()
	}
	cursor := &conversationdomain.TurnCursor{Ordinal: document.Ordinal, QuestionID: foundation.ID(document.ID)}
	if cursor.Validate() != nil {
		return nil, invalidCursor()
	}
	return cursor, nil
}

func encodeCursor(document cursorDocument) (string, error) {
	encoded, err := json.Marshal(document)
	if err != nil {
		return "", invalidCursor()
	}
	return base64.RawURLEncoding.EncodeToString(encoded), nil
}

func decodeCursor(raw string) (cursorDocument, error) {
	encoded, err := base64.RawURLEncoding.DecodeString(raw)
	if err != nil || len(encoded) == 0 || len(encoded) > 1024 {
		return cursorDocument{}, invalidCursor()
	}
	var document cursorDocument
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&document); err != nil || !validTurnCursorVersion(application.APIVersion(document.SchemaVersion)) || document.ID == "" {
		return cursorDocument{}, invalidCursor()
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return cursorDocument{}, invalidCursor()
	}
	return document, nil
}

func validTurnCursorVersion(version application.APIVersion) bool {
	return version == application.APIVersionV1 || version == application.APIVersionV2
}

func invalidCursor() error {
	return foundation.NewError(foundation.ErrorInvalidInput, conversationdomain.ErrorCodeCursorInvalid, false, errors.New("conversation cursor is invalid"))
}
