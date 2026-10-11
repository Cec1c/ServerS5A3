package dnfbridge

import (
	"context"
	"encoding/binary"
	"testing"

	"longheng.io/server/internal/modules/dnf/dnfenum"
	"longheng.io/server/internal/modules/dnf/dungeoncmd"
	"longheng.io/server/internal/modules/dnf/worldmap"
)

func TestPartyBossForcedAddsDieAndBothMembersEnterSettlement(t *testing.T) {
	service, leaderRuntime := prepareTutorialScopeRuntime(t, tutorialScopeFixtureOptions{disableTutorial: true})
	repositories, _ := service.repositoryGroup()
	character := leaderRuntime.Character
	character.CharacterID = "100"
	if err := repositories.Character.Save(context.Background(), character); err != nil {
		t.Fatal(err)
	}
	leader := &gameSession{conn: &bufferConn{}, selectedCharacterID: 99,
		dungeon: dungeonSessionState{runtime: leaderRuntime}}
	follower := &gameSession{conn: &bufferConn{}, selectedCharacterID: 100}
	followerRuntime, _, err := service.prepareDungeonRuntime(context.Background(), follower,
		dungeoncmd.SelectDungeonRequest{DungeonID: 700, Difficulty: 1})
	if err != nil {
		t.Fatal(err)
	}
	follower.dungeon.runtime = followerRuntime
	service.bindGameSessionCharacter(leader, 99)
	service.bindGameSessionCharacter(follower, 100)
	state := runtimePartyStateForSyncFixture()
	storeRuntimePartyState(leader, state)
	storeRuntimePartyState(follower, state)
	for _, member := range []*gameSession{leader, follower} {
		runtime := member.dungeon.runtime
		if err := runtime.Room.AttachExtendedActors(runtimeDungeonExtendedActorPlan{Actors: []runtimeDungeonExtendedActor{
			{Kind: runtimeDungeonActorSpecialMonster, ObjectKey: 9001, State: runtimeDungeonMonsterPlanned},
			{Kind: runtimeDungeonActorAICharacter, ObjectKey: 9002, State: runtimeDungeonMonsterPlanned},
			{Kind: runtimeDungeonActorSpecialObject, ObjectKey: 9003, State: runtimeDungeonMonsterPlanned},
			{Kind: runtimeDungeonActorSpecialMonster, ObjectKey: 9004, HellPartyOrder: 1, State: runtimeDungeonMonsterPlanned},
		}}); err != nil {
			t.Fatal(err)
		}
		if _, err := runtime.Room.AnnounceAllActors(runtime.Session); err != nil {
			t.Fatal(err)
		}
		scene, _ := runtime.Session.Scene()
		if service.adoptPartyDungeonInstance(member, runtime, scene) == nil {
			t.Fatal("no shared instance")
		}
	}
	room := leaderRuntime.Room.Snapshot()
	boss, add := room.Monsters[0].ObjectKey, room.Monsters[1].ObjectKey
	body := make([]byte, dungeoncmd.DieMonsterRequestSize)
	binary.LittleEndian.PutUint32(body, boss)
	binary.LittleEndian.PutUint16(body[4:], 99)
	if err := service.handleDungeonMonsterDeath(leader, body); err != nil {
		t.Fatal(err)
	}
	for _, member := range []*gameSession{leader, follower} {
		runtime := member.dungeon.runtime
		snapshot := runtime.Session.Snapshot()
		if snapshot.Run.Status != worldmap.DungeonRunCompleted || !snapshot.Scene.Cleared ||
			!runtime.settlementEntrySent || !dungeonSceneObjectDefeated(snapshot.Scene.DefeatedObjects, add) {
			t.Fatalf("member %d missing forced-add death/completion: status=%v cleared=%v entry=%v defeated=%v",
				member.selectedCharacterID, snapshot.Run.Status, snapshot.Scene.Cleared,
				runtime.settlementEntrySent, snapshot.Scene.DefeatedObjects)
		}
		var addDeath, specialDeath, entry bool
		for _, frame := range splitUpperFrames(t, member.conn.(*bufferConn).write.Bytes()) {
			if frame.msgID == uint16(dnfenum.CmdPacketNotifyDieMonster) &&
				len(frame.body) >= 2 && binary.LittleEndian.Uint16(frame.body) == uint16(add) {
				addDeath = true
			}
			if frame.msgID == uint16(dnfenum.CmdPacketNotifyDieMonster) &&
				len(frame.body) >= 2 && binary.LittleEndian.Uint16(frame.body) == 9001 {
				specialDeath = true
			}
			if frame.msgID == uint16(dnfenum.CmdPacketNotifyDieMonster) && len(frame.body) >= 2 &&
				binary.LittleEndian.Uint16(frame.body) >= 9002 {
				t.Fatal("boss clear retired friendly AI, passive object or future abyss template")
			}
			if frame.msgID == currentDungeonSettlementEntryMsgID {
				entry = true
			}
		}
		if !addDeath || !specialDeath || !entry {
			t.Fatalf("member %d missing ordinary/special add op38 or settlement op31", member.selectedCharacterID)
		}
	}
	for _, actor := range leaderRuntime.Room.Snapshot().ExtendedActors {
		if (actor.ObjectKey == 9001) != (actor.State == runtimeDungeonMonsterDefeated) {
			t.Fatalf("extended actor retirement changed wrong actor: %+v", actor)
		}
	}
	if replay, ok := leaderRuntime.Room.DeathResponseBody(9001); !ok || len(replay) != 7 {
		t.Fatal("special-add death missing authoritative replay")
	}
	if replay, ok := leaderRuntime.Room.DeathResponseBody(add); !ok || len(replay) == 0 {
		t.Fatal("forced-add death missing authoritative replay")
	}
}
