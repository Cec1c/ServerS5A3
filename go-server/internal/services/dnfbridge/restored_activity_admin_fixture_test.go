package dnfbridge

import (
	"net/http/httptest"
	"strings"
	"testing"
)

// Restored activity cases exercise the current container's authenticated API.
func adminTestService(t *testing.T) *Service {
	t.Helper()
	return &Service{options: options{playerAdminSecret: strings.Repeat("x", 32)}}
}

func adminTestCall(s *Service, body, peer, auth string) *httptest.ResponseRecorder {
	r := httptest.NewRequest("POST", "/api/game/admin", strings.NewReader(body))
	r.RemoteAddr = peer
	r.Header.Set("Authorization", "Bearer "+auth)
	w := httptest.NewRecorder()
	(&accountAPIServer{service: s}).handleGameAdmin(w, r)
	return w
}
