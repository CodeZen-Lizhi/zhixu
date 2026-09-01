//go:build integration

package collectionhttp

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/collection/adapter/postgres"
	collectionapp "github.com/CodeZen-Lizhi/zhixu/internal/collection/application"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	platformpostgres "github.com/CodeZen-Lizhi/zhixu/internal/platform/postgres"
	"github.com/CodeZen-Lizhi/zhixu/internal/platform/testdb"
	"github.com/gin-gonic/gin"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestCollectionHTTPExactReplayAfterMutationAndArchive(t *testing.T) {
	variants := []struct {
		name string
		open func(*platformpostgres.Pool) (collectionapp.Repository, error)
	}{
		{name: "legacy", open: func(pool *platformpostgres.Pool) (collectionapp.Repository, error) {
			return postgres.NewRepository(pool.DB())
		}},
		{name: "gorm", open: func(pool *platformpostgres.Pool) (collectionapp.Repository, error) {
			return postgres.NewGORMRepository(pool)
		}},
	}
	for _, variant := range variants {
		variant := variant
		t.Run(variant.name, func(t *testing.T) {
			fixture := testdb.Require(t, testdb.Config{
				ExternalAdminURL: strings.TrimSpace(os.Getenv("ZHIXU_TEST_DATABASE_URL")),
				Availability:     testdb.FailWhenUnavailable,
				MaxConns:         8,
			})
			ctx, cancel := context.WithTimeout(t.Context(), 60*time.Second)
			defer cancel()
			repository, err := variant.open(fixture.Pool())
			if err != nil {
				t.Fatal(err)
			}
			testCollectionHTTPExactReplayAfterMutationAndArchive(t, ctx, fixture.Pool().DB(), repository)
		})
	}
}

func testCollectionHTTPExactReplayAfterMutationAndArchive(t *testing.T, ctx context.Context, pool *pgxpool.Pool, repository collectionapp.Repository) {
	t.Helper()
	now := time.Date(2026, 7, 22, 3, 0, 0, 0, time.UTC)
	workspaceID := foundation.ID("92000000-0000-4000-8000-000000000001")
	if _, err := pool.Exec(ctx, `INSERT INTO core.workspace(id,name,root_path,git_repository_path,git_checked_at,status,version,created_at,updated_at) VALUES($1,'collection-http-replay',$2,$2,$3,'inactive',1,$3,$3)`, string(workspaceID), "/tmp/collection-http-replay", now); err != nil {
		t.Fatal(err)
	}
	collectionID := foundation.ID("92000000-0000-4000-8000-000000000002")
	service, err := collectionapp.NewService(collectionapp.Dependencies{
		Repository: repository,
		IDs:        &httpReplayIDs{values: []foundation.ID{collectionID, "92000000-0000-4000-8000-000000000003"}},
		Clock:      foundation.FixedClock{Value: now},
	})
	if err != nil {
		t.Fatal(err)
	}
	router := gin.New()
	NewHandler(service, time.Second).Routes(router.Group("/api/v1"))

	createBody := `{"workspace_id":"` + string(workspaceID) + `","name":"Inbox","description":"first","query":{"schema_version":"collection-query/v1","root":{"kind":"group","operator":"AND","clauses":[{"kind":"predicate","field":"object_type","operator":"EQ","value":"TOPIC"}]}},"view_type":"LIST","view_config":{"density":"COMFORTABLE"}}`
	created := httpReplayRequest(t, router, http.MethodPost, "/collections", createBody, "create-http-replay")
	if created.Code != http.StatusCreated {
		t.Fatalf("create status=%d body=%s", created.Code, created.Body.String())
	}
	var createdResponse collectionResponse
	if err := json.Unmarshal(created.Body.Bytes(), &createdResponse); err != nil {
		t.Fatal(err)
	}

	updateBody := `{"workspace_id":"` + string(workspaceID) + `","name":"Inbox Updated","description":"updated","expected_version":1,"query":{"schema_version":"collection-query/v1","root":{"kind":"group","operator":"AND","clauses":[{"kind":"predicate","field":"object_type","operator":"EQ","value":"TOPIC"}]}},"view_type":"TABLE","view_config":{"columns":["title"],"density":"COMPACT"}}`
	updated := httpReplayRequest(t, router, http.MethodPut, "/collections/"+string(collectionID), updateBody, "update-http-replay")
	if updated.Code != http.StatusOK {
		t.Fatalf("update status=%d body=%s", updated.Code, updated.Body.String())
	}
	var updatedResponse collectionResponse
	if err := json.Unmarshal(updated.Body.Bytes(), &updatedResponse); err != nil {
		t.Fatal(err)
	}
	archived := httpReplayRequest(t, router, http.MethodPost, "/collections/"+string(collectionID)+"/archive", `{"workspace_id":"`+string(workspaceID)+`","expected_version":2}`, "archive-http-replay")
	if archived.Code != http.StatusOK {
		t.Fatalf("archive status=%d body=%s", archived.Code, archived.Body.String())
	}

	createReplay := httpReplayRequest(t, router, http.MethodPost, "/collections", createBody, "create-http-replay")
	if createReplay.Code != http.StatusOK {
		t.Fatalf("create replay status=%d body=%s", createReplay.Code, createReplay.Body.String())
	}
	var createReplayResponse collectionResponse
	if err := json.Unmarshal(createReplay.Body.Bytes(), &createReplayResponse); err != nil {
		t.Fatal(err)
	}
	if createReplayResponse.Version != createdResponse.Version || createReplayResponse.Name != createdResponse.Name || createReplayResponse.Status != createdResponse.Status {
		t.Fatalf("create replay returned current resource: replay=%+v original=%+v", createReplayResponse, createdResponse)
	}
	updateReplay := httpReplayRequest(t, router, http.MethodPut, "/collections/"+string(collectionID), updateBody, "update-http-replay")
	if updateReplay.Code != http.StatusOK {
		t.Fatalf("update replay status=%d body=%s", updateReplay.Code, updateReplay.Body.String())
	}
	var updateReplayResponse collectionResponse
	if err := json.Unmarshal(updateReplay.Body.Bytes(), &updateReplayResponse); err != nil {
		t.Fatal(err)
	}
	if updateReplayResponse.Version != updatedResponse.Version || updateReplayResponse.Name != updatedResponse.Name || updateReplayResponse.Status != updatedResponse.Status {
		t.Fatalf("update replay returned current resource: replay=%+v original=%+v", updateReplayResponse, updatedResponse)
	}
}

func httpReplayRequest(t *testing.T, router http.Handler, method, path, body, key string) *httptest.ResponseRecorder {
	t.Helper()
	request := httptest.NewRequest(method, "/api/v1"+path, bytes.NewBufferString(body))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Idempotency-Key", key)
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	return response
}

type httpReplayIDs struct {
	values []foundation.ID
	index  int
}

func (g *httpReplayIDs) New() (foundation.ID, error) {
	value := g.values[g.index]
	g.index++
	return value, nil
}
