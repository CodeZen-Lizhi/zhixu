package workflow

import (
	"context"
	"reflect"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	app "github.com/CodeZen-Lizhi/zhixu/internal/organizing/application"
	"github.com/CodeZen-Lizhi/zhixu/internal/organizing/domain"
	workflowapp "github.com/CodeZen-Lizhi/zhixu/internal/workflow/application"
)

func (e *SynthesisExecutor) prepareBodyRefreshInput(ctx context.Context, execution workflowapp.ExecutionContext, loaded SynthesisExecution) (workflowapp.ExecutionResult, error) {
	if nilScopedDependency(e.dependencies.BodyRefresh) || nilScopedDependency(e.dependencies.Anchors) {
		return workflowapp.ExecutionResult{}, synthesisInvalid("body refresh preparation is unavailable")
	}
	prepared, err := e.dependencies.BodyRefresh.PrepareSynthesisBodyRefresh(ctx, execution.WorkspaceID, loaded.BodyRefreshRequestID)
	if err != nil {
		return workflowapp.ExecutionResult{}, err
	}
	if prepared.Request.ID != loaded.BodyRefreshRequestID || prepared.Request.WorkspaceID != execution.WorkspaceID || prepared.Target.Note.ID != prepared.Request.NoteID {
		return workflowapp.ExecutionResult{}, synthesisInvalid("refresh preparation changed request identity")
	}
	// SourceReady 仅用于溯源。准入依据是受影响已发布条目的精确证据，
	// 不能使用无关引用或目标继承的引用。
	anchor := &app.SynthesisAnchorBinding{}
	snapshot := SynthesisFrozenInput{ProcessingID: loaded.Processing.ID, WorkflowRunID: execution.RunID, SourceEvent: loaded.Processing.SourceEvent,
		Notes:       []SynthesisFrozenNote{{Note: prepared.Target.Note, RevisionID: prepared.Target.Revision.ID, RevisionHash: prepared.Target.Revision.Hash, Anchor: anchor}},
		BodyRefresh: &app.SynthesisBodyRefreshBinding{Request: prepared.Request}, Sources: []domain.SynthesisSourceRef{}}
	admissions := map[domain.SynthesisSourceVersion]map[string]bool{}
	seen := map[string]bool{}
	total := 0
	appendSource := func(ref domain.SynthesisSourceRef) error {
		key, err := ref.IdentityKey()
		if err != nil {
			return err
		}
		if seen[key] {
			return nil
		}
		allowed, loadedAdmission := admissions[ref.Source]
		if !loadedAdmission {
			admission, err := e.dependencies.Anchors.ReadSynthesisAnchorAdmission(ctx, execution.WorkspaceID, prepared.Request.NoteID, ref.Source)
			if err != nil {
				return err
			}
			if admission.AnchorID == "" || len(admission.AllowedSources) == 0 {
				return workflowError(foundation.ErrorVersionConflict, "SYNTHESIS_BODY_REFRESH_SCOPE_REQUIRED", false, "refresh requires current evidence admission")
			}
			if anchor.AnchorID == "" {
				anchor.AnchorID, anchor.ScopeVersion, anchor.Scope = admission.AnchorID, admission.ScopeVersion, admission.Scope
			} else if anchor.AnchorID != admission.AnchorID || anchor.ScopeVersion != admission.ScopeVersion || !reflect.DeepEqual(anchor.Scope, admission.Scope) {
				return workflowError(foundation.ErrorVersionConflict, "SYNTHESIS_BODY_REFRESH_SCOPE_REQUIRED", false, "refresh anchor scope changed during preparation")
			}
			allowed = map[string]bool{}
			for _, admitted := range admission.AllowedSources {
				if admitted.Source != ref.Source || admitted.Validate() != nil {
					return synthesisInvalid("refresh admission returned unrelated evidence")
				}
				identity, _ := admitted.IdentityKey()
				allowed[identity] = true
			}
			admissions[ref.Source] = allowed
		}
		if !allowed[key] {
			return workflowError(foundation.ErrorVersionConflict, "SYNTHESIS_BODY_REFRESH_SCOPE_REQUIRED", false, "refresh introduces evidence outside current admission")
		}
		view, err := e.dependencies.Sources.OpenSynthesisSource(ctx, ref)
		if err != nil {
			return err
		}
		if view.Reference != ref || view.Validate(execution.WorkspaceID) != nil || view.Availability != domain.MaterialAvailable {
			return synthesisInvalid("refresh original evidence is unavailable")
		}
		total += len(view.Text)
		if total > app.MaxSynthesisSourceInputBytes || len(snapshot.Sources) >= domain.MaxSynthesisSources {
			return synthesisInvalid("refresh evidence exceeds input budget")
		}
		seen[key] = true
		snapshot.Sources = append(snapshot.Sources, ref)
		anchor.AllowedSources = append(anchor.AllowedSources, ref)
		return nil
	}
	for _, item := range prepared.Items {
		impact := item.Impact
		if _, err := domain.RefreshSynthesisPublishedItem(execution.WorkspaceID, prepared.Target.Revision.Items, impact.ItemID, item.Original, item.Updated, impact.UpstreamPublicationID, impact.PublicationID); err != nil {
			return workflowapp.ExecutionResult{}, err
		}
		snapshot.BodyRefresh.Items = append(snapshot.BodyRefresh.Items, app.SynthesisBodyRefreshItemBinding{ImpactID: impact.ID, ItemID: impact.ItemID,
			Original: domain.SynthesisBodyReference{WorkspaceID: execution.WorkspaceID, NoteID: item.Original.NoteID, RevisionID: item.Original.ID, PublicationID: impact.UpstreamPublicationID, ItemID: impact.UpstreamItemID, ProjectionHash: item.Original.Hash},
			Updated:  domain.SynthesisBodyReference{WorkspaceID: execution.WorkspaceID, NoteID: item.Updated.NoteID, RevisionID: item.Updated.ID, PublicationID: impact.PublicationID, ItemID: impact.UpstreamItemID, ProjectionHash: item.Updated.Hash}})
		for _, upstreamItem := range item.Updated.Items {
			if upstreamItem.ID == impact.UpstreamItemID {
				for _, ref := range upstreamItem.SourceReferences() {
					if err := appendSource(ref); err != nil {
						return workflowapp.ExecutionResult{}, err
					}
				}
			}
		}
	}
	if anchor.Validate(execution.WorkspaceID) != nil {
		return workflowapp.ExecutionResult{}, synthesisInvalid("refresh opened no currently admitted evidence")
	}
	snapshot.freezeSourceIdentityPromptVersions()
	snapshot.RequestHash, err = snapshot.ComputeHash()
	if err != nil {
		return workflowapp.ExecutionResult{}, err
	}
	if err := snapshot.Validate(); err != nil {
		return workflowapp.ExecutionResult{}, err
	}
	stored, err := e.dependencies.Store.FreezeSynthesisInput(ctx, execution, snapshot)
	if err != nil {
		return workflowapp.ExecutionResult{}, err
	}
	if stored.Validate() != nil || stored.BodyRefresh == nil || stored.BodyRefresh.Request.ID != loaded.BodyRefreshRequestID {
		return workflowapp.ExecutionResult{}, synthesisInvalid("refresh frozen request changed")
	}
	return synthesisReceipt(execution, loaded.Processing.ID, stored.RequestHash, nil)
}
