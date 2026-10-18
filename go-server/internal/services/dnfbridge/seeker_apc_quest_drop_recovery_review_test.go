package dnfbridge

import (
	"fmt"
	"strings"
	"testing"

	"longheng.io/server/internal/modules/dnf/dnfenum"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	"longheng.io/server/internal/modules/dnf/worldmap"
)

func seekerQST022FinalBossState(
	fixture *seekerQST022Fixture,
	packets []seekerQST022WirePacket,
) string {
	snapshot := fixture.runtime.Session.Snapshot()
	room := fixture.runtime.Room.Snapshot()
	return fmt.Sprintf(
		"run=%s scene_cleared=%t defeated=%v room_monster=%s room_apc=%s ordinary_clear=%t boss_accepted=%t boss_response=%t settlement=%t msg_ids=%v",
		snapshot.Run.Status,
		snapshot.Scene.Cleared,
		snapshot.Scene.DefeatedObjects,
		room.Monsters[0].State,
		room.ExtendedActors[0].State,
		fixture.runtime.ordinaryFinalRoomClearAccepted,
		fixture.runtime.bossDieCheckAccepted,
		fixture.runtime.bossDieCheckResponseSent,
		fixture.runtime.settlementEntrySent,
		seekerQST022PacketMessageIDs(packets),
	)
}

func prepareSeekerQST022FinalBossFixture(
	t *testing.T,
) (*seekerQST022Fixture, uint32, uint32) {
	t.Helper()
	fixture := newSeekerQST022Fixture(t, seekerQST022FixtureOptions{
		dungeonID:  seekerQST022DungeonID,
		difficulty: 1,
		active:     true,
	})
	monsterObjectKey, apcObjectKey := seekerQST022AssertActorIdentity(t, fixture)

	// The existing QST022 fixture already renders both a blocking ordinary
	// hostile and a hostile APC. Project that same room into the established
	// story-AI-boss shape: one blocking dummy Boss plus one type-8 APC Boss.
	fixture.runtime.Room.mu.Lock()
	monsterIndex := fixture.runtime.Room.byObjectKey[monsterObjectKey]
	apcIndex := fixture.runtime.Room.extendedByObjectKey[apcObjectKey]
	fixture.runtime.Room.monsters[monsterIndex].Spawn.Rank = "[dummy]"
	fixture.runtime.Room.monsters[monsterIndex].Spawn.SuffixMarker = "[boss]"
	fixture.runtime.Room.extendedActors[apcIndex].AICharacter.AIType = "[boss]"
	fixture.runtime.Room.extendedActors[apcIndex].Packet.Type = 8
	fixture.runtime.Room.mu.Unlock()

	scene, ok := fixture.runtime.Session.Scene()
	if !ok {
		t.Fatal("QST022 final-boss scene unavailable")
	}
	if boss, actorType, source := currentDungeonRuntimeObjectBossActor(
		fixture.runtime,
		apcObjectKey,
		runtimeDungeonMonsterAnnounced,
	); !boss || actorType != 8 || source != "86jp_extended_actor_type" {
		t.Fatalf("APC final-boss identity: boss=%t type=%d source=%q", boss, actorType, source)
	}
	gate, active := currentDungeonStoryAIBossDeathGate(fixture.runtime, scene)
	if !active || gate.Ready ||
		len(gate.DummyBossObjectKeys) != 1 || gate.DummyBossObjectKeys[0] != monsterObjectKey ||
		len(gate.AIBossObjectKeys) != 1 || gate.AIBossObjectKeys[0] != apcObjectKey {
		t.Fatalf("QST022 final-boss gate active=%t gate=%+v dummy=%d APC=%d",
			active, gate, monsterObjectKey, apcObjectKey)
	}
	return fixture, monsterObjectKey, apcObjectKey
}

func seekerQST022PacketMessageIDs(packets []seekerQST022WirePacket) []uint16 {
	ids := make([]uint16, 0, len(packets))
	for _, packet := range packets {
		ids = append(ids, packet.msgID)
	}
	return ids
}

func seekerQST022HasMessageID(packets []seekerQST022WirePacket, want uint16) bool {
	for _, packet := range packets {
		if packet.msgID == want {
			return true
		}
	}
	return false
}

func seekerQST022KillFinalBossPair(
	t *testing.T,
	fixture *seekerQST022Fixture,
	monsterObjectKey uint32,
	apcObjectKey uint32,
) []seekerQST022WirePacket {
	t.Helper()
	if err := seekerQST022SendDeath(t, fixture, monsterObjectKey); err != nil {
		t.Fatalf("kill final-room dummy Boss: %v", err)
	}
	beforeAPC := fixture.runtime.Session.Snapshot()
	room := fixture.runtime.Room.Snapshot()
	if beforeAPC.Run.Status != worldmap.DungeonRunActive ||
		!beforeAPC.Scene.Cleared ||
		fixture.runtime.ordinaryFinalRoomClearAccepted ||
		fixture.runtime.bossDieCheckAccepted ||
		fixture.runtime.settlementEntrySent ||
		room.Monsters[0].State != runtimeDungeonMonsterDefeated ||
		room.ExtendedActors[0].State != runtimeDungeonMonsterAnnounced {
		t.Fatalf("dummy Boss changed final-room ownership incorrectly: run=%s scene_cleared=%t ordinary_clear=%t boss_accepted=%t settlement=%t dummy=%s APC=%s",
			beforeAPC.Run.Status,
			beforeAPC.Scene.Cleared,
			fixture.runtime.ordinaryFinalRoomClearAccepted,
			fixture.runtime.bossDieCheckAccepted,
			fixture.runtime.settlementEntrySent,
			room.Monsters[0].State,
			room.ExtendedActors[0].State)
	}

	fixture.connection.write.Reset()
	if err := seekerQST022SendDeath(t, fixture, apcObjectKey); err != nil {
		t.Fatalf("kill final-room APC Boss: %v", err)
	}
	return seekerQST022Packets(t, fixture.connection.write.Bytes())
}

func TestSeekerQST022FinalBossQuestDropFailureRetryCompletesSettlement(t *testing.T) {
	t.Run("normal final-boss death completes settlement", func(t *testing.T) {
		fixture, monsterObjectKey, apcObjectKey := prepareSeekerQST022FinalBossFixture(t)
		packets := seekerQST022KillFinalBossPair(t, fixture, monsterObjectKey, apcObjectKey)

		snapshot := fixture.runtime.Session.Snapshot()
		if snapshot.Run.Status != worldmap.DungeonRunCompleted ||
			!snapshot.Scene.Cleared ||
			!fixture.runtime.ordinaryFinalRoomClearAccepted ||
			!fixture.runtime.bossDieCheckAccepted ||
			!fixture.runtime.bossDieCheckResponseSent ||
			!fixture.runtime.settlementEntrySent {
			t.Fatalf("normal APC Boss death did not complete settlement: %s",
				seekerQST022FinalBossState(fixture, packets))
		}
		if !seekerQST022HasMessageID(packets, uint16(dnfenum.CmdPacketWalkoutPartyMember)) ||
			!seekerQST022HasMessageID(packets, uint16(dnfenum.CmdPacketNotifyDieMonster)) ||
			!seekerQST022HasMessageID(packets, currentDungeonSettlementEntryMsgID) {
			t.Fatalf("normal final-boss packet chain=%v want op14/op38/op31", seekerQST022PacketMessageIDs(packets))
		}
		seekerQST022AssertInventory(t, fixture, 1)
	})

	t.Run("Type2 transaction failure retry completes settlement", func(t *testing.T) {
		fixture, monsterObjectKey, apcObjectKey := prepareSeekerQST022FinalBossFixture(t)
		if err := seekerQST022SendDeath(t, fixture, monsterObjectKey); err != nil {
			t.Fatalf("kill final-room dummy Boss: %v", err)
		}

		items := &seekerQST022FailOnceItems{base: fixture.repositories.CharacterItems}
		broken := fixture.repositories
		broken.CharacterItems = items
		fixture.service.repositoryProvider = func() (dnfrepo.Group, bool) { return broken, true }
		fixture.connection.write.Reset()
		err := seekerQST022SendDeath(t, fixture, apcObjectKey)
		if err == nil || !strings.Contains(err.Error(), "QST022 injected") ||
			!strings.Contains(err.Error(), "inventory transaction failure") {
			t.Fatalf("final-boss Type2 failure=%v want injected transaction failure", err)
		}

		failedPackets := seekerQST022Packets(t, fixture.connection.write.Bytes())
		failedSnapshot := fixture.runtime.Session.Snapshot()
		failedRoom := fixture.runtime.Room.Snapshot()
		if failedRoom.Monsters[0].State != runtimeDungeonMonsterDefeated ||
			failedRoom.ExtendedActors[0].State != runtimeDungeonMonsterDefeated ||
			!failedSnapshot.Scene.Cleared ||
			failedSnapshot.Run.Status != worldmap.DungeonRunActive ||
			fixture.runtime.ordinaryFinalRoomClearAccepted ||
			fixture.runtime.bossDieCheckAccepted ||
			fixture.runtime.bossDieCheckResponseSent ||
			fixture.runtime.settlementEntrySent {
			t.Fatalf("failed final-boss death changed completion ownership incorrectly: %s",
				seekerQST022FinalBossState(fixture, failedPackets))
		}
		if len(failedPackets) != 0 {
			t.Fatalf("failed final-boss death sent packets=%v", seekerQST022PacketMessageIDs(failedPackets))
		}
		seekerQST022AssertInventory(t, fixture, 0)

		retryConnection := &bufferConn{}
		fixture.connection = retryConnection
		fixture.session.conn = retryConnection
		if err := seekerQST022SendDeath(t, fixture, apcObjectKey); err != nil {
			t.Fatalf("retry final-boss Type2 transaction: %v", err)
		}
		retryPackets := seekerQST022Packets(t, retryConnection.write.Bytes())
		snapshot := fixture.runtime.Session.Snapshot()
		if snapshot.Run.Status != worldmap.DungeonRunCompleted ||
			!snapshot.Scene.Cleared ||
			!fixture.runtime.ordinaryFinalRoomClearAccepted ||
			!fixture.runtime.bossDieCheckAccepted ||
			!fixture.runtime.bossDieCheckResponseSent ||
			!fixture.runtime.settlementEntrySent ||
			!seekerQST022HasMessageID(retryPackets, currentDungeonSettlementEntryMsgID) {
			t.Fatalf("Type2 retry dropped the final-room completion chain: %s",
				seekerQST022FinalBossState(fixture, retryPackets))
		}
		seekerQST022AssertInventory(t, fixture, 1)
	})
}
