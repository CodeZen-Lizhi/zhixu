package application

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/CodeZen-Lizhi/zhixu/internal/changecontrol/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
)

// RevisionMergeInput is the text-only port used by Change Control.  Concrete
// Git/merge implementations stay in platform adapters and cannot leak their
// command or marker types into the application layer.
type RevisionMergeDocuments struct {
	Base     []byte
	Current  []byte
	Proposed []byte
}

type RevisionMergeConflict struct {
	Ordinal  int
	Current  []byte
	Base     []byte
	Proposed []byte
}

type RevisionMergeResult struct {
	Candidate []byte
	Conflicts []RevisionMergeConflict
	Algorithm string
	Contract  string
}

// RevisionMergeEngine is the narrow application port for deterministic text
// merging.  It deliberately accepts bytes and returns bounded facts only.
type RevisionMergeEngine interface {
	Merge(context.Context, RevisionMergeDocuments) (RevisionMergeResult, error)
}

// RevisionWorkflowCancellation binds one old Revision to the Workflow run
// that must reach a terminal state before it can be superseded.
type RevisionWorkflowCancellation struct {
	WorkspaceID    foundation.ID
	ProposalID     foundation.ID
	RevisionID     foundation.ID
	WorkflowRunID  foundation.ID
	IdempotencyKey string
}

// RevisionWorkflowCancellationResult reports whether the owner observed a
// terminal run. A false result means a durable cancellation request is active.
type RevisionWorkflowCancellationResult struct {
	Terminal bool
}

// RevisionWorkflowCanceller keeps Workflow control types out of Change
// Control while leaving cancellation ownership in the Workflow application.
type RevisionWorkflowCanceller interface {
	RequestCancellation(context.Context, RevisionWorkflowCancellation) (RevisionWorkflowCancellationResult, error)
}

// RevisionWorkflowCancellerFunc adapts a function at composition and in tests.
type RevisionWorkflowCancellerFunc func(context.Context, RevisionWorkflowCancellation) (RevisionWorkflowCancellationResult, error)

func (fn RevisionWorkflowCancellerFunc) RequestCancellation(ctx context.Context, command RevisionWorkflowCancellation) (RevisionWorkflowCancellationResult, error) {
	return fn(ctx, command)
}

// RevisionMergeEngineFunc adapts a function to RevisionMergeEngine in tests
// and composition roots without introducing a second implementation path.
type RevisionMergeEngineFunc func(context.Context, RevisionMergeDocuments) (RevisionMergeResult, error)

func (fn RevisionMergeEngineFunc) Merge(ctx context.Context, input RevisionMergeDocuments) (RevisionMergeResult, error) {
	return fn(ctx, input)
}

// RevisionPreviewResult is the stable result returned by the preview command.
type RevisionPreviewResult struct {
	Preview          domain.RevisionMergePreview
	WorkspaceID      foundation.ID
	SourceRevisionNo int
	SourceChangeHash string
	TargetPath       string
	TargetMode       domain.TargetMode
	BaseContent      string
	CurrentContent   string
	ProposedContent  string
}

// AppendRevisionCommand is the HTTP/application command.  The service fills
// CurrentContent, ChangeHash and the new Revision ID from server facts.
type AppendRevisionCommand struct {
	WorkspaceID             foundation.ID
	ProposalID              foundation.ID
	IdempotencyKey          string
	ExpectedProposalVersion int64
	SourceRevisionID        foundation.ID
	SourceChangeHash        string
	ExpectedCurrentHash     string
	PreviewFingerprint      string
	MergeAlgorithm          string
	MergeAlgorithmVersion   string
	FinalContent            string
	EvidenceSummary         string
	Risk                    string
	RollbackPlan            string
	ResolvedConflictIDs     []string
}

// AppendRevisionResult is returned after the repository committed the new
// immutable Revision, or after an exact idempotent replay.
type AppendRevisionResult struct {
	Proposal domain.Proposal
	Revision domain.Revision
	Replayed bool
}

// UnresolvedRevisionConflicts carries only the bounded conflict identifiers
// that a client must acknowledge before appending a revision.
type UnresolvedRevisionConflicts struct {
	ConflictIDs []string
}

func (e *UnresolvedRevisionConflicts) Error() string {
	return "proposal revision has unresolved conflicts"
}

// SetRevisionMergeEngine installs the process-wide merge capability for this
// service.  The capability is optional during the expand window; endpoints
// fail closed with a stable dependency error when it is absent.
func (s *Service) SetRevisionMergeEngine(engine RevisionMergeEngine) {
	s.mergeEngine = engine
}

// SetRevisionWorkflowCanceller installs the Workflow-owned cancellation port.
func (s *Service) SetRevisionWorkflowCanceller(canceller RevisionWorkflowCanceller) {
	s.revisionWorkflowCanceller = canceller
}

// RevisionMergeAvailable reports whether the production conformance probe
// installed the fixed merge engine for this process.
func (s *Service) RevisionMergeAvailable() bool {
	return s != nil && s.mergeEngine != nil
}

// PreviewProposalRevision reads all three authoritative documents and runs a
// side-effect-free merge.  The caller supplies only identity/version facts.
func (s *Service) PreviewProposalRevision(ctx context.Context, input domain.RevisionMergeInput) (RevisionPreviewResult, error) {
	if err := domain.ValidateRevisionMergeInput(input); err != nil {
		return RevisionPreviewResult{}, foundation.NewError(foundation.ErrorInvalidInput, "PROPOSAL_MERGE_CONTENT_INVALID", false, err)
	}
	if s.mergeEngine == nil {
		return RevisionPreviewResult{}, foundation.NewError(foundation.ErrorDependencyUnavailable, "PROPOSAL_MERGE_ENGINE_UNAVAILABLE", true, errors.New("revision merge engine is unavailable"))
	}
	proposal, err := s.repo.GetProposal(ctx, input.ProposalID)
	if err != nil {
		return RevisionPreviewResult{}, err
	}
	if proposal.ID != input.ProposalID || proposal.WorkspaceID != input.WorkspaceID {
		return RevisionPreviewResult{}, foundation.NewError(foundation.ErrorVersionConflict, "PROPOSAL_REVISION_STALE", false, domain.ErrProposalRevisionStale)
	}
	if proposal.Revision.ProposalID != proposal.ID {
		return RevisionPreviewResult{}, foundation.NewError(foundation.ErrorConsistencyViolation, "PROPOSAL_REVISION_SOURCE_INVALID", false, errors.New("source revision is not bound to its proposal"))
	}
	if err := validateRevisionSource(proposal, input.SourceRevisionID, input.SourceChangeHash, input.ExpectedProposalVersion); err != nil {
		return RevisionPreviewResult{}, err
	}
	if len([]byte(proposal.Revision.Content)) > int(domain.ProposalRevisionMaxBytes) {
		return RevisionPreviewResult{}, foundation.NewError(foundation.ErrorInvalidInput, "PROPOSAL_REVISION_INPUT_TOO_LARGE", false, domain.ErrProposalRevisionInputTooLarge)
	}
	current, currentHash, err := s.readRevisionCurrent(ctx, proposal)
	if err != nil {
		return RevisionPreviewResult{}, err
	}
	if input.ExpectedCurrentHash != "" && !strings.EqualFold(input.ExpectedCurrentHash, currentHash) {
		return RevisionPreviewResult{}, foundation.NewError(foundation.ErrorVersionConflict, "PROPOSAL_REVISION_STALE", false, &HashConflict{Expected: input.ExpectedCurrentHash, Current: currentHash})
	}
	base, baseHash, err := s.resolveRevisionBase(ctx, proposal, current, currentHash)
	if err != nil {
		return RevisionPreviewResult{}, err
	}
	if len(base) > int(domain.ProposalRevisionMaxBytes) || len(current) > int(domain.ProposalRevisionMaxBytes) {
		return RevisionPreviewResult{}, foundation.NewError(foundation.ErrorInvalidInput, "PROPOSAL_REVISION_INPUT_TOO_LARGE", false, domain.ErrProposalRevisionInputTooLarge)
	}
	merged, err := s.mergeEngine.Merge(ctx, RevisionMergeDocuments{Base: cloneBytes(base), Current: cloneBytes(current), Proposed: []byte(proposal.Revision.Content)})
	if err != nil {
		return RevisionPreviewResult{}, err
	}
	if len(merged.Candidate) > int(domain.ProposalRevisionMaxBytes) {
		return RevisionPreviewResult{}, foundation.NewError(foundation.ErrorVersionConflict, "PROPOSAL_MERGE_RESULT_TOO_LARGE", false, errors.New("merge candidate exceeds the revision limit"))
	}
	if err := validateRevisionMergeResult(merged); err != nil {
		return RevisionPreviewResult{}, err
	}
	preview := buildRevisionPreview(proposal, input.SourceRevisionID, baseHash, currentHash, merged)
	return RevisionPreviewResult{
		Preview: preview, WorkspaceID: proposal.WorkspaceID, SourceRevisionNo: proposal.Revision.RevisionNo,
		SourceChangeHash: strings.ToLower(proposal.Revision.ChangeHash), TargetPath: proposal.Revision.TargetPath,
		TargetMode: domain.NormalizeTargetMode(proposal.Revision.TargetMode), BaseContent: string(base),
		CurrentContent: string(current), ProposedContent: proposal.Revision.Content,
	}, nil
}

func validateRevisionMergeResult(merged RevisionMergeResult) error {
	if merged.Algorithm != domain.ProposalRevisionMergeAlgorithm || merged.Contract != domain.ProposalRevisionMergeAlgorithmVersion ||
		!utf8.Valid(merged.Candidate) || bytes.IndexByte(merged.Candidate, 0) >= 0 ||
		domain.ContainsProposalRevisionMergeMarker(string(merged.Candidate)) || len(merged.Conflicts) > 1024 {
		return foundation.NewError(foundation.ErrorDependencyUnavailable, "PROPOSAL_MERGE_ENGINE_OUTPUT_INVALID", false, errors.New("merge engine returned an invalid contract result"))
	}
	for index, conflict := range merged.Conflicts {
		if conflict.Ordinal != index+1 {
			return foundation.NewError(foundation.ErrorDependencyUnavailable, "PROPOSAL_MERGE_ENGINE_OUTPUT_INVALID", false, errors.New("merge engine returned an invalid conflict order"))
		}
		for _, content := range [][]byte{conflict.Base, conflict.Current, conflict.Proposed} {
			if len(content) > int(domain.ProposalRevisionMaxBytes) || !utf8.Valid(content) || bytes.IndexByte(content, 0) >= 0 {
				return foundation.NewError(foundation.ErrorDependencyUnavailable, "PROPOSAL_MERGE_ENGINE_OUTPUT_INVALID", false, errors.New("merge engine returned invalid conflict content"))
			}
		}
	}
	return nil
}

// AppendProposalRevision re-runs the preview against a fresh Workspace read,
// verifies the exact conflict acknowledgement, then delegates the atomic
// append to the PostgreSQL repository.
func (s *Service) AppendProposalRevision(ctx context.Context, command AppendRevisionCommand) (AppendRevisionResult, error) {
	if command.WorkspaceID == "" || command.ProposalID == "" {
		return AppendRevisionResult{}, foundation.NewError(foundation.ErrorInvalidInput, "PROPOSAL_MERGE_CONTENT_INVALID", false, errors.New("proposal and workspace are required"))
	}
	repository, ok := s.repo.(domain.ProposalRevisionRepository)
	if !ok {
		return AppendRevisionResult{}, foundation.NewError(foundation.ErrorDependencyUnavailable, "PROPOSAL_REVISION_REPOSITORY_UNAVAILABLE", true, errors.New("revision repository is unavailable"))
	}
	appendCommand := domain.AppendProposalRevision{
		WorkspaceID: command.WorkspaceID, ProposalID: command.ProposalID, IdempotencyKey: command.IdempotencyKey,
		ExpectedProposalVersion: command.ExpectedProposalVersion, SourceRevisionID: command.SourceRevisionID,
		SourceChangeHash: command.SourceChangeHash, ExpectedCurrentHash: command.ExpectedCurrentHash,
		PreviewFingerprint: command.PreviewFingerprint, MergeAlgorithm: command.MergeAlgorithm, MergeAlgorithmVersion: command.MergeAlgorithmVersion,
		FinalContent: command.FinalContent, EvidenceSummary: command.EvidenceSummary, Risk: command.Risk, RollbackPlan: command.RollbackPlan,
		ResolvedConflictIDs: append([]string(nil), command.ResolvedConflictIDs...),
	}
	if err := domain.ValidateAppendProposalRevisionRequest(appendCommand); err != nil {
		return AppendRevisionResult{}, appendRevisionRequestError(err)
	}
	appendCommand.RequestHash = domain.ComputeRevisionRequestHash(appendCommand)
	replayed, found, err := repository.LookupRevisionCommandReceipt(ctx, domain.RevisionCommandReceiptLookup{
		WorkspaceID: appendCommand.WorkspaceID, ProposalID: appendCommand.ProposalID,
		IdempotencyKey: appendCommand.IdempotencyKey, RequestHash: appendCommand.RequestHash,
	})
	if err != nil {
		return AppendRevisionResult{}, err
	}
	if found {
		return AppendRevisionResult{Proposal: replayed.Proposal, Revision: replayed.Revision, Replayed: true}, nil
	}
	proposal, err := s.repo.GetProposal(ctx, appendCommand.ProposalID)
	if err != nil {
		return AppendRevisionResult{}, err
	}
	if proposal.ID != appendCommand.ProposalID || proposal.WorkspaceID != appendCommand.WorkspaceID {
		return AppendRevisionResult{}, foundation.NewError(foundation.ErrorVersionConflict, "PROPOSAL_REVISION_STALE", false, domain.ErrProposalRevisionStale)
	}
	if err := validateRevisionSource(proposal, appendCommand.SourceRevisionID, appendCommand.SourceChangeHash, appendCommand.ExpectedProposalVersion); err != nil {
		return AppendRevisionResult{}, err
	}
	workflowTerminal, err := s.ensureRevisionWorkflowCancellation(ctx, proposal)
	if err != nil {
		return AppendRevisionResult{}, err
	}
	if !workflowTerminal {
		return AppendRevisionResult{}, revisionWorkflowActiveError(errors.New("source revision workflow cancellation is pending"))
	}
	input := domain.RevisionMergeInput{
		WorkspaceID: appendCommand.WorkspaceID, ProposalID: appendCommand.ProposalID,
		SourceRevisionID: appendCommand.SourceRevisionID, SourceChangeHash: appendCommand.SourceChangeHash,
		ExpectedProposalVersion: appendCommand.ExpectedProposalVersion, ExpectedCurrentHash: appendCommand.ExpectedCurrentHash,
	}
	previewResult, err := s.PreviewProposalRevision(ctx, input)
	if err != nil {
		return AppendRevisionResult{}, err
	}
	preview := previewResult.Preview
	if appendCommand.PreviewFingerprint != preview.Fingerprint || appendCommand.MergeAlgorithm != preview.Algorithm || appendCommand.MergeAlgorithmVersion != preview.AlgorithmVersion {
		return AppendRevisionResult{}, foundation.NewError(foundation.ErrorVersionConflict, "PROPOSAL_REVISION_STALE", false, domain.ErrProposalRevisionStale)
	}
	if err := validateResolvedConflicts(preview.Conflicts, appendCommand.ResolvedConflictIDs); err != nil {
		return AppendRevisionResult{}, err
	}
	if appendCommand.ExpectedCurrentHash != preview.CurrentHash {
		return AppendRevisionResult{}, foundation.NewError(foundation.ErrorVersionConflict, "PROPOSAL_REVISION_STALE", false, domain.ErrProposalRevisionStale)
	}
	// The preview read is not an atomic filesystem transaction. Re-read the
	// authoritative target immediately before the database append and refuse to
	// persist a base that changed during preview or user confirmation.
	latestCurrent, latestCurrentHash, err := s.readCurrentContent(ctx, previewResult.WorkspaceID, previewResult.TargetPath)
	if err != nil {
		return AppendRevisionResult{}, err
	}
	if latestCurrentHash != preview.CurrentHash || !bytes.Equal(latestCurrent, []byte(previewResult.CurrentContent)) {
		return AppendRevisionResult{}, foundation.NewError(foundation.ErrorVersionConflict, "PROPOSAL_REVISION_STALE", false, domain.ErrProposalRevisionStale)
	}
	newID, err := s.ids.New()
	if err != nil {
		return AppendRevisionResult{}, err
	}
	appendCommand.ExpectedCurrentHash = preview.CurrentHash
	appendCommand.CurrentContent = string(latestCurrent)
	appendCommand.PreviewFingerprint = preview.Fingerprint
	appendCommand.MergeAlgorithm = preview.Algorithm
	appendCommand.MergeAlgorithmVersion = preview.AlgorithmVersion
	appendCommand.NewRevisionID = newID
	appendCommand.CreatedAt = s.clock.Now()
	result, err := repository.AppendRevision(ctx, appendCommand)
	if err != nil {
		return AppendRevisionResult{}, err
	}
	return AppendRevisionResult{Proposal: result.Proposal, Revision: result.Revision, Replayed: result.Replayed}, nil
}

func (s *Service) ensureRevisionWorkflowCancellation(ctx context.Context, proposal domain.Proposal) (bool, error) {
	if proposal.Status != domain.StatusNeedsRevision || proposal.WorkflowRunID == nil {
		return true, nil
	}
	switch strings.TrimSpace(proposal.WorkflowRunStatus) {
	case "failed", "cancelled", "succeeded":
		return true, nil
	}
	if s == nil || s.revisionWorkflowCanceller == nil || *proposal.WorkflowRunID == "" {
		return false, revisionWorkflowCancellationUnavailable(errors.New("workflow cancellation owner is unavailable"))
	}
	command := RevisionWorkflowCancellation{
		WorkspaceID: proposal.WorkspaceID, ProposalID: proposal.ID, RevisionID: proposal.Revision.ID,
		WorkflowRunID: *proposal.WorkflowRunID,
	}
	command.IdempotencyKey = revisionWorkflowCancellationKey(command)
	result, err := s.revisionWorkflowCanceller.RequestCancellation(ctx, command)
	if err != nil {
		return false, revisionWorkflowCancellationUnavailable(err)
	}
	return result.Terminal, nil
}

func revisionWorkflowCancellationKey(command RevisionWorkflowCancellation) string {
	hash := sha256.Sum256([]byte("proposal-revision-workflow-cancel/v1\n" + string(command.WorkspaceID) + "\n" + string(command.ProposalID) + "\n" + string(command.RevisionID) + "\n" + string(command.WorkflowRunID)))
	return "proposal-revision-cancel:" + hex.EncodeToString(hash[:])
}

func revisionWorkflowActiveError(cause error) error {
	return foundation.NewError(foundation.ErrorVersionConflict, "PROPOSAL_REVISION_WORKFLOW_ACTIVE", false, cause)
}

func revisionWorkflowCancellationUnavailable(cause error) error {
	return foundation.NewError(foundation.ErrorDependencyUnavailable, "PROPOSAL_REVISION_WORKFLOW_CANCEL_UNAVAILABLE", true, cause)
}

func appendRevisionRequestError(err error) error {
	if errors.Is(err, domain.ErrProposalRevisionInputTooLarge) {
		return foundation.NewError(foundation.ErrorInvalidInput, "PROPOSAL_REVISION_INPUT_TOO_LARGE", false, err)
	}
	return foundation.NewError(foundation.ErrorInvalidInput, "PROPOSAL_MERGE_CONTENT_INVALID", false, err)
}

func validateRevisionSource(proposal domain.Proposal, sourceID foundation.ID, sourceHash string, expectedVersion int64) error {
	if domain.NormalizeProposalType(proposal.Type) != domain.ProposalTypeFilePatch || domain.NormalizeTargetMode(proposal.Revision.TargetMode) != domain.TargetModeReplace || (proposal.Status != domain.StatusReady && proposal.Status != domain.StatusNeedsRevision) {
		return foundation.NewError(foundation.ErrorVersionConflict, "PROPOSAL_REVISION_NOT_EDITABLE", false, domain.ErrProposalRevisionNotEditable)
	}
	if proposal.Version != expectedVersion || proposal.CurrentRevisionID == "" || proposal.CurrentRevisionID != sourceID || proposal.Revision.ID != sourceID || !strings.EqualFold(proposal.Revision.ChangeHash, sourceHash) {
		return foundation.NewError(foundation.ErrorVersionConflict, "PROPOSAL_REVISION_STALE", false, domain.ErrProposalRevisionStale)
	}
	return nil
}

func (s *Service) readRevisionCurrent(ctx context.Context, proposal domain.Proposal) ([]byte, string, error) {
	return s.readCurrentContent(ctx, proposal.WorkspaceID, proposal.Revision.TargetPath)
}

func (s *Service) readCurrentContent(ctx context.Context, workspaceID foundation.ID, targetPath string) ([]byte, string, error) {
	reader, ok := s.targets.(CurrentContentReader)
	if !ok {
		return nil, "", foundation.NewError(foundation.ErrorDependencyUnavailable, "PROPOSAL_CURRENT_CONTENT_UNAVAILABLE", true, errors.New("current content reader is unavailable"))
	}
	content, hash, err := reader.CurrentContent(ctx, workspaceID, targetPath, MaxProposalCurrentContentBytes)
	if err != nil {
		var classified *foundation.Error
		if errors.As(err, &classified) && classified.Code == "PROPOSAL_CURRENT_CONTENT_TOO_LARGE" {
			return nil, "", foundation.NewError(foundation.ErrorInvalidInput, "PROPOSAL_REVISION_INPUT_TOO_LARGE", false, err)
		}
		return nil, "", err
	}
	if !domain.ValidHash(hash) || domain.RawContentHash(string(content)) != strings.ToLower(hash) {
		return nil, "", foundation.NewError(foundation.ErrorConsistencyViolation, "PROPOSAL_CURRENT_CONTENT_HASH_INVALID", false, errors.New("current content hash is inconsistent"))
	}
	return content, strings.ToLower(hash), nil
}

func (s *Service) resolveRevisionBase(ctx context.Context, proposal domain.Proposal, current []byte, currentHash string) ([]byte, string, error) {
	repository, ok := s.repo.(domain.ProposalRevisionRepository)
	if !ok {
		return nil, "", foundation.NewError(foundation.ErrorDependencyUnavailable, "PROPOSAL_REVISION_REPOSITORY_UNAVAILABLE", true, errors.New("revision repository is unavailable"))
	}
	snapshot, found, err := repository.GetRevisionBaseSnapshot(ctx, proposal.ID, proposal.Revision.ID)
	if err != nil {
		return nil, "", err
	}
	if found {
		if snapshot.ProposalID != proposal.ID || snapshot.RevisionID != proposal.Revision.ID || snapshot.BaseHash != strings.ToLower(proposal.Revision.BaseHash) || domain.ValidateRevisionBaseSnapshot(snapshot) != nil {
			return nil, "", foundation.NewError(foundation.ErrorConsistencyViolation, "PROPOSAL_BASE_SNAPSHOT_INVALID", false, errors.New("base snapshot binding does not match the source revision"))
		}
		return []byte(snapshot.Content), snapshot.BaseHash, nil
	}
	if strings.EqualFold(currentHash, proposal.Revision.BaseHash) {
		return cloneBytes(current), strings.ToLower(proposal.Revision.BaseHash), nil
	}
	return nil, "", foundation.NewError(foundation.ErrorVersionConflict, "PROPOSAL_BASE_SNAPSHOT_UNAVAILABLE", false, errors.New("legacy revision has no trustworthy base snapshot"))
}

func buildRevisionPreview(proposal domain.Proposal, sourceID foundation.ID, baseHash, currentHash string, merged RevisionMergeResult) domain.RevisionMergePreview {
	proposedHash := domain.RawContentHash(proposal.Revision.Content)
	conflicts := make([]domain.RevisionConflict, 0, len(merged.Conflicts))
	for _, conflict := range merged.Conflicts {
		conflicts = append(conflicts, domain.RevisionConflict{
			ID:      revisionConflictID(merged.Contract, baseHash, currentHash, proposedHash, conflict.Ordinal, conflict.Current, conflict.Base, conflict.Proposed),
			Ordinal: conflict.Ordinal,
			Current: string(conflict.Current), Base: string(conflict.Base), Proposed: string(conflict.Proposed),
		})
	}
	fingerprint := revisionFingerprint(proposal, sourceID, baseHash, currentHash, proposedHash, merged, conflicts)
	return domain.RevisionMergePreview{ProposalID: proposal.ID, SourceRevisionID: sourceID, ProposalVersion: proposal.Version, BaseHash: baseHash, CurrentHash: currentHash, ProposedHash: proposedHash, Candidate: string(merged.Candidate), Conflicts: conflicts, Algorithm: merged.Algorithm, AlgorithmVersion: merged.Contract, Fingerprint: fingerprint}
}

func revisionConflictID(contract, baseHash, currentHash, proposedHash string, ordinal int, current, base, proposed []byte) string {
	h := sha256.New()
	fmt.Fprintf(h, "proposal-conflict/v1\ncontract:%s\nbase:%s\ncurrent:%s\nproposed:%s\nordinal:%d\n", contract, baseHash, currentHash, proposedHash, ordinal)
	h.Write(current)
	h.Write([]byte{0})
	h.Write(base)
	h.Write([]byte{0})
	h.Write(proposed)
	return hex.EncodeToString(h.Sum(nil))
}

func revisionFingerprint(proposal domain.Proposal, sourceID foundation.ID, baseHash, currentHash, proposedHash string, merged RevisionMergeResult, conflicts []domain.RevisionConflict) string {
	h := sha256.New()
	fmt.Fprintf(h, "proposal-merge-fingerprint/v1\nworkspace:%s\nproposal:%s\nversion:%d\nsource:%s\nsource-change:%s\ntarget:%s\nmode:%s\nbase:%s\ncurrent:%s\nproposed:%s\nalgorithm:%s\ncontract:%s\n",
		proposal.WorkspaceID, proposal.ID, proposal.Version, sourceID, proposal.Revision.ChangeHash, proposal.Revision.TargetPath,
		domain.NormalizeTargetMode(proposal.Revision.TargetMode), baseHash, currentHash, proposedHash, merged.Algorithm, merged.Contract)
	for _, conflict := range conflicts {
		fmt.Fprintf(h, "conflict:%s:%d\n", conflict.ID, conflict.Ordinal)
	}
	h.Write(merged.Candidate)
	return hex.EncodeToString(h.Sum(nil))
}

func validateResolvedConflicts(conflicts []domain.RevisionConflict, resolved []string) error {
	want := make([]string, 0, len(conflicts))
	for _, conflict := range conflicts {
		want = append(want, conflict.ID)
	}
	got := append([]string(nil), resolved...)
	sort.Strings(want)
	sort.Strings(got)
	if len(want) != len(got) {
		return unresolvedRevisionConflictsError(want)
	}
	for index := range want {
		if want[index] != got[index] {
			return unresolvedRevisionConflictsError(want)
		}
	}
	return nil
}

func unresolvedRevisionConflictsError(conflictIDs []string) error {
	return foundation.NewError(
		foundation.ErrorVersionConflict,
		"PROPOSAL_REVISION_CONFLICTS_UNRESOLVED",
		false,
		&UnresolvedRevisionConflicts{ConflictIDs: append([]string(nil), conflictIDs...)},
	)
}

func cloneBytes(value []byte) []byte {
	return append([]byte(nil), value...)
}
