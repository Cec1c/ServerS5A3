package dnfbridge

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"testing"

	dnfchronicle "longheng.io/server/internal/modules/dnf/chronicle"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	platformpvf "longheng.io/server/internal/platform/pvf"
)

func saveChronicleWornFixture(t *testing.T, g dnfrepo.Group, slot int16, item int64) {
	t.Helper()
	ctx := context.Background()
	inv, _, _ := g.Inventory.Load(ctx, "2")
	target := inv.Slots["0:30"]
	delete(inv.Slots, "0:30")
	target.ItemID = item
	target.Extra["quality_seed"] = "999999998"
	target.Extra["current_exe_equipment_type"] = fmt.Sprint(slot)
	target.Extra["current_exe_runtime_move"] = "1"
	inv.Slots[fmt.Sprint("0:", slot)] = dnfrepo.ItemStack{ItemID: 100080578, Count: 1, Extra: map[string]string{"item_kind": "equipment", "quality_seed": "1234567"}}
	if err := g.Inventory.Save(ctx, inv); err != nil {
		t.Fatal(err)
	}
	row := currentItemListEntryFromStack(0, slot, target)
	if err := g.Equipment.Save(ctx, dnfrepo.EquipmentRecord{CharacterID: "2", Entries: map[string]dnfrepo.EquipmentEntry{
		fmt.Sprint(slot): {SlotIndex: slot, ItemID: item, RawEntry: append([]byte(nil), row.data[:]...), Extra: target.Extra},
	}}); err != nil {
		t.Fatal(err)
	}
}

func assertWornChronicleWire(t *testing.T, data []byte, slot int16) {
	t.Helper()
	targetSeen, ackSeen := false, false
	for len(data) != 0 {
		packet, rest := splitGameServerUpperPacket(t, data)
		data = rest
		if packet.Header.MsgID == 342 || packet.Header.MsgID == 2 || (packet.Header.MsgID == 13 && len(packet.Body) > 0 && packet.Body[0] == 3) {
			t.Fatal("原位精炼不应发被屏蔽快照或重建物品对象")
		}
		if packet.Header.MsgID == 14 && len(packet.Body) > 0 && packet.Body[0] == 3 {
			if ackSeen || len(packet.Body) != 3+currentItemListEntryWireSize || binary.LittleEndian.Uint16(packet.Body[1:3]) != 1 || int16(binary.LittleEndian.Uint16(packet.Body[3:5])) != slot || packet.Body[3+0x27] == 0 {
				t.Fatalf("结果前穿戴原位属性行不符：%x", packet.Body)
			}
			targetSeen = true
		}
		if packet.Header.MsgID == 370 {
			if !targetSeen || ackSeen || len(packet.Body) != 10 || packet.Body[0] != 1 || packet.Body[5] != 1 || packet.Body[6] != 3 || int16(binary.LittleEndian.Uint16(packet.Body[7:9])) != slot {
				t.Fatalf("穿戴原位更新与精炼结果顺序/槽位错误：%x target=%t", packet.Body, targetSeen)
			}
			ackSeen = true
		}
	}
	if !ackSeen {
		t.Fatal("未收到穿戴精炼结果")
	}
}

func TestChronicleWornSixAurasPersistAndDoNotMutateSameBackpackSlot(t *testing.T) {
	for _, aura := range []int64{1254, 1255, 1256, 10090758, 10090759, 10090760} {
		t.Run(fmt.Sprint(aura), func(t *testing.T) {
			s, session, conn, g := chronicleFixture(t, chronicleFixtureSource(), aura)
			saveChronicleWornFixture(t, g, 14, 100080578)
			body := chronicleBody(uint32(aura), 8)
			body[6] = 3
			binary.LittleEndian.PutUint16(body[7:9], 14)
			if err := s.handleCurrentChronicleRefine(session, body); err != nil {
				t.Fatal(err)
			}
			assertWornChronicleWire(t, conn.write.Bytes(), 14)
			inv, _, _ := g.Inventory.Load(context.Background(), "2")
			if inv.Slots["0:65"].Count != 4 || inv.Slots["0:14"].Count != 1 || inv.Slots["0:14"].Extra["quality_seed"] != "1234567" {
				t.Fatal("材料扣除或同号背包保护失败")
			}
			eq, _, _ := g.Equipment.Load(context.Background(), "2")
			encoded, _ := json.Marshal(eq)
			var cold dnfrepo.EquipmentRecord
			if err := json.Unmarshal(encoded, &cold); err != nil {
				t.Fatal(err)
			}
			e := cold.Entries["14"]
			row, ok := currentItemListEntryFromEquipment(e)
			if !ok || row.data[0x27] != 1 || len(currentChronicleMode1Vector(e.Extra, row)) != 8 || e.Extra["quality_seed"] != "999999998" || binary.LittleEndian.Uint32(row.data[6:10]) != 999999998 {
				t.Fatal("穿戴属性冷读取或品质保持失败")
			}
		})
	}
}

func TestChronicleRealPVFWornRingCapturedRequest(t *testing.T) {
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
	s, session, conn, g := chronicleFixture(t, chronicleFixtureSource(), 10090759)
	s.pvfItemCatalog = items
	saveChronicleWornFixture(t, g, 21, 100322403)
	ctx := context.Background()
	eq, _, _ := g.Equipment.Load(ctx, "2")
	e := eq.Entries["21"]
	e.Extra["durability"] = "0"
	eq.Entries["21"] = e
	if err := g.Equipment.Save(ctx, eq); err != nil {
		t.Fatal(err)
	}
	inv, _, _ := g.Inventory.Load(ctx, "2")
	inv.Slots["0:6"] = inv.Slots["0:65"]
	delete(inv.Slots, "0:65")
	if err := g.Inventory.Save(ctx, inv); err != nil {
		t.Fatal(err)
	}
	body, _ := hex.DecodeString("060007f9990003150063ccfa0508")
	if err := s.handleCurrentChronicleRefine(session, body); err != nil {
		t.Fatal(err)
	}
	assertWornChronicleWire(t, conn.write.Bytes(), 21)
	inv, _, _ = g.Inventory.Load(ctx, "2")
	eq, _, _ = g.Equipment.Load(ctx, "2")
	if inv.Slots["0:6"].Count != 4 || inv.Slots["0:21"].Count != 1 || !bytes.Equal(eq.Entries["21"].RawEntry[0x27:0x28], []byte{1}) {
		t.Fatal("现场穿戴戒指请求未正确持久化")
	}
}

func TestChronicleWornFailureDoesNotDeleteOrRecreateSameNumberedBackpackRow(t *testing.T) {
	s, session, conn, g := chronicleFixture(t, chronicleFixtureSource(), 1254)
	saveChronicleWornFixture(t, g, 14, 100080578)
	ctx := context.Background()
	eq, _, _ := g.Equipment.Load(ctx, "2")
	delete(eq.Entries, "14")
	if err := g.Equipment.Save(ctx, eq); err != nil {
		t.Fatal(err)
	}
	inv, _, _ := g.Inventory.Load(ctx, "2")
	material := inv.Slots["0:65"]
	material.Count = 4
	inv.Slots["0:65"] = material
	inv.Slots["0:121"] = dnfrepo.ItemStack{ItemID: 3311, Count: 7, Extra: map[string]string{"item_kind": "stackable"}}
	if err := g.Inventory.Save(ctx, inv); err != nil {
		t.Fatal(err)
	}
	result := dnfchronicle.Result{Request: dnfchronicle.Request{TargetList: 3, MaterialSlot: 65, TargetSlot: 14},
		MaterialRemaining: 4, Rewards: []dnfchronicle.RewardSlot{{Slot: 121, ItemID: 3311, Count: 7}}, ChangedSlots: []int16{65, 121}}
	if err := s.sendCurrentChronicleRefineResult(session, result); err != nil {
		t.Fatal(err)
	}
	data := conn.write.Bytes()
	ack, wornCleared := false, false
	for len(data) != 0 {
		packet, rest := splitGameServerUpperPacket(t, data)
		data = rest
		if packet.Header.MsgID == 370 {
			if !wornCleared || len(packet.Body) != 20 || packet.Body[5] != 0 || packet.Body[9] != 1 || binary.LittleEndian.Uint16(packet.Body[7:9]) != ^uint16(0) {
				t.Fatalf("碎装奖励结果错误：%x", packet.Body)
			}
			ack = true
		}
		if packet.Header.MsgID == 14 && len(packet.Body) >= 3 && packet.Body[0] == 3 {
			if ack || len(packet.Body) != 3+currentItemListEntryWireSize || binary.LittleEndian.Uint16(packet.Body[3:5]) != 14 || binary.LittleEndian.Uint32(packet.Body[5:9]) != ^uint32(0) {
				t.Fatal("碎装目标未在结果前按穿戴槽清除")
			}
			wornCleared = true
		}
		if packet.Header.MsgID == 14 && len(packet.Body) >= 3 && packet.Body[0] == 0 {
			count := int(binary.LittleEndian.Uint16(packet.Body[1:3]))
			for i := 0; i < count; i++ {
				offset := 3 + i*currentItemListEntryWireSize
				row := packet.Body[offset : offset+currentItemListEntryWireSize]
				if binary.LittleEndian.Uint16(row[:2]) == 14 {
					t.Fatal("未发生变化的同号背包装备不应补回，否则会误报获得物品")
				}
			}
		}
	}
	if !ack || !wornCleared {
		t.Fatal("缺少失败结果或真正穿戴目标的清除")
	}
}
