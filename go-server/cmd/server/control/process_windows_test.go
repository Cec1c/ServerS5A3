//go:build windows

package main

import (
	"os"
	"testing"
)

func TestProcessExistsInSnapshot(t *testing.T) {
	exists, err := processExistsInSnapshot(os.Getpid())
	if err != nil {
		t.Fatalf("processExistsInSnapshot(current) error = %v", err)
	}
	if !exists {
		t.Fatal("current process is absent from the process snapshot")
	}

	// Convert the variable, not the constant: the same impossible DWORD PID
	// must also compile for the new Windows386 controller/test target.
	impossiblePID := ^uint32(0)
	exists, err = processExistsInSnapshot(int(impossiblePID))
	if err != nil {
		t.Fatalf("processExistsInSnapshot(impossible) error = %v", err)
	}
	if exists {
		t.Fatal("impossible PID appears in the process snapshot")
	}
}
