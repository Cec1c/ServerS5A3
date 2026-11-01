package dnfbridge

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http/httptest"
	"testing"
	"time"
)

func TestStrictLoginReplacementDeviceTransaction(t *testing.T) {
	for _, failDeviceInsert := range []bool{false, true} {
		t.Run(map[bool]string{false: "success", true: "rollback"}[failDeviceInsert], func(t *testing.T) {
			f := newReplacementMySQLFixture(t)
			f.service.options.playerStrict = true
			f.service.options.playerProxySecret = "fixture-proxy"
			if err := f.api.ensurePlayerSchema(context.Background()); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				for _, suffix := range []string{"_player_tokens", "_player_accounts", "_registration_sources", "_registration_ips"} {
					if _, err := f.db.Exec("DROP TABLE " + f.api.table + suffix); err != nil {
						t.Error(err)
					}
				}
			})
			if failDeviceInsert {
				if _, err := f.db.Exec("CREATE TRIGGER " + f.api.table + "_device_fault BEFORE INSERT ON " + f.api.table + "_player_tokens FOR EACH ROW SIGNAL SQLSTATE '45000' SET MESSAGE_TEXT='fixture failure'"); err != nil {
					t.Fatal(err)
				}
			}
			device := "0123456789abcdef0123456789abcdef"
			body, _ := json.Marshal(accountAPIRequest{Account: "main", Password: "fixture-password", Device: device})
			r := httptest.NewRequest("POST", "/api/account/login", bytes.NewReader(body))
			r.Header.Set("X-Player-Proxy-Key", "fixture-proxy")
			r.Header.Set("X-Player-IP", "192.0.2.44")
			w := httptest.NewRecorder()
			f.api.handleLogin(w, r)
			var result accountAPIResponse
			_ = json.Unmarshal(w.Body.Bytes(), &result)
			if failDeviceInsert {
				if w.Code != 503 || len(f.service.playerAdmission.leases) != 0 {
					t.Fatalf("failed transaction status=%d leases=%d", w.Code, len(f.service.playerAdmission.leases))
				}
				var count int
				if err := f.db.QueryRow("SELECT COUNT(*) FROM " + f.api.loginTokenTable()).Scan(&count); err != nil || count != 0 {
					t.Fatalf("failed device insert left token count=%d err=%v", count, err)
				}
				return
			}
			if w.Code != 200 || result.Token == "" {
				t.Fatalf("login status=%d", w.Code)
			}
			lease := f.service.playerAdmission.leases[result.AccountID]
			if lease == nil || lease.token != result.Token || lease.device != device {
				t.Fatal("committed token missing matching reservation")
			}
			main, aux := &gameSession{}, &gameSession{}
			for _, session := range []*gameSession{main, aux} {
				if err := f.service.authenticatePlayer(session, result.Token); err != nil {
					t.Fatal("strict SQL token/device lookup failed", err)
				}
				if got, err := f.service.authenticateRemoteLogin(session, result.Token); err != nil || got != result.AccountID {
					t.Fatalf("remote login got=%s err=%v", got, err)
				}
			}
			f.service.playerAdmission.detach(main, time.Now())
			if len(lease.connections) != 1 {
				t.Fatal("main socket cleanup lost auxiliary lease")
			}
		})
	}
}
