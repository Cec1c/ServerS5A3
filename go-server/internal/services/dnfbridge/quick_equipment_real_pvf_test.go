package dnfbridge

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"reflect"
	"testing"

	"longheng.io/server/internal/modules/dnf/dnfenum"
	r "longheng.io/server/internal/modules/dnf/repository"
	platformpvf "longheng.io/server/internal/platform/pvf"
)

func TestRealPVFQuickEquipmentSingle(t *testing.T) {
	path := os.Getenv("DNFBRIDGE_REAL_PVF_SMOKE")
	if path == "" {
		t.Skip("设置真实PVF路径后验证两件现场快捷装备")
	}
	archive, err := platformpvf.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	items, err := newPVFDungeonDropCatalog(archive)
	if err != nil {
		t.Fatal(err)
	}
	definitions := map[uint32]dungeonDropItemDefinition{}
	for _, id := range []uint32{400360014, 400360015} {
		def, err := items.ResolveItem(id)
		if err != nil || def.Kind != dungeonDropItemEquipment || def.EquipmentType != "[charm]" {
			t.Fatalf("现场类型不符：%+v %v", def, err)
		}
		definitions[id] = def
		t.Logf("现场快捷装备：%d %s %s %s", id, def.Name, def.EquipmentType, def.PVFPath)
	}
	ctx := context.Background()
	for _, second := range []uint32{400360014, 400360015} {
		t.Run(fmt.Sprint(second), func(t *testing.T) {
			a, err := buildCurrentDungeonPickupStack(9, definitions[400360014], 1)
			if err != nil {
				t.Fatal(err)
			}
			b, err := buildCurrentDungeonPickupStack(10, definitions[second], 1)
			if err != nil {
				t.Fatal(err)
			}
			// 模拟历史物品缺少/误存分类字段；判定仍须查询当前PVF。
			a.Extra["equipment_type"] = "[weapon]"
			delete(b.Extra, "equipment_type")
			repos := mustCurrentNPCShopRepositories(t, 12345, map[string]r.ItemStack{"0:9": a, "0:10": b})
			service := currentNPCShopTestService(repos, nil, items)
			conn := &bufferConn{}
			session := &gameSession{conn: conn, connID: "快捷装备实物回归", accountID: "account-1", selectedCharacterID: 19}
			move := func(from, to int16, allowed bool) {
				t.Helper()
				before, _, _ := repos.Inventory.Load(ctx, "19")
				conn.write.Reset()
				driveItemSlotFamilyGuardMove(t, service, session, itemSlotFamilyGuardMoveBody(0, from, 0, to))
				ack, rest := splitGameServerUpperPacket(t, conn.write.Bytes())
				if ack.Header.MsgID != uint16(dnfenum.CmdPacketMoveItemspace) {
					t.Fatalf("应回原生op19：%+v", ack.Header)
				}
				after, _, _ := repos.Inventory.Load(ctx, "19")
				if !allowed {
					if !bytes.Equal(ack.Body, []byte{0, 17, 0, 0}) || len(rest) != 0 || !reflect.DeepEqual(before.Slots, after.Slots) {
						t.Fatalf("第二件应失败且无刷新、资产变化：%x 后续%d", ack.Body, len(rest))
					}
				} else if len(ack.Body) == 0 || ack.Body[0] != 1 || reflect.DeepEqual(before.Slots, after.Slots) {
					t.Fatalf("合法移动未成功：%x", ack.Body)
				}
			}
			move(9, 3, true)
			move(10, 4, false)
			move(10, 3, true)
			move(3, 8, true)
			move(8, 11, true)
			move(10, 4, true)
			character, _, _ := repos.Character.Load(ctx, "19")
			if character.Stats["gold"] != 12345 {
				t.Fatal("快捷装备移动不应修改金币")
			}
		})
	}
	for _, def := range definitions {
		t.Run("获得先入装备页_"+fmt.Sprint(def.ItemID), func(t *testing.T) {
			bag := r.InventoryRecord{Slots: map[string]r.ItemStack{}}
			if _, err := grantCurrentCeraShopProduct(&bag, def, 2); err != nil {
				t.Fatal(err)
			}
			if len(bag.Slots) != 2 || bag.Slots["0:9"].ItemID != int64(def.ItemID) || bag.Slots["0:10"].ItemID != int64(def.ItemID) {
				t.Fatalf("批量发放不能自动穿戴：%+v", bag.Slots)
			}
			for slot := int16(9); slot <= 64; slot++ {
				bag.Slots[currentDungeonPickupMainSlotKey(slot)] = r.ItemStack{ItemID: 999, Count: 1}
			}
			before := r.CloneInventory(bag)
			if _, err := grantCurrentCeraShopProduct(&bag, def, 1); err == nil || !reflect.DeepEqual(before.Slots, bag.Slots) {
				t.Fatal("装备页满时快捷装备不能自动落入快捷栏")
			}
			pickup := currentDungeonPickupPlacement(def, 1)
			mail, err := mailboxItemResolutionFromPVF(def, false)
			if err != nil || pickup.Definition.AllowQuickSlots || mail.AllowQuickSlots {
				t.Fatalf("掉落或邮件仍可自动穿戴：%+v %+v %v", pickup.Definition, mail, err)
			}
		})
	}
}
