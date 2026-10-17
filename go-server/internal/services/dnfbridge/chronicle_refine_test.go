package dnfbridge

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"reflect"
	"testing"

	dnfchronicle "longheng.io/server/internal/modules/dnf/chronicle"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	"longheng.io/server/internal/modules/dnf/repository/memory"
)

func chronicleFixtureSource() dungeonDropCatalogTestSource {
	source := dungeonDropCatalogTestSource{"monster/monster.lst": "", "stackable/stackable.lst": "", "equipment/equipment.lst": "100080578 `target.equ`\n", "equipment/target.equ": "[rarity] 5\n[durability] 40\n[equipment type] `[coat]` 20\n[usable job] `[at priest]`\n[/usable job]\n[character item check] 14 4 `coat`\n[/character item check]\n"}
	for typ := 0; typ < 3; typ++ {
		rules := "[3choro enchant]\n[check]\n14 4 `coat`\n[skill]\n8 `[at priest]` 101\n[/skill]\n[skill]\n9 `[at priest]` 102\n[/skill]\n[/check]\n"
		source[fmt.Sprintf("stackable/rules%d.stk", typ)] = rules
		for _, holy := range []bool{false, true} {
			id := 1254 + typ
			prob := 11
			file := fmt.Sprintf("aura%d.stk", typ)
			if holy {
				id = 10090758 + typ
				prob = 100
				file = fmt.Sprintf("cash/holy%d.stk", typ)
			}
			source["stackable/stackable.lst"] += fmt.Sprintf("%d `%s`\n", id, file)
			source["stackable/"+file] = fmt.Sprintf("[name] `测试气息`\n[stackable type] `[etc]` 1\n[stack limit] 1000\n[type] %d\n[need material] 3311 10\n[probability] 100 %d\n[import script] `rules%d.stk`\n", typ, prob, typ)
		}
	}
	return source
}
func chronicleFixture(t *testing.T, source dungeonDropCatalogTestSource, id int64) (*Service, *gameSession, *bufferConn, dnfrepo.Group) {
	t.Helper()
	ctx := context.Background()
	g := memory.NewMemoryGroup()
	if err := g.Character.Save(ctx, dnfrepo.CharacterRecord{CharacterID: "2", AccountID: "account-1", Job: "14", Stats: map[string]int64{"grow_type": 36}}); err != nil {
		t.Fatal(err)
	}
	if err := g.Inventory.Save(ctx, dnfrepo.InventoryRecord{CharacterID: "2", Slots: map[string]dnfrepo.ItemStack{"0:30": {ItemID: 100080578, Count: 1, Extra: map[string]string{"item_kind": "equipment", "durability": "40", "ext_data0": "12", "value_a": "12345"}}, "0:65": {ItemID: id, Count: 5, Extra: map[string]string{"item_kind": "stackable", "stackable_type": "[etc]"}}}}); err != nil {
		t.Fatal(err)
	}
	s := equipmentRecipeCompoundTestService(t, source, g)
	s.options.currentClientVersion = "official-24.9"
	conn := &bufferConn{}
	session := &gameSession{conn: conn, connID: "精炼隔离回归", selectedCharacterID: 2}
	return s, session, conn, g
}
func chronicleBody(id uint32, option byte) []byte {
	body := make([]byte, 14)
	binary.LittleEndian.PutUint16(body, 65)
	binary.LittleEndian.PutUint32(body[2:], id)
	binary.LittleEndian.PutUint16(body[7:], 30)
	binary.LittleEndian.PutUint32(body[9:], 100080578)
	body[13] = option
	return body
}

func TestChronicleCaptured90RequestAndStrictShapes(t *testing.T) {
	body, _ := hex.DecodeString("7000e7040000001e00c21bf70508")
	r, err := parseCurrentChronicleRefineRequest(body)
	if err != nil || r.MaterialSlot != 112 || r.MaterialID != 1255 || r.TargetSlot != 30 || r.TargetID != 100080578 || r.Option != 8 {
		t.Fatalf("实际请求%+v %v", r, err)
	}
	for _, edit := range []func([]byte) []byte{func(b []byte) []byte { return b[:13] }, func(b []byte) []byte { return append(b, 0) }, func(b []byte) []byte { b[6] = 1; return b }, func(b []byte) []byte { b[13] = 32; return b }, func(b []byte) []byte { b[0] = 255; b[1] = 255; return b }} {
		if _, err := parseCurrentChronicleRefineRequest(edit(append([]byte(nil), body...))); err == nil {
			t.Fatal("非法370请求被放行")
		}
	}
}

func TestChronicleAllSixAurasUse90DispatchAndPersist(t *testing.T) {
	for _, id := range []int64{1254, 1255, 1256, 10090758, 10090759, 10090760} {
		t.Run(fmt.Sprint(id), func(t *testing.T) {
			s, session, conn, g := chronicleFixture(t, chronicleFixtureSource(), id)
			handled, err := currentGameplayModules.DispatchUpper(s, session, 370, 1, chronicleBody(uint32(id), 8))
			if err != nil || !handled {
				t.Fatalf("370分发%t %v", handled, err)
			}
			ack, rest := splitGameServerUpperPacket(t, conn.write.Bytes())
			if ack.Header.MsgID != 370 || !bytes.Equal(ack.Body, []byte{1, 65, 0, 4, 0, 1, 0, 30, 0, 0}) || len(rest) == 0 {
				t.Fatalf("90固定结果尾部缺失：%+v %x", ack.Header, ack.Body)
			}
			inv, _, _ := g.Inventory.Load(context.Background(), "2")
			target := inv.Slots["0:30"]
			state, err := currentChronicleItemState(target)
			aura := uint32(id)
			if id > 1256 {
				aura = uint32(id - 10090758 + 1254)
			}
			if err != nil || len(state.Options) != 1 || state.Options[0] != (dnfchronicle.Option{AuraID: aura, Job: 14, Grow: 4, EquipmentType: 14, Number: 8}) || inv.Slots["0:65"].Count != 4 || state.Durability != 40 || state.Reinforce != 12 {
				t.Fatalf("气息或属性未持久化：%+v %v", state, err)
			}
			entry := currentItemListEntryFromStack(0, 30, target)
			if entry.data[0x27] != 1 || binary.LittleEndian.Uint32(entry.data[0x28:]) != aura || entry.data[0x30] != 14 || entry.data[0x32] != 4 || entry.data[0x34] != 14 || entry.data[0x36] != 8 {
				t.Fatalf("当前119字节属性偏移错位%x", entry.data[0x27:0x38])
			}
			raw, _ := json.Marshal(target)
			var restored dnfrepo.ItemStack
			if err := json.Unmarshal(raw, &restored); err != nil {
				t.Fatal(err)
			}
			again, err := currentChronicleItemState(restored)
			if err != nil || !reflect.DeepEqual(again, state) {
				t.Fatal("角色重新载入丢失精炼属性")
			}
		})
	}
}

func TestChronicleSecondOptionAndDuplicateRejection(t *testing.T) {
	s, session, conn, g := chronicleFixture(t, chronicleFixtureSource(), 10090758)
	if err := s.handleCurrentChronicleRefine(session, chronicleBody(10090758, 8)); err != nil {
		t.Fatal(err)
	}
	conn.write.Reset()
	before, _, _ := g.Inventory.Load(context.Background(), "2")
	if err := s.handleCurrentChronicleRefine(session, chronicleBody(10090758, 8)); err != nil {
		t.Fatal(err)
	}
	after, _, _ := g.Inventory.Load(context.Background(), "2")
	if !reflect.DeepEqual(before, after) {
		t.Fatal("重复技能/气息发生扣材")
	}
	ack, rest := splitGameServerUpperPacket(t, conn.write.Bytes())
	if !bytes.Equal(ack.Body, []byte{0, 0x13}) || len(rest) != 0 {
		t.Fatal("重复属性没有明确失败结果")
	}
	conn.write.Reset()
	if err := s.handleCurrentChronicleRefine(session, chronicleBody(10090758, 9)); err != nil {
		t.Fatal(err)
	}
	after, _, _ = g.Inventory.Load(context.Background(), "2")
	state, _ := currentChronicleItemState(after.Slots["0:30"])
	if len(state.Options) != 2 || after.Slots["0:65"].Count != 3 {
		t.Fatal("第二属性或扣材不正确")
	}
}

func TestChronicleProjectionKeepsWornAndMode1Data(t *testing.T) {
	s, session, _, g := chronicleFixture(t, chronicleFixtureSource(), 1254)
	if err := s.handleCurrentChronicleRefine(session, chronicleBody(1254, 8)); err != nil {
		t.Fatal(err)
	}
	inv, _, _ := g.Inventory.Load(context.Background(), "2")
	stack := inv.Slots["0:30"]
	equipped := dnfrepo.EquipmentEntry{SlotIndex: 14, ItemID: stack.ItemID, RawEntry: stack.RawEntry, Extra: stack.Extra}
	entry, ok := currentItemListEntryFromEquipment(equipped)
	if !ok || entry.data[0x27] != 1 {
		t.Fatal("穿戴投影丢属性")
	}
	update := currentEquipmentUpdateEntryFromEquipment(equipped)
	opts, err := decodeCurrentChronicleOptions(update.data[0x1a:0x2b])
	if err != nil || len(opts) != 1 || opts[0].AuraID != 1254 {
		t.Fatal("旧装备更新投影丢属性", err)
	}
	want := []byte{0xe6, 4, 0, 0, 14, 4, 14, 8}
	if got := currentChronicleMode1Vector(equipped.Extra, entry); !bytes.Equal(got, want) {
		t.Fatalf("重登创建向量%x", got)
	}
	if binary.LittleEndian.Uint32(entry.data[0xe:]) != 12345 || binary.LittleEndian.Uint32(entry.data[0x38:]) != 0 {
		t.Fatal("精炼覆盖了附魔或期限字段")
	}
}

func TestChronicle90FailureAckHasRewardPaddingAndDelta(t *testing.T) {
	r := dnfchronicle.Result{Request: dnfchronicle.Request{MaterialSlot: 65, TargetSlot: 30}, MaterialRemaining: 2, Rewards: []dnfchronicle.RewardSlot{{Slot: 354, ItemID: 3033, Count: 7}, {Slot: 121, ItemID: 3311, Count: 13}, {Slot: 122, ItemID: 3314, Count: 2}}}
	body := buildCurrentChronicleRefineSuccessBody(r)
	if len(body) != 40 || body[5] != 0 || body[6] != 0 || binary.LittleEndian.Uint16(body[7:]) != 30 || body[9] != 3 {
		t.Fatalf("90碎装固定字段%x", body)
	}
	for i, row := range r.Rewards {
		off := 10 + 10*i
		if binary.LittleEndian.Uint16(body[off:]) != uint16(row.Slot) || binary.LittleEndian.Uint32(body[off+2:]) != row.ItemID || binary.LittleEndian.Uint32(body[off+6:]) != row.Count {
			t.Fatal("失败奖励行错位")
		}
	}
}

func TestChronicleClassificationMismatchCannotConsume(t *testing.T) {
	s, session, _, g := chronicleFixture(t, chronicleFixtureSource(), 1254)
	before, _, _ := g.Inventory.Load(context.Background(), "2")
	handled, err := currentGameplayModules.DispatchUpper(s, session, 370, 0, chronicleBody(1254, 8))
	if !handled || err != nil {
		t.Fatal(handled, err)
	}
	after, _, _ := g.Inventory.Load(context.Background(), "2")
	if !reflect.DeepEqual(before, after) {
		t.Fatal("错误分类精炼发生消耗")
	}
}

func TestChronicleExplicitZeroDurabilityAndIdentificationOverrideOldRaw(t *testing.T) {
	for _, name := range []string{"零耐久", "未识别"} {
		t.Run(name, func(t *testing.T) {
			s, session, conn, g := chronicleFixture(t, chronicleFixtureSource(), 1254)
			inv, _, _ := g.Inventory.Load(context.Background(), "2")
			target := inv.Slots["0:30"]
			entry := currentItemListEntryFromStack(0, 30, target)
			target.RawEntry = append([]byte(nil), entry.data[:]...)
			want := byte(7)
			if name == "零耐久" {
				target.Extra["durability"] = "0"
			} else {
				target.Extra["amplify_type"] = "128"
				want = 0xae
			}
			inv.Slots["0:30"] = target
			g.Inventory.Save(context.Background(), inv)
			before, _, _ := g.Inventory.Load(context.Background(), "2")
			if err := s.handleCurrentChronicleRefine(session, chronicleBody(1254, 8)); err != nil {
				t.Fatal(err)
			}
			ack, rest := splitGameServerUpperPacket(t, conn.write.Bytes())
			after, _, _ := g.Inventory.Load(context.Background(), "2")
			if !bytes.Equal(ack.Body, []byte{0, want}) || len(rest) != 0 || !reflect.DeepEqual(before, after) {
				t.Fatalf("旧行覆盖了权威状态：%x", ack.Body)
			}
		})
	}
}
