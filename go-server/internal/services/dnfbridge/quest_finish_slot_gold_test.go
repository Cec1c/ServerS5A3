package dnfbridge

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"testing"

	"longheng.io/server/internal/modules/dnf/dnfenum"
	dnfproto "longheng.io/server/internal/modules/dnf/protocol"
	dnfpvf "longheng.io/server/internal/modules/dnf/pvf"
	dnfquest "longheng.io/server/internal/modules/dnf/quest"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

func finishSlotGoldResult(index uint32, bit byte) dnfquest.FinishCommitResult {
	return dnfquest.FinishCommitResult{
		AtomicCommitted:    true,
		QuestID:            6100,
		CompletionKey:      fmt.Sprintf("quest-finish/19/6100/slot-%d", index),
		Source:             "quest_pvf:n_quest/paid_slot.qst",
		ExperienceDelta:    300,
		HasSlotExpansion:   true,
		SlotExpansionIndex: index,
		SlotExpansionBit:   bit,
	}
}

func TestBuildCurrentFinishQuestSuccessBodySlotExpansionAllowsGoldDebit(t *testing.T) {
	tests := []struct {
		name  string
		index uint32
		bit   byte
	}{
		{name: "support", index: 0, bit: 0x01},
		{name: "magic_stone", index: 1, bit: 0x02},
		{name: "earring", index: 2, bit: 0x10},
	}
	want := []byte{
		0xd4, 0x17, // questID
		0x00,                   // completionType
		0x2c, 0x01, 0x00, 0x00, // EXP
		0x00, // consumedCount
		0x00, // ordinary chainType
		0x00, // item count
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			base := finishSlotGoldResult(test.index, test.bit)
			for _, currency := range [][]dnfquest.FinishCommittedCurrency{
				nil,
				{{Name: "gold", Delta: -300, PostValue: 0}},
				{{Name: "gold", Delta: -300000, PostValue: 77}},
			} {
				result := base
				result.Currency = currency
				body, err := buildCurrentFinishQuestSuccessBody(result)
				if err != nil {
					t.Fatalf("currency=%+v: %v", currency, err)
				}
				if !bytes.Equal(body, want) {
					t.Fatalf("currency=%+v changed slot ACK layout: got %x want %x", currency, body, want)
				}
			}
		})
	}
}

func TestBuildCurrentFinishQuestSuccessBodySlotExpansionRejectsInvalidRewardReceipts(t *testing.T) {
	tests := []struct {
		name               string
		currency           []dnfquest.FinishCommittedCurrency
		items              []dnfquest.FinishCommittedItem
		slotExpansionIndex uint32
		slotExpansionBit   byte
	}{
		{name: "positive_delta", currency: []dnfquest.FinishCommittedCurrency{{Name: "gold", Delta: 1, PostValue: 1}}},
		{name: "zero_delta", currency: []dnfquest.FinishCommittedCurrency{{Name: "gold", Delta: 0, PostValue: 0}}},
		{name: "negative_balance", currency: []dnfquest.FinishCommittedCurrency{{Name: "gold", Delta: -1, PostValue: -1}}},
		{name: "unknown_currency", currency: []dnfquest.FinishCommittedCurrency{{Name: "coin", Delta: -1, PostValue: 0}}},
		{name: "duplicate_gold", currency: []dnfquest.FinishCommittedCurrency{
			{Name: "gold", Delta: -1, PostValue: 0},
			{Name: "gold", Delta: -1, PostValue: 0},
		}},
		{name: "gold_with_item_reward", currency: []dnfquest.FinishCommittedCurrency{{Name: "gold", Delta: -1, PostValue: 0}}, items: []dnfquest.FinishCommittedItem{{ItemID: 10403}}},
		{name: "wrong_slot_mask", slotExpansionIndex: 2, slotExpansionBit: dnfquest.ExEquipSlotMagicStone},
		{name: "unknown_slot_index", slotExpansionIndex: 3, slotExpansionBit: 0},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			result := finishSlotGoldResult(2, dnfquest.ExEquipSlotEarring)
			result.Currency = test.currency
			result.Items = test.items
			if test.slotExpansionIndex != 0 || test.slotExpansionBit != 0 {
				result.SlotExpansionIndex = test.slotExpansionIndex
				result.SlotExpansionBit = test.slotExpansionBit
			}
			if body, err := buildCurrentFinishQuestSuccessBody(result); !errors.Is(err, errCurrentFinishQuestAckShape) {
				t.Fatalf("body=%x error=%v", body, err)
			}
		})
	}
}

func TestCurrentFinishQuestSlotExpansionGoldDebitPublishesCommittedAckAndRefresh(t *testing.T) {
	const questID = uint16(6100)
	tests := []struct {
		name  string
		index uint32
		bit   byte
		cost  int64
	}{
		{name: "support", index: 0, bit: 0x01, cost: 300},
		{name: "magic_stone", index: 1, bit: 0x02, cost: 800},
		{name: "earring", index: 2, bit: 0x10, cost: 1000},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			ctx := context.Background()
			repositories := finishBridgeSeed(t)
			character, found, err := repositories.Character.Load(ctx, "19")
			if err != nil || !found {
				t.Fatalf("load character found=%t err=%v", found, err)
			}
			character.Stats["gold"] = test.cost + 77
			character.Stats["ex_equip_slot_stat"] = 0x08 // An unrelated existing slot must survive.
			if err := repositories.Character.Save(ctx, character); err != nil {
				t.Fatal(err)
			}
			if err := repositories.Inventory.Save(ctx, dnfrepo.InventoryRecord{
				CharacterID: "19",
				Slots: map[string]dnfrepo.ItemStack{
					"0:11": {ItemID: 9001, Count: 5},
				},
			}); err != nil {
				t.Fatal(err)
			}
			if err := repositories.Quest.Save(ctx, dnfrepo.QuestRecord{
				CharacterID: "19",
				States: map[int64]dnfrepo.QuestState{
					6100: {
						Status: "active",
						Extra: map[string]string{
							"completion_key":  fmt.Sprintf("slot-gold-%d", test.index),
							"completion_kind": "clear_map",
							"reward_state":    "pending",
						},
					},
				},
			}); err != nil {
				t.Fatal(err)
			}
			catalog := finishSlotGoldCatalog(t, test.index, test.cost)
			connection := &bufferConn{}
			service := &Service{
				options:            options{gameUpperBodyCodec: gameUpperBodyCodecPlain},
				questCatalog:       catalog,
				repositoryProvider: func() (dnfrepo.Group, bool) { return repositories, true },
			}
			session := &gameSession{
				conn:                            connection,
				connID:                          fmt.Sprintf("slot-gold-%d", test.index),
				selectedCharacterID:             19,
				connectionTownActorOwnerChannel: 253,
				townActorOwnerChannel:           253,
			}
			request := dnfquest.FinishQuestRequest{
				QuestID:    questID,
				Multiplier: 1,
				Reserved:   dnfquest.CurrentFinishQuestObservedTailMarker,
			}
			if err := service.handleCurrentFinishQuestWithResources(
				session, request, repositories, catalog, finishBridgeProgression(t), nil,
			); err != nil {
				t.Fatalf("first finish: %v", err)
			}
			packets := splitAllFinishBridgePackets(t, connection.write.Bytes())
			if len(packets) == 0 {
				t.Fatal("committed paid slot quest emitted no ACK or refresh")
			}

			ackIndex, itemIndex, mode1Index := -1, -1, -1
			activeIndex, clearIndex, acceptableIndex := -1, -1, -1
			for index, packet := range packets {
				switch {
				case packet.Header.Classification == dnfproto.DefaultChannelClassification && packet.Header.MsgID == uint16(dnfenum.CmdPacketFinishQuest):
					ackIndex = index
				case packet.Header.Classification == 0 && packet.Header.MsgID == uint16(dnfenum.CmdPacketWalkoutPartyMember):
					itemIndex = index
				case packet.Header.Classification == 0 && packet.Header.MsgID == uint16(dnfenum.CmdPacketSetUDPIPPort) && len(packet.Body) != 0 && packet.Body[0] == 1:
					mode1Index = index
				case packet.Header.Classification == 0 && packet.Header.MsgID == currentActiveQuestSnapshotMsgID:
					activeIndex = index
				case packet.Header.Classification == 0 && packet.Header.MsgID == currentClearQuestListMsgID:
					clearIndex = index
				case packet.Header.Classification == 0 && packet.Header.MsgID == currentAcceptableQuestListMsgID:
					acceptableIndex = index
				}
			}
			if ackIndex != 0 || !(itemIndex > ackIndex && mode1Index > itemIndex &&
				activeIndex > mode1Index && clearIndex > activeIndex && acceptableIndex > clearIndex) {
				t.Fatalf("post-commit order ack=%d item=%d mode1=%d active=%d clear=%d acceptable=%d ids=%v",
					ackIndex, itemIndex, mode1Index, activeIndex, clearIndex, acceptableIndex, finishBridgePacketIDs(packets))
			}

			ack := packets[ackIndex].Body
			if len(ack) != 18 || ack[0] != 1 ||
				binary.LittleEndian.Uint16(ack[1:3]) != questID ||
				ack[8] != 1 || ack[9] != 0 ||
				binary.LittleEndian.Uint16(ack[10:12]) != 11 ||
				binary.LittleEndian.Uint32(ack[12:16]) != 2 ||
				ack[16] != 0 || ack[17] != 0 {
				t.Fatalf("slot/gold/consumed ACK=%x", ack)
			}

			itemBody := packets[itemIndex].Body
			if len(itemBody) != 3+2*currentItemListEntryWireSize || itemBody[0] != 0 ||
				binary.LittleEndian.Uint16(itemBody[1:3]) != 2 ||
				binary.LittleEndian.Uint16(itemBody[3:5]) != 0 ||
				binary.LittleEndian.Uint32(itemBody[5:9]) != 0 ||
				binary.LittleEndian.Uint32(itemBody[9:13]) != 77 {
				t.Fatalf("committed wallet/material refresh=%x", itemBody)
			}
			material := itemBody[3+currentItemListEntryWireSize:]
			if binary.LittleEndian.Uint16(material[0:2]) != 11 ||
				binary.LittleEndian.Uint32(material[2:6]) != 9001 ||
				binary.LittleEndian.Uint32(material[6:10]) != 3 {
				t.Fatalf("committed material refresh=%x", material[:10])
			}

			start := connection.write.Len()
			if err := service.handleCurrentFinishQuestWithResources(
				session, request, repositories, catalog, finishBridgeProgression(t), nil,
			); err != nil {
				t.Fatalf("same-session duplicate: %v", err)
			}
			if duplicate := connection.write.Bytes()[start:]; len(duplicate) != 0 {
				t.Fatalf("same-session duplicate emitted %d bytes: %x", len(duplicate), duplicate)
			}

			after, found, err := repositories.Character.Load(ctx, "19")
			if err != nil || !found {
				t.Fatalf("reload character found=%t err=%v", found, err)
			}
			quests, found, err := repositories.Quest.Load(ctx, "19")
			if err != nil || !found {
				t.Fatalf("reload quest found=%t err=%v", found, err)
			}
			inventory, found, err := repositories.Inventory.Load(ctx, "19")
			if err != nil || !found {
				t.Fatalf("reload inventory found=%t err=%v", found, err)
			}
			if after.Stats["gold"] != 77 || after.Stats["ex_equip_slot_stat"] != int64(test.bit|0x08) ||
				quests.States[6100].Status != "completed" || inventory.Slots["0:11"].Count != 3 {
				t.Fatalf("committed state gold=%d slot=%#x quest=%s material=%d",
					after.Stats["gold"], after.Stats["ex_equip_slot_stat"],
					quests.States[6100].Status, inventory.Slots["0:11"].Count)
			}
		})
	}
}

func TestCurrentFinishQuestSlotExpansionShortagesLeavePersistedStateUnchanged(t *testing.T) {
	tests := []struct {
		name      string
		index     uint32
		cost      int64
		gold      int64
		materials int64
	}{
		{name: "support_gold_short", index: 0, cost: 300, gold: 299, materials: 5},
		{name: "magic_stone_gold_short", index: 1, cost: 800, gold: 799, materials: 5},
		{name: "earring_gold_short", index: 2, cost: 1000, gold: 999, materials: 5},
		{name: "support_material_short", index: 0, cost: 300, gold: 377, materials: 1},
		{name: "magic_stone_material_short", index: 1, cost: 800, gold: 877, materials: 1},
		{name: "earring_material_short", index: 2, cost: 1000, gold: 1077, materials: 1},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := newFinishSlotGoldFixture(t, test.index, test.cost, test.gold, test.materials)
			if err := fixture.finish(t, nil); err != nil {
				t.Fatalf("shortage finish: %v", err)
			}
			packets := splitAllFinishBridgePackets(t, fixture.connection.write.Bytes())
			if len(packets) != 1 || packets[0].Header.Classification != dnfproto.DefaultChannelClassification ||
				packets[0].Header.MsgID != uint16(dnfenum.CmdPacketFinishQuest) ||
				!bytes.Equal(packets[0].Body, []byte{0, 22}) {
				t.Fatalf("shortage response packets=%v body=%x", finishBridgePacketIDs(packets), packetBody(packets))
			}

			ctx := context.Background()
			character, found, err := fixture.repositories.Character.Load(ctx, "19")
			if err != nil || !found {
				t.Fatalf("reload character found=%t err=%v", found, err)
			}
			quests, found, err := fixture.repositories.Quest.Load(ctx, "19")
			if err != nil || !found {
				t.Fatalf("reload quest found=%t err=%v", found, err)
			}
			inventory, found, err := fixture.repositories.Inventory.Load(ctx, "19")
			if err != nil || !found {
				t.Fatalf("reload inventory found=%t err=%v", found, err)
			}
			state := quests.States[6100]
			stack := inventory.Slots["0:11"]
			if len(quests.States) != 1 || len(inventory.Slots) != 1 ||
				character.Level != 1 || character.Stats["exp"] != 0 || character.Stats["grow_type"] != 0 ||
				character.Stats["gold"] != test.gold || character.Stats["ex_equip_slot_stat"] != 0 ||
				stack.ItemID != 9001 || stack.Count != test.materials ||
				state.Status != "active" || state.Extra["completion_kind"] != "clear_map" ||
				state.Extra["reward_state"] != "pending" {
				t.Fatalf("rejected shortage mutated gold=%d slot=%#x material=%+v quest=%+v",
					character.Stats["gold"], character.Stats["ex_equip_slot_stat"], stack, state)
			}
		})
	}
}

func TestCurrentFinishQuestSlotExpansionZeroByteACKFailureRetriesWithoutDoubleDebit(t *testing.T) {
	fixture := newFinishSlotGoldFixture(t, 2, 1000, 1077, 5)
	writeErr := errors.New("injected zero-byte finish ACK failure")
	connection := &failNthDungeonWriteConn{failAt: 1, err: writeErr}
	fixture.session.conn = connection

	if err := fixture.finish(t, nil); !errors.Is(err, writeErr) {
		t.Fatalf("first ACK error=%v want=%v", err, writeErr)
	}
	if connection.writes != 1 || connection.write.Len() != 0 {
		t.Fatalf("zero-byte ACK failure attempts=%d accepted-bytes=%d", connection.writes, connection.write.Len())
	}
	failed := splitAllFinishBridgePackets(t, connection.failed)
	if len(failed) != 1 || failed[0].Header.Classification != dnfproto.DefaultChannelClassification ||
		failed[0].Header.MsgID != uint16(dnfenum.CmdPacketFinishQuest) ||
		len(failed[0].Body) == 0 || failed[0].Body[0] != 1 {
		t.Fatalf("failed write was not the finish ACK: packets=%v body=%x",
			finishBridgePacketIDs(failed), packetBody(failed))
	}
	assertFinishSlotGoldPersistedState(t, fixture.repositories, 77, int64(dnfquest.ExEquipSlotEarring), 3, "completed")

	if err := fixture.finish(t, nil); err != nil {
		t.Fatalf("retry after zero-byte ACK failure: %v", err)
	}
	assertFinishSlotGoldSuccessStream(t, splitAllFinishBridgePackets(t, connection.write.Bytes()), 6100, 77, 3)
	assertFinishSlotGoldPersistedState(t, fixture.repositories, 77, int64(dnfquest.ExEquipSlotEarring), 3, "completed")
}

func TestCurrentFinishQuestSlotExpansionRefreshFailureRecoversOnFreshSession(t *testing.T) {
	fixture := newFinishSlotGoldFixture(t, 2, 1000, 1077, 5)
	writeErr := errors.New("injected post-ACK refresh write failure")
	originalConnection := &failNthDungeonWriteConn{failAt: 2, err: writeErr}
	fixture.session.conn = originalConnection

	if err := fixture.finish(t, nil); !errors.Is(err, writeErr) {
		t.Fatalf("refresh write error=%v want=%v", err, writeErr)
	}
	if originalConnection.writes != 2 {
		t.Fatalf("post-ACK failure attempts=%d want=2", originalConnection.writes)
	}
	ackPackets := splitAllFinishBridgePackets(t, originalConnection.write.Bytes())
	if len(ackPackets) != 1 || ackPackets[0].Header.Classification != dnfproto.DefaultChannelClassification ||
		ackPackets[0].Header.MsgID != uint16(dnfenum.CmdPacketFinishQuest) ||
		len(ackPackets[0].Body) == 0 || ackPackets[0].Body[0] != 1 {
		t.Fatalf("ACK-before-refresh-failure packets=%v body=%x", finishBridgePacketIDs(ackPackets), packetBody(ackPackets))
	}
	failedRefresh := splitAllFinishBridgePackets(t, originalConnection.failed)
	if len(failedRefresh) != 1 || failedRefresh[0].Header.Classification != 0 ||
		failedRefresh[0].Header.MsgID != uint16(dnfenum.CmdPacketWalkoutPartyMember) {
		t.Fatalf("second write was not the item refresh: packets=%v", finishBridgePacketIDs(failedRefresh))
	}
	assertFinishSlotGoldPersistedState(t, fixture.repositories, 77, int64(dnfquest.ExEquipSlotEarring), 3, "completed")

	reconnectConnection := &bufferConn{}
	reconnect := &gameSession{
		conn:                            reconnectConnection,
		connID:                          "slot-gold-earring-reconnect",
		selectedCharacterID:             19,
		connectionTownActorOwnerChannel: 253,
		townActorOwnerChannel:           253,
	}
	if err := fixture.finish(t, reconnect); err != nil {
		t.Fatalf("reconnect finish: %v", err)
	}
	assertFinishSlotGoldSuccessStream(t, splitAllFinishBridgePackets(t, reconnectConnection.write.Bytes()), 6100, 77, 3)
	assertFinishSlotGoldPersistedState(t, fixture.repositories, 77, int64(dnfquest.ExEquipSlotEarring), 3, "completed")

	originalBytes := originalConnection.write.Len()
	originalWrites := originalConnection.writes
	if err := fixture.finish(t, fixture.session); err != nil {
		t.Fatalf("original-session duplicate after reconnect: %v", err)
	}
	if originalConnection.writes != originalWrites || originalConnection.write.Len() != originalBytes {
		t.Fatalf("original-session duplicate wrote again: attempts=%d bytes=%d want=%d/%d",
			originalConnection.writes, originalConnection.write.Len(), originalWrites, originalBytes)
	}
	if packets := splitAllFinishBridgePackets(t, originalConnection.write.Bytes()); len(packets) != 1 ||
		packets[0].Header.MsgID != uint16(dnfenum.CmdPacketFinishQuest) {
		t.Fatalf("original session received a second ACK: packets=%v", finishBridgePacketIDs(packets))
	}
	assertFinishSlotGoldPersistedState(t, fixture.repositories, 77, int64(dnfquest.ExEquipSlotEarring), 3, "completed")
}

type finishSlotGoldFixture struct {
	repositories dnfrepo.Group
	connection   *bufferConn
	service      *Service
	session      *gameSession
	request      dnfquest.FinishQuestRequest
}

func newFinishSlotGoldFixture(t *testing.T, index uint32, cost, gold, materials int64) *finishSlotGoldFixture {
	t.Helper()
	ctx := context.Background()
	repositories := finishBridgeSeed(t)
	character, found, err := repositories.Character.Load(ctx, "19")
	if err != nil || !found {
		t.Fatalf("load character found=%t err=%v", found, err)
	}
	character.Stats["gold"] = gold
	character.Stats["ex_equip_slot_stat"] = 0
	if err := repositories.Character.Save(ctx, character); err != nil {
		t.Fatal(err)
	}
	if err := repositories.Inventory.Save(ctx, dnfrepo.InventoryRecord{
		CharacterID: "19",
		Slots: map[string]dnfrepo.ItemStack{
			"0:11": {ItemID: 9001, Count: materials},
		},
	}); err != nil {
		t.Fatal(err)
	}
	if err := repositories.Quest.Save(ctx, dnfrepo.QuestRecord{
		CharacterID: "19",
		States: map[int64]dnfrepo.QuestState{
			6100: {
				Status: "active",
				Extra: map[string]string{
					"completion_key":  fmt.Sprintf("slot-gold-failure-%d", index),
					"completion_kind": "clear_map",
					"reward_state":    "pending",
				},
			},
		},
	}); err != nil {
		t.Fatal(err)
	}
	catalog := finishSlotGoldCatalog(t, index, cost)
	connection := &bufferConn{}
	return &finishSlotGoldFixture{
		repositories: repositories,
		connection:   connection,
		service: &Service{
			options:            options{gameUpperBodyCodec: gameUpperBodyCodecPlain},
			questCatalog:       catalog,
			repositoryProvider: func() (dnfrepo.Group, bool) { return repositories, true },
		},
		session: &gameSession{
			conn:                            connection,
			connID:                          fmt.Sprintf("slot-gold-failure-%d", index),
			selectedCharacterID:             19,
			connectionTownActorOwnerChannel: 253,
			townActorOwnerChannel:           253,
		},
		request: dnfquest.FinishQuestRequest{
			QuestID:    6100,
			Multiplier: 1,
			Reserved:   dnfquest.CurrentFinishQuestObservedTailMarker,
		},
	}
}

func (fixture *finishSlotGoldFixture) finish(t *testing.T, session *gameSession) error {
	t.Helper()
	if session == nil {
		session = fixture.session
	}
	return fixture.service.handleCurrentFinishQuestWithResources(
		session,
		fixture.request,
		fixture.repositories,
		fixture.service.questCatalog,
		finishBridgeProgression(t),
		nil,
	)
}

func assertFinishSlotGoldPersistedState(
	t *testing.T,
	repositories dnfrepo.Group,
	wantGold int64,
	wantSlot int64,
	wantMaterial int64,
	wantQuestStatus string,
) {
	t.Helper()
	ctx := context.Background()
	character, found, err := repositories.Character.Load(ctx, "19")
	if err != nil || !found {
		t.Fatalf("reload character found=%t err=%v", found, err)
	}
	quests, found, err := repositories.Quest.Load(ctx, "19")
	if err != nil || !found {
		t.Fatalf("reload quest found=%t err=%v", found, err)
	}
	inventory, found, err := repositories.Inventory.Load(ctx, "19")
	if err != nil || !found {
		t.Fatalf("reload inventory found=%t err=%v", found, err)
	}
	stack, found := inventory.Slots["0:11"]
	if len(quests.States) != 1 || len(inventory.Slots) != 1 ||
		character.Stats["gold"] != wantGold || character.Stats["ex_equip_slot_stat"] != wantSlot ||
		quests.States[6100].Status != wantQuestStatus || !found || stack.ItemID != 9001 || stack.Count != wantMaterial {
		t.Fatalf("persisted slot-gold state gold=%d slot=%#x quest=%+v material=%+v found=%t",
			character.Stats["gold"], character.Stats["ex_equip_slot_stat"], quests.States[6100], stack, found)
	}
}

func assertFinishSlotGoldSuccessStream(
	t *testing.T,
	packets []dnfproto.ChannelPacket,
	questID uint16,
	wantGold uint32,
	wantMaterial uint32,
) {
	t.Helper()
	ackIndex, itemIndex, mode1Index := -1, -1, -1
	activeIndex, clearIndex, acceptableIndex := -1, -1, -1
	ackCount, itemCount, mode1Count := 0, 0, 0
	activeCount, clearCount, acceptableCount := 0, 0, 0
	for index, packet := range packets {
		switch {
		case packet.Header.Classification == dnfproto.DefaultChannelClassification && packet.Header.MsgID == uint16(dnfenum.CmdPacketFinishQuest):
			ackIndex = index
			ackCount++
		case packet.Header.Classification == 0 && packet.Header.MsgID == uint16(dnfenum.CmdPacketWalkoutPartyMember):
			itemIndex = index
			itemCount++
		case packet.Header.Classification == 0 && packet.Header.MsgID == uint16(dnfenum.CmdPacketSetUDPIPPort) && len(packet.Body) != 0 && packet.Body[0] == 1:
			mode1Index = index
			mode1Count++
		case packet.Header.Classification == 0 && packet.Header.MsgID == currentActiveQuestSnapshotMsgID:
			activeIndex = index
			activeCount++
		case packet.Header.Classification == 0 && packet.Header.MsgID == currentClearQuestListMsgID:
			clearIndex = index
			clearCount++
		case packet.Header.Classification == 0 && packet.Header.MsgID == currentAcceptableQuestListMsgID:
			acceptableIndex = index
			acceptableCount++
		}
	}
	if ackIndex != 0 || ackCount != 1 || itemCount != 1 || mode1Count != 1 ||
		activeCount != 1 || clearCount != 1 || acceptableCount != 1 ||
		!(itemIndex > ackIndex && mode1Index > itemIndex &&
			activeIndex > mode1Index && clearIndex > activeIndex && acceptableIndex > clearIndex) {
		t.Fatalf("slot-gold committed counts ack=%d item=%d mode1=%d active=%d clear=%d acceptable=%d order=%d/%d/%d/%d/%d/%d packets=%v",
			ackCount, itemCount, mode1Count, activeCount, clearCount, acceptableCount,
			ackIndex, itemIndex, mode1Index, activeIndex, clearIndex, acceptableIndex, finishBridgePacketIDs(packets))
	}

	ack := packets[ackIndex].Body
	if len(ack) != 18 || ack[0] != 1 ||
		binary.LittleEndian.Uint16(ack[1:3]) != questID ||
		ack[8] != 1 || ack[9] != 0 ||
		binary.LittleEndian.Uint16(ack[10:12]) != 11 ||
		binary.LittleEndian.Uint32(ack[12:16]) != 2 ||
		ack[16] != 0 || ack[17] != 0 {
		t.Fatalf("slot/gold/consumed ACK=%x", ack)
	}

	itemBody := packets[itemIndex].Body
	if len(itemBody) != 3+2*currentItemListEntryWireSize || itemBody[0] != 0 ||
		binary.LittleEndian.Uint16(itemBody[1:3]) != 2 ||
		binary.LittleEndian.Uint16(itemBody[3:5]) != 0 ||
		binary.LittleEndian.Uint32(itemBody[5:9]) != 0 ||
		binary.LittleEndian.Uint32(itemBody[9:13]) != wantGold {
		t.Fatalf("slot-gold wallet/material refresh=%x", itemBody)
	}
	material := itemBody[3+currentItemListEntryWireSize:]
	if binary.LittleEndian.Uint16(material[0:2]) != 11 ||
		binary.LittleEndian.Uint32(material[2:6]) != 9001 ||
		binary.LittleEndian.Uint32(material[6:10]) != wantMaterial {
		t.Fatalf("slot-gold material refresh=%x", material[:10])
	}
	assertFinishBridgeClearQuestProjection(t, packets[clearIndex], questID)
}

func packetBody(packets []dnfproto.ChannelPacket) []byte {
	if len(packets) == 0 {
		return nil
	}
	return packets[0].Body
}

func finishSlotGoldCatalog(t *testing.T, index uint32, cost int64) *dnfquest.Catalog {
	t.Helper()
	source := finishBridgePVFSource{
		dnfquest.DefaultList: "6100 `paid_slot.qst`\n",
		"n_quest/paid_slot.qst": fmt.Sprintf(
			"[grade]\n`[side]`\n[type]\n`[seeking]`\n[difficulty]\n`N`\n[int data]\n0 %d 9001 2\n[reward type]\n`[slot expansion]`\n[reward int data]\n%d\n[level]\n1 99\n[job]\n`[all]`\n[exposed by npc]\n1\n",
			cost, index,
		),
	}
	archive, err := dnfpvf.Build(context.Background(), source, dnfpvf.BuildOptions{Lists: []string{dnfquest.DefaultList}})
	if err != nil {
		t.Fatal(err)
	}
	catalog, err := dnfquest.Load(context.Background(), archive)
	if err != nil {
		t.Fatal(err)
	}
	return catalog
}
