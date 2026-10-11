package dnfbridge

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writePatchDistFile 在测试负载目录里造一个文件，返回它的大写 sha256。
func writePatchDistFile(t *testing.T, root string, rel string, data []byte) string {
	t.Helper()
	full := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatalf("mkdir for %s: %v", rel, err)
	}
	if err := os.WriteFile(full, data, 0o644); err != nil {
		t.Fatalf("write %s: %v", rel, err)
	}
	sum := sha256.Sum256(data)
	return strings.ToUpper(hex.EncodeToString(sum[:]))
}

func writePatchDistManifest(t *testing.T, root string, manifest patchManifest) {
	t.Helper()
	raw, err := json.Marshal(manifest)
	if err != nil {
		t.Fatalf("marshal manifest: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, patchManifestName), raw, 0o644); err != nil {
		t.Fatalf("write manifest: %v", err)
	}
}

// TestPatchDistPathAllowedWhitelist 锁定白名单：只有登陆器 PatchManifest 里那几类
// 路径可读，其余（含一切目录穿越写法）都必须被拒。
func TestPatchDistPathAllowedWhitelist(t *testing.T) {
	allowed := []string{
		"Script.pvf",
		"DNF.exe",
		"90CN.dll",
		"90CNLua.dll",
		"ijl15.dll",
		"ijl15_real.dll",
		"ImagePacks2/sprite_worldmap.NPK",
		"lua/init.lua",
		"lua/ai/init.lua",
	}
	for _, rel := range allowed {
		if !patchDistPathAllowed(rel) {
			t.Errorf("patchDistPathAllowed(%q)=false, want true", rel)
		}
	}
	rejected := []string{
		"",
		".",
		"..",
		"../Script.pvf",
		"ImagePacks2/../../etc/passwd",
		"lua/../../etc/passwd",
		"Script.pvf/../DNF.exe",
		"/etc/passwd",
		"/opt/dnf90/data/patch-dist/Script.pvf",
		`..\..\Script.pvf`,
		`ImagePacks2\sprite_worldmap.NPK`,
		"C:/Windows/win.ini",
		"hashes.sha256.txt",
		"manifest.json",
		"90CN.log",
		"ImagePacks2/sprite_worldmap.txt",
		"imagepacks2/sprite_worldmap.NPK",
		"lua/90CN.dll",
		"bin/90CN.dll",
		"DNF.exe/",
	}
	for _, rel := range rejected {
		if patchDistPathAllowed(rel) {
			t.Errorf("patchDistPathAllowed(%q)=true, want false", rel)
		}
	}
}

// TestPatchManifestRouteServesGeneratedManifest 校验清单端点返回脚本生成的清单
// 内容（版本 / 生成时间 / 每条 path+size+sha256+url）。
func TestPatchManifestRouteServesGeneratedManifest(t *testing.T) {
	root := t.TempDir()
	pvf := bytes.Repeat([]byte("pvf-payload\n"), 32)
	pvfSha := writePatchDistFile(t, root, "Script.pvf", pvf)
	dllSha := writePatchDistFile(t, root, "90CN.dll", []byte("dll-payload"))
	npkSha := writePatchDistFile(t, root, "ImagePacks2/sprite_worldmap.NPK", []byte("npk-payload"))
	writePatchDistManifest(t, root, patchManifest{
		Version:     "pvf" + pvfSha[:12] + "-dll" + dllSha[:12],
		GeneratedAt: "2026-09-24T22:00:00Z",
		Files: []patchManifestFile{
			{Path: "Script.pvf", Size: int64(len(pvf)), SHA256: pvfSha, URL: "/api/patch/file?path=Script.pvf"},
			{Path: "90CN.dll", Size: 11, SHA256: dllSha, URL: "/api/patch/file?path=90CN.dll"},
			{Path: "ImagePacks2/sprite_worldmap.NPK", Size: 11, SHA256: npkSha, URL: "/api/patch/file?path=ImagePacks2%2Fsprite_worldmap.NPK"},
		},
	})

	api := &accountAPIServer{patchDir: root}
	recorder := httptest.NewRecorder()
	api.handlePatchManifest(recorder, httptest.NewRequest(http.MethodGet, "/api/patch/manifest.json", nil))

	if recorder.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	if got := recorder.Header().Get("Content-Type"); !strings.HasPrefix(got, "application/json") {
		t.Fatalf("content-type=%q, want application/json", got)
	}
	var got patchManifest
	if err := json.Unmarshal(recorder.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode manifest: %v", err)
	}
	if got.Version != "pvf"+pvfSha[:12]+"-dll"+dllSha[:12] {
		t.Fatalf("version=%q", got.Version)
	}
	if got.GeneratedAt != "2026-09-24T22:00:00Z" {
		t.Fatalf("generatedAt=%q", got.GeneratedAt)
	}
	if len(got.Files) != 3 {
		t.Fatalf("files=%d, want 3", len(got.Files))
	}
	for _, file := range got.Files {
		if file.Size <= 0 || len(file.SHA256) != 64 || !strings.HasPrefix(file.URL, "/api/patch/file?path=") {
			t.Fatalf("incomplete manifest entry: %+v", file)
		}
	}
	if got.Files[0].Path != "Script.pvf" || got.Files[0].SHA256 != pvfSha {
		t.Fatalf("first entry=%+v", got.Files[0])
	}
}

// TestPatchManifestRouteFailsLoudly 清单缺失时 503，清单里出现白名单外路径时
// 宁可 500 也不下发（否则会把登陆器引到越权文件上）。
func TestPatchManifestRouteFailsLoudly(t *testing.T) {
	t.Run("missing manifest", func(t *testing.T) {
		api := &accountAPIServer{patchDir: t.TempDir()}
		recorder := httptest.NewRecorder()
		api.handlePatchManifest(recorder, httptest.NewRequest(http.MethodGet, "/api/patch/manifest.json", nil))
		if recorder.Code != http.StatusServiceUnavailable {
			t.Fatalf("status=%d, want 503", recorder.Code)
		}
	})

	t.Run("non whitelisted path", func(t *testing.T) {
		root := t.TempDir()
		writePatchDistManifest(t, root, patchManifest{
			Version: "v1",
			Files:   []patchManifestFile{{Path: "../dnf90.env", Size: 10, SHA256: strings.Repeat("A", 64)}},
		})
		api := &accountAPIServer{patchDir: root}
		recorder := httptest.NewRecorder()
		api.handlePatchManifest(recorder, httptest.NewRequest(http.MethodGet, "/api/patch/manifest.json", nil))
		if recorder.Code != http.StatusInternalServerError {
			t.Fatalf("status=%d, want 500", recorder.Code)
		}
	})

	t.Run("broken json", func(t *testing.T) {
		root := t.TempDir()
		if err := os.WriteFile(filepath.Join(root, patchManifestName), []byte("{not json"), 0o644); err != nil {
			t.Fatal(err)
		}
		api := &accountAPIServer{patchDir: root}
		recorder := httptest.NewRecorder()
		api.handlePatchManifest(recorder, httptest.NewRequest(http.MethodGet, "/api/patch/manifest.json", nil))
		if recorder.Code != http.StatusInternalServerError {
			t.Fatalf("status=%d, want 500", recorder.Code)
		}
	})

	t.Run("post not allowed", func(t *testing.T) {
		api := &accountAPIServer{patchDir: t.TempDir()}
		recorder := httptest.NewRecorder()
		api.handlePatchManifest(recorder, httptest.NewRequest(http.MethodPost, "/api/patch/manifest.json", nil))
		if recorder.Code != http.StatusMethodNotAllowed {
			t.Fatalf("status=%d, want 405", recorder.Code)
		}
	})
}

// TestPatchFileRouteServesWhitelistedFiles 校验白名单文件能原样读出。
func TestPatchFileRouteServesWhitelistedFiles(t *testing.T) {
	root := t.TempDir()
	cases := map[string][]byte{
		"Script.pvf":                      bytes.Repeat([]byte{0x01, 0x02, 0x00, 0xff}, 512),
		"DNF.exe":                         []byte("MZ-fake-exe"),
		"90CN.dll":                        []byte("MZ-fake-dll"),
		"ImagePacks2/sprite_worldmap.NPK": []byte("npk"),
		"lua/ai/init.lua":                 []byte("-- lua"),
	}
	for rel, data := range cases {
		wantSha := writePatchDistFile(t, root, rel, data)
		api := &accountAPIServer{patchDir: root}
		recorder := httptest.NewRecorder()
		request := httptest.NewRequest(http.MethodGet, "/api/patch/file?path="+url.QueryEscape(rel), nil)
		api.handlePatchFile(recorder, request)

		if recorder.Code != http.StatusOK {
			t.Fatalf("%s: status=%d body=%s", rel, recorder.Code, recorder.Body.String())
		}
		if !bytes.Equal(recorder.Body.Bytes(), data) {
			t.Fatalf("%s: body mismatch (%d bytes)", rel, recorder.Body.Len())
		}
		if got := recorder.Header().Get("Content-Length"); got == "" {
			t.Fatalf("%s: missing Content-Length", rel)
		}
		sum := sha256.Sum256(recorder.Body.Bytes())
		if got := strings.ToUpper(hex.EncodeToString(sum[:])); got != wantSha {
			t.Fatalf("%s: sha=%s want %s", rel, got, wantSha)
		}
	}
}

// TestPatchFileRouteRejectsTraversalAndUnknownPaths 目录穿越、白名单外扩展名、
// 不存在的文件都必须是 404，且响应里不能带出目标文件内容。
func TestPatchFileRouteRejectsTraversalAndUnknownPaths(t *testing.T) {
	root := filepath.Join(t.TempDir(), "patch-dist")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	secret := "TOP-SECRET-ACCOUNT-TABLE"
	secretPath := filepath.Join(filepath.Dir(root), "secret.txt")
	if err := os.WriteFile(secretPath, []byte(secret), 0o644); err != nil {
		t.Fatal(err)
	}
	writePatchDistFile(t, root, "Script.pvf", []byte("real-pvf"))

	api := &accountAPIServer{patchDir: root}
	rejected := []string{
		"../secret.txt",
		"..%2Fsecret.txt",
		"ImagePacks2/../../secret.txt",
		"lua/../../secret.txt",
		"/etc/passwd",
		"..\\secret.txt",
		"secret.txt",
		"hashes.sha256.txt",
		"manifest.json",
		"ImagePacks2/missing.NPK",
		"lua/missing.lua",
		"",
	}
	for _, rel := range rejected {
		recorder := httptest.NewRecorder()
		request := httptest.NewRequest(http.MethodGet, "/api/patch/file?path="+url.QueryEscape(rel), nil)
		api.handlePatchFile(recorder, request)
		if recorder.Code != http.StatusNotFound && recorder.Code != http.StatusBadRequest {
			t.Fatalf("path=%q status=%d, want 404/400 (body=%s)", rel, recorder.Code, recorder.Body.String())
		}
		if strings.Contains(recorder.Body.String(), secret) {
			t.Fatalf("path=%q leaked file content: %s", rel, recorder.Body.String())
		}
	}

	t.Run("post not allowed", func(t *testing.T) {
		recorder := httptest.NewRecorder()
		api.handlePatchFile(recorder, httptest.NewRequest(http.MethodPost, "/api/patch/file?path=Script.pvf", nil))
		if recorder.Code != http.StatusMethodNotAllowed {
			t.Fatalf("status=%d, want 405", recorder.Code)
		}
	})
}

// TestPatchFileRouteSizeLimit 单文件上限：超限回 413，且不去读文件内容。
func TestPatchFileRouteSizeLimit(t *testing.T) {
	root := t.TempDir()
	writePatchDistFile(t, root, "DNF.exe", bytes.Repeat([]byte("x"), 64))
	api := &accountAPIServer{patchDir: root, patchMaxBytes: 16}
	recorder := httptest.NewRecorder()
	api.handlePatchFile(recorder, httptest.NewRequest(http.MethodGet, "/api/patch/file?path=DNF.exe", nil))
	if recorder.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status=%d, want 413", recorder.Code)
	}
}

// TestPatchRoutesOverRealHTTP 走真实 HTTP（和线上一样注册进 mux）验证大文件流式
// 下发：内容长度、sha256、HEAD 语义，以及 111 MB 级 PVF 这种大文件不会被
// 账号 API 的 10s 写超时截断（这里用 4 MiB 代表大文件）。
func TestPatchRoutesOverRealHTTP(t *testing.T) {
	root := t.TempDir()
	big := bytes.Repeat([]byte{0x5a, 0x00, 0xa5, 0xff}, 1<<20) // 4 MiB
	bigSha := writePatchDistFile(t, root, "ImagePacks2/sprite_character_swordman_effect_cutin.NPK", big)
	writePatchDistFile(t, root, "Script.pvf", []byte("pvf"))

	api := &accountAPIServer{patchDir: root}
	mux := http.NewServeMux()
	mux.HandleFunc("/api/patch/manifest.json", api.handlePatchManifest)
	mux.HandleFunc("/api/patch/file", api.handlePatchFile)
	server := httptest.NewServer(mux)
	defer server.Close()

	response, err := http.Get(server.URL + "/api/patch/file?path=" + url.QueryEscape("ImagePacks2/sprite_character_swordman_effect_cutin.NPK"))
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("status=%d", response.StatusCode)
	}
	if response.ContentLength != int64(len(big)) {
		t.Fatalf("content-length=%d, want %d", response.ContentLength, len(big))
	}
	body, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	sum := sha256.Sum256(body)
	if got := strings.ToUpper(hex.EncodeToString(sum[:])); got != bigSha {
		t.Fatalf("sha=%s want %s", got, bigSha)
	}

	head, err := http.Head(server.URL + "/api/patch/file?path=Script.pvf")
	if err != nil {
		t.Fatalf("head: %v", err)
	}
	defer func() { _ = head.Body.Close() }()
	if head.StatusCode != http.StatusOK || head.ContentLength != 3 {
		t.Fatalf("HEAD status=%d content-length=%d", head.StatusCode, head.ContentLength)
	}

	// 穿越请求在真实 HTTP 路径上同样只回 404。
	traversal, err := http.Get(server.URL + "/api/patch/file?path=..%2F..%2Fetc%2Fpasswd")
	if err != nil {
		t.Fatalf("traversal get: %v", err)
	}
	defer func() { _ = traversal.Body.Close() }()
	if traversal.StatusCode != http.StatusNotFound {
		t.Fatalf("traversal status=%d, want 404", traversal.StatusCode)
	}
}
