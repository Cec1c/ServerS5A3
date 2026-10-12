//go:build !windows

package main

import "os"

// captureClientStartFacts has no handle-based implementation off Windows, so it
// reports unsupported with no exit code rather than inventing one.
func captureClientStartFacts(proc *os.Process) clientStartFacts {
	facts := clientStartFacts{Inspect: "unsupported", BirthInspect: "unavailable"}
	if proc != nil {
		facts.PID = proc.Pid
	}
	return facts
}
