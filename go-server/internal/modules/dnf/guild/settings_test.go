package guild

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	r "longheng.io/server/internal/modules/dnf/repository"
)

func TestGuildSettingsEverySelectedPermissionIsRequired(t *testing.T) {
	for _, tc := range []struct {
		name             string
		mask, permission uint32
		grade            byte
		denied           bool
	}{
		{"all_master", SettingsMask, GradePermissionMask, 1, false},
		{"join_only", SettingsJoinMode, SettingsJoinMode, 4, false},
		{"promotion_only", SettingsPromotion, SettingsPromotion, 4, false},
		{"tags_only", SettingsTags, SettingsTags, 4, false},
		{"missing_promotion", SettingsMask, SettingsJoinMode | SettingsTags, 2, true},
		{"deputy_default", SettingsMask, DefaultGradePermissions(2), 2, true},
		{"ordinary", SettingsMask, 0, 4, true},
		{"master_tags_fallback", SettingsTags, 0, 1, false},
		{"master_join_no_fallback", SettingsJoinMode, 0, 1, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			reg, g, _, owner := textRegistry(t)
			m := g.Members["2"]
			m.Grade = tc.grade
			g.Members["2"] = m
			g.GradePermissions = map[byte]uint32{tc.grade: tc.permission}
			before, _ := json.Marshal(g)
			_, err := reg.SetSettings(owner, SettingsChange{Mask: tc.mask, Promotion: "介绍", WebPromotion: true, Tags: 5, JoinMode: 0}, time.Now())
			if tc.denied {
				if !errors.Is(err, ErrPermissionDenied) {
					t.Fatal(err)
				}
				after, _ := json.Marshal(g)
				if string(before) != string(after) {
					t.Fatal("partial write before permission denial")
				}
			} else if err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestGuildSettingsIgnoreUnselectedFieldsAndReplay(t *testing.T) {
	reg, g, master, _ := textRegistry(t)
	g.Promotion = "keep"
	g.WebPromotion = true
	g.Tags = []byte{1, 3}
	now := g.UpdatedAt.Add(time.Hour)
	if _, err := reg.SetSettings(master, SettingsChange{Mask: SettingsJoinMode, JoinMode: 0, Promotion: "", Tags: 0xffffffff}, now); err != nil {
		t.Fatal(err)
	}
	if g.JoinMode != 0 || g.Promotion != "keep" || !g.WebPromotion || !reflect.DeepEqual(g.Tags, []byte{1, 3}) {
		t.Fatal("unselected fields overwritten")
	}
	if _, err := reg.SetSettings(master, SettingsChange{Mask: SettingsTags, Tags: 10, JoinMode: 2}, now.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if !g.UpdatedAt.Equal(now) {
		t.Fatal("same semantic tags changed timestamp")
	}
	if _, err := reg.SetSettings(master, SettingsChange{Mask: SettingsTags, Tags: 0, JoinMode: 2}, now.Add(2*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if len(g.Tags) != 0 {
		t.Fatal("tags did not clear")
	}
}

func TestGuildSettingsValidationIsAtomic(t *testing.T) {
	for name, change := range map[string]SettingsChange{
		"empty_mask": {}, "unknown_bit": {Mask: SettingsMask | 1},
		"mode_fallback":            {Mask: SettingsMask, Promotion: "valid", Tags: 3, JoinMode: 2},
		"mode_255":                 {Mask: SettingsJoinMode, JoinMode: 255},
		"tag_four":                 {Mask: SettingsMask, Promotion: "valid", Tags: 15},
		"tag_unknown":              {Mask: SettingsMask, Promotion: "valid", Tags: 128},
		"clear_existing_promotion": {Mask: SettingsMask, Tags: 3, Promotion: ""},
		"blank_promotion":          {Mask: SettingsMask, Tags: 3, Promotion: "　 \r\n"},
		"long_promotion":           {Mask: SettingsMask, Tags: 3, Promotion: strings.Repeat("会", 81)},
		"long_utf16":               {Mask: SettingsMask, Tags: 3, Promotion: strings.Repeat("😀", 41)},
		"promotion_nul":            {Mask: SettingsMask, Tags: 3, Promotion: "a\x00b"},
	} {
		t.Run(name, func(t *testing.T) {
			reg, g, master, _ := textRegistry(t)
			g.Promotion = "old"
			before, _ := json.Marshal(g)
			if _, err := reg.SetSettings(master, change, time.Now()); err == nil {
				t.Fatal("invalid settings accepted")
			}
			after, _ := json.Marshal(g)
			if string(before) != string(after) {
				t.Fatal("invalid batch partly applied")
			}
		})
	}
}

func TestGuildSettingsPromotionBoundaryAndLegacyJSON(t *testing.T) {
	reg, g, master, _ := textRegistry(t)
	if _, err := reg.SetSettings(master, SettingsChange{Mask: SettingsPromotion, WebPromotion: true}, time.Now()); err != nil {
		t.Fatal(err)
	}
	for _, value := range []string{strings.Repeat("会", 80), strings.Repeat("😀", 40), "  公会\r\n介绍  "} {
		if _, err := reg.SetSettings(master, SettingsChange{Mask: SettingsPromotion, Promotion: value, WebPromotion: true}, time.Now()); err != nil {
			t.Fatal(err)
		}
		if g.Promotion != value {
			t.Fatal("promotion was trimmed")
		}
	}
	var legacy Guild
	if err := json.Unmarshal([]byte(`{"alliance_recruitment":true}`), &legacy); err != nil || !legacy.WebPromotion {
		t.Fatalf("legacy key lost: %v", err)
	}
	data, err := json.Marshal(&legacy)
	if err != nil || !strings.Contains(string(data), `"alliance_recruitment":true`) {
		t.Fatal("persisted schema changed")
	}
}

func TestGuildSettingsOwnershipRollbackAndConcurrency(t *testing.T) {
	ctx := context.Background()
	repos := guildTestGroup(t, 2)
	reg, _, master, member := textRegistry(t)
	now := time.Now()
	if _, err := reg.SetSettings(r.GuildOwner{CharacterID: "1", AccountID: "new-owner"}, SettingsChange{Mask: SettingsTags}, now); !errors.Is(err, ErrNotMember) {
		t.Fatal(err)
	}
	if err := Save(ctx, repos.Settings, reg, now); err != nil {
		t.Fatal(err)
	}
	boom := errors.New("settings save failed")
	err := Update(ctx, lateFailGuildTx{repos.Guild, boom}, []r.GuildOwner{master}, now, func(_ r.Group, reg *Registry) error {
		_, err := reg.SetSettings(master, SettingsChange{Mask: SettingsMask, Promotion: "rollback", Tags: 3}, now)
		return err
	})
	if !errors.Is(err, boom) {
		t.Fatal(err)
	}
	loaded, _ := Load(ctx, repos.Settings)
	if loaded.Guilds[1].Promotion != "" || loaded.Guilds[1].JoinMode != 1 {
		t.Fatal("rollback lost")
	}
	var wg sync.WaitGroup
	results := make(chan error, 2)
	for _, owner := range []r.GuildOwner{master, member} {
		wg.Go(func() {
			results <- Update(ctx, repos.Guild, []r.GuildOwner{owner}, now, func(_ r.Group, reg *Registry) error {
				if owner == master {
					_, err := reg.SetSettings(owner, SettingsChange{Mask: SettingsMask, Promotion: "settings", Tags: 5}, now)
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
	if loaded.Guilds[1].Promotion != "settings" || loaded.Guilds[1].Members["2"].Memo != "memo" {
		t.Fatal("concurrent state lost")
	}
}
