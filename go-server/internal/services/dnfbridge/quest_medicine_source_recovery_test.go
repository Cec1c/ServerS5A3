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

func TestMedicineSourceRecoverySnapshotCheapCandidateSkipsPVFResources(t *testing.T) {
	service := &Service{questCatalogLoadErr: errors.New("PVF catalog must not load")}
	session := &gameSession{selectedCharacterID: 19}
	record := dnfrepo.QuestRecord{
		CharacterID: "19",
		States: map[int64]dnfrepo.QuestState{
			dnfquest.MedicineSourceQuestID: {Status: "completed"},
		},
	}
	got, recovered, err := service.recoverCurrentMedicineSourceForSnapshot(context.Background(), session, record)
	if err != nil || recovered || !reflect.DeepEqual(got, record) {
		t.Fatalf("non-candidate recovery got=%+v recovered=%t err=%v", got, recovered, err)
	}
}

func TestMedicineSourceRecoveryRequiresQuestStackableType(t *testing.T) {
	itemCatalog := qst028MedicineBridgeItemCatalog(t, "[material]")
	service := &Service{
		questCatalog: &dnfquest.Catalog{},
		questFinishResourceLoader: func(context.Context) (*progression.Tables, *pvfDungeonDropCatalog, error) {
			return nil, itemCatalog, nil
		},
	}
	record := dnfrepo.QuestRecord{
		CharacterID: "19",
		States: map[int64]dnfrepo.QuestState{
			dnfquest.MedicineSourceQuestID: {
				Status: "completed",
				Extra: map[string]string{
					"reward_state":         "granted",
					"auto_complete_reason": "main_story_level_clear_no_reward",
				},
			},
			dnfquest.MedicineSourceParentQuestID:   {Status: "active"},
			dnfquest.MedicineSourceDeliveryQuestID: {Status: "active"},
		},
	}
	_, recovered, err := service.recoverCurrentMedicineSourceForSnapshot(
		context.Background(),
		&gameSession{selectedCharacterID: 19},
		record,
	)
	if recovered || !currentMedicineSourceRecoveryItemTypeError(err) {
		t.Fatalf("wrong stackable type recovered=%t err=%v", recovered, err)
	}
}

func TestMedicineSourceRecoveryInventoryUpdateUsesClass0Op14Raw77(t *testing.T) {
	const slot = uint16(50)
	raw := make([]byte, currentItemListEntryWireSize)
	binary.LittleEndian.PutUint16(raw[0:2], slot)
	binary.LittleEndian.PutUint32(raw[2:6], uint32(dnfquest.MedicineSourceItemID))
	binary.LittleEndian.PutUint32(raw[6:10], 1)
	stack := dnfrepo.ItemStack{
		ItemID: dnfquest.MedicineSourceItemID, Count: 1, RawEntry: raw,
		Extra: map[string]string{"count": "1"},
	}
	result := dnfquest.MedicineSourceRecoveryResult{
		Recovered: true,
		PostCommitInventory: dnfrepo.InventoryRecord{
			CharacterID: "19",
			Slots:       map[string]dnfrepo.ItemStack{"0:50": stack},
		},
		Item: dnfquest.FinishCommittedItem{
			SlotKey: "0:50", SlotIndex: slot, ItemID: dnfquest.MedicineSourceItemID,
			Delta: 1, PostCount: 1, CountOrSeed: 1, RawEntry: append([]byte(nil), raw...),
		},
	}
	conn := &bufferConn{}
	service := &Service{}
	session := &gameSession{conn: conn, selectedCharacterID: 19}
	if err := service.sendCurrentMedicineSourceRecoveryInventoryUpdate(session, result); err != nil {
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

func TestMedicineSourceRecoveryInventoryUpdateRejectsStaleCommittedRow(t *testing.T) {
	service := &Service{}
	session := &gameSession{conn: &bufferConn{}, selectedCharacterID: 19}
	result := dnfquest.MedicineSourceRecoveryResult{
		Recovered: true,
		PostCommitInventory: dnfrepo.InventoryRecord{
			CharacterID: "19",
			Slots:       map[string]dnfrepo.ItemStack{},
		},
		Item: dnfquest.FinishCommittedItem{
			SlotKey: "0:50", SlotIndex: 50, ItemID: dnfquest.MedicineSourceItemID,
			Delta: 1, PostCount: 1, CountOrSeed: 1, RawEntry: []byte{1},
		},
	}
	if err := service.sendCurrentMedicineSourceRecoveryInventoryUpdate(session, result); err == nil {
		t.Fatal("stale committed row was projected as op14")
	}
}

func qst028MedicineBridgeItemCatalog(t *testing.T, stackableType string) *pvfDungeonDropCatalog {
	t.Helper()
	catalog, err := newPVFDungeonDropCatalog(dungeonDropCatalogTestSource{
		"monster/monster.lst":     "",
		"equipment/equipment.lst": "",
		"stackable/stackable.lst": "10164764 `medicine.stk`\n",
		"stackable/medicine.stk": "[name] `Medicine`\n[stackable type] `" +
			stackableType + "`\n[stack limit] 1\n",
	})
	if err != nil {
		t.Fatal(err)
	}
	return catalog
}
