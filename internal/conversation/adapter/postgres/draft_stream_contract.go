package postgres

import (
	"context"
	"errors"
	agentapplication "github.com/CodeZen-Lizhi/zhixu/internal/agent/application"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	"strings"
	"unicode/utf8"
)

const draftSessionColumns = `
	id::text,workspace_id::text,answer_id::text,workflow_run_id::text,node_run_id::text,node_attempt_id::text,
	attempt_no,lease_owner,generation,status,next_sequence,total_bytes,expires_at,created_at,updated_at`

func scanDraftSession(row scanner) (agentapplication.DraftStreamSession, error) {
	var session agentapplication.DraftStreamSession
	var id, workspaceID, answerID, runID, nodeID, attemptID, status string
	if err := row.Scan(&id, &workspaceID, &answerID, &runID, &nodeID, &attemptID, &session.Binding.AttemptNo, &session.Binding.LeaseOwner,
		&session.Generation, &status, &session.NextSeq, &session.TotalBytes, &session.ExpiresAt, &session.CreatedAt, &session.UpdatedAt); err != nil {
		return agentapplication.DraftStreamSession{}, err
	}
	session.ID, session.Binding.WorkspaceID, session.Binding.AnswerID = foundation.ID(id), foundation.ID(workspaceID), foundation.ID(answerID)
	session.Binding.WorkflowRunID, session.Binding.NodeRunID, session.Binding.NodeAttemptID = foundation.ID(runID), foundation.ID(nodeID), foundation.ID(attemptID)
	session.Status = agentapplication.DraftStreamStatus(status)
	return session, nil
}

func validateDraftBegin(ctx context.Context, command agentapplication.BeginDraftStreamCommand) error {
	if ctx == nil || !validDraftBinding(command.DraftStreamBinding) || command.TTL <= 0 || command.TTL > agentapplication.MaxDraftStreamTTL {
		return invalid(ErrorCodeDraftStreamInvalid, errors.New("draft begin command is invalid"))
	}
	return nil
}

func validateDraftAppend(ctx context.Context, command agentapplication.DraftStreamAppendCommand) error {
	if ctx == nil || !validDraftBinding(command.Binding) || !validDraftID(command.SessionID) || command.Content == "" ||
		len(command.Content) > agentapplication.MaxAnswerStreamChunkBytes || !utf8.ValidString(command.Content) {
		return invalid(ErrorCodeDraftStreamInvalid, errors.New("draft append command is invalid"))
	}
	return nil
}

func validateDraftTransition(ctx context.Context, command agentapplication.DraftStreamTransitionCommand) error {
	if ctx == nil || !validDraftBinding(command.Binding) || !validDraftID(command.SessionID) {
		return invalid(ErrorCodeDraftStreamInvalid, errors.New("draft transition command is invalid"))
	}
	return nil
}

func validDraftBinding(binding agentapplication.DraftStreamBinding) bool {
	for _, id := range []foundation.ID{binding.WorkspaceID, binding.AnswerID, binding.WorkflowRunID, binding.NodeRunID, binding.NodeAttemptID} {
		if !validDraftID(id) {
			return false
		}
	}
	return binding.AttemptNo > 0 && strings.TrimSpace(binding.LeaseOwner) == binding.LeaseOwner && binding.LeaseOwner != "" && len(binding.LeaseOwner) <= 256
}

func validDraftID(id foundation.ID) bool {
	_, err := foundation.ParseID(string(id))
	return err == nil
}

func sameDraftBinding(left, right agentapplication.DraftStreamBinding) bool {
	return left == right
}

func transitionSatisfied(current, target agentapplication.DraftStreamStatus) bool {
	return current == target || (current == agentapplication.DraftStreamDegraded && target == agentapplication.DraftStreamCompleted)
}

func allowedDraftTransition(current, target agentapplication.DraftStreamStatus) bool {
	switch target {
	case agentapplication.DraftStreamCompleted:
		return current == agentapplication.DraftStreamActive
	case agentapplication.DraftStreamDegraded:
		return current == agentapplication.DraftStreamActive
	case agentapplication.DraftStreamAborted:
		return current == agentapplication.DraftStreamActive || current == agentapplication.DraftStreamCompleted || current == agentapplication.DraftStreamDegraded
	default:
		return false
	}
}
