package dnfbridge

import (
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// Native584 op4 is the 32049 clear announcement, not a persistent row flag.
func TestR158RaidClearNoticeIsAnOwnedEdge(t *testing.T) {
	position := func(run string, cleared bool) currentRaidParticipationPosition {
		return currentRaidParticipationPosition{DungeonID: 210, GroupIndex: 1, RunID: run, Cleared: cleared}
	}
	old := map[uint16]currentRaidParticipationPosition{19: position("a", true)}
	for _, tc := range []struct {
		name         string
		before, next map[uint16]currentRaidParticipationPosition
		fresh        bool
		notices      int
	}{
		{"first_clear", map[uint16]currentRaidParticipationPosition{19: position("a", false)}, old, false, 1},
		{"other_map_enters", old, map[uint16]currentRaidParticipationPosition{19: position("a", true), 20: {DungeonID: 213, GroupIndex: 2, RunID: "b"}}, false, 0},
		{"other_map_leaves", map[uint16]currentRaidParticipationPosition{19: position("a", true), 20: {DungeonID: 213, GroupIndex: 2, RunID: "b"}}, old, false, 0},
		{"same_run_regroup", old, map[uint16]currentRaidParticipationPosition{19: {DungeonID: 210, GroupIndex: 2, RunID: "a", Cleared: true}}, false, 0},
		{"same_dungeon_new_run", old, map[uint16]currentRaidParticipationPosition{19: position("new", true)}, false, 1},
		{"retained_resubscribe", old, old, true, 0},
		{"replacement_snapshot", nil, old, true, 0},
		{"late_member_same_clear", old, map[uint16]currentRaidParticipationPosition{19: position("a", true), 20: position("a", true)}, false, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			packets := currentRaidParticipationProjection(tc.before, tc.next, tc.fresh)
			count := 0
			for _, p := range packets {
				if p.opcode == 584 && p.body[5] == 4 {
					count++
					if binary.LittleEndian.Uint32(p.body[1:5]) != 210 {
						t.Fatal("foreign map announcement")
					}
				}
			}
			if count != tc.notices {
				t.Fatalf("clear announcements=%d want=%d", count, tc.notices)
			}
			if len(packets) == 0 {
				t.Fatal("suppressed occupancy rebuild together with notification")
			}
		})
	}
}

// Authenticated production sender with the real PVF projection. The accepted
// run/participation ledger is controlled; no reward or clear flag is forged by
// the production code. An unrelated entry must not re-announce its old peer.
func TestR158RealPVFUnrelatedEntryDoesNotReplayOtherMapClear(t *testing.T) {
	f, session, _ := realAntonProjectionFixture(t, 0)
	b := f.state.Battle
	f.state.Members = []runtimeRaidMemberState{{CharID: 19, GroupIndex: 1}, {CharID: 20, GroupIndex: 2}}
	b.MemberAccounts[20] = "account-20"
	b.Participation = map[uint16]currentRaidParticipation{19: {RunID: "clear-a", DungeonID: 210, Phase: b.Phase, Attempt: b.Anton.Epoch()}}
	type nativeFrame struct {
		Stage string `json:"stage"`
		Body  string `json:"body_hex"`
	}
	var frames []nativeFrame
	stage := "initial_entry"
	collect := func() int {
		t.Helper()
		f.conn.write.Reset()
		if err := f.service.sendCurrentRaidProgressProjection(session, cloneRuntimeRaidState(*f.state), false, false); err != nil {
			t.Fatal(err)
		}
		stream := f.conn.write.Bytes()
		count := 0
		for len(stream) > 0 {
			p, rest := splitGameServerUpperPacket(t, stream)
			stream = rest
			if p.Header.MsgID == 584 {
				frames = append(frames, nativeFrame{stage, hex.EncodeToString(p.Body)})
				if p.Body[5] == 4 {
					count++
				}
			}
		}
		return count
	}
	if n := collect(); n != 0 {
		t.Fatal("entry announced clear", n)
	}
	if b.ClearedRuns == nil {
		b.ClearedRuns = map[string]currentRaidClearedRun{}
	}
	b.ClearedRuns["clear-a"] = currentRaidClearedRun{DungeonID: 210, Phase: b.Phase, Attempt: b.Anton.Epoch(), Participants: []uint16{19}}
	b.ProgressVersion++
	stage = "first_clear"
	if n := collect(); n != 1 {
		t.Fatal("first accepted clear announcement missing", n)
	}
	b.Participation[20] = currentRaidParticipation{RunID: "entry-b", DungeonID: 213, Phase: b.Phase, Attempt: b.Anton.Epoch()}
	b.ProgressVersion++
	stage = "unrelated_entry"
	if n := collect(); n != 0 {
		t.Fatalf("unrelated dungeon213 entry replayed dungeon210 clear %d times", n)
	}
	if dir := os.Getenv("DNF90_R158_NOTICE_NATIVE_DIR"); dir != "" {
		data, err := json.MarshalIndent(frames, "", "  ")
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "actual-sender-584.json"), data, 0600); err != nil {
			t.Fatal(err)
		}
	}
}
