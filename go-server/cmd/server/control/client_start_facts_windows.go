//go:build windows

package main

import (
	"os"
	"syscall"
	"time"
)

const (
	windowsWaitObject0 = 0x00000000
	windowsWaitTimeout = 0x00000102
)

var (
	diag198Kernel32          = syscall.NewLazyDLL("kernel32.dll")
	diag198WaitForSingleProc = diag198Kernel32.NewProc("WaitForSingleObject")
)

// captureClientStartFacts runs inside the Process.WithHandle callback: the handle
// is guaranteed valid for the callback and must be read before Process.Release or
// Process.Wait consumes it.
//
// Liveness is decided by a non-blocking WaitForSingleObject(handle, 0):
//   - signaled  => the process has exited, so GetExitCodeProcess yields the real
//     exit code (a genuine exit 259 stays 259 and is reported as Exited);
//   - timeout   => still running, and no exit code is claimed;
//   - failed    => inspect error enum, never a guessed state.
func captureClientStartFacts(proc *os.Process) clientStartFacts {
	facts := clientStartFacts{Inspect: "process_missing"}
	if proc == nil {
		return facts
	}
	facts.PID = proc.Pid
	facts.Inspect = "handle_unavailable"
	if err := proc.WithHandle(func(handle uintptr) {
		facts.Probed = true
		wait, _, _ := diag198WaitForSingleProc.Call(handle, 0)
		switch uint32(wait) {
		case windowsWaitTimeout:
			facts.StillRunning = true
		case windowsWaitObject0:
			var code uint32
			if err := syscall.GetExitCodeProcess(syscall.Handle(handle), &code); err != nil {
				facts.Probed = false
				facts.Inspect = "exit_code_unavailable"
				return
			}
			facts.Exited = true
			facts.HasExitCode = true
			facts.ExitCode = int(code)
		default:
			facts.Probed = false
			facts.Inspect = "wait_failed"
			return
		}
		var creation, exit, kernel, user syscall.Filetime
		if err := syscall.GetProcessTimes(syscall.Handle(handle), &creation, &exit, &kernel, &user); err == nil {
			facts.CreatedFiletime = uint64(creation.HighDateTime)<<32 | uint64(creation.LowDateTime)
			facts.Created = time.Unix(0, int64(facts.CreatedFiletime-116444736000000000)*100).UTC()
			facts.HasCreated = true
			facts.BirthInspect = "ok"
		} else {
			// Liveness may be known while the birth probe failed: keep the two
			// independent so Inspect=ok never implies a known birth.
			facts.BirthInspect = "unavailable"
		}
		facts.Inspect = "ok"
	}); err != nil {
		facts.Probed = false
		if err == os.ErrNoHandle {
			facts.Inspect = "unsupported"
		}
	}
	if !facts.Probed {
		facts.Exited = false
		facts.StillRunning = false
		facts.HasExitCode = false
	}
	return facts
}
