package main

import "testing"

func TestNewWorkflowServiceRequiresDatabase(t *testing.T) {
	if service, err := newWorkflowService(nil); err == nil || service != nil {
		t.Fatalf("service=%#v err=%v", service, err)
	}
}
