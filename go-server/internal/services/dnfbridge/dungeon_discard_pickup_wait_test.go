package dnfbridge

import (
	"context"
	"encoding/binary"
	"fmt"
	"testing"

	dnfproto "longheng.io/server/internal/modules/dnf/protocol"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

// 丢弃回包即使被当前客户端过滤，最后的权威主背包重建仍须结清物品操作等待。
// 同时覆盖失败/未知模式：不能生成地面对象，不能把原有物品扣掉。
func TestCurrentDungeonDiscardRestoresPickupAdmission(t *testing.T) {
	for _, test := range []struct {
		name          string
		itemID        int64
		count         int64
		slot          uint16
		mode          byte
		bound         bool
		wantRemaining uint32
		wantDrop      bool
	}{
		{name: "装备整件丢弃", itemID: 9001, count: 1, slot: 43, wantDrop: true},
		{name: "材料部分丢弃", itemID: 3227, count: 5, slot: 138, wantRemaining: 4, wantDrop: true},
		{name: "绑定物品拒绝", itemID: 3227, count: 5, slot: 138, bound: true, wantRemaining: 5},
		{name: "未知丢弃模式拒绝", itemID: 3227, count: 5, slot: 138, mode: 1, wantRemaining: 5},
	} {
		t.Run(test.name, func(t *testing.T) {
			service, runtime, repositories := prepareCurrentDungeonDropTest(t, 0, 3227)
			if test.itemID == 9001 {
				prepareDungeonPickupCapacity(t, repositories, 24, 0)
			}
			stack := currentDungeonDiscardTestStack(test.itemID, test.count)
			stack.Bind = test.bound
			if test.itemID == 9001 {
				stack.Extra = map[string]string{"item_kind": string(dungeonDropItemEquipment), "equipment_type": "[weapon]", "quality_seed": "123456", "durability": "45"}
				entry := currentItemListEntryFromStack(0, 43, stack)
				stack.RawEntry = append([]byte(nil), entry.data[:]...)
			}
			inventory, _, err := repositories.Inventory.Load(context.Background(), "99")
			if err != nil {
				t.Fatal(err)
			}
			if inventory.Slots == nil {
				inventory.Slots = make(map[string]dnfrepo.ItemStack)
			}
			slotKey := fmt.Sprintf("0:%d", test.slot)
			inventory.Slots[slotKey] = stack
			if err := repositories.Inventory.Save(context.Background(), inventory); err != nil {
				t.Fatal(err)
			}
			connection := &bufferConn{}
			session := &gameSession{conn: connection, connID: "discard-pickup-wait", selectedCharacterID: 99, dungeon: dungeonSessionState{runtime: runtime}}
			handled, err := service.handleCurrentDungeonDiscard(session, buildCurrentDungeonDiscardTestRequest(248, 276, 0, test.slot, 1, test.mode))
			if err != nil || !handled {
				t.Fatalf("丢弃处理=%t，错误=%v", handled, err)
			}
			var frames []dnfproto.ChannelPacket
			for raw := connection.write.Bytes(); len(raw) > 0; {
				frame, rest := splitGameServerUpperPacket(t, raw)
				frames = append(frames, frame)
				raw = rest
			}
			if len(frames) == 0 {
				t.Fatal("缺少解除物品等待所需的权威主背包通知")
			}
			last := frames[len(frames)-1]
			if last.Header.MsgID != 13 || last.Header.Classification != 0 {
				t.Fatalf("末包不是完整主背包通知：%+v", last.Header)
			}
			rows := parseCurrentMainInventoryContainerRows(t, last)
			row := currentItemListRefreshRow{Slot: int16(test.slot)}
			for _, candidate := range rows {
				if candidate.Slot == int16(test.slot) {
					row = candidate
				}
			}
			if test.wantRemaining > 0 && (row.ItemID != uint32(test.itemID) || row.Count != test.wantRemaining) {
				t.Fatalf("刷新没有保留事务后的库存：%+v", row)
			}
			if test.wantRemaining == 0 && row.ItemID != 0 {
				t.Fatalf("整件丢弃后仍有原装备：%+v", row)
			}
			if !test.wantDrop {
				if len(frames) != 1 || runtime.DropOwner != nil {
					t.Fatalf("拒绝丢弃却生成了地面对象或成功通知：%d", len(frames))
				}
				return
			}
			if len(frames) != 4 || frames[0].Header.MsgID != currentDungeonDiscardSceneOpcode {
				t.Fatalf("成功丢弃回包数=%d", len(frames))
			}
			key := binary.LittleEndian.Uint32(frames[0].Body[6:10])
			connection.write.Reset()
			if err := service.handleCurrentDungeonPickup(session, currentDungeonPickupTestBody(key)); err != nil {
				t.Fatal(err)
			}
			inventory, _, err = repositories.Inventory.Load(context.Background(), "99")
			if err != nil || inventory.Slots[slotKey].ItemID != test.itemID || inventory.Slots[slotKey].Count != test.count {
				t.Fatalf("重新拾取没有完整恢复原物品：%+v，错误=%v", inventory.Slots[slotKey], err)
			}
		})
	}
}
