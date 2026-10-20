package main

import (
	"net"
	"net/http"
	"os"
	"os/exec"
	"testing"
	"time"
)

// Portable test support shared by the Windows-only and the platform-neutral 198
// tests. Children are referenced only by their -test.run name string, so nothing
// here adds a cross-platform compile dependency on a Windows-only symbol.
const diag198ChildEnv = "DIAG198_CHILD"

func TestDiag198HelperExit259(t *testing.T) {
	if os.Getenv(diag198ChildEnv) == "" {
		t.Skip("helper child only")
	}
	os.Exit(259)
}

func TestDiag198HelperSleep(t *testing.T) {
	if os.Getenv(diag198ChildEnv) == "" {
		t.Skip("helper child only")
	}
	time.Sleep(20 * time.Second)
	os.Exit(0)
}

// diag198AdminStub answers the controller account-bind POST with an in-process
// loopback fixture so the real multi-instance path is reachable. It never starts a
// product service; it binds an ephemeral 127.0.0.1 port and closes with the test.
// (186's memory RoundTripper is an alternative; both avoid real API traffic.)
func diag198AdminStub(t *testing.T, cfg *instanceConfig) {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/local/client-account" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte("{\"success\":true}"))
	})}
	go func() { _ = server.Serve(listener) }()
	t.Cleanup(func() { _ = server.Close() })
	cfg.Server.AdminListen = listener.Addr().String()
}

// helper198Command is used only from Windows-gated tests.
func helper198Command(mode string) *exec.Cmd {
	cmd := exec.Command(os.Args[0], "-test.run=^TestDiag198HelperSleep$")
	if mode == "exit259" {
		cmd = exec.Command(os.Args[0], "-test.run=^TestDiag198HelperExit259$")
	}
	cmd.Env = append(os.Environ(), diag198ChildEnv+"=1")
	return cmd
}
