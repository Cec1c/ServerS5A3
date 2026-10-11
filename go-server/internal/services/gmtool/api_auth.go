package gmtool

import (
	"net/http"
	"strings"
)

// handleStatus 复刻 A21 的 /api/status。前端完全由它决定登录面板和工作区，
// 所以字段名必须齐全（缺字段会让页面停在“等待选择数据源”）。
func (s *Server) handleStatus(w http.ResponseWriter, r *http.Request) {
	_, repoErr := s.repositories()
	ready := repoErr == nil
	authenticated := s.authenticated(r)

	var indexError any
	if err := s.items.failure(); err != nil {
		indexError = err.Error()
	}

	writeJSON(w, map[string]any{
		"configured":             true,
		"ready":                  ready,
		"loading":                !ready,
		"database":               s.cfg.DatabaseLabel,
		"pvf":                    s.cfg.PVFPath,
		"serverBin":              nil,
		"indexReady":             s.items.isReady(),
		"indexError":             indexError,
		"error":                  errorText(repoErr),
		"hasError":               repoErr != nil,
		"toolVersion":            ToolVersion,
		"schemaVersion":          nil,
		"baselineId":             BaselineID,
		"structureCompatible":    true,
		"migrationRequired":      false,
		"migrationBlocked":       false,
		"databaseUnusable":       repoErr != nil,
		"authenticationRequired": true,
		"authenticated":          authenticated,
		"canChangeSource":        false,
	})
}

type loginRequest struct {
	Password string `json:"password"`
}

func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	body, ok := decodeBody[loginRequest](w, r)
	if !ok {
		return
	}
	ip := clientIP(r)
	if !s.allowLogin(ip) {
		writeFailure(w, "密码尝试过于频繁，请稍后再试。")
		return
	}
	expected := sha256Sum(s.cfg.Password)
	provided := sha256Sum(body.Password)
	if !equalHash(expected, provided) {
		s.recordLoginFailure(ip)
		writeFailure(w, "密码错误。")
		return
	}
	token, err := s.startSession()
	if err != nil {
		writeFailure(w, "会话创建失败。")
		return
	}
	s.clearLoginFailures(ip)
	http.SetCookie(w, &http.Cookie{
		Name:     CookieName,
		Value:    token,
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteStrictMode,
		Secure:   r.TLS != nil,
		MaxAge:   int(sessionTTL / 1e9),
	})
	writeOK(w, map[string]any{"authenticated": true})
}

func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request) {
	if cookie, err := r.Cookie(CookieName); err == nil && cookie != nil {
		s.endSession(cookie.Value)
	}
	http.SetCookie(w, &http.Cookie{
		Name:     CookieName,
		Value:    "",
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteStrictMode,
		MaxAge:   -1,
	})
	writeOK(w, nil)
}

func errorText(err error) any {
	if err == nil {
		return nil
	}
	text := strings.TrimSpace(err.Error())
	if text == "" {
		return nil
	}
	return text
}
