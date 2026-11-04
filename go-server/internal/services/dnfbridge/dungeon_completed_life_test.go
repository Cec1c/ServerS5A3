package dnfbridge

import (
	"bytes"
	"encoding/binary"
	"testing"

	"longheng.io/server/internal/modules/dnf/dnfenum"
	"longheng.io/server/internal/modules/dnf/dungeoncmd"
)

func TestCompletedDeathRequiresCurrentOrdinarySettlement(t *testing.T) {
	for _, boundary := range []string{"wrong_owner", "old_run", "missing_entry", "unproved_completion", "pending_return", "raid", "tournament", "tower", "malformed"} {
		t.Run(boundary, func(t *testing.T) {
			s, p, r := newBackToVillageRuntime(t)
			q := newFakeCurrentDungeonDeathTimerQueue()
			s.gameplayTimers = q
			dispatch := func(op uint16, body []byte) {
				t.Helper()
				if err := s.handleGameUpper(p, buildCurrentDungeonClientUpperForPhaseTest(t, op, body)); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := r.Room.AnnounceAllActors(r.Session); err != nil {
				t.Fatal(err)
			}
			boss := r.Room.Snapshot().Monsters[0].ObjectKey
			death := make([]byte, dungeoncmd.DieMonsterRequestSize)
			binary.LittleEndian.PutUint32(death, boss)
			binary.LittleEndian.PutUint16(death[4:], 99)
			dispatch(uint16(dnfenum.CmdPacketDieMonster), death)
			dispatch(uint16(dnfenum.CmdPacketBossDieCheck), bossDieCheckRequestBody(99, uint16(boss)))
			if !currentDungeonCompletedLifeOwner(r, p) {
				t.Fatal("fixture must enter a committed ordinary settlement")
			}
			body := make([]byte, currentDungeonDieCharacterBodySize)
			switch boundary {
			case "wrong_owner":
				p.selectedCharacterID = 100
			case "old_run":
				p.dungeon.runToken++
			case "missing_entry":
				r.settlementEntrySent = false
			case "unproved_completion":
				r.bossDieCheckAccepted = false
			case "pending_return":
				r.townReturnPending = true
			case "raid":
				r.raidEntry = &currentRaidDungeonEntry{}
			case "tournament":
				r.Tournament = &tournamentDungeonRuntime{}
			case "tower":
				if r.Dungeon.Metadata.IntegerValues == nil {
					r.Dungeon.Metadata.IntegerValues = make(map[string]int64)
				}
				r.Dungeon.Metadata.IntegerValues["tower of grief"] = 1
			case "malformed":
				body = append(body, 1, 0)
			}
			phase, pending := r.settlementPhase, r.townReturnPending
			conn := p.conn.(*bufferConn)
			conn.write.Reset()
			dispatch(uint16(dnfenum.CmdPacketDieCharacter), body)
			if conn.write.Len() != 0 || r.deathStateSent || r.deathReturnWaiting || r.settlementPhase != phase || r.townReturnPending != pending {
				t.Fatal("invalid completed death changed life, scene or settlement")
			}
			if _, _, active := q.counts(); active != 0 {
				t.Fatal("invalid completed death scheduled a timer")
			}
		})
	}
}

func TestCompletedPartyDeathAndRevivePreserveBothScenes(t *testing.T) {
	s, leader, member, leaderRuntime, memberRuntime := partyDungeonDeathFixture(t)
	// The shared-room fixture installs prepared runtimes directly. Bind the
	// per-session lifetime that a real commitDungeonRuntime assigns at entry.
	for _, p := range []*gameSession{leader, member} {
		p.dungeon.runToken = 1
		p.dungeon.runtime.lifecycleToken = 1
	}
	q := newFakeCurrentDungeonDeathTimerQueue()
	s.gameplayTimers = q
	for _, r := range []*runtimeDungeonState{leaderRuntime, memberRuntime} {
		if err := r.Session.CompleteCurrentRoom(); err != nil {
			t.Fatal(err)
		}
		// This fixture isolates life fanout; the single-player dispatcher
		// regression exercises the actual boss/result/card completion chain.
		r.settlementEntrySent = true
		r.tutorialCompletionPersisted = true
		r.bossDieCheckAccepted = true
		r.bossDieCheckResponseSent = true
	}
	for _, p := range []*gameSession{leader, member} {
		p.conn.(*bufferConn).write.Reset()
	}
	if err := s.handleDungeonDieCharacter(member, make([]byte, currentDungeonDieCharacterBodySize)); err != nil {
		t.Fatal(err)
	}
	frames := splitUpperFrames(t, leader.conn.(*bufferConn).write.Bytes())
	if len(frames) != 2 || frames[0].msgID != 32 || !bytes.Equal(frames[0].body, []byte{100, 0, 0, 0}) || frames[1].msgID != 153 {
		t.Fatal("completed death did not project life to the peer")
	}
	leader.conn.(*bufferConn).write.Reset()
	if err := s.handleDungeonUseCoin(member, []byte{100, 0}); err != nil {
		t.Fatal(err)
	}
	frames = splitUpperFrames(t, leader.conn.(*bufferConn).write.Bytes())
	if len(frames) != 2 || !bytes.Equal(frames[0].body, []byte{100, 0, 1, 0}) || frames[1].msgID != 153 {
		t.Fatal("completed revive did not project life to the peer")
	}
	if leader.dungeon.runtime != leaderRuntime || member.dungeon.runtime != memberRuntime || leaderRuntime.townReturnPending || memberRuntime.townReturnPending || memberRuntime.deathStateSent {
		t.Fatal("completed life transition changed a party scene")
	}
	if _, _, active := q.counts(); active != 0 {
		t.Fatal("completed party death scheduled an automatic return")
	}
}
