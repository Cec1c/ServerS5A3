package dnfbridge

import (
	"bytes"
	"context"
	"fmt"
	"reflect"
	"testing"
	"time"

	"longheng.io/server/internal/modules/dnf/dnfenum"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	"longheng.io/server/internal/platform/eventloop"
)

type reviewManagerMoveFixture struct {
	f     *raidStartGuardFixture
	peers [2]*gameSession
	conns [2]*deadlineAuditV10Conn
}

func reviewManagerMoveSetup(t *testing.T) reviewManagerMoveFixture {
	t.Helper()
	f, leader, _ := realAntonProjectionFixture(t, 0)
	if err := f.repos.Character.Save(context.Background(), dnfrepo.CharacterRecord{CharacterID: "20", AccountID: "account-20"}); err != nil {
		t.Fatal(err)
	}
	cs := [2]*deadlineAuditV10Conn{{}, {}}
	leader.conn = cs[0]
	member := &gameSession{conn: cs[1], channel: leader.channel, accountID: "account-20", loginPacketSeen: true, accountFromToken: true}
	f.service.bindGameSessionCharacter(member, 20)
	f.state.Members = []runtimeRaidMemberState{{CharID: 19, GroupIndex: 1}, {CharID: 20, GroupIndex: 1, SlotOrder: 1}}
	b := f.state.Battle
	b.MemberAccounts[20] = "account-20"
	b.MemberGroups = map[uint16]byte{19: 1, 20: 1}
	b.Participation = map[uint16]currentRaidParticipation{19: {RunID: "same-admitted-run", DungeonID: 210, Phase: b.Phase, Attempt: b.Anton.Epoch()}, 20: {RunID: "same-admitted-run", DungeonID: 210, Phase: b.Phase, Attempt: b.Anton.Epoch()}}
	peers := [2]*gameSession{leader, member}
	for i, p := range peers {
		if err := f.service.startGameSessionEvents(context.Background(), p); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { stopTestGameSessionEvents(t, f.service, p) })
		if err := f.service.callGameSession(context.Background(), p, "review-manager-initial-subscribe", func() error {
			return f.service.sendCurrentRaidProgressProjection(p, cloneRuntimeRaidState(*f.state), false, false)
		}); err != nil {
			t.Fatal(err)
		}
		cs[i].reset()
	}
	t.Cleanup(func() { _ = f.logger.Close() })
	return reviewManagerMoveFixture{f, peers, cs}
}

func reviewManagerCommand(t *testing.T, x reviewManagerMoveFixture, actor *gameSession, member, group uint32) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), createWriteTimeout)
	defer cancel()
	if err := x.f.service.callGameSession(ctx, actor, "review-manager-actual-inbound", func() error {
		handled, err := x.f.service.handleAlignedGameCommand(actor, byte(dnfenum.GameCmdCommand), uint16(dnfenum.CmdPacketRaidManagerWork), buildRaidManagerWorkRequestForBridgeTest(0, member, group))
		if !handled && err == nil {
			return fmt.Errorf("actual manager command not handled")
		}
		return err
	}); err != nil {
		t.Fatal(err)
	}
}

func reviewManagerWaitVersion(t *testing.T, p *gameSession, version uint64) {
	t.Helper()
	until := time.Now().Add(createWriteTimeout)
	for {
		p.raidProjection.mu.Lock()
		ready := p.raidProjection.initialized && !p.raidProjection.failed && p.raidProjection.version >= version
		p.raidProjection.mu.Unlock()
		if ready {
			return
		}
		if time.Now().After(until) {
			t.Fatal("actual manager queue did not publish live-squad projection")
		}
		time.Sleep(time.Millisecond)
	}
}

// Observe the real medium-priority event counters, without publishing or flushing.
func reviewManagerWaitMetrics(t *testing.T, p *gameSession, submitted, processed uint64) {
	t.Helper()
	until := time.Now().Add(createWriteTimeout)
	for {
		m := p.events.loop.MetricsSnapshot()
		if m.Submitted[eventloop.PriorityMedium] >= submitted && m.Processed[eventloop.PriorityMedium] >= processed {
			return
		}
		if time.Now().After(until) {
			t.Fatalf("actual peer queue counters submitted=%d/%d processed=%d/%d", m.Submitted[eventloop.PriorityMedium], submitted, m.Processed[eventloop.PriorityMedium], processed)
		}
		time.Sleep(time.Millisecond)
	}
}

func reviewManagerRows(t *testing.T, c *deadlineAuditV10Conn) ([][]byte, int) {
	t.Helper()
	stream := c.bytes()
	var rows [][]byte
	rosters := 0
	for len(stream) > 0 {
		p, rest := splitGameServerUpperPacket(t, stream)
		switch p.Header.MsgID {
		case 584:
			rows = append(rows, append([]byte(nil), p.Body...))
		case 591:
			rosters++
		case 669, 587, 583, 596, 597, 601, 602, 603:
			t.Fatalf("manager move emitted ACK/start/movie/clock/result/reward packet %d", p.Header.MsgID)
		}
		stream = rest
	}
	return rows, rosters
}

func TestRealPVFReview584ManagerMoveImmediatelySplitsSameRun(t *testing.T) {
	x := reviewManagerMoveSetup(t)
	before := cloneRuntimeRaidState(*x.f.state).Battle
	reviewManagerCommand(t, x, x.peers[0], 20, 2)
	// This is not a fixture version increment: the real manager mutation must
	// provide exactly one revision, and its own queued publisher must send it.
	x.f.service.raidMu.Lock()
	after := cloneRuntimeRaidState(*x.f.state).Battle
	x.f.service.raidMu.Unlock()
	if after.ProgressVersion != before.ProgressVersion+1 {
		t.Fatalf("manager revision=%d want=%d", after.ProgressVersion, before.ProgressVersion+1)
	}
	if !reflect.DeepEqual(after.MemberGroups, before.MemberGroups) || !reflect.DeepEqual(after.MemberAccounts, before.MemberAccounts) || !reflect.DeepEqual(after.Participation, before.Participation) || !reflect.DeepEqual(after.ClearedRuns, before.ClearedRuns) || !reflect.DeepEqual(after.RewardFlow, before.RewardFlow) || !after.Deadline.Equal(before.Deadline) || !after.EnterAfter.Equal(before.EnterAfter) || after.Phase != before.Phase || after.Anton.Epoch() != before.Anton.Epoch() {
		t.Fatal("manager UI move changed frozen reward/admission/run/phase owners")
	}
	want := [][]byte{{1, 210, 0, 0, 0, 0, 2, 19, 0, 20, 0}, {1, 210, 0, 0, 0, 0, 1, 19, 0}, {1, 210, 0, 0, 0, 0, 1, 20, 0}, {1, 210, 0, 0, 0, 2, 1, 19, 0}, {1, 210, 0, 0, 0, 2, 1, 20, 0}}
	for i, p := range x.peers {
		reviewManagerWaitVersion(t, p, after.ProgressVersion)
		if err := x.f.service.callGameSession(context.Background(), p, "review-manager-readonly-audit", func() error { return nil }); err != nil {
			t.Fatal(err)
		}
		rows, rosters := reviewManagerRows(t, x.conns[i])
		if rosters != 1 || len(rows) != len(want) {
			t.Fatalf("peer%d roster=%d rows=%x", i, rosters, rows)
		}
		for k := range want {
			if !bytes.Equal(rows[k], want[k]) {
				t.Fatalf("peer%d row%d=%x want=%x", i, k, rows[k], want[k])
			}
		}
		p.raidProjection.mu.Lock()
		group := p.raidProjection.participation[20].GroupIndex
		p.raidProjection.mu.Unlock()
		if group != 2 {
			t.Fatal("published subscriber retained old group", group)
		}
	}
}

func TestRealPVFReview584ManagerMoveNoopAndRejections(t *testing.T) {
	x := reviewManagerMoveSetup(t)
	version := x.f.state.Battle.ProgressVersion
	reviewManagerCommand(t, x, x.peers[0], 20, 1)
	for _, c := range x.conns {
		rows, n := reviewManagerRows(t, c)
		if len(rows) != 0 || n != 1 {
			t.Fatal("samegroup changed participation", rows, n)
		}
		c.reset()
	}
	for _, tc := range []struct {
		actor         *gameSession
		member, group uint32
	}{{x.peers[1], 19, 2}, {x.peers[0], 20, 0}, {x.peers[0], 20, uint32(runtimeRaidMaxGroups + 1)}, {x.peers[0], 99, 2}} {
		reviewManagerCommand(t, x, tc.actor, tc.member, tc.group)
	}
	if x.f.state.Battle.ProgressVersion != version {
		t.Fatal("noop/rejection changed revision")
	}
	for _, c := range x.conns {
		rows, n := reviewManagerRows(t, c)
		if len(rows) != 0 || n != 0 {
			t.Fatal("rejected manager command published", rows, n)
		}
	}
}

func TestRealPVFReview584ManagerMoveDropsReplacedPeer(t *testing.T) {
	x := reviewManagerMoveSetup(t)
	s := x.f.service
	metricsBefore := x.peers[1].events.loop.MetricsSnapshot()
	submittedBefore, processedBefore := metricsBefore.Submitted[eventloop.PriorityMedium], metricsBefore.Processed[eventloop.PriorityMedium]
	blocked, release, done := make(chan struct{}), make(chan struct{}), make(chan error, 1)
	go func() {
		done <- s.callGameSession(context.Background(), x.peers[1], "review-block-old-peer", func() error { close(blocked); <-release; return nil })
	}()
	released := false
	defer func() {
		if !released {
			close(release)
		}
	}()
	waitTestSignal(t, blocked, "old peer did not block")
	reviewManagerWaitMetrics(t, x.peers[1], submittedBefore+1, processedBefore)
	oldVersion := x.peers[1].raidProjection.version
	reviewManagerCommand(t, x, x.peers[0], 20, 2)
	// The old loop is still blocked: this proves the actual manager callback
	// has been submitted behind it before canonical owner replacement.
	reviewManagerWaitMetrics(t, x.peers[1], submittedBefore+2, processedBefore)
	replacement := &gameSession{conn: &deadlineAuditV10Conn{}, channel: x.peers[1].channel, accountID: "account-20", loginPacketSeen: true, accountFromToken: true}
	s.bindGameSessionCharacter(replacement, 20)
	close(release)
	released = true
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	// Both the blocker and actual manager callback must finish before audit.
	reviewManagerWaitMetrics(t, x.peers[1], submittedBefore+2, processedBefore+2)
	reviewManagerWaitVersion(t, x.peers[0], oldVersion+1)
	if err := s.callGameSession(context.Background(), x.peers[1], "review-old-peer-readonly-audit", func() error { return nil }); err != nil {
		t.Fatal(err)
	}
	rows, _ := reviewManagerRows(t, x.conns[1])
	if len(rows) != 0 {
		t.Fatal("old peer received new owner projection", rows)
	}
	x.peers[1].raidProjection.mu.Lock()
	v := x.peers[1].raidProjection.version
	x.peers[1].raidProjection.mu.Unlock()
	if v != oldVersion {
		t.Fatal("old peer revision advanced")
	}
}
