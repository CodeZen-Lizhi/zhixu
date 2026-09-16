package owner

import (
	"context"
	"errors"
	"reflect"

	captureapp "github.com/CodeZen-Lizhi/zhixu/internal/capture/application"
	capturedomain "github.com/CodeZen-Lizhi/zhixu/internal/capture/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	organizingapp "github.com/CodeZen-Lizhi/zhixu/internal/organizing/application"
	organizingdomain "github.com/CodeZen-Lizhi/zhixu/internal/organizing/domain"
)

// profileDirectoryReader 是 Capture 的公开只读边界；适配器不得直接查询 Capture 持久化表。
type profileDirectoryReader interface {
	GetProfile(context.Context, captureapp.ProfileQuery) (captureapp.ProfileView, error)
}

// KnowledgeDirectoryReader 将不可变的 Capture Profile 版本适配为 organizing 的只读知识目录投影。
type KnowledgeDirectoryReader struct{ profiles profileDirectoryReader }

var _ organizingapp.KnowledgeDirectoryReader = (*KnowledgeDirectoryReader)(nil)

func NewKnowledgeDirectoryReader(profiles profileDirectoryReader) (*KnowledgeDirectoryReader, error) {
	if nilKnowledgeDirectoryDependency(profiles) {
		return nil, dependencyUnavailable("capture profile reader is unavailable")
	}
	return &KnowledgeDirectoryReader{profiles: profiles}, nil
}

func (reader *KnowledgeDirectoryReader) GetSourceKnowledgeDirectory(
	ctx context.Context,
	query organizingapp.SourceKnowledgeDirectoryQuery,
) (organizingapp.SourceKnowledgeDirectory, error) {
	if err := reader.ready(ctx, query.WorkspaceID, query.SourceVersionID); err != nil {
		return organizingapp.SourceKnowledgeDirectory{}, err
	}
	directory := organizingapp.SourceKnowledgeDirectory{WorkspaceID: query.WorkspaceID, SourceVersionID: query.SourceVersionID, Status: organizingapp.KnowledgeDirectoryUnanalyzed}
	view, err := reader.profiles.GetProfile(ctx, captureapp.ProfileQuery{WorkspaceID: query.WorkspaceID, SourceVersionID: query.SourceVersionID})
	if err != nil {
		if profileNotFound(err) {
			return directory, nil
		}
		return organizingapp.SourceKnowledgeDirectory{}, err
	}
	return directoryFromProfile(query, view)
}

func (reader *KnowledgeDirectoryReader) ProjectSynthesisKnowledgePoints(
	ctx context.Context,
	reference organizingdomain.SynthesisSourceRef,
) (organizingapp.SynthesisKnowledgePointProjection, error) {
	if reference.Validate() != nil {
		return organizingapp.SynthesisKnowledgePointProjection{}, invalid("synthesis source reference is invalid")
	}
	directory, err := reader.GetSourceKnowledgeDirectory(ctx, organizingapp.SourceKnowledgeDirectoryQuery{
		WorkspaceID: reference.Source.WorkspaceID, SourceVersionID: reference.Source.SourceVersionID,
	})
	if err != nil {
		return organizingapp.SynthesisKnowledgePointProjection{}, err
	}
	result := organizingapp.SynthesisKnowledgePointProjection{Reference: reference, Directory: directory, Points: []organizingapp.KnowledgeDirectoryPoint{}}
	if directory.Status != organizingapp.KnowledgeDirectoryAnalyzed || directory.ParseProjectionID != reference.Source.ParseProjectionID {
		return result, nil
	}
	for _, point := range directory.Points {
		if containsSpan(point.SourceSpanIDs, reference.SourceSpanID) {
			result.Points = append(result.Points, point)
		}
	}
	return result, nil
}

func (reader *KnowledgeDirectoryReader) ready(ctx context.Context, workspaceID, sourceVersionID foundation.ID) error {
	if reader == nil || nilKnowledgeDirectoryDependency(reader.profiles) {
		return dependencyUnavailable("capture profile reader is unavailable")
	}
	if ctx == nil || !validID(workspaceID) || !validID(sourceVersionID) {
		return invalid("knowledge directory request is invalid")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	return nil
}

func directoryFromProfile(query organizingapp.SourceKnowledgeDirectoryQuery, view captureapp.ProfileView) (organizingapp.SourceKnowledgeDirectory, error) {
	directory := organizingapp.SourceKnowledgeDirectory{
		WorkspaceID: query.WorkspaceID, SourceVersionID: query.SourceVersionID, ProfileStatus: view.Profile.Status,
	}
	if err := view.Profile.Validate(); err != nil || view.Profile.WorkspaceID != query.WorkspaceID || view.Profile.SourceVersionID != query.SourceVersionID {
		return organizingapp.SourceKnowledgeDirectory{}, inconsistent("capture returned a profile outside the requested source version")
	}
	switch view.Profile.Status {
	case capturedomain.ProfileStatusPending, capturedomain.ProfileStatusRunning:
		directory.Status = organizingapp.KnowledgeDirectoryUnanalyzed
		return directory, nil
	case capturedomain.ProfileStatusFailed, capturedomain.ProfileStatusCapabilityUnavailable:
		directory.Status = organizingapp.KnowledgeDirectoryUnavailable
		return directory, nil
	case capturedomain.ProfileStatusReady, capturedomain.ProfileStatusStale:
		// 过期 Profile 仍保留不可变的当前版本，且可继续用于生成该版本时对应的精确来源版本。
	default:
		return organizingapp.SourceKnowledgeDirectory{}, inconsistent("capture returned an unsupported profile status")
	}
	if view.Revision == nil || view.Profile.CurrentRevisionID != view.Revision.ID || view.Revision.ProfileID != view.Profile.ID {
		return organizingapp.SourceKnowledgeDirectory{}, inconsistent("capture returned an incomplete profile revision")
	}
	return directoryFromRevision(query, view.Revision, view.Evidence, view.Profile.Status)
}

func directoryFromRevision(query organizingapp.SourceKnowledgeDirectoryQuery, revision *capturedomain.ProfileRevision, evidenceRows []capturedomain.ProfileEvidence, profileStatus capturedomain.ProfileStatus) (organizingapp.SourceKnowledgeDirectory, error) {
	if revision == nil || revision.Validate() != nil || revision.WorkspaceID != query.WorkspaceID || revision.SourceVersionID != query.SourceVersionID || len(evidenceRows) == 0 {
		return organizingapp.SourceKnowledgeDirectory{}, inconsistent("capture returned an incomplete profile revision")
	}
	evidence := make(map[foundation.ID]struct{}, len(evidenceRows))
	for _, item := range evidenceRows {
		if item.RevisionID != revision.ID || item.WorkspaceID != query.WorkspaceID || item.SourceVersionID != query.SourceVersionID ||
			!validID(item.SourceSpanID) || item.CreatedAt.IsZero() {
			return organizingapp.SourceKnowledgeDirectory{}, inconsistent("capture returned invalid profile evidence")
		}
		evidence[item.SourceSpanID] = struct{}{}
	}
	points, err := directoryPoints(revision, evidence)
	if err != nil {
		return organizingapp.SourceKnowledgeDirectory{}, err
	}
	if !candidatesUseEvidence(revision.Content.Topics, evidence) || !candidatesUseEvidence(revision.Content.Terms, evidence) {
		return organizingapp.SourceKnowledgeDirectory{}, inconsistent("capture profile metadata is not evidence-backed")
	}
	return organizingapp.SourceKnowledgeDirectory{
		WorkspaceID: query.WorkspaceID, SourceVersionID: query.SourceVersionID, Status: organizingapp.KnowledgeDirectoryAnalyzed,
		ProfileStatus: profileStatus, ProfileRevisionID: revision.ID, ParseProjectionID: revision.ParseProjectionID,
		Summary: revision.Content.Summary, Topics: cloneCandidates(revision.Content.Topics), Terms: cloneCandidates(revision.Content.Terms), Points: points,
	}, nil
}

func directoryPoints(revision *capturedomain.ProfileRevision, evidence map[foundation.ID]struct{}) ([]organizingapp.KnowledgeDirectoryPoint, error) {
	result := make([]organizingapp.KnowledgeDirectoryPoint, 0, len(revision.Content.KnowledgePoints)+len(revision.Content.Examples))
	appendPoints := func(kind organizingapp.KnowledgePointKind, values []capturedomain.ProfilePoint) error {
		for index, point := range values {
			if !spansUseEvidence(point.SourceSpanIDs, evidence) {
				return inconsistent("capture profile point is not evidence-backed")
			}
			result = append(result, organizingapp.KnowledgeDirectoryPoint{Locator: organizingapp.KnowledgePointLocator{
				ProfileRevisionID: revision.ID, Kind: kind, Index: index,
			}, Text: point.Text, SourceSpanIDs: append([]foundation.ID(nil), point.SourceSpanIDs...)})
		}
		return nil
	}
	if err := appendPoints(organizingapp.KnowledgePointKindKnowledgePoint, revision.Content.KnowledgePoints); err != nil {
		return nil, err
	}
	if err := appendPoints(organizingapp.KnowledgePointKindExample, revision.Content.Examples); err != nil {
		return nil, err
	}
	return result, nil
}

func candidatesUseEvidence(values []capturedomain.ProfileCandidate, evidence map[foundation.ID]struct{}) bool {
	for _, item := range values {
		if !spansUseEvidence(item.SourceSpanIDs, evidence) {
			return false
		}
	}
	return true
}

func spansUseEvidence(spans []foundation.ID, evidence map[foundation.ID]struct{}) bool {
	if len(spans) == 0 {
		return false
	}
	for _, spanID := range spans {
		if _, ok := evidence[spanID]; !ok {
			return false
		}
	}
	return true
}

func containsSpan(values []foundation.ID, target foundation.ID) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}

func cloneCandidates(values []capturedomain.ProfileCandidate) []capturedomain.ProfileCandidate {
	result := make([]capturedomain.ProfileCandidate, len(values))
	for index, value := range values {
		result[index] = value
		result[index].Aliases = append([]string(nil), value.Aliases...)
		result[index].SourceSpanIDs = append([]foundation.ID(nil), value.SourceSpanIDs...)
	}
	return result
}

func profileNotFound(err error) bool {
	var classified *foundation.Error
	return errors.As(err, &classified) && classified.Kind == foundation.ErrorNotFound
}

func nilKnowledgeDirectoryDependency(value any) bool {
	if value == nil {
		return true
	}
	ref := reflect.ValueOf(value)
	switch ref.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return ref.IsNil()
	default:
		return false
	}
}

// ProjectSynthesisKnowledgeSnapshot 从不读取可变的 Profile 指针。
func (reader *KnowledgeDirectoryReader) ProjectSynthesisKnowledgeSnapshot(ctx context.Context, reference organizingdomain.SynthesisSourceRef, profileRevisionID foundation.ID) (organizingapp.SynthesisKnowledgePointProjection, error) {
	if reference.Validate() != nil {
		return organizingapp.SynthesisKnowledgePointProjection{}, invalid("synthesis source reference is invalid")
	}
	if err := reader.ready(ctx, reference.Source.WorkspaceID, reference.Source.SourceVersionID); err != nil {
		return organizingapp.SynthesisKnowledgePointProjection{}, err
	}
	result := organizingapp.SynthesisKnowledgePointProjection{Reference: reference, Directory: organizingapp.SourceKnowledgeDirectory{WorkspaceID: reference.Source.WorkspaceID, SourceVersionID: reference.Source.SourceVersionID, Status: organizingapp.KnowledgeDirectoryUnrecorded}, Points: []organizingapp.KnowledgeDirectoryPoint{}}
	if profileRevisionID == "" {
		return result, nil
	}
	if !validID(profileRevisionID) {
		return result, invalid("profile revision identity is invalid")
	}
	profiles, ok := reader.profiles.(captureapp.ProfileRevisionReader)
	if !ok || nilKnowledgeDirectoryDependency(profiles) {
		return result, dependencyUnavailable("historical profile reader is unavailable")
	}
	snapshot, err := profiles.GetProfileRevision(ctx, captureapp.ProfileRevisionQuery{WorkspaceID: reference.Source.WorkspaceID, SourceVersionID: reference.Source.SourceVersionID, RevisionID: profileRevisionID})
	if err != nil {
		return result, err
	}
	if snapshot.Revision.ID != profileRevisionID || snapshot.Revision.ParseProjectionID != reference.Source.ParseProjectionID {
		return result, inconsistent("historical profile differs from frozen synthesis source")
	}
	directory, err := directoryFromRevision(organizingapp.SourceKnowledgeDirectoryQuery{WorkspaceID: reference.Source.WorkspaceID, SourceVersionID: reference.Source.SourceVersionID}, &snapshot.Revision, snapshot.Evidence, capturedomain.ProfileStatusReady)
	if err != nil {
		return result, err
	}
	result.Directory = directory
	for _, point := range directory.Points {
		if containsSpan(point.SourceSpanIDs, reference.SourceSpanID) {
			result.Points = append(result.Points, point)
		}
	}
	return result, nil
}
