package main

import (
	"os"
	"testing"
)

// diag211ControllerSource resolves the controller.go compiled beside this test.
// go test sets the working directory to the package directory, so the relative
// name is the repository-relative source under test; no absolute author evidence
// path is involved.
func diag211ControllerSource(t *testing.T) string {
	t.Helper()
	name := "controller.go"
	if _, err := os.Stat(name); err != nil {
		wd, _ := os.Getwd()
		t.Fatalf("controller.go not found beside the running test (cwd=%s): %v", wd, err)
	}
	return name
}
