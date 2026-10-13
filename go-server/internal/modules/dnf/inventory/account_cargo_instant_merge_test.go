package inventory

import (
	"context"
	"encoding/binary"
	"reflect"
	"strings"
	"testing"

	"longheng.io/server/internal/modules/dnf/alignedcmd"
	"longheng.io/server/internal/modules/dnf/dnfenum"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	dnfrepomemory "longheng.io/server/internal/modules/dnf/repository/memory"
)

// P61/P68 现场（用户 2026-10-13 最新反馈）：账号金库同种道具**能堆叠**，但存入时物品先在
// 被点名的空槽里停约 1 秒，随后才并入已有同类堆；用户要求存入当场归入已有堆，
// 不能先摆出独立一行。
//
// 已有 owner/UOW 会在事务中合并数量。候选修正 ACK 点名空槽与实际合并行的差异。
// 当前 EXE 对重定向 ACK 的即时表现仍需主代理验证；这些测试只约束服务端输出。
// 回归文件尚未执行，不能作为 RED/GREEN 或实机通过证据。

type moveAckEndpoints struct {
	sourceList byte
	sourceSlot int16
	moveCount  int32
	destList   byte
	destSlot   int16
}

func decodeMoveAckForTest(t *testing.T, body []byte) moveAckEndpoints {
	t.Helper()
	if len(body) != 11 {
		t.Fatalf("ACK length = %d, want 11 (% X)", len(body), body)
	}
	if body[0] != 1 {
		t.Fatalf("ACK result byte = %d, want success=1", body[0])
	}
	return moveAckEndpoints{
		sourceList: body[1],
		sourceSlot: int16(binary.LittleEndian.Uint16(body[2:4])),
		moveCount:  int32(binary.LittleEndian.Uint32(body[4:8])),
		destList:   body[8],
		destSlot:   int16(binary.LittleEndian.Uint16(body[9:11])),
	}
}

func saveTestAccountCargo(t *testing.T, ctx context.Context, repos dnfrepo.Group, accountID string, slots map[string]dnfrepo.ItemStack) {
	t.Helper()
	if err := repos.Account.Save(ctx, dnfrepo.AccountRecord{
		AccountID: accountID,
		Metadata:  map[string]string{"account_cargo_created": "true", "account_cargo_level": "8"},
	}); err != nil {
		t.Fatal(err)
	}
	if err := repos.AccountInventory.Save(ctx, dnfrepo.AccountInventoryRecord{AccountID: accountID, Slots: slots}); err != nil {
		t.Fatal(err)
	}
}

// 存入：背包 → 账号金库空槽，金库里已有同一件可堆叠行。ACK 必须直接回那一行。
func TestAccountCargoDepositAckProjectsCommittedMergeSlot(t *testing.T) {
	ctx := context.Background()
	repos := dnfrepomemory.NewMemoryGroup()
	extra := stackableExtra("stackable/material/solvent_magicpower.stk", "1000")
	saveTestInventory(t, ctx, repos, dnfrepo.InventoryRecord{
		CharacterID: "77",
		Slots: map[string]dnfrepo.ItemStack{
			slotKey(listTypeMain, 21): {ItemID: 3227, Count: 5, Extra: extra},
		},
	})
	saveTestAccountCargo(t, ctx, repos, "dnf:ack-deposit", map[string]dnfrepo.ItemStack{
		slotKey(listTypeAccountCargo, 3): {ItemID: 3227, Count: 7, Extra: extra},
	})

	got, err := NewHandler().Handle(ctx, alignedcmd.Request{
		Opcode:              uint16(dnfenum.CmdPacketMoveItemspace),
		Body:                currentMoveItemspaceBody(listTypeMain, 21, 0, 5, listTypeAccountCargo, 4, 0, 0, -1),
		AccountID:           "dnf:ack-deposit",
		SelectedCharacterID: 77,
		Repositories:        repos,
	})
	if err != nil || !got.Handled || !got.ResponseAllowed {
		t.Fatalf("deposit result=%+v err=%v", got, err)
	}
	// 包顺序：先 class1/op19 ACK，再 list 0 的 class0/0x000D 权威刷新，最后才是
	// 账号金库整容器（由 PostActions 交给 bridge 构包，见 game_aligned.go 的
	// PostActionRefreshSelectedAccountCargo）。
	if len(got.UpperResponses) != 2 {
		t.Fatalf("upper responses=%d, want ACK + list0 refresh", len(got.UpperResponses))
	}
	ack := got.UpperResponses[0]
	if ack.MsgID != uint16(dnfenum.CmdPacketMoveItemspace) || ack.Classification != 1 {
		t.Fatalf("first packet=%+v, want class1 op19 ACK", ack)
	}
	endpoints := decodeMoveAckForTest(t, ack.Body)
	if endpoints.sourceList != listTypeMain || endpoints.sourceSlot != 21 || endpoints.moveCount != 5 {
		t.Fatalf("ACK source=%+v, want the requested bag row (0,21) of 5", endpoints)
	}
	if endpoints.destList != listTypeAccountCargo || endpoints.destSlot != 3 {
		t.Fatalf("ACK destination=(%d,%d), want the existing vault row (12,3)", endpoints.destList, endpoints.destSlot)
	}
	refresh := got.UpperResponses[1]
	if refresh.MsgID != msgItemListRefresh || refresh.Classification != 0 || len(refresh.Body) == 0 || refresh.Body[0] != listTypeMain {
		t.Fatalf("second packet=%+v, want class0 list0 refresh", refresh)
	}
	if !reflect.DeepEqual(got.PostActions, []alignedcmd.PostAction{alignedcmd.PostActionRefreshSelectedAccountCargo}) {
		t.Fatalf("post actions=%v, want one account-cargo refresh", got.PostActions)
	}
	if !strings.Contains(got.Reason, "ackEndpoints=(0,21)->(12,3)") {
		t.Fatalf("reason=%q, want the committed ACK endpoints recorded", got.Reason)
	}

	character := loadTestInventory(t, ctx, repos, "77")
	if _, exists := character.Slots[slotKey(listTypeMain, 21)]; exists {
		t.Fatalf("source row survived: %+v", character.Slots)
	}
	cargo, found, err := repos.AccountInventory.Load(ctx, "dnf:ack-deposit")
	if err != nil || !found {
		t.Fatalf("load cargo found=%t err=%v", found, err)
	}
	if len(cargo.Slots) != 1 || cargo.Slots[slotKey(listTypeAccountCargo, 3)].Count != 12 {
		t.Fatalf("vault rows=%+v, want exactly the merged row (12,3)=12", cargo.Slots)
	}
	if _, exists := cargo.Slots[slotKey(listTypeAccountCargo, 4)]; exists {
		t.Fatalf("requested slot kept an intermediate row: %+v", cargo.Slots)
	}
}

// 取出：账号金库 → 背包空槽，背包里已有同一件可堆叠行。ACK 必须直接回背包那一行。
func TestAccountCargoWithdrawAckProjectsCommittedMergeSlot(t *testing.T) {
	ctx := context.Background()
	repos := dnfrepomemory.NewMemoryGroup()
	extra := stackableExtra("stackable/material/solvent_magicpower.stk", "1000")
	saveTestInventory(t, ctx, repos, dnfrepo.InventoryRecord{
		CharacterID: "77",
		Slots: map[string]dnfrepo.ItemStack{
			slotKey(listTypeMain, 30): {ItemID: 3227, Count: 7, Extra: extra},
		},
	})
	saveTestAccountCargo(t, ctx, repos, "dnf:ack-withdraw", map[string]dnfrepo.ItemStack{
		slotKey(listTypeAccountCargo, 3): {ItemID: 3227, Count: 5, Extra: extra},
	})

	got, err := NewHandler().Handle(ctx, alignedcmd.Request{
		Opcode:              uint16(dnfenum.CmdPacketMoveItemspace),
		Body:                currentMoveItemspaceBody(listTypeAccountCargo, 3, 0, 5, listTypeMain, 25, 0, 0, -1),
		AccountID:           "dnf:ack-withdraw",
		SelectedCharacterID: 77,
		Repositories:        repos,
	})
	if err != nil || !got.ResponseAllowed || len(got.UpperResponses) != 2 {
		t.Fatalf("withdraw result=%+v err=%v", got, err)
	}
	endpoints := decodeMoveAckForTest(t, got.UpperResponses[0].Body)
	if endpoints.sourceList != listTypeAccountCargo || endpoints.sourceSlot != 3 || endpoints.moveCount != 5 {
		t.Fatalf("ACK source=%+v, want the requested vault row (12,3) of 5", endpoints)
	}
	if endpoints.destList != listTypeMain || endpoints.destSlot != 30 {
		t.Fatalf("ACK destination=(%d,%d), want the existing bag row (0,30)", endpoints.destList, endpoints.destSlot)
	}
	character := loadTestInventory(t, ctx, repos, "77")
	if len(character.Slots) != 1 || character.Slots[slotKey(listTypeMain, 30)].Count != 12 {
		t.Fatalf("bag rows=%+v, want one merged row (0,30)=12", character.Slots)
	}
	cargo, _, err := repos.AccountInventory.Load(ctx, "dnf:ack-withdraw")
	if err != nil {
		t.Fatal(err)
	}
	if len(cargo.Slots) != 0 {
		t.Fatalf("vault kept the consumed row: %+v", cargo.Slots)
	}
}

// 个人金库（list 2）：同一份判据，ACK 也必须回角色仓库里已有的那一行。
func TestPersonalCargoDepositAckProjectsCommittedMergeSlot(t *testing.T) {
	ctx := context.Background()
	repos := dnfrepomemory.NewMemoryGroup()
	extra := stackableExtra("stackable/material/solvent_magicpower.stk", "1000")
	saveTestInventory(t, ctx, repos, dnfrepo.InventoryRecord{
		CharacterID: "77",
		Slots: map[string]dnfrepo.ItemStack{
			slotKey(listTypeMain, 21): {ItemID: 3227, Count: 5, Extra: extra},
		},
		Warehouse: map[string]dnfrepo.ItemStack{
			slotKey(listTypePersonalCargo, 3): {ItemID: 3227, Count: 7, Extra: extra},
		},
	})

	got, err := NewHandler().Handle(ctx, alignedcmd.Request{
		Opcode:              uint16(dnfenum.CmdPacketMoveItemspace),
		Body:                currentMoveItemspaceBody(listTypeMain, 21, 0, 5, listTypePersonalCargo, 4, 0, 0, -1),
		AccountID:           "dnf:ack-personal",
		SelectedCharacterID: 77,
		Repositories:        repos,
	})
	if err != nil || !got.ResponseAllowed || len(got.UpperResponses) != 3 {
		t.Fatalf("personal deposit result=%+v err=%v", got, err)
	}
	endpoints := decodeMoveAckForTest(t, got.UpperResponses[0].Body)
	if endpoints.destList != listTypePersonalCargo || endpoints.destSlot != 3 {
		t.Fatalf("ACK destination=(%d,%d), want the existing personal-vault row (2,3)", endpoints.destList, endpoints.destSlot)
	}
	if got.UpperResponses[1].Body[0] != listTypeMain || got.UpperResponses[2].Body[0] != listTypePersonalCargo {
		t.Fatalf("refresh order=%d,%d, want list0 then list2 after the ACK", got.UpperResponses[1].Body[0], got.UpperResponses[2].Body[0])
	}
	if len(got.PostActions) != 0 {
		t.Fatalf("post actions=%v, want none for the character-owned vault", got.PostActions)
	}
	loaded := loadTestInventory(t, ctx, repos, "77")
	if len(loaded.Warehouse) != 1 || loaded.Warehouse[slotKey(listTypePersonalCargo, 3)].Count != 12 {
		t.Fatalf("personal vault rows=%+v, want one merged row (2,3)=12", loaded.Warehouse)
	}
}

// 满额：已有同类堆装不下这次数量时不并堆，物品按请求落进空槽，ACK 保持请求端点。
func TestAccountCargoDepositOverflowKeepsRequestedSlotAck(t *testing.T) {
	ctx := context.Background()
	repos := dnfrepomemory.NewMemoryGroup()
	extra := stackableExtra("stackable/material/solvent_magicpower.stk", "10")
	saveTestInventory(t, ctx, repos, dnfrepo.InventoryRecord{
		CharacterID: "77",
		Slots: map[string]dnfrepo.ItemStack{
			slotKey(listTypeMain, 21): {ItemID: 3227, Count: 9, Extra: extra},
		},
	})
	saveTestAccountCargo(t, ctx, repos, "dnf:ack-overflow", map[string]dnfrepo.ItemStack{
		slotKey(listTypeAccountCargo, 3): {ItemID: 3227, Count: 7, Extra: extra},
	})

	got, err := NewHandler().Handle(ctx, alignedcmd.Request{
		Opcode:              uint16(dnfenum.CmdPacketMoveItemspace),
		Body:                currentMoveItemspaceBody(listTypeMain, 21, 0, 5, listTypeAccountCargo, 4, 0, 0, -1),
		AccountID:           "dnf:ack-overflow",
		SelectedCharacterID: 77,
		Repositories:        repos,
	})
	if err != nil || !got.ResponseAllowed {
		t.Fatalf("overflow deposit result=%+v err=%v", got, err)
	}
	if !strings.Contains(got.Reason, "moveMode=split") {
		t.Fatalf("reason=%q, want the split fallback for a full stack", got.Reason)
	}
	endpoints := decodeMoveAckForTest(t, got.UpperResponses[0].Body)
	if endpoints.destList != listTypeAccountCargo || endpoints.destSlot != 4 || endpoints.moveCount != 5 {
		t.Fatalf("ACK destination=%+v, want the requested slot (12,4) with 5", endpoints)
	}
	cargo, _, err := repos.AccountInventory.Load(ctx, "dnf:ack-overflow")
	if err != nil {
		t.Fatal(err)
	}
	if len(cargo.Slots) != 2 || cargo.Slots[slotKey(listTypeAccountCargo, 3)].Count != 7 || cargo.Slots[slotKey(listTypeAccountCargo, 4)].Count != 5 {
		t.Fatalf("vault rows=%+v, want the full row kept and a new (12,4)=5", cargo.Slots)
	}
	character := loadTestInventory(t, ctx, repos, "77")
	if character.Slots[slotKey(listTypeMain, 21)].Count != 4 {
		t.Fatalf("source remainder=%d, want 4", character.Slots[slotKey(listTypeMain, 21)].Count)
	}
}

// 身份不混叠：同 item id、堆叠身份键不同（这里 stackable_type）的两行不并堆；
// 物品落进请求的空槽，ACK 也不得改端点。
func TestAccountCargoDepositKeepsDistinctStackIdentityOnLiteralSlot(t *testing.T) {
	ctx := context.Background()
	repos := dnfrepomemory.NewMemoryGroup()
	sourceExtra := stackableExtra("stackable/material/solvent_magicpower.stk", "1000")
	sourceExtra["stackable_type"] = "[material]"
	targetExtra := stackableExtra("stackable/material/solvent_magicpower.stk", "1000")
	targetExtra["stackable_type"] = "[quest]"
	saveTestInventory(t, ctx, repos, dnfrepo.InventoryRecord{
		CharacterID: "77",
		Slots: map[string]dnfrepo.ItemStack{
			slotKey(listTypeMain, 21): {ItemID: 3227, Count: 5, Extra: sourceExtra},
		},
	})
	saveTestAccountCargo(t, ctx, repos, "dnf:ack-identity", map[string]dnfrepo.ItemStack{
		slotKey(listTypeAccountCargo, 3): {ItemID: 3227, Count: 7, Extra: targetExtra},
	})

	got, err := NewHandler().Handle(ctx, alignedcmd.Request{
		Opcode:              uint16(dnfenum.CmdPacketMoveItemspace),
		Body:                currentMoveItemspaceBody(listTypeMain, 21, 0, 5, listTypeAccountCargo, 4, 0, 0, -1),
		AccountID:           "dnf:ack-identity",
		SelectedCharacterID: 77,
		Repositories:        repos,
	})
	if err != nil || !got.ResponseAllowed {
		t.Fatalf("identity deposit result=%+v err=%v", got, err)
	}
	endpoints := decodeMoveAckForTest(t, got.UpperResponses[0].Body)
	if endpoints.destList != listTypeAccountCargo || endpoints.destSlot != 4 {
		t.Fatalf("ACK destination=%+v, want the requested slot (12,4)", endpoints)
	}
	cargo, _, err := repos.AccountInventory.Load(ctx, "dnf:ack-identity")
	if err != nil {
		t.Fatal(err)
	}
	if cargo.Slots[slotKey(listTypeAccountCargo, 3)].Count != 7 || cargo.Slots[slotKey(listTypeAccountCargo, 4)].Count != 5 {
		t.Fatalf("vault rows=%+v, want distinct identities kept apart", cargo.Slots)
	}
}

// 拒绝回滚：金库侧落库失败时一个字节都不改，也不回成功包、不发金库刷新。
func TestAccountCargoDepositFailureKeepsCommittedAckClosedAndRollsBack(t *testing.T) {
	ctx := context.Background()
	repos := dnfrepomemory.NewMemoryGroup()
	extra := stackableExtra("stackable/material/solvent_magicpower.stk", "1000")
	saveTestInventory(t, ctx, repos, dnfrepo.InventoryRecord{
		CharacterID: "77",
		Slots: map[string]dnfrepo.ItemStack{
			slotKey(listTypeMain, 21): {ItemID: 3227, Count: 5, Extra: extra},
		},
	})
	saveTestAccountCargo(t, ctx, repos, "dnf:ack-rollback", map[string]dnfrepo.ItemStack{
		slotKey(listTypeAccountCargo, 3): {ItemID: 3227, Count: 7, Extra: extra},
	})
	repos.AccountItems = persistenceFailUOW{repos.AccountItems}

	got, err := NewHandler().Handle(ctx, alignedcmd.Request{
		Opcode:              uint16(dnfenum.CmdPacketMoveItemspace),
		Body:                currentMoveItemspaceBody(listTypeMain, 21, 0, 5, listTypeAccountCargo, 4, 0, 0, -1),
		AccountID:           "dnf:ack-rollback",
		SelectedCharacterID: 77,
		Repositories:        repos,
	})
	if err != nil {
		t.Fatal(err)
	}
	if got.ResponseAllowed || len(got.UpperResponses) != 0 || len(got.PostActions) != 0 {
		t.Fatalf("failed vault save still answered: %+v", got)
	}
	if !strings.Contains(got.Reason, "禁止回成功包") {
		t.Fatalf("reason=%q, want the blocked-ACK marker", got.Reason)
	}
	character := loadTestInventory(t, ctx, repos, "77")
	if character.Slots[slotKey(listTypeMain, 21)].Count != 5 {
		t.Fatalf("failed deposit changed the bag: %+v", character.Slots)
	}
	cargo, _, err := repos.AccountInventory.Load(ctx, "dnf:ack-rollback")
	if err != nil {
		t.Fatal(err)
	}
	if len(cargo.Slots) != 1 || cargo.Slots[slotKey(listTypeAccountCargo, 3)].Count != 7 {
		t.Fatalf("failed deposit changed the vault: %+v", cargo.Slots)
	}
}

// 闸门：非金库端点（背包内自动并堆）的 ACK 端点逐字节保持请求端点。
func TestOwnerAppliedMoveAckLeavesNonVaultAutoStackUnchanged(t *testing.T) {
	cmd := Command{
		SelectedCharacterID:  77,
		SourceListType:       listTypeMain,
		SourceSlotIndex:      65,
		MoveCount:            3,
		DestinationListType:  listTypeMain,
		DestinationSlotIndex: 80,
	}
	result := MoveResult{
		CharacterID:          "77",
		SourceListType:       listTypeMain,
		SourceSlotIndex:      65,
		DestinationListType:  listTypeMain,
		DestinationSlotIndex: 70,
		MoveCount:            3,
		Mode:                 "auto_stack",
		Changed:              true,
	}
	got := ownerAppliedMoveResult(context.Background(), nil, uint16(dnfenum.CmdPacketMoveItemspace), cmd, result)
	if !got.ResponseAllowed || len(got.UpperResponses) != 1 {
		t.Fatalf("result=%+v, want the bare ACK", got)
	}
	endpoints := decodeMoveAckForTest(t, got.UpperResponses[0].Body)
	if endpoints.destList != listTypeMain || endpoints.destSlot != 80 {
		t.Fatalf("ACK destination=%+v, want the requested bag slot (0,80) unchanged", endpoints)
	}
	if !strings.Contains(got.Reason, "ackEndpoints=(0,65)->(0,80)") {
		t.Fatalf("reason=%q, want the requested endpoints recorded", got.Reason)
	}
}
