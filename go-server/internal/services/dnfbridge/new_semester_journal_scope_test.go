package dnfbridge

import (
	"context"
	"testing"
)

func TestNewSemesterActualFrozenReceiptRejectsMutation(t *testing.T) {
	for _, kind := range []string{"version", "nonce", "dungeon", "map", "boss", "campaign"} {
		t.Run(kind, func(t *testing.T) {
			s, a, r, _ := prepareSemesterActualCompleted(t)
			r.clearMapCompletionPhaseAPersisted = false
			switch kind {
			case "version":
				r.semesterRun.receipt.Version = 2
			case "nonce":
				r.semesterRun.nonce = "bad"
			case "dungeon":
				r.semesterRun.receipt.DungeonID = 6063
			case "map":
				r.semesterRun.receipt.MapID = 80486
			case "boss":
				r.semesterRun.receipt.BossID = 107005197
			case "campaign":
				r.semesterRun.campaign.Version = 0
			}
			if e := s.persistCurrentDungeonClearMapCompletionPhaseA(context.Background(), a, r, r.Session.Snapshot()); e == nil || r.clearMapCompletionPhaseAPersisted {
				t.Fatal("mutated receipt accepted")
			}
		})
	}
}
func TestNewSemesterActualInvalidDayBitmapRejectsAllWrites(t *testing.T) {
	for _, bitmap := range []int64{-1, 1 << 28} {
		t.Run(string(rune('a'+(bitmap&1))), func(t *testing.T) {
			s, a, c := prepareSemesterActualReady(t)
			r := completeSemesterActualRun(t, s, a, true)
			g, _ := s.repositoryGroup()
			ch, _, _ := g.Character.Load(context.Background(), "99")
			ch.Stats[semesterDayKey(c)+":day_bitmap"] = bitmap
			if e := g.Character.Save(context.Background(), ch); e != nil {
				t.Fatal(e)
			}
			if e := s.handleDungeonMonsterDeath(a, semesterActualBossBody(t, r, 99)); e != nil {
				t.Fatal(e)
			}
			if r.clearMapCompletionPhaseAPersisted {
				t.Fatal("invalid day bitmap accepted")
			}
			after, _, _ := g.Character.Load(context.Background(), "99")
			if after.Stats[semesterDayKey(c)+":day_bitmap"] != bitmap || after.Stats[semesterQualifiedKey(c)] != 0 {
				t.Fatal("invalid bitmap mutated state")
			}
			if _, found, e := g.Settings.Load(context.Background(), semesterReceiptScope(r.semesterRun.nonce)); e != nil || found {
				t.Fatal("invalid bitmap committed receipt")
			}
		})
	}
}
func TestNewSemesterActualPeerBindingChangedBeforeFreezeExcluded(t *testing.T) {
	for _, kind := range []string{"generation", "account"} {
		t.Run(kind, func(t *testing.T) {
			s, sessions, runs, c := prepareSemesterActualPartyCompleted(t, func(s *Service, sessions []*gameSession, r []*runtimeDungeonState) {
				s.mu.Lock()
				defer s.mu.Unlock()
				if kind == "generation" {
					sessions[3].characterGeneration++
				} else {
					sessions[3].accountID = "foreign"
				}
			})
			if e := s.persistCurrentDungeonClearMapCompletionPhaseA(context.Background(), sessions[0], runs[0], runs[0].Session.Snapshot()); e != nil {
				t.Fatal("leader deferred clear retry", e)
			}
			g, _ := s.repositoryGroup()
			ch, _, _ := g.Character.Load(context.Background(), "102")
			if ch.Stats[semesterQualifiedKey(c)] != 0 {
				t.Fatal("changed peer gained qualification")
			}
			if runs[0].semesterRun.receipt == nil || len(runs[0].semesterRun.receipt.Owners) != 3 {
				t.Fatal("changed peer remained frozen owner")
			}
		})
	}
}
