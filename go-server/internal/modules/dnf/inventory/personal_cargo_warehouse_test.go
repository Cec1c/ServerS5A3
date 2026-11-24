package inventory

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"longheng.io/server/internal/modules/dnf/alignedcmd"
	"longheng.io/server/internal/modules/dnf/dnfenum"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	dnfrepomemory "longheng.io/server/internal/modules/dnf/repository/memory"
)

func TestPersonalCargoWarehouseWithdrawalRoutesActualItemAndAck(t *testing.T) {
	for _, id := range []int64{3033, 3034, 3035, 3036, 3037, 3262, 10100115, 10100116, 10099773, 10099774, 10099775, 10158124} {
		for _, partial := range []bool{false, true} {
			t.Run(fmt.Sprintf("%d/partial=%t", id, partial), func(t *testing.T) {
				ctx := context.Background()
				repos := dnfrepomemory.NewMemoryGroup()
				saveTestInventory(t, ctx, repos, dnfrepo.InventoryRecord{CharacterID: "77", Warehouse: map[string]dnfrepo.ItemStack{"2:4": pvfMoveStack(id, 10, 100)}})
				slot, _ := dnfrepo.AccountWarehouseSlotForItem(id)
				if err := repos.AccountInventory.Save(ctx, dnfrepo.AccountInventoryRecord{AccountID: "a", Slots: map[string]dnfrepo.ItemStack{slotKey(0, slot): pvfMoveStack(id, 20, 100)}}); err != nil {
					t.Fatal(err)
				}
				count := int32(10)
				if partial {
					count = 3
				}
				result, err := NewHandler().Handle(ctx, alignedcmd.Request{Opcode: uint16(dnfenum.CmdPacketMoveItemspace), Body: currentMoveItemspaceBody(2, 4, 0, count, 0, 150, 0, 0, -1), AccountID: "a", SelectedCharacterID: 77, Repositories: repos})
				if err != nil || len(result.UpperResponses) != 1 {
					t.Fatalf("result=%+v err=%v", result, err)
				}
				ack := decodeMoveAckForTest(t, result.UpperResponses[0].Body)
				if ack.destList != 0 || ack.destSlot != slot || ack.sourceList != 2 || ack.sourceSlot != 4 || ack.moveCount != count {
					t.Fatalf("ACK=%+v", ack)
				}
				ch := loadTestInventory(t, ctx, repos, "77")
				account, _, err := repos.AccountInventory.Load(ctx, "a")
				if err != nil || account.Slots[slotKey(0, slot)].Count != 20+int64(count) || ch.Warehouse["2:4"].Count != 10-int64(count) || len(ch.Slots) != 0 {
					t.Fatalf("ch=%+v account=%+v err=%v", ch, account, err)
				}
				if len(result.ItemSlotRefreshes) != 2 || result.ItemSlotRefreshes[0].SlotIndex != 4 || result.ItemSlotRefreshes[1].SlotIndex != slot {
					t.Fatalf("refresh=%+v", result.ItemSlotRefreshes)
				}
			})
		}
	}
}

func TestPersonalCargoWarehouseWithdrawalEmptyFullConflictAndRollback(t *testing.T) {
	for _, mode := range []string{"empty", "full", "occupied", "rollback"} {
		t.Run(mode, func(t *testing.T) {
			ctx := context.Background()
			repos := dnfrepomemory.NewMemoryGroup()
			saveTestInventory(t, ctx, repos, dnfrepo.InventoryRecord{CharacterID: "77", Warehouse: map[string]dnfrepo.ItemStack{"2:4": pvfMoveStack(3037, 10, 100)}})
			slots := map[string]dnfrepo.ItemStack{}
			if mode != "empty" {
				slots["0:358"] = pvfMoveStack(3037, 95, 100)
			}
			if mode == "occupied" {
				slots["0:358"] = pvfMoveStack(3033, 20, 100)
			}
			if mode == "rollback" {
				slots["0:358"] = pvfMoveStack(3037, 20, 100)
			}
			if err := repos.AccountInventory.Save(ctx, dnfrepo.AccountInventoryRecord{AccountID: "a", Slots: slots}); err != nil {
				t.Fatal(err)
			}
			if mode == "rollback" {
				repos.AccountItems = rollbackAccountCharacterItemsAfterApply{base: repos.AccountItems, err: errors.New("forced commit failure")}
			}
			owner, err := NewOwner(repos)
			if err != nil {
				t.Fatal(err)
			}
			result, err := owner.Move(ctx, Command{AccountID: "a", SelectedCharacterID: 77, SourceListType: 2, SourceSlotIndex: 4, MoveCount: 10, DestinationListType: 0, DestinationSlotIndex: 150})
			ch := loadTestInventory(t, ctx, repos, "77")
			account, _, _ := repos.AccountInventory.Load(ctx, "a")
			if mode == "empty" {
				if err != nil || !result.Changed || account.Slots["0:358"].Count != 10 || len(ch.Warehouse) != 0 {
					t.Fatalf("result=%+v err=%v", result, err)
				}
			} else {
				if err == nil || ch.Warehouse["2:4"].Count != 10 || account.Slots["0:358"].Count != slots["0:358"].Count || account.Slots["0:358"].ItemID != slots["0:358"].ItemID {
					t.Fatalf("failed move changed assets: result=%+v ch=%+v account=%+v err=%v", result, ch, account, err)
				}
			}
		})
	}
}
