package dnfbridge

import (
	"bytes"
	"encoding/binary"
	"reflect"
	"testing"
)

func TestRaidParticipation584SeparatesLiveSquadsAndRunOwners(t *testing.T) {
	for _, tc := range []struct {
		name      string
		positions map[uint16]currentRaidParticipationPosition
		rows      [][]uint16
	}{
		{"two_squads", map[uint16]currentRaidParticipationPosition{99: {DungeonID: 210, GroupIndex: 1, RunID: "a"}, 100: {DungeonID: 210, GroupIndex: 1, RunID: "a"}, 101: {DungeonID: 210, GroupIndex: 2, RunID: "b"}, 102: {DungeonID: 210, GroupIndex: 2, RunID: "b"}}, [][]uint16{{99, 100}, {101, 102}}},
		{"same_live_squad_distinct_run", map[uint16]currentRaidParticipationPosition{99: {DungeonID: 210, GroupIndex: 1, RunID: "a"}, 100: {DungeonID: 210, GroupIndex: 1, RunID: "b"}}, [][]uint16{{99}, {100}}},
		{"unassigned_is_individual", map[uint16]currentRaidParticipationPosition{99: {DungeonID: 210, RunID: "a"}, 100: {DungeonID: 210, RunID: "a"}}, [][]uint16{{99}, {100}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			packets, _ := currentRaidParticipationProjection(nil, tc.positions, false, nil)
			if len(packets) != 2*len(tc.rows) {
				t.Fatal("expected remove then append for each native vector row", len(packets))
			}
			for i, p := range packets {
				op := byte(0)
				if i >= len(tc.rows) {
					op = 2
				}
				if p.opcode != 584 || !bytes.Equal(p.body, currentRaidParticipationBody(210, op, tc.rows[i%len(tc.rows)])) {
					t.Fatalf("row%d=%x", i, p.body)
				}
			}
			if p, _ := currentRaidParticipationProjection(tc.positions, tc.positions, false, nil); len(p) != 0 {
				t.Fatal("equal snapshot replayed")
			}
			fresh, _ := currentRaidParticipationProjection(tc.positions, tc.positions, true, nil)
			if len(fresh) != 3*len(tc.rows) {
				t.Fatal("fresh subscriber did not rebuild remove/remove/add", len(fresh))
			}
			for i, p := range fresh {
				op := byte(0)
				if i >= 2*len(tc.rows) {
					op = 2
				}
				if p.opcode != 584 || !bytes.Equal(p.body, currentRaidParticipationBody(210, op, tc.rows[i%len(tc.rows)])) {
					t.Fatalf("fresh row%d=%x", i, p.body)
				}
			}
			cleared := make(map[uint16]currentRaidParticipationPosition, len(tc.positions))
			for id, position := range tc.positions {
				position.Cleared = true
				cleared[id] = position
			}
			completed, _ := currentRaidParticipationProjection(cleared, cleared, true, nil)
			// Native op4 is a clear announcement, not stored row state.
			// Restore all squad rows silently without replaying past clears.
			if len(completed) != 3*len(tc.rows) {
				t.Fatal("cleared fresh subscriber must silently restore rows", len(completed))
			}
			for i, p := range completed {
				op := byte(0)
				if i >= 2*len(tc.rows) {
					op = 2
				}
				if p.opcode != 584 || !bytes.Equal(p.body, currentRaidParticipationBody(210, op, tc.rows[i%len(tc.rows)])) {
					t.Fatalf("cleared row%d=%x", i, p.body)
				}
			}
		})
	}
}

func TestRaidParticipationUsesLiveRosterNotRewardSquad(t *testing.T) {
	b := &runtimeRaidBattle{MemberAccounts: map[uint16]string{99: "a", 100: "b"}, MemberGroups: map[uint16]byte{99: 1, 100: 1}, Participation: map[uint16]currentRaidParticipation{99: {RunID: "r", DungeonID: 210}, 100: {RunID: "r", DungeonID: 210}}}
	positions, err := currentRaidParticipationPositions(b, []runtimeRaidMemberState{{CharID: 99, GroupIndex: 2}})
	if err != nil || len(positions) != 1 || positions[99].GroupIndex != 2 || positions[99].RunID != "r" {
		t.Fatal("live membership/run mapping", positions, err)
	}
	if !reflect.DeepEqual(b.MemberGroups, map[uint16]byte{99: 1, 100: 1}) || len(b.Participation) != 2 {
		t.Fatal("UI altered reward or participation owners")
	}
	packets, _ := currentRaidParticipationProjection(nil, positions, false, nil)
	if len(packets) != 2 || binary.LittleEndian.Uint16(packets[1].body[7:]) != 99 {
		t.Fatal("departed owner projected")
	}
	for _, members := range [][]runtimeRaidMemberState{{{CharID: 99, GroupIndex: 7}}, {{CharID: 99}, {CharID: 99}}} {
		if _, err := currentRaidParticipationPositions(b, members); err == nil {
			t.Fatal("invalid live roster accepted")
		}
	}
}
