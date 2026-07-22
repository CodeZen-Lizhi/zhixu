package postgres

import "testing"

func TestProposalRequestHashReplayMatchesOnlyAllowsForwardCompatibility(t *testing.T) {
	const (
		requestHashV1 = "v1"
		requestHashV2 = "v2"
	)
	tests := []struct {
		name          string
		storedHash    string
		requestedHash string
		want          bool
	}{
		{name: "v1 exact", storedHash: requestHashV1, requestedHash: requestHashV1, want: true},
		{name: "v2 replays historical v1", storedHash: requestHashV1, requestedHash: requestHashV2, want: true},
		{name: "v2 exact", storedHash: requestHashV2, requestedHash: requestHashV2, want: true},
		{name: "v1 cannot replay v2", storedHash: requestHashV2, requestedHash: requestHashV1, want: false},
		{name: "unknown stored hash", storedHash: "unknown", requestedHash: requestHashV2, want: false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := proposalRequestHashReplayMatches(test.storedHash, test.requestedHash, requestHashV2, requestHashV1); got != test.want {
				t.Fatalf("replay match = %v, want %v", got, test.want)
			}
		})
	}
}
