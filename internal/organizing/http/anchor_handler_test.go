package organizinghttp

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	app "github.com/CodeZen-Lizhi/zhixu/internal/organizing/application"
	"github.com/CodeZen-Lizhi/zhixu/internal/organizing/domain"
	"github.com/gin-gonic/gin"
)

type anchorHTTPStub struct {
	app.AnchorService
	decide func(context.Context, app.DecideAnchorCommand) (app.AnchorDecisionResult, error)
}

func (s anchorHTTPStub) DecideAnchor(ctx context.Context, c app.DecideAnchorCommand) (app.AnchorDecisionResult, error) {
	return s.decide(ctx, c)
}
func TestAnchorHTTPRejectsInvalidBeforeOwner(t *testing.T) {
	router := gin.New()
	NewAnchorHandler(anchorHTTPStub{}, time.Second).Routes(router.Group("/api/v1"))
	base := "/api/v1/workspaces/" + string(testID(1)) + "/synthesis/anchors"
	for _, body := range []string{`{}`, `{"scope":null}`, `{"note_id":null}`, `{"workspace_id":"` + string(testID(2)) + `"}`, `{"title":"a","title":"b"}`} {
		r := httptest.NewRecorder()
		router.ServeHTTP(r, newJSONRequest(http.MethodPost, base, body, "create"))
		if r.Code != 400 {
			t.Fatalf("invalid create %s: %d %s", body, r.Code, r.Body.String())
		}
	}
	for _, body := range []string{`{}`, `{"expected_anchor_version":1,"decision":"ACCEPTED","items":[]}`, `{"expected_anchor_version":1,"decision":"PENDING","items":[{"proposal_id":"` + string(testID(3)) + `","expected_version":1}]}`} {
		r := httptest.NewRecorder()
		router.ServeHTTP(r, newJSONRequest(http.MethodPost, base+"/"+string(testID(2))+"/associations/decisions", body, "decision"))
		if r.Code != 400 {
			t.Fatalf("invalid decision %d %s", r.Code, r.Body.String())
		}
	}
	for _, query := range []string{"?limit=101", "?limit=01", "?limit=1&limit=2", "?unknown=true", "?after_id=bad"} {
		r := httptest.NewRecorder()
		router.ServeHTTP(r, httptest.NewRequest(http.MethodGet, base+query, nil))
		if r.Code != 400 {
			t.Fatalf("invalid list %s: %d", query, r.Code)
		}
	}
}
func TestAnchorHTTPBindsDecisionRouteAndRejectsForgedResponse(t *testing.T) {
	called := false
	stub := anchorHTTPStub{decide: func(_ context.Context, c app.DecideAnchorCommand) (app.AnchorDecisionResult, error) {
		called = true
		if c.WorkspaceID != testID(1) || c.AnchorID != testID(2) || c.Kind != domain.AnchorSourceAssociation || c.IdempotencyKey != "decision" {
			t.Fatal(c)
		}
		return app.AnchorDecisionResult{}, nil
	}}
	router := gin.New()
	NewAnchorHandler(stub, time.Second).Routes(router.Group("/api/v1"))
	base := "/api/v1/workspaces/" + string(testID(1)) + "/synthesis/anchors/" + string(testID(2)) + "/associations/decisions"
	body := `{"expected_anchor_version":1,"decision":"ACCEPTED","items":[{"proposal_id":"` + string(testID(3)) + `","expected_version":1}]}`
	r := httptest.NewRecorder()
	router.ServeHTTP(r, newJSONRequest(http.MethodPost, base, body, "decision"))
	if !called || r.Code != http.StatusConflict {
		t.Fatalf("owner binding/response validation %v %d", called, r.Code)
	}
}
