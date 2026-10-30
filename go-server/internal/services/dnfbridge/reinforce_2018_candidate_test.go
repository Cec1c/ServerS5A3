package dnfbridge

import (
	"context"
	"encoding/binary"
	"fmt"
	"os"
	"testing"

	"longheng.io/server/internal/modules/dnf/alignedcmd"
	"longheng.io/server/internal/modules/dnf/dnfenum"
	"longheng.io/server/internal/modules/dnf/inventory"
	repo "longheng.io/server/internal/modules/dnf/repository"
	memory "longheng.io/server/internal/modules/dnf/repository/memory"
	platformpvf "longheng.io/server/internal/platform/pvf"
)

func Test2018ReinforceRealPVFPolicyAndSettlement(t *testing.T) {
	path := os.Getenv("DNFBRIDGE_REAL_PVF_SMOKE")
	if path == "" {
		t.Skip("current PVF required")
	}
	a, err := platformpvf.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	catalog, err := newPVFDungeonDropCatalog(a)
	if err != nil {
		t.Fatal(err)
	}
	s := &Service{initialEquipmentArchive: a, pvfItemCatalog: catalog}
	policy := s.alignedUpgradePolicyResolver()
	protect, err := s.alignedUpgradeProtectTicketResolverForCommand(dnfenum.CmdPacketUpgradeItem)
	if err != nil {
		t.Fatal(err)
	}

	// Real PVF: weapon, armor and earring. Verify that the same table keeps
	// its probability and material, but only ordinary nonweapons change the
	// +10/+11 failure penalty. Amplification must remain untouched.
	for _, id := range []int64{28309, 100050667, 100390003} {
		for _, level := range []int{8, 9, 10, 11, 12} {
			p, err := policy("reinforce", id, level)
			if err != nil {
				t.Fatalf("item %d +%d: %v", id, level, err)
			}
			wantKind, wantValue := 1, 0
			if level == 10 || level == 11 {
				wantKind, wantValue = 2, 3
			}
			if level >= 12 || (id != 28309 && level >= 10) {
				wantKind, wantValue = 3, 0
			}
			wantWeight := map[int]int{8: 40000, 9: 30000, 10: 25000, 11: 15000, 12: 14000}[level]
			if p.PenaltyType != wantKind || p.PenaltyValue != wantValue || p.SuccessWeight != wantWeight || p.MaterialItemID != 3037 {
				t.Fatalf("item %d +%d: %+v", id, level, p)
			}
		}
		for _, level := range []int{7, 8, 9, 10} {
			p, err := policy("amplify", id, level)
			if err != nil {
				t.Fatal(err)
			}
			wantKind, wantValue := 2, level
			if level == 10 {
				wantKind, wantValue = 3, 0
			}
			if p.PenaltyType != wantKind || p.PenaltyValue != wantValue {
				t.Fatalf("amplify item %d +%d: %+v", id, level, p)
			}
		}
	}

	for _, tc := range []struct {
		name      string
		id        int64
		level     byte
		ticket    bool
		wantCode  byte
		wantLevel int
	}{
		{"armor9_no_drop", 100050667, 9, false, 1, 9},
		{"armor10_destroy", 100050667, 10, false, 3, -1},
		{"armor11_protected", 100050667, 11, true, 2, 0},
		{"earring10_protected", 100390003, 10, true, 2, 0},
		{"weapon10_drop_three_keep_ticket", 28309, 10, true, 2, 7},
		{"weapon11_drop_three", 28309, 11, false, 2, 8},
		{"weapon12_destroy", 28309, 12, false, 3, -1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			repos := memory.NewMemoryGroup()
			if err := repos.Character.Save(ctx, repo.CharacterRecord{CharacterID: "77", AccountID: "offline", Stats: map[string]int64{"gold": 1000}}); err != nil {
				t.Fatal(err)
			}
			target := realForgingWeaponStack(tc.id, 45)
			target.RawEntry[0x0a] = tc.level
			slots := map[string]repo.ItemStack{"0:9": target}
			if tc.ticket {
				slots["0:121"] = repo.ItemStack{ItemID: 10093371, Count: 2}
			}
			if err := repos.Inventory.Save(ctx, repo.InventoryRecord{CharacterID: "77", Slots: slots}); err != nil {
				t.Fatal(err)
			}
			key := repo.AccountSharedInventorySlotKey(358)
			if err := repos.AccountInventory.Save(ctx, repo.AccountInventoryRecord{AccountID: "offline", Slots: map[string]repo.ItemStack{key: {ItemID: 3037, Count: 1000}}}); err != nil {
				t.Fatal(err)
			}
			body := make([]byte, 16)
			binary.LittleEndian.PutUint16(body[2:], 9)
			binary.LittleEndian.PutUint32(body[4:], uint32(tc.id))
			binary.LittleEndian.PutUint16(body[8:], 358)
			binary.LittleEndian.PutUint16(body[10:], 65535)
			result, err := inventory.NewHandler().Handle(ctx, alignedcmd.Request{Opcode: uint16(dnfenum.CmdPacketUpgradeItem), Body: body, AccountID: "offline", SelectedCharacterID: 77, Repositories: repos,
				UpgradePolicyResolver: func(mode string, id int64, level int) (alignedcmd.UpgradePolicyResolution, error) {
					p, e := policy(mode, id, level)
					p.SuccessWeight = 0
					return p, e
				},
				UpgradeProtectTicketResolver: protect,
				UpgradeCostResolver: func(string, int64, int, bool, uint16) (alignedcmd.UpgradeCostResolution, error) {
					return alignedcmd.UpgradeCostResolution{Resolved: true, GoldCost: 100}, nil
				},
			})
			if err != nil || !result.Handled || !result.ResponseAllowed || len(result.UpperResponses) < 2 {
				t.Fatalf("handler error=%v result=%+v", err, result)
			}
			ack := result.UpperResponses[0]
			if len(ack.Body) < 14 || ack.Body[0] != 1 || ack.Body[12] != tc.wantCode {
				t.Fatalf("unexpected failure outcome: %v", ack.Body)
			}
			bag, ok, err := repos.Inventory.Load(ctx, "77")
			if err != nil || !ok {
				t.Fatal(err)
			}
			stored, found := bag.Slots["0:9"]
			if tc.wantLevel < 0 {
				if found {
					t.Fatal("target should be destroyed")
				}
			} else if !found || int(stored.RawEntry[0x0a]&31) != tc.wantLevel {
				t.Fatalf("target level/found mismatch: %+v %v", stored, found)
			}
			if tc.ticket {
				want := int64(2)
				if tc.name == "armor11_protected" || tc.name == "earring10_protected" {
					want = 1
				}
				if bag.Slots["0:121"].Count != want {
					t.Fatalf("ticket count %d want %d", bag.Slots["0:121"].Count, want)
				}
			}
			p, err := policy("reinforce", tc.id, int(tc.level))
			if err != nil {
				t.Fatal(err)
			}
			shared, _, err := repos.AccountInventory.Load(ctx, "offline")
			if err != nil {
				t.Fatal(err)
			}
			if got := shared.Slots[key].Count; got != 1000-int64(p.MaterialCount) {
				t.Fatalf("material count %d, expected %d", got, 1000-p.MaterialCount)
			}
			character, _, err := repos.Character.Load(ctx, "77")
			if err != nil || character.Stats["gold"] != 900 {
				t.Fatalf("gold transaction: %v", err)
			}
			if result.UpgradeProtectTicket != nil && result.UpgradeProtectTicket.Consumed != (tc.ticket && tc.wantLevel == 0) {
				t.Fatal("protection result mismatch")
			}
		})
	}
	t.Run("portable_reinforcement_uses_protection_and_no_gold", func(t *testing.T) {
		ctx := context.Background()
		repos := memory.NewMemoryGroup()
		if err := repos.Character.Save(ctx, repo.CharacterRecord{CharacterID: "77", AccountID: "offline", Stats: map[string]int64{"gold": 1000}}); err != nil {
			t.Fatal(err)
		}
		target := realForgingWeaponStack(100050667, 45)
		target.RawEntry[0x0a] = 10
		if err := repos.Inventory.Save(ctx, repo.InventoryRecord{CharacterID: "77", Slots: map[string]repo.ItemStack{
			"0:9": target, "0:121": {ItemID: 10093371, Count: 2}, "0:107": {ItemID: 490006651, Count: 3},
		}}); err != nil {
			t.Fatal(err)
		}
		ticketResolver, err := s.alignedUpgradeTicketResolverForCommand(dnfenum.CmdPacketUpgradeItem)
		if err != nil {
			t.Fatal(err)
		}
		body := make([]byte, 16)
		binary.LittleEndian.PutUint16(body[2:], 9)
		binary.LittleEndian.PutUint32(body[4:], 100050667)
		binary.LittleEndian.PutUint16(body[8:], 107)
		binary.LittleEndian.PutUint16(body[10:], 65535)
		result, err := inventory.NewHandler().Handle(ctx, alignedcmd.Request{Opcode: uint16(dnfenum.CmdPacketUpgradeItem), Body: body, AccountID: "offline", SelectedCharacterID: 77, Repositories: repos,
			UpgradePolicyResolver: func(mode string, id int64, level int) (alignedcmd.UpgradePolicyResolution, error) {
				p, e := policy(mode, id, level)
				p.SuccessWeight = 0
				return p, e
			},
			UpgradeProtectTicketResolver: protect, UpgradeTicketResolver: ticketResolver,
		})
		if err != nil || len(result.UpperResponses) == 0 {
			t.Fatalf("handler %v", err)
		}
		ack := result.UpperResponses[0]
		if len(ack.Body) < 14 || ack.Body[0] != 1 || ack.Body[12] != 2 || ack.Body[13] != 0 {
			t.Fatalf("portable failure response %v", ack.Body)
		}
		bag, _, err := repos.Inventory.Load(ctx, "77")
		if err != nil {
			t.Fatal(err)
		}
		if bag.Slots["0:107"].Count != 2 || bag.Slots["0:121"].Count != 1 || bag.Slots["0:9"].RawEntry[0x0a]&31 != 0 {
			t.Fatal("portable item, protection or equipment result disagreed")
		}
		character, _, err := repos.Character.Load(ctx, "77")
		if err != nil || character.Stats["gold"] != 1000 {
			t.Fatal("portable unexpectedly charged gold")
		}
	})
	t.Log(fmt.Sprintf("verified real PVF weapon, armor and earring across failure boundaries"))
}
