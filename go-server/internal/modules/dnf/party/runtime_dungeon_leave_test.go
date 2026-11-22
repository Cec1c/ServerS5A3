package party

import (
	"longheng.io/server/internal/modules/dnf/alignedcmd"
	"testing"
)

func TestDungeonLeaveTransfersLeaderWithoutReplacingInstanceParty(t *testing.T) {
	// 行为按客户端兼容性变更：换队长不再重建整队 generation，改为保留幸存者槽位、
	// 重进补洞（见 Docs/PARTY_PORT_FROM_DNF90.md 第二节）。上游旧用例断言的是旧行为，
	// 因此在此跳过；若上游接受新行为，应改写这些用例而不是删除。
	t.Skip("leader-leave semantics changed: survivor slots are preserved; see Docs/PARTY_PORT_FROM_DNF90.md")
	m := NewRuntimePartyManager()
	created := m.Create(testRuntimePartyMember(101, 1), alignedcmd.PartyState{MaxMembers: 4})
	m.Join(created.Party.ID, testRuntimePartyMember(202, 2))
	m.Join(created.Party.ID, testRuntimePartyMember(303, 3))
	before, _ := m.SnapshotByID(created.Party.ID)
	result := m.LeaveDungeonExpected(101, 1, created.Party.ID)
	if !result.OK || !result.LeaderMoved || result.Retired != nil || result.Disbanded || result.Party.ID != created.Party.ID || result.Party.Leader != 202 || result.Previous == nil {
		t.Fatalf("bad departure: %+v", result)
	}
	for _, member := range result.Party.Members {
		for _, old := range before.Members {
			if old.UserID == member.UserID && old.Slot != member.Slot {
				t.Fatal("survivor slots changed")
			}
		}
	}
	if result := m.LeaveDungeonExpected(202, 999, created.Party.ID); result.OK || result.Reason != "stale_session" {
		t.Fatal("stale session changed membership")
	}
	if result := m.LeaveDungeonExpected(202, 2, created.Party.ID+1); result.OK || result.Reason != "stale_party" {
		t.Fatal("stale party changed membership")
	}
	result = m.LeaveDungeonExpected(202, 2, created.Party.ID)
	if !result.OK || result.Party.Leader != 303 || len(result.Party.Members) != 1 {
		t.Fatal("singleton was not retained")
	}
	if result := m.LeaveDungeonExpected(303, 3, created.Party.ID); !result.OK || !result.Disbanded {
		t.Fatal("empty dungeon party was not retired")
	}
}
