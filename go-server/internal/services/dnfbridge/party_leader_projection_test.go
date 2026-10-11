package dnfbridge

import (
	"testing"

	"longheng.io/server/internal/modules/dnf/alignedcmd"
)

// 当前客户端没有独立的队长字段：它把队伍表的第一行当作队长（slot0）。
// 所以任何投影都必须把权威队长排在第一位，即使进来的 state.Members 还是
// 移交前的旧顺序（旧队长排在前面）。
func TestRuntimePartyMembersPutsAuthoritativeLeaderFirst(t *testing.T) {
	state := alignedcmd.PartyState{
		PartyID:    7,
		UserID:     303,
		IsLeader:   true,
		MaxMembers: 4,
		Members: []alignedcmd.PartyMemberState{
			{UserID: 101, UserState: 1},
			{UserID: 202, UserState: 1},
			{UserID: 303, UserState: 1},
		},
	}
	members := runtimePartyMembers(state)
	if len(members) != 3 {
		t.Fatalf("members = %+v", members)
	}
	if members[0].UserID != 303 {
		t.Fatalf("first row = %d, want the authoritative leader 303 (%+v)", members[0].UserID, members)
	}
	// 其余成员保持相对顺序，不能被丢掉或复制。
	if members[1].UserID != 101 || members[2].UserID != 202 {
		t.Fatalf("tail order = %+v, want 101 then 202", members)
	}
	// slot 查询同样按队长在前的投影解析。
	if member, ok := runtimePartyMemberBySlot(state, 0); !ok || member.UserID != 303 {
		t.Fatalf("slot 0 = %+v ok=%v, want leader 303", member, ok)
	}
	if member, ok := runtimePartyMemberBySlot(state, 2); !ok || member.UserID != 202 {
		t.Fatalf("slot 2 = %+v ok=%v, want 202", member, ok)
	}
	if _, ok := runtimePartyMemberBySlot(state, 3); ok {
		t.Fatal("empty slot must not resolve")
	}
}
