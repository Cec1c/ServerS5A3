package dnfbridge

import (
	"bytes"
	"context"
	"longheng.io/server/internal/modules/dnf/raid/fiendwar"
	"testing"
)

func TestFiendWarAliveMemberClearBeforeLocalSettlement(t *testing.T) {
	for _, scenario := range []string{"late_death", "dead_before_return", "return", "actual_retreat"} {
		t.Run(scenario, func(t *testing.T) {
			s, p, r, b := fiendWarCompletedLifeGuardFixture(t)
			_, _, active := newBackToVillageRuntime(t)
			r.Session = active.Session
			r.deathStateSent, r.deathReturnWaiting = false, false
			r.bossDieCheckAccepted, r.bossDieCheckResponseSent, r.settlementEntrySent = false, false, false
			e := r.raidEntry
			e.Participants = []uint16{100, 99}
			clear := b.ClearedRuns[e.RunID]
			clear.Participants = e.Participants
			b.ClearedRuns[e.RunID] = clear
			b.MemberLives = nil
			b.PhaseDeaths[0] = 0
			b.Participation = map[uint16]currentRaidParticipation{99: {RunID: e.RunID, Session: p, Runtime: r}}
			b.FiendWar = &fiendwar.FiendWarBattle{Config: &fiendwar.FiendWarBattleConfig{WaitingArea: 2, DieImprisonSeconds: []int{20}}, MemberAreas: map[uint16]int{99: 9}}
			switch scenario {
			case "late_death":
				if err := s.handleDungeonDieCharacter(p, make([]byte, 4)); err != nil {
					t.Fatal(err)
				}
				frames := splitUpperFrames(t, p.conn.(*bufferConn).write.Bytes())
				if len(frames) != 1 || frames[0].msgID != 32 || !bytes.Equal(frames[0].body, []byte{99, 0, 1, 0}) || r.deathStateSent || r.deathReturnWaiting || b.PhaseDeaths[0] != 0 {
					t.Fatal("settled live member left dead", frames)
				}
			case "dead_before_return":
				r.deathStateSent = true
				rows := []currentSceneTransitionRow{{ObjectOrResourceKey: 99}}
				body, err := buildCurrentSceneTransitionBody(98, 9, rows)
				if err != nil {
					t.Fatal(err)
				}
				if err = s.sendCurrentDungeonReturnToTownLocked(p, r, currentDungeonTownTransition{TownID: 98, AreaID: 9, ActorObjectKey: 99, Body: body}, 0, "cleared_member"); err != nil {
					t.Fatal(err)
				}
				frames := splitUpperFrames(t, p.conn.(*bufferConn).write.Bytes())
				if len(frames) < 2 || frames[0].msgID != 32 || !bytes.Equal(frames[0].body, []byte{99, 0, 1, 0}) || frames[1].msgID != 24 || r.deathStateSent {
					t.Fatal("town transition preceded life restoration", frames)
				}
			default:
				if scenario == "actual_retreat" {
					delete(b.ClearedRuns, e.RunID)
				}
				r.townReturnTransition.AreaID = 9
				if err := s.commitCurrentFiendWarReturnLocked(p, r); err != nil {
					t.Fatal(err)
				}
				penalized := b.FiendWar.ImprisonCounts[99] > 0
				if penalized != (scenario == "actual_retreat") || !penalized && b.FiendWar.MemberAreas[99] != 9 {
					t.Fatal("wrong return location or penalty", b.FiendWar.MemberAreas, b.FiendWar.ImprisonCounts)
				}
			}
		})
	}
}

func TestFiendWarAutomaticEntryConnectionFailureThenRetry(t *testing.T) {
	f, peers := r134N1Fixture(t)
	t.Setenv("DNFBRIDGE_PARTY_NATIVE_ADMISSION", "")
	s, b := f.service, f.state.Battle
	b.MemberClears = map[uint16]uint32{peers[1].selectedCharacterID: 1}
	if err := s.preloadSkillCatalog(context.Background()); err != nil {
		t.Fatal(err)
	}
	area := 0
	for id, a := range b.FiendWar.Config.Areas {
		if a.Dungeon > 0 && b.FiendWar.AreaOpen(id) && a.MemberMax >= 2 && (area == 0 || id < area) {
			area = id
		}
	}
	if area == 0 {
		t.Fatal("missing real PVF encounter")
	}
	for _, p := range peers {
		r134N1Presence(t, f, p, area)
		if err := s.startGameSessionEvents(context.Background(), p); err != nil {
			t.Fatal(err)
		}
		defer s.stopGameSessionEvents(context.Background(), p)
	}
	for _, mode := range []string{"refused", "timeout", "ready"} {
		if err := s.callGameSession(context.Background(), peers[0], "test-automatic-encounter", func() error { return s.tryCurrentFiendWarEncounter(peers[0]) }); err != nil {
			t.Fatal(err)
		}
		s.partyNativeAdmission.mu.Lock()
		proof := s.partyNativeAdmission.bySession[peers[0]]
		s.partyNativeAdmission.mu.Unlock()
		if proof == nil {
			t.Fatal(mode, "automatic encounter skipped connection check")
		}
		if mode != "timeout" {
			for i, p := range peers {
				status := byte(0)
				if mode == "refused" && i == 1 {
					status = 1
				}
				if err := s.handlePartyNativeConnectionReply(p, nativeAdmissionReply(proof, p, status)); err != nil {
					t.Fatal(err)
				}
			}
		}
		waitNativePartyAdmissionForTest(t, s, proof)
		for _, p := range peers {
			if (p.dungeon.runtime != nil) != (mode == "ready") {
				t.Fatal(mode, "unexpected runtime", p.selectedCharacterID, f.logText(t))
			}
			if b.FiendWar.ImprisonCounts[p.selectedCharacterID] != 0 || !b.FiendWar.RecoveryUntil[p.selectedCharacterID].IsZero() {
				t.Fatal(mode, "spurious retreat")
			}
		}
		if b.FiendWarAdmissionClaim.active() || mode != "ready" && (len(b.FiendWarEncounters) != 0 || b.FiendWar.Areas[area].Group != 0) {
			t.Fatal(mode, "reservation leaked")
		}
	}
}
