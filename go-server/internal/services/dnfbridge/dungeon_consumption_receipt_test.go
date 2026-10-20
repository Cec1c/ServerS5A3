package dnfbridge

import (
	"context"
	"errors"
	"testing"

	"longheng.io/server/internal/modules/dnf/alignedcmd"
	"longheng.io/server/internal/modules/dnf/dnfenum"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

// 复现现场：拾取物品1066后正常用完，结算不能再要求背包里保留该物品。
func TestDungeonConsumptionNormalUseThenSettlement(t *testing.T) {
	for _, test := range []struct {
		name  string
		slot  int16
		count int64
	}{
		{"原格用完", 7, 1},
		{"移到物品栏后用完", 65, 1},
		{"部分使用仍保留一件", 7, 2},
	} {
		t.Run(test.name, func(t *testing.T) {
			service, runtime, session, conn, _ := prepareCompletedSettlementRuntime(t)
			repos, _ := service.repositoryGroup()
			ctx := context.Background()
			character, _, _ := repos.Character.Load(ctx, "99")
			character.Level = 1
			character.Stats["exp"] = 90
			if err := repos.Character.Save(ctx, character); err != nil {
				t.Fatal(err)
			}
			runtime.Character = character
			if err := repos.Skill.Save(ctx, dnfrepo.SkillRecord{CharacterID: "99", Points: dnfrepo.SkillPointState{TotalSP: 10, RemainingSP: 10, SyncedLevel: 1}}); err != nil {
				t.Fatal(err)
			}
			key := currentDungeonPickupMainSlotKey(test.slot)
			if err := repos.Inventory.Save(ctx, dnfrepo.InventoryRecord{CharacterID: "99", Slots: map[string]dnfrepo.ItemStack{
				key: {ItemID: 1066, Count: test.count, Extra: map[string]string{"item_kind": "stackable", "pvf_path": "stackable/aroma_elf.stk", "stackable_type": "[waste]"}},
			}}); err != nil {
				t.Fatal(err)
			}
			runtime.DropOwner = newRuntimeDungeonDropOwner()
			drop := &runtimeDungeonDrop{ObjectKey: 408, OwnerActorObjectKey: 99, Item: dungeonDropItemDefinition{ItemID: 1066}, Amount: uint32(test.count), Status: runtimeDungeonDropConsumed, DestinationSlot: 7}
			drop.PickupResponseBody, _ = buildCurrentDungeonNormalPickupResultBody(408, 99, 7)
			runtime.DropOwner.byObjectKey[408] = drop
			conn.write.Reset()
			if err := service.handleGameCommand(session, byte(dnfenum.GameCmdCommand), uint16(dnfenum.CmdPacketUseStackable), buildRejectedUseStackableBody(test.slot, 0, 0x16f7, 1066)); err != nil {
				t.Fatal(err)
			}
			after, _, err := repos.Inventory.Load(ctx, "99")
			if err != nil || after.Slots[key].Count != test.count-1 {
				t.Fatalf("消耗数量未正常提交：%+v %v", after, err)
			}
			runtime.Dungeon.Metadata.BasisLevel.Set, runtime.Dungeon.Metadata.BasisLevel.Value = true, 1
			runtime.Dungeon.Metadata.ExperienceIncreasingPoint.Set, runtime.Dungeon.Metadata.ExperienceIncreasingPoint.Value = true, 1
			runtime.Request.Difficulty = 1
			runtime.settlementPlayResultReceived = true
			runtime.settlementPlayResultBody = make([]byte, currentDungeonPlayResultBaseSize)
			runtime.settlementPlayResultBody[currentDungeonPlayResultClientRankPointOffset] = 73
			if err := service.produceCurrentDungeonSettlementPlanWithResourcesLocked(ctx, session, runtime, currentDungeonSettlementTestResources(t)); err != nil {
				t.Fatalf("合法使用消耗品后结算被拒绝：%v", err)
			}
			if runtime.settlementResultPlan == nil {
				t.Fatal("结算计划没有生成")
			}
			if _, exists := after.Slots[key]; test.count == 1 && exists {
				t.Fatal("已经用完的物品被结算重新加入背包")
			}
			drops, err := service.currentDungeonCommittedDropReceiptsLocked(session, runtime, after)
			want := 0
			if test.count > 1 {
				want = 1
			}
			if err != nil || len(drops) != want {
				t.Fatalf("结算掉落投影=%+v err=%v", drops, err)
			}
			runtime.settlementStatisticReceived = true
			conn.write.Reset()
			if err := service.sendCurrentDungeonSettlementResultsLocked(session, runtime, "消耗品结算回归"); err != nil {
				t.Fatal(err)
			}
			if !completedDungeonSettlementResultReady(runtime, 99) {
				t.Fatal("结算结果发出后，回城就绪条件仍被阻断")
			}
		})
	}
}

func TestDungeonConsumptionDoesNotAcceptMissingOrCorruptProof(t *testing.T) {
	for _, name := range []string{"无使用凭证", "错误角色", "错误物品", "数量未用完", "数量非法", "错误拾取者", "损坏拾取回包"} {
		t.Run(name, func(t *testing.T) {
			drop := &runtimeDungeonDrop{ObjectKey: 408, Item: dungeonDropItemDefinition{ItemID: 1066}, Amount: 1, Status: runtimeDungeonDropConsumed, DestinationSlot: 7}
			drop.PickupResponseBody, _ = buildCurrentDungeonNormalPickupResultBody(408, 99, 7)
			drop.ConsumptionReceipt = &alignedcmd.StackableConsumption{CharacterID: "99", ListType: 0, SlotIndex: 7, ItemID: 1066, PreviousCount: 1, RemainingCount: 0}
			switch name {
			case "无使用凭证":
				drop.ConsumptionReceipt = nil
			case "错误角色":
				drop.ConsumptionReceipt.CharacterID = "98"
			case "错误物品":
				drop.ConsumptionReceipt.ItemID = 1067
			case "数量未用完":
				drop.ConsumptionReceipt.RemainingCount = 1
			case "数量非法":
				drop.ConsumptionReceipt.PreviousCount = 0
			case "错误拾取者":
				drop.PickupResponseBody, _ = buildCurrentDungeonNormalPickupResultBody(408, 98, 7)
			case "损坏拾取回包":
				drop.PickupResponseBody[0] ^= 1
			}
			runtime := &runtimeDungeonState{DropOwner: newRuntimeDungeonDropOwner()}
			runtime.DropOwner.byObjectKey[408] = drop
			if _, err := currentDungeonCommittedDropReceipts(runtime, dnfrepo.InventoryRecord{CharacterID: "99", Slots: map[string]dnfrepo.ItemStack{}}); !errors.Is(err, errCurrentDungeonSettlementStateInvalid) {
				t.Fatalf("异常丢失或凭证损坏被放行：%v", err)
			}
		})
	}
}

type consumptionWriteFailureConn struct{ bufferConn }

func (c *consumptionWriteFailureConn) Write([]byte) (int, error) {
	return 0, errors.New("测试回包写入失败")
}

func TestDungeonConsumptionCommitSurvivesAckFailureAndRejectsFailedUse(t *testing.T) {
	for _, failedUse := range []bool{false, true} {
		service, runtime, session, _, _ := prepareCompletedSettlementRuntime(t)
		repos, _ := service.repositoryGroup()
		inventory := dnfrepo.InventoryRecord{CharacterID: "99", Slots: map[string]dnfrepo.ItemStack{"0:7": {ItemID: 1066, Count: 1, Extra: map[string]string{"item_kind": "stackable", "pvf_path": "stackable/aroma_elf.stk", "stackable_type": "[waste]"}}}}
		if err := repos.Inventory.Save(context.Background(), inventory); err != nil {
			t.Fatal(err)
		}
		drop := &runtimeDungeonDrop{ObjectKey: 408, Item: dungeonDropItemDefinition{ItemID: 1066}, Amount: 1, Status: runtimeDungeonDropConsumed, DestinationSlot: 7}
		drop.PickupResponseBody, _ = buildCurrentDungeonNormalPickupResultBody(408, 99, 7)
		runtime.DropOwner = newRuntimeDungeonDropOwner()
		runtime.DropOwner.byObjectKey[408] = drop
		itemID := int32(1066)
		if failedUse {
			itemID++
		} else {
			session.conn = &consumptionWriteFailureConn{}
		}
		err := service.handleGameCommand(session, byte(dnfenum.GameCmdCommand), uint16(dnfenum.CmdPacketUseStackable), buildRejectedUseStackableBody(7, 0, 0x16f7, itemID))
		after, _, loadErr := repos.Inventory.Load(context.Background(), "99")
		if loadErr != nil {
			t.Fatal(loadErr)
		}
		if failedUse {
			if after.Slots["0:7"].Count != 1 || drop.ConsumptionReceipt != nil {
				t.Fatal("被拒绝的使用产生了扣物品或消耗凭证")
			}
		} else {
			if err == nil || after.Slots["0:7"].Count != 0 || drop.ConsumptionReceipt == nil {
				t.Fatalf("回包失败丢失真实提交凭证：err=%v 库存=%+v", err, after)
			}
			if _, err := currentDungeonCommittedDropReceipts(runtime, after); err != nil {
				t.Fatalf("真实使用已提交仍拒绝结算：%v", err)
			}
		}
	}
}
