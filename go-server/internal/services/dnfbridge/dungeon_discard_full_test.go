package dnfbridge

import (
	"bytes"
	"context"
	"encoding/binary"
	"fmt"
	"os"
	"reflect"
	"strconv"
	"testing"
	"time"

	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	platformpvf "longheng.io/server/internal/platform/pvf"
)

func TestCurrentDungeonDiscardFullBagAnswersAndRetriesOtherSlot(t *testing.T) {
	for _, tc := range []struct {
		name         string
		equipment    bool
		source, free int16
		amount       uint32
	}{
		{"现场快捷栏装备", true, 6, 49, 1},
		{"材料原格已满", false, 137, 149, 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			service, runtime, repositories := prepareCurrentDungeonDropTest(t, 0, 3227)
			prepareDungeonPickupCapacity(t, repositories, 24, 64)
			inventory, _, err := repositories.Inventory.Load(ctx, "99")
			if err != nil {
				t.Fatal(err)
			}
			original := currentDungeonDiscardTestStack(3227, 5)
			if tc.equipment {
				original = dnfrepo.ItemStack{ItemID: 9001, Count: 1, Extra: map[string]string{"item_kind": "equipment", "quality_seed": "305419896", "durability": "45", "reinforce_level": "7"}}
			} else {
				for slot := 121; slot <= 176; slot++ {
					inventory.Slots[fmt.Sprintf("0:%d", slot)] = currentDungeonDiscardTestStack(3227, 999)
				}
				for slot := 3; slot <= 8; slot++ {
					inventory.Slots[fmt.Sprintf("0:%d", slot)] = currentDungeonDiscardTestStack(3227, 999)
				}
			}
			original.ExpireAt = time.Now().Add(time.Hour).UTC().Truncate(time.Second)
			entry := currentItemListEntryFromStack(0, tc.source, original)
			original.RawEntry = append([]byte(nil), entry.data[:]...)
			sourceKey, freeKey := fmt.Sprintf("0:%d", tc.source), fmt.Sprintf("0:%d", tc.free)
			inventory.Slots[sourceKey] = original
			if err := repositories.Inventory.Save(ctx, inventory); err != nil {
				t.Fatal(err)
			}
			connection := &bufferConn{}
			session := &gameSession{conn: connection, selectedCharacterID: 99, dungeon: dungeonSessionState{runtime: runtime}}
			handled, err := service.handleCurrentDungeonDiscard(session, buildCurrentDungeonDiscardTestRequest(248, 276, 0, uint16(tc.source), tc.amount, 0))
			if !handled || err != nil {
				t.Fatalf("丢弃失败：%t %v", handled, err)
			}
			scene, _ := splitGameServerUpperPacket(t, connection.write.Bytes())
			key := binary.LittleEndian.Uint32(scene.Body[6:10])
			// 用户丢出物品后把原格放入其他物品，此时合法分类页和快捷栏都已占满。
			inventory, _, err = repositories.Inventory.Load(ctx, "99")
			if err != nil {
				t.Fatal(err)
			}
			if tc.equipment {
				inventory.Slots[sourceKey] = dnfrepo.ItemStack{ItemID: 9001, Count: 1, Extra: map[string]string{"quality_seed": "987654"}}
			} else {
				inventory.Slots[sourceKey] = currentDungeonDiscardTestStack(3227, 999)
			}
			if err := repositories.Inventory.Save(ctx, inventory); err != nil {
				t.Fatal(err)
			}
			before := dnfrepo.CloneInventory(inventory)
			connection.write.Reset()
			request := currentDungeonPickupTestBody(key)
			if err := service.handleCurrentDungeonPickup(session, request); err != nil {
				t.Fatal(err)
			}
			if connection.write.Len() == 0 {
				t.Fatal("满包拾取没有返回提示及等待复位应答")
			}
			ack, tail := splitGameServerUpperPacket(t, connection.write.Bytes())
			if ack.Header.MsgID != 43 || ack.Header.Classification != 1 || !bytes.Equal(ack.Body, []byte{0, 4}) || len(tail) != 0 {
				t.Fatalf("满包应答不符：%+v %x", ack.Header, ack.Body)
			}
			after, _, err := repositories.Inventory.Load(ctx, "99")
			if err != nil || !reflect.DeepEqual(before, after) || runtime.DropOwner.byObjectKey[key].Status != runtimeDungeonDropAvailable {
				t.Fatal("满包拒绝改变了库存或消耗了地面物品")
			}
			delete(after.Slots, freeKey)
			if err := repositories.Inventory.Save(ctx, after); err != nil {
				t.Fatal(err)
			}
			connection.write.Reset()
			if err := service.handleCurrentDungeonPickup(session, request); err != nil {
				t.Fatal(err)
			}
			pickup, rest := splitGameServerUpperPacket(t, connection.write.Bytes())
			update, tail := splitGameServerUpperPacket(t, rest)
			if pickup.Header.MsgID != 39 || binary.LittleEndian.Uint16(pickup.Body[16:18]) != uint16(tc.free) || update.Header.MsgID != 14 || len(tail) != 0 {
				t.Fatalf("清格后没有拾取到合法新格：%x", pickup.Body)
			}
			after, _, err = repositories.Inventory.Load(ctx, "99")
			got := after.Slots[freeKey]
			if err != nil || got.ItemID != original.ItemID || got.Count != int64(tc.amount) || !reflect.DeepEqual(got.Extra, original.Extra) || !got.ExpireAt.Equal(original.ExpireAt) || !reflect.DeepEqual(after.Slots[sourceKey], before.Slots[sourceKey]) {
				t.Fatalf("重拾改变原实例属性或覆盖原格：%+v", got)
			}
			if tc.equipment && binary.LittleEndian.Uint32(got.RawEntry[6:10]) != 305419896 {
				t.Fatal("装备品质实例值没有保留")
			}
			count := len(after.Slots)
			connection.write.Reset()
			if err := service.handleCurrentDungeonPickup(session, request); err != nil {
				t.Fatal(err)
			}
			after, _, err = repositories.Inventory.Load(ctx, "99")
			if err != nil || len(after.Slots) != count || after.Slots[freeKey].Count != int64(tc.amount) {
				t.Fatal("重复拾取再次发放了物品")
			}
		})
	}
}

func TestRealScriptPVFDungeonEquipmentPickupCapacityMatrix(t *testing.T) {
	path := os.Getenv("DNFBRIDGE_REAL_PVF_SMOKE")
	if path == "" {
		t.Skip("未指定真实PVF")
	}
	archive, err := platformpvf.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	catalog, err := newPVFDungeonDropCatalog(archive)
	if err != nil {
		t.Fatal(err)
	}
	for _, itemID := range []uint32{20001, 35003, 100352903} {
		definition, err := catalog.ResolveItem(itemID)
		if err != nil || definition.Kind != dungeonDropItemEquipment {
			t.Fatalf("真实装备定义 %d：%+v %v", itemID, definition, err)
		}
		for _, tc := range []struct {
			name                string
			expansion, occupied int
			want, free          uint16
		}{
			{"基础容量有空格", 0, 8, 9, 0},
			{"基础满包及重试", 0, 40, 0, 40},
			{"扩容可用格", 24, 48, 49, 0},
			{"扩容满包及重试", 24, 64, 0, 49},
		} {
			t.Run(strconv.FormatUint(uint64(itemID), 10)+"/"+tc.name, func(t *testing.T) {
				service, runtime, repositories := prepareCurrentDungeonDropTest(t, 1, 3227)
				prepareDungeonPickupCapacity(t, repositories, tc.expansion, tc.occupied)
				key := seedCurrentDungeonPickupDrop(t, service, runtime, 9001, currentSceneActorObjectKey(99))
				drop := runtime.DropOwner.byObjectKey[key]
				drop.Item = definition
				drop.QualitySeed = 305419896
				assertDungeonPickupCapacityResult(t, service, runtime, repositories, key, tc.want, itemID)
				want := tc.want
				if tc.free != 0 {
					record, _, err := repositories.Inventory.Load(context.Background(), "99")
					if err != nil {
						t.Fatal(err)
					}
					delete(record.Slots, fmt.Sprintf("0:%d", tc.free))
					if err := repositories.Inventory.Save(context.Background(), record); err != nil {
						t.Fatal(err)
					}
					want = tc.free
					assertDungeonPickupCapacityResult(t, service, runtime, repositories, key, want, itemID)
				}
				record, _, err := repositories.Inventory.Load(context.Background(), "99")
				stack := record.Slots[fmt.Sprintf("0:%d", want)]
				if err != nil || len(stack.RawEntry) != currentItemListEntryWireSize || binary.LittleEndian.Uint32(stack.RawEntry[6:10]) != drop.QualitySeed {
					t.Fatalf("正常装备拾取的原始实例行不符：%+v", stack)
				}
			})
		}
	}
}
