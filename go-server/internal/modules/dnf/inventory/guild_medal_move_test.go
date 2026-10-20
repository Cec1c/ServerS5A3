package inventory

import (
	"context"
	"errors"
	"testing"

	"longheng.io/server/internal/modules/dnf/alignedcmd"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	dnfrepomemory "longheng.io/server/internal/modules/dnf/repository/memory"
)

// 现场报障回归（2026-09-26）：
//
//	2. 「公会勋章……无法被拖拽进入勋章栏-勋章」
//	3. 「公会守护珠……拖拽装备无反应 无法被拖拽进入勋章栏-守护珠」
//
// 发放侧的根因在 dnfbridge 的容器判定（勋章/守护珠家族缺失）；**拖拽侧**的根因
// 在这里：`checkMoveListType` 的允许集合只有 {0,1,2,7}，wire list 38（公会勋章容器，
// 0..48 勋章页 + 49..97 守护珠页）从未被接受，于是"背包 → 勋章栏"的 op19 被直接判成
// ErrUnsupportedList，客户端表现就是"拖拽无反应"，而右键穿戴走的是 list 3 槽 32 的
// 装备端点（equipment_placement.go 已支持 38→3/32），所以"右键可直接装备"。
//
// list 38 与 list 7 一样是**角色记录里的普通 (list, slot) 行**，不是穿戴端点，
// 因此由通用 inventory owner 落库，并复用"整容器重发"收口客户端刷新。

func TestOwnerMoveAcceptsGuildMedalContainerEndpoints(t *testing.T) {
	ctx := context.Background()
	repos := dnfrepomemory.NewMemoryGroup()
	saveTestInventory(t, ctx, repos, dnfrepo.InventoryRecord{
		CharacterID: "77",
		Slots: map[string]dnfrepo.ItemStack{
			slotKey(listTypeMain, 12): pvfMoveStack(100380017, 1, 0),
		},
	})
	owner, err := NewOwner(repos)
	if err != nil {
		t.Fatalf("NewOwner error = %v", err)
	}

	// 背包 → 勋章栏：以前这里是 ErrUnsupportedList（拖拽无反应）。
	result, err := owner.Move(ctx, NewMoveCommand(alignedcmd.Request{
		AccountID:           "dnf:1",
		SelectedCharacterID: 77,
	}, MoveItemspaceRequest{
		SourceListType:       listTypeMain,
		SourceSlotIndex:      12,
		MoveCount:            1,
		DestinationListType:  listTypeGuildMedal,
		DestinationSlotIndex: 0,
	}))
	if err != nil {
		t.Fatalf("move into the guild-medal page must be accepted, got %v", err)
	}
	if !result.Changed || result.Mode != "move" {
		t.Fatalf("result = %+v", result)
	}
	loaded := loadTestInventory(t, ctx, repos, "77")
	if _, still := loaded.Slots[slotKey(listTypeMain, 12)]; still {
		t.Fatalf("source row survived the accepted move: %+v", loaded.Slots)
	}
	if got := loaded.Slots[slotKey(listTypeGuildMedal, 0)]; got.ItemID != 100380017 || got.Count != 1 {
		t.Fatalf("guild-medal row 38:0 = %+v", got)
	}

	// 勋章栏 → 背包：同一容器的反向拖拽也必须被接受。
	result, err = owner.Move(ctx, NewMoveCommand(alignedcmd.Request{
		AccountID:           "dnf:1",
		SelectedCharacterID: 77,
	}, MoveItemspaceRequest{
		SourceListType:       listTypeGuildMedal,
		SourceSlotIndex:      0,
		MoveCount:            1,
		DestinationListType:  listTypeMain,
		DestinationSlotIndex: 12,
	}))
	if err != nil {
		t.Fatalf("move out of the guild-medal page must be accepted, got %v", err)
	}
	if !result.Changed {
		t.Fatalf("result = %+v", result)
	}
	loaded = loadTestInventory(t, ctx, repos, "77")
	if got := loaded.Slots[slotKey(listTypeMain, 12)]; got.ItemID != 100380017 || got.Count != 1 {
		t.Fatalf("returned main row = %+v", got)
	}
	if _, still := loaded.Slots[slotKey(listTypeGuildMedal, 0)]; still {
		t.Fatalf("guild-medal source row survived: %+v", loaded.Slots)
	}
}

// 守护珠页（49..97）与勋章页同样被接受；守护珠的堆叠合并也照常走。
func TestOwnerMoveAcceptsGuardianGemPageAndMergesThere(t *testing.T) {
	ctx := context.Background()
	repos := dnfrepomemory.NewMemoryGroup()
	saveTestInventory(t, ctx, repos, dnfrepo.InventoryRecord{
		CharacterID: "77",
		Slots: map[string]dnfrepo.ItemStack{
			slotKey(listTypeMain, 121):      pvfMoveStack(90002, 2, 10),
			slotKey(listTypeGuildMedal, 49): pvfMoveStack(90002, 1, 10),
		},
	})
	owner, err := NewOwner(repos)
	if err != nil {
		t.Fatalf("NewOwner error = %v", err)
	}
	result, err := owner.Move(ctx, NewMoveCommand(alignedcmd.Request{
		AccountID:           "dnf:1",
		SelectedCharacterID: 77,
	}, MoveItemspaceRequest{
		SourceListType:       listTypeMain,
		SourceSlotIndex:      121,
		MoveCount:            2,
		DestinationListType:  listTypeGuildMedal,
		DestinationSlotIndex: 49,
	}))
	if err != nil {
		t.Fatalf("move into the guardian-gem page must be accepted, got %v", err)
	}
	if !result.Changed || result.Mode != "stack" || result.MoveCount != 2 {
		t.Fatalf("result = %+v", result)
	}
	loaded := loadTestInventory(t, ctx, repos, "77")
	if got := loaded.Slots[slotKey(listTypeGuildMedal, 49)]; got.ItemID != 90002 || got.Count != 3 {
		t.Fatalf("guardian-gem row 38:49 = %+v", got)
	}
	if _, still := loaded.Slots[slotKey(listTypeMain, 121)]; still {
		t.Fatalf("source row survived the merge: %+v", loaded.Slots)
	}
}

// 页外槽位必须失败关闭：list 38 的物理格只有 0..97（超出既不在客户端勋章栏里，
// 也不该被落库）。
func TestOwnerMoveRejectsGuildMedalSlotsOutsideTheContainer(t *testing.T) {
	ctx := context.Background()
	repos := dnfrepomemory.NewMemoryGroup()
	saveTestInventory(t, ctx, repos, dnfrepo.InventoryRecord{
		CharacterID: "77",
		Slots: map[string]dnfrepo.ItemStack{
			slotKey(listTypeMain, 12):      pvfMoveStack(100380017, 1, 0),
			slotKey(listTypeGuildMedal, 7): pvfMoveStack(100380017, 1, 0),
		},
	})
	owner, err := NewOwner(repos)
	if err != nil {
		t.Fatalf("NewOwner error = %v", err)
	}
	for _, slot := range []int16{98, 200, -1} {
		_, err := owner.Move(ctx, NewMoveCommand(alignedcmd.Request{
			AccountID:           "dnf:1",
			SelectedCharacterID: 77,
		}, MoveItemspaceRequest{
			SourceListType:       listTypeMain,
			SourceSlotIndex:      12,
			MoveCount:            1,
			DestinationListType:  listTypeGuildMedal,
			DestinationSlotIndex: slot,
		}))
		if !errors.Is(err, ErrUnsupportedList) {
			t.Fatalf("destination slot %d error = %v, want ErrUnsupportedList", slot, err)
		}
	}
	loaded := loadTestInventory(t, ctx, repos, "77")
	if got := loaded.Slots[slotKey(listTypeMain, 12)]; got.ItemID != 100380017 {
		t.Fatalf("rejected move mutated the source row: %+v", loaded.Slots)
	}
	if len(loaded.Slots) != 2 {
		t.Fatalf("rejected move created rows: %+v", loaded.Slots)
	}
}

// handler 层：动了 list 38 的移动必须请求"整容器重发"（list 38 有自己的权威快照
// 构造器，不能走 list 0/1/2 的 op14 增量行）。
func TestHandlerMoveTouchingGuildMedalRequestsContainerRefresh(t *testing.T) {
	ctx := context.Background()
	repos := dnfrepomemory.NewMemoryGroup()
	saveTestInventory(t, ctx, repos, dnfrepo.InventoryRecord{
		CharacterID: "77",
		Slots: map[string]dnfrepo.ItemStack{
			slotKey(listTypeMain, 12): pvfMoveStack(100380017, 1, 0),
		},
	})
	result, err := NewHandler().Handle(ctx, alignedcmd.Request{
		Opcode:              uint16(0x0013),
		AccountID:           "dnf:1",
		SelectedCharacterID: 77,
		Repositories:        repos,
		Body:                currentMoveItemspaceBody(listTypeMain, 12, 100380017, 1, listTypeGuildMedal, 0, 0, 0, -1),
	})
	if err != nil {
		t.Fatalf("Handle error = %v", err)
	}
	if !result.Handled {
		t.Fatalf("handler did not apply the move: %+v", result)
	}
	found := false
	for _, action := range result.PostActions {
		if action == alignedcmd.PostActionRefreshSelectedItemContainers {
			found = true
		}
	}
	if !found {
		t.Fatalf("post actions = %v, want %v", result.PostActions, alignedcmd.PostActionRefreshSelectedItemContainers)
	}
	loaded := loadTestInventory(t, ctx, repos, "77")
	if got := loaded.Slots[slotKey(listTypeGuildMedal, 0)]; got.ItemID != 100380017 {
		t.Fatalf("guild-medal row after the handler move = %+v", got)
	}
}

// 未动 list 38 的普通移动不得新增该 post-action（逐值回归）。
func TestHandlerMoveWithoutGuildMedalKeepsPostActions(t *testing.T) {
	ctx := context.Background()
	repos := dnfrepomemory.NewMemoryGroup()
	saveTestInventory(t, ctx, repos, dnfrepo.InventoryRecord{
		CharacterID: "77",
		Slots: map[string]dnfrepo.ItemStack{
			slotKey(listTypeMain, 12): pvfMoveStack(100380017, 1, 0),
		},
	})
	result, err := NewHandler().Handle(ctx, alignedcmd.Request{
		Opcode:              uint16(0x0013),
		AccountID:           "dnf:1",
		SelectedCharacterID: 77,
		Repositories:        repos,
		Body:                currentMoveItemspaceBody(listTypeMain, 12, 100380017, 1, listTypeMain, 20, 0, 0, -1),
	})
	if err != nil {
		t.Fatalf("Handle error = %v", err)
	}
	for _, action := range result.PostActions {
		if action == alignedcmd.PostActionRefreshSelectedItemContainers {
			t.Fatalf("ordinary main-list move requested the list-38 container refresh: %v", result.PostActions)
		}
	}
}
