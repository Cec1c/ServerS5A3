package dnfbridge

import (
	"context"
	"fmt"
	"longheng.io/server/internal/modules/dnf/qqbinding"
	"testing"
	"time"
)

func TestQQMembershipMySQLLifecycle(t *testing.T) {
	f := newReplacementMySQLFixture(t)
	ctx := context.Background()
	check := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	cfg, err := f.api.saveQQSettings(ctx, qqbinding.Settings{Enabled: true, Groups: []string{"123456789", "123456788"}})
	check(err)
	code := func(name string) string {
		t.Helper()
		w, out := securityRequest(f, "binding-code", name, "fixture-password", "")
		if w.Code != 200 || out.BindingCode == "" {
			t.Fatal(w.Code, out)
		}
		return out.BindingCode
	}
	bind := func(name, qq, group string) *qqbinding.Binding {
		t.Helper()
		b, err := f.api.completeQQBinding(ctx, qqbinding.Complete{Code: code(name), QQ: qq, Group: group, Revision: cfg.Revision})
		check(err)
		return b
	}
	b := bind("main", "234567890", cfg.Groups[0])
	if checks, err := f.api.claimQQChecks(ctx); err != nil || len(checks) != 0 {
		t.Fatal("not due", checks, err)
	}
	claim := func() qqbinding.Check {
		t.Helper()
		check(f.api.queueQQCheck(ctx, b.QQ))
		checks, err := f.api.claimQQChecks(ctx)
		check(err)
		if len(checks) != 1 {
			t.Fatal(checks)
		}
		return checks[0]
	}
	finish := func(c qqbinding.Check, outcome, group string) bool {
		t.Helper()
		changed, err := f.api.finishQQCheck(ctx, qqbinding.CheckResult{AccountID: c.AccountID, QQ: c.QQ, Revision: c.Revision, SettingsRevision: c.SettingsRevision, Lease: c.Lease, Outcome: outcome, Group: group})
		check(err)
		return changed
	}
	row := func() *qqbinding.Binding {
		t.Helper()
		p, err := f.api.queryQQBindings(ctx, qqbinding.Query{Search: b.QQ})
		check(err)
		if len(p.Rows) != 1 {
			t.Fatal(p)
		}
		return &p.Rows[0]
	}
	c := claim()
	if !finish(c, "unknown", "") || row().State != "active" {
		t.Fatal("unknown invalidated")
	}
	c = claim()
	if !finish(c, "member", cfg.Groups[1]) || row().Group != cfg.Groups[1] {
		t.Fatal("second group ignored")
	}
	c = claim()
	if !finish(c, "absent", "") || row().State != "left" {
		t.Fatal("absence not recorded")
	}
	if finish(c, "member", cfg.Groups[0]) {
		t.Fatal("stale scan reactivated")
	}
	if w, _ := f.login("main", "fixture-password"); w.Code != 403 {
		t.Fatal("inactive binding admitted", w.Code)
	}
	bad := qqbinding.Complete{Code: code("other"), QQ: b.QQ, Group: cfg.Groups[0], Revision: cfg.Revision}
	if _, err = f.api.completeQQBinding(ctx, bad); err == nil {
		t.Fatal("inactive QQ reused by other account")
	}
	bad = qqbinding.Complete{Code: code("main"), QQ: "234567891", Group: cfg.Groups[0], Revision: cfg.Revision}
	if _, err = f.api.completeQQBinding(ctx, bad); err == nil {
		t.Fatal("inactive account changed QQ")
	}
	b = bind("main", b.QQ, cfg.Groups[0])
	if b.State != "active" || row().Group != cfg.Groups[0] {
		t.Fatal(b)
	}
	if w, _ := f.login("main", "fixture-password"); w.Code != 200 {
		t.Fatal(w.Code)
	}
	c = claim()
	b = row()
	change := qqbinding.Change{AccountID: b.AccountID, QQ: b.QQ, Revision: b.Revision, Action: "disable", RequestID: "membership-disable-0001"}
	b, err = f.api.changeQQBinding(ctx, change)
	check(err)
	if finish(c, "absent", "") || row().State != "disabled" {
		t.Fatal("scan overwrote admin")
	}
	if w, _ := securityRequest(f, "binding-code", "main", "fixture-password", ""); w.Code != 403 {
		t.Fatal("disabled code issued", w.Code)
	}
	if _, err = f.api.changeQQBinding(ctx, change); err != nil {
		t.Fatal("retry failed", err)
	}
	enable := qqbinding.Change{AccountID: b.AccountID, QQ: b.QQ, Revision: b.Revision, Action: "enable", RequestID: "membership-enable-0001"}
	b, err = f.api.changeQQBinding(ctx, enable)
	check(err)
	if _, err = f.api.changeQQBinding(ctx, change); err != nil || row().State != "active" {
		t.Fatal("old retry overwrote enable", err)
	}
	c = claim()
	old := row()
	deleteChange := qqbinding.Change{AccountID: old.AccountID, QQ: old.QQ, Revision: old.Revision, Action: "delete", RequestID: "membership-delete-0001"}
	deleted, err := f.api.changeQQBinding(ctx, deleteChange)
	check(err)
	if deleted != nil {
		t.Fatal(deleted)
	}
	b = bind("main", old.QQ, cfg.Groups[0])
	if b.Revision == old.Revision {
		t.Fatal("identity generation reused")
	}
	if finish(c, "absent", "") {
		t.Fatal("deleted generation updated new binding")
	}
	_, err = f.api.changeQQBinding(ctx, deleteChange)
	check(err)
	if row().State != "active" {
		t.Fatal("delete replay deleted replacement")
	}
	var next int64
	check(f.db.QueryRow("SELECT next_check_at FROM "+f.api.table+"_qq_bindings WHERE account_id=?", b.AccountID).Scan(&next))
	if delta := next - time.Now().Unix(); delta < 21590 || delta > 21600 {
		t.Fatal("six-hour schedule", delta)
	}
	check(f.api.ensureAccountSecuritySchema(ctx))
	if row().QQ != b.QQ {
		t.Fatal("migration removed relation")
	}
}

func TestQQMembershipMySQLStalePolicyAndOptional(t *testing.T) {
	f := newReplacementMySQLFixture(t)
	ctx := context.Background()
	cfg, err := f.api.saveQQSettings(ctx, qqbinding.Settings{AllowBinding: true, Groups: []string{"123456789"}})
	if err != nil {
		t.Fatal(err)
	}
	_, out := securityRequest(f, "binding-code", "main", "fixture-password", "")
	b, err := f.api.completeQQBinding(ctx, qqbinding.Complete{Code: out.BindingCode, QQ: "234567890", Group: cfg.Groups[0], Revision: cfg.Revision})
	if err != nil {
		t.Fatal(err)
	}
	if err = f.api.queueQQCheck(ctx, b.QQ); err != nil {
		t.Fatal(err)
	}
	checks, err := f.api.claimQQChecks(ctx)
	if err != nil || len(checks) != 1 {
		t.Fatal(checks, err)
	}
	c := checks[0]
	cfg.Groups = []string{"123456788"}
	if _, err = f.api.saveQQSettings(ctx, *cfg); err != nil {
		t.Fatal(err)
	}
	changed, err := f.api.finishQQCheck(ctx, qqbinding.CheckResult{AccountID: c.AccountID, QQ: c.QQ, Revision: c.Revision, SettingsRevision: c.SettingsRevision, Lease: c.Lease, Outcome: "absent"})
	if err != nil || changed {
		t.Fatal(changed, err)
	}
	page, err := f.api.queryQQBindings(ctx, qqbinding.Query{})
	if err != nil {
		t.Fatal(err)
	}
	b = &page.Rows[0]
	_, err = f.api.changeQQBinding(ctx, qqbinding.Change{AccountID: b.AccountID, QQ: b.QQ, Revision: b.Revision, Action: "disable", RequestID: fmt.Sprintf("optional-disable-%d", b.Revision)})
	if err != nil {
		t.Fatal(err)
	}
	if w, login := f.login("main", "fixture-password"); w.Code != 200 || login.BoundQQ != "" {
		t.Fatal("optional login policy changed", w.Code, login)
	}
}
