package dnfbridge

import (
	"bytes"
	"context"
	"encoding/binary"
	"longheng.io/server/internal/modules/dnf/channelcatalog"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	dnfrepomemory "longheng.io/server/internal/modules/dnf/repository/memory"
	platformpvf "longheng.io/server/internal/platform/pvf"
	"os"
	"strconv"
	"testing"
)

func TestReviewRealPVFTownMode1RebindRestoresEquippedEnchant(t *testing.T) {
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
	session := &gameSession{conn: conn, selectedCharacterID: 9, accountID: "account-1", channel: channelcatalog.Channel{ID: 23, Type: 23}, selectedCreatureStateTableSent: true}
	if err := s.sendSelectedActorMode1AppearanceRefresh(session, "town_post_transition", "test_actual_town_mode1_lifecycle"); err != nil {
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
		t.Fatalf("real town full mode1 rebind mode1=%t but dynamic equipped enchant refresh=%t; cold-login-only repair is lost on recreation", mode1, refresh)
	}
}

// The dungeon layout is the existing legal return fixture. Creature equipment
// and its enchant projection use the actual PVF, without force-moving a client.
func TestReviewRealPVFConfirmedDungeonReturnRestoresPetOnce(t *testing.T) {
	path := os.Getenv("DNFBRIDGE_SINGLE_ANTON_PVF_SMOKE")
	if path == "" {
		t.Skip("requires current369d PVF")
	}
	a, err := platformpvf.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	s, session, _ := newBackToVillageRuntime(t)
	s.initialEquipmentArchive = a
	s.options.gameUpperHeader = gameUpperHeaderServer16
	s.options.currentClientVersion = "official-24.9"
	session.accountID = "account-1"
	repos, _ := s.repositoryGroup()
	ctx := context.Background()
	if err := repos.Equipment.Save(ctx, dnfrepo.EquipmentRecord{CharacterID: "99", Entries: map[string]dnfrepo.EquipmentEntry{"26": {SlotIndex: 26, ItemID: 400990168, RawEntry: testCurrentEquippedCreatureEnchantRaw(26, 400990168, 2, 0, 0)}}}); err != nil {
		t.Fatal(err)
	}
	if err := repos.Pet.Save(ctx, dnfrepo.PetRecord{CharacterID: "99", EquippedKey: "2", Entries: map[string]dnfrepo.PetEntry{"2": {PetKey: "2", CreatureKey: 2, ItemID: 400990168, Level: 1, Extra: map[string]string{"pet_enchant_card_item_id": "10008713", "enchant_upgrade_count": "2"}}}}); err != nil {
		t.Fatal(err)
	}
	if err := s.handleDungeonBackToVillage(session, nil); err != nil {
		t.Fatal(err)
	}
	conn := session.conn.(*bufferConn)
	conn.write.Reset()
	if err := s.commitPendingDungeonReturnForSceneRequest(session, "test_actual_return_pet"); err != nil {
		t.Fatal(err)
	}
	if session.dungeon.runtime != nil || session.confirmedDungeonReturnStatePending {
		t.Fatal("legal confirmed return did not retire dungeon and complete town suffix")
	}
	raw := conn.write.Bytes()
	mode1, petUpdates := false, 0
	for len(raw) > 0 {
		p, rest := splitLongHengGameServerUpperPacket(t, raw)
		raw = rest
		if (p.Header.MsgID == 2 || p.Header.MsgID == 1371) && len(p.Body) > 0 && p.Body[0] == 1 {
			mode1 = true
		}
		if p.Header.MsgID == 14 && len(p.Body) > 5 && p.Body[0] == 3 && binary.LittleEndian.Uint16(p.Body[3:5]) == 26 {
			if !mode1 {
				t.Fatal("pet update preceded full town mode1")
			}
			assertEquippedCreatureItemRefreshPacket(t, p, 400990168, 2, 10008713, 2)
			petUpdates++
		}
	}
	if !mode1 || petUpdates != 1 {
		t.Fatalf("return mode1=%t pet updates=%d want exactly one", mode1, petUpdates)
	}
	before := conn.write.Len()
	if err := s.commitPendingDungeonReturnForSceneRequest(session, "test_actual_return_duplicate"); err != nil {
		t.Fatal(err)
	}
	if conn.write.Len() != before {
		t.Fatal("duplicate return reissued actor, Buff or pet update")
	}
}

type reviewTownForeignPet struct{ dnfrepo.PetRepository }

func (r reviewTownForeignPet) Load(ctx context.Context, key string) (dnfrepo.PetRecord, bool, error) {
	v, ok, err := r.PetRepository.Load(ctx, key)
	v.CharacterID = "foreign-character"
	return v, ok, err
}

func TestReviewRealPVFTownPetRebindControls(t *testing.T) {
	path := os.Getenv("DNFBRIDGE_SINGLE_ANTON_PVF_SMOKE")
	if path == "" {
		t.Skip("requires current369d PVF")
	}
	a, err := platformpvf.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv(buffSwapReapplyEnv, "on")
	for _, tc := range []struct {
		name               string
		equipment          bool
		item               int64
		petSerial          uint32
		foreign, noEnchant bool
		updates            int
		card               uint32
	}{
		{"equipped_exact", true, 400990168, 2, false, false, 1, 10008713},
		{"absent", false, 0, 2, false, false, 0, 0},
		{"unequipped_stale_pet", true, 0, 2, false, false, 0, 0},
		{"ordinary_slot26", true, 101040493, 2, false, false, 0, 0},
		{"wrong_pet_owner", true, 400990168, 2, true, false, 1, 0},
		{"wrong_same_template_instance", true, 400990168, 3, false, false, 1, 0},
		{"no_enchant", true, 400990168, 2, false, true, 1, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			repos := dnfrepomemory.NewMemoryGroup()
			if err := repos.Character.Save(ctx, dnfrepo.CharacterRecord{CharacterID: "9", AccountID: "account-1", Job: "14", Level: 90, Stats: map[string]int64{"fatigue": 156}}); err != nil {
				t.Fatal(err)
			}
			if tc.equipment {
				entries := map[string]dnfrepo.EquipmentEntry{}
				if tc.item != 0 {
					raw := testCurrentEquippedCreatureEnchantRaw(26, uint32(tc.item), 2, 0, 0)
					if tc.name == "ordinary_slot26" {
						raw = buildInitialEquipmentRawEntry(26, tc.item, 20)
					}
					entries["26"] = dnfrepo.EquipmentEntry{SlotIndex: 26, ItemID: tc.item, RawEntry: raw}
				}
				if err := repos.Equipment.Save(ctx, dnfrepo.EquipmentRecord{CharacterID: "9", Entries: entries}); err != nil {
					t.Fatal(err)
				}
			}
			extra := map[string]string{}
			if !tc.noEnchant {
				extra["pet_enchant_card_item_id"] = "10008713"
				extra["enchant_upgrade_count"] = "2"
			}
			if err := repos.Pet.Save(ctx, dnfrepo.PetRecord{CharacterID: "9", Entries: map[string]dnfrepo.PetEntry{"candidate": {PetKey: strconv.FormatUint(uint64(tc.petSerial), 10), CreatureKey: tc.petSerial, ItemID: 400990168, Level: 1, Extra: extra}}}); err != nil {
				t.Fatal(err)
			}
			if tc.foreign {
				repos.Pet = reviewTownForeignPet{repos.Pet}
			}
			beforeSettings, beforeFound, err := repos.Settings.Load(ctx, "character:9:buff_swap_native")
			if err != nil {
				t.Fatal(err)
			}
			conn := &bufferConn{}
			s := &Service{initialEquipmentArchive: a, options: options{accountID: "account-1", gameUpperHeader: gameUpperHeaderServer16, gameUpperBodyCodec: gameUpperBodyCodecPlain, currentClientVersion: "official-24.9"}, repositoryProvider: func() (dnfrepo.Group, bool) { return repos, true }}
			session := &gameSession{conn: conn, selectedCharacterID: 9, accountID: "account-1", channel: channelcatalog.Channel{ID: 23, Type: 23}}
			if err := s.sendSelectedActorMode1AppearanceRefresh(session, "review_town_control", "review_town_control"); err != nil {
				t.Fatal(err)
			}
			raw := conn.write.Bytes()
			updates, buffs := 0, 0
			mode1 := false
			for len(raw) > 0 {
				p, rest := splitLongHengGameServerUpperPacket(t, raw)
				raw = rest
				if (p.Header.MsgID == 2 || p.Header.MsgID == 1371) && len(p.Body) > 0 && p.Body[0] == 1 {
					mode1 = true
				}
				if p.Header.MsgID == 14 {
					if !mode1 {
						t.Fatal("pet update before equipment object")
					}
					upgrade := byte(0)
					if tc.card != 0 {
						upgrade = 2
					}
					assertEquippedCreatureItemRefreshPacket(t, p, 400990168, 2, tc.card, upgrade)
					updates++
				}
				if p.Header.MsgID == nativeBuffSwapSnapshotID {
					t.Fatal("historical1369 cannot restore current1368 manager")
				}
				if p.Header.MsgID == currentBuffSwapSnapshotID {
					if p.Header.Classification != 0 {
						t.Fatalf("current1368 classification=%d want=0", p.Header.Classification)
					}
					if !mode1 || updates != tc.updates {
						t.Fatal("Buff must follow mode1 and its expected pet refresh")
					}
					// Independent current-EXE wire expectation: skill0,33 empty rows,
					// list45 and indexFFFF. No registration exists in this fixture.
					want := make([]byte, 168)
					want[2] = 33
					for slot := 0; slot < 33; slot++ {
						i := 3 + 5*slot
						want[i+1], want[i+2], want[i+3], want[i+4] = byte(slot), 45, 255, 255
					}
					if !bytes.Equal(p.Body, want) {
						t.Fatalf("current1368 body=%x want=%x", p.Body, want)
					}
					buffs++
				}
			}
			afterSettings, afterFound, err := repos.Settings.Load(ctx, "character:9:buff_swap_native")
			if err != nil {
				t.Fatal(err)
			}
			if beforeFound || afterFound || len(beforeSettings.Values) != 0 || len(afterSettings.Values) != 0 {
				t.Fatal("empty current1368 projection must not persist synthetic registration")
			}
			if !mode1 || updates != tc.updates || buffs != 1 {
				t.Fatalf("mode1=%t pet updates=%d want=%d Buff=%d want=1", mode1, updates, tc.updates, buffs)
			}
		})
	}
}

func TestReviewRealPVFTownPostTransitionRestoresEquippedEnchant(t *testing.T) {
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
	session := &gameSession{conn: conn, selectedCharacterID: 9, accountID: "account-1", channel: channelcatalog.Channel{ID: 23, Type: 23}, selectedCreatureStateTableSent: true}
	if err := (func() error {
		s.armCurrentTownPostTransitionPlayerState(session, "test_town_full_transition")
		return s.sendCurrentTownPostTransitionPlayerState(session, "test_town_full_transition")
	})(); err != nil {
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
		t.Fatalf("real town full mode1 rebind mode1=%t but dynamic equipped enchant refresh=%t; cold-login-only repair is lost on recreation", mode1, refresh)
	}
}
