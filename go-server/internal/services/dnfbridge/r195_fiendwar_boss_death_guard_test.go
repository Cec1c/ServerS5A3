package dnfbridge

import (
	"encoding/binary"
	"reflect"
	"strings"
	"testing"

	"longheng.io/server/internal/modules/dnf/dungeoncmd"
	"longheng.io/server/internal/modules/dnf/worldmap"
)

// Keep the real FiendWar config and authenticated encounter owner from the
// existing fixture. The announced combat actors are synthetic, as in that
// fixture; these tests prove packet/commit boundaries, not native rendering.
func r195BossDeathGuardFixture(t *testing.T, adds bool) (*raidStartGuardFixture, []*gameSession, *runtimeDungeonState, []*deadlineAuditV10Conn) {
	t.Helper()
	f, peers, runs, conns := r195BossLiveFixture(t)
	r := runs[0]
	if adds {
		_, combat := prepareTutorialScopeRuntime(t, tutorialScopeFixtureOptions{disableTutorial: true})
		combat.Dungeon.ID, combat.AccountID, combat.Character = r.Dungeon.ID, r.AccountID, r.Character
		combat.raidEntry = r.raidEntry
		r = combat
		peers[0].dungeon.runtime = r
	}
	// The original fixture only needs a temporary party key for admission.
	// No shared combat instance is fabricated for this solo packet test.
	r.partyInstance = nil
	if _, err := r.Room.AnnounceAllActors(r.Session); err != nil {
		t.Fatal(err)
	}
	return f, peers, r, conns
}

func r195BossDeathGuardDispatch(t *testing.T, s *Service, p *gameSession, command uint16, target uint32) {
	t.Helper()
	body := bossDieCheckRequestBody(currentSceneActorObjectKey(p.selectedCharacterID), uint16(target))
	if command == 39 {
		body = make([]byte, dungeoncmd.DieMonsterRequestSize)
		if s.options.currentClientVersion == "official-24.9" {
			body = make([]byte, dungeoncmd.Official249DieMonsterBaseSize+dungeoncmd.Official249DieMonsterEntrySize)
			body[30] = 1
		}
		binary.LittleEndian.PutUint32(body, target)
		binary.LittleEndian.PutUint16(body[4:6], currentSceneActorObjectKey(p.selectedCharacterID))
		decoded, err := s.decodeCurrentDungeonMonsterDeath(body)
		if err != nil || decoded.RuntimeObjectKey != target || decoded.OwnerObjectKey != currentSceneActorObjectKey(p.selectedCharacterID) {
			t.Fatalf("death fixture does not match the active protocol: decoded=%+v err=%v", decoded, err)
		}
	}
	if err := s.handleGameUpper(p, buildCurrentDungeonClientUpperForPhaseTest(t, command, body)); err != nil {
		t.Fatalf("op%d: %v", command, err)
	}
}

func TestR195FiendWarBossDeathGuardRejectsWaitingAndRestartPackets(t *testing.T) {
	for _, tc := range []struct {
		name        string
		state, pool int
	}{{"waiting", 4, 1}, {"stale_state9_with_ball", 9, 1}, {"new_round", 5, 1111}} {
		t.Run(tc.name, func(t *testing.T) {
			f, peers, r, conns := r195BossDeathGuardFixture(t, false)
			b := f.state.Battle
			b.FiendWar.Symbols[b.FiendWar.Config.Symbols["top boss state"]] = tc.state
			b.FiendWar.Symbols[b.FiendWar.Config.Symbols["boss pattern state"]] = tc.pool
			before, beforeRoom, beforeBattle := r.Session.Snapshot(), r.Room.Snapshot(), b.FiendWar.Clone()
			key := beforeRoom.Monsters[0].ObjectKey
			for _, command := range []uint16{117, 39, 117, 39} {
				r195BossDeathGuardDispatch(t, f.service, peers[0], command, key)
			}
			if !reflect.DeepEqual(before, r.Session.Snapshot()) || !reflect.DeepEqual(beforeRoom, r.Room.Snapshot()) {
				t.Fatal("closed window committed a death or completed the active room")
			}
			if !reflect.DeepEqual(beforeBattle.Symbols, b.FiendWar.Symbols) || !reflect.DeepEqual(beforeBattle.Areas, b.FiendWar.Areas) ||
				!reflect.DeepEqual(beforeBattle.DungeonStates, b.FiendWar.DungeonStates) || beforeBattle.BossWindowDone != b.FiendWar.BossWindowDone ||
				len(b.ClearedRuns) != 0 || len(b.MemberClears) != 0 || len(b.FiendWarEncounters) != 2 || b.PhaseCompleted {
				t.Fatal("closed window changed raid progress or encounter ownership")
			}
			if r.bossDieCheckAccepted || r.ordinaryFinalRoomClearAccepted || r.bossDieCheckPending || r.settlementEntrySent || len(conns[0].bytes()) != 0 {
				t.Fatal("closed window emitted death/settlement or retained completion")
			}
			if logs := f.logText(t); !strings.Contains(logs, "game-fiendwar-boss-death-blocked") || !strings.Contains(logs, "fiendwar_boss_damage_window_closed") {
				t.Fatalf("packet did not reach the shared-window guard: %s", logs)
			}
		})
	}
}

func TestR195FiendWarBossDeathGuardRechecksCompletionEntrances(t *testing.T) {
	for _, entry := range []string{"op117", "ordinary", "settlement_retry"} {
		t.Run(entry, func(t *testing.T) {
			f, peers, r, conns := r195BossDeathGuardFixture(t, false)
			key := r.Room.Snapshot().Monsters[0].ObjectKey
			// A previously accepted death can outlive a window; it is not a
			// permit for a later completion while a new round still has balls.
			if _, cleared, err := r.Room.CommitActorDeathReport(key, r.Session); err != nil || !cleared {
				t.Fatalf("prepare old death: cleared=%t err=%v", cleared, err)
			}
			if entry == "settlement_retry" {
				if err := r.Session.Complete(); err != nil {
					t.Fatal(err)
				}
				r.bossDieCheckAccepted = true
				r.bossDieCheckRelatedActorObjectKey = currentSceneActorObjectKey(peers[0].selectedCharacterID)
				r.bossDieCheckTargetObjectKey = uint16(key)
			}
			before := r.Session.Snapshot()
			if entry == "ordinary" {
				peers[0].dungeon.mu.Lock()
				err := f.service.completeCurrentDungeonOrdinaryFinalRoomAfterDeathLocked(peers[0], r, before.Scene, key)
				peers[0].dungeon.mu.Unlock()
				if err != nil {
					t.Fatal(err)
				}
			} else {
				r195BossDeathGuardDispatch(t, f.service, peers[0], 117, key)
			}
			if !reflect.DeepEqual(before, r.Session.Snapshot()) || r.bossDieCheckResponseSent || r.settlementEntrySent || len(conns[0].bytes()) != 0 || len(f.state.Battle.ClearedRuns) != 0 {
				t.Fatal("completion entrance bypassed the shared mechanism")
			}
		})
	}
}

func TestR195FiendWarBossDeathGuardAllowsWindowAndExactReplay(t *testing.T) {
	f, peers, r, conns := r195BossDeathGuardFixture(t, false)
	b := f.state.Battle
	for name, value := range map[string]int{"top boss state": 9, "bot boss state": 9, "boss pattern state": 0, "top boss hp": 75} {
		b.FiendWar.Symbols[b.FiendWar.Config.Symbols[name]] = value
	}
	key := r.Room.Snapshot().Monsters[0].ObjectKey
	r195BossDeathGuardDispatch(t, f.service, peers[0], 39, key)
	if r.Session.Snapshot().Run.Status != worldmap.DungeonRunCompleted || !r.bossDieCheckAccepted || !r.settlementEntrySent ||
		len(b.ClearedRuns) != 1 || b.MemberClears[peers[0].selectedCharacterID] != 1 || b.FiendWar.Value("top boss hp") != 0 {
		t.Fatalf("valid body death: run=%v accepted=%t settlement=%t clears=%d member=%d hp=%d logs=%s", r.Session.Snapshot().Run.Status, r.bossDieCheckAccepted, r.settlementEntrySent, len(b.ClearedRuns), b.MemberClears[peers[0].selectedCharacterID], b.FiendWar.Value("top boss hp"), f.logText(t))
	}
	// A cleared recipient may already be returning to the battlefield, so it
	// is not an active in-dungeon projection target. Compare settlement packets
	// rather than unrelated asynchronous raid projection bytes.
	settlements := func() map[uint16]int {
		counts := map[uint16]int{}
		for wire := conns[0].bytes(); len(wire) > 0; {
			packet, rest := splitGameServerUpperPacket(t, wire)
			wire = rest
			if packet.Header.MsgID == 31 || packet.Header.MsgID == 115 {
				counts[packet.Header.MsgID]++
			}
		}
		return counts
	}
	before := settlements()
	if before[31] != 1 {
		t.Fatalf("valid death settlement packets=%v", before)
	}
	r195BossDeathGuardDispatch(t, f.service, peers[0], 117, key)
	if !reflect.DeepEqual(settlements(), before) || len(b.ClearedRuns) != 1 || b.MemberClears[peers[0].selectedCharacterID] != 1 {
		t.Fatal("exact completed-run replay duplicated settlement or clear count")
	}
}

func TestR195FiendWarBossDeathGuardPreservesNonBossAndOtherDungeon(t *testing.T) {
	t.Run("ordinary_add_in_fiendwar_boss_room", func(t *testing.T) {
		f, peers, r, _ := r195BossDeathGuardFixture(t, true)
		room := r.Room.Snapshot()
		if len(room.Monsters) != 2 {
			t.Fatal("expected one boss and one ordinary add")
		}
		if boss, _, why := currentDungeonRuntimeObjectBossActor(r, room.Monsters[1].ObjectKey, runtimeDungeonMonsterAnnounced); boss {
			t.Fatalf("ordinary add fixture is a boss: %s", why)
		}
		r195BossDeathGuardDispatch(t, f.service, peers[0], 39, room.Monsters[1].ObjectKey)
		after := r.Room.Snapshot()
		if after.Monsters[1].State != runtimeDungeonMonsterDefeated || after.Monsters[0].State != runtimeDungeonMonsterAnnounced ||
			r.Session.Snapshot().Run.Status != worldmap.DungeonRunActive || r.settlementEntrySent {
			t.Fatalf("ordinary add: states=%v/%v run=%v settled=%t logs=%s", after.Monsters[0].State, after.Monsters[1].State, r.Session.Snapshot().Run.Status, r.settlementEntrySent, f.logText(t))
		}
	})
	t.Run("ordinary_dungeon", func(t *testing.T) {
		s, r := prepareTutorialScopeRuntime(t, tutorialScopeFixtureOptions{disableTutorial: true, singleMonster: true})
		s.options.gameUpperHeader = gameUpperHeaderChannel13
		p := &gameSession{conn: &bufferConn{}, selectedCharacterID: 99, dungeon: dungeonSessionState{runtime: r}}
		if _, err := r.Room.AnnounceAllActors(r.Session); err != nil {
			t.Fatal(err)
		}
		key := r.Room.Snapshot().Monsters[0].ObjectKey
		r195BossDeathGuardDispatch(t, s, p, 39, key)
		r195BossDeathGuardDispatch(t, s, p, 117, key)
		if r.Session.Snapshot().Run.Status != worldmap.DungeonRunCompleted || !r.settlementEntrySent {
			t.Fatal("FiendWar guard changed ordinary dungeon completion")
		}
	})
}
