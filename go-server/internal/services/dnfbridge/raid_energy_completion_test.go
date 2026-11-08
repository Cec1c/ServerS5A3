package dnfbridge

import (
	"longheng.io/server/internal/modules/dnf/worldmap"
	"testing"
)

func TestRealPVFEnergyCompletionReducesBarrierWithoutClientSymbol(t *testing.T) {
	for _, invalid := range []string{"", "unnotified", "unsettled", "foreign-run"} {
		t.Run(invalid, func(t *testing.T) {
			s, session, r, b, at := actualEnergySymbolFixture(t, true)
			for _, monster := range r.Room.Snapshot().Monsters {
				if monster.Definition.ID != 69311 {
					continue
				}
				if _, _, err := r.Room.CommitActorDeathReport(monster.ObjectKey, r.Session); err != nil {
					t.Fatal(err)
				}
				if invalid != "unnotified" {
					response, err := buildCurrentDungeonDeathNotificationBodyWithDrops(monster.ObjectKey, currentDungeonDeathResponseMonster, nil)
					if err != nil {
						t.Fatal(err)
					}
					r.Room.RecordDeathResponseBody(monster.ObjectKey, response)
				}
			}
			if r.Session.Snapshot().Run.Status == worldmap.DungeonRunActive {
				if err := r.Session.CompleteCurrentRoom(); err != nil {
					t.Fatal(err)
				}
			}
			r.bossDieCheckAccepted, r.bossDieCheckResponseSent, r.settlementEntrySent = true, true, true
			if invalid == "unsettled" {
				r.settlementEntrySent = false
			}
			if invalid == "foreign-run" {
				r.raidEntry.InstanceID = "other"
			}
			before := b.Anton.NativeSymbols()[110]
			_, changed, err := s.reduceCurrentRaidDungeonClearLocked(session, r, at)
			if invalid != "" {
				if err == nil || b.Anton.NativeSymbols()[110] != before || len(b.ClearedRuns) != 0 {
					t.Fatalf("invalid completion changed barrier: changed=%t err=%v", changed, err)
				}
				return
			}
			if err != nil || !changed || b.Anton.NativeSymbols()[110] != before-3000 || b.MemberClears[99] != 1 {
				t.Fatalf("verified clear without client symbol: changed=%t err=%v barrier=%d clears=%d", changed, err, b.Anton.NativeSymbols()[110], b.MemberClears[99])
			}
			for i := 0; i < 4; i++ {
				if _, changed, err := s.reduceCurrentRaidDungeonClearLocked(session, r, at); err != nil || changed {
					t.Fatalf("clear replay: %t %v", changed, err)
				}
				if _, changed, err := s.recordCurrentAntonEnergySymbolLocked(session, energySymbolBody(3000), at); err != nil || changed {
					t.Fatalf("late device replay: %t %v", changed, err)
				}
			}
			if b.Anton.NativeSymbols()[110] != before-3000 || b.MemberClears[99] != 1 {
				t.Fatal("replay subtracted again")
			}
		})
	}
}
