package dnfbridge

import (
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// 客户端补丁负载的只读分发端点（登陆器「云端自动拉取补丁」的主端点）：
//
//	GET /patch/manifest.json       当前补丁清单（由 /root/patch-publish.sh 生成）
//	GET /patch/files/<相对路径>     单个文件（http.ServeFile 自带 Range / 206，支持断点续传）
//
// 负载目录 = DNFBRIDGE_PATCH_DIR（默认 /opt/dnf90/patch-payload）。目录不存在时整体
// 404 —— 默认不暴露任何东西。只有一个白名单根目录，拒绝 ".."、绝对路径与符号链接逃逸。
//
// 为什么单独一套（而不是改造 /api/patch/*）：那一套（account_api_patch.go）由
// /root/make-patch-manifest.sh 生成清单、按 ?path= 取文件，且 **不支持 Range**；
// 本套路径直接映射到 URL，靠 http.ServeFile 支持断点续传（111 MB 的 PVF 断流后
// 不必从头再下）。两套并存是为了让已经发出去的旧登陆器（只认 /api/patch/*）继续能用，
// 新登陆器优先走 /patch/*。
const (
	// defaultPatchPayloadDir 是 HK 服务器上补丁负载的落地目录（/root/patch-publish.sh 的目标）。
	defaultPatchPayloadDir = "/opt/dnf90/patch-payload"
)

// patchPayloadRoot 返回补丁负载目录的绝对路径（结构体字段优先，其次环境变量，最后默认值）。
func (a *accountAPIServer) patchPayloadRoot() string {
	root := ""
	if a != nil {
		root = strings.TrimSpace(a.patchPayloadDir)
	}
	if root == "" {
		root = strings.TrimSpace(os.Getenv("DNFBRIDGE_PATCH_DIR"))
	}
	if root == "" {
		root = defaultPatchPayloadDir
	}
	return root
}

// handlePatchFiles 接管 /patch/ 下的两个只读端点。返回 false 表示"没接管"
// （负载目录不存在，或路径不在白名单内），调用方回 404。
func (a *accountAPIServer) handlePatchFiles(w http.ResponseWriter, r *http.Request) bool {
	base, err := filepath.EvalSymlinks(a.patchPayloadRoot())
	if err != nil {
		return false
	}
	info, err := os.Stat(base)
	if err != nil || !info.IsDir() {
		return false
	}

	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", "GET, HEAD")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return true
	}

	// 两个端点，路径与 /root/patch-publish.sh 生成的清单 url 一一对应：
	//	/patch/manifest.json        → <payload>/manifest.json
	//	/patch/files/<相对路径>      → <payload>/<相对路径>
	var rel string
	switch {
	case r.URL.Path == "/patch/manifest.json":
		rel = "manifest.json"
	case strings.HasPrefix(r.URL.Path, "/patch/files/"):
		rel = r.URL.Path[len("/patch/files/"):]
	default:
		http.NotFound(w, r)
		return true
	}
	if rel == "" || strings.Contains(rel, "..") || strings.HasPrefix(rel, "/") {
		http.NotFound(w, r)
		return true
	}
	cleaned := filepath.Clean(filepath.FromSlash(rel))
	if cleaned == "." || cleaned == string(os.PathSeparator) || filepath.IsAbs(cleaned) ||
		cleaned == ".." || strings.HasPrefix(cleaned, ".."+string(os.PathSeparator)) {
		http.NotFound(w, r)
		return true
	}

	target := filepath.Join(base, cleaned)
	// 软链接也要挡住：解析后再确认仍在负载目录内。
	resolved, err := filepath.EvalSymlinks(target)
	if err != nil || !strings.HasPrefix(resolved, base+string(os.PathSeparator)) {
		http.NotFound(w, r)
		return true
	}
	st, err := os.Stat(resolved)
	if err != nil || st.IsDir() {
		http.NotFound(w, r)
		return true
	}

	// 111 MB 的 PVF 必须解除本次请求的写超时：账号 API 全局 WriteTimeout 只有 10s，
	// 不解除会被拦腰掐断（那正是加这套端点的原因之一）。
	if rc := http.NewResponseController(w); rc != nil {
		_ = rc.SetWriteDeadline(time.Time{})
	}
	w.Header().Set("Cache-Control", "no-cache")
	http.ServeFile(w, r, resolved)
	return true
}
