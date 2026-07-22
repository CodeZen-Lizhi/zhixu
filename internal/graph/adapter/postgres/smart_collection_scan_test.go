package postgres

import (
	"testing"

	collectionapp "github.com/CodeZen-Lizhi/zhixu/internal/collection/application"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
)

func TestDurableScanPairKeysKeepMixedEndpointsUniqueAndOrdered(t *testing.T) {
	claim := collectionapp.DurableScanKey{ObjectType: "CLAIM", ID: foundation.ID("93000000-0000-4000-8000-000000000003")}
	topic := collectionapp.DurableScanKey{ObjectType: "TOPIC", ID: foundation.ID("93000000-0000-4000-8000-000000000002")}
	keys := durableScanPairKeys([]collectionapp.DurableScanPair{{Source: claim, Target: topic}, {Source: claim, Target: topic}})
	if len(keys) != 2 || keys[0] != claim || keys[1] != topic {
		t.Fatalf("keys=%#v", keys)
	}
}
