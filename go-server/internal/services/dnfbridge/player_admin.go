package dnfbridge

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"net"
	"net/http"
	"strings"
	"time"

	"golang.org/x/crypto/bcrypt"
)

// This endpoint is reachable only on the existing private account listener and
// is deliberately absent from the public gateway route allowlist.
func (a *accountAPIServer) handlePlayerAdmin(w http.ResponseWriter, r *http.Request) {
	secret := a.service.options.playerAdminSecret
	if len(secret) < 32 || subtle.ConstantTimeCompare([]byte(r.Header.Get("Authorization")), []byte("Bearer "+secret)) != 1 {
		http.NotFound(w, r)
		return
	}
	if r.Method != "POST" {
		w.WriteHeader(405)
		return
	}
	var request struct{ Action, Account, Password string }
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&request); err != nil {
		writeAccountAPIError(w, 400, "invalid request")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	if request.Action == "list" {
		rows, err := a.db.QueryContext(ctx, "SELECT a.account, a.account_id, COALESCE(p.disabled,0), COALESCE(r.ip,'') FROM "+a.table+" a LEFT JOIN "+a.table+"_player_accounts p ON BINARY p.account_id=BINARY a.account_id LEFT JOIN "+a.table+"_registration_sources r ON BINARY r.account_id=BINARY a.account_id ORDER BY a.id LIMIT 1000")
		if err != nil {
			writeAccountAPIError(w, 500, "query failed")
			return
		}
		defer rows.Close()
		type item struct {
			Account        string `json:"account"`
			AccountID      string `json:"account_id"`
			Disabled       bool   `json:"disabled"`
			RegistrationIP string `json:"registration_ip"`
		}
		items := []item{}
		for rows.Next() {
			var i item
			if err = rows.Scan(&i.Account, &i.AccountID, &i.Disabled, &i.RegistrationIP); err != nil {
				writeAccountAPIError(w, 500, "query failed")
				return
			}
			items = append(items, i)
		}
		if rows.Err() != nil {
			writeAccountAPIError(w, 500, "query failed")
			return
		}
		json.NewEncoder(w).Encode(map[string]any{"ok": true, "accounts": items})
		return
	}
	p := &a.service.playerAdmission
	if request.Action == "sessions" {
		p.mu.Lock()
		p.prune(time.Now())
		items := []map[string]any{}
		for _, lease := range p.leases {
			items = append(items, map[string]any{"account_id": lease.account, "connections": len(lease.connections), "reserved_until": lease.until})
		}
		p.mu.Unlock()
		json.NewEncoder(w).Encode(map[string]any{"ok": true, "sessions": items})
		return
	}
	if request.Action != "disable" && request.Action != "enable" && request.Action != "reset" && request.Action != "kick" {
		writeAccountAPIError(w, 400, "unknown action")
		return
	}
	var hash []byte
	if request.Action == "reset" {
		if len(request.Password) < 6 || len(request.Password) > 64 {
			writeAccountAPIError(w, 400, "password must be 6-64 bytes")
			return
		}
		var err error
		hash, err = bcrypt.GenerateFromPassword([]byte(request.Password), bcrypt.DefaultCost)
		if err != nil {
			writeAccountAPIError(w, 500, "password processing failed")
			return
		}
	}
	p.mu.Lock()
	var accountID string
	if err := a.db.QueryRowContext(ctx, "SELECT account_id FROM "+a.table+" WHERE account=?", strings.TrimSpace(request.Account)).Scan(&accountID); err != nil {
		p.mu.Unlock()
		writeAccountAPIError(w, 404, "account not found")
		return
	}
	err := a.mutatePlayerAccount(ctx, accountID, request.Action, hash)
	var connections []net.Conn
	if err == nil && request.Action != "enable" {
		if lease := p.leases[accountID]; lease != nil {
			for session := range lease.connections {
				if session.conn != nil {
					connections = append(connections, session.conn)
				}
			}
			delete(p.leases, accountID)
		}
	}
	p.mu.Unlock()
	for _, conn := range connections {
		conn.Close()
	}
	if err != nil {
		writeAccountAPIError(w, 500, "account update failed")
		return
	}
	writeAccountAPIJSON(w, 200, accountAPIResponse{OK: true})
}

func (a *accountAPIServer) mutatePlayerAccount(ctx context.Context, accountID, action string, hash []byte) error {
	tx, err := a.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if action == "disable" || action == "enable" {
		_, err = tx.ExecContext(ctx, "INSERT INTO "+a.table+"_player_accounts (account_id,disabled) VALUES (?,?) ON DUPLICATE KEY UPDATE disabled=VALUES(disabled)", accountID, action == "disable")
	} else if action == "reset" {
		_, err = tx.ExecContext(ctx, "UPDATE "+a.table+" SET password_hash=? WHERE account_id=?", string(hash), accountID)
	}
	if err != nil {
		return err
	}
	if action != "enable" {
		if _, err = tx.ExecContext(ctx, "DELETE FROM "+a.loginTokenTable()+" WHERE account_id=?", accountID); err != nil {
			return err
		}
	}
	return tx.Commit()
}
