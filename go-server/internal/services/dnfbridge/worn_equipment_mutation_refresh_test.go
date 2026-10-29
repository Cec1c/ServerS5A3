package dnfbridge

import (
	"bytes"
	"context"
	"encoding/binary"
	"reflect"
	"testing"

	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

func TestWornMutationRefreshOnlyRequestedSlotAndPreservesCompleteCurrentRow(t *testing.T) {
	s, session, conn, g := chronicleFixture(t, chronicleFixtureSource(), 10090759)
	saveChronicleWornFixture(t, g, 14, 100080578)
	ctx := context.Background()
	eq, _, _ := g.Equipment.Load(ctx, "2")
	e := eq.Entries["14"]
	e.Extra["enchant_card_id"] = "490702716"
	e.Extra["value_a"] = "490702716"
	e.Extra["byte_12"] = "2"
	e.RawEntry[0x60] = 1 // 仓储旧排序锁不应混入穿戴行。
	eq.Entries["14"] = e
	eq.Entries["15"] = dnfrepo.EquipmentEntry{SlotIndex: 15, ItemID: 700, Extra: map[string]string{"current_exe_equipment_type": "15"}}
	if err := g.Equipment.Save(ctx, eq); err != nil {
		t.Fatal(err)
	}
	before, _, _ := g.Equipment.Load(ctx, "2")
	if err := s.sendWornEquipmentMutationSlots(session, "附魔原位刷新专项", []int16{14, 14}); err != nil {
		t.Fatal(err)
	}
	packet, rest := splitGameServerUpperPacket(t, conn.write.Bytes())
	want, ok := currentItemListEntryFromEquipment(e)
	if !ok || packet.Header.MsgID != 14 || len(rest) != 0 || !bytes.Equal(packet.Body, buildCurrentItemUpdateBody(3, []currentItemListEntry{want})) {
		t.Fatalf("没有仅发送目标权威行：%x", packet.Body)
	}
	if binary.LittleEndian.Uint32(packet.Body[3+0x0e:3+0x12]) != 490702716 || packet.Body[3+0x12] != 2 || packet.Body[3+0x60] != 0 {
		t.Fatal("即时刷新卡片/升级次数或穿戴行投影错误")
	}
	after, _, _ := g.Equipment.Load(ctx, "2")
	if !reflect.DeepEqual(before, after) {
		t.Fatal("只读投影修改了仓储")
	}
	conn.write.Reset()
	if err := s.sendRepairedWornEquipmentSlots(session, "维修仅更新实际存在装备", []int16{16}); err == nil || conn.write.Len() != 0 {
		t.Fatal("repair must not clear an absent worn slot")
	}
	if err := s.sendWornEquipmentMutationSlots(session, "禁止通用扩展", []int16{26}); err == nil || conn.write.Len() != 0 {
		t.Fatal("普通装备原位路径不应覆盖宠物移动生命周期")
	}
}

func TestChronicleWornSecondOptionIsRefreshedBeforeAckAndFullTargetDoesNotConsume(t *testing.T) {
	s, session, conn, g := chronicleFixture(t, chronicleFixtureSource(), 10090759)
	saveChronicleWornFixture(t, g, 14, 100080578)
	body := chronicleBody(10090759, 8)
	body[6] = 3
	binary.LittleEndian.PutUint16(body[7:9], 14)
	for _, option := range []byte{8, 9} {
		conn.write.Reset()
		body[13] = option
		if err := s.handleCurrentChronicleRefine(session, body); err != nil {
			t.Fatal(err)
		}
		assertWornChronicleWire(t, conn.write.Bytes(), 14)
		packet, _ := splitGameServerUpperPacket(t, conn.write.Bytes())
		wantCount := byte(option - 7)
		if packet.Header.MsgID != 14 || packet.Body[3+0x27] != wantCount {
			t.Fatalf("第%d条属性未在原位更新行出现：%x", wantCount, packet.Body)
		}
	}
	ctx := context.Background()
	eq, _, _ := g.Equipment.Load(ctx, "2")
	inv, _, _ := g.Inventory.Load(ctx, "2")
	if inv.Slots["0:65"].Count != 3 || eq.Entries["14"].RawEntry[0x27] != 2 {
		t.Fatal("两次成功的扣材或持久化不符")
	}
	conn.write.Reset()
	if err := s.handleCurrentChronicleRefine(session, body); err != nil {
		t.Fatal(err)
	}
	packet, _ := splitGameServerUpperPacket(t, conn.write.Bytes())
	if packet.Header.MsgID != 370 || !bytes.Equal(packet.Body, []byte{0, 19}) {
		t.Fatalf("属性已满时失败回包不符：%x", packet.Body)
	}
	afterInv, _, _ := g.Inventory.Load(ctx, "2")
	afterEq, _, _ := g.Equipment.Load(ctx, "2")
	if !reflect.DeepEqual(inv, afterInv) || !reflect.DeepEqual(eq, afterEq) {
		t.Fatal("满属性拒绝扣除了气息或改变装备")
	}
}
