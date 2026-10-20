package dnfbridge

import (
	"context"
	"longheng.io/server/internal/modules/dnf/dungeoncmd"
	"longheng.io/server/internal/modules/dnf/worldmap"
	"testing"
	"time"
)

func preparedActualAntonHatcheryTransfer(t *testing.T) (*Service, *gameSession, *runtimeDungeonState, *runtimeDungeonState) {
	t.Helper()
	s, session, old, target := actualAntonHatchWarpOwner(t)
	r, _, err := s.prepareDungeonRuntime(context.Background(), session, dungeoncmd.SelectDungeonRequest{DungeonID: target, Difficulty: 2})
	if err != nil {
		t.Fatal(err)
	}
	if err = s.freezeCurrentRaidDungeonGroupEntry(session, r, nil); err != nil {
		t.Fatal(err)
	}
	if err = freezeCurrentDungeonTownReturnOrigin(session, r); err != nil {
		t.Fatal(err)
	}
	if session.dungeon.runtime != old {
		t.Fatal("prepare retired old energy run")
	}
	return s, session, old, r
}

func TestReviewActualAntonHatcheryTransferPreparedOwnerControls(t *testing.T) {
	cases := []struct {
		name   string
		change func(*Service, *gameSession, *runtimeDungeonState, *runtimeDungeonState)
	}{
		{"generation", func(s *Service, p *gameSession, o, n *runtimeDungeonState) { p.characterGeneration++ }},
		{"lifecycle", func(s *Service, p *gameSession, o, n *runtimeDungeonState) { p.dungeon.runToken++ }},
		{"placement-not-committed", func(s *Service, p *gameSession, o, n *runtimeDungeonState) { p.postStartMapPlayerStateSent = false }},
		{"lost-auth", func(s *Service, p *gameSession, o, n *runtimeDungeonState) { p.accountFromToken = false }},
		{"canonical", func(s *Service, p *gameSession, o, n *runtimeDungeonState) {
			s.gameSessions[p.selectedCharacterID] = &gameSession{}
		}},
		{"phase", func(s *Service, p *gameSession, o, n *runtimeDungeonState) {
			s.raids[o.raidEntry.RaidKey].Battle.Phase = 0
		}},
		{"deadline", func(s *Service, p *gameSession, o, n *runtimeDungeonState) {
			s.raids[o.raidEntry.RaidKey].Battle.Deadline = time.Now().Add(-time.Second)
		}},
		{"closed-target", func(s *Service, p *gameSession, o, n *runtimeDungeonState) {
			b := s.raids[o.raidEntry.RaidKey].Battle
			for i := range b.Dungeons {
				if int64(b.Dungeons[i].ID) == n.Dungeon.ID {
					b.Dungeons[i].State = 1
				}
			}
		}},
		{"dead", func(s *Service, p *gameSession, o, n *runtimeDungeonState) { o.deathStateSent = true }},
		{"foreign-target-owner", func(s *Service, p *gameSession, o, n *runtimeDungeonState) { n.AccountID = "foreign" }},
		{"cost-contract", func(s *Service, p *gameSession, o, n *runtimeDungeonState) { n.Dungeon.Metadata.NoFatigue = false }},
		{"request-difficulty", func(s *Service, p *gameSession, o, n *runtimeDungeonState) { n.Request.Difficulty = 3 }},
		{"replacement", func(s *Service, p *gameSession, o, n *runtimeDungeonState) {
			replacement := *o
			p.dungeon.runtime = &replacement
		}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			s, p, o, n := preparedActualAntonHatcheryTransfer(t)
			c.change(s, p, o, n)
			before := p.dungeon.runtime
			token := p.dungeon.runToken
			if err := s.commitCurrentAntonHatcheryTransferGroup(p, n, nil); err == nil {
				t.Fatal("invalid transfer accepted")
			}
			if p.dungeon.runtime != before || p.dungeon.runToken != token {
				t.Fatal("failed transfer changed old runtime owner")
			}
		})
	}
}

func TestReviewActualAntonHatcheryTransferCommitKeepsReturnOrigin(t *testing.T) {
	s, p, o, n := preparedActualAntonHatcheryTransfer(t)
	origin := o.townReturnOrigin
	if err := s.commitCurrentAntonHatcheryTransferGroup(p, n, nil); err != nil {
		t.Fatal(err)
	}
	if p.dungeon.runtime != n || n.townReturnOrigin != origin || n.raidEntry.RunID == o.raidEntry.RunID || n.lifecycleToken != p.dungeon.runToken {
		t.Fatal("new transfer lost immutable origin/run identity")
	}
	if o.Session.Snapshot().Run.Status != worldmap.DungeonRunActive {
		t.Fatal("transfer invented ordinary give-up or clear")
	}
}

func TestReviewActualAntonHatcheryTransferWrongCornerRejects(t *testing.T) {
	s, p, o, target := actualAntonHatchWarpOwner(t)
	wrong := uint32(221)
	if wrong == target {
		wrong = 222
	}
	_, _, err := s.prepareDungeonRuntime(context.Background(), p, dungeoncmd.SelectDungeonRequest{DungeonID: wrong, Difficulty: 2})
	if err == nil || p.dungeon.runtime != o {
		t.Fatal("unrelated open gate admitted from wrong corner")
	}
}
