package dnfbridge

import (
	"context"
	"errors"
	"strings"
	"testing"

	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

// 本文件钉住操作者缺陷「**单人挑战深渊排队 通关后无法结算**」的根因修复。
//
// 现场（`packet_log.txt`，`conn_id=game-000024` char6 单人深渊 310）：
//
//	17:14:08–09 七条 `game-dungeon-pickup-accepted epic_piece=true destination_slot=64`
//	17:14:52.885 op115 + **op31**（走到 Boss 房、通关、结算入口已发）
//	17:14:52.924 客户端 op46
//	17:14:54.680 `game-dungeon-settlement-plan-blocked … error=dungeon settlement persisted
//	              state is invalid`   ← 结算计划整条被挡 ⇒ 没有 op34/op37/op35、没有翻牌界面
//
// 根因：碎片按设计**不占背包槽位**（只入账号级图鉴账本、目的槽位写哨兵 64），
// 而结算的"已拾取掉落收据"校验要求每一条已消耗掉落都能在背包里按目的槽位找到同一件
// 物品 ⇒ 只要这一局捡过碎片，整局结算计划就失败。全日志 6 条 `…-plan-blocked`
// **全部**是深渊局，26 条 `…-plan-frozen` 全是普通局。

// abyssSettlementReceiptFixtureRuntime 用真档形状的深渊 fixture 建一份运行时
// （会话/场景/房间都在），再往里塞**两条已消耗掉落**：
// 一条账号级史诗碎片（目的槽位 = 哨兵 64、不在背包里）、一条普通背包掉落（在背包里）。
func abyssSettlementReceiptFixtureRuntime(
	t *testing.T,
) (*Service, *gameSession, *runtimeDungeonState, dnfrepo.Group, *bufferConn) {
	t.Helper()
	service, session, conn, runtime, _, repositories := prepareAbyssEpicFragmentRuntime(
		t, bridgeAbyssEpicFragmentPVF(),
	)
	owner := newRuntimeDungeonDropOwner()
	const inventorySlot = uint16(7)
	const inventoryItem = uint32(9001)
	owner.byObjectKey[444] = &runtimeDungeonDrop{
		ObjectKey:        444,
		SceneSlot:        4,
		Item:             dungeonDropItemDefinition{ItemID: abyssEpicFragmentTestPieceA},
		Amount:           1,
		Status:           runtimeDungeonDropConsumed,
		DestinationSlot:  currentEpicPiecePickupEffectSlot,
		EpicPieceBalance: 1,
	}
	owner.byObjectKey[445] = &runtimeDungeonDrop{
		ObjectKey:       445,
		SceneSlot:       5,
		Item:            dungeonDropItemDefinition{ItemID: inventoryItem},
		Amount:          1,
		Status:          runtimeDungeonDropConsumed,
		DestinationSlot: inventorySlot,
	}
	runtime.DropOwner = owner
	if err := repositories.Inventory.Save(context.Background(), dnfrepo.InventoryRecord{
		CharacterID: "99",
		Slots: map[string]dnfrepo.ItemStack{
			currentDungeonPickupMainSlotKey(int16(inventorySlot)): {
				ItemID: int64(inventoryItem),
				Count:  1,
			},
		},
	}); err != nil {
		t.Fatal(err)
	}
	return service, session, runtime, repositories, conn
}

// TestAbyssSettlementExemptsTheAccountLedgerFragmentReceipt 是缺陷 (2) 的判别力用例：
// 同一条已消耗掉落账本，默认档给出可用收据（只含背包那一条）且**不报错**；
// 回退档逐字节复现线上那条失败（`dungeon settlement persisted state is invalid`）。
func TestAbyssSettlementExemptsTheAccountLedgerFragmentReceipt(t *testing.T) {
	logger, logPath := testPassiveObjectDropPacketLogger(t)
	service, session, runtime, repositories, _ := abyssSettlementReceiptFixtureRuntime(t)
	service.packetLog = logger
	inventory, found, err := repositories.Inventory.Load(context.Background(), "99")
	if err != nil || !found {
		t.Fatalf("load inventory found=%t err=%v", found, err)
	}

	t.Setenv(abyssSettlementFragmentReceiptEnv, "")
	drops, exempted, err := currentDungeonCommittedDropReceiptsWithExemptions(runtime, inventory, true)
	if err != nil {
		t.Fatalf("the shipped tier must not fail on an account-ledger receipt: %v", err)
	}
	if len(exempted) != 1 || exempted[0].ItemID != abyssEpicFragmentTestPieceA ||
		exempted[0].DestinationSlot != currentEpicPiecePickupEffectSlot {
		t.Fatalf("exempted receipts=%+v want exactly the epic-piece pickup", exempted)
	}
	if len(drops) != 1 || drops[0].TemplateID != 9001 {
		t.Fatalf("the inventory-backed receipt must survive: %+v", drops)
	}
	// 会话入口（会写事件）在同一条账本上同样成功。
	if _, err := service.currentDungeonCommittedDropReceiptsLocked(session, runtime, inventory); err != nil {
		t.Fatalf("the session entry point must succeed: %v", err)
	}
	// 回退档：逐字节复现线上那条失败。
	t.Setenv(abyssSettlementFragmentReceiptEnv, abyssSettlementFragmentReceiptLegacyValue)
	if _, err := currentDungeonCommittedDropReceipts(runtime, inventory); !errors.Is(
		err, errCurrentDungeonSettlementStateInvalid,
	) {
		t.Fatalf("the legacy tier must reproduce the deployed failure, got err=%v", err)
	}
	if _, err := service.currentDungeonCommittedDropReceiptsLocked(session, runtime, inventory); !errors.Is(
		err, errCurrentDungeonSettlementStateInvalid,
	) {
		t.Fatalf("the legacy session entry point must reproduce the deployed failure, got err=%v", err)
	}
	// 真正损坏的状态（目的槽位 0）在两档下都必须继续失败 —— 豁免不是"放过一切"。
	t.Setenv(abyssSettlementFragmentReceiptEnv, "")
	broken := newRuntimeDungeonDropOwner()
	broken.byObjectKey[446] = &runtimeDungeonDrop{
		ObjectKey: 446,
		Item:      dungeonDropItemDefinition{ItemID: 9002},
		Amount:    1,
		Status:    runtimeDungeonDropConsumed,
	}
	brokenRuntime := *runtime
	brokenRuntime.DropOwner = broken
	if _, _, err := currentDungeonCommittedDropReceiptsWithExemptions(&brokenRuntime, inventory, true); !errors.Is(
		err, errCurrentDungeonSettlementStateInvalid,
	) {
		t.Fatalf("a zero destination slot must still fail closed, got err=%v", err)
	}

	logged := closePassiveObjectDropPacketLogger(t, logger, logPath)
	if !strings.Contains(logged, "game-dungeon-settlement-drop-receipt-exempt") {
		t.Fatalf("the exemption must be grep-able:\n%s", logged)
	}
	for _, field := range []string{
		"room_type=", "boss_room=", "boss_coordinate_matched=", "decision=",
		"asset_owner=account_epic_piece_metadata_counter", "destination_slot=64",
	} {
		if !strings.Contains(logged, field) {
			t.Fatalf("the exemption event is missing %q:\n%s", field, logged)
		}
	}
	if strings.Contains(logged, "game-dungeon-settlement-drop-receipt-exempt") &&
		strings.Contains(logged, "switch=legacy") {
		t.Fatal("the legacy tier must not write an exemption event")
	}
}

// TestAbyssBossRoomClearProducesUsableSettlementReceipts 是端到端那一半：
// 一局深渊（深渊房清空 **不** 结算）→ 走到 Boss 房清空 → **恰好一条 op31**，
// 且此刻结算读掉落收据**不再被碎片挡住**（现场那条 `…-plan-blocked` 不再出现）。
func TestAbyssBossRoomClearProducesUsableSettlementReceipts(t *testing.T) {
	logger, logPath := testPassiveObjectDropPacketLogger(t)
	service, session, conn, runtime, _ := prepareHellPartyClearSettlementRuntime(t)
	service.packetLog = logger
	collected := make([]byte, 0, 4096)

	// 深渊房：房图怪 + 全部波次怪。默认档下**不得**结算。
	conn.write.Reset()
	killEntireHellRoomAndWaves(t, service, session, runtime)
	collected = append(collected, conn.write.Bytes()...)
	if count := settlementEntryPackets(t, conn.write.Bytes()); count != 0 {
		t.Fatalf("the hell room must never settle: op31 count=%d", count)
	}

	// Boss 房：走到 1:0 并清空。
	conn.write.Reset()
	moveDungeonSessionToRoomForTest(t, service, session, 1, 0)
	collected = append(collected, conn.write.Bytes()...)
	conn.write.Reset()
	killDungeonActors(t, service, session, currentRoomMonsterKeys(t, runtime)...)
	collected = append(collected, conn.write.Bytes()...)
	if count := settlementEntryPackets(t, collected); count != 1 {
		t.Fatalf("a full abyss run must settle exactly once: op31 count=%d", count)
	}

	// 结算读收据：这一局没有任何碎片掉落（普通档被 rarity 闸门挡下），所以这里是
	// "没有碎片也要照常结算"的对照；碎片那一半由上一个用例逐个字段钉住。
	repositories, ok := service.repositoryProvider()
	if !ok {
		t.Fatal("memory repository group is missing")
	}
	inventory, found, err := repositories.Inventory.Load(context.Background(), "99")
	if err != nil {
		t.Fatalf("load inventory: %v", err)
	}
	if !found {
		inventory = dnfrepo.InventoryRecord{CharacterID: "99", Slots: map[string]dnfrepo.ItemStack{}}
	}
	drops, err := service.currentDungeonCommittedDropReceiptsLocked(session, runtime, inventory)
	if err != nil {
		t.Fatalf("the settlement receipt read must succeed at the boss room: %v", err)
	}
	if len(drops) != 0 {
		t.Fatalf("no pickup was made in this run, receipts=%+v", drops)
	}
	logged := closePassiveObjectDropPacketLogger(t, logger, logPath)
	if strings.Contains(logged, "game-dungeon-settlement-plan-blocked") {
		t.Fatalf("the settlement plan must not be blocked:\n%s", logged)
	}
}
