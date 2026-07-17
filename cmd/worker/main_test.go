package main

import "testing"

func TestNewWorkerComponentsRequiresDatabase(t *testing.T) {
	components, err := newWorkerComponents(nil)
	if err == nil || components.safeWriteback != nil {
		t.Fatalf("components=%#v err=%v", components, err)
	}
}
