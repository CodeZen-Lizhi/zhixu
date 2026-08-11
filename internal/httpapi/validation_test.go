package httpapi

import (
	"sync"
	"testing"
)

func TestValidateAppliesDeclarativeRequestConstraintsConcurrently(t *testing.T) {
	type request struct {
		Name  string `validate:"required,max=8"`
		Count int    `validate:"gte=1,lte=10"`
	}

	if err := Validate(request{Name: "valid", Count: 4}); err != nil {
		t.Fatalf("valid request rejected: %v", err)
	}
	if err := Validate(request{Name: "", Count: 11}); err == nil {
		t.Fatal("invalid request accepted")
	}

	var waitGroup sync.WaitGroup
	for range 16 {
		waitGroup.Add(1)
		go func() {
			defer waitGroup.Done()
			if err := Validate(request{Name: "valid", Count: 1}); err != nil {
				t.Errorf("concurrent validation failed: %v", err)
			}
		}()
	}
	waitGroup.Wait()
}
