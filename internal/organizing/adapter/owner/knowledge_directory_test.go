package owner

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	captureapp "github.com/CodeZen-Lizhi/zhixu/internal/capture/application"
	capturedomain "github.com/CodeZen-Lizhi/zhixu/internal/capture/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	organizingapp "github.com/CodeZen-Lizhi/zhixu/internal/organizing/application"
	organizingdomain "github.com/CodeZen-Lizhi/zhixu/internal/organizing/domain"
)

func TestKnowledgeDirectoryProjectsImmutableProfileRevision(t *testing.T) {
	t.Parallel()
	workspaceID, sourceVersionID := knowledgeDirectoryID(1), knowledgeDirectoryID(2)
	spanOne, spanTwo := knowledgeDirectoryID(3), knowledgeDirectoryID(4)
	view := knowledgeDirectoryProfile(t, workspaceID, sourceVersionID, knowledgeDirectoryID(5), 10, []capturedomain.ProfilePoint{
		{Text: "Redis uses an in-memory data model.", SourceSpanIDs: []foundation.ID{spanOne}},
		{Text: "Persistence strategy depends on durability requirements.", SourceSpanIDs: []foundation.ID{spanOne, spanTwo}},
	}, []capturedomain.ProfilePoint{{Text: "A cache-aside example uses Redis.", SourceSpanIDs: []foundation.ID{spanTwo}}}, []foundation.ID{spanOne, spanTwo})
	reader := mustKnowledgeDirectoryReader(t, &knowledgeDirectoryProfileFake{views: map[foundation.ID]captureapp.ProfileView{sourceVersionID: view}})

	directory, err := reader.GetSourceKnowledgeDirectory(context.Background(), organizingapp.SourceKnowledgeDirectoryQuery{
		WorkspaceID: workspaceID, SourceVersionID: sourceVersionID,
	})
	if err != nil {
		t.Fatal(err)
	}
	if directory.Status != organizingapp.KnowledgeDirectoryAnalyzed || directory.ProfileRevisionID != view.Revision.ID ||
		directory.ParseProjectionID != view.Revision.ParseProjectionID || len(directory.Points) != 3 ||
		directory.Points[0].Locator != (organizingapp.KnowledgePointLocator{ProfileRevisionID: view.Revision.ID, Kind: organizingapp.KnowledgePointKindKnowledgePoint, Index: 0}) ||
		directory.Points[2].Locator != (organizingapp.KnowledgePointLocator{ProfileRevisionID: view.Revision.ID, Kind: organizingapp.KnowledgePointKindExample, Index: 0}) {
		t.Fatalf("directory=%#v", directory)
	}
	// 目录是值投影；后续变化的 Profile 指针不得改变已经返回的历史版本视图。
	directory.Points[0].Text = "mutated caller value"
	if view.Revision.Content.KnowledgePoints[0].Text != "Redis uses an in-memory data model." {
		t.Fatalf("directory mutated profile revision: %#v", view.Revision.Content)
	}
}

func TestKnowledgeDirectoryPreservesHistoricalRevisionProjection(t *testing.T) {
	t.Parallel()
	workspaceID, sourceVersionID, spanID := knowledgeDirectoryID(11), knowledgeDirectoryID(12), knowledgeDirectoryID(13)
	fake := &knowledgeDirectoryProfileFake{views: map[foundation.ID]captureapp.ProfileView{}}
	fake.views[sourceVersionID] = knowledgeDirectoryProfile(t, workspaceID, sourceVersionID, knowledgeDirectoryID(14), 15,
		[]capturedomain.ProfilePoint{{Text: "Original Redis point.", SourceSpanIDs: []foundation.ID{spanID}}}, nil, []foundation.ID{spanID})
	reader := mustKnowledgeDirectoryReader(t, fake)
	first, err := reader.GetSourceKnowledgeDirectory(context.Background(), organizingapp.SourceKnowledgeDirectoryQuery{WorkspaceID: workspaceID, SourceVersionID: sourceVersionID})
	if err != nil {
		t.Fatal(err)
	}
	fake.views[sourceVersionID] = knowledgeDirectoryProfile(t, workspaceID, sourceVersionID, knowledgeDirectoryID(14), 16,
		[]capturedomain.ProfilePoint{{Text: "Rebuilt Redis point.", SourceSpanIDs: []foundation.ID{spanID}}}, nil, []foundation.ID{spanID})
	second, err := reader.GetSourceKnowledgeDirectory(context.Background(), organizingapp.SourceKnowledgeDirectoryQuery{WorkspaceID: workspaceID, SourceVersionID: sourceVersionID})
	if err != nil {
		t.Fatal(err)
	}
	if first.ProfileRevisionID == second.ProfileRevisionID || first.Points[0].Text != "Original Redis point." || second.Points[0].Text != "Rebuilt Redis point." {
		t.Fatalf("historical=%#v current=%#v", first, second)
	}
}

func TestProjectSynthesisKnowledgePointsRequiresExactTupleAndEvidence(t *testing.T) {
	t.Parallel()
	workspaceID, sourceVersionID := knowledgeDirectoryID(21), knowledgeDirectoryID(22)
	parseProjectionID, spanOne, spanTwo := knowledgeDirectoryID(23), knowledgeDirectoryID(24), knowledgeDirectoryID(25)
	view := knowledgeDirectoryProfile(t, workspaceID, sourceVersionID, parseProjectionID, 26,
		[]capturedomain.ProfilePoint{{Text: "Redis can serve cache reads.", SourceSpanIDs: []foundation.ID{spanOne}}, {Text: "Redis persistence requires a policy.", SourceSpanIDs: []foundation.ID{spanTwo}}}, nil, []foundation.ID{spanOne, spanTwo})
	reader := mustKnowledgeDirectoryReader(t, &knowledgeDirectoryProfileFake{views: map[foundation.ID]captureapp.ProfileView{sourceVersionID: view}})

	matched, err := reader.ProjectSynthesisKnowledgePoints(context.Background(), knowledgeDirectorySynthesisRef(workspaceID, sourceVersionID, parseProjectionID, spanOne))
	if err != nil || len(matched.Points) != 1 || matched.Points[0].Text != "Redis can serve cache reads." {
		t.Fatalf("matched=%#v err=%v", matched, err)
	}
	wrongSpan, err := reader.ProjectSynthesisKnowledgePoints(context.Background(), knowledgeDirectorySynthesisRef(workspaceID, sourceVersionID, parseProjectionID, knowledgeDirectoryID(99)))
	if err != nil || wrongSpan.Directory.Status != organizingapp.KnowledgeDirectoryAnalyzed || len(wrongSpan.Points) != 0 {
		t.Fatalf("wrong span=%#v err=%v", wrongSpan, err)
	}
	wrongProjection, err := reader.ProjectSynthesisKnowledgePoints(context.Background(), knowledgeDirectorySynthesisRef(workspaceID, sourceVersionID, knowledgeDirectoryID(98), spanOne))
	if err != nil || wrongProjection.Directory.Status != organizingapp.KnowledgeDirectoryAnalyzed || len(wrongProjection.Points) != 0 {
		t.Fatalf("wrong projection=%#v err=%v", wrongProjection, err)
	}
}

func TestKnowledgeDirectoryReturnsExplicitUnanalyzedAndUnavailableStates(t *testing.T) {
	t.Parallel()
	workspaceID, sourceVersionID, spanID := knowledgeDirectoryID(31), knowledgeDirectoryID(32), knowledgeDirectoryID(33)
	pending := knowledgeDirectoryProfile(t, workspaceID, sourceVersionID, knowledgeDirectoryID(34), 35,
		[]capturedomain.ProfilePoint{{Text: "Ignored because the profile is pending.", SourceSpanIDs: []foundation.ID{spanID}}}, nil, []foundation.ID{spanID})
	pending.Revision = nil
	pending.Profile.CurrentRevisionID = ""
	pending.Profile.Status = capturedomain.ProfileStatusPending
	pending.Profile.Version++
	pending.Profile.UpdatedAt = pending.Profile.UpdatedAt.Add(time.Second)
	failed := pending
	failed.Profile.Status = capturedomain.ProfileStatusCapabilityUnavailable
	failed.Profile.ErrorCode = "CAPTURE_PROFILE_CAPABILITY_UNAVAILABLE"
	failed.Profile.Retryable = false
	failed.Profile.Version++
	failed.Profile.UpdatedAt = failed.Profile.UpdatedAt.Add(time.Second)
	reader := mustKnowledgeDirectoryReader(t, &knowledgeDirectoryProfileFake{views: map[foundation.ID]captureapp.ProfileView{sourceVersionID: pending}})

	unanalyzed, err := reader.GetSourceKnowledgeDirectory(context.Background(), organizingapp.SourceKnowledgeDirectoryQuery{WorkspaceID: workspaceID, SourceVersionID: sourceVersionID})
	if err != nil || unanalyzed.Status != organizingapp.KnowledgeDirectoryUnanalyzed || len(unanalyzed.Points) != 0 {
		t.Fatalf("unanalyzed=%#v err=%v", unanalyzed, err)
	}
	missing, err := reader.GetSourceKnowledgeDirectory(context.Background(), organizingapp.SourceKnowledgeDirectoryQuery{WorkspaceID: workspaceID, SourceVersionID: knowledgeDirectoryID(36)})
	if err != nil || missing.Status != organizingapp.KnowledgeDirectoryUnanalyzed || len(missing.Points) != 0 {
		t.Fatalf("missing profile=%#v err=%v", missing, err)
	}
	reader.profiles.(*knowledgeDirectoryProfileFake).views[sourceVersionID] = failed
	unavailable, err := reader.GetSourceKnowledgeDirectory(context.Background(), organizingapp.SourceKnowledgeDirectoryQuery{WorkspaceID: workspaceID, SourceVersionID: sourceVersionID})
	if err != nil || unavailable.Status != organizingapp.KnowledgeDirectoryUnavailable || len(unavailable.Points) != 0 {
		t.Fatalf("unavailable=%#v err=%v", unavailable, err)
	}
}

func TestKnowledgeDirectoryRejectsCrossWorkspaceProfileAndUnboundEvidence(t *testing.T) {
	t.Parallel()
	workspaceID, sourceVersionID, spanID := knowledgeDirectoryID(41), knowledgeDirectoryID(42), knowledgeDirectoryID(43)
	foreign := knowledgeDirectoryProfile(t, knowledgeDirectoryID(44), sourceVersionID, knowledgeDirectoryID(45), 46,
		[]capturedomain.ProfilePoint{{Text: "Foreign workspace point.", SourceSpanIDs: []foundation.ID{spanID}}}, nil, []foundation.ID{spanID})
	reader := mustKnowledgeDirectoryReader(t, &knowledgeDirectoryProfileFake{views: map[foundation.ID]captureapp.ProfileView{sourceVersionID: foreign}})
	_, err := reader.GetSourceKnowledgeDirectory(context.Background(), organizingapp.SourceKnowledgeDirectoryQuery{WorkspaceID: workspaceID, SourceVersionID: sourceVersionID})
	assertOwnerError(t, err, foundation.ErrorConsistencyViolation, ErrorCodeOwnerResultInvalid)

	unbound := knowledgeDirectoryProfile(t, workspaceID, sourceVersionID, knowledgeDirectoryID(45), 47,
		[]capturedomain.ProfilePoint{{Text: "Unbound point.", SourceSpanIDs: []foundation.ID{spanID}}}, nil, []foundation.ID{knowledgeDirectoryID(48)})
	reader = mustKnowledgeDirectoryReader(t, &knowledgeDirectoryProfileFake{views: map[foundation.ID]captureapp.ProfileView{sourceVersionID: unbound}})
	_, err = reader.GetSourceKnowledgeDirectory(context.Background(), organizingapp.SourceKnowledgeDirectoryQuery{WorkspaceID: workspaceID, SourceVersionID: sourceVersionID})
	assertOwnerError(t, err, foundation.ErrorConsistencyViolation, ErrorCodeOwnerResultInvalid)
}

type knowledgeDirectoryProfileFake struct {
	views map[foundation.ID]captureapp.ProfileView
}

func (fake *knowledgeDirectoryProfileFake) GetProfile(_ context.Context, query captureapp.ProfileQuery) (captureapp.ProfileView, error) {
	view, found := fake.views[query.SourceVersionID]
	if !found {
		return captureapp.ProfileView{}, foundation.NewError(foundation.ErrorNotFound, "CAPTURE_PROFILE_NOT_FOUND", false, errors.New("missing profile"))
	}
	return view, nil
}

func mustKnowledgeDirectoryReader(t *testing.T, profiles profileDirectoryReader) *KnowledgeDirectoryReader {
	t.Helper()
	reader, err := NewKnowledgeDirectoryReader(profiles)
	if err != nil {
		t.Fatal(err)
	}
	return reader
}

func knowledgeDirectoryProfile(t *testing.T, workspaceID, sourceVersionID, parseProjectionID foundation.ID, seed int, points, examples []capturedomain.ProfilePoint, evidence []foundation.ID) captureapp.ProfileView {
	t.Helper()
	content := capturedomain.ProfileContent{Summary: "Redis knowledge profile", Topics: []capturedomain.ProfileCandidate{{
		Label: "Redis", Aliases: []string{"cache"}, SourceSpanIDs: append([]foundation.ID(nil), evidence...),
	}}, Terms: []capturedomain.ProfileCandidate{{Label: "TTL", SourceSpanIDs: []foundation.ID{evidence[0]}}}, KnowledgePoints: points, Examples: examples}
	digest, err := capturedomain.ComputeProfileDigest(content)
	if err != nil {
		t.Fatal(err)
	}
	revisionID := knowledgeDirectoryID(1000 + seed)
	now := time.Date(2026, 9, 14, 1, 0, 0, 0, time.UTC)
	revision := capturedomain.ProfileRevision{ID: revisionID, ProfileID: knowledgeDirectoryID(2000 + seed), WorkspaceID: workspaceID,
		SourceVersionID: sourceVersionID, ParseProjectionID: parseProjectionID, IndexVersionID: knowledgeDirectoryID(3000 + seed),
		ModelRunID: knowledgeDirectoryID(4000 + seed), PromptVersion: "profile-v1", SchemaVersion: capturedomain.ProfileSchemaVersion,
		Content: content, ContentDigest: digest, CreatedAt: now}
	profile := capturedomain.Profile{ID: revision.ProfileID, WorkspaceID: workspaceID, CaptureID: knowledgeDirectoryID(5000 + seed),
		SourceVersionID: sourceVersionID, CurrentRevisionID: revisionID, Status: capturedomain.ProfileStatusReady, Version: 1, CreatedAt: now, UpdatedAt: now}
	profileEvidence := make([]capturedomain.ProfileEvidence, len(evidence))
	for index, spanID := range evidence {
		profileEvidence[index] = capturedomain.ProfileEvidence{RevisionID: revisionID, WorkspaceID: workspaceID, SourceVersionID: sourceVersionID, SourceSpanID: spanID, CreatedAt: now}
	}
	return captureapp.ProfileView{Profile: profile, Revision: &revision, Evidence: profileEvidence}
}

func knowledgeDirectorySynthesisRef(workspaceID, sourceVersionID, parseProjectionID, spanID foundation.ID) organizingdomain.SynthesisSourceRef {
	return organizingdomain.SynthesisSourceRef{Source: organizingdomain.SynthesisSourceVersion{
		WorkspaceID: workspaceID, SourceID: knowledgeDirectoryID(6000), SourceVersionID: sourceVersionID,
		ContentArtifactID: knowledgeDirectoryID(6001), ParseProjectionID: parseProjectionID, ContentHash: knowledgeDirectoryHash(6002),
	}, SourceSpanID: spanID, ExcerptHash: knowledgeDirectoryHash(6003), Title: "Redis source"}
}

func knowledgeDirectoryID(seed int) foundation.ID {
	return foundation.ID(fmt.Sprintf("92000000-0000-4000-8000-%012d", seed))
}

func knowledgeDirectoryHash(seed int) string { return fmt.Sprintf("%064x", seed) }
