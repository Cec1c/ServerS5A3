package inventory

import (
	"context"
	"encoding/binary"
	"errors"
	"reflect"
	"testing"

	"longheng.io/server/internal/modules/dnf/alignedcmd"
	"longheng.io/server/internal/modules/dnf/dnfenum"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	memory "longheng.io/server/internal/modules/dnf/repository/memory"
)

func TestUpgradeFailureUsesPVFPenaltyValueAndPreservesProtection(t *testing.T) {
	for _, tc := range []struct {
		name        string
		kind, value int
		level, want byte
	}{
		{"unchanged", 1, 0, 8, 8}, {"amplify reset", 2, 8, 8, 0}, {"subtract one", 2, 1, 6, 5},
		{"reinforce subtract three", 2, 3, 10, 7}, {"clamp subtraction", 2, 99, 8, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			for attempt := 0; attempt < 64; attempt++ {
				ctx := context.Background()
				repos := memory.NewMemoryGroup()
				saveTestCharacter(t, ctx, repos, dnfrepo.CharacterRecord{CharacterID: "77", AccountID: "acc", Stats: map[string]int64{"gold": 12345}})
				saveTestInventory(t, ctx, repos, dnfrepo.InventoryRecord{CharacterID: "77", Slots: map[string]dnfrepo.ItemStack{
					slotKey(0, 9):   protectTicketEquipment("amplify", tc.level),
					slotKey(0, 121): protectTicketStack(10303749, 2, "stackable/10303001/10303749.stk"),
					slotKey(0, 122): {ItemID: 3242, Count: 20},
				}})
				owner, err := NewOwner(repos)
				if err != nil {
					t.Fatal(err)
				}
				cmd := Command{Operation: "upgrade_item", Mode: "amplify", AccountID: "acc", SelectedCharacterID: 77, TargetSlotIndex: 9, TargetItemTemplateID: 700, MaterialSlotIndex: 122, OptionalTicketSlot: -1,
					UpgradeSuccessWeight: 1, UpgradePenaltyType: tc.kind, UpgradePenaltyValue: tc.value, UpgradeMaterialItemID: 3242, UpgradeMaterialCount: 9,
					UpgradeProtectTicketResolver: protectTicketResolver(liveProtectTicketResolutions()),
					UpgradeCostResolver: func(string, int64, int, bool, uint16) (alignedcmd.UpgradeCostResolution, error) {
						return alignedcmd.UpgradeCostResolution{Resolved: true, GoldCost: 100}, nil
					},
				}
				got, err := owner.Upgrade(ctx, cmd)
				if err != nil || !got.Success {
					t.Fatalf("result=%+v err=%v", got, err)
				}
				if got.UpgradeSucceeded {
					continue
				}
				bag := loadTestInventory(t, ctx, repos, "77")
				char, _, _ := repos.Character.Load(ctx, "77")
				stack, present := bag.Slots[slotKey(0, 9)]
				if !present || got.NewLevel != tc.want || upgradeLevelOf(stack) != tc.want || got.ProtectTicketConsumed || got.DestroyBonusCount != 0 || bag.Slots[slotKey(0, 121)].Count != 2 || bag.Slots[slotKey(0, 122)].Count != 11 || char.Stats["gold"] != 12245 {
					t.Fatalf("penalty=(%d,%d) result=%+v target=%+v bag=%+v gold=%d want level=%d", tc.kind, tc.value, got, stack, bag.Slots, char.Stats["gold"], tc.want)
				}
				wantCode := byte(upgradeResultCodeFailNoChange)
				if tc.kind == 2 {
					wantCode = upgradeResultCodeDowngrade
				}
				if got.ResultCode != wantCode {
					t.Fatalf("result code=%d want=%d", got.ResultCode, wantCode)
				}
				return
			}
			t.Fatal("64 attempts all succeeded at success weight 1")
		})
	}
}

func TestHandlerUpgradePolicyFailureAndChangedLevelPreserveAssets(t *testing.T) {
	for _, name := range []string{"policy error", "missing policy", "changed level", "zero success weight", "late save failure"} {
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			repos := memory.NewMemoryGroup()
			saveTestCharacter(t, ctx, repos, dnfrepo.CharacterRecord{CharacterID: "77", AccountID: "acc", Stats: map[string]int64{"gold": 12345}})
			saveTestInventory(t, ctx, repos, dnfrepo.InventoryRecord{CharacterID: "77", Slots: map[string]dnfrepo.ItemStack{
				slotKey(0, 9): protectTicketEquipment("amplify", 8), slotKey(0, 122): {ItemID: 3242, Count: 20},
			}})
			before := loadTestInventory(t, ctx, repos, "77")
			if name == "late save failure" {
				repos.CharacterAssets = upgradeLateFailureAssets{base: repos.CharacterAssets, probe: &upgradeLateFailureProbe{saveErr: errUpgradeLateBagSave}}
			}
			body := make([]byte, 16)
			binary.LittleEndian.PutUint16(body, 1)
			binary.LittleEndian.PutUint16(body[2:], 9)
			binary.LittleEndian.PutUint32(body[4:], 700)
			binary.LittleEndian.PutUint16(body[8:], 122)
			binary.LittleEndian.PutUint16(body[10:], 65535)
			policy := alignedcmd.UpgradePolicyResolver(func(mode string, item int64, level int) (alignedcmd.UpgradePolicyResolution, error) {
				if mode != "amplify" || item != 700 || level != 8 {
					t.Fatalf("wrong policy identity %s/%d/%d", mode, item, level)
				}
				if name == "policy error" {
					return alignedcmd.UpgradePolicyResolution{}, errors.New("policy unavailable")
				}
				if name == "changed level" {
					row := before.Slots[slotKey(0, 9)]
					setUpgradeLevel(&row, 9)
					before.Slots[slotKey(0, 9)] = row
					saveTestInventory(t, ctx, repos, before)
				}
				return alignedcmd.UpgradePolicyResolution{SuccessWeight: 0, PenaltyType: 2, PenaltyValue: 8, MaterialItemID: 3242, MaterialCount: 9}, nil
			})
			if name == "missing policy" {
				policy = nil
			}
			result, err := NewHandler().Handle(ctx, alignedcmd.Request{Opcode: uint16(dnfenum.CmdPacketUpgradeItem), Body: body, AccountID: "acc", SelectedCharacterID: 77, Repositories: repos, UpgradePolicyResolver: policy})
			if err != nil {
				t.Fatal(err)
			}
			if !result.Handled {
				t.Fatalf("upgrade handler did not handle request: %+v", result)
			}
			if name == "late save failure" && result.ResponseAllowed {
				t.Fatal("late transaction failure allowed a success response")
			}
			after := loadTestInventory(t, ctx, repos, "77")
			char, _, _ := repos.Character.Load(ctx, "77")
			if name == "zero success weight" {
				if !result.ResponseAllowed || upgradeLevelOf(after.Slots[slotKey(0, 9)]) != 0 || after.Slots[slotKey(0, 122)].Count != 11 {
					t.Fatalf("zero success weight did not execute exact failure: %+v", result)
				}
				return
			}
			if !reflect.DeepEqual(before.Slots, after.Slots) || char.Stats["gold"] != 12345 {
				t.Fatalf("failed/stale policy mutated assets: case=%s", name)
			}
			if name != "late save failure" && (len(result.UpperResponses) != 1 || len(result.UpperResponses[0].Body) != 2 || result.UpperResponses[0].Body[0] != 0) {
				t.Fatalf("missing failure ACK: %+v", result)
			}
		})
	}
}
