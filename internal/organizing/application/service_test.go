package application

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	"github.com/CodeZen-Lizhi/zhixu/internal/organizing/domain"
)

const (
	organizingTestWorkspaceID foundation.ID = "10000000-0000-4000-8000-000000000001"
	organizingTestTemplateID  foundation.ID = "20000000-0000-4000-8000-000000000001"
	organizingTestRevisionID  foundation.ID = "30000000-0000-4000-8000-000000000001"
)

func TestSearchMaterialsCanonicalizesAndValidatesOwnerResults(t *testing.T) {
	t.Parallel()
	claimID := foundation.ID("40000000-0000-4000-8000-000000000001")
	provider := &organizingMaterialSearchProvider{items: []MaterialSearchHit{{
		Selector: MaterialSelector{Kind: domain.MaterialClaim, ClaimID: claimID}, Title: "可恢复的幂等流程", Availability: domain.MaterialAvailable,
	}}}
	service := organizingTestService(t, &organizingTemplateReplayRepository{}, &organizingCountingIDs{}, time.Date(2026, 8, 3, 8, 0, 0, 0, time.UTC))
	service.dependencies.Searches = provider
	page, err := service.SearchMaterials(context.Background(), MaterialSearchQuery{
		WorkspaceID: organizingTestWorkspaceID, Query: "  幂等恢复  ", Kind: domain.MaterialClaim,
	})
	if err != nil {
		t.Fatalf("SearchMaterials() error = %v", err)
	}
	if provider.query.WorkspaceID != organizingTestWorkspaceID || provider.query.Query != "幂等恢复" || provider.query.Limit != DefaultMaterialSearchLimit ||
		page.WorkspaceID != organizingTestWorkspaceID || page.Query != "幂等恢复" || len(page.Items) != 1 || page.Items[0].Selector.ClaimID != claimID {
		t.Fatalf("provider query=%+v page=%+v", provider.query, page)
	}
	provider.items = append(provider.items, provider.items[0])
	_, err = service.SearchMaterials(context.Background(), MaterialSearchQuery{
		WorkspaceID: organizingTestWorkspaceID, Query: "幂等恢复", Kind: domain.MaterialClaim, Limit: 2,
	})
	assertOrganizingApplicationError(t, err, foundation.ErrorConsistencyViolation, ErrorCodeResultInvalid)
}

func TestSearchMaterialsFailsClosedWithoutProvider(t *testing.T) {
	t.Parallel()
	service := organizingTestService(t, &organizingTemplateReplayRepository{}, &organizingCountingIDs{}, time.Date(2026, 8, 3, 8, 0, 0, 0, time.UTC))
	_, err := service.SearchMaterials(context.Background(), MaterialSearchQuery{
		WorkspaceID: organizingTestWorkspaceID, Query: "幂等", Kind: domain.MaterialClaim, Limit: 1,
	})
	assertOrganizingApplicationError(t, err, foundation.ErrorDependencyUnavailable, ErrorCodeDependencyUnavailable)
}

func TestCreateTemplateReplaysBeforeAllocatingIDs(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 8, 3, 8, 0, 0, 0, time.UTC)
	detail := organizingTestTemplateDetail(t, now)
	templates := &organizingTemplateReplayRepository{replay: TemplateResult{Detail: detail, Replayed: true}}
	ids := &organizingCountingIDs{}
	service := organizingTestService(t, templates, ids, now)

	result, err := service.CreateTemplate(context.Background(), CreateTemplateCommand{
		WorkspaceID:    organizingTestWorkspaceID,
		Declaration:    detail.Revision.Declaration,
		IdempotencyKey: "create-template-replay",
	})
	if err != nil {
		t.Fatalf("CreateTemplate() error = %v", err)
	}
	if !result.Replayed || result.Detail.Template.ID != detail.Template.ID {
		t.Fatalf("CreateTemplate() result = %+v", result)
	}
	if ids.calls != 0 {
		t.Fatalf("ID generator calls = %d, want 0 for exact replay", ids.calls)
	}
	if templates.createCalls != 0 || templates.getCalls != 0 {
		t.Fatalf("template owner calls: create=%d get=%d, want 0 for exact replay", templates.createCalls, templates.getCalls)
	}
}

func TestReviseTemplateReplaysBeforeReadingCurrentTemplateOrAllocatingID(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 8, 3, 8, 0, 0, 0, time.UTC)
	detail := organizingTestTemplateDetail(t, now)
	templates := &organizingTemplateReplayRepository{replay: TemplateResult{Detail: detail, Replayed: true}}
	ids := &organizingCountingIDs{}
	service := organizingTestService(t, templates, ids, now)

	result, err := service.ReviseTemplate(context.Background(), ReviseTemplateCommand{
		WorkspaceID:     organizingTestWorkspaceID,
		TemplateID:      detail.Template.ID,
		ExpectedVersion: detail.Template.Version,
		Declaration:     detail.Revision.Declaration,
		IdempotencyKey:  "revise-template-replay",
	})
	if err != nil {
		t.Fatalf("ReviseTemplate() error = %v", err)
	}
	if !result.Replayed || result.Detail.Revision.ID != detail.Revision.ID {
		t.Fatalf("ReviseTemplate() result = %+v", result)
	}
	if ids.calls != 0 {
		t.Fatalf("ID generator calls = %d, want 0 for exact replay", ids.calls)
	}
	if templates.getCalls != 0 || templates.reviseCalls != 0 {
		t.Fatalf("template owner calls: get=%d revise=%d, want 0 for exact replay", templates.getCalls, templates.reviseCalls)
	}
}

func TestCreateDraftLeavesTemplateRevisionUnselected(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 8, 3, 8, 15, 0, 0, time.UTC)
	draftID := foundation.ID("70000000-0000-4000-8000-000000000010")
	drafts := &organizingRecordingDraftRepository{}
	service := organizingTestServiceWithDrafts(t, drafts, &organizingTemplateReplayRepository{}, organizingFixedIDs{id: draftID}, now)

	result, err := service.CreateDraft(context.Background(), CreateDraftCommand{
		WorkspaceID: organizingTestWorkspaceID, Intent: "new topic", IdempotencyKey: "create-draft-without-template",
	})
	if err != nil {
		t.Fatalf("CreateDraft() error = %v", err)
	}
	if result.Draft.TemplateRevisionID != "" || drafts.createRecord.Draft.TemplateRevisionID != "" {
		t.Fatalf("CreateDraft() selected a Template Revision: result=%q record=%q", result.Draft.TemplateRevisionID, drafts.createRecord.Draft.TemplateRevisionID)
	}
	if drafts.createCalls != 1 || result.Draft.ID != draftID {
		t.Fatalf("CreateDraft() calls=%d result=%+v", drafts.createCalls, result)
	}
}

func TestUpdateDraftPersistsValidatedTemplateRevision(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 8, 3, 8, 20, 0, 0, time.UTC)
	draftID := foundation.ID("70000000-0000-4000-8000-000000000011")
	draft, err := domain.NewDraft(draftID, organizingTestWorkspaceID, "old topic", now.Add(-time.Minute))
	if err != nil {
		t.Fatalf("NewDraft() error = %v", err)
	}
	detail := organizingTestTemplateDetail(t, now)
	drafts := &organizingRecordingDraftRepository{draft: draft}
	templates := &organizingTemplateLookupRepository{detail: detail}
	ids := &organizingCountingIDs{}
	service := organizingTestServiceWithDrafts(t, drafts, templates, ids, now)

	command := UpdateDraftCommand{
		WorkspaceID: organizingTestWorkspaceID, DraftID: draftID, ExpectedVersion: draft.Version,
		Intent: "  normalized topic  ", TemplateRevisionID: detail.Revision.ID, IdempotencyKey: "update-draft-template",
	}
	result, err := service.UpdateDraft(context.Background(), command)
	if err != nil {
		t.Fatalf("UpdateDraft() error = %v", err)
	}
	if result.Draft.Intent != "normalized topic" || result.Draft.TemplateRevisionID != detail.Revision.ID || result.Draft.Version != draft.Version+1 {
		t.Fatalf("UpdateDraft() result = %+v", result)
	}
	if templates.revisionCalls != 1 || templates.workspaceID != command.WorkspaceID || templates.revisionID != command.TemplateRevisionID {
		t.Fatalf("GetTemplateRevision() calls=%d workspace=%q revision=%q", templates.revisionCalls, templates.workspaceID, templates.revisionID)
	}
	if drafts.updateCalls != 1 || drafts.updateRecord.TemplateRevisionID != command.TemplateRevisionID || drafts.updateRecord.Intent != "normalized topic" {
		t.Fatalf("DraftRepository.UpdateDraft() calls=%d record=%+v", drafts.updateCalls, drafts.updateRecord)
	}
	expectedHash, err := requestHash(CommandUpdateDraft, command.WorkspaceID, command.DraftID, command.ExpectedVersion, struct {
		Intent             string        `json:"intent"`
		TemplateRevisionID foundation.ID `json:"template_revision_id"`
	}{"normalized topic", command.TemplateRevisionID})
	if err != nil {
		t.Fatalf("requestHash() error = %v", err)
	}
	if drafts.updateRecord.Binding.RequestHash != expectedHash {
		t.Fatalf("UpdateDraft() request hash = %q, want %q", drafts.updateRecord.Binding.RequestHash, expectedHash)
	}
	if ids.calls != 0 {
		t.Fatalf("ID generator calls = %d, want 0", ids.calls)
	}
}

func TestUpdateDraftReplaysBeforeTemplateLookup(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 8, 3, 8, 22, 0, 0, time.UTC)
	draftID := foundation.ID("70000000-0000-4000-8000-000000000013")
	draft, err := domain.NewDraft(draftID, organizingTestWorkspaceID, "old topic", now.Add(-time.Minute))
	if err != nil {
		t.Fatalf("NewDraft() error = %v", err)
	}
	draft, err = domain.UpdateDraft(draft, draft.Version, "replayed topic", organizingTestRevisionID, now)
	if err != nil {
		t.Fatalf("domain.UpdateDraft() error = %v", err)
	}
	drafts := &organizingRecordingDraftRepository{
		findResult: DraftResult{Draft: draft, Replayed: true},
		findFound:  true,
	}
	templates := &organizingTemplateLookupRepository{err: errors.New("template lookup must not run for an exact replay")}
	ids := &organizingCountingIDs{}
	service := organizingTestServiceWithDrafts(t, drafts, templates, ids, now)

	result, err := service.UpdateDraft(context.Background(), UpdateDraftCommand{
		WorkspaceID: organizingTestWorkspaceID, DraftID: draftID, ExpectedVersion: 1,
		Intent: "replayed topic", TemplateRevisionID: organizingTestRevisionID, IdempotencyKey: "update-draft-replay",
	})
	if err != nil {
		t.Fatalf("UpdateDraft() error = %v", err)
	}
	if !result.Replayed || result.Draft.TemplateRevisionID != organizingTestRevisionID {
		t.Fatalf("UpdateDraft() result = %+v", result)
	}
	if drafts.findCalls != 1 || drafts.updateCalls != 0 || templates.revisionCalls != 0 || ids.calls != 0 {
		t.Fatalf("dependency calls: find=%d update=%d template=%d ids=%d", drafts.findCalls, drafts.updateCalls,
			templates.revisionCalls, ids.calls)
	}
}

func TestUpdateDraftRejectsInvisibleOrInvalidTemplateRevision(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 8, 3, 8, 25, 0, 0, time.UTC)
	valid := organizingTestTemplateDetail(t, now)
	foreignWorkspaceID := foundation.ID("10000000-0000-4000-8000-000000000002")
	otherRevisionID := foundation.ID("30000000-0000-4000-8000-000000000002")

	foreign := valid
	foreign.Template.WorkspaceID = foreignWorkspaceID
	foreign.Revision.WorkspaceID = foreignWorkspaceID
	invalidWorkspaceBinding := valid
	invalidWorkspaceBinding.Revision.WorkspaceID = foreignWorkspaceID
	wrongRevision := valid
	wrongRevision.Template.CurrentRevisionID = otherRevisionID
	wrongRevision.Revision.ID = otherRevisionID
	missing := foundation.NewError(foundation.ErrorNotFound, ErrorCodeNotFound, false, errors.New("template revision not found"))

	tests := []struct {
		name      string
		detail    TemplateDetail
		lookupErr error
		wantKind  foundation.ErrorKind
		wantCode  string
	}{
		{name: "nonexistent", lookupErr: missing, wantKind: foundation.ErrorNotFound, wantCode: ErrorCodeNotFound},
		{name: "cross workspace", detail: foreign, wantKind: foundation.ErrorConsistencyViolation, wantCode: ErrorCodeResultInvalid},
		{name: "invalid workspace binding", detail: invalidWorkspaceBinding, wantKind: foundation.ErrorConsistencyViolation, wantCode: ErrorCodeResultInvalid},
		{name: "wrong exact revision", detail: wrongRevision, wantKind: foundation.ErrorConsistencyViolation, wantCode: ErrorCodeResultInvalid},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			drafts := &organizingRecordingDraftRepository{}
			templates := &organizingTemplateLookupRepository{detail: test.detail, err: test.lookupErr}
			service := organizingTestServiceWithDrafts(t, drafts, templates, &organizingCountingIDs{}, now)

			_, err := service.UpdateDraft(context.Background(), UpdateDraftCommand{
				WorkspaceID: organizingTestWorkspaceID,
				DraftID:     "70000000-0000-4000-8000-000000000012", ExpectedVersion: 1,
				Intent: "topic", TemplateRevisionID: organizingTestRevisionID, IdempotencyKey: "invalid-template-" + strings.ReplaceAll(test.name, " ", "-"),
			})
			assertOrganizingApplicationError(t, err, test.wantKind, test.wantCode)
			if drafts.updateCalls != 0 {
				t.Fatalf("DraftRepository.UpdateDraft() calls = %d, want 0", drafts.updateCalls)
			}
			if templates.revisionCalls != 1 {
				t.Fatalf("GetTemplateRevision() calls = %d, want 1", templates.revisionCalls)
			}
		})
	}
}

func TestConfirmDraftRejectsUnselectedOrMismatchedTemplateBeforeDependencies(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 8, 3, 8, 30, 0, 0, time.UTC)
	draftID := foundation.ID("70000000-0000-4000-8000-000000000013")
	otherRevisionID := foundation.ID("30000000-0000-4000-8000-000000000003")

	tests := []struct {
		name               string
		selectedRevisionID foundation.ID
	}{
		{name: "unselected"},
		{name: "mismatched", selectedRevisionID: otherRevisionID},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			draft, err := domain.NewDraft(draftID, organizingTestWorkspaceID, "topic", now.Add(-time.Minute))
			if err != nil {
				t.Fatalf("NewDraft() error = %v", err)
			}
			if test.selectedRevisionID != "" {
				draft, err = domain.UpdateDraft(draft, draft.Version, draft.Intent, test.selectedRevisionID, now.Add(-time.Second))
				if err != nil {
					t.Fatalf("domain.UpdateDraft() error = %v", err)
				}
			}
			drafts := &organizingRecordingDraftRepository{draft: draft}
			templates := &organizingTemplateLookupRepository{}
			materials := &organizingCountingMaterialResolver{}
			ids := &organizingCountingIDs{}
			service, err := NewService(Dependencies{
				Drafts: drafts, Templates: templates, Starts: organizingNoopStartRepository{}, Materials: materials,
				IDs: ids, Clock: foundation.FixedClock{Value: now},
			})
			if err != nil {
				t.Fatalf("NewService() error = %v", err)
			}

			_, err = service.ConfirmDraft(context.Background(), ConfirmCommand{
				WorkspaceID: organizingTestWorkspaceID, DraftID: draftID, ExpectedVersion: draft.Version,
				TemplateRevisionID: organizingTestRevisionID, IdempotencyKey: "confirm-" + test.name,
			})
			assertOrganizingApplicationError(t, err, foundation.ErrorVersionConflict, domain.ErrorCodeVersionConflict)
			if templates.revisionCalls != 0 || materials.freezeCalls != 0 || ids.calls != 0 {
				t.Fatalf("dependency calls: template=%d freeze=%d ids=%d, want all 0", templates.revisionCalls, materials.freezeCalls, ids.calls)
			}
		})
	}
}

func TestConfirmDraftPassesMaterialResolverAsTransactionFence(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 8, 3, 8, 35, 0, 0, time.UTC)
	draftID := foundation.ID("70000000-0000-4000-8000-000000000014")
	draft, err := domain.NewDraft(draftID, organizingTestWorkspaceID, "transaction fence", now.Add(-3*time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	draft, err = domain.UpdateDraft(draft, draft.Version, draft.Intent, organizingTestRevisionID, now.Add(-2*time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	reference := organizingTestSourceRef("50000000-0000-4000-8000-000000000010", "")
	draft, err = domain.ReplaceMaterials(draft, draft.Version, []domain.DraftMaterial{{
		ID: "60000000-0000-4000-8000-000000000010", Ref: reference, Title: "frozen source",
		Reasons: []domain.SuggestionReasonCode{domain.ReasonUserAdded}, Origin: domain.MaterialOriginUser,
		Availability: domain.MaterialAvailable, Score: 1, Selected: true,
	}}, now.Add(-time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	confirmErr := errors.New("recorded confirmation")
	drafts := &organizingRecordingDraftRepository{draft: draft, confirmErr: confirmErr}
	templates := &organizingTemplateLookupRepository{detail: organizingTestTemplateDetail(t, now)}
	materials := &organizingFixedMaterialResolver{frozen: []domain.MaterialRef{reference}}
	ids := &organizingSequenceIDs{values: []foundation.ID{
		"70000000-0000-4000-8000-000000000015",
		"70000000-0000-4000-8000-000000000016",
		"70000000-0000-4000-8000-000000000017",
	}}
	service, err := NewService(Dependencies{
		Drafts: drafts, Templates: templates, Starts: organizingNoopStartRepository{}, Materials: materials,
		IDs: ids, Clock: foundation.FixedClock{Value: now},
	})
	if err != nil {
		t.Fatal(err)
	}

	_, err = service.ConfirmDraft(context.Background(), ConfirmCommand{
		WorkspaceID: organizingTestWorkspaceID, DraftID: draftID, ExpectedVersion: draft.Version,
		TemplateRevisionID: organizingTestRevisionID, IdempotencyKey: "confirm-transaction-fence",
	})
	if !errors.Is(err, confirmErr) {
		t.Fatalf("ConfirmDraft() error = %v, want %v", err, confirmErr)
	}
	if drafts.confirmCalls != 1 || drafts.confirmRecord.Fence != materials {
		t.Fatalf("ConfirmDraft() calls=%d fence=%T, want resolver %T", drafts.confirmCalls, drafts.confirmRecord.Fence, materials)
	}
	if len(drafts.confirmRecord.FrozenMaterials) != 1 ||
		drafts.confirmRecord.FrozenMaterials[0].SourceVersionID != reference.SourceVersionID ||
		drafts.confirmRecord.FrozenMaterials[0].ContentHash != reference.ContentHash || materials.freezeCalls != 1 {
		t.Fatalf("ConfirmDraft() frozen=%#v freeze_calls=%d", drafts.confirmRecord.FrozenMaterials, materials.freezeCalls)
	}
}

func TestValidateFrozenMaterialsRejectsUnselectedCollectionExpansion(t *testing.T) {
	t.Parallel()
	collectionA := foundation.ID("40000000-0000-4000-8000-000000000001")
	collectionB := foundation.ID("40000000-0000-4000-8000-000000000002")
	selected := []domain.MaterialRef{organizingTestCollectionRef(collectionA)}
	frozen := []domain.MaterialRef{
		organizingTestCollectionRef(collectionA),
		organizingTestSourceRef("50000000-0000-4000-8000-000000000001", collectionB),
	}

	err := validateFrozenMaterials(selected, frozen, organizingTestExpansionPolicy())
	if err == nil {
		t.Fatal("validateFrozenMaterials() error = nil, want unselected collection expansion rejection")
	}
}

func TestRunProjectionValidatesDispatchShape(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 8, 3, 8, 28, 0, 0, time.UTC)
	workspaceID := organizingTestWorkspaceID
	snapshotID := foundation.ID("70000000-0000-4000-8000-000000000020")
	binding := domain.RunBinding{
		ID: "70000000-0000-4000-8000-000000000021", WorkspaceID: workspaceID, SnapshotID: snapshotID,
		WorkflowRunID: "70000000-0000-4000-8000-000000000022", DefinitionKey: "organizing.topic-article",
		DefinitionVersion: 1, CreatedAt: now,
	}
	result := domain.RunResult{
		ID: "70000000-0000-4000-8000-000000000023", WorkspaceID: workspaceID,
		RunBindingID: binding.ID, SnapshotID: snapshotID, WorkflowRunID: binding.WorkflowRunID,
		NodeRunID: "70000000-0000-4000-8000-000000000024", Kind: domain.ResultArtifact,
		ResultRef: "70000000-0000-4000-8000-000000000025", ResultHash: strings.Repeat("a", 64), CreatedAt: now,
	}
	cases := []struct {
		name       string
		projection RunProjection
		valid      bool
	}{
		{name: "pending", projection: RunProjection{WorkspaceID: workspaceID, SnapshotID: snapshotID, Status: StartPending, UpdatedAt: now}, valid: true},
		{name: "pending retry", projection: RunProjection{WorkspaceID: workspaceID, SnapshotID: snapshotID, Status: StartPending, AttemptCount: 2, LastErrorCode: "TEMPORARY_FAILURE", UpdatedAt: now}, valid: true},
		{name: "poisoned", projection: RunProjection{WorkspaceID: workspaceID, SnapshotID: snapshotID, Status: StartPoisoned, AttemptCount: 3, LastErrorCode: "PERMANENT_FAILURE", UpdatedAt: now}, valid: true},
		{name: "started", projection: RunProjection{WorkspaceID: workspaceID, SnapshotID: snapshotID, Status: StartStarted, AttemptCount: 2, LastErrorCode: "TEMPORARY_FAILURE", UpdatedAt: now, Binding: &binding}, valid: true},
		{name: "started with result", projection: RunProjection{WorkspaceID: workspaceID, SnapshotID: snapshotID, Status: StartStarted, AttemptCount: 2, UpdatedAt: now, Binding: &binding, Result: &result}, valid: true},
		{name: "pending with binding", projection: RunProjection{WorkspaceID: workspaceID, SnapshotID: snapshotID, Status: StartPending, UpdatedAt: now, Binding: &binding}},
		{name: "poisoned without error", projection: RunProjection{WorkspaceID: workspaceID, SnapshotID: snapshotID, Status: StartPoisoned, AttemptCount: 1, UpdatedAt: now}},
		{name: "started without binding", projection: RunProjection{WorkspaceID: workspaceID, SnapshotID: snapshotID, Status: StartStarted, AttemptCount: 1, UpdatedAt: now}},
	}
	for _, test := range cases {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			if got := test.projection.Valid(); got != test.valid {
				t.Fatalf("RunProjection.Valid() = %v, want %v: %+v", got, test.valid, test.projection)
			}
		})
	}
}

func TestValidateFrozenMaterialsRejectsExtraMaterialWithoutCollectionOrigin(t *testing.T) {
	t.Parallel()
	collectionID := foundation.ID("40000000-0000-4000-8000-000000000001")
	selected := []domain.MaterialRef{organizingTestCollectionRef(collectionID)}
	frozen := []domain.MaterialRef{
		organizingTestCollectionRef(collectionID),
		organizingTestSourceRef("50000000-0000-4000-8000-000000000001", ""),
	}

	err := validateFrozenMaterials(selected, frozen, organizingTestExpansionPolicy())
	if err == nil {
		t.Fatal("validateFrozenMaterials() error = nil, want unselected material rejection")
	}
}

func TestValidateFrozenMaterialsAcceptsSelectedCollectionExpansion(t *testing.T) {
	t.Parallel()
	collectionID := foundation.ID("40000000-0000-4000-8000-000000000001")
	selected := []domain.MaterialRef{organizingTestCollectionRef(collectionID)}
	frozen := []domain.MaterialRef{
		organizingTestCollectionRef(collectionID),
		organizingTestSourceRef("50000000-0000-4000-8000-000000000001", collectionID),
	}

	if err := validateFrozenMaterials(selected, frozen, organizingTestExpansionPolicy()); err != nil {
		t.Fatalf("validateFrozenMaterials() error = %v", err)
	}
}

func TestMaterialFromCandidateOwnsExplicitAddOrigin(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 8, 3, 8, 0, 0, 0, time.UTC)
	candidate := MaterialCandidate{
		Reference:    organizingTestSourceRef("50000000-0000-4000-8000-000000000001", ""),
		Title:        "用户选择的材料",
		Reasons:      []domain.SuggestionReasonCode{domain.ReasonHybridMatch},
		Origin:       domain.MaterialOriginSuggested,
		Availability: domain.MaterialAvailable,
		Score:        0.8,
	}

	material, err := materialFromCandidate(
		"60000000-0000-4000-8000-000000000001",
		"70000000-0000-4000-8000-000000000001",
		0, candidate, domain.MaterialOriginUser, now,
	)
	if err != nil {
		t.Fatalf("materialFromCandidate() error = %v", err)
	}
	if material.Origin != domain.MaterialOriginUser || !material.Selected || len(material.Reasons) != 1 || material.Reasons[0] != domain.ReasonUserAdded {
		t.Fatalf("materialFromCandidate() origin=%q reasons=%v", material.Origin, material.Reasons)
	}
}

func TestMaterialFromCandidateOwnsSuggestionOrigin(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 8, 3, 8, 0, 0, 0, time.UTC)
	candidate := MaterialCandidate{
		Reference:    organizingTestSourceRef("50000000-0000-4000-8000-000000000001", ""),
		Title:        "系统建议的材料",
		Reasons:      []domain.SuggestionReasonCode{domain.ReasonUserAdded, domain.ReasonHybridMatch},
		Origin:       domain.MaterialOriginUser,
		Availability: domain.MaterialAvailable,
		Score:        0.8,
	}

	material, err := materialFromCandidate(
		"60000000-0000-4000-8000-000000000001",
		"70000000-0000-4000-8000-000000000001",
		0, candidate, domain.MaterialOriginSuggested, now,
	)
	if err != nil {
		t.Fatalf("materialFromCandidate() error = %v", err)
	}
	if material.Origin != domain.MaterialOriginSuggested || material.Selected || len(material.Reasons) != 1 || material.Reasons[0] != domain.ReasonHybridMatch {
		t.Fatalf("materialFromCandidate() origin=%q reasons=%v", material.Origin, material.Reasons)
	}

	candidate.Reasons = []domain.SuggestionReasonCode{domain.ReasonUserAdded}
	if _, err := materialFromCandidate(
		"60000000-0000-4000-8000-000000000002",
		"70000000-0000-4000-8000-000000000001",
		0, candidate, domain.MaterialOriginSuggested, now,
	); err == nil {
		t.Fatal("materialFromCandidate() error = nil, want forged user-only suggestion rejection")
	}
}

func TestSetMaterialSelectionPersistsExplicitAuthorization(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 8, 3, 8, 10, 0, 0, time.UTC)
	draftID := foundation.ID("70000000-0000-4000-8000-000000000020")
	materialID := foundation.ID("60000000-0000-4000-8000-000000000020")
	draft, err := domain.NewDraft(draftID, organizingTestWorkspaceID, "explicit material authorization", now.Add(-time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	draft, err = domain.ReplaceMaterials(draft, draft.Version, []domain.DraftMaterial{{
		ID: materialID, Ref: organizingTestSourceRef("50000000-0000-4000-8000-000000000020", ""),
		Title: "suggested source", Reasons: []domain.SuggestionReasonCode{domain.ReasonHybridMatch},
		Origin: domain.MaterialOriginSuggested, Availability: domain.MaterialAvailable, Score: 0.9,
	}}, now.Add(-time.Second))
	if err != nil {
		t.Fatal(err)
	}
	drafts := &organizingRecordingDraftRepository{draft: draft}
	service := organizingTestServiceWithDrafts(t, drafts, &organizingTemplateReplayRepository{}, &organizingCountingIDs{}, now)

	result, err := service.SetMaterialSelection(context.Background(), SetMaterialSelectionCommand{
		WorkspaceID: organizingTestWorkspaceID, DraftID: draftID, MaterialID: materialID,
		ExpectedVersion: draft.Version, Selected: true, IdempotencyKey: "select-material",
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Draft.Version != draft.Version+1 || len(result.Draft.Materials) != 1 || !result.Draft.Materials[0].Selected {
		t.Fatalf("SetMaterialSelection() result=%+v", result)
	}
	if drafts.replaceCalls != 1 || drafts.replaceRecord.Binding.CommandType != CommandSetMaterialSelection ||
		drafts.replaceRecord.Binding.ExpectedVersion != draft.Version {
		t.Fatalf("ReplaceMaterials() calls=%d record=%+v", drafts.replaceCalls, drafts.replaceRecord)
	}
}

func TestSuggestKeepsUserMaterialAndSkipsDuplicateSuggestion(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 8, 3, 8, 12, 0, 0, time.UTC)
	draftID := foundation.ID("70000000-0000-4000-8000-000000000021")
	materialID := foundation.ID("60000000-0000-4000-8000-000000000021")
	reference := organizingTestSourceRef("50000000-0000-4000-8000-000000000021", "")
	draft, err := domain.NewDraft(draftID, organizingTestWorkspaceID, "deduplicate suggestions", now.Add(-time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	draft, err = domain.ReplaceMaterials(draft, draft.Version, []domain.DraftMaterial{{
		ID: materialID, Ref: reference, Title: "user source", Reasons: []domain.SuggestionReasonCode{domain.ReasonUserAdded},
		Origin: domain.MaterialOriginUser, Availability: domain.MaterialAvailable, Score: 1, Selected: true,
	}}, now.Add(-time.Second))
	if err != nil {
		t.Fatal(err)
	}
	drafts := &organizingRecordingDraftRepository{draft: draft}
	service, err := NewService(Dependencies{
		Drafts: drafts, Templates: &organizingTemplateReplayRepository{}, Starts: organizingNoopStartRepository{},
		Materials: organizingNoopMaterialResolver{}, Suggestions: organizingFixedSuggestionProvider{candidates: []MaterialCandidate{{
			Reference: reference, Title: "suggested source", Reasons: []domain.SuggestionReasonCode{domain.ReasonHybridMatch},
			Availability: domain.MaterialAvailable, Score: 0.9,
		}}}, IDs: &organizingSequenceIDs{values: []foundation.ID{"60000000-0000-4000-8000-000000000022"}},
		Clock: foundation.FixedClock{Value: now},
	})
	if err != nil {
		t.Fatal(err)
	}

	result, err := service.Suggest(context.Background(), SuggestCommand{
		WorkspaceID: organizingTestWorkspaceID, DraftID: draftID, ExpectedVersion: draft.Version,
		Limit: 10, IdempotencyKey: "suggest-deduplicate-user-material",
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Draft.Materials) != 1 || result.Draft.Materials[0].ID != materialID ||
		result.Draft.Materials[0].Origin != domain.MaterialOriginUser || !result.Draft.Materials[0].Selected {
		t.Fatalf("Suggest() materials = %#v", result.Draft.Materials)
	}
}

func organizingTestExpansionPolicy() domain.MaterialPolicy {
	return domain.MaterialPolicy{
		AllowedKinds: []domain.MaterialKind{domain.MaterialSmartCollection, domain.MaterialSourceVersion},
		MinMaterials: 1,
		MaxMaterials: 2,
	}
}

func organizingTestCollectionRef(id foundation.ID) domain.MaterialRef {
	return domain.MaterialRef{
		Kind:              domain.MaterialSmartCollection,
		CollectionID:      id,
		Version:           1,
		QueryHash:         strings.Repeat("a", 64),
		ReadModelRevision: strings.Repeat("b", 64),
	}
}

func organizingTestSourceRef(id, originCollectionID foundation.ID) domain.MaterialRef {
	return domain.MaterialRef{
		Kind:               domain.MaterialSourceVersion,
		SourceVersionID:    id,
		OriginCollectionID: originCollectionID,
		ContentHash:        strings.Repeat("c", 64),
	}
}

func organizingTestTemplateDetail(t *testing.T, now time.Time) TemplateDetail {
	t.Helper()
	_, builtInRevisions, err := domain.BuiltInTemplates(now)
	if err != nil {
		t.Fatalf("BuiltInTemplates() error = %v", err)
	}
	template, revision, err := domain.CloneBuiltIn(
		builtInRevisions[0], organizingTestTemplateID, organizingTestRevisionID,
		organizingTestWorkspaceID, "工作区模板", now,
	)
	if err != nil {
		t.Fatalf("CloneBuiltIn() error = %v", err)
	}
	return TemplateDetail{Template: template, Revision: revision}
}

func organizingTestService(t *testing.T, templates TemplateRepository, ids foundation.IDGenerator, now time.Time) *Service {
	t.Helper()
	return organizingTestServiceWithDrafts(t, organizingNoopDraftRepository{}, templates, ids, now)
}

func organizingTestServiceWithDrafts(t *testing.T, drafts DraftRepository, templates TemplateRepository, ids foundation.IDGenerator, now time.Time) *Service {
	t.Helper()
	service, err := NewService(Dependencies{
		Drafts:    drafts,
		Templates: templates,
		Starts:    organizingNoopStartRepository{},
		Materials: organizingNoopMaterialResolver{},
		IDs:       ids,
		Clock:     foundation.FixedClock{Value: now},
	})
	if err != nil {
		t.Fatalf("NewService() error = %v", err)
	}
	return service
}

func assertOrganizingApplicationError(t *testing.T, err error, kind foundation.ErrorKind, code string) {
	t.Helper()
	var classified *foundation.Error
	if !errors.As(err, &classified) || classified.Kind != kind || classified.Code != code {
		t.Fatalf("error = %v, want kind=%s code=%s", err, kind, code)
	}
}

type organizingFixedIDs struct{ id foundation.ID }

func (ids organizingFixedIDs) New() (foundation.ID, error) { return ids.id, nil }

type organizingCountingIDs struct{ calls int }

func (ids *organizingCountingIDs) New() (foundation.ID, error) {
	ids.calls++
	return "", errors.New("ID generation must not run for an exact replay")
}

type organizingSequenceIDs struct {
	values []foundation.ID
	index  int
}

func (ids *organizingSequenceIDs) New() (foundation.ID, error) {
	if ids.index >= len(ids.values) {
		return "", errors.New("ID sequence exhausted")
	}
	value := ids.values[ids.index]
	ids.index++
	return value, nil
}

type organizingTemplateReplayRepository struct {
	replay      TemplateResult
	getCalls    int
	createCalls int
	reviseCalls int
}

type organizingTemplateLookupRepository struct {
	organizingTemplateReplayRepository
	detail        TemplateDetail
	err           error
	workspaceID   foundation.ID
	revisionID    foundation.ID
	revisionCalls int
}

func (repository *organizingTemplateLookupRepository) GetTemplateRevision(_ context.Context, workspaceID, revisionID foundation.ID) (TemplateDetail, error) {
	repository.revisionCalls++
	repository.workspaceID = workspaceID
	repository.revisionID = revisionID
	return repository.detail, repository.err
}

func (repository *organizingTemplateReplayRepository) FindTemplateCommand(context.Context, CommandBinding) (TemplateResult, bool, error) {
	return repository.replay, true, nil
}

func (repository *organizingTemplateReplayRepository) CreateTemplate(context.Context, CreateTemplateRecord) (TemplateResult, error) {
	repository.createCalls++
	return TemplateResult{}, errors.New("CreateTemplate must not run for an exact replay")
}

func (repository *organizingTemplateReplayRepository) ReviseTemplate(context.Context, ReviseTemplateRecord) (TemplateResult, error) {
	repository.reviseCalls++
	return TemplateResult{}, errors.New("ReviseTemplate must not run for an exact replay")
}

func (repository *organizingTemplateReplayRepository) GetTemplate(context.Context, foundation.ID, foundation.ID) (TemplateDetail, error) {
	repository.getCalls++
	return TemplateDetail{}, errors.New("GetTemplate must not run for an exact replay")
}

func (repository *organizingTemplateReplayRepository) GetTemplateRevision(context.Context, foundation.ID, foundation.ID) (TemplateDetail, error) {
	return TemplateDetail{}, errors.New("unexpected GetTemplateRevision")
}

func (repository *organizingTemplateReplayRepository) ListTemplates(context.Context, TemplateListQuery) (TemplatePage, error) {
	return TemplatePage{}, errors.New("unexpected ListTemplates")
}

type organizingNoopDraftRepository struct{}

type organizingRecordingDraftRepository struct {
	organizingNoopDraftRepository
	draft         domain.Draft
	findResult    DraftResult
	findFound     bool
	findErr       error
	createRecord  CreateDraftRecord
	updateRecord  UpdateDraftRecord
	replaceRecord ReplaceMaterialsRecord
	confirmRecord ConfirmRecord
	confirmErr    error
	findCalls     int
	createCalls   int
	updateCalls   int
	replaceCalls  int
	confirmCalls  int
}

func (repository *organizingRecordingDraftRepository) FindDraftCommand(_ context.Context, binding CommandBinding) (DraftResult, bool, error) {
	repository.findCalls++
	return repository.findResult, repository.findFound, repository.findErr
}

func (repository *organizingRecordingDraftRepository) FindConfirmCommand(context.Context, CommandBinding) (ConfirmResult, bool, error) {
	return ConfirmResult{}, false, nil
}

func (repository *organizingRecordingDraftRepository) GetDraft(context.Context, foundation.ID, foundation.ID) (domain.Draft, error) {
	return repository.draft, nil
}

func (repository *organizingRecordingDraftRepository) CreateDraft(_ context.Context, record CreateDraftRecord) (DraftResult, error) {
	repository.createCalls++
	repository.createRecord = record
	repository.draft = record.Draft
	return DraftResult{Draft: record.Draft}, nil
}

func (repository *organizingRecordingDraftRepository) UpdateDraft(_ context.Context, record UpdateDraftRecord) (DraftResult, error) {
	repository.updateCalls++
	repository.updateRecord = record
	next, err := domain.UpdateDraft(repository.draft, record.Binding.ExpectedVersion, record.Intent, record.TemplateRevisionID, record.UpdatedAt)
	if err != nil {
		return DraftResult{}, err
	}
	repository.draft = next
	return DraftResult{Draft: next}, nil
}

func (repository *organizingRecordingDraftRepository) ReplaceMaterials(_ context.Context, record ReplaceMaterialsRecord) (DraftResult, error) {
	repository.replaceCalls++
	repository.replaceRecord = record
	next, err := domain.ReplaceMaterials(repository.draft, record.Binding.ExpectedVersion, record.Materials, record.UpdatedAt)
	if err != nil {
		return DraftResult{}, err
	}
	repository.draft = next
	return DraftResult{Draft: next}, nil
}

func (repository *organizingRecordingDraftRepository) ConfirmDraft(_ context.Context, record ConfirmRecord) (ConfirmResult, error) {
	repository.confirmCalls++
	repository.confirmRecord = record
	return ConfirmResult{}, repository.confirmErr
}

func (organizingNoopDraftRepository) FindDraftCommand(context.Context, CommandBinding) (DraftResult, bool, error) {
	return DraftResult{}, false, errors.New("unexpected FindDraftCommand")
}
func (organizingNoopDraftRepository) FindConfirmCommand(context.Context, CommandBinding) (ConfirmResult, bool, error) {
	return ConfirmResult{}, false, errors.New("unexpected FindConfirmCommand")
}
func (organizingNoopDraftRepository) CreateDraft(context.Context, CreateDraftRecord) (DraftResult, error) {
	return DraftResult{}, errors.New("unexpected CreateDraft")
}
func (organizingNoopDraftRepository) GetDraft(context.Context, foundation.ID, foundation.ID) (domain.Draft, error) {
	return domain.Draft{}, errors.New("unexpected GetDraft")
}
func (organizingNoopDraftRepository) UpdateDraft(context.Context, UpdateDraftRecord) (DraftResult, error) {
	return DraftResult{}, errors.New("unexpected UpdateDraft")
}
func (organizingNoopDraftRepository) ReplaceMaterials(context.Context, ReplaceMaterialsRecord) (DraftResult, error) {
	return DraftResult{}, errors.New("unexpected ReplaceMaterials")
}
func (organizingNoopDraftRepository) ConfirmDraft(context.Context, ConfirmRecord) (ConfirmResult, error) {
	return ConfirmResult{}, errors.New("unexpected ConfirmDraft")
}
func (organizingNoopDraftRepository) GetSnapshot(context.Context, foundation.ID, foundation.ID) (domain.Snapshot, error) {
	return domain.Snapshot{}, errors.New("unexpected GetSnapshot")
}

type organizingNoopMaterialResolver struct{}

type organizingMaterialSearchProvider struct {
	query MaterialSearchQuery
	items []MaterialSearchHit
}

func (provider *organizingMaterialSearchProvider) SearchMaterials(_ context.Context, query MaterialSearchQuery) ([]MaterialSearchHit, error) {
	provider.query = query
	return append([]MaterialSearchHit(nil), provider.items...), nil
}

type organizingFixedSuggestionProvider struct {
	candidates []MaterialCandidate
}

func (provider organizingFixedSuggestionProvider) Suggest(context.Context, SuggestionQuery) ([]MaterialCandidate, error) {
	return append([]MaterialCandidate(nil), provider.candidates...), nil
}

type organizingFixedMaterialResolver struct {
	frozen      []domain.MaterialRef
	freezeCalls int
}

func (resolver *organizingFixedMaterialResolver) Resolve(context.Context, foundation.ID, []MaterialSelector) ([]MaterialCandidate, error) {
	return nil, errors.New("unexpected Resolve")
}

func (resolver *organizingFixedMaterialResolver) Freeze(context.Context, foundation.ID, []domain.MaterialRef) ([]domain.MaterialRef, error) {
	resolver.freezeCalls++
	return append([]domain.MaterialRef(nil), resolver.frozen...), nil
}

func (*organizingFixedMaterialResolver) VerifyFrozenScoped(context.Context, foundation.TransactionScope, foundation.ID, []domain.MaterialRef) error {
	return errors.New("VerifyFrozen must run inside the repository transaction")
}

type organizingCountingMaterialResolver struct {
	resolveCalls int
	freezeCalls  int
}

func (resolver *organizingCountingMaterialResolver) Resolve(context.Context, foundation.ID, []MaterialSelector) ([]MaterialCandidate, error) {
	resolver.resolveCalls++
	return nil, errors.New("unexpected Resolve")
}

func (resolver *organizingCountingMaterialResolver) Freeze(context.Context, foundation.ID, []domain.MaterialRef) ([]domain.MaterialRef, error) {
	resolver.freezeCalls++
	return nil, errors.New("unexpected Freeze")
}
func (resolver *organizingCountingMaterialResolver) VerifyFrozenScoped(context.Context, foundation.TransactionScope, foundation.ID, []domain.MaterialRef) error {
	return errors.New("unexpected VerifyFrozen")
}

func (organizingNoopMaterialResolver) Resolve(context.Context, foundation.ID, []MaterialSelector) ([]MaterialCandidate, error) {
	return nil, errors.New("unexpected Resolve")
}
func (organizingNoopMaterialResolver) Freeze(context.Context, foundation.ID, []domain.MaterialRef) ([]domain.MaterialRef, error) {
	return nil, errors.New("unexpected Freeze")
}
func (organizingNoopMaterialResolver) VerifyFrozenScoped(context.Context, foundation.TransactionScope, foundation.ID, []domain.MaterialRef) error {
	return errors.New("unexpected VerifyFrozen")
}

type organizingNoopStartRepository struct{}

func (organizingNoopStartRepository) ClaimStart(context.Context, string, time.Duration) (StartOutboxLease, bool, error) {
	return StartOutboxLease{}, false, errors.New("unexpected ClaimStart")
}
func (organizingNoopStartRepository) CompleteStart(context.Context, CompleteStartRecord) (domain.RunBinding, bool, error) {
	return domain.RunBinding{}, false, errors.New("unexpected CompleteStart")
}
func (organizingNoopStartRepository) RetryStart(context.Context, RetryStartRecord) error {
	return errors.New("unexpected RetryStart")
}
func (organizingNoopStartRepository) PoisonStart(context.Context, PoisonStartRecord) error {
	return errors.New("unexpected PoisonStart")
}
func (organizingNoopStartRepository) GetRunProjection(context.Context, foundation.ID, foundation.ID) (RunProjection, error) {
	return RunProjection{}, errors.New("unexpected GetRunProjection")
}
func (organizingNoopStartRepository) GetRunBinding(context.Context, foundation.ID, foundation.ID) (domain.RunBinding, error) {
	return domain.RunBinding{}, errors.New("unexpected GetRunBinding")
}
func (organizingNoopStartRepository) BindRunResult(context.Context, BindRunResultRecord) (domain.RunResult, bool, error) {
	return domain.RunResult{}, false, errors.New("unexpected BindRunResult")
}
func (organizingNoopStartRepository) GetRunResult(context.Context, foundation.ID, foundation.ID) (domain.RunResult, error) {
	return domain.RunResult{}, errors.New("unexpected GetRunResult")
}
