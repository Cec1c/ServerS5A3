package dnfbridge

import (
	"context"
	"longheng.io/server/internal/modules/dnf/qqbinding"
	"testing"
)

func TestAccountSecurityMySQLOptionalBinding(t *testing.T) {
	f := newReplacementMySQLFixture(t)
	ctx := context.Background()
	cfg, err := f.api.saveQQSettings(ctx, qqbinding.Settings{AllowBinding: true, Groups: []string{"123456789"}})
	if err != nil {
		t.Fatal(err)
	}
	if w, out := f.login("main", "fixture-password"); w.Code != 200 || out.BoundQQ != "" {
		t.Fatal(w.Body.String())
	}
	if w, _ := f.login("other", "fixture-password"); w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	w, out := securityRequest(f, "binding-code", "main", "fixture-password", "")
	if w.Code != 200 || !shortBindingCode.MatchString(out.BindingCode) {
		t.Fatal(w.Body.String())
	}
	binding, err := f.api.completeQQBinding(ctx, qqbinding.Complete{Code: out.BindingCode, QQ: "234567890", Group: cfg.Groups[0], Revision: cfg.Revision})
	if err != nil || binding.QQ != "234567890" {
		t.Fatal(binding, err)
	}
	cfg.Enabled = true
	cfg, err = f.api.saveQQSettings(ctx, *cfg)
	if err != nil {
		t.Fatal(err)
	}
	if w, out := f.login("other", "fixture-password"); w.Code != 403 || out.Code != "qq_binding_required" {
		t.Fatal(w.Body.String())
	}
	if w, out := f.login("main", "fixture-password"); w.Code != 200 || out.BoundQQ != binding.QQ {
		t.Fatal(w.Body.String())
	}
	cfg.Enabled = false
	cfg.AllowBinding = false
	if _, err = f.api.saveQQSettings(ctx, *cfg); err != nil {
		t.Fatal(err)
	}
	if w, _ := f.login("other", "fixture-password"); w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	if w, out := securityRequest(f, "binding-code", "other", "fixture-password", ""); w.Code != 409 || out.Code != "binding_disabled" {
		t.Fatal(w.Body.String())
	}
	page, err := f.api.queryQQBindings(ctx, qqbinding.Query{})
	if err != nil || page.Total != 1 {
		t.Fatal(page, err)
	}
}

func TestAccountSecurityMySQLLegacySettingsMigration(t *testing.T) {
	f := newReplacementMySQLFixture(t)
	ctx := context.Background()
	cfg, err := f.api.saveQQSettings(ctx, qqbinding.Settings{Enabled: true, Groups: []string{"123456789"}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.db.Exec("ALTER TABLE " + f.api.table + "_qq_settings DROP COLUMN allow_binding"); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		if err = f.api.ensureAccountSecuritySchema(ctx); err != nil {
			t.Fatal(err)
		}
	}
	after, err := f.api.qqSettings(ctx)
	if err != nil || !after.Enabled || after.AllowBinding || after.Revision != cfg.Revision || len(after.Groups) != 1 || after.Groups[0] != cfg.Groups[0] {
		t.Fatal(after, err)
	}
	if w, _ := f.login("other", "fixture-password"); w.Code != 403 {
		t.Fatal("migration changed required login policy", w.Body.String())
	}
	if w, _ := securityRequest(f, "binding-code", "main", "fixture-password", ""); w.Code != 200 {
		t.Fatal(w.Body.String())
	}
}
