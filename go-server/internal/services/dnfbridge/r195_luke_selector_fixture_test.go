package dnfbridge

import (
	"context"
	"strconv"
	"testing"
	"time"

	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	dnftown "longheng.io/server/internal/modules/dnf/town"
)

// lukeSelectorPartyOnlySettlementFixture reuses the published real-PVF Luke
// phases and reward flow. Master commits the actual party cards at AutoSelectAt
// and retains the following configured movie window before it stamps FinishedAt.
// Advancing both real boundaries keeps this fixture independent of the private
// cumulative party-only settlement changes.
func lukeSelectorPartyOnlySettlementFixture(t *testing.T) (*raidStartGuardFixture, *gameSession, *gameSession) {
	t.Helper()
	f, leader, at := normalLukeFixture(t)
	peer := f.session()
	peer.selectedCharacterID, peer.accountID = 20, "account-20"
	peer.channel, peer.loginPacketSeen, peer.accountFromToken = leader.channel, true, true
	f.service.bindGameSessionCharacter(peer, 20)

	for _, id := range []uint32{5054, 5055, 5056} {
		for n := 0; n < 3; n++ {
			clearOwnedLuke(t, f, leader, id, at)
			at = at.Add(time.Second)
		}
	}
	battle := f.state.Battle
	if battle == nil || !battle.PhaseCompleted {
		t.Fatal("real Luke phase zero was not completed")
	}
	flow := battle.RewardFlow[0]
	if flow == nil || flow.Mode != 2 || flow.Phase != 0 || flow.Single || flow.squadRequired() || len(flow.Recipients) != 2 {
		t.Fatal("real Luke party-only reward flow missing")
	}
	if _, errs := f.service.advanceCurrentAntonRewardFlows(flow.AutoSelectAt, zeroAntonRoll); len(errs) != 0 {
		t.Fatal(errs)
	}
	var movie uint32
	for _, seconds := range flow.Configuration.MovieEndSeconds {
		if seconds > movie {
			movie = seconds
		}
	}
	if movie == 0 {
		t.Fatal("real Luke reward movie window missing")
	}
	settledAt := flow.AutoSelectAt.Add(time.Duration(movie) * time.Second)
	if _, errs := f.service.advanceCurrentAntonRewardFlows(settledAt, zeroAntonRoll); len(errs) != 0 {
		t.Fatal(errs)
	}
	wantBreakUntil := settledAt.Add(time.Duration(flow.Configuration.PhaseBreakSeconds) * time.Second)
	if !flow.FinishedAt.Equal(settledAt) || !flow.BreakUntil.Equal(wantBreakUntil) {
		t.Fatal("real Luke party-only rewards did not settle")
	}
	for _, owner := range flow.Recipients {
		card := flow.Party[owner.CharacterID]
		if card == nil || !card.Committed {
			t.Fatalf("real Luke party card %d did not commit", owner.CharacterID)
		}
	}
	return f, leader, peer
}

// An independently cleared raid group can retire its completed run into the
// selector. Its town presence was removed on dungeon entry. Returning with
// op132 then confirming with op35 must not require an extra op36 area crossing.
// The public selector repair restores the waiting-area records. A deployment
// that gates phase two on the online start path must additionally use the
// existing cumulative strict gate; these tests verify only its master presence
// primitives and do not claim that the public start entry contains that gate.
func lukeSelectorReturnWaitingFixture(t *testing.T, deferReturn ...bool) (*raidStartGuardFixture, *gameSession, *gameSession) {
	t.Helper()
	f, leader, peer := lukeSelectorPartyOnlySettlementFixture(t)
	s := f.service
	ctx := context.Background()
	towns, err := dnftown.Load(ctx, s.initialEquipmentArchive, dnftown.Options{})
	if err != nil {
		t.Fatal(err)
	}
	s.townCatalog = towns
	s.onlinePlayers = newOnlinePlayerManager()
	// This regression starts at the retired selector boundary. The synthetic
	// map runtime used for phase progression does not exercise the native result UI.
	leader.dungeon.runtime = nil
	peer.conn = &bufferConn{}
	s.raidMu.Lock()
	old := f.state.RaidKey
	f.state.RaidKey = uint32(s.channelAdvertiseID())<<24 | uint32(leader.channel.ID)<<16 | old&0xffff
	delete(s.raids, old)
	s.raids[f.state.RaidKey] = f.state
	s.raidMu.Unlock()
	for _, member := range []*gameSession{leader, peer} {
		member.connectionTownActorOwnerChannel = byte(member.channel.ID)
		member.townActorOwnerChannel = byte(member.channel.ID)
		member.residentChannel = member.channel
		id := strconv.Itoa(int(member.selectedCharacterID))
		character, found, err := f.repos.Character.Load(ctx, id)
		if err != nil || !found {
			t.Fatal("missing character", id, err)
		}
		character.Name, character.Job, character.Level = "member"+id, "0", 90
		character.Stats = map[string]int64{"town_id": 32, "area_id": 4, "pos_x": 580, "pos_y": 243, "direction": 5, "area_state": 3}
		if err := f.repos.Character.Save(ctx, character); err != nil {
			t.Fatal(err)
		}
		area, _ := s.townArea(32, 4)
		quests := dnfrepo.QuestRecord{CharacterID: id, States: map[int64]dnfrepo.QuestState{}}
		for _, quest := range area.NeedQuests {
			quests.States[quest] = dnfrepo.QuestState{Status: "completed"}
		}
		if err := f.repos.Quest.Save(ctx, quests); err != nil {
			t.Fatal(err)
		}
		s.markCurrentTownSceneReady(member)
		if err := s.handleTownSetUserArea(member, buildTownMoveRequest(32, 4, 580, 243, 5)); err != nil {
			t.Fatal(err)
		}
		if _, found := s.onlinePlayers.PlayerForCharacter(member.selectedCharacterID); !found {
			t.Fatal("initial waiting area admission failed", id)
		}
		// This is the real entry-side removal, not a fabricated mismatched key.
		s.leaveTownPresenceForDungeon(member)
		bindDungeonSelectorOriginForTestAt(t, s, member, 32, 4, 580, 243)
		if err := s.sendEnterSelectDungeonState(member, "luke_phase_zero_completed_selector", false, true); err != nil {
			t.Fatal(err)
		}
		if !member.enterSelectDungeonSent || !member.enterSelectDungeonAckSent || !member.enterSelectDungeonContextSent {
			t.Fatal("selector context was not established", id)
		}
		if len(deferReturn) == 0 || !deferReturn[0] {
			if err := s.handleDungeonBackToVillage(member, nil); err != nil {
				t.Fatal(err)
			}
		}
		if _, found := s.onlinePlayers.PlayerForCharacter(member.selectedCharacterID); found {
			t.Fatal("unconfirmed selector return published presence", id)
		}
	}
	return f, leader, peer
}

func lukeSelectorWaitingInventories(t *testing.T, f *raidStartGuardFixture) map[uint16]dnfrepo.InventoryRecord {
	t.Helper()
	inventories := make(map[uint16]dnfrepo.InventoryRecord, len(f.state.Members))
	for _, member := range f.state.Members {
		inventory, found, err := f.repos.Inventory.Load(context.Background(), strconv.Itoa(int(member.CharID)))
		if err != nil || !found {
			t.Fatal("load waiting inventory", member.CharID, found, err)
		}
		inventories[member.CharID] = inventory
	}
	return inventories
}

// lukeSelectorWaitingPresenceError exercises the production waiting-area
// authority exactly as a strict phase gate would: freeze the current raid
// scope under raidMu, then require every member's committed town presence.
// It deliberately does not enter startRuntimeRaidWithEntryCost, which lacks
// that cumulative phase-two guard in the public baseline.
func lukeSelectorWaitingPresenceError(t *testing.T, f *raidStartGuardFixture) error {
	t.Helper()
	s := f.service
	s.raidMu.Lock()
	defer s.raidMu.Unlock()
	state := cloneRuntimeRaidState(*f.state)
	if state.Battle == nil {
		t.Fatal("real Luke battle missing")
	}
	leader, found := s.onlineGameSession(state.LeaderID)
	if !found {
		t.Fatal("real Luke leader session missing")
	}
	scope, err := s.prepareCurrentRaidWaitingBattleScope(leader, state, state.Battle)
	if err != nil {
		t.Fatal("prepare current raid waiting battle scope", err)
	}
	return s.onlinePlayers.requireRaidStartWaitingArea(scope, state.Battle.InstanceID)
}
