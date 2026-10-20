package inventory

import (
	"context"
	"encoding/binary"
	"errors"
	"reflect"
	"testing"

	"longheng.io/server/internal/modules/dnf/alignedcmd"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	memory "longheng.io/server/internal/modules/dnf/repository/memory"
)

func TestUpgradeAuthoritativeCostAndConversionAreAtomic(t *testing.T) {
	for _, name := range []string{"ordinary", "converted", "short conversion", "wrong conversion", "alias target", "reserved currency slot", "quote error", "insufficient gold", "late save failure", "shared late save failure"} {
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			repos := memory.NewMemoryGroup()
			seedUpgradeLateFailureWalletFixture(t, ctx, repos)
			record := loadTestInventory(t, ctx, repos, "77")
			record.Slots[slotKey(0, 135)] = dnfrepo.ItemStack{ItemID: 10158964, Count: 3}
			if name == "short conversion" {
				s := record.Slots[slotKey(0, 135)]
				s.Count = 2
				record.Slots[slotKey(0, 135)] = s
			}
			if name == "wrong conversion" {
				s := record.Slots[slotKey(0, 135)]
				s.ItemID = 999
				record.Slots[slotKey(0, 135)] = s
			}
			saveTestInventory(t, ctx, repos, record)
			before := loadTestInventory(t, ctx, repos, "77")
			if name == "late save failure" {
				repos.CharacterAssets = upgradeLateFailureAssets{base: repos.CharacterAssets, probe: &upgradeLateFailureProbe{saveErr: errUpgradeLateBagSave}}
			}
			calls := 0
			cmd := Command{Operation: "upgrade_item", Mode: "reinforce", AccountID: "acc", SelectedCharacterID: 77, TargetSlotIndex: 9, TargetItemTemplateID: 700, MaterialSlotIndex: 10, OptionalTicketSlot: -1, UpgradeSuccessWeight: 100000, UpgradeMaterialItemID: 3242, UpgradeMaterialCount: 10, UpgradeNPCPresent: true, UpgradeNPCID: 476, UpgradeNPCMaterialSlot: 135}
			if name == "reserved currency slot" {
				cmd.UpgradeNPCMaterialSlot = 1
			}
			if name == "shared late save failure" {
				if err := repos.AccountInventory.Save(ctx, dnfrepo.AccountInventoryRecord{AccountID: "acc", Slots: map[string]dnfrepo.ItemStack{dnfrepo.AccountSharedInventorySlotKey(358): {ItemID: 3037, Count: 12}}}); err != nil {
					t.Fatal(err)
				}
				repos.AccountAssets = upgradeLateFailureAccountAssets{base: repos.AccountAssets, probe: &upgradeLateFailureProbe{saveErr: errUpgradeLateBagSave}}
				cmd.MaterialSlotIndex = 358
				cmd.UpgradeMaterialItemID = 3037
			}
			if name == "alias target" {
				cmd.UpgradeNPCMaterialSlot = 9
			}
			cmd.UpgradeCostResolver = func(mode string, item int64, level int, present bool, npc uint16) (alignedcmd.UpgradeCostResolution, error) {
				calls++
				if mode != "reinforce" || item != 700 || level != 0 || !present || npc != 476 {
					t.Fatalf("unexpected quote context %s %d %d %t %d", mode, item, level, present, npc)
				}
				if name == "quote error" {
					return alignedcmd.UpgradeCostResolution{}, errors.New("unavailable authoritative PVF")
				}
				q := alignedcmd.UpgradeCostResolution{Resolved: true, ConversionItemID: 10158964, ConversionCount: 3}
				if name == "ordinary" {
					q = alignedcmd.UpgradeCostResolution{Resolved: true, GoldCost: 800}
				}
				if name == "insufficient gold" {
					q = alignedcmd.UpgradeCostResolution{Resolved: true, GoldCost: 12346}
				}
				return q, nil
			}
			owner, err := NewOwner(repos)
			if err != nil {
				t.Fatal(err)
			}
			got, err := owner.Upgrade(ctx, cmd)
			if calls != 1 {
				t.Fatalf("quote called %d times", calls)
			}
			after := loadTestInventory(t, ctx, repos, "77")
			char, _, _ := repos.Character.Load(ctx, "77")
			success := name == "ordinary" || name == "converted"
			if !success {
				if got.Success {
					t.Fatalf("accepted %s", name)
				}
				if (name == "quote error" || name == "late save failure" || name == "shared late save failure") && err == nil {
					t.Fatal("lost repository/resolver failure")
				}
				if name == "shared late save failure" {
					shared, _, err := repos.AccountInventory.Load(ctx, "acc")
					if err != nil || shared.Slots[dnfrepo.AccountSharedInventorySlotKey(358)].Count != 12 {
						t.Fatal("failed conversion retained shared-material debit")
					}
				}
				if !reflect.DeepEqual(before.Slots, after.Slots) || char.Stats["gold"] != 12345 {
					t.Fatalf("failed request changed assets: %+v", after)
				}
				return
			}
			if err != nil || !got.Success || upgradeLevelOf(after.Slots[slotKey(0, 9)]) != 1 || after.Slots[slotKey(0, 10)].Count != 1 {
				t.Fatalf("upgrade=%+v err=%v", got, err)
			}
			if name == "ordinary" {
				reply := ownerAppliedUpgradeResult(ctx, repos.Settings, 80, cmd, got)
				walletFound := false
				for _, packet := range reply.UpperResponses {
					body := packet.Body
					if len(body) == 3+currentItemListEntrySize && body[0] == 0 && binary.LittleEndian.Uint16(body[1:3]) == 1 && binary.LittleEndian.Uint16(body[3:5]) == 0 && binary.LittleEndian.Uint32(body[5:9]) == 0 {
						walletFound = true
						if binary.LittleEndian.Uint32(body[9:13]) != 11545 {
							t.Fatal("wallet projection is not committed balance")
						}
					}
				}
				if !walletFound {
					t.Fatal("authoritative fee success lacks wallet projection")
				}
				if char.Stats["gold"] != 11545 || after.Slots[slotKey(0, 135)].Count != 3 {
					t.Fatalf("ordinary cost did not replace Extra fee: %+v", char)
				}
			} else {
				if char.Stats["gold"] != 12345 {
					t.Fatal("conversion also charged gold")
				}
				if _, exists := after.Slots[slotKey(0, 135)]; exists {
					t.Fatal("conversion row not consumed")
				}
				entries := upgradeItemUpdateEntries(cmd, got)
				found := false
				for _, entry := range entries {
					if entry.slot == 135 {
						found = true
						if entry.stack.ItemID != 0 {
							t.Fatal("deleted material projected as nonempty")
						}
					}
				}
				if !found {
					t.Fatal("missing conversion material refresh")
				}
			}
		})
	}
}

func TestUpgradeProtectionSkipsReservedConversionSlot(t *testing.T) {
	t.Setenv(UpgradeProtectTicketEnv, UpgradeProtectTicketConsumeFirstMatch)
	items := map[string]dnfrepo.ItemStack{
		"0:135": {ItemID: 10158964, Count: 3},
		"0:136": {ItemID: 999, Count: 1},
	}
	cmd := Command{Mode: "reinforce", TargetSlotIndex: 9, MaterialSlotIndex: 10,
		UpgradeProtectTicketResolver: func(id int64) (alignedcmd.UpgradeProtectTicketResolution, error) {
			return alignedcmd.UpgradeProtectTicketResolution{Mode: "reinforce", UnitCost: 1}, nil
		}}
	selected, asked, _, ok := findUpgradeProtectTicket(items, cmd, 135)
	if !ok || selected.slot != 136 || asked != 1 {
		t.Fatalf("reserved conversion consumed as protection: %+v asked=%d ok=%t", selected, asked, ok)
	}
}

func TestUpgradeQuoteFailureReturnsOnlyErrorACK(t *testing.T) {
	ctx := context.Background()
	repos := memory.NewMemoryGroup()
	seedUpgradeLateFailureWalletFixture(t, ctx, repos)
	before := loadTestInventory(t, ctx, repos, "77")
	body := make([]byte, 16)
	binary.LittleEndian.PutUint16(body[2:4], 9)
	binary.LittleEndian.PutUint32(body[4:8], 700)
	binary.LittleEndian.PutUint16(body[8:10], 10)
	binary.LittleEndian.PutUint16(body[10:12], 0xffff)
	got, err := NewHandler().Handle(ctx, alignedcmd.Request{
		Opcode: 80, Body: body, AccountID: "acc", SelectedCharacterID: 77, Repositories: repos,
		UpgradePolicyResolver: func(mode string, targetItemID int64, level int) (alignedcmd.UpgradePolicyResolution, error) {
			return alignedcmd.UpgradePolicyResolution{SuccessWeight: 100000, MaterialItemID: 3242, MaterialCount: 10}, nil
		},
		UpgradeCostResolver: func(string, int64, int, bool, uint16) (alignedcmd.UpgradeCostResolution, error) {
			return alignedcmd.UpgradeCostResolution{}, errors.New("missing cost data")
		},
	})
	if err != nil || !got.ResponseAllowed || len(got.UpperResponses) != 1 {
		t.Fatalf("missing rejection: %+v err=%v", got, err)
	}
	packet := got.UpperResponses[0]
	if packet.MsgID != 80 || !reflect.DeepEqual(packet.Body, buildUpgradeItemErrorAck(upgradeErrorInvalidMaterial)) {
		t.Fatalf("wrong error ACK %+v", packet)
	}
	after := loadTestInventory(t, ctx, repos, "77")
	char, _, _ := repos.Character.Load(ctx, "77")
	if !reflect.DeepEqual(before.Slots, after.Slots) || char.Stats["gold"] != 12345 {
		t.Fatal("quote rejection changed assets")
	}
}
