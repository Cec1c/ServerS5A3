package dnfbridge

import (
	"testing"
	"time"

	"longheng.io/server/internal/modules/dnf/raid/fiendwar"
)

// A timeout is authoritative when the raid clock observes it. The retry/refund
// entitlement must be frozen in that same pass so a party that immediately
// disbands cannot delete the battle and lose the already-earned ticket return.
func TestFiendWarTimeoutFreezesRefundBeforeRosterDisbands(t *testing.T) {
	now := time.Date(2026, 10, 19, 12, 0, 0, 0, time.UTC)
	state := &runtimeRaidState{
		RaidKey: 42<<16 | 19,
		Started: true,
		Members: []runtimeRaidMemberState{{CharID: 19}, {CharID: 20}},
		Battle: &runtimeRaidBattle{
			InstanceID: "00112233445566778899aabbccddeeff",
			Mode:       3,
			EnterAfter: now.Add(-time.Hour),
			Deadline:   now.Add(-time.Second),
			FiendWar:   &fiendwar.FiendWarBattle{},
			FiendWarConfig: &fiendwar.FiendWarConfig{
				Retry:     true,
				RetryItem: fiendwar.AntonEntryCost{ItemID: 490007844, Count: 1},
			},
			MemberAccounts: map[uint16]string{19: "account-19", 20: "account-20"},
			MemberGroups:   map[uint16]byte{19: 1, 20: 2},
		},
	}
	s := &Service{options: options{currentClientVersion: "official-24.9"}, raids: map[uint32]*runtimeRaidState{state.RaidKey: state}}
	if states, errs := s.advanceCurrentRaidClocks(now); len(states) != 1 || len(errs) != 0 {
		t.Fatalf("timeout transition states=%d errors=%v", len(states), errs)
	}
	if !state.Battle.TimedOut {
		t.Fatal("deadline did not mark the FiendWar attempt failed")
	}
	key := state.Battle.InstanceID + ":retry"
	if s.antonRewardFlows[key] == nil {
		t.Fatal("timeout did not freeze the retry/refund entitlement before the next clock pass")
	}
	s.removeRuntimeRaidMember(19)
	s.removeRuntimeRaidMember(20)
	if s.raids[state.RaidKey] != nil {
		t.Fatal("fixture did not disband the roster")
	}
	if s.antonRewardFlows[key] == nil {
		t.Fatal("disband discarded the frozen refund entitlement")
	}
}
