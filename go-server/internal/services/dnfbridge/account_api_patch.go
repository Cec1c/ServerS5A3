package dnfbridge

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// 客户端补丁分发（只读）。
//
// 登陆器的「④ 检查并拉取更新」不需要重新分发整个启动器包，只要服务端把当前
// 补丁负载挂在已有的公网账号 API 端口上即可：
//
//	GET /api/patch/manifest.json                     当前补丁清单（版本 + 每文件大小/sha256/下载地址）
//	GET /api/patch/file?path=<相对路径>               按清单下载单个文件
//
// 负载目录固定为服务器上的 /opt/dnf90/data/patch-dist/（可用
// DNFBRIDGE_PATCH_DIST_DIR 覆盖），内容与分发包里的 patch\ 一致，
// manifest.json 由 /root/make-patch-manifest.sh 生成。
//
// 两条路由都只在白名单内可读：Script.pvf、DNF.exe、顶层 *.dll、
// ImagePacks2/*.npk、lua/**.lua。其余路径（含目录穿越）一律 404，
// 避免把账号表、PVF 原件目录或日志顺手暴露到公网。
const (
	// defaultPatchDistDir 是 HK 服务器上补丁负载的落地目录。
	defaultPatchDistDir = "/opt/dnf90/data/patch-dist"

	patchManifestName = "manifest.json"

	// patchDistMaxFileBytes 是单文件下载上限。当前最大负载是 111 MB 的
	// Script.pvf，512 MB 留足余量又能挡住误放的整包/镜像文件。
	patchDistMaxFileBytes int64 = 512 << 20

	// patchFileWriteTimeout 覆盖账号 API 默认的 10s 写超时：
	// 111 MB 的 PVF 在慢速客户端线路上下载几分钟是正常的。
	patchFileWriteTimeout = 30 * time.Minute
)

// patchManifestFile 是清单里的一条文件记录，字段名和登陆器侧解析的结构一致。
type patchManifestFile struct {
	Path   string `json:"path"`
	Size   int64  `json:"size"`
	SHA256 string `json:"sha256"`
	URL    string `json:"url"`
}

type patchManifest struct {
	Version     string              `json:"version"`
	GeneratedAt string              `json:"generatedAt"`
	Files       []patchManifestFile `json:"files"`
}

// patchDistPathAllowed 判断 payload 根目录下的相对路径是否在白名单内。
// 只接受 "/" 分隔的已清洁相对路径，目录穿越在这里就被拒绝。
func patchDistPathAllowed(rel string) bool {
	if rel == "" || rel == "." || strings.Contains(rel, "\\") {
		return false
	}
	if strings.ContainsRune(rel, 0) || strings.HasPrefix(rel, "/") {
		return false
	}
	if rel != path.Clean(rel) || strings.HasPrefix(rel, "../") || strings.Contains(rel, "/../") {
		return false
	}
	switch {
	case rel == "Script.pvf", rel == "DNF.exe":
		return true
	case !strings.Contains(rel, "/") && strings.EqualFold(path.Ext(rel), ".dll"):
		// 顶层 *.dll：ijl15.dll / ijl15_real.dll / 90CN.dll / 90CNLua.dll
		return true
	case strings.HasPrefix(rel, "ImagePacks2/") && strings.EqualFold(path.Ext(rel), ".npk"):
		return true
	case strings.HasPrefix(rel, "lua/") && strings.EqualFold(path.Ext(rel), ".lua"):
		// lua/** 只放脚本；不允许从这里下发 dll/exe。
		return true
	}
	return false
}

// patchDistRoot 返回补丁负载目录的绝对路径。
func (a *accountAPIServer) patchDistRoot() string {
	root := ""
	if a != nil {
		root = strings.TrimSpace(a.patchDir)
	}
	if root == "" {
		root = defaultPatchDistDir
	}
	if abs, err := filepath.Abs(root); err == nil {
		return abs
	}
	return root
}

// resolvePatchDistFile 把相对路径解析成 payload 目录内的绝对路径。
// 白名单 + filepath.Clean + 前缀校验三重检查，任一不通过都返回 false。
func (a *accountAPIServer) resolvePatchDistFile(rel string) (string, bool) {
	cleaned := path.Clean(strings.TrimSpace(strings.ReplaceAll(rel, "\\", "/")))
	if !patchDistPathAllowed(cleaned) {
		return "", false
	}
	root := a.patchDistRoot()
	full := filepath.Join(root, filepath.FromSlash(cleaned))
	abs, err := filepath.Abs(full)
	if err != nil {
		return "", false
	}
	if abs != root && !strings.HasPrefix(abs, root+string(os.PathSeparator)) {
		return "", false
	}
	return abs, true
}

func (a *accountAPIServer) patchMaxFileBytes() int64 {
	if a != nil && a.patchMaxBytes > 0 {
		return a.patchMaxBytes
	}
	return patchDistMaxFileBytes
}

// handlePatchManifest 返回 /root/make-patch-manifest.sh 生成的 manifest.json。
// 这里会重新校验每条记录：清单里出现白名单之外的路径时宁可报错，
// 也不把一个会诱导登陆器去取越权文件的清单发出去。
func (a *accountAPIServer) handlePatchManifest(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", http.MethodGet)
		writeAccountAPIError(w, http.StatusMethodNotAllowed, "只支持 GET")
		return
	}
	root := a.patchDistRoot()
	raw, err := os.ReadFile(filepath.Join(root, patchManifestName))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			writeAccountAPIError(w, http.StatusServiceUnavailable,
				"补丁清单尚未生成，请在服务器上执行 /root/make-patch-manifest.sh")
			return
		}
		writeAccountAPIError(w, http.StatusInternalServerError, "读取补丁清单失败")
		return
	}
	var manifest patchManifest
	if err := json.Unmarshal(raw, &manifest); err != nil {
		writeAccountAPIError(w, http.StatusInternalServerError, "补丁清单格式不正确")
		return
	}
	for _, file := range manifest.Files {
		if !patchDistPathAllowed(file.Path) {
			writeAccountAPIError(w, http.StatusInternalServerError,
				"补丁清单包含白名单之外的路径，已拒绝下发")
			return
		}
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusOK)
	if r.Method == http.MethodHead {
		return
	}
	_ = json.NewEncoder(w).Encode(manifest)
}

// handlePatchFile 流式下发单个补丁文件（只读打开，不写、不缓存）。
func (a *accountAPIServer) handlePatchFile(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", http.MethodGet)
		writeAccountAPIError(w, http.StatusMethodNotAllowed, "只支持 GET")
		return
	}
	rel := strings.TrimSpace(r.URL.Query().Get("path"))
	if rel == "" {
		writeAccountAPIError(w, http.StatusBadRequest, "缺少 path 参数")
		return
	}
	full, ok := a.resolvePatchDistFile(rel)
	if !ok {
		// 白名单外与目录穿越统一回 404，不区分原因以免透露payload 结构。
		writeAccountAPIError(w, http.StatusNotFound, "补丁文件不存在或不在白名单内")
		return
	}
	file, err := os.Open(full)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			writeAccountAPIError(w, http.StatusNotFound, "补丁文件不存在")
			return
		}
		writeAccountAPIError(w, http.StatusInternalServerError, "读取补丁文件失败")
		return
	}
	defer func() { _ = file.Close() }()

	info, err := file.Stat()
	if err != nil || info.IsDir() {
		writeAccountAPIError(w, http.StatusNotFound, "补丁文件不存在")
		return
	}
	if info.Size() > a.patchMaxFileBytes() {
		writeAccountAPIError(w, http.StatusRequestEntityTooLarge, "补丁文件超过单文件大小上限")
		return
	}

	// 大文件下载需要临时放宽写超时（账号 API 的默认 10s 只适合小 JSON）。
	// httptest.ResponseRecorder 不支持该扩展，失败时按原超时继续。
	_ = http.NewResponseController(w).SetWriteDeadline(time.Now().Add(patchFileWriteTimeout))

	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Length", strconv.FormatInt(info.Size(), 10))
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusOK)
	if r.Method == http.MethodHead {
		return
	}
	if _, err := io.Copy(w, file); err != nil {
		// 客户端中断（大文件下很常见）只记日志，不再改状态码。
		if a != nil && a.service != nil {
			a.service.logWarn("dnfbridge patch download interrupted", "path", rel, "error", err)
		}
	}
}
