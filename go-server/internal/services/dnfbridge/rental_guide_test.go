package dnfbridge

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"reflect"
	"strconv"
	"testing"
	"time"

	"longheng.io/server/internal/modules/dnf/alignedcmd"
	"longheng.io/server/internal/modules/dnf/dnfenum"
	dnfpvf "longheng.io/server/internal/modules/dnf/pvf"
	dnfquest "longheng.io/server/internal/modules/dnf/quest"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	dnfrepomemory "longheng.io/server/internal/modules/dnf/repository/memory"
)

const (
	rentalGuideQuestTestID   = int64(8581)
	rentalGuideQuestTestPath = "n_quest/common/chn_system_10_starpoint_guide.qst"
)

type rentalGuideQuestTestSource map[string]string

func (s rentalGuideQuestTestSource) ReadText(path string) (string, error) {
	return s[path], nil
}

func rentalGuideQuestTestCatalog(t *testing.T) *dnfquest.Catalog {
	t.Helper()
	index, err := dnfpvf.Build(context.Background(), rentalGuideQuestTestSource{
		dnfquest.DefaultList: "8581 `common/chn_system_10_starpoint_guide.qst`\n",
		rentalGuideQuestTestPath: "[grade]\n`[system]`\n" +
			"[type]\n`[custom quest]`\n" +
			"[sub type]\n0\n" +
			"[int data]\n1 0 1\n" +
			"[level]\n10 99\n" +
			"[job]\n`[all]`\n" +
			"[grow type]\n-1\n" +
			"[reward type]\n`[item]`\n" +
			"[reward int data]\n0 0\n" +
			"[gold multiple]\n10\n",
	}, dnfpvf.BuildOptions{Lists: []string{dnfquest.DefaultList}})
	if err != nil {
		t.Fatal(err)
	}
	catalog, err := dnfquest.Load(context.Background(), index)
	if err != nil {
		t.Fatal(err)
	}
	definition, found := catalog.Find(rentalGuideQuestTestID)
	if !found ||
		definition.Path != rentalGuideQuestTestPath ||
		definition.Type != "[custom quest]" ||
		definition.SubType != 0 ||
		!reflect.DeepEqual(definition.IntData, []int64{1, 0, 1}) {
		t.Fatalf("rental guide fixture definition = %+v", definition)
	}
	return catalog
}

func seedRentalGuideQuestTest(t *testing.T, repositories dnfrepo.Group) {
	t.Helper()
	ctx := context.Background()
	if err := repositories.Quest.Save(ctx, dnfrepo.QuestRecord{
		CharacterID: "19",
		States: map[int64]dnfrepo.QuestState{
			rentalGuideQuestTestID: {
				Status:        "active",
				ProgressValue: 1,
				UpdatedAt:     time.Unix(1_700_000_000, 0).UTC(),
				Extra: map[string]string{
					"keep":       "metadata",
					"pvf_path":   rentalGuideQuestTestPath,
					"quest_type": "[custom quest]",
				},
			},
			2901: {
				Status:        "active",
				ProgressValue: 7,
				Extra:         map[string]string{"keep": "other"},
			},
		},
	}); err != nil {
		t.Fatal(err)
	}
}

func TestCurrentRentalAdvancesAcceptedGuideQuestAtomically(t *testing.T) {
	ctx := context.Background()
	repositories := dnfrepomemory.NewMemoryGroup()
	now := time.Unix(1_700_000_000, 0).UTC()
	if err := repositories.Account.Save(ctx, dnfrepo.AccountRecord{
		AccountID: "account-1",
		Metadata:  map[string]string{currentRentalPointMetadataKey: "5"},
	}); err != nil {
		t.Fatal(err)
	}
	if err := repositories.Character.Save(ctx, dnfrepo.CharacterRecord{
		CharacterID: "19",
		AccountID:   "account-1",
		Level:       20,
		Stats:       map[string]int64{"gold": 1_000_000},
	}); err != nil {
		t.Fatal(err)
	}
	if err := repositories.Inventory.Save(ctx, dnfrepo.InventoryRecord{
		CharacterID: "19",
		Slots:       map[string]dnfrepo.ItemStack{},
	}); err != nil {
		t.Fatal(err)
	}
	if err := repositories.Equipment.Save(ctx, dnfrepo.EquipmentRecord{
		CharacterID: "19",
		Entries:     map[string]dnfrepo.EquipmentEntry{},
	}); err != nil {
		t.Fatal(err)
	}
	seedRentalGuideQuestTest(t, repositories)

	owner, err := currentRentalAssetOwner(repositories)
	if err != nil {
		t.Fatal(err)
	}
	definition := dungeonDropItemDefinition{
		ItemID: 416000000, Kind: dungeonDropItemEquipment,
		PVFPath: "equipment/rental.equ", SlotStart: 9, SlotEnd: 64, Durability: 45,
	}
	catalog := rentalGuideQuestTestCatalog(t)
	result, err := rentCurrentEquipment(ctx, owner, "account-1", "19", 416000000, 3, definition, now, catalog)
	if err != nil {
		t.Fatalf("rent equipment: %v", err)
	}
	if result.Points != 2 || result.Slot != 9 {
		t.Fatalf("rent result = %+v", result)
	}
	account, _, _ := repositories.Account.Load(ctx, "account-1")
	inventory, _, _ := repositories.Inventory.Load(ctx, "19")
	quests, _, _ := repositories.Quest.Load(ctx, "19")
	if account.Metadata[currentRentalPointMetadataKey] != "2" ||
		inventory.Slots["0:9"].ItemID != 416000000 {
		t.Fatalf("rental assets account=%+v inventory=%+v", account, inventory)
	}
	guide := quests.States[rentalGuideQuestTestID]
	if guide.Status != "active" || guide.ProgressValue != 0 {
		t.Fatalf("rental guide quest = %+v, want active progress 0", guide)
	}
	if guide.Extra["keep"] != "metadata" ||
		quests.States[2901].ProgressValue != 7 ||
		quests.States[2901].Extra["keep"] != "other" {
		t.Fatalf("unrelated quest state changed: %+v", quests.States)
	}
}

func TestRentalGuideClientTriggerEchoesWithoutProgressMutation(t *testing.T) {
	ctx := context.Background()
	repositories := dnfrepomemory.NewMemoryGroup()
	seedRentalGuideQuestTest(t, repositories)
	if err := repositories.Character.Save(ctx, dnfrepo.CharacterRecord{
		CharacterID: "19",
		AccountID:   "account-1",
	}); err != nil {
		t.Fatal(err)
	}
	owner, err := dnfquest.NewOwner(repositories)
	if err != nil {
		t.Fatal(err)
	}
	result, err := owner.ApplySetTrigger(ctx, dnfquest.NewTriggerCommand(
		alignedcmd.Request{AccountID: "account-1", SelectedCharacterID: 19},
		dnfquest.SetTriggerRequest{
			QuestID:     uint16(rentalGuideQuestTestID),
			TriggerType: 0,
			IsIncrement: false,
		},
	))
	if err != nil {
		t.Fatalf("ApplySetTrigger: %v", err)
	}
	if !result.Known || result.StateChanged || result.ProgressValue != 1 {
		t.Fatalf("client trigger result = %+v, want an unchanged echo at progress 1", result)
	}
	quests, _, _ := repositories.Quest.Load(ctx, "19")
	guide := quests.States[rentalGuideQuestTestID]
	if guide.ProgressValue != 1 || guide.Extra["keep"] != "metadata" {
		t.Fatalf("client trigger mutated rental guide state: %+v", guide)
	}
}

func TestRentalGuideClientTriggerSendsOneAuthoritativeEcho(t *testing.T) {
	ctx := context.Background()
	repositories := dnfrepomemory.NewMemoryGroup()
	seedRentalGuideQuestTest(t, repositories)
	if err := repositories.Character.Save(ctx, dnfrepo.CharacterRecord{
		CharacterID: "19",
		AccountID:   "account-1",
	}); err != nil {
		t.Fatal(err)
	}
	connection := &bufferConn{}
	service := &Service{
		options:            options{accountID: "account-1", currentClientVersion: "official-24.9", gameUpperBodyCodec: gameUpperBodyCodecPlain},
		questCatalog:       rentalGuideQuestTestCatalog(t),
		repositoryProvider: func() (dnfrepo.Group, bool) { return repositories, true },
	}
	session := &gameSession{conn: connection, selectedCharacterID: 19}
	body := make([]byte, currentSetQuestTriggerRequestBodySize)
	binary.LittleEndian.PutUint16(body[0:2], uint16(dnfenum.CmdPacketSetQuestTrigger))
	binary.LittleEndian.PutUint16(body[2:4], uint16(rentalGuideQuestTestID))
	body[4] = 0
	body[5] = 0
	if err := service.handleCurrentSetQuestTrigger(session, body); err != nil {
		t.Fatal(err)
	}
	packet, rest := splitGameServerUpperPacket(t, connection.write.Bytes())
	if packet.Header.Classification != 1 ||
		packet.Header.MsgID != uint16(dnfenum.CmdPacketSetQuestTrigger) ||
		len(rest) != 0 {
		t.Fatalf("rental guide echo packet=%+v rest=%x", packet.Header, rest)
	}
	var payload packetWriter
	payload.writeUint16(uint16(rentalGuideQuestTestID))
	payload.writeUint32(1)
	if want := upperSuccessBody(payload.bytes()); !bytes.Equal(packet.Body, want) {
		t.Fatalf("rental guide echo body=%x want=%x", packet.Body, want)
	}
	quests, _, _ := repositories.Quest.Load(ctx, "19")
	if got := quests.States[rentalGuideQuestTestID].ProgressValue; got != 1 {
		t.Fatalf("client trigger committed progress=%d want unchanged 1", got)
	}
}

func TestCurrentRentalGuideFullInventoryLeavesAllStateUnchanged(t *testing.T) {
	ctx := context.Background()
	repositories := dnfrepomemory.NewMemoryGroup()
	if err := repositories.Account.Save(ctx, dnfrepo.AccountRecord{
		AccountID: "account-1",
		Metadata:  map[string]string{currentRentalPointMetadataKey: "5"},
	}); err != nil {
		t.Fatal(err)
	}
	if err := repositories.Character.Save(ctx, dnfrepo.CharacterRecord{
		CharacterID: "19",
		AccountID:   "account-1",
		Level:       20,
	}); err != nil {
		t.Fatal(err)
	}
	slots := make(map[string]dnfrepo.ItemStack)
	for slot := int16(9); slot <= 64; slot++ {
		slots["0:"+strconv.Itoa(int(slot))] = dnfrepo.ItemStack{ItemID: 1, Count: 1}
	}
	if err := repositories.Inventory.Save(ctx, dnfrepo.InventoryRecord{
		CharacterID: "19",
		Slots:       slots,
	}); err != nil {
		t.Fatal(err)
	}
	if err := repositories.Equipment.Save(ctx, dnfrepo.EquipmentRecord{
		CharacterID: "19",
		Entries:     map[string]dnfrepo.EquipmentEntry{},
	}); err != nil {
		t.Fatal(err)
	}
	seedRentalGuideQuestTest(t, repositories)
	owner, _ := currentRentalAssetOwner(repositories)
	definition := dungeonDropItemDefinition{
		ItemID: 416000000, Kind: dungeonDropItemEquipment,
		PVFPath: "equipment/rental.equ", SlotStart: 9, SlotEnd: 64, Durability: 45,
	}
	_, err := rentCurrentEquipment(
		ctx,
		owner,
		"account-1",
		"19",
		416000000,
		3,
		definition,
		time.Unix(1_700_000_000, 0).UTC(),
		rentalGuideQuestTestCatalog(t),
	)
	if !errors.Is(err, errCurrentRentalInventoryFull) {
		t.Fatalf("rent full-inventory error = %v", err)
	}
	account, _, _ := repositories.Account.Load(ctx, "account-1")
	quests, _, _ := repositories.Quest.Load(ctx, "19")
	if account.Metadata[currentRentalPointMetadataKey] != "5" ||
		quests.States[rentalGuideQuestTestID].ProgressValue != 1 {
		t.Fatalf("full inventory changed wallet or guide: account=%+v quests=%+v", account, quests)
	}
}
