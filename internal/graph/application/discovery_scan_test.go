package application

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	graphdomain "github.com/CodeZen-Lizhi/zhixu/internal/graph/domain"
	knowledge "github.com/CodeZen-Lizhi/zhixu/internal/knowledge/domain"
)

func TestSemanticLinkDiscoveryServiceCallsProvidersOncePerBatch(t *testing.T) {
	workspaceID := discoveryAppID(900)
	pair := discoveryAppPair(workspaceID, 1, 2, "shared")
	semantic := &discoverySignalFake{method: graphdomain.SemanticLinkDiscoveryMethodClaimSemanticSimilarity, hash: strings.Repeat("a", 64), score: .9, mutate: true}
	rag := &discoverySignalFake{method: graphdomain.SemanticLinkDiscoveryMethodRAGCoRetrieval, hash: strings.Repeat("b", 64), score: .7}
	service := NewSemanticLinkDiscoveryService(semantic, rag)
	result, err := service.Discover(context.Background(), SemanticLinkDiscoveryCommand{
		WorkspaceID: workspaceID, Pairs: []graphdomain.SemanticLinkDiscoveryPair{pair},
		Limit: 100, PerSourceLimit: 100, RuleGeneration: discoveryAppRuleGeneration(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if semantic.calls != 1 || rag.calls != 1 || semantic.pairCount != 1 || rag.pairCount != 1 {
		t.Fatalf("provider calls semantic=%d/%d rag=%d/%d", semantic.calls, semantic.pairCount, rag.calls, rag.pairCount)
	}
	if pair.Source.Endpoint.Ref.ID != discoveryAppID(1) || rag.observedSource != discoveryAppID(1) {
		t.Fatalf("provider mutated shared input: pair=%q rag=%q", pair.Source.Endpoint.Ref.ID, rag.observedSource)
	}
	if len(result.Items) != 1 || len(result.Items[0].Methods) != 3 {
		t.Fatalf("result = %+v", result)
	}
}

func TestSemanticLinkDiscoveryServiceReportsMissingProvidersAndFailsClosed(t *testing.T) {
	workspaceID := discoveryAppID(901)
	pair := discoveryAppPair(workspaceID, 1, 2, "left")
	pair.Target.Labels = []string{"right"}
	service := NewSemanticLinkDiscoveryService(nil, nil)
	result, err := service.Discover(context.Background(), SemanticLinkDiscoveryCommand{
		WorkspaceID: workspaceID, Pairs: []graphdomain.SemanticLinkDiscoveryPair{pair},
		Limit: 100, PerSourceLimit: 100, RuleGeneration: discoveryAppRuleGeneration(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Items) != 0 || result.Signals[4].Status != graphdomain.SemanticLinkDiscoverySignalStatusUnsupported || result.Signals[5].Status != graphdomain.SemanticLinkDiscoverySignalStatusUnsupported {
		t.Fatalf("missing providers were not explicit: %+v", result)
	}

	cause := errors.New("semantic backend timed out")
	failing := &discoverySignalFake{method: graphdomain.SemanticLinkDiscoveryMethodClaimSemanticSimilarity, err: cause}
	service = NewSemanticLinkDiscoveryService(failing, ragNeverCalled{})
	_, err = service.Discover(context.Background(), SemanticLinkDiscoveryCommand{
		WorkspaceID: workspaceID, Pairs: []graphdomain.SemanticLinkDiscoveryPair{pair},
		Limit: 100, PerSourceLimit: 100, RuleGeneration: discoveryAppRuleGeneration(),
	})
	var classified *foundation.Error
	if !errors.As(err, &classified) || classified.Code != graphdomain.ErrorCodeSemanticLinkDiscoveryUnavailable || !errors.Is(err, cause) {
		t.Fatalf("provider error = %v", err)
	}
}

func TestSemanticLinkDiscoveryServiceValidatesBeforeCallingProviders(t *testing.T) {
	provider := &discoverySignalFake{method: graphdomain.SemanticLinkDiscoveryMethodClaimSemanticSimilarity}
	service := NewSemanticLinkDiscoveryService(provider, nil)
	_, err := service.Discover(context.Background(), SemanticLinkDiscoveryCommand{WorkspaceID: discoveryAppID(900), Limit: 0, PerSourceLimit: 100, RuleGeneration: discoveryAppRuleGeneration()})
	if err == nil || provider.calls != 0 {
		t.Fatalf("invalid command reached provider: calls=%d err=%v", provider.calls, err)
	}
}

func TestSemanticLinkScanServiceStartsWithWorkflowBindingAndStatusURL(t *testing.T) {
	now := time.Date(2026, 7, 21, 2, 0, 0, 0, time.UTC)
	starter := &scanStarterFake{now: now}
	state := &scanStateFake{}
	service, err := NewSemanticLinkScanService(starter, state)
	if err != nil {
		t.Fatal(err)
	}
	command := SemanticLinkScanStartCommand{
		WorkspaceID: discoveryAppID(900),
		Scope:       graphdomain.SemanticLinkScanScope{Type: graphdomain.SemanticLinkScanScopeTopic, Ref: string(discoveryAppID(20)), Version: 3, SchemaVersion: "topic-scan/v1"},
		Generation:  discoveryAppScanGeneration(), TotalNodes: 42, IdempotencyKey: "  scan-one  ",
	}
	result, err := service.StartTopicScan(context.Background(), command)
	if err != nil {
		t.Fatal(err)
	}
	if starter.calls != 1 || starter.request.IdempotencyKey != "scan-one" || starter.request.WorkflowDefinitionKey != SemanticLinkScanWorkflowDefinitionKey || starter.request.WorkflowDefinitionVersion != 1 || starter.request.WorkflowInputSchemaVersion != 1 {
		t.Fatalf("start request = %+v", starter.request)
	}
	wantURL := "/api/v1/graph/candidate-scans/" + string(result.Scan.ID) + "?workspace_id=" + string(result.Scan.WorkspaceID)
	if result.StatusURL != wantURL || result.Scan.WorkflowRunID == "" || result.Scan.TotalNodes != 42 {
		t.Fatalf("start result = %+v", result)
	}
}

func TestSemanticLinkScanCommandServicePlansServerOwnedTopicScope(t *testing.T) {
	now := time.Date(2026, 7, 21, 2, 0, 0, 0, time.UTC)
	workspaceID, topicID := discoveryAppID(900), discoveryAppID(20)
	planner := &topicScanPlannerFake{plan: SemanticLinkTopicScanPlan{
		WorkspaceID: workspaceID, TopicID: topicID, TopicVersion: 7, TotalNodes: 23,
	}}
	starter := &scanStarterFake{now: now}
	scans, err := NewSemanticLinkScanService(starter, &scanStateFake{})
	if err != nil {
		t.Fatal(err)
	}
	service, err := NewSemanticLinkScanCommandService(planner, scans)
	if err != nil {
		t.Fatal(err)
	}
	result, err := service.StartTopicScan(context.Background(), SemanticLinkTopicScanRequest{
		WorkspaceID: workspaceID, TopicID: topicID, IdempotencyKey: "topic-scan",
	})
	if err != nil {
		t.Fatal(err)
	}
	if planner.calls != 1 || starter.request.Scope.Type != graphdomain.SemanticLinkScanScopeTopic ||
		starter.request.Scope.Ref != string(topicID) || starter.request.Scope.Version != 7 ||
		starter.request.Scope.SchemaVersion != SemanticLinkTopicScanScopeSchemaVersion || starter.request.TotalNodes != 23 ||
		starter.request.Generation.Rule.RuleID == nil || *starter.request.Generation.Rule.RuleID != SemanticLinkScanRuleID ||
		starter.request.Generation.Rule.RuleVersion != SemanticLinkScanRuleVersion ||
		starter.request.Generation.WorkflowVersion != SemanticLinkScanWorkflowGenerationVersion ||
		result.StatusURL != "/api/v1/graph/candidate-scans/"+string(result.Scan.ID)+"?workspace_id="+string(result.Scan.WorkspaceID) {
		t.Fatalf("plan=%#v start=%#v result=%#v", planner.plan, starter.request, result)
	}
}

func TestSemanticLinkScanCommandServiceBindsSmartCollectionScope(t *testing.T) {
	now := time.Date(2026, 7, 21, 2, 0, 0, 0, time.UTC)
	workspaceID, collectionID := discoveryAppID(900), discoveryAppID(220)
	queryHash, revision := strings.Repeat("a", 64), strings.Repeat("b", 64)
	planner := &smartCollectionScanPlannerFake{plan: SemanticLinkSmartCollectionScanPlan{
		WorkspaceID: workspaceID, CollectionID: collectionID, CollectionVersion: 9,
		QueryHash: queryHash, ReadModelRevision: revision, TotalNodes: 31,
	}}
	starter := &scanStarterFake{now: now}
	scans, err := NewSemanticLinkScanService(starter, &scanStateFake{})
	if err != nil {
		t.Fatal(err)
	}
	service, err := NewSemanticLinkScanCommandService(planner, scans)
	if err != nil {
		t.Fatal(err)
	}
	result, err := service.StartSmartCollectionScan(context.Background(), SemanticLinkSmartCollectionScanRequest{
		WorkspaceID: workspaceID, CollectionID: collectionID, IdempotencyKey: "collection-scan",
	})
	if err != nil {
		t.Fatal(err)
	}
	scope := starter.request.Scope
	if planner.calls != 1 || scope.Type != graphdomain.SemanticLinkScanScopeSmartCollection || scope.Ref != string(collectionID) || scope.Version != 9 || scope.SchemaVersion != SemanticLinkSmartCollectionScanScopeSchemaVersion || scope.QueryHash != queryHash || scope.ReadModelRevision != revision || starter.request.TotalNodes != 31 || result.Scan.Scope != scope {
		t.Fatalf("planner=%#v request=%#v result=%#v", planner.plan, starter.request, result)
	}
}

func TestSemanticLinkScanPageSourceRouterDispatchesByScope(t *testing.T) {
	topic := &scanPageSourceFake{}
	smart := &scanPageSourceFake{}
	router, err := NewSemanticLinkScanPageSourceRouter(topic, smart)
	if err != nil {
		t.Fatal(err)
	}
	request := SemanticLinkTopicScanPageRequest{Scope: graphdomain.SemanticLinkScanScope{Type: graphdomain.SemanticLinkScanScopeTopic}}
	if _, err := router.LoadPage(context.Background(), request); err != nil {
		t.Fatal(err)
	}
	request.Scope.Type = graphdomain.SemanticLinkScanScopeSmartCollection
	if _, err := router.LoadPage(context.Background(), request); err != nil {
		t.Fatal(err)
	}
	if topic.calls != 1 || smart.calls != 1 {
		t.Fatalf("topic calls=%d smart calls=%d", topic.calls, smart.calls)
	}
}

func TestSemanticLinkScanPageSourceRouterKeepsTopicAvailableWithoutCollection(t *testing.T) {
	topic := &scanPageSourceFake{}
	router, err := NewSemanticLinkScanPageSourceRouter(topic, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := router.LoadPage(context.Background(), SemanticLinkTopicScanPageRequest{Scope: graphdomain.SemanticLinkScanScope{Type: graphdomain.SemanticLinkScanScopeTopic}}); err != nil {
		t.Fatal(err)
	}
	if _, err := router.LoadPage(context.Background(), SemanticLinkTopicScanPageRequest{Scope: graphdomain.SemanticLinkScanScope{Type: graphdomain.SemanticLinkScanScopeSmartCollection}}); err == nil {
		t.Fatal("expected unavailable smart source")
	}
}

func TestSemanticLinkScanServiceValidatesProgressAndTerminalProjection(t *testing.T) {
	now := time.Date(2026, 7, 21, 2, 0, 0, 0, time.UTC)
	state := &scanStateFake{now: now}
	service, err := NewSemanticLinkScanService(&scanStarterFake{now: now}, state)
	if err != nil {
		t.Fatal(err)
	}
	lastNode := knowledge.NodeRef{Type: knowledge.NodeTypeClaim, ID: discoveryAppID(10)}
	progress := graphdomain.SemanticLinkScanProgress{
		ScanID: discoveryAppID(1), WorkspaceID: discoveryAppID(900), ExpectedVersion: 1,
		Checkpoint:     graphdomain.SemanticLinkScanCheckpoint{Cursor: "page-2", ProcessedPage: 1, LastNode: &lastNode},
		ProcessedDelta: 2, CandidateDelta: 1,
	}
	state.advance = validApplicationScan(now)
	state.advance.ID, state.advance.WorkspaceID, state.advance.Version, state.advance.Status = progress.ScanID, progress.WorkspaceID, 2, graphdomain.SemanticLinkScanStatusRunning
	state.advance.TotalNodes, state.advance.ProcessedNodes, state.advance.CandidateCount, state.advance.Checkpoint = 2, 2, 1, progress.Checkpoint
	if _, err := service.AdvancePage(context.Background(), progress); err != nil {
		t.Fatal(err)
	}

	terminal := graphdomain.SemanticLinkScanTerminal{ScanID: progress.ScanID, WorkspaceID: progress.WorkspaceID, ExpectedVersion: 2, Status: graphdomain.SemanticLinkScanStatusSucceeded, At: now.Add(time.Minute)}
	state.finish = state.advance
	state.finish.Version, state.finish.Status, state.finish.UpdatedAt, state.finish.CompletedAt = 3, terminal.Status, terminal.At, appTimePointer(terminal.At)
	if _, err := service.Finish(context.Background(), terminal); err != nil {
		t.Fatal(err)
	}

	badProgress := progress
	badProgress.ProcessedDelta = -1
	if _, err := service.AdvancePage(context.Background(), badProgress); err == nil {
		t.Fatal("expected negative-progress page rejection")
	}
	badTerminal := terminal
	badTerminal.Status = graphdomain.SemanticLinkScanStatusFailed
	if _, err := service.Finish(context.Background(), badTerminal); err == nil {
		t.Fatal("expected failed terminal without error rejection")
	}
}

func TestSameScanCompletionTimeAllowsOnlySubMicrosecondPersistenceDrift(t *testing.T) {
	requested := time.Date(2026, 7, 21, 2, 0, 0, 123_456_789, time.UTC)
	for _, test := range []struct {
		name   string
		stored *time.Time
		want   bool
	}{
		{name: "missing", stored: nil, want: false},
		{name: "exact", stored: appTimePointer(requested), want: true},
		{name: "truncated", stored: appTimePointer(requested.Add(-789 * time.Nanosecond)), want: true},
		{name: "rounded", stored: appTimePointer(requested.Add(211 * time.Nanosecond)), want: true},
		{name: "different microsecond", stored: appTimePointer(requested.Add(time.Microsecond)), want: false},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := sameScanCompletionTime(test.stored, requested); got != test.want {
				t.Fatalf("sameScanCompletionTime()=%v want=%v", got, test.want)
			}
		})
	}
}

func TestSemanticLinkScanStateServiceDoesNotExposeFakeStartCapability(t *testing.T) {
	service, err := NewSemanticLinkScanStateService(&scanStateFake{})
	if err != nil {
		t.Fatal(err)
	}
	_, err = service.StartTopicScan(context.Background(), SemanticLinkScanStartCommand{})
	var classified *foundation.Error
	if !errors.As(err, &classified) || classified.Code != graphdomain.ErrorCodeSemanticLinkDiscoveryUnavailable {
		t.Fatalf("state-only start error=%v", err)
	}
}

func TestSemanticLinkTopicScanExecutorProcessesBoundedPageAndEmptyPage(t *testing.T) {
	workspaceID := discoveryAppID(905)
	pair := discoveryAppPair(workspaceID, 1, 2, "shared")
	source := &scanPageSourceFake{page: SemanticLinkTopicScanPage{
		WorkspaceID: workspaceID, ScanID: discoveryAppID(50), ScopeVersion: 1,
		Cursor: "", Complete: true, ProcessedNodes: 1, LastNode: &pair.Source.Endpoint.Ref,
		Pairs: []graphdomain.SemanticLinkDiscoveryPair{pair},
	}}
	executor, err := NewSemanticLinkTopicScanExecutor(source, NewSemanticLinkDiscoveryService(nil, nil))
	if err != nil {
		t.Fatal(err)
	}
	request := SemanticLinkTopicScanPageRequest{
		WorkspaceID: workspaceID, ScanID: discoveryAppID(50),
		Scope: graphdomain.SemanticLinkScanScope{Type: graphdomain.SemanticLinkScanScopeTopic, Ref: string(discoveryAppID(60)), Version: 1, SchemaVersion: "v1"}, Limit: 100,
		Generation: graphdomain.SemanticLinkScanGeneration{Rule: discoveryAppRuleGeneration(), WorkflowVersion: "workflow/v1"},
	}
	result, err := executor.ProcessPage(context.Background(), request)
	if err != nil || source.calls != 1 || len(result.Discovery.Items) != 1 || !result.Complete || result.ProcessedNodes != 1 {
		t.Fatalf("page result=%+v calls=%d err=%v", result, source.calls, err)
	}

	source.page.Pairs = nil
	empty, err := executor.ProcessPage(context.Background(), request)
	if err != nil || len(empty.Discovery.Items) != 0 || len(empty.Discovery.Signals) != 6 || empty.Discovery.Signals[4].Status != graphdomain.SemanticLinkDiscoverySignalStatusUnsupported {
		t.Fatalf("empty page=%+v err=%v", empty, err)
	}
}

func TestSemanticLinkTopicScanExecutorRejectsProviderGenerationDrift(t *testing.T) {
	workspaceID := discoveryAppID(906)
	source := &scanPageSourceFake{page: SemanticLinkTopicScanPage{
		WorkspaceID: workspaceID, ScanID: discoveryAppID(51), ScopeVersion: 1,
		Complete: true, ProcessedNodes: 1, Pairs: []graphdomain.SemanticLinkDiscoveryPair{discoveryAppPair(workspaceID, 1, 2, "shared")},
	}}
	source.page.LastNode = &source.page.Pairs[0].Source.Endpoint.Ref
	provider := &discoverySignalFake{method: graphdomain.SemanticLinkDiscoveryMethodClaimSemanticSimilarity, hash: strings.Repeat("c", 64), score: .8}
	executor, err := NewSemanticLinkTopicScanExecutor(source, NewSemanticLinkDiscoveryService(provider, nil))
	if err != nil {
		t.Fatal(err)
	}
	request := SemanticLinkTopicScanPageRequest{
		WorkspaceID: workspaceID, ScanID: discoveryAppID(51),
		Scope:      graphdomain.SemanticLinkScanScope{Type: graphdomain.SemanticLinkScanScopeTopic, Ref: string(discoveryAppID(61)), Version: 1, SchemaVersion: "v1"},
		Generation: discoveryAppScanGeneration(), Limit: 100,
	}
	if _, err := executor.ProcessPage(context.Background(), request); err != nil {
		t.Fatal(err)
	}
	drifted := *request.Generation.Semantic
	drifted.ModelVersion = "model/v2"
	request.Generation.Semantic = &drifted
	if _, err := executor.ProcessPage(context.Background(), request); err == nil {
		t.Fatal("expected provider generation drift rejection")
	}
}

func TestSemanticLinkTopicScanPageRejectsUnboundedOrUnstableAdapterResults(t *testing.T) {
	workspaceID := discoveryAppID(907)
	request := SemanticLinkTopicScanPageRequest{
		WorkspaceID: workspaceID, ScanID: discoveryAppID(52),
		Scope:      graphdomain.SemanticLinkScanScope{Type: graphdomain.SemanticLinkScanScopeTopic, Ref: string(discoveryAppID(62)), Version: 1, SchemaVersion: "v1"},
		Generation: discoveryAppScanGeneration(), Limit: 100,
	}
	first := discoveryAppPair(workspaceID, 1, 2, "shared")
	second := discoveryAppPair(workspaceID, 1, 3, "shared")
	valid := SemanticLinkTopicScanPage{
		WorkspaceID: workspaceID, ScanID: request.ScanID, ScopeVersion: request.Scope.Version,
		Complete: true, ProcessedNodes: 1, LastNode: &first.Source.Endpoint.Ref,
		Pairs: []graphdomain.SemanticLinkDiscoveryPair{first, second},
		Exclusions: []graphdomain.SemanticLinkDiscoveryExclusion{{
			Source: second.Target.Endpoint.Ref, Target: second.Source.Endpoint.Ref, Reason: "FORMAL_RELATION",
		}},
	}
	if err := validateTopicScanPage(request, valid); err != nil {
		t.Fatalf("valid page: %v", err)
	}

	tests := []struct {
		name   string
		mutate func(*SemanticLinkTopicScanPage)
	}{
		{name: "request limit exceeded", mutate: func(page *SemanticLinkTopicScanPage) {
			page.ProcessedNodes = 2
			page.LastNode = &second.Target.Endpoint.Ref
		}},
		{name: "duplicate pair", mutate: func(page *SemanticLinkTopicScanPage) { page.Pairs = append(page.Pairs, second) }},
		{name: "reverse pair", mutate: func(page *SemanticLinkTopicScanPage) {
			page.Pairs[0].Source, page.Pairs[0].Target = page.Pairs[0].Target, page.Pairs[0].Source
		}},
		{name: "unordered pair", mutate: func(page *SemanticLinkTopicScanPage) { page.Pairs[0], page.Pairs[1] = page.Pairs[1], page.Pairs[0] }},
		{name: "foreign exclusion", mutate: func(page *SemanticLinkTopicScanPage) {
			page.Exclusions[0].Target = discoveryAppPair(workspaceID, 4, 5, "other").Target.Endpoint.Ref
		}},
		{name: "duplicate exclusion", mutate: func(page *SemanticLinkTopicScanPage) { page.Exclusions = append(page.Exclusions, page.Exclusions[0]) }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			page := valid
			page.Pairs = append([]graphdomain.SemanticLinkDiscoveryPair(nil), valid.Pairs...)
			page.Exclusions = append([]graphdomain.SemanticLinkDiscoveryExclusion(nil), valid.Exclusions...)
			test.mutate(&page)
			localRequest := request
			if test.name == "request limit exceeded" {
				localRequest.Limit = 1
			}
			if err := validateTopicScanPage(localRequest, page); err == nil {
				t.Fatal("expected inconsistent page rejection")
			}
		})
	}

	many := valid
	many.ProcessedNodes = 2
	many.Pairs = make([]graphdomain.SemanticLinkDiscoveryPair, 0, MaxSemanticLinkScanPagePairsPerNode+1)
	for target := 2; target <= MaxSemanticLinkScanPagePairsPerNode+2; target++ {
		many.Pairs = append(many.Pairs, discoveryAppPair(workspaceID, 1, target, "shared"))
	}
	many.Exclusions = nil
	if err := validateTopicScanPage(request, many); err == nil {
		t.Fatal("expected per-source pair limit rejection")
	}
}

type discoverySignalFake struct {
	method         graphdomain.SemanticLinkDiscoveryMethod
	hash           string
	score          float64
	err            error
	mutate         bool
	calls          int
	pairCount      int
	observedSource foundation.ID
}

func (fake *discoverySignalFake) Evaluate(_ context.Context, request graphdomain.SemanticLinkDiscoveryProviderRequest) (graphdomain.SemanticLinkDiscoveryExternalResult, error) {
	fake.calls++
	fake.pairCount = len(request.Pairs)
	pairs := append([]graphdomain.SemanticLinkDiscoveryProviderPair(nil), request.Pairs...)
	if len(request.Pairs) > 0 {
		fake.observedSource = request.Pairs[0].Source.ID
		if fake.mutate {
			request.Pairs[0].Source.ID = discoveryAppID(999)
		}
	}
	if fake.err != nil {
		return graphdomain.SemanticLinkDiscoveryExternalResult{}, fake.err
	}
	result := graphdomain.SemanticLinkDiscoveryExternalResult{Method: fake.method, Supported: true, Generation: discoveryAppModelGeneration()}
	for _, pair := range pairs {
		result.Hits = append(result.Hits, graphdomain.SemanticLinkDiscoveryExternalHit{Source: pair.Source, Target: pair.Target, Score: fake.score, EvidenceHashes: []string{fake.hash}})
	}
	return result, nil
}

type ragNeverCalled struct{}

func (ragNeverCalled) Evaluate(context.Context, graphdomain.SemanticLinkDiscoveryProviderRequest) (graphdomain.SemanticLinkDiscoveryExternalResult, error) {
	return graphdomain.SemanticLinkDiscoveryExternalResult{}, errors.New("rag should not be called after semantic failure")
}

type scanStarterFake struct {
	now     time.Time
	calls   int
	request SemanticLinkScanStartRequest
}

func (fake *scanStarterFake) StartOrReplay(_ context.Context, request SemanticLinkScanStartRequest) (SemanticLinkScanStartResult, error) {
	fake.calls++
	fake.request = request
	scan := validApplicationScan(fake.now)
	scan.WorkspaceID, scan.Scope, scan.Fingerprint, scan.RequestHash = request.WorkspaceID, request.Scope, request.Fingerprint, request.RequestHash
	scan.IdempotencyKey, scan.TotalNodes = request.IdempotencyKey, request.TotalNodes
	return SemanticLinkScanStartResult{Scan: scan}, nil
}

type scanStateFake struct {
	now     time.Time
	get     graphdomain.SemanticLinkScan
	advance graphdomain.SemanticLinkScan
	finish  graphdomain.SemanticLinkScan
}

type topicScanPlannerFake struct {
	plan  SemanticLinkTopicScanPlan
	err   error
	calls int
}

type smartCollectionScanPlannerFake struct {
	plan  SemanticLinkSmartCollectionScanPlan
	err   error
	calls int
}

func (fake *smartCollectionScanPlannerFake) PlanTopicScan(context.Context, foundation.ID, foundation.ID) (SemanticLinkTopicScanPlan, error) {
	return SemanticLinkTopicScanPlan{}, errors.New("topic plan should not be called")
}

func (fake *smartCollectionScanPlannerFake) PlanSmartCollectionScan(context.Context, foundation.ID, foundation.ID) (SemanticLinkSmartCollectionScanPlan, error) {
	fake.calls++
	return fake.plan, fake.err
}

func (fake *topicScanPlannerFake) PlanTopicScan(context.Context, foundation.ID, foundation.ID) (SemanticLinkTopicScanPlan, error) {
	fake.calls++
	return fake.plan, fake.err
}

func (fake *scanStateFake) Get(context.Context, foundation.ID, foundation.ID) (graphdomain.SemanticLinkScan, error) {
	return fake.get, nil
}

func (fake *scanStateFake) AdvancePage(context.Context, graphdomain.SemanticLinkScanProgress) (graphdomain.SemanticLinkScan, error) {
	return fake.advance, nil
}

func (fake *scanStateFake) Finish(context.Context, graphdomain.SemanticLinkScanTerminal) (graphdomain.SemanticLinkScan, error) {
	return fake.finish, nil
}

type scanPageSourceFake struct {
	page  SemanticLinkTopicScanPage
	calls int
}

func (fake *scanPageSourceFake) LoadPage(context.Context, SemanticLinkTopicScanPageRequest) (SemanticLinkTopicScanPage, error) {
	fake.calls++
	return fake.page, nil
}

func validApplicationScan(now time.Time) graphdomain.SemanticLinkScan {
	return graphdomain.SemanticLinkScan{
		ID: discoveryAppID(1), WorkspaceID: discoveryAppID(900), WorkflowRunID: discoveryAppID(2),
		Scope:       graphdomain.SemanticLinkScanScope{Type: graphdomain.SemanticLinkScanScopeTopic, Ref: string(discoveryAppID(20)), Version: 1, SchemaVersion: "v1"},
		Fingerprint: strings.Repeat("a", 64), IdempotencyKey: "scan", RequestHash: strings.Repeat("b", 64),
		Status: graphdomain.SemanticLinkScanStatusPending, Version: 1, CreatedAt: now, UpdatedAt: now,
	}
}

func discoveryAppPair(workspaceID foundation.ID, source, target int, label string) graphdomain.SemanticLinkDiscoveryPair {
	node := func(number int) graphdomain.SemanticLinkDiscoveryNode {
		return graphdomain.SemanticLinkDiscoveryNode{
			WorkspaceID: workspaceID,
			Endpoint:    graphdomain.SemanticLinkCandidateEndpoint{Ref: knowledge.NodeRef{Type: knowledge.NodeTypeClaim, ID: discoveryAppID(number)}, Version: 1, Summary: fmt.Sprintf("summary %d", number), Excerpt: fmt.Sprintf("excerpt %d", number)},
			Lifecycle:   knowledge.NodeLifecycleConfirmed, Labels: []string{label},
		}
	}
	return graphdomain.SemanticLinkDiscoveryPair{Source: node(source), Target: node(target)}
}

func discoveryAppRuleGeneration() graphdomain.SemanticLinkCandidateGeneration {
	id := discoveryAppID(700)
	return graphdomain.SemanticLinkCandidateGeneration{RuleID: &id, RuleVersion: "rules/v1"}
}

func discoveryAppModelGeneration() graphdomain.SemanticLinkCandidateGeneration {
	indexID, embeddingID := discoveryAppID(710), discoveryAppID(711)
	return graphdomain.SemanticLinkCandidateGeneration{IndexVersionID: &indexID, EmbeddingVersionID: &embeddingID, ModelVersion: "model/v1", ModelProfileVersion: "profile/v1", PromptVersion: "prompt/v1", SchemaVersion: "schema/v1"}
}

func discoveryAppScanGeneration() graphdomain.SemanticLinkScanGeneration {
	semantic := discoveryAppModelGeneration()
	return graphdomain.SemanticLinkScanGeneration{Rule: discoveryAppRuleGeneration(), Semantic: &semantic, WorkflowVersion: "workflow/v1"}
}

func discoveryAppID(number int) foundation.ID {
	return foundation.ID(fmt.Sprintf("00000000-0000-4000-8000-%012d", number))
}

func appTimePointer(value time.Time) *time.Time { return &value }
