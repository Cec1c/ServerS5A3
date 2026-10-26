package dnfbridge

import (
	"testing"

	"longheng.io/server/internal/modules/dnf/raid/fiendwar"
)

// A FiendWar raid start must not turn its single-member attack groups into
// ordinary parties. The first party is owned by same-source-map encounter
// admission, after preparation of that particular combat entry.
func TestR195FiendWarStartDoesNotCreateSingletonOrdinaryParties(t *testing.T) {
	service := &Service{}
	state := runtimeRaidState{
		RaidKey:  0x01020001,
		LeaderID: 19,
		Members: []runtimeRaidMemberState{
			{CharID: 19, GroupIndex: 1, SlotOrder: 0},
			{CharID: 20, GroupIndex: 2, SlotOrder: 0},
		},
		Started: true,
		Battle:  &runtimeRaidBattle{FiendWar: &fiendwar.FiendWarBattle{}},
	}

	if err := service.applyRuntimeRaidParties(state); err != nil {
		t.Fatalf("FiendWar raid start returned an error: %v", err)
	}
	if snapshots := service.runtimePartyManagerForService().Snapshots(); len(snapshots) != 0 {
		t.Fatalf("FiendWar raid start created ordinary party snapshots: %+v", snapshots)
	}
}
