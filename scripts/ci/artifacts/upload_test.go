package main

import (
	"archive/zip"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func noPause(context.Context, time.Duration) error { return nil }

func TestTransportFailureDoesNotExposeRawError(t *testing.T) {
	for _, tc := range []struct {
		err  error
		want string
	}{
		{nil, "none"}, {context.DeadlineExceeded, "timeout"}, {io.EOF, "eof"},
		{io.ErrUnexpectedEOF, "unexpected_eof"},
		{errors.New("secret endpoint: connection reset by peer"), "connection_reset_by_peer"},
		{errors.New("secret endpoint: malformed HTTP response"), "malformed_http_response"},
		{errors.New("secret-token"), "other"},
	} {
		if got := transportFailure(tc.err); got != tc.want {
			t.Fatalf("got %q want %q", got, tc.want)
		}
	}
}

func TestCountedBodyReportsBytesRead(t *testing.T) {
	body := &countedBody{reader: strings.NewReader("diagnostic fixture")}
	b, err := io.ReadAll(body)
	if err != nil || string(b) != "diagnostic fixture" || body.read.Load() != int64(len(b)) {
		t.Fatalf("read=%d bytes=%d error=%v", body.read.Load(), len(b), err)
	}
}

func TestArtifactClientUsesHTTP1WhenServerOffersHTTP2(t *testing.T) {
	f, _ := archiveFixture(t)
	protocol := 0
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		protocol = r.ProtoMajor
		io.Copy(io.Discard, r.Body)
		w.WriteHeader(http.StatusCreated)
	}))
	srv.EnableHTTP2 = true
	srv.StartTLS()
	defer srv.Close()
	client := artifactClient()
	transport := client.Transport.(*http.Transport)
	// Preserve the client's ALPN list, as a real proxy does. Replacing the whole
	// TLS config would hide inherited h2 negotiation from DefaultTransport.Clone.
	transport.TLSClientConfig.RootCAs = srv.Client().Transport.(*http.Transport).TLSClientConfig.RootCAs
	defer transport.CloseIdleConnections()
	if err := upload(context.Background(), client, srv.URL, "fixture-token", "build.zip", f, noPause); err != nil {
		t.Fatal(err)
	}
	if protocol != 1 {
		t.Fatalf("artifact upload negotiated HTTP/%d, want HTTP/1", protocol)
	}
}

func archiveFixture(t *testing.T) (*os.File, []byte) {
	t.Helper()
	root := t.TempDir()
	dir := filepath.Join(root, "dist", "linux-amd64")
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"DNF90Server", "DNF90Control", "DNF90Doctor"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("binary\x00"+name), 0755); err != nil {
			t.Fatal(err)
		}
	}
	f, err := os.CreateTemp(t.TempDir(), "artifact-*.zip")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { f.Close() })
	if err := archiveDist(root, f); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(f.Name())
	if err != nil {
		t.Fatal(err)
	}
	return f, b
}

func TestArchivePreservesDistPathsAndExecutableBytes(t *testing.T) {
	_, b := archiveFixture(t)
	z, err := zip.NewReader(bytes.NewReader(b), int64(len(b)))
	if err != nil {
		t.Fatal(err)
	}
	if len(z.File) != 3 {
		t.Fatalf("got %d files", len(z.File))
	}
	for _, f := range z.File {
		if !strings.HasPrefix(f.Name, "dist/linux-amd64/") {
			t.Fatal(f.Name)
		}
		r, err := f.Open()
		if err != nil {
			t.Fatal(err)
		}
		b, err := io.ReadAll(r)
		r.Close()
		if err != nil || string(b) != "binary\x00"+filepath.Base(f.Name) {
			t.Fatalf("changed artifact %s", f.Name)
		}
	}
}

func TestEmptyBuildFails(t *testing.T) {
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "dist"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := archiveDist(root, io.Discard); err == nil {
		t.Fatal("accepted empty build")
	}
}

func TestKnownLengthAvoidsChunkedUploadRejectionAndReplaysIdenticalZip(t *testing.T) {
	f, want := archiveFixture(t)
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.ContentLength <= 0 || len(r.TransferEncoding) != 0 {
			w.WriteHeader(524)
			return
		}
		if r.Header.Get("JOB-TOKEN") != "test-token" {
			t.Error("missing job token")
		}
		mr, err := r.MultipartReader()
		if err != nil {
			t.Error(err)
			w.WriteHeader(400)
			return
		}
		p, err := mr.NextPart()
		if err != nil {
			t.Error(err)
			w.WriteHeader(400)
			return
		}
		b, err := io.ReadAll(p)
		if err != nil || !bytes.Equal(b, want) || p.FileName() != "build.zip" || p.FormName() != "file" {
			t.Error("multipart artifact changed")
		}
		if _, err = mr.NextPart(); !errors.Is(err, io.EOF) {
			t.Error("unexpected multipart tail", err)
		}
		calls++
		if calls == 1 {
			w.WriteHeader(524)
			return
		}
		w.WriteHeader(201)
	}))
	defer srv.Close()
	// RED control: the old streamed request has no length and is rejected.
	r, _ := http.NewRequest(http.MethodPost, srv.URL, io.NopCloser(bytes.NewReader(want)))
	resp, err := srv.Client().Do(r)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 524 {
		t.Fatal("streamed control was not rejected")
	}
	if err := upload(context.Background(), srv.Client(), srv.URL, "test-token", "build.zip", f, noPause); err != nil {
		t.Fatal(err)
	}
	if calls != 2 {
		t.Fatalf("got %d fixed-length requests", calls)
	}
}

func TestUploadStatusAndRetryBoundaries(t *testing.T) {
	for _, tc := range []struct {
		code, calls int
		success     bool
	}{{201, 1, true}, {200, 1, false}, {403, 1, false}, {413, 1, false}, {429, 3, false}, {524, 3, false}} {
		t.Run(fmt.Sprintf("HTTP%d", tc.code), func(t *testing.T) {
			f, _ := archiveFixture(t)
			calls := 0
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				io.Copy(io.Discard, r.Body)
				calls++
				w.WriteHeader(tc.code)
				io.WriteString(w, "secret-response")
			}))
			defer srv.Close()
			err := upload(context.Background(), srv.Client(), srv.URL, "secret-token", "build.zip", f, noPause)
			if (err == nil) != tc.success || calls != tc.calls {
				t.Fatalf("calls=%d err=%v", calls, err)
			}
			if err != nil && strings.Contains(err.Error(), "secret") {
				t.Fatal("secret in error")
			}
		})
	}
}

type failingTransport struct{ calls int }

func (r *failingTransport) RoundTrip(*http.Request) (*http.Response, error) {
	r.calls++
	return nil, errors.New("secret-token in transport diagnostic")
}

func TestTransportFailureIsBoundedAndSanitized(t *testing.T) {
	f, _ := archiveFixture(t)
	transport := &failingTransport{}
	err := upload(context.Background(), &http.Client{Transport: transport}, "https://example.test", "secret-token", "build.zip", f, noPause)
	if err == nil || strings.Contains(err.Error(), "secret") || transport.calls != 3 {
		t.Fatalf("calls=%d err=%v", transport.calls, err)
	}
}

func TestRedirectDoesNotForwardToken(t *testing.T) {
	f, _ := archiveFixture(t)
	destCalls := 0
	dest := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { destCalls++; w.WriteHeader(201) }))
	defer dest.Close()
	src := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, dest.URL, 307) }))
	defer src.Close()
	if err := upload(context.Background(), src.Client(), src.URL, "secret-token", "build.zip", f, noPause); err == nil {
		t.Fatal("redirect accepted")
	}
	if destCalls != 0 {
		t.Fatal("forwarded credentials")
	}
}

func TestEndpointValidation(t *testing.T) {
	for _, api := range []string{"http://example.test/api/v4", "https://user:pass@example.test/api/v4", "https://example.test/api/v4?token=x", "https://example.test/api/v4#x"} {
		if _, err := endpoint(api, "123"); err == nil {
			t.Fatal(api)
		}
	}
	for _, job := range []string{"", "0", "../1", "1?token=x"} {
		if _, err := endpoint("https://example.test/api/v4", job); err == nil {
			t.Fatal(job)
		}
	}
	u, err := endpoint("https://example.test/api/v4", "123")
	if err != nil || !strings.Contains(u, "/api/v4/jobs/123/artifacts?") || !strings.Contains(u, "artifact_format=zip") {
		t.Fatal(u, err)
	}
}
