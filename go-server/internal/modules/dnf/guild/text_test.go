package guild

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	r "longheng.io/server/internal/modules/dnf/repository"
)

func textRegistry(t *testing.T) (Registry, *Guild, r.GuildOwner, r.GuildOwner) {
	t.Helper()
	reg := NewRegistry()
	now := time.Unix(1700000000, 0)
	a := r.CharacterRecord{CharacterID: "1", AccountID: "a1", Name: "会长"}
	b := r.CharacterRecord{CharacterID: "2", AccountID: "a2", Name: "会员"}
	g, err := reg.Create(a, "测试公会", now)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := reg.JoinDirect(g.ID, b, now); err != nil {
		t.Fatal(err)
	}
	return reg, g, r.GuildOwner{CharacterID: "1", AccountID: "a1"}, r.GuildOwner{CharacterID: "2", AccountID: "a2"}
}

func TestGuildTextPermissionAndOwnership(t *testing.T) {
	for _, tc := range []struct {
		name  string
		grade byte
		mask  *uint32
		want  error
	}{
		{"master", 1, nil, nil}, {"deputy", 2, nil, nil}, {"member", 4, nil, ErrPermissionDenied},
		{"custom_allow", 4, new(NoticePermission), nil}, {"custom_deny", 2, new(uint32(0)), ErrPermissionDenied},
	} {
		t.Run(tc.name, func(t *testing.T) {
			reg, g, _, owner := textRegistry(t)
			m := g.Members[owner.CharacterID]
			m.Grade = tc.grade
			g.Members[owner.CharacterID] = m
			if tc.mask != nil {
				g.GradePermissions = map[byte]uint32{tc.grade: *tc.mask}
			}
			_, err := reg.SetNotice(owner, "notice", time.Now())
			if !errors.Is(err, tc.want) {
				t.Fatalf("error=%v want=%v", err, tc.want)
			}
			if tc.want != nil && g.Notice != "" {
				t.Fatal("denied notice persisted")
			}
			owner.AccountID = "new-owner"
			if _, err := reg.SetNotice(owner, "stale", time.Now()); !errors.Is(err, ErrNotMember) {
				t.Fatal(err)
			}
			if _, err := reg.SetMemberMemo(owner, "stale", time.Now()); !errors.Is(err, ErrNotMember) {
				t.Fatal(err)
			}
		})
	}
}

func TestGuildTextNativeByteLimitsAndWhitespace(t *testing.T) {
	for _, tc := range []struct {
		name, text    string
		notice, valid bool
	}{
		{"notice_empty", "", true, true}, {"notice_spaces", "  公会\r\n公告  ", true, true},
		{"notice_255", strings.Repeat("a", 255), true, true}, {"notice_256", strings.Repeat("a", 256), true, false},
		{"notice_cjk_255", strings.Repeat("会", 127) + "a", true, true}, {"notice_cjk_256", strings.Repeat("会", 128), true, false},
		{"notice_nul", "a\x00b", true, false}, {"notice_tab", "a\tb", true, false},
		{"memo_20", strings.Repeat("a", 20), false, true}, {"memo_21", strings.Repeat("a", 21), false, false},
		{"memo_cjk_20", strings.Repeat("会", 10), false, true}, {"memo_cjk_22", strings.Repeat("会", 11), false, false},
		{"memo_4byte", strings.Repeat("😀", 5), false, true}, {"memo_4byte_over", strings.Repeat("😀", 6), false, false},
		{"memo_empty", "", false, false}, {"memo_blank", "　 ", false, false}, {"memo_newline", "a\nb", false, false},
		{"memo_spaces", "  会友  ", false, true}, {"invalid_utf8", string([]byte{0xff}), true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			reg, g, master, member := textRegistry(t)
			before := g.UpdatedAt
			var err error
			if tc.notice {
				_, err = reg.SetNotice(master, tc.text, before.Add(time.Hour))
			} else {
				_, err = reg.SetMemberMemo(member, tc.text, before.Add(time.Hour))
			}
			if (err == nil) != tc.valid {
				t.Fatalf("err=%v valid=%v", err, tc.valid)
			}
			if !tc.valid && (g.Notice != "" || g.Members["2"].Memo != "" || !g.UpdatedAt.Equal(before)) {
				t.Fatal("invalid input mutated guild")
			}
			if tc.valid {
				got := g.Notice
				if !tc.notice {
					got = g.Members["2"].Memo
				}
				if got != tc.text {
					t.Fatalf("changed whitespace: %q", got)
				}
			}
		})
	}
}

func TestGuildTextReplayAndUnrelatedFields(t *testing.T) {
	reg, g, master, member := textRegistry(t)
	g.Mileage, g.Experience = 35, 70
	m := g.Members["2"]
	m.ContributionPoints = 9
	m.Mileage = 11
	g.Members["2"] = m
	now := g.UpdatedAt.Add(time.Hour)
	if _, err := reg.SetNotice(master, " notice ", now); err != nil {
		t.Fatal(err)
	}
	if _, err := reg.SetMemberMemo(member, " memo ", now); err != nil {
		t.Fatal(err)
	}
	want := g.Members["2"]
	want.Memo = m.Memo
	if !reflect.DeepEqual(want, m) || g.Mileage != 35 || g.Experience != 70 || g.Members["1"].Memo != "" {
		t.Fatal("unrelated member or assets changed")
	}
	if _, err := reg.SetNotice(master, " notice ", now.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if _, err := reg.SetMemberMemo(member, " memo ", now.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if !g.UpdatedAt.Equal(now) {
		t.Fatal("same-value retry changed update time")
	}
}

func TestGuildTextTransactionRollbackAndConcurrentFields(t *testing.T) {
	ctx := context.Background()
	repos := guildTestGroup(t, 2)
	reg, _, master, member := textRegistry(t)
	now := time.Now()
	if err := Save(ctx, repos.Settings, reg, now); err != nil {
		t.Fatal(err)
	}
	boom := errors.New("late save failure")
	err := Update(ctx, lateFailGuildTx{repos.Guild, boom}, []r.GuildOwner{master}, now, func(_ r.Group, reg *Registry) error { _, err := reg.SetNotice(master, "rollback", now); return err })
	if !errors.Is(err, boom) {
		t.Fatal(err)
	}
	loaded, _ := Load(ctx, repos.Settings)
	if loaded.Guilds[1].Notice != "" {
		t.Fatal("rollback lost")
	}
	var wg sync.WaitGroup
	results := make(chan error, 2)
	for _, owner := range []r.GuildOwner{master, member} {
		wg.Go(func() {
			results <- Update(ctx, repos.Guild, []r.GuildOwner{owner}, now, func(_ r.Group, reg *Registry) error {
				if owner == master {
					_, err := reg.SetNotice(owner, "notice", now)
					return err
				}
				_, err := reg.SetMemberMemo(owner, "memo", now)
				return err
			})
		})
	}
	wg.Wait()
	close(results)
	for err := range results {
		if err != nil {
			t.Fatal(err)
		}
	}
	loaded, _ = Load(ctx, repos.Settings)
	if loaded.Guilds[1].Notice != "notice" || loaded.Guilds[1].Members["2"].Memo != "memo" {
		t.Fatal("concurrent fields lost")
	}
}
