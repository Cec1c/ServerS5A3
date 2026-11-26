package dnfbridge

import (
	"context"
	"encoding/binary"
	"errors"
	"net"
	"testing"
	"time"

	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	"longheng.io/server/internal/modules/dnf/weaponskin"
)

type pr2FailWriteConn struct {
	net.Conn
}

func (c *pr2FailWriteConn) Write(_ []byte) (int, error) {
	return 0, errors.New("simulated ack write failure")
}

func (c *pr2FailWriteConn) SetWriteDeadline(_ time.Time) error { return nil }

func TestPR2WeaponSkinUpdateInvalidatesTownMode0CacheBeforeRefresh(t *testing.T) {
	t.Setenv(townDisplayOnlyEnv, "on")
	baseRepos, s, observer0, actorSession, _, _ := newTownCoPresenceTestService(t)
	s.options.currentClientVersion = "official-24.9"

	sample, ok := weaponskin.FromAppearance(weaponskin.SampleID)
	if !ok {
		t.Fatal("sample weapon skin missing from catalog")
	}
	s.pvfItemCatalog = &pvfDungeonDropCatalog{
		source: bridgePVFSource{},
		itemCache: map[uint32]dungeonDropItemDefinition{
			sample.ID: {
				ItemID:        sample.ID,
				Kind:          dungeonDropItemEquipment,
				EquipmentType: "[weapon avatar]",
				PVFPath:       sample.Path,
			},
		},
	}

	ctx := context.Background()
	actorID := actorSession.selectedCharacterID
	actorIDStr := "1002"
	actorChar, found, err := baseRepos.Character.Load(ctx, actorIDStr)
	if err != nil || !found {
		t.Fatalf("load actor character: found=%v err=%v", found, err)
	}
	actorChar.Job = "0"
	actorChar.AccountID = actorSession.accountID
	if err := baseRepos.Character.Save(ctx, actorChar); err != nil {
		t.Fatal(err)
	}
	if err := baseRepos.Inventory.Save(ctx, dnfrepo.InventoryRecord{
		CharacterID: actorIDStr,
		Slots: map[string]dnfrepo.ItemStack{
			"1:7": {ItemID: weaponskin.SampleID, Count: 1},
		},
	}); err != nil {
		t.Fatal(err)
	}
	if err := baseRepos.Equipment.Save(ctx, dnfrepo.EquipmentRecord{
		CharacterID: actorIDStr,
		Entries: map[string]dnfrepo.EquipmentEntry{
			"12": {SlotIndex: 12, ItemID: 101030517},
		},
	}); err != nil {
		t.Fatal(err)
	}

	equipProbe := &pr2CountingEquipStore{Store: baseRepos.Equipment}
	repos := baseRepos
	repos.Equipment = equipProbe
	s.repositoryProvider = func() (dnfrepo.Group, bool) { return repos, true }

	actorInfo := onlinePlayerInfo{
		CharacterID: actorID,
		AccountID:   actorSession.accountID,
		Session:     actorSession,
		ChannelID:   actorSession.channel.ID,
		TownID:      38,
		AreaID:      2,
	}

	// Populate cache before applying weapon skin and verify a second observer hits cache.
	const townSource = "town_copresence_actor_state_existing_to_newcomer"
	obs1, _ := newPR2TownObserver(t, s, observer0, 3101)
	if err := s.sendTownRemoteActorState(obs1, actorInfo, townSource); err != nil {
		t.Fatalf("initial town mode0 build failed: %v", err)
	}
	obs1Hit, _ := newPR2TownObserver(t, s, observer0, 3100)
	if err := s.sendTownRemoteActorState(obs1Hit, actorInfo, townSource); err != nil {
		t.Fatalf("cached town mode0 build failed: %v", err)
	}
	if equipProbe.loads.Load() != 1 {
		t.Fatalf("expected 1 equipment load across two observers before weapon skin update, got %d", equipProbe.loads.Load())
	}

	// Simulate an ACK write failure so handleWeaponSkinDemo returns before
	// sendSelectedActorMode0AppearanceRefreshGuarded. The post-Update hook must
	// still have invalidated townMode0Cache and bumped appearanceRev.
	actorSession.ctx = ctx
	actorSession.accountFromToken = true
	if err := s.startGameSessionEvents(ctx, actorSession); err != nil {
		t.Fatalf("startGameSessionEvents: %v", err)
	}
	t.Cleanup(func() {
		stopCtx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = s.stopGameSessionEvents(stopCtx, actorSession)
	})
	actorSession.townSceneReadyCharacterID = actorID
	actorSession.preDungeonContextPlayerStateSent = false
	actorSession.conn = &pr2FailWriteConn{Conn: actorSession.conn}
	revBefore := currentCrossSceneAppearanceRevision(actorSession)

	req := make([]byte, 20)
	copy(req[:4], "WSQ1")
	binary.LittleEndian.PutUint32(req[4:8], 1)
	binary.LittleEndian.PutUint32(req[8:12], 1)
	req[12] = byte(weaponskin.Apply)
	binary.LittleEndian.PutUint32(req[16:20], 0)

	if err := s.handleWeaponSkinDemo(actorSession, req); err == nil {
		t.Fatal("expected simulated ACK write error")
	}
	if revAfter := currentCrossSceneAppearanceRevision(actorSession); revAfter == revBefore {
		t.Fatalf("expected appearanceRev to advance after weaponskin.Update, stayed %d", revAfter)
	}

	// Verify next observer rebuilds mode0 (cache miss) and observes the applied weapon skin.
	obs2, obs2Conn := newPR2TownObserver(t, s, observer0, 3102)
	if err := s.sendTownRemoteActorState(obs2, actorInfo, townSource); err != nil {
		t.Fatalf("post-weaponskin town mode0 build failed: %v", err)
	}
	if equipProbe.loads.Load() != 2 {
		t.Fatalf("expected cache miss rebuild after weaponskin.Update (loads=2), got %d", equipProbe.loads.Load())
	}
	// Verify third observer hits the refreshed cache (no 3rd DB load).
	obs3, _ := newPR2TownObserver(t, s, observer0, 3103)
	if err := s.sendTownRemoteActorState(obs3, actorInfo, townSource); err != nil {
		t.Fatalf("third observer town mode0 build failed: %v", err)
	}
	if equipProbe.loads.Load() != 2 {
		t.Fatalf("expected third observer to hit refreshed cache (loads=2), got %d", equipProbe.loads.Load())
	}
	frames := townDisplayFrames(t, obs2Conn.write.Bytes())
	foundWeaponSkinMode0 := false
	for _, f := range frames {
		if currentWeaponSkinDemoBodyHasSample(f.body) {
			foundWeaponSkinMode0 = true
			break
		}
	}
	if !foundWeaponSkinMode0 {
		t.Fatalf("refreshed mode0 sent to observer did not reflect newly applied weapon skin (frames=%d)", len(frames))
	}
}
