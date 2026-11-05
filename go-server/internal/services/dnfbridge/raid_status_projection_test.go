package dnfbridge

import (
	"bytes"
	"context"
	"strconv"
	"testing"
	"time"

	"longheng.io/server/internal/modules/dnf/dnfenum"
)

func TestRaidCompactProfilesReadRosterOncePerBroadcast(t *testing.T) {
	s, _, _, _, _, state := prepareApprovedCurrentRaid(t)
	loads := wrapCurrentCrossSceneProfileBatchCharacterLoads(t, s, nil)
	batch := newCurrentRaidProfileBatch(s, context.Background())
	first, err := s.currentRuntimeRaidProfileBody(state, batch)
	if err != nil || len(first) == 0 {
		t.Fatal(err)
	}
	for range 20 {
		body, err := s.currentRuntimeRaidProfileBody(state, batch)
		if err != nil || !bytes.Equal(body, first) {
			t.Fatal("broadcast compact profile changed", err)
		}
	}
	for _, member := range state.Members {
		if loads[strconv.Itoa(int(member.CharID))] != 1 {
			t.Fatal("repeated per-receiver repository reads", loads)
		}
	}
	// A later refresh must reload authoritative fields, with no cross-refresh
	// caching of names, levels, job or owner context.
	_, err = s.currentRuntimeRaidProfileBody(state, newCurrentRaidProfileBatch(s, context.Background()))
	if err != nil {
		t.Fatal(err)
	}
	for _, member := range state.Members {
		if loads[strconv.Itoa(int(member.CharID))] != 2 {
			t.Fatal("later broadcast reused stale repository rows", loads)
		}
	}
}

func TestRaidMemberStatusDoesNotRebuildProfilesOrWaitForDungeonLocks(t *testing.T) {
	s, leader, member, lc, mc, _ := prepareApprovedCurrentRaid(t)
	leader.connectionTownActorOwnerChannel = byte(leader.channel.ID)
	member.connectionTownActorOwnerChannel = byte(member.channel.ID)
	lc.write.Reset()
	mc.write.Reset()
	// A combat handler may own these locks. A roster status update must not
	// enter mode8's town-HP sampling/retry loop while those handlers run.
	leader.dungeon.mu.Lock()
	member.dungeon.mu.Lock()
	done := make(chan error, 1)
	go func() {
		done <- s.handleOnlineRaidMemberChangeState(member, uint16(dnfenum.CmdPacketRaidMemberChangeState), []byte{3})
	}()
	select {
	case err := <-done:
		member.dungeon.mu.Unlock()
		leader.dungeon.mu.Unlock()
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		member.dungeon.mu.Unlock()
		leader.dungeon.mu.Unlock()
		<-done
		t.Fatal("member state refresh waited for cosmetic dungeon lock sampling")
	}
	for _, conn := range []*bufferConn{lc, mc} {
		roster := 0
		for _, frame := range splitUpperFrames(t, conn.write.Bytes()) {
			if frame.msgID == 2 || frame.msgID == 752 {
				t.Fatal("status-only notification replayed actor/profile data")
			}
			if frame.msgID == 591 {
				roster++
			}
		}
		if roster != 1 {
			t.Fatalf("roster notifications=%d, want one", roster)
		}
	}
}
