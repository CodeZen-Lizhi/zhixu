package application

import "testing"

func TestAnchorRecommendationStartInputAcceptsDatabaseJSONAndRejectsAmbiguity(t *testing.T) {
	const id = "00000000-0000-4000-8000-000000000001"
	// PostgreSQL jsonb 在回读时会改变空白和对象键顺序。
	for _, raw := range []string{
		`{"expected_version":1,"request_id":"` + id + `"}`,
		`{ "request_id": "` + id + `", "expected_version": 1 }`,
	} {
		got, err := DecodeAnchorRecommendationStartInput([]byte(raw))
		if err != nil || string(got.RequestID) != id || got.ExpectedVersion != 1 {
			t.Fatalf("database input: %+v %v", got, err)
		}
	}
	for _, raw := range []string{
		`{"request_id":"` + id + `"}`,
		`{"request_id":"` + id + `","expected_version":0}`,
		`{"request_id":"` + id + `","expected_version":1,"extra":true}`,
		`{"request_id":"` + id + `","expected_version":1,"expected_version":2}`,
		`{"request_id":"` + id + `","expected_version":1} {}`,
	} {
		if _, err := DecodeAnchorRecommendationStartInput([]byte(raw)); err == nil {
			t.Fatalf("accepted ambiguous input: %s", raw)
		}
	}
}
