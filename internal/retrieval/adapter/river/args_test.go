package river

import (
	"errors"
	"testing"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
)

func TestArgsRoundTripUsesOnlyStableDeliveryIdentity(t *testing.T) {
	t.Parallel()
	args, err := NewArgs("10000000-0000-4000-8000-000000000001", 3)
	if err != nil {
		t.Fatal(err)
	}
	if args.Kind() != JobKind || args.SchemaVersion != JobSchemaVersion || args.DispatchNo != 3 {
		t.Fatalf("args=%#v kind=%q", args, args.Kind())
	}
	decoded, err := DecodeStrict([]byte(`{"schema_version":1,"delivery_id":"10000000-0000-4000-8000-000000000001","dispatch_no":3}`))
	if err != nil || decoded != args {
		t.Fatalf("DecodeStrict()=%#v, %v", decoded, err)
	}
}

func TestArgsRejectUnknownTrailingAndInvalidIdentity(t *testing.T) {
	t.Parallel()
	for _, encoded := range [][]byte{
		[]byte(`{"schema_version":1,"delivery_id":"10000000-0000-4000-8000-000000000001","dispatch_no":1,"path":"/secret"}`),
		[]byte(`{"schema_version":1,"delivery_id":"10000000-0000-4000-8000-000000000001","dispatch_no":1}{}`),
		[]byte(`{"schema_version":2,"delivery_id":"10000000-0000-4000-8000-000000000001","dispatch_no":1}`),
		[]byte(`{"schema_version":1,"delivery_id":"not-a-uuid","dispatch_no":1}`),
		[]byte(`{"schema_version":1,"delivery_id":"10000000-0000-4000-8000-000000000001","dispatch_no":0}`),
	} {
		if _, err := DecodeStrict(encoded); errorCode(err) == "" {
			t.Fatalf("invalid args accepted: %s", encoded)
		}
	}
}

func TestValidateEncodedArgsRejectsTypedPayloadMismatch(t *testing.T) {
	t.Parallel()
	expected, err := NewArgs("10000000-0000-4000-8000-000000000001", 1)
	if err != nil {
		t.Fatal(err)
	}
	err = ValidateEncodedArgs([]byte(`{"schema_version":1,"delivery_id":"10000000-0000-4000-8000-000000000001","dispatch_no":2}`), expected)
	if errorCode(err) != "REINDEX_JOB_DECODE_CONFLICT" {
		t.Fatalf("mismatch error=%#v", err)
	}
}

func errorCode(err error) string {
	var classified *foundation.Error
	if errors.As(err, &classified) {
		return classified.Code
	}
	return ""
}
