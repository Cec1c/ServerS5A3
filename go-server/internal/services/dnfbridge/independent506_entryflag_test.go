package dnfbridge

import (
	"encoding/binary"
	"testing"

	"longheng.io/server/internal/modules/dnf/dnfenum"
	"longheng.io/server/internal/modules/dnf/dungeoncmd"
)

// Candidate-only assertions. These reference identifiers that only exist in the
// candidate (the private teamMode fact), so the file lives in the candidate
// tree only; the portable RED/GREEN lives in
// independent506_prepared_party_test.go.
func TestIndependent506FrozenEntryCoversEveryParticipant(t *testing.T) {
	for _, kind := range []string{"anton", "luke"} {
		t.Run(kind, func(t *testing.T) {
			party := independent506PrepareParty(t, kind, false, true)
			for index, runtime := range []*runtimeDungeonState{party.leaderRuntime, party.peerRuntime} {
				if runtime.raidEntry == nil || !runtime.raidEntry.teamMode {
					t.Fatalf("participant %d lost the frozen team mode fact", index)
				}
				if !currentTeamRaidGroundDropsDisabled(runtime) {
					t.Fatalf("participant %d is not covered by the ground drop ban", index)
				}
			}
		})
	}
	// A single raid must not carry the fact at all.
	single := independent506PrepareParty(t, "anton", true, false)
	if single.leaderRuntime.raidEntry == nil || single.leaderRuntime.raidEntry.teamMode {
		t.Fatal("single raid carried the team mode fact")
	}
	if !currentTeamRaidGroundDropsDisabled(single.leaderRuntime) {
		t.Fatal("single Anton lost its independent ground drop ban")
	}
}

// The ban only zeroes the planned drop list; it must not cut the monster death
// ACK itself. Same call, same fixture, only the frozen team flag differs.
// A legacy-shaped runtime keeps the 62-byte request decoder this fixture drives;
// the ground ban reads only runtime.raidEntry.teamMode.
func TestIndependent506TeamBanKeepsDeathAck(t *testing.T) {
	for _, team := range []bool{false, true} {
		name := "plain"
		if team {
			name = "team-ban"
		}
		t.Run(name, func(t *testing.T) {
			service, runtime, _ := prepareCurrentDungeonDropTest(t, 0, 3227)
			connection := &bufferConn{}
			session := &gameSession{conn: connection, connID: "ind474-ack", selectedCharacterID: 99, dungeon: dungeonSessionState{runtime: runtime}}
			if team {
				runtime.raidEntry = &currentRaidDungeonEntry{RunID: "506ack", Participants: []uint16{99}, teamMode: true}
			}
			monster := review452PositiveMonsterRules(t, service, runtime)
			body := make([]byte, dungeoncmd.DieMonsterRequestSize)
			binary.LittleEndian.PutUint32(body[0:4], monster.ObjectKey)
			binary.LittleEndian.PutUint16(body[4:6], currentSceneActorObjectKey(99))
			if err := service.handleDungeonMonsterDeath(session, body); err != nil {
				t.Fatal(err)
			}
			packet, _ := splitGameServerUpperPacket(t, connection.write.Bytes())
			if packet.Header.MsgID != uint16(dnfenum.CmdPacketNotifyDieMonster) {
				t.Fatalf("death ACK missing: header=%+v", packet.Header)
			}
			if team && packet.Body[2] != 0 {
				t.Fatalf("team death ACK carried %d drops", packet.Body[2])
			}
			if !team && packet.Body[2] == 0 {
				t.Fatal("plain death ACK lost its ground drops")
			}
		})
	}
}
