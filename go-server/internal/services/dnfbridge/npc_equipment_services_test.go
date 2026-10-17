package dnfbridge

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	dnfequip "longheng.io/server/internal/modules/dnf/equip"
	dnfsvc "longheng.io/server/internal/modules/dnf/equipmentservice"
	r "longheng.io/server/internal/modules/dnf/repository"
	"longheng.io/server/internal/modules/dnf/repository/memory"
	platformpvf "longheng.io/server/internal/platform/pvf"
	"os"
	"reflect"
	"sort"
	"strings"
	"testing"
)

func npcEquipmentSource() dungeonDropCatalogTestSource {
	s := regenerationSource()
	s["stackable/stackable.lst"] += "6100 `cash/stone.stk`\n6200 `cash/candle_goldwax.stk`\n6201 `cash/candle_goldwax_oneshot.stk`\n15 `cash/box.stk`\n"
	s["stackable/cash/stone.stk"] = "[name]\n`装备跨界石`\n[stackable type]\n`[waste]` 0\n[action type]\n`[cargo transport stone]` 8\n[stack limit]\n9999\n"
	for _, p := range []string{"candle_goldwax.stk", "candle_goldwax_oneshot.stk"} {
		s["stackable/cash/"+p] = "[name]\n`测试蜜蜡`\n[stackable type]\n`[waste]` 0\n[stack limit]\n9999\n"
	}
	s["stackable/cash/box.stk"] = "[name]\n`品级调整箱`\n[stackable type]\n`[etc]` 0\n"
	s[dnfsvc.ResealConfigPath] = "[grade]\n1 90 4\n[rarity weight]\n1 2 1 2 8 8 6\n[extended rarity weight]\n0 0 1 1 1 1 1\n[reseal soul index]\n3 10099774\n[soul cost weight]\n0 0 100 10 0 1 2\n[soul reseal constant]\n0 0 1.1 1.1 0 0 1.11\n[reseal count limit]\n1 90 7\n"
	rule := "[cargo transport stone grade]\n1 90 5\n[cargo transport stone rarity weight]\n3 2\n[cargo transport stone enable equip type]\n`[weapon]`\n[cargo transport stone soul index]\n3 10099774\n[cargo transport stone soul weight]\n3 10 10 10 10\n[cargo transport stone element crystals cost weight]\n3167 3 2 2 2 2\n[cargo transport stone epic piece cost weight]\n2 3 3 3\n"
	s[dnfsvc.TransferConfigPath] = "[stone type]\n7\n" + rule + "[/stone type]\n[stone type]\n8\n" + rule + "[/stone type]\n"
	return s
}
func npcEquipmentFixture(t *testing.T, items *pvfDungeonDropCatalog, gear, wax, stone uint32) (*Service, *gameSession, *bufferConn, r.Group) {
	t.Helper()
	ctx := context.Background()
	g := memory.NewMemoryGroup()
	for _, v := range []r.CharacterRecord{{CharacterID: "19", AccountID: "a", Slot: 0, Name: "来源"}, {CharacterID: "42", AccountID: "a", Slot: 3, Name: "接收"}} {
		if e := g.Character.Save(ctx, v); e != nil {
			t.Fatal(e)
		}
	}
	if e := g.Account.Save(ctx, r.AccountRecord{AccountID: "a", Metadata: map[string]string{"account_cargo_level": "8"}}); e != nil {
		t.Fatal(e)
	}
	d, e := items.ResolveItem(gear)
	if e != nil {
		t.Fatal(e)
	}
	target, e := buildCurrentDungeonPickupStack(9, d, 1)
	if e != nil {
		t.Fatal(e)
	}
	if target.Extra == nil {
		target.Extra = map[string]string{}
	}
	target.Extra["seal_flag"] = "0"
	target.Extra["seal"] = "0"
	target.Extra["ext_data0"] = "11"
	target.Bind = false
	entry := currentItemListEntryFromStack(0, 9, target)
	target.RawEntry = append([]byte(nil), entry.data[:]...)
	target.RawEntry[13] = 0
	slots := map[string]r.ItemStack{"0:9": target, "0:121": {ItemID: 3167, Count: 1000000, Extra: map[string]string{"item_kind": "stackable"}}}
	if wax != 0 {
		slots["0:65"] = r.ItemStack{ItemID: int64(wax), Count: 10000, Extra: map[string]string{"item_kind": "stackable"}}
	}
	if stone != 0 {
		slots["0:66"] = r.ItemStack{ItemID: int64(stone), Count: 2, Extra: map[string]string{"item_kind": "stackable"}}
	}
	if e = g.Inventory.Save(ctx, r.InventoryRecord{CharacterID: "19", Slots: slots}); e != nil {
		t.Fatal(e)
	}
	account := r.AccountInventoryRecord{AccountID: "a", Slots: map[string]r.ItemStack{}}
	for _, id := range []int64{10099773, 10099774, 10099775, 10158124} {
		slot, _ := r.AccountWarehouseSlotForItem(id)
		account.Slots[fmt.Sprintf("0:%d", slot)] = r.ItemStack{ItemID: id, Count: 1000000, Extra: map[string]string{"item_kind": "stackable"}}
	}
	if e = g.AccountInventory.Save(ctx, account); e != nil {
		t.Fatal(e)
	}
	s := &Service{options: options{accountID: "a", currentClientVersion: "official-24.9"}, pvfItemCatalog: items, repositoryProvider: func() (r.Group, bool) { return g, true }}
	conn := &bufferConn{}
	session := &gameSession{conn: conn, connID: "NPC装备服务回归", selectedCharacterID: 19}
	return s, session, conn, g
}
func npcEquipmentBody81(slot int16, id uint32, material int16, mode uint16) []byte {
	b := make([]byte, 10)
	binary.LittleEndian.PutUint16(b, uint16(slot))
	binary.LittleEndian.PutUint32(b[2:], id)
	binary.LittleEndian.PutUint16(b[6:], uint16(material))
	binary.LittleEndian.PutUint16(b[8:], mode)
	return b
}
func npcEquipmentBody557(mode byte) []byte { b := []byte{66, 0, 9, 0, mode, 1, 0, 0, 0}; return b }
func assertNPCSealPackets(t *testing.T, b []byte, account bool) {
	t.Helper()
	first, rest := splitGameServerUpperPacket(t, b)
	if first.Header.MsgID != 14 || first.Header.Classification != 0 {
		t.Fatal("封装必须先刷新权威行", first)
	}
	ack, rest := splitGameServerUpperPacket(t, rest)
	if len(rest) != 0 || ack.Header.MsgID != 81 || ack.Header.Classification != 1 || len(ack.Body) != 9 || ack.Body[0] != 1 || binary.LittleEndian.Uint16(ack.Body[7:]) != 1 {
		t.Fatal("封装回包不匹配原生契约", ack)
	}
	slot := binary.LittleEndian.Uint16(ack.Body[1:])
	if account && slot != 0xffff || !account && slot != 65 {
		t.Fatal("封装回包错误引用装备槽", slot)
	}
}
func TestNPCEquipmentServicesModeAndWireRouting(t *testing.T) {
	items, e := newPVFDungeonDropCatalog(npcEquipmentSource())
	if e != nil {
		t.Fatal(e)
	}
	for _, kind := range []string{"NPC跨界", "跨界石", "NPC封装", "蜜蜡封装"} {
		t.Run(kind, func(t *testing.T) {
			s, session, conn, g := npcEquipmentFixture(t, items, 3000, 6200, 6100)
			ctx := context.Background()
			before, _, _ := g.Inventory.Load(ctx, "19")
			switch kind {
			case "NPC跨界":
				e = s.handleCurrentEquipmentTransfer(session, npcEquipmentBody557(2))
			case "跨界石":
				e = s.handleCurrentEquipmentTransfer(session, npcEquipmentBody557(0))
			case "NPC封装":
				e = s.handleCurrentResetItemAttr(session, npcEquipmentBody81(9, 3000, 65, 1))
			case "蜜蜡封装":
				e = s.handleCurrentResetItemAttr(session, npcEquipmentBody81(9, 3000, 65, 0))
			}
			if e != nil {
				t.Fatal(e)
			}
			if kind == "NPC封装" || kind == "蜜蜡封装" {
				assertNPCSealPackets(t, conn.write.Bytes(), kind == "NPC封装")
				i, _, _ := g.Inventory.Load(ctx, "19")
				if dnfsvc.SealFlag(i.Slots["0:9"]) != 1 || dnfsvc.ResealCount(i.Slots["0:9"]) != 1 {
					t.Fatal("封装结果没有保存")
				}
				return
			}
			ack, rest := splitGameServerUpperPacket(t, conn.write.Bytes())
			wantMode := byte(2)
			if kind == "跨界石" {
				wantMode = 0
			}
			if ack.Header.MsgID != 557 || len(ack.Body) != 10 || ack.Body[0] != 1 || ack.Body[9] != wantMode {
				t.Fatal(ack)
			}
			if kind == "NPC跨界" {
				m, _, _ := g.Mailbox.Load(ctx, "42")
				if len(m.Mails) != 1 {
					t.Fatal(m)
				}
				for _, mail := range m.Mails {
					if !bytes.Equal(mail.Attachments[0].RawEntry, before.Slots["0:9"].RawEntry) {
						t.Fatal("NPC邮寄丢失原实例")
					}
				}
			} else {
				a, _, _ := g.AccountInventory.Load(ctx, "a")
				raw := append([]byte(nil), before.Slots["0:9"].RawEntry...)
				binary.LittleEndian.PutUint16(raw, 0)
				if !bytes.Equal(a.Slots["12:0"].RawEntry, raw) || binary.LittleEndian.Uint16(ack.Body[5:7]) != 1 {
					t.Fatal("跨界石改变实例或剩余数量")
				}
			}
			update, _ := splitGameServerUpperPacket(t, rest)
			if update.Header.MsgID != 14 {
				t.Fatal(update)
			}
		})
	}
}
func TestNPCEquipmentServicesStrictBodiesAndGradeBoxIsolation(t *testing.T) {
	for _, b := range [][]byte{nil, make([]byte, 8), make([]byte, 10), {0, 0, 9, 0, 1, 0, 0, 0, 0}} {
		if _, e := decodeCurrentEquipmentTransferRequest(b); e == nil {
			t.Fatalf("557放行畸形请求%x", b)
		}
	}
	for _, n := range []int{0, 7, 9, 11} {
		if _, e := decodeCurrentResetItemAttrRequest(make([]byte, n)); e == nil {
			t.Fatalf("81放行长度%d", n)
		}
	}
	items, e := newPVFDungeonDropCatalog(npcEquipmentSource())
	if e != nil {
		t.Fatal(e)
	}
	s, session, conn, g := npcEquipmentFixture(t, items, 3000, 6200, 6100)
	ctx := context.Background()
	i, _, _ := g.Inventory.Load(ctx, "19")
	i.Slots["0:67"] = r.ItemStack{ItemID: 15, Count: 3, Extra: map[string]string{"item_kind": "stackable"}}
	g.Inventory.Save(ctx, i)
	if e = s.handleCurrentResetItemAttr(session, npcEquipmentBody81(9, 3000, 67, 0)); e != nil {
		t.Fatal(e)
	}
	ack, _ := splitGameServerUpperPacket(t, conn.write.Bytes())
	if ack.Header.MsgID != 81 || ack.Body[0] != 1 || binary.LittleEndian.Uint16(ack.Body[7:]) != 2 {
		t.Fatal("品级调整箱误走封装", ack)
	}
	after, _, _ := g.Inventory.Load(ctx, "19")
	if after.Slots["0:67"].Count != 2 || after.Slots["0:65"].Count != 10000 || dnfsvc.SealFlag(after.Slots["0:9"]) != 0 {
		t.Fatal("调整箱动了蜜蜡或封装状态")
	}
}

type npcEquipmentBrokenConn struct{ bufferConn }

func (c *npcEquipmentBrokenConn) Write([]byte) (int, error) {
	return 0, errors.New("测试回包发送失败")
}
func TestNPCEquipmentSendFailureDoesNotRepeatAssetCommit(t *testing.T) {
	items, e := newPVFDungeonDropCatalog(npcEquipmentSource())
	if e != nil {
		t.Fatal(e)
	}
	s, session, _, g := npcEquipmentFixture(t, items, 3000, 6200, 6100)
	session.conn = &npcEquipmentBrokenConn{}
	if e = s.handleCurrentEquipmentTransfer(session, npcEquipmentBody557(2)); e == nil {
		t.Fatal("发送错误未传播")
	}
	a, _, _ := g.AccountInventory.Load(context.Background(), "a")
	session.conn = &bufferConn{}
	if e = s.handleCurrentEquipmentTransfer(session, npcEquipmentBody557(2)); e != nil {
		t.Fatal(e)
	}
	again, _, _ := g.AccountInventory.Load(context.Background(), "a")
	if again.Slots["0:363"].Count != a.Slots["0:363"].Count {
		t.Fatal("发送失败后重复扣费")
	}
	m, _, _ := g.Mailbox.Load(context.Background(), "42")
	if len(m.Mails) != 1 {
		t.Fatal(m)
	}
}

func TestNPCEquipmentResealRequiresPositiveTargetID(t *testing.T) {
	items, e := newPVFDungeonDropCatalog(npcEquipmentSource())
	if e != nil {
		t.Fatal(e)
	}
	for _, id := range []uint32{0, 0xffffffff} {
		s, session, conn, g := npcEquipmentFixture(t, items, 3000, 6200, 6100)
		ctx := context.Background()
		before, _, _ := g.Inventory.Load(ctx, "19")
		account, _, _ := g.AccountInventory.Load(ctx, "a")
		if e = s.handleCurrentResetItemAttr(session, npcEquipmentBody81(9, id, 65, 1)); e != nil {
			t.Fatal(e)
		}
		if conn.write.Len() != 0 {
			t.Fatal("NPC无效装备应静默拒绝")
		}
		after, _, _ := g.Inventory.Load(ctx, "19")
		again, _, _ := g.AccountInventory.Load(ctx, "a")
		if !reflect.DeepEqual(before, after) || !reflect.DeepEqual(account, again) {
			t.Fatal("无效目标编号导致扣材或封装")
		}
	}
}
func TestRealPVFNPCEquipmentServicesFourEntries(t *testing.T) {
	p := os.Getenv("DNFBRIDGE_REAL_PVF_SMOKE")
	if p == "" {
		t.Skip("设置真实PVF后验收NPC和道具四入口")
	}
	a, e := platformpvf.Open(p)
	if e != nil {
		t.Fatal(e)
	}
	items, e := newPVFDungeonDropCatalog(a)
	if e != nil {
		t.Fatal(e)
	}
	wax := uint32(0)
	for id, ref := range items.itemRefs {
		if strings.EqualFold(strings.TrimPrefix(ref.path, "stackable/"), "cash/candle_goldwax.stk") {
			wax = id
			break
		}
	}
	if wax == 0 {
		t.Fatal("当前PVF无普通黄金蜜蜡")
	}
	for _, kind := range []string{"NPC跨界", "跨界石", "NPC封装", "蜜蜡封装"} {
		t.Run(kind, func(t *testing.T) {
			gear := uint32(10016)
			if kind == "跨界石" {
				gear = 100140069
			}
			s, session, conn, g := npcEquipmentFixture(t, items, gear, wax, 10304349)
			switch kind {
			case "NPC跨界":
				e = s.handleCurrentEquipmentTransfer(session, npcEquipmentBody557(2))
			case "跨界石":
				e = s.handleCurrentEquipmentTransfer(session, npcEquipmentBody557(0))
			case "NPC封装":
				e = s.handleCurrentResetItemAttr(session, npcEquipmentBody81(9, gear, 65, 1))
			case "蜜蜡封装":
				e = s.handleCurrentResetItemAttr(session, npcEquipmentBody81(9, gear, 65, 0))
			}
			if e != nil {
				t.Fatal(e)
			}
			if kind == "NPC封装" || kind == "蜜蜡封装" {
				assertNPCSealPackets(t, conn.write.Bytes(), kind == "NPC封装")
				return
			}
			ack, _ := splitGameServerUpperPacket(t, conn.write.Bytes())
			if ack.Header.MsgID != 557 || ack.Body[0] != 1 {
				t.Fatal("真实PVF跨界未成功", ack)
			}
			if kind == "NPC跨界" {
				m, _, _ := g.Mailbox.Load(context.Background(), "42")
				if len(m.Mails) != 1 {
					t.Fatal(m)
				}
			}
		})
	}
}

func TestRealPVFNPCEquipmentTransferMaterialAndPieceDebits(t *testing.T) {
	p := os.Getenv("DNFBRIDGE_REAL_PVF_SMOKE")
	if p == "" {
		t.Skip("设置真实PVF核对异界及史诗跨界实际扣材")
	}
	a, err := platformpvf.Open(p)
	if err != nil {
		t.Fatal(err)
	}
	items, err := newPVFDungeonDropCatalog(a)
	if err != nil {
		t.Fatal(err)
	}
	config, err := dnfsvc.LoadTransferConfig(a)
	if err != nil {
		t.Fatal(err)
	}
	rule := config.Rules[7]
	book, err := loadCurrentEpicPieceCatalog(a)
	if err != nil {
		t.Fatal(err)
	}
	catalog := currentNPCEquipmentCatalog{items}
	ids := []uint32{}
	for id := range book.pieceByOutput {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	samples := []uint32{100080578}
	for _, level := range []int64{85, 90} {
		found := uint32(0)
		for _, id := range ids {
			d, e := catalog.Resolve(id)
			if e != nil {
				t.Fatal(e)
			}
			if d.Rarity == 4 && d.MinimumLevel == level && !rule.ExcludeItemIDs[id] && !d.HasGrowthType && !d.PeriodEquipment && (d.EquipmentType == "[weapon]" || d.EquipmentType == "[coat]") {
				found = id
				break
			}
		}
		if found == 0 {
			t.Fatalf("当前PVF未找到%d级可核对史诗", level)
		}
		samples = append(samples, found)
	}
	for sampleIndex, id := range samples {
		d, e := catalog.Resolve(id)
		if e != nil {
			t.Fatal(e)
		}
		if sampleIndex == 0 && d.Rarity != 5 {
			t.Fatalf("异界样本%d品质=%d", id, d.Rarity)
		}
		category := 1
		if d.EquipmentType == "[weapon]" {
			category = 0
		}
		grade := int64(0)
		for _, v := range rule.GradeWeights {
			if d.MinimumLevel >= v.Minimum && d.MinimumLevel <= v.Maximum {
				grade = v.Weight
				break
			}
		}
		base := grade * rule.RarityWeights[d.Rarity]
		if base <= 0 {
			t.Fatal("真实费用基数无效")
		}
		soul := base * rule.SoulWeights[d.Rarity][category]
		if d.Rarity == 5 {
			soul = rule.ChronicleCost
		}
		elements := base * rule.ElementWeights[d.Rarity][category]
		pieces := int64(0)
		if d.Rarity == 4 {
			pieces = base * rule.EpicPieceWeight[category]
		}
		costs := map[uint32]int64{rule.SoulItemIDs[d.Rarity]: soul, rule.ElementItemID: elements}
		for _, c := range rule.ParticularCosts {
			if c.Rarity == d.Rarity && c.Level == d.MinimumLevel {
				costs[c.ItemID] += c.Count
			}
		}
		for _, short := range []bool{false, true} {
			t.Run(fmt.Sprintf("品质%d_等级%d_装备%d_不足%t", d.Rarity, d.MinimumLevel, id, short), func(t *testing.T) {
				s, session, conn, g := npcEquipmentFixture(t, items, id, 0, 0)
				ctx := context.Background()
				i, _, _ := g.Inventory.Load(ctx, "19")
				shared, _, _ := g.AccountInventory.Load(ctx, "a")
				account, _, _ := g.Account.Load(ctx, "a")
				for material := range costs {
					if _, ok := r.AccountWarehouseSlotForItem(int64(material)); !ok && material != 3167 {
						i.Slots["0:122"] = r.ItemStack{ItemID: int64(material), Count: 1000000}
					}
				}
				if pieces > 0 {
					account.Metadata[r.AccountEpicPieceMetadataKey(d.EpicPieceID)] = "1000000"
				}
				if short {
					if pieces > 0 {
						account.Metadata[r.AccountEpicPieceMetadataKey(d.EpicPieceID)] = fmt.Sprint(pieces - 1)
					} else {
						i.Slots["0:122"] = r.ItemStack{ItemID: 3311, Count: 249}
					}
				}
				if e := g.Inventory.Save(ctx, i); e != nil {
					t.Fatal(e)
				}
				if e := g.Account.Save(ctx, account); e != nil {
					t.Fatal(e)
				}
				if e := s.handleCurrentEquipmentTransfer(session, npcEquipmentBody557(2)); e != nil {
					t.Fatal(e)
				}
				after, _, _ := g.Inventory.Load(ctx, "19")
				gotShared, _, _ := g.AccountInventory.Load(ctx, "a")
				gotAccount, _, _ := g.Account.Load(ctx, "a")
				mail, _, _ := g.Mailbox.Load(ctx, "42")
				if short {
					if conn.write.Len() != 0 || !reflect.DeepEqual(i, after) || !reflect.DeepEqual(shared, gotShared) || !reflect.DeepEqual(account, gotAccount) || len(mail.Mails) != 0 {
						t.Fatal("真实PVF费用不足未保持资产")
					}
					return
				}
				ack, _ := splitGameServerUpperPacket(t, conn.write.Bytes())
				if ack.Body[0] != 1 || len(mail.Mails) != 1 {
					t.Fatal("真实PVF跨界未成功", ack, mail)
				}
				for material, cost := range costs {
					key := "0:122"
					store := after.Slots
					if slot, ok := r.AccountWarehouseSlotForItem(int64(material)); ok {
						key = fmt.Sprintf("0:%d", slot)
						store = gotShared.Slots
					} else if material == 3167 {
						key = "0:121"
					}
					if store[key].Count != 1000000-cost {
						t.Fatalf("材料%d扣除结果=%d，预期%d", material, store[key].Count, 1000000-cost)
					}
				}
				if pieces > 0 && gotAccount.Metadata[r.AccountEpicPieceMetadataKey(d.EpicPieceID)] != fmt.Sprint(1000000-pieces) {
					t.Fatal("史诗碎片未从现有碎片ID账本扣除")
				}
				for _, m := range mail.Mails {
					if !bytes.Equal(m.Attachments[0].RawEntry, i.Slots["0:9"].RawEntry) || !reflect.DeepEqual(m.Attachments[0].Extra, i.Slots["0:9"].Extra) {
						t.Fatal("真实装备实例变化")
					}
				}
				if e := s.handleCurrentEquipmentTransfer(session, npcEquipmentBody557(2)); e != nil {
					t.Fatal(e)
				}
				again, _, _ := g.Account.Load(ctx, "a")
				againInv, _, _ := g.Inventory.Load(ctx, "19")
				againShared, _, _ := g.AccountInventory.Load(ctx, "a")
				if !reflect.DeepEqual(gotAccount, again) || !reflect.DeepEqual(after, againInv) || !reflect.DeepEqual(gotShared, againShared) {
					t.Fatal("重复跨界重复扣材或碎片")
				}
				t.Logf("装备%d 等级%d 品质%d：材料=%v，对应史诗碎片%d扣%d", id, d.MinimumLevel, d.Rarity, costs, d.EpicPieceID, pieces)
			})
		}
	}
}

func TestNPCAndWaxResealEquipUnequipAndResealKeepVisibleStateConsistent(t *testing.T) {
	items, err := newPVFDungeonDropCatalog(npcEquipmentSource())
	if err != nil {
		t.Fatal(err)
	}
	for _, mode := range []uint16{0, 1} {
		t.Run(fmt.Sprint(mode), func(t *testing.T) {
			s, session, _, g := npcEquipmentFixture(t, items, 3000, 6200, 6100)
			ctx := context.Background()
			if e := g.Equipment.Save(ctx, r.EquipmentRecord{CharacterID: "19", Entries: map[string]r.EquipmentEntry{}}); e != nil {
				t.Fatal(e)
			}
			wear, e := dnfequip.NewOwnerWithPlacementValidator(g, dnfequip.PlacementValidatorFunc(func(context.Context, dnfequip.Placement) error { return nil }))
			if e != nil {
				t.Fatal(e)
			}
			for cycle := byte(1); cycle <= 2; cycle++ {
				if e = s.handleCurrentResetItemAttr(session, npcEquipmentBody81(9, 3000, 65, mode)); e != nil {
					t.Fatal(e)
				}
				i, _, _ := g.Inventory.Load(ctx, "19")
				sealed := i.Slots["0:9"]
				if dnfsvc.SealFlag(sealed) != 1 || dnfsvc.ResealCount(sealed) != cycle || !currentEquipmentRegenerationSourceSealed(sealed) {
					t.Fatal("再封装仍携带上次解封证据", sealed)
				}
				move := dnfequip.MoveCommand{SelectedCharacterID: 19, SourceListType: 0, SourceSlotIndex: 9, SourceInstanceValue: 3000, DestinationListType: 3, DestinationSlotIndex: 12, MoveCount: 1, PlayerRequestFlag: true}
				if _, e = wear.Move(ctx, move); e != nil {
					t.Fatal(e)
				}
				eq, _, _ := g.Equipment.Load(ctx, "19")
				entry := eq.Entries["12"]
				row, ok := currentItemListEntryFromEquipment(entry)
				if !ok || entry.RawEntry[13] != 0 || row.data[13] != 0 || !entry.Bind {
					t.Fatalf("穿戴后封装投影不正确：cycle=%d raw=%d wire=%d bind=%t extra=%v", cycle, entry.RawEntry[13], row.data[13], entry.Bind, entry.Extra)
				}
				move = dnfequip.MoveCommand{SelectedCharacterID: 19, SourceListType: 3, SourceSlotIndex: 12, SourceInstanceValue: 3000, DestinationListType: 0, DestinationSlotIndex: 9, MoveCount: 1, PlayerRequestFlag: true, WornSlotClaimOccurrence: 2}
				if _, e = wear.Move(ctx, move); e != nil {
					t.Fatal(e)
				}
				i, _, _ = g.Inventory.Load(ctx, "19")
				unsealed := i.Slots["0:9"]
				row = currentItemListEntryFromStack(0, 9, unsealed)
				if dnfsvc.SealFlag(unsealed) != 0 || row.data[13] != 0 || dnfsvc.ResealCount(unsealed) != cycle {
					t.Fatal("脱下或重登投影又恢复封装", unsealed)
				}
			}
		})
	}
}
