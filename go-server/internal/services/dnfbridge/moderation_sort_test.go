package dnfbridge

import (
	"context"
	"longheng.io/server/internal/modules/dnf/moderation"
	"strings"
	"testing"
	"time"
)

func TestModerationMySQLSortingAcrossPages(t *testing.T) {
	f := newReplacementMySQLFixture(t)
	ctx := context.Background()
	for i, name := range []string{"main", "other", "third"} {
		ip := []string{"192.0.2.3", "192.0.2.1", "192.0.2.2"}[i]
		if _, err := f.db.Exec("UPDATE "+f.api.table+" SET last_login_ip=? WHERE account=?", ip, name); err != nil {
			t.Fatal(err)
		}
		if _, err := f.db.Exec("INSERT INTO "+f.api.table+"_registration_sources (account_id,ip) VALUES (?,?)", "fixture:"+name, ip); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := f.db.Exec("INSERT INTO "+f.api.table+"_player_accounts (account_id,disabled) VALUES (?,1)", "fixture:other"); err != nil {
		t.Fatal(err)
	}
	for key, want := range map[string]string{"id": "main,other,third", "name": "main,other,third", "lastLoginIp": "other,third,main", "registrationIp": "other,third,main", "disabled": "main,third,other"} {
		for _, direction := range []string{"asc", "desc"} {
			expected := strings.Split(want, ",")
			if direction == "desc" {
				if key == "disabled" {
					expected = []string{"other", "main", "third"}
				} else {
					expected[0], expected[2] = expected[2], expected[0]
				}
			}
			for i, name := range expected {
				p, err := f.api.moderationAccounts(ctx, moderation.Query{SortBy: key, SortOrder: direction, Limit: 1, Offset: i})
				if err != nil || len(p.Rows) != 1 || p.Rows[0].Name != name || p.HasMore != (i < 2) {
					t.Fatalf("%s %s page %d = %+v %v", key, direction, i, p, err)
				}
			}
		}
	}
	now := time.Now().UTC()
	past := now.Add(-time.Hour)
	future := now.Add(time.Hour)
	for i, target := range []string{"a", "b", "c"} {
		var expires any = future
		if i == 1 {
			expires = past
		}
		if i == 2 {
			expires = nil
		}
		kind := "account"
		if i == 0 {
			kind = "ip"
		}
		if _, err := f.db.Exec("INSERT INTO "+f.api.table+"_bans (kind,target,label,reason,expires_at,revoked,revision,updated_at) VALUES (?,?,?,?,?,?,1,?)", kind, target, target, target, expires, i == 2, now.Add(time.Duration(i)*time.Minute)); err != nil {
			t.Fatal(err)
		}
	}
	for key, want := range map[string]string{"kind": "bca", "target": "abc", "label": "abc", "state": "abc", "reason": "abc", "expiresAt": "bac", "updatedAt": "abc"} {
		for _, direction := range []string{"asc", "desc"} {
			expected := want
			if direction == "desc" {
				switch key {
				case "kind":
					expected = "abc"
				case "expiresAt":
					expected = "abc"
				default:
					expected = "cba"
				}
			}
			for i, target := range expected {
				p, err := f.api.moderationBans(ctx, moderation.Query{SortBy: key, SortOrder: direction, Limit: 1, Offset: i})
				if err != nil || len(p.Rows) != 1 || p.Rows[0].Target != string(target) || p.HasMore != (i < 2) {
					t.Fatalf("%s %s page %d = %+v %v", key, direction, i, p, err)
				}
			}
		}
	}
	if _, err := f.api.moderationBans(ctx, moderation.Query{SortBy: "count"}); err == nil {
		t.Fatal("cross-resource sort accepted")
	}
}
