//go:build integration

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"mime"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/app"
	authpostgres "github.com/CodeZen-Lizhi/zhixu/internal/auth/adapter/postgres"
	authapplication "github.com/CodeZen-Lizhi/zhixu/internal/auth/application"
	authhttp "github.com/CodeZen-Lizhi/zhixu/internal/auth/http"
	"github.com/CodeZen-Lizhi/zhixu/internal/capability"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	graphtestfixture "github.com/CodeZen-Lizhi/zhixu/internal/graph/testfixture"
	knowledgepostgres "github.com/CodeZen-Lizhi/zhixu/internal/knowledge/adapter/postgres"
	"github.com/CodeZen-Lizhi/zhixu/internal/knowledge/domain"
	platformmigration "github.com/CodeZen-Lizhi/zhixu/internal/platform/migration"
	platformpostgres "github.com/CodeZen-Lizhi/zhixu/internal/platform/postgres"
	projectmigrations "github.com/CodeZen-Lizhi/zhixu/migrations"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

const timelineImpactBootstrapToken = "timeline-impact-auth-bootstrap-token-at-least-32-bytes"

func TestTimelineImpactPublicHTTPIntegration(t *testing.T) {
	database := newTimelineImpactTestDatabase(t)
	pool := database.DB()
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()

	fixture, err := graphtestfixture.SeedFunctional(ctx, pool)
	if err != nil {
		t.Fatal(err)
	}
	workspaceID := fixture.WorkspaceID
	isolationWorkspaceID := seedTimelineImpactWorkspace(t, ctx, pool, "timeline-impact-isolation")
	eventID := mustTimelineImpactID(t)
	aggregateID := fixture.PrimaryTopicID
	now := time.Now().UTC().Truncate(time.Microsecond)
	event := domain.KnowledgeEvent{
		ID: eventID, WorkspaceID: workspaceID, EventType: domain.EventVersionPublished,
		AggregateType: domain.TimelineAggregateTopic, AggregateID: &aggregateID,
		SourceEventRef: "integration:topic-published:" + string(eventID), SourceRef: "topic:" + string(aggregateID),
		EventVersion: 1, SchemaVersion: domain.KnowledgeEventSchemaVersion, Summary: "Topic version published",
		Payload: json.RawMessage(`{}`), OccurredAt: now, CreatedAt: now,
	}
	repository, err := knowledgepostgres.NewRepository(pool)
	if err != nil {
		t.Fatal(err)
	}
	if persisted, replayed, err := repository.AppendEvent(ctx, event); err != nil || replayed || persisted.ID != event.ID {
		t.Fatalf("append trusted timeline event=%#v replayed=%t err=%v", persisted, replayed, err)
	}

	handler, err := newKnowledgeHandler(pool, 2*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(app.NewRouter(app.Dependencies{
		Version: "timeline-impact-integration", Database: pool, Knowledge: handler,
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
	}))
	t.Cleanup(server.Close)
	client := server.Client()

	listURL := fmt.Sprintf("%s/api/v1/workspaces/%s/timeline?event_type=%s&aggregate_type=%s&aggregate_id=%s&limit=10",
		server.URL, workspaceID, event.EventType, event.AggregateType, aggregateID)
	page := getTimelineImpactResponse[timelineImpactPageWire](t, client, listURL, http.StatusOK)
	if page.WorkspaceID != string(workspaceID) || len(page.Items) != 1 || page.NextCursor != nil {
		t.Fatalf("timeline page=%#v", page)
	}
	item := page.Items[0]
	if item.ID != string(event.ID) || item.WorkspaceID != string(workspaceID) || item.SourceEventRef != event.SourceEventRef ||
		item.EventType != event.EventType || item.AggregateType != event.AggregateType || item.AggregateID == nil || *item.AggregateID != string(aggregateID) ||
		item.EventVersion != 1 || item.SchemaVersion != domain.KnowledgeEventSchemaVersion || item.Summary != event.Summary {
		t.Fatalf("timeline item=%#v", item)
	}

	detailURL := fmt.Sprintf("%s/api/v1/workspaces/%s/timeline/%s", server.URL, workspaceID, eventID)
	detail := getTimelineImpactResponse[timelineImpactEventWire](t, client, detailURL, http.StatusOK)
	if !reflect.DeepEqual(detail, item) {
		t.Fatalf("timeline detail drifted from list: detail=%#v item=%#v", detail, item)
	}
	crossTimelineURL := fmt.Sprintf("%s/api/v1/workspaces/%s/timeline/%s", server.URL, isolationWorkspaceID, eventID)
	crossTimeline := getTimelineImpactResponse[timelineImpactProblemWire](t, client, crossTimelineURL, http.StatusNotFound)
	if crossTimeline.ErrorCode != domain.ErrorCodeTimelineNotFound || crossTimeline.Retryable {
		t.Fatalf("cross-workspace timeline problem=%#v", crossTimeline)
	}

	analysisURL := detailURL + "/impact-analysis"
	created := postTimelineImpactResponse[timelineImpactAnalysisWire](t, client, analysisURL, "timeline-impact-http-1", http.StatusCreated)
	if created.Replayed || created.Report.WorkspaceID != string(workspaceID) || created.Report.SourceEventID != string(eventID) ||
		created.Report.SourceEventRef != event.SourceEventRef || created.Report.SourceEventVersion != 1 || created.Report.Status != domain.ImpactReportReady ||
		created.Report.SchemaVersion != domain.ImpactReportSchemaVersion || created.Report.Version != 1 || len(created.Report.Fingerprint) != 64 ||
		len(created.Report.Objects) != 2 || len(created.ProposalDrafts) != 0 || created.Report.Summary[string(domain.ImpactObjectRelation)] != 2 ||
		created.Report.Summary["action:"+string(domain.ImpactActionReview)] != 2 || created.Report.Summary["requires_proposal"] != 0 {
		t.Fatalf("created impact response=%#v", created)
	}
	for _, object := range created.Report.Objects {
		if object.Type != domain.ImpactObjectRelation || object.Action != domain.ImpactActionReview || object.Reason == "" || object.RequiresProposal {
			t.Fatalf("impact report lost read-only relation semantics: %#v", object)
		}
	}

	replayed := postTimelineImpactResponse[timelineImpactAnalysisWire](t, client, analysisURL, "timeline-impact-http-1", http.StatusOK)
	if !replayed.Replayed || !reflect.DeepEqual(replayed.Report, created.Report) || !reflect.DeepEqual(replayed.ProposalDrafts, created.ProposalDrafts) {
		t.Fatalf("impact replay drifted: created=%#v replayed=%#v", created, replayed)
	}
	replayedWithNewKey := postTimelineImpactResponse[timelineImpactAnalysisWire](t, client, analysisURL, "timeline-impact-http-2", http.StatusOK)
	if !replayedWithNewKey.Replayed || !reflect.DeepEqual(replayedWithNewKey.Report, created.Report) || !reflect.DeepEqual(replayedWithNewKey.ProposalDrafts, created.ProposalDrafts) {
		t.Fatalf("impact replay with new key drifted: created=%#v replayed=%#v", created, replayedWithNewKey)
	}

	reportURL := fmt.Sprintf("%s/api/v1/workspaces/%s/impact-reports/%s", server.URL, workspaceID, created.Report.ID)
	report := getTimelineImpactResponse[timelineImpactReportWire](t, client, reportURL, http.StatusOK)
	if !reflect.DeepEqual(report, created.Report) {
		t.Fatalf("impact report drifted from command: command=%#v report=%#v", created.Report, report)
	}
	crossReportURL := fmt.Sprintf("%s/api/v1/workspaces/%s/impact-reports/%s", server.URL, isolationWorkspaceID, created.Report.ID)
	crossReport := getTimelineImpactResponse[timelineImpactProblemWire](t, client, crossReportURL, http.StatusNotFound)
	if crossReport.ErrorCode != domain.ErrorCodeImpactNotFound || crossReport.Retryable {
		t.Fatalf("cross-workspace impact problem=%#v", crossReport)
	}

	var reports, events, queuedImpactEvents, auditEvents, proposals, approvals int
	if err := pool.QueryRow(ctx, `SELECT
		(SELECT count(*) FROM ops.impact_report WHERE workspace_id=$1 AND source_event_id=$2),
		(SELECT count(*) FROM ops.knowledge_event WHERE workspace_id=$1),
		(SELECT count(*) FROM ops.timeline_projection_outbox WHERE workspace_id=$1 AND event_type='IMPACT_ANALYZED'),
		(SELECT count(*) FROM ops.audit_event WHERE workspace_id=$1 AND action='IMPACT_ANALYZED'),
		(SELECT count(*) FROM change_control.proposal WHERE workspace_id=$1),
		(SELECT count(*) FROM change_control.approval AS approval
			JOIN change_control.proposal AS proposal ON proposal.id=approval.proposal_id
			WHERE proposal.workspace_id=$1)`,
		string(workspaceID), string(eventID)).Scan(&reports, &events, &queuedImpactEvents, &auditEvents, &proposals, &approvals); err != nil {
		t.Fatal(err)
	}
	if reports != 1 || events != 1 || queuedImpactEvents != 1 || auditEvents != 2 || proposals != 0 || approvals != 0 {
		t.Fatalf("impact persistence reports=%d events=%d queued_impact_events=%d audit_events=%d proposals=%d approvals=%d", reports, events, queuedImpactEvents, auditEvents, proposals, approvals)
	}
	var auditID, action, resourceType, resourceRef, idempotencyKey, actorType string
	var payload json.RawMessage
	if err := pool.QueryRow(ctx, `SELECT id::text,action,resource_type,resource_ref,idempotency_key,actor_type,payload
		FROM ops.audit_event WHERE workspace_id=$1 AND action='IMPACT_ANALYZED' AND idempotency_key=$2`, string(workspaceID), "impact:timeline-impact-http-1").Scan(&auditID, &action, &resourceType, &resourceRef, &idempotencyKey, &actorType, &payload); err != nil {
		t.Fatal(err)
	}
	var replayAuditID string
	if err := pool.QueryRow(ctx, `SELECT id::text FROM ops.audit_event WHERE workspace_id=$1 AND action='IMPACT_ANALYZED' AND idempotency_key=$2`, string(workspaceID), "impact:timeline-impact-http-2").Scan(&replayAuditID); err != nil {
		t.Fatal(err)
	}
	var auditPayload map[string]json.RawMessage
	if err := json.Unmarshal(payload, &auditPayload); err != nil {
		t.Fatal(err)
	}
	if auditID == replayAuditID || action != "IMPACT_ANALYZED" || resourceType != "IMPACT_REPORT" || resourceRef != created.Report.ID || idempotencyKey != "impact:timeline-impact-http-1" || actorType != "ANONYMOUS" || string(auditPayload["object_count"]) != "2" {
		t.Fatalf("impact audit id=%q replay_id=%q action=%q resource_type=%q resource_ref=%q key=%q actor=%q payload=%s", auditID, replayAuditID, action, resourceType, resourceRef, idempotencyKey, actorType, payload)
	}
}

type timelineImpactAuthenticatedFixture struct {
	ctx         context.Context
	pool        *pgxpool.Pool
	workspaceID foundation.ID
	eventID     foundation.ID
	authService *authapplication.Service
	server      *httptest.Server
}

func newTimelineImpactAuthenticatedFixture(t *testing.T) timelineImpactAuthenticatedFixture {
	t.Helper()
	database := newTimelineImpactTestDatabase(t)
	pool := database.DB()
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	t.Cleanup(cancel)

	fixture, err := graphtestfixture.SeedFunctional(ctx, pool)
	if err != nil {
		t.Fatal(err)
	}
	eventID := mustTimelineImpactID(t)
	now := time.Now().UTC().Truncate(time.Microsecond)
	event := domain.KnowledgeEvent{
		ID: eventID, WorkspaceID: fixture.WorkspaceID, EventType: domain.EventVersionPublished,
		AggregateType: domain.TimelineAggregateTopic, AggregateID: &fixture.PrimaryTopicID,
		SourceEventRef: "integration:authenticated-impact:" + string(eventID), SourceRef: "topic:" + string(fixture.PrimaryTopicID),
		EventVersion: 1, SchemaVersion: domain.KnowledgeEventSchemaVersion, Summary: "Authenticated impact analysis",
		Payload: json.RawMessage(`{}`), OccurredAt: now, CreatedAt: now,
	}
	knowledgeRepository, err := knowledgepostgres.NewRepository(pool)
	if err != nil {
		t.Fatal(err)
	}
	if persisted, replayed, err := knowledgeRepository.AppendEvent(ctx, event); err != nil || replayed || persisted.ID != event.ID {
		t.Fatalf("append authenticated timeline event=%#v replayed=%t err=%v", persisted, replayed, err)
	}

	authRepository, err := authpostgres.NewRepository(pool)
	if err != nil {
		t.Fatal(err)
	}
	authService, err := authapplication.NewService(authRepository, foundation.NewUUIDGenerator(nil), foundation.SystemClock{}, authapplication.Options{
		BootstrapToken: timelineImpactBootstrapToken,
		SessionTTL:     time.Hour,
		APITokenTTL:    time.Hour,
	})
	if err != nil {
		t.Fatal(err)
	}
	authHandler, err := authhttp.NewHandler(authService, authhttp.Options{SecureCookie: false, AllowedOrigins: []string{"https://timeline-impact.test"}})
	if err != nil {
		t.Fatal(err)
	}
	knowledgeHandler, err := newKnowledgeHandler(pool, 2*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(app.NewRouter(app.Dependencies{
		Version: "timeline-impact-authenticated-integration", Database: pool, Knowledge: knowledgeHandler,
		Auth: authHandler, AuthRequired: true, Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
	}))
	t.Cleanup(server.Close)
	return timelineImpactAuthenticatedFixture{
		ctx: ctx, pool: pool, workspaceID: fixture.WorkspaceID, eventID: eventID, authService: authService, server: server,
	}
}

func TestTimelineImpactAuthenticatedAPITokenAuditIntegration(t *testing.T) {
	authenticated := newTimelineImpactAuthenticatedFixture(t)
	session, err := authenticated.authService.ExchangeBootstrap(authenticated.ctx, timelineImpactBootstrapToken)
	if err != nil {
		t.Fatal(err)
	}
	owner, err := authenticated.authService.AuthenticateSession(authenticated.ctx, session.Token, session.CSRFToken, true)
	if err != nil {
		t.Fatal(err)
	}
	listURL := fmt.Sprintf("%s/api/v1/workspaces/%s/timeline", authenticated.server.URL, authenticated.workspaceID)
	detailURL := fmt.Sprintf("%s/api/v1/workspaces/%s/timeline/%s", authenticated.server.URL, authenticated.workspaceID, authenticated.eventID)
	analysisURL := detailURL + "/impact-analysis"
	for _, endpoint := range []struct {
		name   string
		method string
		target string
		body   string
	}{
		{name: "timeline list", method: http.MethodGet, target: listURL},
		{name: "timeline detail", method: http.MethodGet, target: detailURL},
		{name: "impact analysis", method: http.MethodPost, target: analysisURL, body: `{}`},
	} {
		t.Run("anonymous "+endpoint.name, func(t *testing.T) {
			request := newTimelineImpactHTTPRequest(t, authenticated.ctx, endpoint.method, endpoint.target, endpoint.body, "")
			problem := doTimelineImpactResponse[timelineImpactProblemWire](t, authenticated.server.Client(), request, http.StatusUnauthorized)
			if problem.ErrorCode != authapplication.ErrorCodeUnauthorized {
				t.Fatalf("anonymous %s problem=%#v", endpoint.name, problem)
			}
		})
	}

	readToken, err := authenticated.authService.CreateAPIToken(authenticated.ctx, owner, "timeline-read-only", []capability.Capability{capability.ReadLocal}, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	for _, endpoint := range []struct {
		name   string
		target string
	}{
		{name: "timeline list", target: listURL},
		{name: "timeline detail", target: detailURL},
	} {
		t.Run("read token "+endpoint.name, func(t *testing.T) {
			request := newTimelineImpactHTTPRequest(t, authenticated.ctx, http.MethodGet, endpoint.target, "", readToken.Plain)
			response := doTimelineImpactResponse[json.RawMessage](t, authenticated.server.Client(), request, http.StatusOK)
			if len(response) == 0 {
				t.Fatalf("read token %s returned an empty response", endpoint.name)
			}
		})
	}
	insufficient := newTimelineImpactHTTPRequest(t, authenticated.ctx, http.MethodPost, analysisURL, `{}`, readToken.Plain)
	insufficient.Header.Set("Idempotency-Key", "timeline-impact-insufficient-scope")
	insufficientProblem := doTimelineImpactResponse[timelineImpactProblemWire](t, authenticated.server.Client(), insufficient, http.StatusForbidden)
	if insufficientProblem.ErrorCode != authapplication.ErrorCodeForbidden {
		t.Fatalf("read-only impact problem=%#v", insufficientProblem)
	}

	apiToken, err := authenticated.authService.CreateAPIToken(authenticated.ctx, owner, "timeline-impact-audit", []capability.Capability{capability.WriteProposal}, time.Hour)
	if err != nil {
		t.Fatal(err)
	}

	request := newTimelineImpactHTTPRequest(t, authenticated.ctx, http.MethodPost, analysisURL, `{}`, apiToken.Plain)
	request.Header.Set("Idempotency-Key", "timeline-impact-authenticated-1")
	result := doTimelineImpactResponse[timelineImpactAnalysisWire](t, authenticated.server.Client(), request, http.StatusCreated)
	if result.Replayed || result.Report.WorkspaceID != string(authenticated.workspaceID) || result.Report.SourceEventID != string(authenticated.eventID) {
		t.Fatalf("authenticated impact result=%#v", result)
	}

	var actorType, actorRef string
	var lastUsedAt *time.Time
	if err := authenticated.pool.QueryRow(authenticated.ctx, `SELECT audit.actor_type,audit.actor_ref,token.last_used_at
		FROM ops.audit_event AS audit
		JOIN auth.api_token AS token ON token.id=$3::uuid
		WHERE audit.workspace_id=$1::uuid AND audit.action='IMPACT_ANALYZED' AND audit.idempotency_key=$2`,
		string(authenticated.workspaceID), "impact:timeline-impact-authenticated-1", string(apiToken.Token.ID)).Scan(&actorType, &actorRef, &lastUsedAt); err != nil {
		t.Fatal(err)
	}
	if actorType != "API_TOKEN" || actorRef != string(apiToken.Token.ID) || lastUsedAt == nil {
		t.Fatalf("authenticated audit actor_type=%q actor_ref=%q token_last_used_at=%v", actorType, actorRef, lastUsedAt)
	}

	reportURL := fmt.Sprintf("%s/api/v1/workspaces/%s/impact-reports/%s", authenticated.server.URL, authenticated.workspaceID, result.Report.ID)
	readReport := newTimelineImpactHTTPRequest(t, authenticated.ctx, http.MethodGet, reportURL, "", readToken.Plain)
	report := doTimelineImpactResponse[timelineImpactReportWire](t, authenticated.server.Client(), readReport, http.StatusOK)
	if report.ID != result.Report.ID || report.SourceEventID != string(authenticated.eventID) {
		t.Fatalf("read-only report=%#v", report)
	}
	anonymousReport := newTimelineImpactHTTPRequest(t, authenticated.ctx, http.MethodGet, reportURL, "", "")
	reportProblem := doTimelineImpactResponse[timelineImpactProblemWire](t, authenticated.server.Client(), anonymousReport, http.StatusUnauthorized)
	if reportProblem.ErrorCode != authapplication.ErrorCodeUnauthorized {
		t.Fatalf("anonymous report problem=%#v", reportProblem)
	}
}

func TestTimelineImpactAuthenticatedSessionAuditIntegration(t *testing.T) {
	authenticated := newTimelineImpactAuthenticatedFixture(t)
	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	client := authenticated.server.Client()
	client.Jar = jar
	bootstrapURL := authenticated.server.URL + "/api/v1/auth/sessions"
	bootstrap, err := http.NewRequestWithContext(authenticated.ctx, http.MethodPost, bootstrapURL, nil)
	if err != nil {
		t.Fatal(err)
	}
	bootstrap.Header.Set("Authorization", "Bearer "+timelineImpactBootstrapToken)
	bootstrap.Header.Set("Origin", "https://timeline-impact.test")
	session := doTimelineImpactResponse[timelineImpactSessionWire](t, client, bootstrap, http.StatusCreated)
	sessionID, err := foundation.ParseID(session.SessionID)
	if err != nil || string(sessionID) != session.SessionID || session.CSRFToken == "" || session.ExpiresAt == "" {
		t.Fatalf("bootstrap session=%#v parse_err=%v", session, err)
	}
	serverURL, err := url.Parse(authenticated.server.URL)
	if err != nil {
		t.Fatal(err)
	}
	foundSessionCookie := false
	for _, cookie := range jar.Cookies(serverURL) {
		if cookie.Name == authhttp.SessionCookieName && cookie.Value != "" {
			foundSessionCookie = true
		}
	}
	if !foundSessionCookie {
		t.Fatal("bootstrap did not retain a session cookie")
	}

	var initialLastSeenAt time.Time
	if err := authenticated.pool.QueryRow(authenticated.ctx, `SELECT last_seen_at FROM auth.session WHERE id=$1::uuid`, string(sessionID)).Scan(&initialLastSeenAt); err != nil {
		t.Fatal(err)
	}
	analysisURL := fmt.Sprintf("%s/api/v1/workspaces/%s/timeline/%s/impact-analysis", authenticated.server.URL, authenticated.workspaceID, authenticated.eventID)
	for _, invalid := range []struct {
		name  string
		setup func(*http.Request)
	}{
		{name: "missing origin", setup: func(request *http.Request) { request.Header.Set(authhttp.CSRFHeaderName, session.CSRFToken) }},
		{name: "missing csrf", setup: func(request *http.Request) { request.Header.Set("Origin", "https://timeline-impact.test") }},
		{name: "untrusted origin", setup: func(request *http.Request) {
			request.Header.Set("Origin", "https://evil.example")
			request.Header.Set(authhttp.CSRFHeaderName, session.CSRFToken)
		}},
	} {
		t.Run(invalid.name, func(t *testing.T) {
			request := newTimelineImpactHTTPRequest(t, authenticated.ctx, http.MethodPost, analysisURL, `{}`, "")
			request.Header.Set("Idempotency-Key", "timeline-impact-session-rejected-"+strings.ReplaceAll(invalid.name, " ", "-"))
			invalid.setup(request)
			problem := doTimelineImpactResponse[timelineImpactProblemWire](t, client, request, http.StatusForbidden)
			if problem.ErrorCode != authapplication.ErrorCodeCSRF {
				t.Fatalf("%s problem=%#v", invalid.name, problem)
			}
		})
	}
	time.Sleep(2 * time.Millisecond)
	request := newTimelineImpactHTTPRequest(t, authenticated.ctx, http.MethodPost, analysisURL, `{}`, "")
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Idempotency-Key", "timeline-impact-session-1")
	request.Header.Set("Origin", "https://timeline-impact.test")
	request.Header.Set(authhttp.CSRFHeaderName, session.CSRFToken)
	result := doTimelineImpactResponse[timelineImpactAnalysisWire](t, client, request, http.StatusCreated)
	if result.Replayed || result.Report.WorkspaceID != string(authenticated.workspaceID) || result.Report.SourceEventID != string(authenticated.eventID) {
		t.Fatalf("session impact result=%#v", result)
	}

	var actorType, actorRef string
	var lastSeenAt time.Time
	if err := authenticated.pool.QueryRow(authenticated.ctx, `SELECT audit.actor_type,audit.actor_ref,session.last_seen_at
		FROM ops.audit_event AS audit
		JOIN auth.session AS session ON session.id=$3::uuid
		WHERE audit.workspace_id=$1::uuid AND audit.action='IMPACT_ANALYZED' AND audit.idempotency_key=$2`,
		string(authenticated.workspaceID), "impact:timeline-impact-session-1", string(sessionID)).Scan(&actorType, &actorRef, &lastSeenAt); err != nil {
		t.Fatal(err)
	}
	if actorType != "USER" || actorRef != string(sessionID) || !lastSeenAt.After(initialLastSeenAt) {
		t.Fatalf("session audit actor_type=%q actor_ref=%q last_seen_at=%s initial_last_seen_at=%s", actorType, actorRef, lastSeenAt, initialLastSeenAt)
	}
}

type timelineImpactPageWire struct {
	WorkspaceID string                    `json:"workspace_id"`
	Items       []timelineImpactEventWire `json:"items"`
	NextCursor  *string                   `json:"next_cursor,omitempty"`
}

type timelineImpactEventWire struct {
	ID             string                       `json:"id"`
	WorkspaceID    string                       `json:"workspace_id"`
	EventType      domain.EventType             `json:"event_type"`
	AggregateType  domain.TimelineAggregateType `json:"aggregate_type"`
	AggregateID    *string                      `json:"aggregate_id,omitempty"`
	SourceEventRef string                       `json:"source_event_ref"`
	SourceRef      string                       `json:"source_ref"`
	EventVersion   int                          `json:"event_version"`
	SchemaVersion  string                       `json:"schema_version"`
	Summary        string                       `json:"summary"`
	Payload        json.RawMessage              `json:"payload"`
	Correlation    domain.EventCorrelation      `json:"correlation"`
	OccurredAt     string                       `json:"occurred_at"`
	CreatedAt      string                       `json:"created_at"`
}

type timelineImpactAnalysisWire struct {
	Report         timelineImpactReportWire `json:"report"`
	ProposalDrafts []domain.ProposalDraft   `json:"proposal_drafts"`
	Replayed       bool                     `json:"replayed"`
}

type timelineImpactSessionWire struct {
	SessionID string `json:"session_id"`
	CSRFToken string `json:"csrf_token"`
	ExpiresAt string `json:"expires_at"`
}

type timelineImpactReportWire struct {
	ID                 string                    `json:"id"`
	WorkspaceID        string                    `json:"workspace_id"`
	SourceEventID      string                    `json:"source_event_id"`
	SourceEventRef     string                    `json:"source_event_ref"`
	SourceEventVersion int64                     `json:"source_event_version"`
	Status             domain.ImpactReportStatus `json:"status"`
	Objects            []domain.ImpactObject     `json:"objects"`
	Summary            map[string]int            `json:"summary"`
	Fingerprint        string                    `json:"fingerprint"`
	ErrorCode          *string                   `json:"error_code,omitempty"`
	StaleReason        *string                   `json:"stale_reason,omitempty"`
	SchemaVersion      string                    `json:"schema_version"`
	GeneratedAt        string                    `json:"generated_at"`
	CreatedAt          string                    `json:"created_at"`
	Version            int64                     `json:"version"`
}

type timelineImpactProblemWire struct {
	ErrorCode     string                     `json:"error_code"`
	Message       string                     `json:"message"`
	Retryable     bool                       `json:"retryable"`
	WorkflowRunID string                     `json:"workflow_run_id,omitempty"`
	Details       map[string]json.RawMessage `json:"details,omitempty"`
}

func getTimelineImpactResponse[T any](t *testing.T, client *http.Client, target string, wantStatus int) T {
	t.Helper()
	request, err := http.NewRequestWithContext(t.Context(), http.MethodGet, target, nil)
	if err != nil {
		t.Fatal(err)
	}
	return doTimelineImpactResponse[T](t, client, request, wantStatus)
}

func postTimelineImpactResponse[T any](t *testing.T, client *http.Client, target, idempotencyKey string, wantStatus int) T {
	t.Helper()
	request, err := http.NewRequestWithContext(t.Context(), http.MethodPost, target, bytes.NewReader([]byte(`{}`)))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Idempotency-Key", idempotencyKey)
	return doTimelineImpactResponse[T](t, client, request, wantStatus)
}

func newTimelineImpactHTTPRequest(t *testing.T, ctx context.Context, method, target, body, bearer string) *http.Request {
	t.Helper()
	request, err := http.NewRequestWithContext(ctx, method, target, bytes.NewReader([]byte(body)))
	if err != nil {
		t.Fatal(err)
	}
	if body != "" {
		request.Header.Set("Content-Type", "application/json")
	}
	if bearer != "" {
		request.Header.Set("Authorization", "Bearer "+bearer)
	}
	return request
}

func doTimelineImpactResponse[T any](t *testing.T, client *http.Client, request *http.Request, wantStatus int) T {
	t.Helper()
	response, err := client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	body, err := io.ReadAll(io.LimitReader(response.Body, 2*1024*1024+1))
	if err != nil {
		t.Fatal(err)
	}
	if len(body) > 2*1024*1024 {
		t.Fatal("Timeline/Impact response exceeds integration test limit")
	}
	for _, forbidden := range [][]byte{[]byte("/tmp/"), []byte("root_path"), []byte("postgres://")} {
		if bytes.Contains(body, forbidden) {
			t.Fatalf("Timeline/Impact response exposed internal data %q", forbidden)
		}
	}
	if response.StatusCode != wantStatus {
		t.Fatalf("%s %s status=%d want=%d body=%s", request.Method, request.URL, response.StatusCode, wantStatus, body)
	}
	mediaType, _, err := mime.ParseMediaType(response.Header.Get("Content-Type"))
	if err != nil || mediaType != "application/json" {
		t.Fatalf("content-type=%q: %v", response.Header.Get("Content-Type"), err)
	}
	var result T
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&result); err != nil {
		t.Fatalf("decode Timeline/Impact response: %v; body=%s", err, body)
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		t.Fatalf("Timeline/Impact response has trailing JSON: %v; body=%s", err, body)
	}
	return result
}

func seedTimelineImpactWorkspace(t *testing.T, ctx context.Context, pool *pgxpool.Pool, label string) foundation.ID {
	t.Helper()
	workspaceID := mustTimelineImpactID(t)
	now := time.Now().UTC().Truncate(time.Microsecond)
	root := "/tmp/" + label + "-" + string(workspaceID)
	if _, err := pool.Exec(ctx, `INSERT INTO core.workspace(
		id,name,root_path,git_repository_path,git_checked_at,status,version,created_at,updated_at
	) VALUES($1,$2,$3,$3,$4,'test',1,$4,$4)`, string(workspaceID), label, root, now); err != nil {
		t.Fatal(err)
	}
	return workspaceID
}

func mustTimelineImpactID(t *testing.T) foundation.ID {
	t.Helper()
	id, err := foundation.NewUUIDGenerator(nil).New()
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func newTimelineImpactTestDatabase(t *testing.T) *platformpostgres.Pool {
	t.Helper()
	baseURL := strings.TrimSpace(os.Getenv("ZHIXU_TEST_DATABASE_URL"))
	if baseURL == "" {
		t.Skip("set ZHIXU_TEST_DATABASE_URL to a PostgreSQL admin database")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Minute)
	defer cancel()
	parsed, err := url.Parse(baseURL)
	if err != nil {
		t.Fatal(err)
	}
	admin, err := pgxpool.New(ctx, baseURL)
	if err != nil {
		t.Fatal(err)
	}
	name := fmt.Sprintf("zhixu_timeline_impact_%d", time.Now().UnixNano())
	identifier := pgx.Identifier{name}.Sanitize()
	if _, err := admin.Exec(ctx, "CREATE DATABASE "+identifier); err != nil {
		admin.Close()
		t.Fatal(err)
	}
	parsed.Path = "/" + name
	databaseURL := parsed.String()
	migrationPool, err := platformpostgres.OpenMigration(ctx, databaseURL, 4, 0)
	if err == nil {
		var runner *platformmigration.Runner
		runner, err = platformmigration.NewRunner(migrationPool.DB(), projectmigrations.FS)
		if err == nil {
			err = runner.Up(ctx)
		}
		migrationPool.Close()
	}
	if err != nil {
		_, _ = admin.Exec(ctx, "DROP DATABASE "+identifier+" WITH (FORCE)")
		admin.Close()
		t.Fatal(err)
	}
	database, err := platformpostgres.Open(ctx, databaseURL, 4, 0)
	if err != nil {
		_, _ = admin.Exec(ctx, "DROP DATABASE "+identifier+" WITH (FORCE)")
		admin.Close()
		t.Fatal(err)
	}
	if err := database.Ping(ctx); err != nil {
		database.Close()
		_, _ = admin.Exec(ctx, "DROP DATABASE "+identifier+" WITH (FORCE)")
		admin.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() {
		database.Close()
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cleanupCancel()
		_, _ = admin.Exec(cleanupCtx, "DROP DATABASE "+identifier+" WITH (FORCE)")
		admin.Close()
	})
	return database
}
