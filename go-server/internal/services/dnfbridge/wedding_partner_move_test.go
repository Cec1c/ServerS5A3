package dnfbridge

import (
	"context"
	"encoding/binary"
	"fmt"
	"reflect"
	"testing"

	r "longheng.io/server/internal/modules/dnf/repository"
	"longheng.io/server/internal/modules/dnf/town"
	"longheng.io/server/internal/modules/dnf/wedding"
)

func TestWeddingPartnerMoveNativeChannelAndConfirmSlotWire(t *testing.T) {
	query := make([]byte, 21)
	binary.LittleEndian.PutUint32(query[13:], 0x12345678)
	binary.LittleEndian.PutUint32(query[17:], 65)
	token, slot, ok := decodeCurrentWeddingPartnerMoveCheck(query)
	if !ok || token != 0x12345678 || slot != 65 {
		t.Fatal("check shape")
	}
	p := &currentWeddingPartnerMovePending{channel: 10, town: 39, area: 0, x: 700, y: 250}
	ack := buildCurrentWeddingPartnerMoveACK(0, p)
	if len(ack) != 23 || ack[0] != 1 || binary.LittleEndian.Uint16(ack[5:]) != 10 || binary.LittleEndian.Uint32(ack[7:]) != 0 || binary.LittleEndian.Uint32(ack[11:]) != 39 || binary.LittleEndian.Uint16(ack[19:]) != 700 {
		t.Fatalf("channel/reserved/location %x", ack)
	}
	body := make([]byte, 33)
	binary.LittleEndian.PutUint32(body[13:], 66)
	binary.LittleEndian.PutUint32(body[17:], 39)
	binary.LittleEndian.PutUint32(body[25:], 700)
	binary.LittleEndian.PutUint32(body[29:], 250)
	confirm, ok := decodeCurrentWeddingPartnerMoveConfirm(body)
	if !ok || confirm.slot != 66 || confirm.x != 700 || confirm.y != 250 {
		t.Fatal("confirmation must carry its current source slot")
	}
	if _, _, ok = decodeCurrentWeddingPartnerMoveCheck(append(query, 0)); ok {
		t.Fatal("check trailing bytes")
	}
	if _, ok = decodeCurrentWeddingPartnerMoveConfirm(body[:32]); ok {
		t.Fatal("confirm short body")
	}
	binary.LittleEndian.PutUint32(body[13:], 0x10041)
	if _, ok = decodeCurrentWeddingPartnerMoveConfirm(body); ok {
		t.Fatal("truncated u32 slot accepted")
	}
}

// The item/rules come from the supplied matching PVF. Only the ordinary village
// topology is reduced so this exercises the committed op24 owner deterministically.
func TestWeddingPartnerMoveCurrentPVFPreviewConfirmAndReplay(t *testing.T) {
	s, peers, repos := weddingDivorceFixture(t)
	ctx := context.Background()
	catalog, err := town.Load(ctx, townMoveTestSource{
		"town/town.lst": "38 `from.twn` 39 `to.twn`",
		"town/from.twn": "[area]\n0 `Town/Elvengard/village.map` `[normal]`\n[/area]\n",
		"town/to.twn":   "[area]\n0 `Town/HendonMyre/village.map` `[normal]`\n[/area]\n",
	}, town.Options{})
	if err != nil {
		t.Fatal(err)
	}
	s.townCatalog = catalog
	rules, err := s.currentWeddingExtensionRules()
	if err != nil {
		t.Fatal(err)
	}
	definition, err := s.pvfItemCatalog.ResolveItem(uint32(wedding.PartnerMoveItemID))
	if err != nil {
		t.Fatal(err)
	}
	slots := currentPVFItemPlacementPolicy(definition).EmptySlots()
	if len(slots) < 2 {
		t.Fatal("real guard ticket lacks two eligible slots")
	}
	first, second := slots[0], slots[1]
	for i, peer := range peers {
		id := fmt.Sprint(peer.selectedCharacterID)
		c, _, _ := repos.Character.Load(ctx, id)
		c.Stats["town_id"], c.Stats["area_id"] = int64(38+i), 0
		c.Stats["pos_x"], c.Stats["pos_y"], c.Stats["direction"], c.Stats["area_state"] = int64(600+i*100), 250, 5, 3
		if err = r.SaveCharacterFields(ctx, repos.Character, c, r.CharacterFieldStats); err != nil {
			t.Fatal(err)
		}
		peer.townPositionSnapshot = currentTownPositionSnapshot{CharacterID: peer.selectedCharacterID, TownID: byte(38 + i), AreaID: 0, PositionX: uint16(600 + i*100), PositionY: 250, PositionValid: true}
		s.onlinePlayers.EnterArea(&onlinePlayerInfo{CharacterID: peer.selectedCharacterID, AccountID: peer.accountID, Session: peer, TownID: byte(38 + i), AreaID: 0, PositionX: uint16(600 + i*100), PositionY: 250, Direction: 5, AreaState: 3})
	}
	actorID := fmt.Sprint(peers[0].selectedCharacterID)
	enableTownMoveSkillProjection(t, s, repos, actorID)
	stack := r.ItemStack{ItemID: wedding.PartnerMoveItemID, Count: 2}
	if err = repos.Inventory.Save(ctx, r.InventoryRecord{CharacterID: actorID, Slots: map[string]r.ItemStack{experienceItemMainSlotKey(first): stack}}); err != nil {
		t.Fatal(err)
	}
	before, _, _ := repos.Character.Load(ctx, actorID)
	state, err := wedding.Read(before)
	if err != nil {
		t.Fatal(err)
	}
	query := make([]byte, 21)
	binary.LittleEndian.PutUint32(query[13:], currentWeddingRelationToken(state))
	binary.LittleEndian.PutUint32(query[17:], uint32(first))
	peers[0].conn.(*bufferConn).write.Reset()
	if err = s.handleCurrentWeddingPartnerMoveCheck(peers[0], query); err != nil {
		t.Fatal(err)
	}
	ops, bodies := cargoReplies(t, peers[0])
	if len(ops) != 1 || ops[0] != 1043 || binary.LittleEndian.Uint32(bodies[0][1:]) != 0 || binary.LittleEndian.Uint16(bodies[0][5:]) != 10 {
		t.Fatalf("preview ACK %v %x", ops, bodies)
	}
	afterPreview, _, _ := repos.Character.Load(ctx, actorID)
	if !reflect.DeepEqual(before, afterPreview) {
		t.Fatal("preview mutated persisted actor")
	}
	// Native confirmation looks up the same ticket again, so a legitimate bag
	// rearrangement may change its slot between the two requests.
	bag, _, _ := repos.Inventory.Load(ctx, actorID)
	delete(bag.Slots, experienceItemMainSlotKey(first))
	bag.Slots[experienceItemMainSlotKey(second)] = stack
	if err = repos.Inventory.Save(ctx, bag); err != nil {
		t.Fatal(err)
	}
	confirm := make([]byte, 33)
	binary.LittleEndian.PutUint32(confirm[13:], uint32(second))
	binary.LittleEndian.PutUint32(confirm[17:], 39)
	binary.LittleEndian.PutUint32(confirm[25:], 700)
	binary.LittleEndian.PutUint32(confirm[29:], 250)
	wrong := append([]byte(nil), confirm...)
	binary.LittleEndian.PutUint32(wrong[25:], 701)
	peers[0].conn.(*bufferConn).write.Reset()
	if err = s.handleCurrentWeddingPartnerMove(peers[0], wrong); err != nil {
		t.Fatal(err)
	}
	afterWrong, _, _ := repos.Character.Load(ctx, actorID)
	if !reflect.DeepEqual(before, afterWrong) {
		t.Fatal("altered confirmation changed location or cooldown")
	}
	peers[0].conn.(*bufferConn).write.Reset()
	if err = s.handleCurrentWeddingPartnerMove(peers[0], confirm); err != nil {
		t.Fatal(err)
	}
	ops, bodies = cargoReplies(t, peers[0])
	transitions, slotRefreshes := 0, 0
	for _, op := range ops {
		if op == 24 {
			transitions++
		}
		if op == 14 {
			slotRefreshes++
		}
	}
	if transitions != 1 || slotRefreshes != 1 || len(ops) == 0 || ops[len(ops)-1] != 1044 || binary.LittleEndian.Uint32(bodies[len(bodies)-1][1:]) != 0 {
		t.Fatalf("committed trip %v", ops)
	}
	afterMove, _, _ := repos.Character.Load(ctx, actorID)
	bag, _, _ = repos.Inventory.Load(ctx, actorID)
	remaining := stack
	remaining.Count--
	if afterMove.Stats["town_id"] != 39 || afterMove.Stats["area_id"] != 0 || afterMove.Stats["pos_x"] != 700 || afterMove.Stats["pos_y"] != 250 || afterMove.Stats["wedding_v1_partner_move_at_ms"] != s.gameplayNow().UnixMilli() || !reflect.DeepEqual(bag.Slots[experienceItemMainSlotKey(second)], remaining) || rules.PartnerMove.CooldownMilliseconds != 1000 {
		t.Fatalf("location/cooldown/voucher %+v %+v", afterMove.Stats, bag.Slots)
	}
	peers[0].conn.(*bufferConn).write.Reset()
	if err = s.handleCurrentWeddingPartnerMove(peers[0], confirm); err != nil {
		t.Fatal(err)
	}
	ops, bodies = cargoReplies(t, peers[0])
	if len(ops) != 1 || ops[0] != 1044 || binary.LittleEndian.Uint32(bodies[0][1:]) != 0 {
		t.Fatalf("duplicate scene or failed replay %v %x", ops, bodies)
	}
	afterReplay, _, _ := repos.Character.Load(ctx, actorID)
	if !reflect.DeepEqual(afterMove, afterReplay) {
		t.Fatal("confirmation replay mutated actor")
	}
}
