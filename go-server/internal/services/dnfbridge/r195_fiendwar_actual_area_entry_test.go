package dnfbridge

import (
	"context"
	"fmt"
	"testing"

	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

// Unlike the earlier entry fixtures, go through the real op36 area handler.
// Do not manufacture a valid destination position with r134N1Presence or op35.
func TestR195FiendWarActualAreaMoveKeepsCommittedLanding(t *testing.T) {
	f, peers := r134N1Fixture(t)
	s, b := f.service, f.state.Battle
	b.FiendWar.Symbols[b.FiendWar.Config.Symbols["top boss state"]] = 1
	b.FiendWar.Symbols[b.FiendWar.Config.Symbols["bot boss state"]] = 1
	syncCurrentFiendWarDungeons(b)
	for i, p := range peers {
		origin := 28 + i
		r134N1Presence(t, f, p, origin)
		c, _, err := f.repos.Character.Load(context.Background(), fmt.Sprint(p.selectedCharacterID))
		if err != nil {
			t.Fatal(err)
		}
		c.Stats["area_state"] = 3
		if err := f.repos.Character.Save(context.Background(), c); err != nil {
			t.Fatal(err)
		}
		if err := f.repos.Quest.Save(context.Background(), dnfrepo.QuestRecord{CharacterID: c.CharacterID, States: map[int64]dnfrepo.QuestState{8641: {Status: "completed"}}}); err != nil {
			t.Fatal(err)
		}
		if err := s.startGameSessionEvents(context.Background(), p); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = s.stopGameSessionEvents(context.Background(), p) })
	}
	for i, p := range peers {
		target := 30 + i
		err := s.callGameSession(context.Background(), p, "actual_op36_entry_test", func() error {
			// These are fixture client coordinates, not a fabricated PVF landing.
			// The real move path decides whether to accept this request. Preserve
			// the exact coordinates written in its successful transition response.
			x, y := p.townPositionSnapshot.PositionX, p.townPositionSnapshot.PositionY
			if err := s.handleTownSetUserArea(p, buildTownMoveRequest(98, byte(target), int16(x), int16(y), 5)); err != nil {
				return err
			}
			p.townMu.Lock()
			position := p.townPositionSnapshot
			pending := p.fiendWarPendingMove
			p.townMu.Unlock()
			if pending == nil || pending.to != target || position.AreaID != byte(target) {
				return fmt.Errorf("area move did not commit target=%d position=%+v pending=%+v", target, position, pending)
			}
			d := newCurrentFiendWarAdmissionDiagnostic(p, uint32(b.FiendWar.Config.Areas[target].Dungeon))
			if err := s.tryCurrentFiendWarEncounterWithDiagnostic(p, &d); err != nil {
				return err
			}
			if p.dungeon.runtime == nil {
				return fmt.Errorf("committed op36 landing did not admit target=%d position=%+v reason=%s", target, position, d.reason)
			}
			if p.dungeon.runtime.Dungeon.ID != int64(b.FiendWar.Config.Areas[target].Dungeon) {
				return fmt.Errorf("wrong entry dungeon")
			}
			if len(p.dungeon.runtime.raidEntry.Participants) != 1 {
				return fmt.Errorf("op36 combined the two boss sides")
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	if peers[0].dungeon.runtime.raidEntry.RunID == peers[1].dungeon.runtime.raidEntry.RunID {
		t.Fatal("boss sides share a run")
	}
}
