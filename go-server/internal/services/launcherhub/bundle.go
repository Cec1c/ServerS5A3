package launcherhub

import (
	"archive/zip"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

var bundlePath = regexp.MustCompile(`^/launcher/v2/bundles/([a-f0-9]{64})\.zip$`)
var versionPattern = regexp.MustCompile(`^[0-9]{1,5}\.[0-9]{1,5}\.[0-9]{1,5}$`)

func versionAfter(a, b string) bool {
	aa := strings.Split(a, ".")
	bb := strings.Split(b, ".")
	for i := 0; i < 3; i++ {
		av, _ := strconv.Atoi(aa[i])
		bv, _ := strconv.Atoi(bb[i])
		if av != bv {
			return av > bv
		}
	}
	return false
}
func validateBundle(file *os.File, size int64) error {
	z, e := zip.NewReader(file, size)
	if e != nil {
		return e
	}
	if len(z.File) < 5 || len(z.File) > 2000 {
		return errors.New("invalid bundle file count")
	}
	seen := map[string]bool{}
	var total int64
	for _, f := range z.File {
		if strings.HasSuffix(f.Name, "/") {
			continue
		}
		lower := strings.ToLower(f.Name)
		if !safePath(f.Name) || seen[lower] || f.Mode()&os.ModeSymlink != 0 || f.UncompressedSize64 > 500<<20 {
			return errors.New("invalid bundle path/type/size")
		}
		seen[lower] = true
		total += int64(f.UncompressedSize64)
		if total > 500<<20 {
			return errors.New("bundle too large")
		}
		// Read each member to validate CRC and bound the actual decompressed bytes.
		in, e := f.Open()
		if e != nil {
			return e
		}
		n, e := io.Copy(io.Discard, io.LimitReader(in, int64(f.UncompressedSize64)+1))
		in.Close()
		if e != nil || n != int64(f.UncompressedSize64) {
			return errors.New("corrupt bundle entry")
		}
	}
	for _, name := range []string{"dnf90.exe", "dnf90.core.exe", "flutter_windows.dll", "data/app.so", "data/icudtl.dat"} {
		if !seen[name] {
			return fmt.Errorf("bundle missing %s", name)
		}
	}
	return nil
}
func (h *Hub) uploadBundle(w http.ResponseWriter, r *http.Request) {
	version := strings.TrimPrefix(r.URL.Path, "/admin/api/bundle/")
	if !versionPattern.MatchString(version) {
		failure(w, 400, "版本格式应为 x.y.z")
		return
	}
	dir := filepath.Join(h.cfg.DataDir, "bundles")
	if e := os.MkdirAll(dir, 0700); e != nil {
		failure(w, 500, "无法创建发布目录")
		return
	}
	f, e := os.CreateTemp(dir, ".upload-")
	if e != nil {
		failure(w, 500, "无法暂存发布包")
		return
	}
	defer os.Remove(f.Name())
	defer f.Close()
	hash := sha256.New()
	size, e := io.Copy(io.MultiWriter(f, hash), http.MaxBytesReader(w, r.Body, 300<<20))
	if e != nil {
		failure(w, 400, "上传失败或超过 300 MiB 限制")
		return
	}
	if e = validateBundle(f, size); e != nil {
		failure(w, 400, e.Error())
		return
	}
	if e = f.Sync(); e != nil {
		failure(w, 500, "写入失败")
		return
	}
	f.Close()
	sha := hex.EncodeToString(hash.Sum(nil))
	target := filepath.Join(dir, sha+".zip")
	if e = os.Rename(f.Name(), target); e != nil {
		failure(w, 500, "发布文件保存失败")
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	previous := h.state.Published
	if old := previous.Launcher; old != nil && !versionAfter(version, old.Version) {
		failure(w, 409, "登录器版本必须递增")
		return
	}
	h.state.Published.Launcher = &Bundle{Version: version, URL: strings.TrimRight(h.profile.ApiURL, "/") + "/launcher/v2/bundles/" + sha + ".zip", Size: size, SHA256: sha}
	h.state.Published.Sequence++
	if e = h.save(); e != nil {
		h.state.Published = previous
		failure(w, 500, "签名发布失败")
		return
	}
	reply(w, 200, h.state.Published.Launcher)
}
func (h *Hub) serveBundle(w http.ResponseWriter, r *http.Request) bool {
	match := bundlePath.FindStringSubmatch(r.URL.Path)
	if match == nil {
		return false
	}
	path := filepath.Join(h.cfg.DataDir, "bundles", match[1]+".zip")
	info, e := os.Lstat(path)
	if e != nil || !info.Mode().IsRegular() {
		http.NotFound(w, r)
		return true
	}
	w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
	w.Header().Set("ETag", `"`+match[1]+`"`)
	http.ServeFile(w, r, path)
	return true
}
