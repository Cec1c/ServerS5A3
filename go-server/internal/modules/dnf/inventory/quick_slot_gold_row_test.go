package inventory

import (
	"context"
	"encoding/binary"
	"testing"

	"longheng.io/server/internal/modules/dnf/alignedcmd"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	dnfrepomemory "longheng.io/server/internal/modules/dnf/repository/memory"
)

// 现场报障回归（2026-09-26）：
//
//	5. 「快捷栏道具用完或者改变位置有可能会变成金币 已通过将快捷栏内的徽章移至
//	    装扮栏-徽章栏 复现」
//
// 机制与 2026-09-25 那条 op14 事故**同源**：运行时 Script.pvf 的 **item 0 是
// `stackable/gold.stk`（`[name] 金币`）**，客户端按 itemID 查物品表，所以任何被编码成
// `itemID=0` 的 0x77 行都会被画成「金币」道具。
//
// op14 增量编码早已把 `ItemID<=0` 归一化成空行哨兵（`buildCommonItemListUpdateBody`，
// 见 TestBuildCommonItemListUpdateEncodesExhaustedStackAsEmptyRowSentinel），但
// **op13 全量刷新**（移动/整理成功后的整表回包，`buildCommonItemListRefreshBodyWithState`
// / `buildPetItemListRefreshBody`，两者共用 `commonItemListEntries`）当时没有这道闸门：
// 「用完」留下的零值堆叠与任何非正 itemID 的残留行都会随刷新原样下发成 item 0。
//
// 主背包槽 0 是唯一例外——那**就是**客户端自己那条金币行。

// 零值/非正 itemID 的行不得进 op13 全量刷新（否则客户端画成金币）。
func TestBuildCommonItemListRefreshDropsNonPositiveItemRows(t *testing.T) {
	slots := map[string]dnfrepo.ItemStack{
		// 「用完」留下的零值堆叠：旧实现把它写成 itemID=0 ⇒ 客户端「金币」。
		slotKey(listTypeMain, 5): {},
		// 另一个非正 itemID 的残留行（-1 是历史脏数据）。
		slotKey(listTypeMain, 6): {ItemID: -1, Count: 9},
		// 正常行必须原样保留。
		slotKey(listTypeMain, 7): {ItemID: 3227, Count: 2},
		// 主背包槽 0 = 客户端自己的金币行：item 0 必须保留。
		slotKey(listTypeMain, 0): {ItemID: 0, Count: 777},
	}
	body := buildCommonItemListRefreshBodyWithState(listTypeMain, slots, dnfrepo.CharacterContainerState{})
	if body[0] != listTypeMain {
		t.Fatalf("refresh list=%d want=%d", body[0], listTypeMain)
	}
	count := int(binary.LittleEndian.Uint16(body[3:5]))
	if count != 2 {
		t.Fatalf("refresh row count=%d want=2 (只保留 0:0 金币行与 0:7 正常行) body=%x", count, body)
	}
	rows := body[5:]
	first := rows[:currentItemListEntrySize]
	if got := binary.LittleEndian.Uint16(first[0x00:0x02]); got != 0 {
		t.Fatalf("first kept row slot=%d want=0 (金币行)", got)
	}
	if got := binary.LittleEndian.Uint32(first[0x02:0x06]); got != 0 {
		t.Fatalf("gold row itemID=%d want=0", got)
	}
	if got := binary.LittleEndian.Uint32(first[0x06:0x0A]); got != 777 {
		t.Fatalf("gold row count=%d want=777", got)
	}
	second := rows[currentItemListEntrySize : 2*currentItemListEntrySize]
	if got := binary.LittleEndian.Uint16(second[0x00:0x02]); got != 7 {
		t.Fatalf("second kept row slot=%d want=7", got)
	}
	if got := binary.LittleEndian.Uint32(second[0x02:0x06]); got != 3227 {
		t.Fatalf("second kept row itemID=%d want=3227", got)
	}
	// 整包里绝不能出现任何 itemID=0 的普通行。
	for offset := 5; offset+currentItemListEntrySize <= len(rows)+5; offset += currentItemListEntrySize {
		slot := binary.LittleEndian.Uint16(rows[offset-5 : offset-3])
		itemID := binary.LittleEndian.Uint32(rows[offset-3 : offset+1])
		if itemID == 0 && slot != 0 {
			t.Fatalf("list-0 refresh still emits itemID=0 at slot %d (客户端会画成金币)", slot)
		}
	}
}

// 宠物背包（list 7）同样不得下发 itemID=0 的行。
func TestBuildPetItemListRefreshDropsNonPositiveItemRows(t *testing.T) {
	slots := map[string]dnfrepo.ItemStack{
		slotKey(listTypePet, 5): {},
		slotKey(listTypePet, 6): {ItemID: 3001, Count: 1},
	}
	body := buildPetItemListRefreshBody(slots)
	if body[0] != listTypePet {
		t.Fatalf("pet refresh list=%d", body[0])
	}
	if count := int(binary.LittleEndian.Uint16(body[1:3])); count != 1 {
		t.Fatalf("pet refresh row count=%d want=1 body=%x", count, body)
	}
	if got := binary.LittleEndian.Uint32(body[3+0x02 : 3+0x06]); got != 3001 {
		t.Fatalf("pet refresh first row itemID=%d want=3001", got)
	}
}

// 端到端：把快捷格里的徽章移到装扮栏的徽章页之后，服务端下发的整表刷新里不得再出现
// 任何 itemID=0 的行。这就是操作者的复现路径（快捷栏 → 装扮栏-徽章栏），
// 同表另一格留着"用完"的零值残留行。
func TestHandlerMoveQuickSlotBadgeRefreshHasNoGoldRows(t *testing.T) {
	ctx := context.Background()
	repos := dnfrepomemory.NewMemoryGroup()
	saveTestInventory(t, ctx, repos, dnfrepo.InventoryRecord{
		CharacterID: "77",
		Slots: map[string]dnfrepo.ItemStack{
			slotKey(listTypeMain, 5): pvfMoveStack(10303480, 2, 10),
			// 同身份行：合并走 `stack` 分支，服务端会回一条 list 0 整表刷新。
			slotKey(listTypeMain, 289): pvfMoveStack(10303480, 1, 10),
			// 同一张表里另一格是"用完"留下的零值残留行。
			slotKey(listTypeMain, 6): {},
			// 客户端自己的金币行。
			slotKey(listTypeMain, 0): {ItemID: 0, Count: 4321},
		},
	})
	result, err := NewHandler().Handle(ctx, alignedcmd.Request{
		Opcode:              uint16(0x0013),
		AccountID:           "dnf:1",
		SelectedCharacterID: 77,
		Repositories:        repos,
		Body:                currentMoveItemspaceBody(listTypeMain, 5, 10303480, 2, listTypeMain, 289, 10303480, 1, -1),
	})
	if err != nil {
		t.Fatalf("Handle error = %v", err)
	}
	if !result.Handled || !result.ResponseAllowed {
		t.Fatalf("badge move was not applied: %+v", result)
	}
	loaded := loadTestInventory(t, ctx, repos, "77")
	if got := loaded.Slots[slotKey(listTypeMain, 289)]; got.ItemID != 10303480 || got.Count != 3 {
		t.Fatalf("emblem-page row after the merge = %+v", got)
	}
	if _, still := loaded.Slots[slotKey(listTypeMain, 5)]; still {
		t.Fatalf("quick-slot source row survived: %+v", loaded.Slots)
	}
	checked := 0
	for _, response := range result.UpperResponses {
		if response.Classification != 0 || response.MsgID != msgItemListRefresh {
			continue
		}
		body := response.Body
		if len(body) < 5 || body[0] != listTypeMain {
			continue
		}
		checked++
		count := int(binary.LittleEndian.Uint16(body[3:5]))
		for index := 0; index < count; index++ {
			row := body[5+index*currentItemListEntrySize:]
			slot := binary.LittleEndian.Uint16(row[0x00:0x02])
			itemID := binary.LittleEndian.Uint32(row[0x02:0x06])
			if itemID == 0 && slot != 0 {
				t.Fatalf("post-move list-0 refresh emits itemID=0 at slot %d (现场报障「变成金币」)", slot)
			}
		}
	}
	if checked == 0 {
		t.Fatal("no list-0 refresh was emitted for the badge move")
	}
}
