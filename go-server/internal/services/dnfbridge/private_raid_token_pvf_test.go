package dnfbridge

import (
	"context"
	"encoding/json"
	"os"
	"reflect"
	"testing"
	"time"

	"longheng.io/server/internal/modules/dnf/cerashop"
	"longheng.io/server/internal/modules/dnf/raid"
	repo "longheng.io/server/internal/modules/dnf/repository"
	"longheng.io/server/internal/modules/dnf/repository/memory"
	pvf "longheng.io/server/internal/platform/pvf"
)

func privateRaidTokenCatalog(t *testing.T) (*pvf.Archive, *pvfDungeonDropCatalog) {
	t.Helper()
	file := os.Getenv("DNFBRIDGE_RAID_TOKEN_PVF")
	if file == "" {
		t.Skip("explicit private candidate PVF required")
	}
	a, err := pvf.Open(file)
	if err != nil {
		t.Fatal(err)
	}
	c, err := newPVFDungeonDropCatalog(a)
	if err != nil {
		t.Fatal(err)
	}
	return a, c
}

func TestPrivateRaidTokenPVFProbabilityAndIsolation(t *testing.T) {
	a, catalog := privateRaidTokenCatalog(t)
	oldPath := os.Getenv("DNFBRIDGE_RAID_TOKEN_BASELINE")
	if oldPath == "" {
		t.Fatal("baseline PVF required")
	}
	oldArchive, err := pvf.Open(oldPath)
	if err != nil {
		t.Fatal(err)
	}
	oldCatalog, err := newPVFDungeonDropCatalog(oldArchive)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	template, err := parseDungeonCardPVFDocument(a, "stackable/490006001/chn_490006075.stk")
	if err != nil {
		t.Fatal(err)
	}
	for i, amount := range []uint32{2000, 4000, 5000} {
		id := uint32(990027101 + i)
		def, err := catalog.ResolveItem(id)
		if err != nil {
			t.Fatal(err)
		}
		doc, err := parseDungeonCardPVFDocument(a, def.PVFPath)
		if err != nil {
			t.Fatal(err)
		}
		if magicBoxDocumentText(doc, "icon") != magicBoxDocumentText(template, "icon") || !reflect.DeepEqual(doc.Ints("icon"), template.Ints("icon")) {
			t.Fatal("icon differs from native token box", id)
		}
		if !def.ExpirationDate.IsZero() || def.UsablePeriodDays != 0 || magicBoxDocumentText(doc, "attach type") != "[account]" {
			t.Fatal("unexpected box lifetime or binding", id)
		}
		box, err := resolveCurrentBoosterDefinition(catalog, id, currentBoosterRequestRandom)
		if err != nil {
			t.Fatal(err)
		}
		contents, _, err := resolveCurrentBoosterRewardAmounts(box, currentBoosterOpenRequest{Kind: currentBoosterRequestRandom}, func(int64) (int64, error) { return 0, nil })
		if err != nil || !reflect.DeepEqual(contents, map[uint32]uint32{2681917: amount}) {
			t.Fatalf("id=%d contents=%v err=%v", id, contents, err)
		}
		if box.MaterialCount != 0 || box.MaterialItemID != 0 || box.LotteryGoldCost != 0 {
			t.Fatal("paid box", id)
		}
	}
	loaders := []func(*pvfDungeonDropCatalog) (raid.AntonRewardConfiguration, error){
		func(c *pvfDungeonDropCatalog) (raid.AntonRewardConfiguration, error) {
			return raid.LoadAntonRewardConfiguration(c.source)
		},
		func(c *pvfDungeonDropCatalog) (raid.AntonRewardConfiguration, error) {
			return raid.LoadLukeRewardConfiguration(c.source)
		},
		func(c *pvfDungeonDropCatalog) (raid.AntonRewardConfiguration, error) {
			return raid.LoadFiendWarRewardConfiguration(c.source)
		},
	}
	for mode, load := range loaders {
		before, err := load(oldCatalog)
		if err != nil {
			t.Fatal(err)
		}
		after, err := load(catalog)
		if err != nil {
			t.Fatal(err)
		}
		if err := validateCurrentAntonRewardDependencies(catalog, after, now); err != nil {
			t.Fatal(err)
		}
		last := len(after.Phases) - 1
		for phase := 0; phase < last; phase++ {
			if !reflect.DeepEqual(before.Phases[phase], after.Phases[phase]) {
				t.Fatal("earlier phase changed", mode, phase)
			}
		}
		b, p := before.Phases[last], after.Phases[last]
		if b.MinimumClears != p.MinimumClears || !reflect.DeepEqual(b.Ranks, p.Ranks) {
			t.Fatal("entry/rank criteria changed")
		}
		for rank := byte(0); rank < 4; rank++ {
			for _, kind := range []string{"party_card", "pcroom_card", "fw_party_card"} {
				if kind == "fw_party_card" && mode != 2 {
					continue
				}
				x, tx, ex := b.Pool(kind, rank)
				y, ty, ey := p.Pool(kind, rank)
				if ex != nil || ey != nil || tx != ty || !reflect.DeepEqual(x, y) {
					t.Fatal("non-team pool changed", mode, kind, rank)
				}
			}
			old, totalOld, err := b.Pool("squad_item", rank)
			if err != nil {
				t.Fatal(err)
			}
			rows, total, err := p.Pool("squad_item", rank)
			if err != nil {
				t.Fatal(err)
			}
			if len(rows) != len(old)+1 || total != totalOld*10 {
				t.Fatal("pool shape", mode, rank)
			}
			for n, row := range old {
				want := row
				want.Weight *= 9
				if rows[n] != want {
					t.Fatal("old reward proportions changed", mode, rank, n)
				}
			}
			newRow := rows[len(rows)-1]
			if newRow.ContainerID != uint32(990027101+mode) || newRow.Flags != 1 || newRow.Weight != totalOld {
				t.Fatal("not 10% gold reward", newRow)
			}
			var hits uint32
			for draw := uint32(0); draw < total; draw++ {
				choice, e := p.Select("squad_item", rank, draw)
				if e != nil {
					t.Fatal(e)
				}
				if choice.ContainerID == newRow.ContainerID {
					hits++
				}
			}
			if uint64(hits)*10 != uint64(total) {
				t.Fatal("exhaustive probability mismatch", hits, total)
			}
			t.Logf("mode=%d rank=%d gold-token outcomes=%d/%d exactly 10%%", mode+1, rank, hits, total)
		}
	}
	for _, path := range []string{"stackable/stackable.lst", "etc/raid/anton.etc", "etc/raid/luke/luke.etc", "etc/raid/fiendwar/fiendwar.etc"} {
		if a.FindFileIndex(path) < 0 {
			t.Fatal("missing configured path", path)
		}
	}
}

// This probe reports the current server's actual inventory AND wallet effects.
// It does not treat an inventory voucher as spendable account currency.
func TestPrivateRaidTokenPVFActualGrantProbe(t *testing.T) {
	_, catalog := privateRaidTokenCatalog(t)
	out := os.Getenv("DNFBRIDGE_RAID_TOKEN_PROBE_OUT")
	if out == "" {
		t.Fatal("probe output path required")
	}
	ctx := context.Background()
	now := time.Now().UTC()
	results := []map[string]any{}
	for mode, amount := range []uint32{2000, 4000, 5000} {
		id := uint32(990027101 + mode)
		choice := raid.AntonRewardChoice{Type: "squad_item", Rank: -1, Weight: 100, ContainerID: id, Flags: 1}
		plan, err := resolveCurrentAntonConfiguredReward(catalog, choice, func(int64) (int64, error) { return 0, nil }, now)
		if err != nil {
			t.Fatal(err)
		}
		if len(plan.Rewards) != 1 || plan.Rewards[0].Definition.ItemID != 2681917 || plan.Rewards[0].Count != amount {
			t.Fatal("box not expanded", plan.Rewards)
		}
		g := memory.NewMemoryGroup()
		if err = g.Account.Save(ctx, repo.AccountRecord{AccountID: "token-fixture"}); err != nil {
			t.Fatal(err)
		}
		if err = g.Character.Save(ctx, repo.CharacterRecord{CharacterID: "31", AccountID: "token-fixture"}); err != nil {
			t.Fatal(err)
		}
		if err = g.Inventory.Save(ctx, repo.InventoryRecord{CharacterID: "31", Slots: map[string]repo.ItemStack{"0:50": {ItemID: 10096296, Count: 2}}}); err != nil {
			t.Fatal(err)
		}
		cmd := raid.RewardCommand{AccountID: "token-fixture", CharacterID: "31", InstanceID: "00112233445566778899aabbccddeeff", Mode: byte(mode + 1), Phase: 1, Kind: 3, Card: 0, MinimumClears: 1, OwnedClears: 6, Fingerprint: plan.Fingerprint, CompletedAt: now}
		if mode == 2 {
			cmd.Phase = 0 // FiendWar has one phase; use its real receipt identity.
		}
		for i := 0; i < 2; i++ {
			got, e := grantCurrentAntonRewardPlan(ctx, g, catalog, plan, cmd)
			if e != nil {
				t.Fatal(e)
			}
			if got.Replayed != (i == 1) || got.WalletChanged != (i == 0) {
				t.Fatal("receipt replay mismatch")
			}
		}
		inventory, _, err := g.Inventory.Load(ctx, "31")
		if err != nil {
			t.Fatal(err)
		}
		account, _, err := g.Account.Load(ctx, "token-fixture")
		if err != nil {
			t.Fatal(err)
		}
		var currencyItems, outerBoxes int64
		for _, stack := range inventory.Slots {
			if stack.ItemID == 2681917 {
				currencyItems += stack.Count
			}
			if stack.ItemID == int64(id) {
				outerBoxes += stack.Count
			}
		}
		if currencyItems != 0 || outerBoxes != 0 {
			t.Fatal("actual inventory mismatch", currencyItems, outerBoxes)
		}
		wallet := cerashop.WalletBalance(account)
		if wallet.HappyTokenCera != int64(amount) || wallet.TokenCera != 0 {
			t.Fatal("wallet credit mismatch", wallet)
		}
		results = append(results, map[string]any{"mode": mode + 1, "source_box": id, "display_gold": choice.Flags == 1, "reward_item": 2681917, "quantity": amount, "inventory_currency_items": currencyItems, "outer_boxes": outerBoxes, "happy_token_wallet": wallet.HappyTokenCera, "spendable_wallet_credited": wallet.HappyTokenCera == int64(amount), "duplicate_grant_prevented": true})
		t.Logf("mode=%d happy-token inventory=%d wallet=%d outer-boxes=%d", mode+1, currencyItems, wallet.HappyTokenCera, outerBoxes)
	}
	raw, err := json.MarshalIndent(map[string]any{"database": "memory only", "production_server_logic_changed": true, "results": results}, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(out, raw, 0644); err != nil {
		t.Fatal(err)
	}
}
