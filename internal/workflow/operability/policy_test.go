package operability

import (
	"strings"
	"testing"
	"time"
)

func TestValidateRiverOptions(t *testing.T) {
	valid := func(queue string, workers int, job, rescue, soft time.Duration) error {
		return ValidateRiverOptions(queue, workers, job, rescue, soft)
	}
	if err := valid("workflow_priority", 4, time.Minute, 2*time.Minute, time.Second); err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name string
		err  error
	}{
		{name: "empty queue", err: valid("", 4, time.Minute, 2*time.Minute, time.Second)},
		{name: "invalid queue", err: valid("workflow queue", 4, time.Minute, 2*time.Minute, time.Second)},
		{name: "long queue", err: valid(strings.Repeat("a", 65), 4, time.Minute, 2*time.Minute, time.Second)},
		{name: "too many workers", err: valid("workflow", 10_001, time.Minute, 2*time.Minute, time.Second)},
		{name: "invalid rescue", err: valid("workflow", 4, time.Minute, time.Minute, time.Second)},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if test.err == nil {
				t.Fatal("invalid options accepted")
			}
		})
	}
}
