package guild

import (
	"context"
	"errors"
	"fmt"
	"os"
	"reflect"
	"sync"
	"testing"
	"time"

	r "longheng.io/server/internal/modules/dnf/repository"
)

const merchantUpgradeCatalogFixture = `
[building upgrade]

[building]
0 0 1000 0 3500 0 8000 0 15000 20 25000 200 35000 700 55000 1500 75000 3000 95000 5000
[/building]

[mecenary]
0 0 1000 0 3500 0 8000 0 15000 20 25000 200 35000 700 55000 1500 75000 3000 95000 5000
[/mecenary]

[/building upgrade]
`

type merchantUpgradeTestSource string

func (s merchantUpgradeTestSource) ReadText(relativePath string) (string, error) {
	if relativePath != EssenceCatalogPath {
		return "", os.ErrNotExist
	}
	return string(s), nil
}

func merchantUpgradeFixture(t *testing.T, repos r.Group, levels [2]uint8) (MerchantUpgradeRequest, *MerchantUpgradeCatalog, time.Time) {
	t.Helper()
	reg, g, owner, _ := textRegistry(t)
	if levels != [2]uint8{0, 0} {
		g.Merchants = &MerchantState{Levels: levels}
	}
	g.Essence = EssenceBalance{AP: 300000, GP: 20000}
	now := time.Date(2026, 10, 25, 8, 0, 0, 0, time.UTC)
	if err := Save(context.Background(), repos.Settings, reg, now); err != nil {
		t.Fatal(err)
	}
	cat, err := LoadMerchantUpgradeCatalog(merchantUpgradeTestSource(merchantUpgradeCatalogFixture))
	if err != nil {
		t.Fatal(err)
	}
	q := MerchantUpgradeRequest{
		Owner: owner, GuildID: g.ID, RequestID: fmt.Sprintf("%064x", 1),
		NPCID: 390, CurrentLevel: 1, TargetLevel: 2,
		Revalidate:  func() bool { return true },
		CanInteract: func(r.CharacterRecord, *Guild, bool) bool { return true },
	}
	return q, cat, now
}

func TestMerchantUpgradeCatalogReadsRealShapeAndCostRows(t *testing.T) {
	cat, err := LoadMerchantUpgradeCatalog(merchantUpgradeTestSource(merchantUpgradeCatalogFixture))
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		index, target uint8
		want          EssenceBalance
	}{
		{0, 2, EssenceBalance{AP: 1000, GP: 0}},
		{0, 10, EssenceBalance{AP: 95000, GP: 5000}},
		{1, 2, EssenceBalance{AP: 1000, GP: 0}},
		{1, 10, EssenceBalance{AP: 95000, GP: 5000}},
	} {
		got, err := cat.Cost(tc.index, tc.target)
		if err != nil || got != tc.want {
			t.Fatalf("index=%d target=%d cost=%+v err=%v", tc.index, tc.target, got, err)
		}
	}
	for _, tc := range []struct {
		index, target uint8
	}{{0, 1}, {0, 11}, {2, 2}} {
		if _, err := cat.Cost(tc.index, tc.target); !errors.Is(err, ErrMerchantCatalog) {
			t.Fatalf("accepted cost index=%d target=%d: %v", tc.index, tc.target, err)
		}
	}
	if _, err := LoadMerchantUpgradeCatalog(nil); !errors.Is(err, ErrRepositoryUnavailable) {
		t.Fatal(err)
	}
}

func TestMerchantUpgradeCatalogRealExtractedPVF(t *testing.T) {
	path := os.Getenv("GUILD_MERCHANT_UPGRADE_EXTRACTED_ETC")
	if path == "" {
		t.Skip("set GUILD_MERCHANT_UPGRADE_EXTRACTED_ETC to the extracted agitwar.etc")
	}
	text, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		t.Skip("extracted agitwar.etc is not present")
	}
	if err != nil {
		t.Fatal(err)
	}
	cat, err := ParseMerchantUpgradeCatalog(string(text))
	if err != nil {
		t.Fatal(err)
	}
	for index := uint8(0); index < 2; index++ {
		if cost, err := cat.Cost(index, 2); err != nil || cost != (EssenceBalance{AP: 1000}) {
			t.Fatalf("index=%d first cost=%+v err=%v", index, cost, err)
		}
		if cost, err := cat.Cost(index, 10); err != nil || cost != (EssenceBalance{AP: 95000, GP: 5000}) {
			t.Fatalf("index=%d last cost=%+v err=%v", index, cost, err)
		}
	}
}

func TestMerchantUpgradeCatalogRejectsMalformedTables(t *testing.T) {
	row := "0 0 1000 0 3500 0 8000 0 15000 20 25000 200 35000 700 55000 1500 75000 3000 95000 5000"
	for name, text := range map[string]string{
		"missing_table":     "",
		"missing_mercenary": "[building upgrade]\n[building]\n" + row + "\n[/building]\n[/building upgrade]\n",
		"short_row":         "[building upgrade]\n[building]\n0 0\n[/building]\n[mecenary]\n" + row + "\n[/mecenary]\n[/building upgrade]\n",
		"negative":          "[building upgrade]\n[building]\n" + "-1 " + row[2:] + "\n[/building]\n[mecenary]\n" + row + "\n[/mecenary]\n[/building upgrade]\n",
		"duplicate":         "[building upgrade]\n[building]\n" + row + "\n[/building]\n[building]\n" + row + "\n[/building]\n[mecenary]\n" + row + "\n[/mecenary]\n[/building upgrade]\n",
		"unknown_nested":    "[building upgrade]\n[other]\n1\n[/other]\n[building]\n" + row + "\n[/building]\n[mecenary]\n" + row + "\n[/mecenary]\n[/building upgrade]\n",
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := ParseMerchantUpgradeCatalog(text); !errors.Is(err, ErrMerchantCatalog) {
				t.Fatalf("accepted malformed table: %v", err)
			}
		})
	}
}

func TestMerchantLevelDefaultsPersistsAndValidates(t *testing.T) {
	repos := guildTestGroup(t, 2)
	reg, g, _, _ := textRegistry(t)
	for index := uint8(0); index < 2; index++ {
		if level, err := MerchantLevel(g, index); err != nil || level != 1 {
			t.Fatalf("default index=%d level=%d err=%v", index, level, err)
		}
	}
	if _, err := MerchantLevel(g, 2); !errors.Is(err, ErrMerchantState) {
		t.Fatal(err)
	}
	if _, err := MerchantLevel(nil, 0); !errors.Is(err, ErrMerchantState) {
		t.Fatal(err)
	}
	for _, state := range []*MerchantState{{Levels: [2]uint8{0, 1}}, {Levels: [2]uint8{1, 11}}} {
		g.Merchants = state
		if err := Save(context.Background(), repos.Settings, reg, time.Now()); !errors.Is(err, ErrMerchantState) {
			t.Fatalf("accepted state %+v: %v", state, err)
		}
	}
	g.Merchants = &MerchantState{Levels: [2]uint8{7, 9}}
	now := time.Now()
	if err := Save(context.Background(), repos.Settings, reg, now); err != nil {
		t.Fatal(err)
	}
	loaded, err := Load(context.Background(), repos.Settings)
	if err != nil || !reflect.DeepEqual(loaded.Guilds[g.ID].Merchants, g.Merchants) {
		t.Fatalf("persisted state=%+v err=%v", loaded.Guilds[g.ID].Merchants, err)
	}
}

func TestMerchantUpgradeCommitsRealCostAndLevelForBothNPCs(t *testing.T) {
	for _, tc := range []struct {
		name     string
		npc      uint32
		start    [2]uint8
		from, to uint32
		index    uint8
		want     EssenceBalance
	}{
		{"defence_first", 390, [2]uint8{0, 0}, 1, 2, 0, EssenceBalance{AP: 1000}},
		{"mercenary_first", 389, [2]uint8{0, 0}, 1, 2, 1, EssenceBalance{AP: 1000}},
		{"defence_last", 390, [2]uint8{9, 9}, 9, 10, 0, EssenceBalance{AP: 95000, GP: 5000}},
		{"mercenary_last", 389, [2]uint8{9, 9}, 9, 10, 1, EssenceBalance{AP: 95000, GP: 5000}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			repos := guildTestGroup(t, 2)
			q, cat, now := merchantUpgradeFixture(t, repos, tc.start)
			q.NPCID, q.CurrentLevel, q.TargetLevel = tc.npc, tc.from, tc.to
			got, err := ApplyMerchantUpgrade(context.Background(), repos.Guild, q, cat, now)
			if err != nil {
				t.Fatal(err)
			}
			wantAfter := EssenceBalance{AP: 300000 - tc.want.AP, GP: 20000 - tc.want.GP}
			if got.Owner != q.Owner || got.GuildID != q.GuildID || got.NPCID != tc.npc ||
				got.MerchantIndex != tc.index || got.FromLevel != uint8(tc.from) ||
				got.ToLevel != uint8(tc.to) || got.Cost != tc.want ||
				got.BalanceAfter != wantAfter || !got.At.Equal(now) {
				t.Fatalf("receipt=%+v", got)
			}
			reg, err := Load(context.Background(), repos.Settings)
			if err != nil {
				t.Fatal(err)
			}
			level, err := MerchantLevel(reg.Guilds[q.GuildID], tc.index)
			if err != nil || uint32(level) != tc.to || reg.Guilds[q.GuildID].Essence != wantAfter ||
				reg.MerchantUpgradeReceipts[merchantUpgradeKeyPrefix+q.RequestID] != got {
				t.Fatalf("persisted level=%d guild=%+v err=%v", level, reg.Guilds[q.GuildID], err)
			}
		})
	}
}

func TestMerchantUpgradeReplayConflictAndStaleLevel(t *testing.T) {
	repos := guildTestGroup(t, 2)
	q, cat, now := merchantUpgradeFixture(t, repos, [2]uint8{0, 0})
	ctx := context.Background()
	first, err := ApplyMerchantUpgrade(ctx, repos.Guild, q, cat, now)
	if err != nil {
		t.Fatal(err)
	}
	callbackReplays := 0
	q.CanInteract = func(_ r.CharacterRecord, _ *Guild, replay bool) bool {
		if replay {
			callbackReplays++
		}
		return true
	}
	replayed, err := ApplyMerchantUpgrade(ctx, repos.Guild, q, cat, now.Add(time.Hour))
	if err != nil || !reflect.DeepEqual(replayed, first) || callbackReplays != 1 {
		t.Fatalf("replay=%+v callback=%d err=%v", replayed, callbackReplays, err)
	}
	q.NPCID = 389
	if _, err = ApplyMerchantUpgrade(ctx, repos.Guild, q, cat, now); !errors.Is(err, ErrPurchaseIdentity) {
		t.Fatalf("same identity with changed body: %v", err)
	}
	q.NPCID = 390
	q.RequestID = fmt.Sprintf("%064x", 2)
	if _, err = ApplyMerchantUpgrade(ctx, repos.Guild, q, cat, now); !errors.Is(err, ErrMerchantStaleLevel) {
		t.Fatalf("new identity with old level: %v", err)
	}
	q.CurrentLevel, q.TargetLevel = 2, 3
	second, err := ApplyMerchantUpgrade(ctx, repos.Guild, q, cat, now.Add(2*time.Hour))
	if err != nil || second.FromLevel != 2 || second.ToLevel != 3 || second.Cost != (EssenceBalance{AP: 3500}) {
		t.Fatalf("second upgrade=%+v err=%v", second, err)
	}
	q.RequestID = firstOwnerRequestID()
	old, err := ApplyMerchantUpgrade(ctx, repos.Guild, MerchantUpgradeRequest{
		Owner: q.Owner, GuildID: q.GuildID, RequestID: q.RequestID, NPCID: 390,
		CurrentLevel: 1, TargetLevel: 2, Revalidate: q.Revalidate, CanInteract: q.CanInteract,
	}, cat, now.Add(3*time.Hour))
	if err != nil || !reflect.DeepEqual(old, first) {
		t.Fatalf("original receipt after later upgrade=%+v err=%v", old, err)
	}
	reg, err := Load(ctx, repos.Settings)
	if err != nil || len(reg.MerchantUpgradeReceipts) != 2 || reg.Guilds[q.GuildID].Essence != (EssenceBalance{AP: 295500, GP: 20000}) {
		t.Fatalf("registry=%+v err=%v", reg, err)
	}
}

func firstOwnerRequestID() string {
	return fmt.Sprintf("%064x", 1)
}

func TestMerchantUpgradeRejectsWithoutChangingAssets(t *testing.T) {
	for _, mode := range []string{
		"max", "cross", "stale", "ap", "gp", "permission_purchase", "permission_upgrade",
		"nonmember", "other_guild", "fake_npc", "scene", "session_before",
		"session_before_save", "session_after_save", "save_failure", "late_failure",
		"request_id", "zero_time", "nil_uow", "nil_catalog", "nil_revalidate", "nil_interact",
	} {
		t.Run(mode, func(t *testing.T) {
			start := [2]uint8{0, 0}
			if mode == "max" {
				start = [2]uint8{10, 1}
			}
			if mode == "stale" {
				start = [2]uint8{2, 1}
			}
			repos := guildTestGroup(t, 3)
			q, cat, now := merchantUpgradeFixture(t, repos, start)
			ctx := context.Background()
			reg, err := Load(ctx, repos.Settings)
			if err != nil {
				t.Fatal(err)
			}
			g := reg.Guilds[q.GuildID]
			uow := repos.Guild
			switch mode {
			case "max":
				q.CurrentLevel, q.TargetLevel = 10, 11
			case "cross":
				q.TargetLevel = 3
			case "ap":
				g.Essence.AP = 999
			case "gp":
				if g.Merchants == nil {
					g.Merchants = &MerchantState{Levels: [2]uint8{1, 1}}
				}
				g.Merchants.Levels[0] = 9
				g.Essence.AP, g.Essence.GP = 300000, 4999
				q.CurrentLevel, q.TargetLevel = 9, 10
			case "permission_purchase", "permission_upgrade":
				q.Owner = r.GuildOwner{CharacterID: "2", AccountID: "a2"}
				g.GradePermissions = map[byte]uint32{4: MerchantUpgradePermission}
				if mode == "permission_upgrade" {
					g.GradePermissions[4] = PurchasePermission
				}
			case "nonmember":
				q.Owner = r.GuildOwner{CharacterID: "3", AccountID: "a3"}
			case "other_guild":
				q.GuildID = 2
			case "fake_npc":
				q.NPCID = 388
			case "scene":
				q.CanInteract = func(r.CharacterRecord, *Guild, bool) bool { return false }
			case "session_before", "session_before_save", "session_after_save":
				calls := 0
				limit := map[string]int{"session_before": 1, "session_before_save": 2, "session_after_save": 3}[mode]
				q.Revalidate = func() bool { calls++; return calls < limit }
			case "save_failure":
				uow = cargoInitializationFailUOW{uow}
			case "late_failure":
				uow = lateFailGuildTx{base: uow, fail: errors.New("late failure")}
			case "request_id":
				q.RequestID = "bad"
			case "zero_time":
				now = time.Time{}
			case "nil_uow":
				uow = nil
			case "nil_catalog":
				cat = nil
			case "nil_revalidate":
				q.Revalidate = nil
			case "nil_interact":
				q.CanInteract = nil
			}
			if err := Save(ctx, repos.Settings, reg, time.Unix(1700000000, 0)); err != nil {
				t.Fatal(err)
			}
			before, _, _ := repos.Settings.Load(ctx, RegistryScope)
			got, err := ApplyMerchantUpgrade(ctx, uow, q, cat, now)
			if err == nil || got.GuildID != 0 {
				t.Fatalf("accepted %s: %+v %v", mode, got, err)
			}
			after, _, _ := repos.Settings.Load(ctx, RegistryScope)
			if !reflect.DeepEqual(before, after) {
				t.Fatalf("%s failed request persisted", mode)
			}
		})
	}
}

func TestMerchantUpgradeConcurrentReceiptsAndFinalBalance(t *testing.T) {
	for _, mode := range []string{"same_receipt", "same_merchant", "both_merchants"} {
		t.Run(mode, func(t *testing.T) {
			repos := guildTestGroup(t, 2)
			q, cat, now := merchantUpgradeFixture(t, repos, [2]uint8{0, 0})
			ctx := context.Background()
			var wg sync.WaitGroup
			results := make(chan error, 16)
			for i := 0; i < 16; i++ {
				wg.Add(1)
				go func(i int) {
					defer wg.Done()
					request := q
					if mode != "same_receipt" {
						request.RequestID = fmt.Sprintf("%064x", i+1)
					}
					if mode == "both_merchants" {
						request.NPCID = []uint32{390, 389}[i%2]
					}
					_, err := ApplyMerchantUpgrade(ctx, repos.Guild, request, cat, now)
					results <- err
				}(i)
			}
			wg.Wait()
			close(results)
			success := 0
			for err := range results {
				if err == nil {
					success++
				} else if !errors.Is(err, ErrMerchantStaleLevel) {
					t.Fatalf("concurrent err=%v", err)
				}
			}
			wantSuccess := 1
			wantReceipts := 1
			wantBalance := EssenceBalance{AP: 299000, GP: 20000}
			if mode == "same_receipt" {
				wantSuccess = 16
			}
			if mode == "both_merchants" {
				wantSuccess, wantReceipts = 2, 2
				wantBalance = EssenceBalance{AP: 298000, GP: 20000}
			}
			reg, err := Load(ctx, repos.Settings)
			if err != nil || success != wantSuccess || len(reg.MerchantUpgradeReceipts) != wantReceipts ||
				reg.Guilds[q.GuildID].Essence != wantBalance ||
				reg.Guilds[q.GuildID].Merchants.Levels[0] != 2 {
				t.Fatalf("success=%d registry=%+v err=%v", success, reg, err)
			}
			if mode == "both_merchants" && reg.Guilds[q.GuildID].Merchants.Levels[1] != 2 {
				t.Fatalf("mercenary level=%d", reg.Guilds[q.GuildID].Merchants.Levels[1])
			}
		})
	}
}

func TestMerchantUpgradeReceiptSurvivesLeaveAndDisband(t *testing.T) {
	repos := guildTestGroup(t, 2)
	q, cat, now := merchantUpgradeFixture(t, repos, [2]uint8{0, 0})
	ctx := context.Background()
	receipt, err := ApplyMerchantUpgrade(ctx, repos.Guild, q, cat, now)
	if err != nil {
		t.Fatal(err)
	}
	reg, err := Load(ctx, repos.Settings)
	if err != nil {
		t.Fatal(err)
	}
	member := r.GuildOwner{CharacterID: "2", AccountID: "a2"}
	if _, err = reg.DepartMember(q.Owner, member, now.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if err = Save(ctx, repos.Settings, reg, now.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	reg, err = Load(ctx, repos.Settings)
	if err != nil || len(reg.MerchantUpgradeReceipts) != 1 {
		t.Fatalf("after leave receipts=%d err=%v", len(reg.MerchantUpgradeReceipts), err)
	}
	if _, err = reg.Disband(q.Owner, now.Add(2*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if err = Save(ctx, repos.Settings, reg, now.Add(2*time.Hour)); err != nil {
		t.Fatal(err)
	}
	reg, err = Load(ctx, repos.Settings)
	if err != nil || len(reg.Guilds) != 0 || len(reg.MerchantUpgradeReceipts) != 1 ||
		reg.MerchantUpgradeReceipts[merchantUpgradeKeyPrefix+q.RequestID] != receipt {
		t.Fatalf("after disband registry=%+v err=%v", reg, err)
	}
}
