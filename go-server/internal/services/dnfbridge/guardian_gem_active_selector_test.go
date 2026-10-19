package dnfbridge

import (
	"context"
	"encoding/binary"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	"reflect"
	"testing"
)

// The active selector preserves the town actor. The last full generation
// refresh must therefore already have restored its medal before op15/op27.
func TestP161LoginRebuildThenActiveSelectorKeepsWornMedal(t *testing.T) {
	t.Setenv(loginActorGenerationRebuildEnv, "")
	service, session, conn, _ := newSceneReadyReplayFixture(t)
	p161BindSessionOwner(t, service, session)
	item, words := p161SeedWornMedalSockets(t, service)
	p161MakeFixturePetValid(t, service)
	repos, _ := service.repositoryGroup()
	before, found, err := repos.Equipment.Load(context.Background(), p161CharacterID)
	if err != nil || !found {
		t.Fatalf("load equipment: found=%t err=%v", found, err)
	}
	armLoginRouteUpToOp24(session)
	session.initialTownRouteStage = currentInitialTownRouteTransitionSent
	session.initialTownLegacySceneReadyAccepted = true
	conn.write.Reset()
	if err := service.rebuildLoginActorGenerationAfterSceneCommit(session, "p161_before_active_selector", 6); err != nil {
		t.Fatal(err)
	}
	boundary := conn.write.Len()
	session.townMu.Lock()
	session.townSceneReadyCharacterID = session.selectedCharacterID
	session.townPositionSnapshot = currentTownPositionSnapshot{CharacterID: session.selectedCharacterID, TownID: 38, AreaID: 0, PositionX: 884, PositionY: 248, PositionValid: true}
	session.townMu.Unlock()
	if err := service.sendEnterSelectDungeonState(session, "p161_active_selector", false, true); err != nil {
		t.Fatal(err)
	}
	if !session.enterSelectDungeonContextSent || !session.enterSelectDungeonSent {
		t.Fatal("selector did not open")
	}
	p161RequireWornMedalRefresh(t, conn.write.Bytes(), item, words)
	frames := loginActorGenerationSplit(t, conn.write.Bytes()[boundary:])
	ack, contextSeen := false, false
	for _, f := range frames {
		if f.msgID == 2 {
			t.Fatal("active selector unexpectedly rebuilt actor")
		}
		if f.msgID == 15 {
			ack = true
		}
		if f.msgID == currentDungeonContextMsgID {
			contextSeen = true
		}
	}
	if !ack || !contextSeen {
		t.Fatal("selector ACK/context missing")
	}
	after, _, err := repos.Equipment.Load(context.Background(), p161CharacterID)
	if err != nil || !reflect.DeepEqual(before, after) {
		t.Fatalf("selector changed durable equipment: %v", err)
	}
}

func TestP161PassiveSelectorPreservesExistingMedalGeneration(t *testing.T) {
	t.Setenv(partySelectorPreserveActorEnv, "on")
	repos, service, leader, member, leaderConn, memberConn := newTownCoPresenceTestService(t)
	raw := currentGuardianGemTestRaw(32, 100380058, 1)
	for i, item := range p161RealGuardianGemItemIDs {
		word, err := currentGuardianGemSocketValue(item)
		if err != nil {
			t.Fatal(err)
		}
		binary.LittleEndian.PutUint16(raw[101+i*2:103+i*2], word)
	}
	equipment := dnfrepo.EquipmentRecord{CharacterID: "1002", Entries: map[string]dnfrepo.EquipmentEntry{"32": {SlotIndex: 32, ItemID: 100380058, RawEntry: raw, Extra: map[string]string{"current_exe_equipment_type": "32"}}}}
	if err := repos.Equipment.Save(context.Background(), equipment); err != nil {
		t.Fatal(err)
	}
	storeRuntimePartyState(leader, townCoPresencePartyState())
	storeRuntimePartyState(member, townCoPresencePartyState())
	if err := service.sendSelectedGuardianGemWornMedalRefresh(member, "p161_existing_town_generation"); err != nil {
		t.Fatal(err)
	}
	if len(p161WornMedalRows(t, memberConn.write.Bytes(), 100380058)) != 1 {
		t.Fatal("initial medal row missing")
	}
	memberConn.write.Reset()
	leaderConn.write.Reset()
	if err := service.synchronizeRuntimePartyMembersEnterSelect(leader, "p161_passive_selector"); err != nil {
		t.Fatal(err)
	}
	frames := loginActorGenerationSplit(t, memberConn.write.Bytes())
	if len(frames) != 3 || frames[0].msgID != 15 || frames[1].msgID != currentFatigueMsgID || frames[2].msgID != currentDungeonContextMsgID {
		t.Fatalf("selector replaced the established town generation: %+v", frames)
	}
	if member.preDungeonContextPlayerStateSent || member.selectPreviewActorRemoved || !member.enterSelectDungeonContextSent {
		t.Fatal("passive selector did not retain town actor")
	}
	if leaderConn.write.Len() != 0 {
		t.Fatal("follower synchronization wrote to leader")
	}
	after, _, err := repos.Equipment.Load(context.Background(), "1002")
	if err != nil || !reflect.DeepEqual(equipment, after) {
		t.Fatalf("passive selector changed equipment: %v", err)
	}
}
