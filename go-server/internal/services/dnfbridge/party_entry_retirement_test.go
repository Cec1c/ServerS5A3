package dnfbridge

import "testing"

func TestPartyPreparedEntryRejectsRetiredMembershipBeforeStagedTail(t *testing.T) {
	f := newLeaderExitFixture(t)
	identity, ok := f.service.boundGameSessionCharacterSnapshot(f.member)
	if !ok {
		t.Fatal("member identity missing")
	}
	state := f.service.runtimePartyStateForSession(f.member)
	current := f.service.currentPartyProjectionMembershipGuard(identity, &state)
	f.member.dungeon.mu.Lock()
	runtime := f.member.dungeon.runtime
	f.member.dungeon.mu.Unlock()
	prepared := &runtimePartyPreparedDungeonEntry{session: f.member, runtime: runtime,
		current: current, committed: true, op28Staged: true}
	replacementPartyForRetirementProbe(t, f)
	f.memberConn.write.Reset()
	err := f.service.handleDungeonSelectUpperPlanned(f.member, nil, prepared, false, nil)
	if err == nil || f.memberConn.write.Len() != 0 {
		t.Fatalf("retired barrier entered a replacement party: err=%v bytes=%d", err, f.memberConn.write.Len())
	}
}

func TestPartyPreparedEntryRejectsReplacedRuntimeBeforeStagedTail(t *testing.T) {
	f := newLeaderExitFixture(t)
	identity, ok := f.service.boundGameSessionCharacterSnapshot(f.member)
	if !ok {
		t.Fatal("member identity missing")
	}
	state := f.service.runtimePartyStateForSession(f.member)
	current := f.service.currentPartyProjectionMembershipGuard(identity, &state)
	f.member.dungeon.mu.Lock()
	oldRuntime := f.member.dungeon.runtime
	replacement := &runtimeDungeonState{}
	f.member.dungeon.runtime = replacement
	f.member.dungeon.mu.Unlock()
	prepared := &runtimePartyPreparedDungeonEntry{session: f.member, runtime: oldRuntime,
		current: current, committed: true, op28Staged: true}
	f.memberConn.write.Reset()
	err := f.service.handleDungeonSelectUpperPlanned(f.member, nil, prepared, false, nil)
	f.member.dungeon.mu.Lock()
	actual := f.member.dungeon.runtime
	f.member.dungeon.mu.Unlock()
	if err == nil || f.memberConn.write.Len() != 0 || actual != replacement {
		t.Fatalf("old barrier tail affected replacement runtime: err=%v bytes=%d replaced=%t", err, f.memberConn.write.Len(), actual != replacement)
	}
}
