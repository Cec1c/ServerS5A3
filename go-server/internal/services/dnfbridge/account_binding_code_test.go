package dnfbridge

import (
	"bytes"
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"longheng.io/server/internal/modules/dnf/qqbinding"
)

func TestBindingCodeMySQLCollisionAndLeadingZero(t *testing.T) {
	f := newReplacementMySQLFixture(t)
	ctx := context.Background()
	until := time.Now().Add(10 * time.Minute).Unix()
	reserve := func(id string, data []byte) string {
		t.Helper()
		tx, err := f.db.BeginTx(ctx, nil)
		if err != nil {
			t.Fatal(err)
		}
		defer tx.Rollback()
		code, err := f.api.reserveBindingCode(ctx, tx, id, until, bytes.NewReader(data))
		if err != nil {
			t.Fatal(err)
		}
		if err = tx.Commit(); err != nil {
			t.Fatal(err)
		}
		return code
	}
	if code := reserve("first", []byte{0, 0, 1}); code != "000001" {
		t.Fatal(code)
	}
	// An active code, including a consumed-code reservation, is never reassigned.
	if code := reserve("second", []byte{0, 0, 1, 0, 0, 2}); code != "000002" {
		t.Fatal(code)
	}
	if _, err := f.db.Exec("UPDATE "+f.api.table+"_qq_code_reservations SET expires_at=0 WHERE code_hash=?", bindingCodeHash("000001")); err != nil {
		t.Fatal(err)
	}
	if code := reserve("third", []byte{0, 0, 1}); code != "000001" {
		t.Fatal(code)
	}
	var owner string
	if err := f.db.QueryRow("SELECT account_id FROM "+f.api.table+"_qq_code_reservations WHERE code_hash=?", bindingCodeHash("000001")).Scan(&owner); err != nil || owner != "third" {
		t.Fatal(owner, err)
	}
}

func TestBindingCodeMySQLConsumedAndIdentity(t *testing.T) {
	f := newReplacementMySQLFixture(t)
	ctx := context.Background()
	cfg, err := f.api.saveQQSettings(ctx, qqbinding.Settings{AllowBinding: true, Groups: []string{"123456789"}})
	if err != nil {
		t.Fatal(err)
	}
	w, out := securityRequest(f, "binding-code", "main", "fixture-password", "")
	if w.Code != 200 || !shortBindingCode.MatchString(out.BindingCode) || out.ExpiresAt-time.Now().Unix() < 599 || out.ExpiresAt-time.Now().Unix() > 600 {
		t.Fatal(w.Body.String())
	}
	// Six-digit lookup uses the code's owner, never an optional legacy account.
	c := qqbinding.Complete{Account: "other", Code: out.BindingCode, QQ: "234567890", Group: cfg.Groups[0], Revision: cfg.Revision}
	b, err := f.api.completeQQBinding(ctx, c)
	if err != nil || b.Account != "main" {
		t.Fatal(b, err)
	}
	var count int
	if err = f.db.QueryRow("SELECT COUNT(*) FROM "+f.api.table+"_qq_codes WHERE account_id=?", b.AccountID).Scan(&count); err != nil || count != 0 {
		t.Fatal(count, err)
	}
	if _, err = f.api.completeQQBinding(ctx, c); err == nil {
		t.Fatal("consumed code replayed")
	}
	if _, err = f.db.Exec("DELETE FROM "+f.api.table+"_qq_bindings WHERE account_id=?", b.AccountID); err != nil {
		t.Fatal(err)
	}
	if _, err = f.api.completeQQBinding(ctx, c); err == nil {
		t.Fatal("unbinding resurrected consumed code")
	}
}

func TestBindingCodeMySQLConcurrentReservation(t *testing.T) {
	f := newReplacementMySQLFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	type outcome struct {
		code string
		err  error
	}
	results := make(chan outcome, 2)
	start := make(chan struct{})
	for _, n := range []byte{2, 3} {
		go func(n byte) {
			<-start
			tx, err := f.db.BeginTx(ctx, nil)
			if err != nil {
				results <- outcome{err: err}
				return
			}
			defer tx.Rollback()
			code, err := f.api.reserveBindingCode(ctx, tx, string([]byte{'a' + n}), time.Now().Add(10*time.Minute).Unix(), bytes.NewReader([]byte{0, 0, 1, 0, 0, n}))
			if err == nil {
				err = tx.Commit()
			}
			results <- outcome{code, err}
		}(n)
	}
	close(start)
	first, second := <-results, <-results
	if first.err != nil || second.err != nil || first.code == second.code {
		t.Fatal(first, second)
	}
}

func TestBindingCodeMySQLPersistentConcurrentBudget(t *testing.T) {
	f := newReplacementMySQLFixture(t)
	ctx := context.Background()
	var wg sync.WaitGroup
	results := make(chan error, 12)
	for i := 0; i < 12; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); results <- f.api.chargeBindingAttempt(ctx, "234567890") }()
	}
	wg.Wait()
	close(results)
	accepted := 0
	for err := range results {
		if err == nil {
			accepted++
		} else if !strings.Contains(err.Error(), "过于频繁") {
			t.Fatal(err)
		}
	}
	if accepted != 6 {
		t.Fatal(accepted)
	}
	restarted := &accountAPIServer{db: f.db, table: f.api.table}
	if err := restarted.chargeBindingAttempt(ctx, "234567890"); err == nil {
		t.Fatal("restart reset budget")
	}
	if w, _ := f.login("main", "fixture-password"); w.Code != 200 {
		t.Fatal("binding cooldown affected login")
	}
	if _, err := f.db.Exec("UPDATE "+f.api.table+"_auth_failures SET updated_at=? WHERE subject=?", time.Now().Add(-10*time.Minute).Unix(), bindingCodeHash("qq-binding:234567890")); err != nil {
		t.Fatal(err)
	}
	if err := restarted.chargeBindingAttempt(ctx, "234567890"); err != nil {
		t.Fatal(err)
	}
}

func TestBindingCodeMySQLLegacyAndSchemaUpgrade(t *testing.T) {
	f := newReplacementMySQLFixture(t)
	ctx := context.Background()
	cfg, err := f.api.saveQQSettings(ctx, qqbinding.Settings{AllowBinding: true, Groups: []string{"123456789"}})
	if err != nil {
		t.Fatal(err)
	}
	var id string
	if err = f.db.QueryRow("SELECT account_id FROM " + f.api.table + " WHERE account='main'").Scan(&id); err != nil {
		t.Fatal(err)
	}
	code := strings.Repeat("a", 32)
	if _, err = f.db.Exec("INSERT INTO "+f.api.table+"_qq_codes(account_id,code_hash,expires_at) VALUES(?,?,?)", id, bindingCodeHash(code), time.Now().Add(10*time.Minute).Unix()); err != nil {
		t.Fatal(err)
	}
	if _, err = f.db.Exec("DROP TABLE " + f.api.table + "_qq_code_reservations"); err != nil {
		t.Fatal(err)
	}
	if err = f.api.ensureAccountSecuritySchema(ctx); err != nil {
		t.Fatal(err)
	}
	if err = f.api.ensureAccountSecuritySchema(ctx); err != nil {
		t.Fatal(err)
	}
	c := qqbinding.Complete{Account: "main", Code: code, QQ: "234567890", Group: cfg.Groups[0], Revision: cfg.Revision}
	if _, err = f.api.completeQQBinding(ctx, c); err != nil {
		t.Fatal(err)
	}
	if _, err = f.api.completeQQBinding(ctx, c); err == nil {
		t.Fatal("legacy code replayed")
	}
}
