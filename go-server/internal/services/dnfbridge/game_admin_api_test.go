package dnfbridge

import (
	"net/http/httptest"
	"strings"
	"testing"
)

func TestGameAdminPrivateContract(t *testing.T) {
	api := &accountAPIServer{service: &Service{options: options{playerAdminSecret: strings.Repeat("x", 32)}}}
	for _, tc := range []struct {
		auth, body string
		code       int
	}{
		{"", `{"version":1,"action":"online","characterId":1}`, 404},
		{"wrong", `{"version":1,"action":"online","characterId":1}`, 404},
		{strings.Repeat("x", 32), `{"version":9,"action":"online","characterId":1}`, 400},
		{strings.Repeat("x", 32), `{"version":1,"action":"online","characterId":1,"extra":true}`, 400},
		{strings.Repeat("x", 32), `{"version":1,"action":"online","characterId":1} {}`, 400},
		{strings.Repeat("x", 32), `{"version":1,"action":"online","characterId":1}`, 200},
	} {
		r := httptest.NewRequest("POST", "/api/game/admin", strings.NewReader(tc.body))
		r.Header.Set("Authorization", "Bearer "+tc.auth)
		w := httptest.NewRecorder()
		api.handleGameAdmin(w, r)
		if w.Code != tc.code {
			t.Fatalf("code=%d want=%d", w.Code, tc.code)
		}
		if tc.code == 200 && (!strings.Contains(w.Body.String(), `"online":false`) || w.Header().Get("Cache-Control") != "no-store") {
			t.Fatal(w.Body.String())
		}
	}
}
