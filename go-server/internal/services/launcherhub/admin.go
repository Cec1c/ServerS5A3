package launcherhub

import (
	"crypto/subtle"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"strings"
	"time"
)

const cookieName = "dnf90_admin"

func reply(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}
func failure(w http.ResponseWriter, status int, message string) {
	reply(w, status, map[string]any{"error": message, "success": false, "loginRequired": status == 401})
}
func readBody(w http.ResponseWriter, r *http.Request, v any) error {
	if !strings.HasPrefix(r.Header.Get("Content-Type"), "application/json") {
		return errors.New("JSON body required")
	}
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 128<<10))
	dec.DisallowUnknownFields()
	if e := dec.Decode(v); e != nil {
		return errors.New("invalid request")
	}
	if dec.Decode(new(any)) != io.EOF {
		return errors.New("extra JSON data")
	}
	return nil
}
func originAllowed(r *http.Request) bool {
	if r.Header.Get("Sec-Fetch-Site") == "cross-site" {
		return false
	}
	if origin := r.Header.Get("Origin"); origin != "" {
		scheme := "http"
		if r.TLS != nil {
			scheme = "https"
		}
		return origin == scheme+"://"+r.Host
	}
	return true
}
func (h *Hub) identity(r *http.Request) (string, session, bool) {
	if auth := r.Header.Get("Authorization"); strings.HasPrefix(auth, "Bearer ") {
		actual := digest([]byte(strings.TrimPrefix(auth, "Bearer ")))
		if subtle.ConstantTimeCompare([]byte(actual), []byte(strings.ToLower(h.cfg.AdminTokenSHA256))) == 1 {
			return "bearer", session{}, true
		}
		return "", session{}, false
	}
	c, e := r.Cookie(cookieName)
	if e != nil {
		return "", session{}, false
	}
	id := digest([]byte(c.Value))
	h.mu.Lock()
	s, ok := h.state.Sessions[id]
	h.mu.Unlock()
	return id, s, ok && time.Now().Before(s.Expires)
}
func (h *Hub) login(w http.ResponseWriter, r *http.Request) {
	var input struct {
		Token    string `json:"token"`
		Remember bool   `json:"remember"`
	}
	if e := readBody(w, r, &input); e != nil {
		failure(w, 400, e.Error())
		return
	}
	ip, _, _ := net.SplitHostPort(r.RemoteAddr)
	now := time.Now()
	h.mu.Lock()
	defer h.mu.Unlock()
	for k, list := range h.failures {
		remaining := list[:0]
		for _, t := range list {
			if now.Sub(t) < 10*time.Minute {
				remaining = append(remaining, t)
			}
		}
		if len(remaining) == 0 {
			delete(h.failures, k)
		} else {
			h.failures[k] = remaining
		}
	}
	if len(h.failures[ip]) >= 8 || len(h.failures) > 2048 {
		failure(w, 429, "尝试过于频繁，请稍后重试")
		return
	}
	actual := digest([]byte(input.Token))
	if subtle.ConstantTimeCompare([]byte(actual), []byte(strings.ToLower(h.cfg.AdminTokenSHA256))) != 1 {
		h.failures[ip] = append(h.failures[ip], now)
		failure(w, 401, "Admin Token 无效")
		return
	}
	delete(h.failures, ip)
	for k, s := range h.state.Sessions {
		if now.After(s.Expires) {
			delete(h.state.Sessions, k)
			delete(h.gmCookies, k)
		}
	}
	if len(h.state.Sessions) >= 1024 {
		failure(w, 503, "会话已满，请注销旧会话")
		return
	}
	ttl := 12 * time.Hour
	maxAge := 0
	if input.Remember {
		ttl = 30 * 24 * time.Hour
		maxAge = int(ttl / time.Second)
	}
	token := randomHex()
	id := digest([]byte(token))
	s := session{Expires: now.Add(ttl), CSRF: randomHex()}
	h.state.Sessions[id] = s
	if e := h.save(); e != nil {
		delete(h.state.Sessions, id)
		failure(w, 500, "无法保存会话")
		return
	}
	http.SetCookie(w, &http.Cookie{Name: cookieName, Value: token, Path: "/", HttpOnly: true, Secure: r.TLS != nil, SameSite: http.SameSiteStrictMode, MaxAge: maxAge})
	reply(w, 200, map[string]any{"ok": true, "csrf": s.CSRF, "expires": s.Expires})
}
func (h *Hub) logout(w http.ResponseWriter, r *http.Request, id string) {
	h.mu.Lock()
	old, exists := h.state.Sessions[id]
	delete(h.state.Sessions, id)
	delete(h.gmCookies, id)
	e := h.save()
	if e != nil && exists {
		h.state.Sessions[id] = old
	}
	h.mu.Unlock()
	if e != nil {
		failure(w, 500, "无法注销会话，请重试")
		return
	}
	http.SetCookie(w, &http.Cookie{Name: cookieName, Value: "", Path: "/", HttpOnly: true, Secure: r.TLS != nil, SameSite: http.SameSiteStrictMode, MaxAge: -1})
	reply(w, 200, map[string]bool{"ok": true})
}
func (h *Hub) AdminHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "no-referrer")
		unsafe := r.Method != "GET" && r.Method != "HEAD"
		if unsafe && !originAllowed(r) {
			failure(w, 403, "来源验证失败")
			return
		}
		if (r.Method == "GET" || r.Method == "HEAD") && (r.URL.Path == "/admin/" || r.URL.Path == "/admin/app.js" || r.URL.Path == "/admin/style.css" || r.URL.Path == "/admin/gm-session.js") {
			name := strings.TrimPrefix(r.URL.Path, "/admin/")
			if name == "" {
				name = "index.html"
			}
			raw, e := web.ReadFile("www/" + name)
			if e != nil {
				http.NotFound(w, r)
				return
			}
			w.Header().Set("Content-Security-Policy", "default-src 'self'; script-src 'self'; style-src 'self'; img-src 'self' data:; frame-ancestors 'none'; base-uri 'none'; form-action 'self'")
			contentType := map[string]string{"index.html": "text/html; charset=utf-8", "app.js": "text/javascript; charset=utf-8", "gm-session.js": "text/javascript; charset=utf-8", "style.css": "text/css; charset=utf-8"}[name]
			w.Header().Set("Content-Type", contentType)
			w.Write(raw)
			return
		}
		if r.URL.Path == "/" && r.Method == "GET" {
			http.Redirect(w, r, "/admin/", 302)
			return
		}
		if r.URL.Path == "/admin/api/login" && r.Method == "POST" {
			h.login(w, r)
			return
		}
		id, s, ok := h.identity(r)
		if !ok {
			if r.URL.Path == "/gm/" {
				http.Redirect(w, r, "/admin/", 302)
			} else {
				failure(w, 401, "请先验证 Admin Token")
			}
			return
		}
		if unsafe && id != "bearer" {
			// GM's existing frontend sends same-origin JSON but has no hub CSRF header.
			// Require a browser Origin for that path and an unguessable CSRF token for hub APIs.
			if strings.HasPrefix(r.URL.Path, "/admin/") {
				if subtle.ConstantTimeCompare([]byte(r.Header.Get("X-CSRF-Token")), []byte(s.CSRF)) != 1 {
					failure(w, 403, "会话校验失败")
					return
				}
			} else if r.Header.Get("Origin") == "" {
				failure(w, 403, "GM 写入必须来自同源页面")
				return
			}
		}
		switch {
		case r.Method == "POST" && strings.HasPrefix(r.URL.Path, "/admin/api/bundle/"):
			h.uploadBundle(w, r)
		case r.Method == "POST" && (r.URL.Path == "/admin/api/logout" || r.URL.Path == "/api/auth/logout"):
			h.logout(w, r, id)
		case r.Method == "GET" && r.URL.Path == "/admin/api/state":
			h.mu.Lock()
			defer h.mu.Unlock()
			reply(w, 200, map[string]any{"csrf": s.CSRF, "expires": s.Expires, "sources": h.state.Sources, "published": h.state.Published, "gmConfigured": h.cfg.GMUpstream != ""})
		case r.Method == "PUT" && r.URL.Path == "/admin/api/source":
			var source Source
			if e := readBody(w, r, &source); e != nil {
				failure(w, 400, e.Error())
				return
			}
			if e := source.validate(); e != nil {
				failure(w, 400, e.Error())
				return
			}
			h.mu.Lock()
			defer h.mu.Unlock()
			old := append([]Source{}, h.state.Sources...)
			found := false
			for i, s := range h.state.Sources {
				if s.ID == source.ID {
					h.state.Sources[i] = source
					found = true
					break
				}
			}
			if !found {
				if len(h.state.Sources) >= 32 {
					failure(w, 400, "来源数量超过限制")
					return
				}
				h.state.Sources = append(h.state.Sources, source)
			}
			if e := h.save(); e != nil {
				h.state.Sources = old
				failure(w, 500, "保存失败")
				return
			}
			reply(w, 200, map[string]bool{"ok": true})
		case r.Method == "POST" && r.URL.Path == "/admin/api/check":
			var input struct {
				ID      string `json:"id"`
				Channel string `json:"channel"`
			}
			if e := readBody(w, r, &input); e != nil {
				failure(w, 400, e.Error())
				return
			}
			candidate, m, hash, e := h.inspect(r.Context(), input.ID, input.Channel)
			if e != nil {
				failure(w, 400, e.Error())
				return
			}
			content, compatibilityError := selectContent(m, candidate.Source, h.profile, hash)
			message := ""
			if compatibilityError != nil {
				message = compatibilityError.Error()
			}
			downloadable := []File{}
			for _, f := range m.Files {
				if f.Downloadable {
					downloadable = append(downloadable, f)
				}
			}
			reply(w, 200, map[string]any{"candidateId": candidate.ID, "version": m.Version, "sequence": m.Sequence, "totalFiles": len(m.Files), "downloadable": downloadable, "compatible": compatibilityError == nil, "compatibilityError": message, "content": content})
		case r.Method == "POST" && r.URL.Path == "/admin/api/publish":
			var input struct {
				CandidateID string `json:"candidateId"`
				Confirm     bool   `json:"confirmCompatibility"`
			}
			if e := readBody(w, r, &input); e != nil || !input.Confirm {
				failure(w, 400, "需要明确确认已核对配套资源和服务端兼容")
				return
			}
			result, e := h.publish(input.CandidateID)
			if e != nil {
				failure(w, 409, e.Error())
				return
			}
			reply(w, 200, result)
		case r.Method == "PUT" && r.URL.Path == "/admin/api/news":
			var news []News
			if e := readBody(w, r, &news); e != nil || len(news) > 30 {
				failure(w, 400, "公告格式或数量无效")
				return
			}
			for _, n := range news {
				if len(n.Title) > 160 || len(n.Body) > 12000 || len(n.Date) > 32 || (n.Category != "综合" && n.Category != "更新" && n.Category != "指南") {
					failure(w, 400, "公告长度或分类无效")
					return
				}
			}
			h.mu.Lock()
			defer h.mu.Unlock()
			previous := h.state.Published
			h.state.Published.News = news
			h.state.Published.Sequence++
			if e := h.save(); e != nil {
				h.state.Published = previous
				failure(w, 500, "发布公告失败")
				return
			}
			reply(w, 200, map[string]bool{"ok": true})
		case strings.HasPrefix(r.URL.Path, "/admin/"):
			http.NotFound(w, r)
		default:
			h.gm(w, r, id)
		}
	})
}
