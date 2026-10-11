package dnfbridge

import (
	"reflect"
	"testing"
)

func TestCurrentRaidMemberPhaseClearProjectionContracts(t *testing.T) {
	state := runtimeRaidState{Started: true, Members: []runtimeRaidMemberState{{CharID: 19}, {CharID: 20}}, Battle: &runtimeRaidBattle{
		InstanceID: "projection-unit-instance", Single: true, Phase: 0,
		MemberAccounts: map[uint16]string{19: "owner-19", 20: "owner-20"},
		MemberClears:   map[uint16]uint32{19: 5, 20: 1},
		ClearedRuns: map[string]currentRaidClearedRun{
			"accepted-one": {Phase: 0, Attempt: 1, Participants: []uint16{19, 20}},
			"accepted-two": {Phase: 0, Attempt: 1, Participants: []uint16{19}},
			"old-phase":    {Phase: 1, Attempt: 1, Participants: []uint16{19}},
			"old-attempt":  {Phase: 0, Attempt: 2, Participants: []uint16{19}},
			"foreign":      {Phase: 0, Attempt: 1, Participants: []uint16{21}},
		},
	}}
	state = cloneRuntimeRaidState(state)
	before := cloneRuntimeRaidState(state)
	members := currentRuntimeRaidMembers(state)
	if members[0].Core.Field56 != 5 || members[1].Core.Field56 != 1 {
		t.Fatalf("accepted owner counts missing: %#v", members)
	}
	metadata := currentRuntimeRaidMetadata(state)
	if !reflect.DeepEqual(metadata.Members, members) || !reflect.DeepEqual(state, before) {
		t.Fatal("metadata diverged or projection mutated authoritative journals")
	}
	if runtimeRaidMemberRefresh(state).Members[0].Field56 != 0 {
		t.Fatal("current-client projection changed legacy member contract")
	}
	for _, name := range []string{"not-started", "no-battle", "no-instance", "not-member", "no-account", "no-registered", "invalid-phase"} {
		t.Run(name, func(t *testing.T) {
			v := cloneRuntimeRaidState(state)
			switch name {
			case "not-started":
				v.Started = false
			case "no-battle":
				v.Battle = nil
			case "no-instance":
				v.Battle.InstanceID = ""
			case "not-member":
				v.Members = nil
			case "no-account":
				delete(v.Battle.MemberAccounts, 19)
			case "no-registered":
				delete(v.Battle.MemberClears, 19)
			case "invalid-phase":
				v.Battle.Phase = 2
			}
			if currentRaidMemberPhaseClearCount(v, 19) != 0 {
				t.Fatal("invalid or unowned current member received a clear count")
			}
		})
	}
	t.Run("byte-saturation-preserves-uint32", func(t *testing.T) {
		v := cloneRuntimeRaidState(state)
		v.Battle.MemberClears[19] = 300
		if currentRaidMemberPhaseClearCount(v, 19) != 255 || v.Battle.MemberClears[19] != 300 || !reflect.DeepEqual(v.Battle.ClearedRuns, state.Battle.ClearedRuns) {
			t.Fatal("wire count wrapped or modified internal journal")
		}
	})
}
