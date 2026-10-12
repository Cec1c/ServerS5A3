//go:build windows

package main

import (
	"context"
	"io"
	"os"
	"strings"
	"testing"
)

// Windows-only: the real controller path plus a child that genuinely exits 259.
// On non-Windows the process exit code is truncated to 8 bits (259 becomes 3), so
// this assertion is not a portable contract and is confined by the build tag.
func TestDiag198RealPathExit259Message(t *testing.T) {
	cfg := native46Instance()
	diag198AdminStub(t, &cfg)
	paths := newProjectPaths(t.TempDir())
	if err := paths.ensureDirectories(); err != nil {
		t.Fatal(err)
	}
	c := newController(paths, io.Discard, io.Discard)
	t.Setenv(diag198ChildEnv, "1")
	err := c.startLocalClient(context.Background(), cfg, t.TempDir(), os.Args[0], true, "diag198", "-test.run=^TestDiag198HelperExit259$")
	if err == nil {
		t.Fatalf("real multi-instance path unexpectedly succeeded for an exited child")
	}
	msg := err.Error()
	t.Logf("REAL_PATH_ERROR %s", msg)
	if !strings.Contains(msg, "exited during the 5-second startup check") {
		t.Fatalf("unexpected real failure text: %s", msg)
	}
	if strings.Contains(msg, "exit_code=0") {
		t.Fatalf("failure text fabricated exit_code=0: %s", msg)
	}
	if !strings.Contains(msg, "exit_code=259") {
		t.Fatalf("a real exit 259 was not reported as 259: %s", msg)
	}
	if !strings.Contains(msg, "created_filetime=") {
		t.Fatalf("failure text lacks FILETIME-precision creation time: %s", msg)
	}
	for _, banned := range []string{os.Args[0], diag198ChildEnv, "-test.run"} {
		if strings.Contains(msg, banned) {
			t.Fatalf("failure text leaked %q: %s", banned, msg)
		}
	}
}
