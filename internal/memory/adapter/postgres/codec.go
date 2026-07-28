package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"time"

	authdomain "github.com/CodeZen-Lizhi/zhixu/internal/auth/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation/strictjson"
	memoryapp "github.com/CodeZen-Lizhi/zhixu/internal/memory/application"
	"github.com/CodeZen-Lizhi/zhixu/internal/memory/domain"
	"github.com/jackc/pgx/v5"
)

var memoryReceiptLimits = strictjson.Limits{
	MaxDocumentBytes: domain.MaxContentBytes + 4*1024,
	MaxDepth:         10,
	MaxStringBytes:   domain.MaxContentBytes,
	MaxArrayItems:    128,
	MaxObjectFields:  64,
}

const memoryColumns = `
	id::text,workspace_id::text,owner_principal_kind,owner_principal_id::text,
	memory_type,content::text,source_type,source_ref,task_scope_id::text,status,
	expires_at,confirmed_at,confirmed_by_principal_kind,confirmed_by_principal_id::text,
	version,created_at,updated_at`

type scanner interface{ Scan(...any) error }

func scanMemory(row scanner) (domain.Memory, error) {
	var (
		memory                                      domain.Memory
		ownerKind, memoryType, sourceType, status   string
		content                                     []byte
		taskScopeID, confirmedByKind, confirmedByID *string
		expiresAt, confirmedAt                      *time.Time
	)
	if err := row.Scan(
		&memory.ID, &memory.WorkspaceID, &ownerKind, &memory.Owner.ID,
		&memoryType, &content, &sourceType, &memory.Source.Ref, &taskScopeID, &status,
		&expiresAt, &confirmedAt, &confirmedByKind, &confirmedByID,
		&memory.Version, &memory.CreatedAt, &memory.UpdatedAt,
	); err != nil {
		return domain.Memory{}, err
	}
	canonicalContent, err := domain.CanonicalContent(content)
	if err != nil {
		return domain.Memory{}, inconsistent(fmt.Errorf("decode memory content: %w", err))
	}
	memory.Owner.Kind = authdomain.PrincipalKind(ownerKind)
	memory.Type = domain.Type(memoryType)
	memory.Content = canonicalContent
	memory.Source.Type = domain.SourceType(sourceType)
	memory.Status = domain.Status(status)
	memory.TaskScopeID, err = optionalID(taskScopeID)
	if err != nil {
		return domain.Memory{}, err
	}
	memory.ExpiresAt = optionalTime(expiresAt)
	memory.ConfirmedAt = optionalTime(confirmedAt)
	if (confirmedByKind == nil) != (confirmedByID == nil) {
		return domain.Memory{}, inconsistent(errors.New("memory confirmation principal is incomplete"))
	}
	if confirmedByKind != nil {
		memory.ConfirmedBy = &domain.Principal{Kind: authdomain.PrincipalKind(*confirmedByKind), ID: foundation.ID(*confirmedByID)}
	}
	memory.CreatedAt = memory.CreatedAt.UTC().Truncate(time.Microsecond)
	memory.UpdatedAt = memory.UpdatedAt.UTC().Truncate(time.Microsecond)
	if err := domain.ValidateMemory(memory); err != nil {
		return domain.Memory{}, inconsistent(fmt.Errorf("validate persisted memory: %w", err))
	}
	return memory, nil
}

type persistedCommand struct {
	workspaceID foundation.ID
	owner       domain.Principal
	requestHash string
	commandType memoryapp.CommandType
	memoryID    foundation.ID
	expected    int64
	version     int64
	response    []byte
}

func loadCommand(ctx context.Context, db interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}, workspaceID foundation.ID, key string) (persistedCommand, bool, error) {
	var command persistedCommand
	var ownerKind string
	command.workspaceID = workspaceID
	err := db.QueryRow(ctx, `SELECT owner_principal_kind,owner_principal_id::text,request_hash,command_type,memory_id::text,expected_version,memory_version,response::text
		FROM learning.memory_command WHERE workspace_id=$1 AND idempotency_key=$2 FOR UPDATE`, string(workspaceID), key).Scan(
		&ownerKind, &command.owner.ID, &command.requestHash, &command.commandType, &command.memoryID, &command.expected, &command.version, &command.response,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return persistedCommand{}, false, nil
	}
	if err != nil {
		return persistedCommand{}, false, classify(err)
	}
	command.owner.Kind = authdomain.PrincipalKind(ownerKind)
	if err := domain.ValidatePrincipal(command.owner); err != nil || !validHash(command.requestHash) || !validCommandType(command.commandType) || !validID(command.memoryID) || !validCommandVersion(command.commandType, command.expected, command.version) {
		return persistedCommand{}, false, inconsistent(errors.New("memory command row is invalid"))
	}
	return command, true, nil
}

func commandResult(command persistedCommand) (memoryapp.CommandResult, error) {
	memory, err := decodeMemorySnapshot(command.response)
	if err != nil {
		return memoryapp.CommandResult{}, err
	}
	if memory.ID != command.memoryID || memory.WorkspaceID != command.workspaceID || memory.Version != command.version || !samePrincipal(memory.Owner, command.owner) {
		return memoryapp.CommandResult{}, inconsistent(errors.New("memory command response does not match its receipt"))
	}
	return memoryapp.CommandResult{Memory: memory, Replayed: true}, nil
}

func decodeMemorySnapshot(raw []byte) (domain.Memory, error) {
	memory, err := strictjson.DecodeObject[domain.Memory](raw, memoryReceiptLimits, nil)
	if err != nil {
		return domain.Memory{}, inconsistent(errors.New("memory receipt response is invalid"))
	}
	canonicalContent, err := domain.CanonicalContent(memory.Content)
	if err != nil {
		return domain.Memory{}, inconsistent(err)
	}
	memory.Content = canonicalContent
	memory.CreatedAt = memory.CreatedAt.UTC().Truncate(time.Microsecond)
	memory.UpdatedAt = memory.UpdatedAt.UTC().Truncate(time.Microsecond)
	memory.ExpiresAt = optionalTime(memory.ExpiresAt)
	memory.ConfirmedAt = optionalTime(memory.ConfirmedAt)
	if err := domain.ValidateMemory(memory); err != nil {
		return domain.Memory{}, inconsistent(err)
	}
	return memory, nil
}

func encodeMemorySnapshot(memory domain.Memory) ([]byte, error) {
	if err := domain.ValidateMemory(memory); err != nil {
		return nil, err
	}
	encoded, err := json.Marshal(memory)
	if err != nil {
		return nil, inconsistent(err)
	}
	return encoded, nil
}

func optionalID(value *string) (*foundation.ID, error) {
	if value == nil {
		return nil, nil
	}
	parsed, err := foundation.ParseID(*value)
	if err != nil || parsed != foundation.ID(*value) {
		return nil, inconsistent(errors.New("persisted optional memory id is invalid"))
	}
	return &parsed, nil
}

func optionalTime(value *time.Time) *time.Time {
	if value == nil {
		return nil
	}
	copy := value.UTC().Truncate(time.Microsecond)
	return &copy
}

func optionalIDValue(value *foundation.ID) any {
	if value == nil {
		return nil
	}
	return string(*value)
}

func optionalTimeValue(value *time.Time) any {
	if value == nil {
		return nil
	}
	return value.UTC().Truncate(time.Microsecond)
}

func optionalPrincipalKind(value *domain.Principal) any {
	if value == nil {
		return nil
	}
	return string(value.Kind)
}

func optionalPrincipalID(value *domain.Principal) any {
	if value == nil {
		return nil
	}
	return string(value.ID)
}

func sameMemory(left, right domain.Memory) bool { return reflect.DeepEqual(left, right) }

func samePrincipal(left, right domain.Principal) bool {
	return left.Kind == right.Kind && left.ID == right.ID
}

func validID(value foundation.ID) bool {
	parsed, err := foundation.ParseID(string(value))
	return err == nil && parsed == value
}

func validHash(value string) bool {
	if len(value) != 64 || value != strings.ToLower(value) {
		return false
	}
	for _, character := range value {
		if character < '0' || character > '9' {
			if character < 'a' || character > 'f' {
				return false
			}
		}
	}
	return true
}

func validCommandType(value memoryapp.CommandType) bool {
	return value == memoryapp.CommandCreateCandidate || value == memoryapp.CommandConfirm || value == memoryapp.CommandUpdate ||
		value == memoryapp.CommandPause || value == memoryapp.CommandResume || value == memoryapp.CommandDelete
}

func validCommandVersion(commandType memoryapp.CommandType, expected, version int64) bool {
	if commandType == memoryapp.CommandCreateCandidate {
		return expected == 0 && version == 1
	}
	return expected > 0 && version == expected+1
}
