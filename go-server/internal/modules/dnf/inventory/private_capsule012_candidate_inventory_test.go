package inventory

import (
	"context"
	"fmt"
	"reflect"
	"sync"
	"sync/atomic"
	"testing"
)

func TestPrivateCapsule012CandidateAtomicRollback(t *testing.T) {
	for phase := 0; phase < 4; phase++ {
		t.Run(fmt.Sprint(phase), func(t *testing.T) {
			g, tables, q, req, now := levelTicketFixture(t)
			req.Definition.TargetLevel = 90
			before := levelTicketSnapshot(t, g)
			g.CharacterSettlement = levelTicketFailUOW{g.CharacterSettlement, phase}
			if _, e := ApplyLevelUpTicket(context.Background(), g, tables, q, req, now); e == nil {
				t.Fatal("write failure accepted")
			}
			if !reflect.DeepEqual(before, levelTicketSnapshot(t, g)) {
				t.Fatal("target capsule partially committed")
			}
		})
	}
}
func TestPrivateCapsule012CandidateInvalidTargetAndLateBinding(t *testing.T) {
	for _, kind := range []string{"negative", "over_cap", "not_above_maximum", "random", "minimum_zero", "late_binding"} {
		t.Run(kind, func(t *testing.T) {
			g, tables, q, req, now := levelTicketFixture(t)
			req.Definition.TargetLevel = 90
			switch kind {
			case "negative":
				req.Definition.TargetLevel = -1
			case "over_cap":
				req.Definition.TargetLevel = 91
			case "not_above_maximum":
				req.Definition.TargetLevel = 89
			case "random":
				req.Definition.Outcomes = []LevelUpTicketOutcome{{Levels: 1, Weight: 1}}
				req.Roll = func(int64) (int64, error) { return 0, nil }
			case "minimum_zero":
				req.Definition.MinimumLevel = 0
			case "late_binding":
				calls := 0
				req.Revalidate = func() bool { calls++; return calls == 1 }
			}
			before := levelTicketSnapshot(t, g)
			if _, e := ApplyLevelUpTicket(context.Background(), g, tables, q, req, now); e == nil {
				t.Fatal("invalid target accepted")
			}
			if !reflect.DeepEqual(before, levelTicketSnapshot(t, g)) {
				t.Fatal("invalid target changed assets")
			}
		})
	}
}
func TestPrivateCapsule012CandidateConcurrentUse(t *testing.T) {
	g, tables, q, req, now := levelTicketFixture(t)
	req.Definition.TargetLevel = 90
	var successes atomic.Int32
	var wg sync.WaitGroup
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, e := ApplyLevelUpTicket(context.Background(), g, tables, q, req, now); e == nil {
				successes.Add(1)
			}
		}()
	}
	wg.Wait()
	c, _, _ := g.Character.Load(context.Background(), "2")
	bag, _, _ := g.Inventory.Load(context.Background(), "2")
	if successes.Load() != 1 || c.Level != 90 || bag.Slots["0:4"].Count != 1 {
		t.Fatal("concurrent duplicate debit or level mismatch", successes.Load(), c.Level, bag)
	}
}
