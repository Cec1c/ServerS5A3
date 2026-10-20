package dnfbridge

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writePatchPayloadFile 在测试负载目录里造一个文件，返回它的小写 sha256。
func writePatchPayloadFile(t *testing.T, root string, rel string, data []byte) {
	t.Helper()
	full := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", filepath.Dir(full), err)
	}
	if err := os.WriteFile(full, data, 0o644); err != nil {
		t.Fatalf("write %s: %v", full, err)
	}
}

// newPatchPayloadServer 起一个真实 HTTP 服务（和线上一样注册 /patch/ 前缀）。
func newPatchPayloadServer(t *testing.T, root string) *httptest.Server {
	t.Helper()
	api := &accountAPIServer{patchPayloadDir: root}
	mux := http.NewServeMux()
	mux.HandleFunc("/patch/", func(w http.ResponseWriter, r *http.Request) {
		if !api.handlePatchFiles(w, r) {
			http.NotFound(w, r)
		}
	})
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)
	return server
}

// TestPatchFilesRouteServesManifestAndPayload 校验 /patch/manifest.json 与
// /patch/files/<相对路径> 都能原样读出（含子目录）。
func TestPatchFilesRouteServesManifestAndPayload(t *testing.T) {
	root := t.TempDir()
	manifest := []byte(`{"version":"test-1","files":[]}`)
	pvf := bytes.Repeat([]byte("pvf-payload-"), 128)
	writePatchPayloadFile(t, root, "manifest.json", manifest)
	writePatchPayloadFile(t, root, "Script.pvf", pvf)
	writePatchPayloadFile(t, root, "lua/init.lua", []byte("-- init"))
	server := newPatchPayloadServer(t, root)

	for _, tc := range []struct {
		url  string
		want []byte
	}{
		{"/patch/manifest.json", manifest},
		{"/patch/files/Script.pvf", pvf},
		{"/patch/files/lua/init.lua", []byte("-- init")},
	} {
		response, err := http.Get(server.URL + tc.url)
		if err != nil {
			t.Fatalf("GET %s: %v", tc.url, err)
		}
		body, _ := io.ReadAll(response.Body)
		response.Body.Close()
		if response.StatusCode != http.StatusOK {
			t.Fatalf("GET %s status=%d want 200", tc.url, response.StatusCode)
		}
		if !bytes.Equal(body, tc.want) {
			t.Fatalf("GET %s body mismatch: %d bytes", tc.url, len(body))
		}
	}
}

// TestPatchFilesRangeResume 锁定断点续传：带 Range 必须回 206 + Content-Range，
// 且只回请求的那一段（登陆器靠它从中断处接着下 111 MB 的 PVF）。
func TestPatchFilesRangeResume(t *testing.T) {
	root := t.TempDir()
	data := bytes.Repeat([]byte("0123456789"), 200) // 2000 字节
	writePatchPayloadFile(t, root, "Script.pvf", data)
	server := newPatchPayloadServer(t, root)

	request, err := http.NewRequest(http.MethodGet, server.URL+"/patch/files/Script.pvf", nil)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	request.Header.Set("Range", "bytes=1000-")
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatalf("range request: %v", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusPartialContent {
		t.Fatalf("status=%d want 206", response.StatusCode)
	}
	if got := response.Header.Get("Content-Range"); !strings.HasPrefix(got, "bytes 1000-1999/2000") {
		t.Fatalf("Content-Range=%q want prefix bytes 1000-1999/2000", got)
	}
	body, _ := io.ReadAll(response.Body)
	if !bytes.Equal(body, data[1000:]) {
		t.Fatalf("range body mismatch: got %d bytes", len(body))
	}
}

// TestPatchFilesRouteRejectsTraversalAndMissing 目录穿越、绝对路径、软链接逃逸、
// 不存在的文件、目录本身，以及负载目录不存在时，都必须 404（不泄露任何东西）。
func TestPatchFilesRouteRejectsTraversalAndMissing(t *testing.T) {
	parent := t.TempDir()
	root := filepath.Join(parent, "patch-payload")
	writePatchPayloadFile(t, root, "Script.pvf", []byte("real"))
	secret := filepath.Join(parent, "dnf90.env")
	if err := os.WriteFile(secret, []byte("SECRET=1"), 0o600); err != nil {
		t.Fatalf("write secret: %v", err)
	}
	if err := os.Symlink(secret, filepath.Join(root, "escape.env")); err != nil {
		t.Logf("symlink unavailable, skipping link case: %v", err)
	}
	server := newPatchPayloadServer(t, root)

	for _, url := range []string{
		"/patch/files/../../dnf90.env",
		"/patch/files/..%2F..%2Fdnf90.env",
		"/patch/files//etc/passwd",
		"/patch/files/escape.env",
		"/patch/files/nope.pvf",
		"/patch/files/",
		"/patch/",
	} {
		response, err := http.Get(server.URL + url)
		if err != nil {
			t.Fatalf("GET %s: %v", url, err)
		}
		body, _ := io.ReadAll(response.Body)
		response.Body.Close()
		if response.StatusCode != http.StatusNotFound {
			t.Fatalf("GET %s status=%d want 404 (body=%q)", url, response.StatusCode, string(body))
		}
		if bytes.Contains(body, []byte("SECRET=1")) {
			t.Fatalf("GET %s leaked the secret file", url)
		}
	}

	// 负载目录不存在 → 整体 404（默认不暴露任何东西）。
	missing := newPatchPayloadServer(t, filepath.Join(parent, "does-not-exist"))
	response, err := http.Get(missing.URL + "/patch/manifest.json")
	if err != nil {
		t.Fatalf("GET manifest on missing dir: %v", err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusNotFound {
		t.Fatalf("missing dir status=%d want 404", response.StatusCode)
	}
}

// TestPatchFilesRouteRejectsWrites 只读：POST 必须 405，不能借这套端点改服务器文件。
func TestPatchFilesRouteRejectsWrites(t *testing.T) {
	root := t.TempDir()
	writePatchPayloadFile(t, root, "Script.pvf", []byte("real"))
	server := newPatchPayloadServer(t, root)

	response, err := http.Post(server.URL+"/patch/files/Script.pvf", "application/octet-stream", strings.NewReader("x"))
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusMethodNotAllowed {
		t.Fatalf("POST status=%d want 405", response.StatusCode)
	}
}
