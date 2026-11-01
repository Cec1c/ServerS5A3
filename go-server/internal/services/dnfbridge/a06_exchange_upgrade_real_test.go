package dnfbridge

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"reflect"
	"strings"
	"testing"

	proto "longheng.io/server/internal/modules/dnf/protocol"
	repo "longheng.io/server/internal/modules/dnf/repository"
)

// Candidate only: not run by the sidecar. Uses the existing checksum-gated
// Aiken archive and instance fixture; never substitutes a synthetic catalog.
// ShopContext247/ActorContext0 is also present in the 04:06 durable snapshot.
// This is an immediate server request regression, not a GUI click simulation.
func TestA06RealExchange247ThenImmediateUpgrade(t *testing.T) {
	archive := aikenR142Archive(t)
	shop, err := newCurrentNPCShopCatalog(archive)
	if err != nil {
		t.Fatal(err)
	}
	for _, transport := range []string{"legacy", "upper"} {
		for _, scenario := range []string{"merge_existing", "new_slot_cost_remains", "reuse_exhausted_cost_slot"} {
			t.Run(transport+"/"+scenario, func(t *testing.T) {
				s, session, group, logs, _ := aikenR142Fixture(t, "missing_material")
				s.npcShopCatalog = shop
				session.connID = "a06-exchange-247-" + transport + "-" + scenario
				pricing, err := resolveCurrentNPCShopActorPricing(shop, s.pvfItemCatalog, 10161536, 0)
				if err != nil {
					t.Fatal(err)
				}
				if !pricing.Buyable || !pricing.MaterialExchange || pricing.NeedMaterialItem != 10161538 || pricing.NeedMaterialCount <= 0 {
					t.Fatalf("real exchange contract differs: %+v", pricing)
				}
				rules, err := currentAikenRules(s.pvfItemCatalog)
				if err != nil || rules.Material != 10161536 || rules.Costs[1] != 11 {
					t.Fatalf("real grade0->1 material contract: %+v err=%v", rules, err)
				}
				initialTarget := int64(0)
				if scenario == "merge_existing" {
					initialTarget = 5
				}
				// All cases reach16 before upgrading and5 afterwards. The 04:04
				// snapshot's16 is context, not evidence of the 03:36 pre-state.
				buy := uint32(16 - initialTarget)
				costAfter := int64(20)
				if scenario == "reuse_exhausted_cost_slot" {
					costAfter = 0
				}
				before := aikenR142Load(t, group)
				costDefinition, err := s.pvfItemCatalog.ResolveItem(10161538)
				if err != nil {
					t.Fatal(err)
				}
				costBefore := int64(buy)*pricing.NeedMaterialCount + costAfter
				if costBefore <= 0 || costBefore > int64(^uint32(0)) {
					t.Fatalf("unexpected resource cost=%d", costBefore)
				}
				costSlots, err := grantCurrentCeraShopProductAt(&before, costDefinition, uint32(costBefore), s.gameplayNow())
				if err != nil || len(costSlots) != 1 {
					t.Fatalf("seed real cost: slots=%v err=%v", costSlots, err)
				}
				costSlot := int16(costSlots[0])
				targetBeforeSlot := int16(-1)
				if initialTarget > 0 {
					slots, err := grantCurrentCeraShopProductAt(&before, pricing.Definition, uint32(initialTarget), s.gameplayNow())
					if err != nil || len(slots) != 1 {
						t.Fatalf("seed real target: slots=%v err=%v", slots, err)
					}
					targetBeforeSlot = int16(slots[0])
				}
				aikenR142Save(t, group, before)
				equipmentBefore := append([]byte(nil), before.Slots["0:9"].RawEntry...)
				request := currentNPCShopBuyTestBody(10161536, buy, 247, 0)
				a06DispatchFrame(t, s, session, transport, 21, request)
				conn := session.conn.(*bufferConn)
				ack, rest := splitGameServerUpperPacket(t, conn.write.Bytes())
				if ack.Header.MsgID != 21 || ack.Header.Classification != 1 || len(ack.Body) != 1+16+119+1+8 || ack.Body[0] != 1 {
					t.Fatalf("op21 success missing: header=%+v body=%x logs=%s", ack.Header, ack.Body, logs.String())
				}
				product := ack.Body[17:136]
				targetSlot := int16(binary.LittleEndian.Uint16(product))
				if binary.LittleEndian.Uint32(product[2:6]) != 10161536 || binary.LittleEndian.Uint32(product[6:10]) != buy || ack.Body[136] != 1 || binary.LittleEndian.Uint32(ack.Body[137:141]) != 10161538 || binary.LittleEndian.Uint32(ack.Body[141:145]) != uint32(costAfter) {
					t.Fatalf("ACK identity/delta/cost differs: %x", ack.Body)
				}
				if scenario == "merge_existing" && targetSlot != targetBeforeSlot {
					t.Fatalf("real target did not merge: got=%d want=%d", targetSlot, targetBeforeSlot)
				}
				reused := targetSlot == costSlot
				if reused != (scenario == "reuse_exhausted_cost_slot") {
					t.Fatalf("scenario not reached: cost=%d target=%d", costSlot, targetSlot)
				}
				update, tail := splitGameServerUpperPacket(t, rest)
				if len(tail) != 0 || update.Header.MsgID != 14 || update.Header.Classification != 0 {
					t.Fatalf("expected ACK21 then exactly one class0/14: %+v trailing=%d", update.Header, len(tail))
				}
				rows := a06InventoryRows(t, update.Body)
				// Replay the documented ACK effects, then every14 row in wire
				// order. This model checks projection, not native UI callbacks.
				projected := map[int16]a06InventoryRow{}
				if costAfter > 0 {
					projected[costSlot] = a06InventoryRow{costSlot, 10161538, uint32(costAfter)}
				}
				projected[targetSlot] = a06InventoryRow{targetSlot, 10161536, 16}
				seen := map[int16]int{}
				for i, row := range rows {
					seen[row.slot]++
					t.Logf("14 row=%d slot=%d item=%d count=%d duplicate=%t", i, row.slot, row.item, row.count, seen[row.slot] > 1)
					if row.item == ^uint32(0) {
						delete(projected, row.slot)
					} else {
						projected[row.slot] = row
					}
				}
				wantRows := 1
				if reused {
					wantRows = 2 // Current projection: deletion followed by final row.
				}
				if seen[targetSlot] != wantRows || projected[targetSlot].item != 10161536 || projected[targetSlot].count != 16 {
					t.Fatalf("final14 target or duplicate-slot contract: rows=%+v state=%+v", rows, projected)
				}
				if !reused && (projected[costSlot].item != 10161538 || projected[costSlot].count != uint32(costAfter)) {
					t.Fatalf("final14 cost differs: %+v", projected)
				}
				afterBuy := aikenR142Load(t, group)
				stack := afterBuy.Slots[fmt.Sprintf("0:%d", targetSlot)]
				if stack.ItemID != 10161536 || stack.Count != 16 || stack.Extra["last_npc_shop_context"] != "247" || stack.Extra["last_npc_shop_actor_context"] != "0" || stack.Extra["last_grant_source"] != "npc_shop" {
					t.Fatalf("durable target/real context mismatch: %+v", stack)
				}
				if !bytes.Equal(afterBuy.Slots["0:9"].RawEntry, equipmentBefore) {
					t.Fatal("exchange changed selected equipment")
				}
				if !reused {
					cost := afterBuy.Slots[fmt.Sprintf("0:%d", costSlot)]
					if cost.ItemID != 10161538 || cost.Count != costAfter {
						t.Fatalf("durable cost differs: %+v", cost)
					}
				}
				// No new session, character reselection, login snapshot or
				// direct owner invocation between21 and1342.
				conn.write.Reset()
				a06DispatchFrame(t, s, session, transport, 1342, []byte{9, 0})
				aikenR142Outcome(t, session, true)
				afterUpgrade := aikenR142Load(t, group)
				remaining := afterUpgrade.Slots[fmt.Sprintf("0:%d", targetSlot)]
				if remaining.ItemID != 10161536 || remaining.Count != 5 {
					t.Fatalf("immediate upgrade material=%+v logs=%s", remaining, logs.String())
				}
				wantEquipment := append([]byte(nil), equipmentBefore...)
				wantEquipment[0x76] = 1
				if !bytes.Equal(afterUpgrade.Slots["0:9"].RawEntry, wantEquipment) {
					t.Fatal("grade or opaque equipment bytes differ")
				}
				upgradeUpdate, _ := splitGameServerUpperPacket(t, conn.write.Bytes())
				materialFound := false
				for _, row := range a06InventoryRows(t, upgradeUpdate.Body) {
					if row.slot == targetSlot && row.item == 10161536 && row.count == 5 {
						materialFound = true
					}
				}
				if !materialFound {
					t.Fatal("upgrade14 omitted final material5")
				}
				// Concrete insufficient-material control: next grade needs18,
				// only5 remain. Failure must not mutate the post-upgrade state.
				conn.write.Reset()
				a06DispatchFrame(t, s, session, transport, 1342, []byte{9, 0})
				aikenR142OutcomeReason(t, session, false, 8)
				if !reflect.DeepEqual(afterUpgrade, aikenR142Load(t, group)) || !strings.Contains(logs.String(), "materials_insufficient") {
					t.Fatalf("expected unchanged grade1/material5 rejection: logs=%s", logs.String())
				}
				t.Logf("exchange item=10161536 count=%d context=247 actor=0 target_slot=%d cost_slot=%d; immediate1342 need=%d have=16 success; next1342 need=%d have=5 rejected", buy, targetSlot, costSlot, rules.Costs[1], rules.Costs[2])
			})
		}
	}
}

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
