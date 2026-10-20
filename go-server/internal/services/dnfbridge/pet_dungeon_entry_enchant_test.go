package dnfbridge

import (
	"context"
	"encoding/binary"
	"longheng.io/server/internal/modules/dnf/channelcatalog"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	dnfrepomemory "longheng.io/server/internal/modules/dnf/repository/memory"
	"longheng.io/server/internal/modules/dnf/worldmap"
	platformpvf "longheng.io/server/internal/platform/pvf"
	"os"
	"testing"
)

func TestRealPVFPetDungeonMode1RebindRestoresOnlyEquippedEnchant(t *testing.T) {
	path := os.Getenv("DNFBRIDGE_SINGLE_ANTON_PVF_SMOKE")
	if path == "" {
		t.Skip("requires current369d PVF")
	}
	a, err := platformpvf.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	repos := dnfrepomemory.NewMemoryGroup()
	if err := repos.Character.Save(ctx, dnfrepo.CharacterRecord{CharacterID: "9", AccountID: "account-1", Name: "Actor9", Job: "14", Level: 90, Stats: map[string]int64{"fatigue": 156}}); err != nil {
		t.Fatal(err)
	}
	if err := repos.Inventory.Save(ctx, dnfrepo.InventoryRecord{CharacterID: "9", Slots: map[string]dnfrepo.ItemStack{}}); err != nil {
		t.Fatal(err)
	}
	worn := dnfrepo.EquipmentRecord{CharacterID: "9", Entries: map[string]dnfrepo.EquipmentEntry{"26": {SlotIndex: 26, ItemID: 400990168, RawEntry: testCurrentEquippedCreatureEnchantRaw(26, 400990168, 2, 0, 0)}}}
	if err := repos.Equipment.Save(ctx, worn); err != nil {
		t.Fatal(err)
	}
	if err := repos.Pet.Save(ctx, dnfrepo.PetRecord{CharacterID: "9", EquippedKey: "2", Entries: map[string]dnfrepo.PetEntry{"2": {PetKey: "2", CreatureKey: 2, ItemID: 400990168, Level: 1, Extra: map[string]string{"pet_enchant_card_item_id": "10008713", "enchant_upgrade_count": "2"}}}}); err != nil {
		t.Fatal(err)
	}
	conn := &bufferConn{}
	s := &Service{initialEquipmentArchive: a, options: options{accountID: "account-1", gameUpperHeader: gameUpperHeaderServer16, gameUpperBodyCodec: gameUpperBodyCodecPlain, currentClientVersion: "official-24.9"}, repositoryProvider: func() (dnfrepo.Group, bool) { return repos, true }}
	session := &gameSession{conn: conn, selectedCharacterID: 9, accountID: "account-1", channel: channelcatalog.Channel{ID: 23, Type: 23}, preDungeonContextPlayerStateSent: true, selectedCreatureStateTableSent: true}
	if err := s.sendCurrentPostStartMapPlayerPlacement(session, nil, worldmap.DungeonRoomScene{}, "test_actual_post_start_lifecycle"); err != nil {
		t.Fatal(err)
	}
	raw := conn.write.Bytes()
	mode1 := false
	refresh := false
	for len(raw) > 0 {
		p, rest := splitLongHengGameServerUpperPacket(t, raw)
		raw = rest
		if (p.Header.MsgID == 2 || p.Header.MsgID == 1371) && len(p.Body) > 0 && p.Body[0] == 1 {
			mode1 = true
		}
		if p.Header.MsgID == 14 && len(p.Body) > 5 && p.Body[0] == 3 && binary.LittleEndian.Uint16(p.Body[3:5]) == 26 {
			if !mode1 {
				t.Fatal("slot26 dynamic state before new actor equipment object")
			}
			assertEquippedCreatureItemRefreshPacket(t, p, 400990168, 2, 10008713, 2)
			refresh = true
		}
	}
	if !mode1 || !refresh {
		t.Fatalf("real dungeon rebind mode1=%t but dynamic equipped enchant refresh=%t; cold-login-only repair is lost on recreation", mode1, refresh)
	}
}
