package dnfbridge

import (
	"bytes"
	"context"
	"encoding/binary"
	"fmt"
	"reflect"
	"testing"

	aiken "longheng.io/server/internal/modules/dnf/aikenupgrade"
	proto "longheng.io/server/internal/modules/dnf/protocol"
	repo "longheng.io/server/internal/modules/dnf/repository"
)

// These are actual dispatcher/transaction/wire regressions using the existing
// checksum-gated real PVF fixture. They do not simulate native inventory objects,
// ACK callbacks, or the upgrade window. Resource selection uses the existing
// DNFBRIDGE_AIKEN_REAL_PVF_SMOKE / DNFBRIDGE_REAL_PVF_SMOKE opt-in.
func TestA06R195RealUpgradeExhaustionThenExchange(t *testing.T) {
	t.Setenv(emptySlotRowEncodingEnv, "current")
	archive := aikenR142Archive(t)
	shop, err := newCurrentNPCShopCatalog(archive)
	if err != nil {
		t.Fatal(err)
	}
	for _, transport := range []string{"legacy", "upper"} {
		for _, tc := range []struct {
			name      string
			initial   int64
			remaining uint32
		}{
			{"exhausted18_then_exchange16", 18, 0},
			{"remaining34_then_exchange16", 34, 16},
		} {
			t.Run(transport+"/"+tc.name, func(t *testing.T) {
				s, session, group, logs, _ := aikenR142Fixture(t, "missing_material")
				s.npcShopCatalog = shop
				session.connID = "a06-exhaustion-" + transport + "-" + tc.name
				// This fixture explicitly selects plain server bodies and the
				// existing channel13 header, not the town fixture's server16.
				if s.options.gameUpperHeader != gameUpperHeaderChannel13 || s.gameUpperBodyCodecMode() != gameUpperBodyCodecPlain {
					t.Fatal("real fixture transport changed; update the parser explicitly")
				}
				rules, err := currentAikenRules(s.pvfItemCatalog)
				if err != nil || rules.Material != 10161536 || rules.Costs[2] != 18 {
					t.Fatalf("real grade1->2 contract differs: material=%d costs=%v err=%v", rules.Material, rules.Costs, err)
				}
				pricing, err := resolveCurrentNPCShopActorPricing(shop, s.pvfItemCatalog, 10161536, 0)
				if err != nil {
					t.Fatal(err)
				}
				if !pricing.Buyable || !pricing.MaterialExchange || pricing.NeedMaterialItem != 10161538 || pricing.NeedMaterialCount <= 0 || pricing.ExchangeGold != 0 {
					t.Fatalf("real 247/actor0 material exchange differs: %+v", pricing)
				}
				const buyCount uint32 = 16
				const costAfter int64 = 20
				const goldBefore int64 = 100000
				if pricing.NeedMaterialCount > (int64(^uint32(0))-costAfter)/int64(buyCount) {
					t.Fatal("real exchange cost exceeds the wire quantity range")
				}
				costBefore := int64(buyCount)*pricing.NeedMaterialCount + costAfter
				before := aikenR142Load(t, group)
				equipment := before.Slots["0:9"]
				if len(equipment.RawEntry) != currentItemListEntryWireSize {
					t.Fatal("real equipment fixture has no complete native row")
				}
				equipment.RawEntry = append([]byte(nil), equipment.RawEntry...)
				equipment.RawEntry[0x76] = 1
				before.Slots["0:9"] = equipment
				// Reserved wallet row is not an exhausted material slot. This
				// checks its actual shop projection, not a new Aiken helper contract.
				before.Slots["0:0"] = repo.ItemStack{ItemID: 0, Count: goldBefore}
				costDefinition, err := s.pvfItemCatalog.ResolveItem(10161538)
				if err != nil {
					t.Fatal(err)
				}
				costSlots, err := grantCurrentCeraShopProductAt(&before, costDefinition, uint32(costBefore), s.gameplayNow())
				if err != nil || len(costSlots) != 1 {
					t.Fatalf("seed real exchange cost: slots=%v err=%v", costSlots, err)
				}
				materialSlots, err := grantCurrentCeraShopProductAt(&before, pricing.Definition, uint32(tc.initial), s.gameplayNow())
				if err != nil || len(materialSlots) != 1 {
					t.Fatalf("seed real upgrade material: slots=%v err=%v", materialSlots, err)
				}
				costSlot, materialSlot := int16(costSlots[0]), int16(materialSlots[0])
				if costSlot <= 0 || materialSlot <= 0 || costSlot == materialSlot || costSlot == 9 || materialSlot == 9 {
					t.Fatalf("invalid seeded endpoints: cost=%d material=%d", costSlot, materialSlot)
				}
				costKey, materialKey := fmt.Sprintf("0:%d", costSlot), fmt.Sprintf("0:%d", materialSlot)
				aikenR142Save(t, group, before)
				before = aikenR142Load(t, group)
				equipmentBefore := before.Slots["0:9"]
				wantRawEquipment := append([]byte(nil), equipmentBefore.RawEntry...)
				wantRawEquipment[0x76] = 2
				wantWireEquipment := currentItemListEntryFromStack(0, 9, equipmentBefore)
				wantWireEquipment.data[0x76] = 2
				conn := session.conn.(*bufferConn)

				a06DispatchFrame(t, s, session, transport, 1342, []byte{9, 0})
				update, rest := splitGameServerUpperPacket(t, conn.write.Bytes())
				ack, tail := splitGameServerUpperPacket(t, rest)
				if update.Header.MsgID != 14 || update.Header.Classification != 0 || ack.Header.MsgID != 1342 || ack.Header.Classification != 1 || !bytes.Equal(ack.Body, []byte{1}) || len(tail) != 0 {
					t.Fatalf("upgrade must send exactly class0/14 then successful class1/1342: update=%+v ack=%+v tail=%d logs=%s", update.Header, ack.Header, len(tail), logs.String())
				}
				rows := a06R195ExhaustionRows(t, update.Body)
				if len(rows) != 2 || !bytes.Equal(rows[9], wantWireEquipment.data[:]) {
					t.Fatalf("upgrade equipment projection changed beyond grade2: rows=%d equipment=%x", len(rows), rows[9])
				}
				materialRow, found := rows[materialSlot]
				if !found {
					t.Fatalf("upgrade14 omitted material slot %d", materialSlot)
				}
				if tc.remaining == 0 {
					// Fixed v157's existing-object delete branch compares itemID
					// to FFFFFFFF. item0/amount1 or item0/amount0 cannot substitute.
					wantEmpty := make([]byte, currentItemListEntryWireSize)
					binary.LittleEndian.PutUint16(wantEmpty, uint16(materialSlot))
					binary.LittleEndian.PutUint32(wantEmpty[2:6], ^uint32(0))
					if !bytes.Equal(materialRow, wantEmpty) {
						t.Fatalf("exhausted nonzero slot must be FFFFFFFF/0 with zero instance tail: slot=%d item=%08x amount=%d row=%x", materialSlot, binary.LittleEndian.Uint32(materialRow[2:6]), binary.LittleEndian.Uint32(materialRow[6:10]), materialRow)
					}
				} else if binary.LittleEndian.Uint32(materialRow[2:6]) != 10161536 || binary.LittleEndian.Uint32(materialRow[6:10]) != tc.remaining {
					t.Fatalf("remaining material must keep its ID and authoritative16: %x", materialRow)
				}
				afterUpgrade := aikenR142Load(t, group)
				if !bytes.Equal(afterUpgrade.Slots["0:9"].RawEntry, wantRawEquipment) || afterUpgrade.Slots["0:9"].ItemID != equipmentBefore.ItemID || afterUpgrade.Slots["0:9"].Count != 1 {
					t.Fatal("upgrade did not preserve the equipment instance except grade1->2")
				}
				material, exists := afterUpgrade.Slots[materialKey]
				if tc.remaining == 0 {
					if exists {
						t.Fatalf("exhausted material remained durable: %+v", material)
					}
				} else if !exists || material.ItemID != 10161536 || material.Count != int64(tc.remaining) {
					t.Fatalf("remaining material durable state differs: %+v", material)
				}
				if !reflect.DeepEqual(afterUpgrade.Slots[costKey], before.Slots[costKey]) || !reflect.DeepEqual(afterUpgrade.Slots["0:0"], before.Slots["0:0"]) {
					t.Fatal("upgrade changed the exchange cost or reserved wallet")
				}

				// Same session, no reselection/login/reload projection or direct
				// domain mutation between the first upgrade and this exchange.
				conn.write.Reset()
				a06DispatchFrame(t, s, session, transport, 21, currentNPCShopBuyTestBody(10161536, buyCount, 247, 0))
				buyACK, rest := splitGameServerUpperPacket(t, conn.write.Bytes())
				if buyACK.Header.MsgID != 21 || buyACK.Header.Classification != 1 || len(buyACK.Body) != 145 || buyACK.Body[0] != 1 {
					t.Fatalf("exchange success ACK missing: header=%+v body=%x logs=%s", buyACK.Header, buyACK.Body, logs.String())
				}
				product := buyACK.Body[17:136]
				targetSlot := int16(binary.LittleEndian.Uint16(product))
				if targetSlot != materialSlot || binary.LittleEndian.Uint32(product[2:6]) != 10161536 || binary.LittleEndian.Uint32(product[6:10]) != buyCount {
					t.Fatalf("ACK must reuse/merge the material slot and carry delta16: initial=%d target=%d product=%x", materialSlot, targetSlot, product)
				}
				if binary.LittleEndian.Uint32(buyACK.Body[1:5]) != uint32(goldBefore) || buyACK.Body[136] != 1 || binary.LittleEndian.Uint32(buyACK.Body[137:141]) != 10161538 || binary.LittleEndian.Uint32(buyACK.Body[141:145]) != uint32(costAfter) {
					t.Fatalf("ACK wallet or real exchange cost remaining differs: %x", buyACK.Body)
				}
				buyUpdate, tail := splitGameServerUpperPacket(t, rest)
				if buyUpdate.Header.MsgID != 14 || buyUpdate.Header.Classification != 0 || len(tail) != 0 {
					t.Fatalf("exchange must send ACK21 then exactly one class0/14: header=%+v trailing=%d", buyUpdate.Header, len(tail))
				}
				buyRows := a06InventoryRows(t, buyUpdate.Body)
				wantCount := tc.remaining + buyCount // exhausted=16; remainder control=32.
				wantRows := map[int16]a06InventoryRow{
					0:            {slot: 0, item: 0, count: uint32(goldBefore)},
					costSlot:     {slot: costSlot, item: 10161538, count: uint32(costAfter)},
					materialSlot: {slot: materialSlot, item: 10161536, count: wantCount},
				}
				if len(buyRows) != len(wantRows) {
					t.Fatalf("exchange14 expected only wallet, cost and product: %+v", buyRows)
				}
				for _, row := range buyRows {
					want, ok := wantRows[row.slot]
					if !ok || row != want {
						t.Fatalf("exchange14 wrong/duplicate/extra row: got=%+v want=%+v", row, want)
					}
					delete(wantRows, row.slot)
				}
				if len(wantRows) != 0 {
					t.Fatalf("exchange14 omitted authoritative rows: %+v", wantRows)
				}
				afterBuy := aikenR142Load(t, group)
				got := afterBuy.Slots[materialKey]
				if got.ItemID != 10161536 || got.Count != int64(wantCount) || got.Extra["last_grant_source"] != "npc_shop" || got.Extra["last_npc_shop_context"] != "247" || got.Extra["last_npc_shop_actor_context"] != "0" {
					t.Fatalf("exchange durable product/context differs: %+v", got)
				}
				cost := afterBuy.Slots[costKey]
				if cost.ItemID != 10161538 || cost.Count != costAfter || before.Slots[costKey].Count-cost.Count != int64(buyCount)*pricing.NeedMaterialCount {
					t.Fatalf("exchange durable cost differs: before=%d after=%+v", before.Slots[costKey].Count, cost)
				}
				if !reflect.DeepEqual(afterBuy.Slots["0:9"], afterUpgrade.Slots["0:9"]) || !reflect.DeepEqual(afterBuy.Slots["0:0"], before.Slots["0:0"]) {
					t.Fatal("exchange altered the grade2 equipment or reserved wallet record")
				}
				character, exists, err := group.Character.Load(context.Background(), "19")
				if err != nil || !exists || character.Stats["gold"] != goldBefore {
					t.Fatalf("zero-gold material exchange changed the character wallet: found=%t gold=%d err=%v", exists, character.Stats["gold"], err)
				}
				t.Logf("actual %s dispatch: grade1->2 consumed18; material_before=%d remaining=%d; same-session21 context247 actor0 delta16; authoritative14=%d; native UI not executed", transport, tc.initial, tc.remaining, wantCount)
			})
		}
	}
}

// Keep the exact row bytes for the native deletion sentinel and equipment
// carrier checks. Reject duplicate slots rather than silently folding a stream.
func a06R195ExhaustionRows(t *testing.T, body []byte) map[int16][]byte {
	t.Helper()
	parsed := a06InventoryRows(t, body)
	rows := make(map[int16][]byte, len(parsed))
	for i, row := range parsed {
		if _, exists := rows[row.slot]; exists {
			t.Fatalf("duplicate upgrade14 slot %d", row.slot)
		}
		rows[row.slot] = append([]byte(nil), body[3+i*currentItemListEntryWireSize:3+(i+1)*currentItemListEntryWireSize]...)
	}
	return rows
}

// The pre-fix Aiken projection clears an ItemID0 stack before projecting it.
// Preserve that existing slot0 behavior here; this change must not turn slot0
// into the nonzero-slot deletion sentinel or claim to fix its wallet Count.
func TestA06R195AikenChangedEntriesKeepsSlotZeroContract(t *testing.T) {
	t.Setenv(emptySlotRowEncodingEnv, "current")
	entries := currentAikenChangedEntries(aiken.Result{Changed: map[int16]repo.ItemStack{
		0: {ItemID: 0, Count: 100000},
	}})
	rows := a06InventoryRows(t, buildCurrentItemUpdateBody(0, entries))
	if len(rows) != 1 || rows[0] != (a06InventoryRow{slot: 0, item: 0, count: 0}) {
		t.Fatalf("preserve the old Aiken slot0 item0/count0 contract, never FFFFFFFF: %+v", rows)
	}
}

// Helpers are scoped to the real dispatcher regression above.
type a06InventoryRow struct {
	slot  int16
	item  uint32
	count uint32
}

func a06InventoryRows(t *testing.T, body []byte) []a06InventoryRow {
	t.Helper()
	if len(body) < 3 || body[0] != byte(repo.MainInventoryListType) {
		t.Fatalf("not a main-container14: %x", body)
	}
	n := int(binary.LittleEndian.Uint16(body[1:3]))
	if len(body) != 3+n*119 {
		t.Fatalf("14 row length=%d count=%d", len(body), n)
	}
	rows := make([]a06InventoryRow, 0, n)
	for i := 0; i < n; i++ {
		r := body[3+i*119 : 3+(i+1)*119]
		rows = append(rows, a06InventoryRow{int16(binary.LittleEndian.Uint16(r)), binary.LittleEndian.Uint32(r[2:6]), binary.LittleEndian.Uint32(r[6:10])})
	}
	return rows
}

func a06DispatchFrame(t *testing.T, s *Service, session *gameSession, transport string, opcode uint16, body []byte) {
	t.Helper()
	var err error
	if transport == "legacy" {
		frame := make([]byte, proto.LegacyGameHeaderSize+len(body))
		frame[0] = 1
		binary.LittleEndian.PutUint16(frame[1:3], opcode)
		binary.LittleEndian.PutUint32(frame[3:7], uint32(len(frame)))
		binary.LittleEndian.PutUint16(frame[11:13], 1)
		copy(frame[proto.LegacyGameHeaderSize:], body)
		err = s.handleLegacyGamePacket(session, frame)
	} else {
		var frame []byte
		frame, err = proto.BuildChannelPacket(opcode, body, 1, 1)
		if err == nil {
			err = s.handleGameUpper(session, frame)
		}
	}
	if err != nil {
		t.Fatal(err)
	}
}
