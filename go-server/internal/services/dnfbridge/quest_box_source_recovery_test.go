package dnfbridge

import (
	"context"
	"encoding/binary"
	"errors"
	"reflect"
	"testing"

	"longheng.io/server/internal/modules/dnf/progression"
	dnfquest "longheng.io/server/internal/modules/dnf/quest"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

func TestBoxSourceRecoverySnapshotCheapCandidateSkipsPVFResources(t *testing.T) {
	service := &Service{questCatalogLoadErr: errors.New("PVF catalog must not load")}
	session := &gameSession{selectedCharacterID: 19}
	record := dnfrepo.QuestRecord{
		CharacterID: "19",
		States: map[int64]dnfrepo.QuestState{
			dnfquest.BoxSourceQuestID: {Status: "completed"},
		},
	}
	got, recovered, err := service.recoverCurrentBoxSourceForSnapshot(context.Background(), session, record)
	if err != nil || recovered || !reflect.DeepEqual(got, record) {
		t.Fatalf("non-candidate recovery got=%+v recovered=%t err=%v", got, recovered, err)
	}
}

func TestBoxSourceRecoveryRequiresQuestStackableType(t *testing.T) {
	itemCatalog := qst019BoxBridgeItemCatalog(t, "[material]")
	service := &Service{
		questCatalog: &dnfquest.Catalog{},
		questFinishResourceLoader: func(context.Context) (*progression.Tables, *pvfDungeonDropCatalog, error) {
			return nil, itemCatalog, nil
		},
	}
	record := dnfrepo.QuestRecord{
		CharacterID: "19",
		States: map[int64]dnfrepo.QuestState{
			dnfquest.BoxSourceQuestID: {
				Status: "completed",
				Extra: map[string]string{
					"reward_state":         "granted",
					"auto_complete_reason": "main_story_level_clear_no_reward",
				},
			},
			dnfquest.BoxSourceParentQuestID:   {Status: "active"},
			dnfquest.BoxSourceDeliveryQuestID: {Status: "active"},
		},
	}
	_, recovered, err := service.recoverCurrentBoxSourceForSnapshot(
		context.Background(),
		&gameSession{selectedCharacterID: 19},
		record,
	)
	if recovered || !currentBoxSourceRecoveryItemTypeError(err) {
		t.Fatalf("wrong stackable type recovered=%t err=%v", recovered, err)
	}
}

func TestBoxSourceRecoveryInventoryUpdateUsesClass0Op14Raw77(t *testing.T) {
	const slot = uint16(50)
	raw := make([]byte, currentItemListEntryWireSize)
	binary.LittleEndian.PutUint16(raw[0:2], slot)
	binary.LittleEndian.PutUint32(raw[2:6], uint32(dnfquest.BoxSourceItemID))
	binary.LittleEndian.PutUint32(raw[6:10], 1)
	stack := dnfrepo.ItemStack{
		ItemID: dnfquest.BoxSourceItemID, Count: 1, RawEntry: raw,
		Extra: map[string]string{"count": "1"},
	}
	result := dnfquest.BoxSourceRecoveryResult{
		Recovered: true,
		PostCommitInventory: dnfrepo.InventoryRecord{
			CharacterID: "19",
			Slots:       map[string]dnfrepo.ItemStack{"0:50": stack},
		},
		Item: dnfquest.FinishCommittedItem{
			SlotKey: "0:50", SlotIndex: slot, ItemID: dnfquest.BoxSourceItemID,
			Delta: 1, PostCount: 1, CountOrSeed: 1, RawEntry: append([]byte(nil), raw...),
		},
	}
	conn := &bufferConn{}
	service := &Service{}
	session := &gameSession{conn: conn, selectedCharacterID: 19}
	if err := service.sendCurrentBoxSourceRecoveryInventoryUpdate(session, result); err != nil {
		t.Fatal(err)
	}
	packet, rest := splitGameServerUpperPacket(t, conn.write.Bytes())
	wantBody := buildCurrentItemUpdateBody(
		dnfrepo.MainInventoryListType,
		[]currentItemListEntry{currentItemListEntryFromStack(dnfrepo.MainInventoryListType, int16(slot), stack)},
	)
	if len(rest) != 0 || packet.Header.MsgID != 14 || packet.Header.Classification != 0 ||
		!reflect.DeepEqual(packet.Body, wantBody) {
		t.Fatalf("op14 header=%+v body=%x want=%x rest=%x", packet.Header, packet.Body, wantBody, rest)
	}
}

func qst019BoxBridgeItemCatalog(t *testing.T, stackableType string) *pvfDungeonDropCatalog {
	t.Helper()
	catalog, err := newPVFDungeonDropCatalog(finishBridgePVFSource{
		"monster/monster.lst":     "",
		"equipment/equipment.lst": "",
		"stackable/stackable.lst": "10164767 `box.stk`\n",
		"stackable/box.stk": "[name]\n`Box`\n[stackable type]\n`" +
			stackableType + "`\n[stack limit]\n1\n",
	})
	if err != nil {
		t.Fatal(err)
	}
	return catalog
}
