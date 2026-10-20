package main

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"runtime"
	"strconv"
	"strings"
	"testing"
)

// 186 proved three presentation defects by parsing the frozen 185 controller, and
// those three tests necessarily keep failing because they read that frozen file.
// These are the same three claims scoped to the ACTIVE 198 controller source.
func diag198InspectFormat(t *testing.T) string {
	t.Helper()
	// The controller under test is the file compiled beside this test: go test runs
	// with its working directory set to the package directory, so a plain relative
	// name is correct in the repository and carries no author-absolute evidence path.
	path := diag211ControllerSource(t)
	file, err := parser.ParseFile(token.NewFileSet(), path, nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	result := ""
	ast.Inspect(file, func(node ast.Node) bool {
		call, ok := node.(*ast.CallExpr)
		if !ok || len(call.Args) == 0 {
			return true
		}
		literal, ok := call.Args[0].(*ast.BasicLit)
		if !ok || literal.Kind != token.STRING {
			return true
		}
		value, unquoteErr := strconv.Unquote(literal.Value)
		if unquoteErr == nil && strings.HasPrefix(value, "controller inspect of additional client PID %d failed") {
			result = value
		}
		return true
	})
	if result == "" {
		t.Fatal("198 controller-inspect formatter missing")
	}
	return result
}

func TestDiag198InspectErrorFormatSeparatesControllerFromHandle(t *testing.T) {
	format := diag198InspectFormat(t)
	if !strings.Contains(format, "controller_inspect=failed") {
		t.Fatalf("controller inspect failure not labeled explicitly: %s", format)
	}
	// nil carries no handle. The expected enum is platform exact: Windows reports
	// process_missing; non-Windows reports unsupported because the handle API is
	// unavailable there. Asserting the exact platform value keeps this strict (a
	// wrong-platform enum must fail) while remaining portable.
	wantNilInspect := "unsupported"
	if runtime.GOOS == "windows" {
		wantNilInspect = "process_missing"
	}
	facts := captureClientStartFacts(nil)
	if facts.Inspect != wantNilInspect {
		t.Fatalf("nil facts enum = %q, want %q (GOOS=%s): %+v", facts.Inspect, wantNilInspect, runtime.GOOS, facts)
	}
	if facts.Exited || facts.StillRunning || facts.HasExitCode || facts.HasCreated {
		t.Fatalf("nil process must carry no exit or birth facts: %+v", facts)
	}
	text := fmt.Sprintf(format, 4242, facts.Describe())
	if strings.Contains(text, "inspect=ok") {
		t.Fatalf("controller inspect failure labeled with a native ok: %s", text)
	}
	if !strings.Contains(text, "controller_inspect=failed") {
		t.Fatalf("controller inspect failure not labeled: %s", text)
	}
	if !strings.Contains(text, "exit_code=unknown") || strings.Contains(text, "exit_code=0") {
		t.Fatalf("nil process rendering must stay unknown and never zero: %s", text)
	}
	if !strings.Contains(text, "liveness_inspect="+facts.Inspect) {
		t.Fatalf("handle probe enum missing from rendering: %s", text)
	}
	t.Logf("CONTROLLED_INSPECT_ERROR_PRESENTATION %s", text)
}

func TestDiag198UnknownAndNilNeverPrintZeroExit(t *testing.T) {
	format := diag198InspectFormat(t)
	cases := []struct {
		name  string
		facts clientStartFacts
	}{
		{"nil", captureClientStartFacts(nil)},
		{"released_handle", clientStartFacts{PID: 7, Inspect: "handle_unavailable", BirthInspect: "unavailable"}},
		{"unsupported", clientStartFacts{PID: 7, Inspect: "unsupported", BirthInspect: "unavailable"}},
	}
	for _, c := range cases {
		text := fmt.Sprintf(format, c.facts.PID, c.facts.Describe())
		t.Logf("CONTROLLED_UNKNOWN_PRESENTATION %s %s", c.name, text)
		if strings.Contains(text, "exit_code=0") {
			t.Fatalf("%s: unknown native code rendered as known zero: %s", c.name, text)
		}
		if !strings.Contains(text, "exit_code=unknown") || !strings.Contains(text, "state=unknown") {
			t.Fatalf("%s: unknown not explicit: %s", c.name, text)
		}
	}
}

// A successful liveness read must not imply a known birth: the author draft kept
// Inspect=ok while GetProcessTimes failed, so the birth flag has to be independent.
func TestDiag198BirthUnknownIsIndependentFromLiveness(t *testing.T) {
	facts := clientStartFacts{PID: 11, Probed: true, StillRunning: true, Inspect: "ok", BirthInspect: "unavailable"}
	text := facts.Describe()
	t.Logf("BIRTH_INDEPENDENT %s", text)
	if !strings.Contains(text, "liveness_inspect=ok") {
		t.Fatalf("liveness not reported: %s", text)
	}
	if !strings.Contains(text, "birth_inspect=unavailable") {
		t.Fatalf("birth probe not reported independently: %s", text)
	}
	if !strings.Contains(text, "created_utc=unknown") || !strings.Contains(text, "created_filetime=unknown") {
		t.Fatalf("unknown birth not explicit: %s", text)
	}
	if strings.Contains(text, "birth_inspect=ok") {
		t.Fatalf("birth claimed ok without evidence: %s", text)
	}
}
