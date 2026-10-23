package dnfbridge

import (
	"bytes"
	"context"
	"encoding/binary"
	"log/slog"
	"testing"

	repo "longheng.io/server/internal/modules/dnf/repository"
	memory "longheng.io/server/internal/modules/dnf/repository/memory"
)

// The live test99 snapshot contains four dungeon-issued Aiken templates whose
// complete 119-byte rows have raw[0x72:0x77] == 0. A zero five-byte state is
// therefore a real creation state, not an absent or corrupt carrier.
func aikenR167ZeroStateFixture(t *testing.T) (*Service, *gameSession, repo.Group, *bytes.Buffer) {
	t.Helper()
	ctx := context.Background()
	g := memory.NewMemoryGroup()
	raw := make([]byte, currentItemListEntryWireSize)
	binary.LittleEndian.PutUint16(raw[0:], 25)
	binary.LittleEndian.PutUint32(raw[2:], 100120561)
	binary.LittleEndian.PutUint32(raw[6:], 554884773)
	inv := repo.InventoryRecord{CharacterID: "19", Slots: map[string]repo.ItemStack{
		"0:25": {
			ItemID: 100120561, Count: 1, RawEntry: raw,
			Extra: map[string]string{
				"durability": "32", "equipment_type": "[pants]", "item_kind": "equipment",
				"max_durability": "32", "pvf_path": "equipment/character/common/pants/larmor/100120561.equ",
				"quality_seed": "554884773", "source": "dungeon_pvf_drop_pickup",
			},
		},
		"0:153": {ItemID: 10161536, Count: 11},
		"0:154": {ItemID: 10161538, Count: 38},
		"0:155": {ItemID: 10161533, Count: 100},
		"0:156": {ItemID: 10161534, Count: 100},
		"0:157": {ItemID: 10161535, Count: 100},
	}}
	for _, err := range []error{
		g.Character.Save(ctx, repo.CharacterRecord{CharacterID: "19", AccountID: "account-1", Level: 90}),
		g.Inventory.Save(ctx, inv),
		g.Settings.Save(ctx, repo.SettingsRecord{
			Scope: repo.CharacterContainerStateScope("19"),
			Values: map[string]string{
				"main_list_param16": "24", "avatar_list_param16": "0", "personal_cargo_list_param16": "8",
			},
		}),
		g.AccountInventory.Save(ctx, repo.AccountInventoryRecord{AccountID: "account-1", Slots: map[string]repo.ItemStack{}}),
	} {
		if err != nil {
			t.Fatal(err)
		}
	}
	catalog, err := newPVFDungeonDropCatalog(aikenR142Archive(t))
	if err != nil {
		t.Fatal(err)
	}
	logs := new(bytes.Buffer)
	service := &Service{
		options: options{
			accountID: "account-1", currentClientVersion: "official-24.9",
			gameUpperHeader: gameUpperHeaderChannel13, gameUpperBodyCodec: gameUpperBodyCodecPlain,
			gameUpperClientBodyCodec: gameUpperClientBodyCodecPlain,
		},
		pvfItemCatalog:     catalog,
		repositoryProvider: func() (repo.Group, bool) { return g, true },
		logger:             slog.New(slog.NewTextHandler(logs, nil)),
	}
	session := &gameSession{conn: &bufferConn{}, selectedCharacterID: 19, accountFromToken: true, itemListMigrationsDone: true}
	return service, session, g, logs
}

func TestAikenR167ZeroStateGradeUpgrade(t *testing.T) {
	s, session, g, logs := aikenR167ZeroStateFixture(t)
	before := aikenR142Load(t, g)
	if got := before.Slots["0:25"].RawEntry[0x72:0x77]; !bytes.Equal(got, make([]byte, 5)) {
		t.Fatalf("fixture is not the live zero state: %x", got)
	}
	if err := aikenR142Request(t, s, session, 25); err != nil {
		t.Fatal(err)
	}
	after := aikenR142Load(t, g)
	updated := after.Slots["0:25"]
	if updated.RawEntry[0x76] != 1 || !bytes.Equal(updated.RawEntry[0x72:0x74], []byte{0, 0}) {
		t.Fatalf("zero options were not preserved through grade upgrade: %x log=%s", updated.RawEntry, logs.String())
	}
	if _, ok := after.Slots["0:153"]; ok {
		t.Fatalf("D-to-C material was not fully consumed: %+v", after.Slots["0:153"])
	}
	aikenR142Outcome(t, session, true)
}

func TestAikenR167ZeroStateOptionSelectionThenUpgrade(t *testing.T) {
	s, session, g, logs := aikenR167ZeroStateFixture(t)
	aikenR160Dispatch(t, s, session, 1340, []byte{25, 0, 0, 13})
	after := aikenR142Load(t, g)
	updated := after.Slots["0:25"]
	if updated.RawEntry[0x72] != 13 || updated.RawEntry[0x73] != 0 || updated.RawEntry[0x76] != 0 {
		t.Fatalf("first option selection changed more than its slot: %x log=%s", updated.RawEntry, logs.String())
	}
	if got := after.Slots["0:156"].Count; got != 77 {
		t.Fatalf("option 13 D-grade material=%d want=77", got)
	}
	packet, rest := splitGameServerUpperPacket(t, session.conn.(*bufferConn).write.Bytes())
	second, rest := splitGameServerUpperPacket(t, rest)
	if len(rest) != 0 || packet.Header.MsgID != 14 || second.Header.MsgID != 1340 || !bytes.Equal(second.Body, []byte{1}) {
		t.Fatalf("wrong first option packets: %+v rest=%x", packet, rest)
	}

	session.conn.(*bufferConn).write.Reset()
	aikenR160Dispatch(t, s, session, 1340, []byte{25, 0, 1, 1})
	after = aikenR142Load(t, g)
	updated = after.Slots["0:25"]
	if updated.RawEntry[0x72] != 13 || updated.RawEntry[0x73] != 1 || updated.RawEntry[0x76] != 0 {
		t.Fatalf("second independent option selection failed: %x log=%s", updated.RawEntry, logs.String())
	}
	if got := after.Slots["0:155"].Count; got != 72 {
		t.Fatalf("option 1 D-grade material=%d want=72", got)
	}

	session.conn.(*bufferConn).write.Reset()
	if err := aikenR142Request(t, s, session, 25); err != nil {
		t.Fatal(err)
	}
	after = aikenR142Load(t, g)
	updated = after.Slots["0:25"]
	if updated.RawEntry[0x76] != 1 || updated.RawEntry[0x72] != 13 || updated.RawEntry[0x73] != 1 {
		t.Fatalf("grade upgrade disturbed selected options: %x log=%s", updated.RawEntry, logs.String())
	}
	if _, ok := after.Slots["0:153"]; ok {
		t.Fatal("D-to-C material was not consumed after option selection")
	}
	aikenR142Outcome(t, session, true)
}
