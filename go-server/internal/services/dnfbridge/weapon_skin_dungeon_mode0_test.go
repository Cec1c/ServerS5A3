package dnfbridge

import (
	"context"
	"testing"

	"longheng.io/server/internal/modules/dnf/dnfenum"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	dnfrepomemory "longheng.io/server/internal/modules/dnf/repository/memory"
	"longheng.io/server/internal/modules/dnf/weaponskin"
)

func testWeaponSkinCatalogForMode0UserState(t *testing.T) (*pvfDungeonDropCatalog, weaponskin.Appearance) {
	t.Helper()
	appearance, ok := weaponskin.FromAppearance(weaponskin.SampleID)
	if !ok {
		t.Fatalf("missing sample weapon skin %d", weaponskin.SampleID)
	}
	catalog := &pvfDungeonDropCatalog{
		source: bridgePVFSource{},
		itemCache: map[uint32]dungeonDropItemDefinition{
			appearance.ID: {
				ItemID:        appearance.ID,
				Kind:          dungeonDropItemEquipment,
				EquipmentType: "[weapon avatar]",
				PVFPath:       appearance.Path,
			},
		},
	}
	return catalog, appearance
}

func TestRestoreDungeonWeaponSkinDemoAppearancePreservesActiveDungeonUserState(t *testing.T) {
	ctx := context.Background()
	catalog, appearance := testWeaponSkinCatalogForMode0UserState(t)
	repos := dnfrepomemory.NewMemoryGroup()
	character := dnfrepo.CharacterRecord{
		CharacterID: "3",
		AccountID:   "dnf:1003",
		Name:        "test1",
		Job:         "0",
		Level:       90,
		Stats: map[string]int64{
			"weapon_skin_demo_beiyun_owned": int64(appearance.ID),
			"weapon_skin_demo_selected":     int64(appearance.ID),
			"weapon_skin_demo_revision":     2,
		},
	}
	if err := repos.Character.Save(ctx, character); err != nil {
		t.Fatal(err)
	}
	if err := repos.Equipment.Save(ctx, dnfrepo.EquipmentRecord{
		CharacterID: "3",
		Entries: map[string]dnfrepo.EquipmentEntry{
			"12": {SlotIndex: 12, ItemID: 101030517},
		},
	}); err != nil {
		t.Fatal(err)
	}
	repos.Skill = nil
	service := &Service{
		options:            options{currentClientVersion: "official-24.9"},
		repositoryProvider: func() (dnfrepo.Group, bool) { return repos, true },
		pvfItemCatalog:     catalog,
	}

	t.Run("active_dungeon_preserves_state_1_and_weapon_skin", func(t *testing.T) {
		conn := &bufferConn{}
		session := &gameSession{
			conn:                        conn,
			accountID:                   character.AccountID,
			selectedCharacterID:         3,
			postStartMapPlayerStateSent: true,
		}
		session.dungeonUserStateActive.Store(true)

		if err := service.sendCurrentPostFinishLoadingPlayerState(session, "test_active_dungeon_finish_loading"); err != nil {
			t.Fatal(err)
		}
		packets := conn.write.Bytes()
		var mode0Body []byte
		for len(packets) != 0 {
			packet, rest := splitGameServerUpperPacket(t, packets)
			packets = rest
			if packet.Header.Classification == 0 && packet.Header.MsgID == uint16(dnfenum.CmdPacketSetUDPIPPort) {
				mode0Body = packet.Body
			}
		}
		if len(mode0Body) == 0 {
			t.Fatal("expected weapon skin restore mode0 packet after finish loading")
		}
		if got := mode0TailUserStateForTest(t, mode0Body); got != currentDungeonPlayerUserState {
			t.Fatalf("mode0 tail[5] user_state = %d, want %d", got, currentDungeonPlayerUserState)
		}
		if !currentWeaponSkinBodyHasAppearance(mode0Body, appearance.ID) {
			t.Fatal("mode0 lost weapon skin appearance marker")
		}
	})

	t.Run("inactive_dungeon_keeps_state_0", func(t *testing.T) {
		conn := &bufferConn{}
		session := &gameSession{
			conn:                        conn,
			accountID:                   character.AccountID,
			selectedCharacterID:         3,
			postStartMapPlayerStateSent: true,
		}
		if err := service.restoreDungeonWeaponSkinDemoAppearance(ctx, session, 3, character.Name, character, "test_inactive"); err != nil {
			t.Fatal(err)
		}
		packet, rest := splitGameServerUpperPacket(t, conn.write.Bytes())
		if len(rest) != 0 || packet.Header.MsgID != uint16(dnfenum.CmdPacketSetUDPIPPort) {
			t.Fatalf("unexpected packet=%+v rest=%d", packet.Header, len(rest))
		}
		if got := mode0TailUserStateForTest(t, packet.Body); got != 0 {
			t.Fatalf("inactive dungeon mode0 tail[5] = %d, want 0", got)
		}
	})

	t.Run("deferred_tutorial_noti3_activates_before_weapon_skin_mode0", func(t *testing.T) {
		conn := &bufferConn{}
		session := &gameSession{
			conn:                              conn,
			accountID:                         character.AccountID,
			selectedCharacterID:               3,
			postStartMapPlayerStateSent:       true,
			deferredDungeonUserStateObjectKey: 3,
		}
		if err := service.sendCurrentPostFinishLoadingPlayerState(session, "test_deferred_noti3"); err != nil {
			t.Fatal(err)
		}
		if !session.dungeonUserStateActive.Load() {
			t.Fatal("expected deferred NOTI 3 to mark dungeonUserStateActive")
		}
		packets := conn.write.Bytes()
		var seenNoti3 bool
		var mode0Body []byte
		for len(packets) != 0 {
			packet, rest := splitGameServerUpperPacket(t, packets)
			packets = rest
			if packet.Header.Classification == 0 && packet.Header.MsgID == uint16(dnfenum.CmdPacketNotifyUserState) {
				seenNoti3 = true
			}
			if packet.Header.Classification == 0 && packet.Header.MsgID == uint16(dnfenum.CmdPacketSetUDPIPPort) {
				if !seenNoti3 {
					t.Fatal("mode0 sent before deferred NOTI 3")
				}
				mode0Body = packet.Body
			}
		}
		if !seenNoti3 || len(mode0Body) == 0 {
			t.Fatalf("seenNoti3=%t mode0Len=%d", seenNoti3, len(mode0Body))
		}
		if got := mode0TailUserStateForTest(t, mode0Body); got != currentDungeonPlayerUserState {
			t.Fatalf("deferred mode0 tail[5] = %d, want %d", got, currentDungeonPlayerUserState)
		}
	})

	t.Run("mid_dungeon_equip_refresh_preserves_state_1_and_town_return_resets_to_0", func(t *testing.T) {
		conn := &bufferConn{}
		session := &gameSession{
			conn:                             conn,
			accountID:                        character.AccountID,
			selectedCharacterID:              3,
			connectionTownActorOwnerChannel:  10,
			townActorOwnerChannel:            10,
			preDungeonContextPlayerStateSent: true,
			postStartMapPlayerStateSent:      true,
		}
		session.dungeonUserStateActive.Store(true)

		if err := service.sendSelectedActorMode0AppearanceRefresh(session, "wear_in_dungeon", "test"); err != nil {
			t.Fatal(err)
		}
		packet, _ := splitGameServerUpperPacket(t, conn.write.Bytes())
		if packet.Header.MsgID != uint16(dnfenum.CmdPacketSetUDPIPPort) {
			t.Fatalf("unexpected msgID=%d", packet.Header.MsgID)
		}
		if got := mode0TailUserStateForTest(t, packet.Body); got != currentDungeonPlayerUserState {
			t.Fatalf("mid-dungeon refresh tail[5] = %d, want %d", got, currentDungeonPlayerUserState)
		}

		captured := captureCurrentRaidRetrySceneGates(session)
		resetDungeonReturnSceneGates(session)
		if session.dungeonUserStateActive.Load() {
			t.Fatal("resetDungeonReturnSceneGates did not clear dungeonUserStateActive")
		}
		session.townActorOwnerChannel = 10
		conn.write.Reset()
		if err := service.sendSelectedActorMode0AppearanceRefresh(session, "wear_in_town", "test"); err != nil {
			t.Fatal(err)
		}
		townPacket, _ := splitGameServerUpperPacket(t, conn.write.Bytes())
		if got := mode0TailUserStateForTest(t, townPacket.Body); got != 0 {
			t.Fatalf("town refresh tail[5] = %d, want 0", got)
		}

		captured.restore(session)
		if !session.dungeonUserStateActive.Load() {
			t.Fatal("raid retry gate restore did not restore dungeonUserStateActive")
		}
		resetDungeonEntrySceneGates(session)
		if session.dungeonUserStateActive.Load() {
			t.Fatal("resetDungeonEntrySceneGates did not clear dungeonUserStateActive")
		}
	})
}
