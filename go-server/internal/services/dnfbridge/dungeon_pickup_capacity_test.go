package dnfbridge

import (
	"bytes"
	"context"
	"encoding/binary"
	"fmt"
	"os"
	"testing"

	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	platformpvf "longheng.io/server/internal/platform/pvf"
)

// 现场已扩容至56格，基础32格满后仍有16个空格，魔法石拾取却报满包。
// 通过真实拾取处理器检查扩容、物品持久化、失败保留和重复请求。
func TestCurrentDungeonPickupUsesPersistedInventoryCapacity(t *testing.T) {
	for _, tc := range []struct {
		name       string
		expansion  int
		occupiedTo int
		wantSlot   uint16
	}{
		{"基础容量最后一格", 0, 39, 40},
		{"未扩容不写锁定格", 0, 40, 0},
		{"扩容八格", 8, 40, 41},
		{"扩容十六格", 16, 48, 49},
		{"现场扩容二十四格", 24, 48, 49},
		{"扩容后真正满包", 24, 64, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			service, runtime, repositories := prepareCurrentDungeonDropTest(t, 1, 3227)
			prepareDungeonPickupCapacity(t, repositories, tc.expansion, tc.occupiedTo)
			key := seedCurrentDungeonPickupDrop(t, service, runtime, 9001, currentSceneActorObjectKey(99))
			assertDungeonPickupCapacityResult(t, service, runtime, repositories, key, tc.wantSlot, 9001)
		})
	}
}

func TestRealScriptPVFDungeonMagicStonePickupExpandedCapacity(t *testing.T) {
	path := os.Getenv("DNFBRIDGE_REAL_PVF_SMOKE")
	if path == "" {
		t.Skip("未指定真实PVF")
	}
	archive, err := platformpvf.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	items, err := newPVFDungeonDropCatalog(archive)
	if err != nil {
		t.Fatal(err)
	}
	definition, err := items.ResolveItem(100352903)
	if err != nil {
		t.Fatal(err)
	}
	if definition.Kind != dungeonDropItemEquipment || definition.Durability != 0 {
		t.Fatalf("现场魔法石定义不符：%+v", definition)
	}
	service, runtime, repositories := prepareCurrentDungeonDropTest(t, 1, 3227)
	prepareDungeonPickupCapacity(t, repositories, 24, 48)
	key := seedCurrentDungeonPickupDrop(t, service, runtime, 9001, currentSceneActorObjectKey(99))
	runtime.DropOwner.byObjectKey[key].Item = definition
	assertDungeonPickupCapacityResult(t, service, runtime, repositories, key, 49, 100352903)
}

func prepareDungeonPickupCapacity(t *testing.T, repositories dnfrepo.Group, expansion, occupiedTo int) {
	t.Helper()
	ctx := context.Background()
	if err := repositories.Settings.Save(ctx, dnfrepo.SettingsRecord{
		Scope: dnfrepo.CharacterContainerStateScope("99"),
		Values: map[string]string{
			"main_list_param16":           fmt.Sprint(expansion),
			"avatar_list_param16":         "0",
			"personal_cargo_list_param16": "8",
			"source":                      "拾取扩容回归",
		},
	}); err != nil {
		t.Fatal(err)
	}
	record, found, err := repositories.Inventory.Load(ctx, "99")
	if err != nil || !found {
		t.Fatalf("读取隔离背包：存在=%t 错误=%v", found, err)
	}
	if record.Slots == nil {
		record.Slots = make(map[string]dnfrepo.ItemStack)
	}
	// 现有规则允许装备回落到空快捷格；现场六个快捷格也已占用。
	for slot := 3; slot <= 8; slot++ {
		record.Slots[fmt.Sprintf("0:%d", slot)] = dnfrepo.ItemStack{ItemID: 3227, Count: 1}
	}
	for slot := 9; slot <= occupiedTo; slot++ {
		record.Slots[fmt.Sprintf("0:%d", slot)] = dnfrepo.ItemStack{ItemID: 9001, Count: 1}
	}
	if err := repositories.Inventory.Save(ctx, record); err != nil {
		t.Fatal(err)
	}
}

func assertDungeonPickupCapacityResult(t *testing.T, service *Service, runtime *runtimeDungeonState, repositories dnfrepo.Group, key uint32, wantSlot uint16, itemID uint32) {
	t.Helper()
	ctx := context.Background()
	before, _, err := repositories.Inventory.Load(ctx, "99")
	if err != nil {
		t.Fatal(err)
	}
	connection := &bufferConn{}
	session := &gameSession{conn: connection, connID: "拾取扩容回归", selectedCharacterID: 99, dungeon: dungeonSessionState{runtime: runtime}}
	request := currentDungeonPickupTestBody(key)
	if err := service.handleCurrentDungeonPickup(session, request); err != nil {
		t.Fatal(err)
	}
	after, _, err := repositories.Inventory.Load(ctx, "99")
	if err != nil {
		t.Fatal(err)
	}
	drop := runtime.DropOwner.byObjectKey[key]
	if wantSlot == 0 {
		if len(after.Slots) != len(before.Slots) || drop.Status != runtimeDungeonDropAvailable {
			t.Fatalf("满包拒绝后改变资产或吃掉地面物品：格数=%d/%d 状态=%d", len(before.Slots), len(after.Slots), drop.Status)
		}
		ack, tail := splitGameServerUpperPacket(t, connection.write.Bytes())
		if ack.Header.Classification != 1 || ack.Header.MsgID != 43 || !bytes.Equal(ack.Body, []byte{0, 4}) || len(tail) != 0 {
			t.Fatalf("满包缺少客户端复位与提示应答：头=%+v 内容=%x 尾部=%x", ack.Header, ack.Body, tail)
		}
		return
	}
	if drop.Status != runtimeDungeonDropConsumed || drop.DestinationSlot != wantSlot {
		t.Fatalf("合法扩展格未被用于拾取：期望格=%d 实际格=%d 状态=%d 回包字节=%d", wantSlot, drop.DestinationSlot, drop.Status, connection.write.Len())
	}
	stack := after.Slots[fmt.Sprintf("0:%d", wantSlot)]
	if stack.ItemID != int64(itemID) || stack.Count != 1 || len(stack.RawEntry) != currentItemListEntryWireSize || len(after.Slots) != len(before.Slots)+1 {
		t.Fatalf("拾取装备未正确保存：格=%d 物品=%+v", wantSlot, stack)
	}
	ack, rest := splitGameServerUpperPacket(t, connection.write.Bytes())
	update, tail := splitGameServerUpperPacket(t, rest)
	if ack.Header.MsgID != currentDungeonPickupResultOpcode || ack.Header.Classification != 0 || binary.LittleEndian.Uint16(ack.Body[16:18]) != wantSlot || update.Header.MsgID != 14 || len(tail) != 0 {
		t.Fatalf("拾取回包或物品刷新不符：应答=%+v 刷新=%+v 尾部=%x", ack.Header, update.Header, tail)
	}
	connection.write.Reset()
	if err := service.handleCurrentDungeonPickup(session, request); err != nil {
		t.Fatal(err)
	}
	reloaded, _, err := repositories.Inventory.Load(ctx, "99")
	if err != nil || len(reloaded.Slots) != len(after.Slots) {
		t.Fatalf("重复拾取改变库存：前=%d 后=%d 错误=%v", len(after.Slots), len(reloaded.Slots), err)
	}
}

func TestCurrentDungeonPickupFullBagCanRetrySameGroundDrop(t *testing.T) {
	service, runtime, repositories := prepareCurrentDungeonDropTest(t, 1, 3227)
	prepareDungeonPickupCapacity(t, repositories, 24, 64)
	key := seedCurrentDungeonPickupDrop(t, service, runtime, 9001, currentSceneActorObjectKey(99))
	assertDungeonPickupCapacityResult(t, service, runtime, repositories, key, 0, 9001)
	// 失败只反馈满包；角色自行清出一格后，同一掉落对象须允许重新入包。
	ctx := context.Background()
	record, found, err := repositories.Inventory.Load(ctx, "99")
	if err != nil || !found {
		t.Fatalf("读取隔离背包：存在=%t 错误=%v", found, err)
	}
	delete(record.Slots, "0:49")
	if err := repositories.Inventory.Save(ctx, record); err != nil {
		t.Fatal(err)
	}
	assertDungeonPickupCapacityResult(t, service, runtime, repositories, key, 49, 9001)
}
