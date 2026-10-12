package main

import (
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestDiag198DescribeNeverPrintsZeroExit(t *testing.T) {
	var zero clientStartFacts
	text := zero.Describe()
	if strings.Contains(text, "exit_code=0") {
		t.Fatalf("zero-value facts printed exit_code=0: %s", text)
	}
	if !strings.Contains(text, "exit_code=unknown") || !strings.Contains(text, "state=unknown") {
		t.Fatalf("zero-value facts not explicit about unknown: %s", text)
	}
	if !strings.Contains(text, "liveness_inspect=not_probed") {
		t.Fatalf("liveness inspect not explicit: %s", text)
	}
	if !strings.Contains(text, "birth_inspect=unavailable") {
		t.Fatalf("birth inspect not independent: %s", text)
	}
}

func TestDiag198DescribeKeepsFiletimeTicks(t *testing.T) {
	created := time.Unix(0, int64(133000000000000000-116444736000000000)*100).UTC()
	facts := clientStartFacts{Probed: true, Exited: true, HasExitCode: true, ExitCode: 259,
		HasCreated: true, Created: created, CreatedFiletime: 133000000000000000, Inspect: "ok", BirthInspect: "ok"}
	text := facts.Describe()
	if !strings.HasPrefix(text, "state=exited exit_code=259") {
		t.Fatalf("unexpected rendering: %s", text)
	}
	if !strings.Contains(text, "created_filetime=133000000000000000") {
		t.Fatalf("filetime ticks missing from rendering: %s", text)
	}
	if facts.CreatedUTC().IsZero() {
		t.Fatalf("CreatedUTC returned zero for a valid FILETIME")
	}
	if facts.CreatedText() != created.Format(time.RFC3339Nano) {
		t.Fatalf("birth text lost FILETIME precision: %s", facts.CreatedText())
	}
	if !strings.Contains(text, "birth_inspect=ok") {
		t.Fatalf("birth inspect not reported: %s", text)
	}
}

func TestDiag198RealExit259IsExitedNotRunning(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("windows handle facts only")
	}
	cmd := helper198Command("exit259")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() {
		_ = cmd.Process.Kill()
		_, _ = cmd.Process.Wait()
	}()
	time.Sleep(500 * time.Millisecond)
	facts := captureClientStartFacts(cmd.Process)
	t.Logf("FACTS_EXIT259 %s", facts.Describe())
	if !facts.Exited || !facts.HasExitCode || facts.ExitCode != 259 {
		t.Fatalf("a real exit 259 was misjudged: %s", facts.Describe())
	}
	if facts.StillRunning {
		t.Fatalf("exited child reported as still running: %s", facts.Describe())
	}
	if !facts.HasCreated {
		t.Fatalf("creation time not captured: %s", facts.Describe())
	}
}

func TestDiag198RealRunningChildHasNoExitCode(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("windows handle facts only")
	}
	cmd := helper198Command("sleep")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() {
		_ = cmd.Process.Kill()
		_, _ = cmd.Process.Wait()
	}()
	facts := captureClientStartFacts(cmd.Process)
	t.Logf("FACTS_RUNNING %s", facts.Describe())
	if !facts.StillRunning || facts.Exited || facts.HasExitCode {
		t.Fatalf("running child facts are wrong: %s", facts.Describe())
	}
}

// Platform-neutral: off Windows the handle API is unavailable, so the closed enum
// must say unsupported and no exit code may be claimed. This is the enum-unsupported
// coverage that keeps the pure formatter tests meaningful on Linux too.
func TestDiag198NonWindowsEnumIsUnsupported(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("non-windows enum check")
	}
	cmd := helper198Command("sleep")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() {
		_ = cmd.Process.Kill()
		_, _ = cmd.Process.Wait()
	}()
	facts := captureClientStartFacts(cmd.Process)
	if facts.Inspect != "unsupported" || facts.HasExitCode || facts.Exited || facts.StillRunning {
		t.Fatalf("non-windows facts must be unsupported without exit code: %s", facts.Describe())
	}
	if !strings.Contains(facts.Describe(), "exit_code=unknown") {
		t.Fatalf("non-windows facts must not claim an exit code: %s", facts.Describe())
	}
}
