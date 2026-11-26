package dnfbridge

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"longheng.io/server/internal/modules/dnf/dnfenum"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

// orderedGuildRosterConn keeps the test's read of the wire independent from the
// handler's writes while a peer profile read is deliberately blocked.
type orderedGuildRosterConn struct {
	bufferConn
	mu sync.Mutex
}

func (c *orderedGuildRosterConn) Write(data []byte) (int, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.bufferConn.Write(data)
}

func (c *orderedGuildRosterConn) snapshot() []byte {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]byte(nil), c.bufferConn.write.Bytes()...)
}

type blockingGuildRosterCharacterRepo struct {
	dnfrepo.CharacterRepository
	target  string
	started chan struct{}
	release chan struct{}
	once    sync.Once
}

func (r *blockingGuildRosterCharacterRepo) Load(ctx context.Context, id string) (dnfrepo.CharacterRecord, bool, error) {
	if id == r.target {
		r.once.Do(func() { close(r.started) })
		select {
		case <-r.release:
		case <-ctx.Done():
			return dnfrepo.CharacterRecord{}, false, ctx.Err()
		}
	}
	return r.CharacterRepository.Load(ctx, id)
}

func containsGuildRosterFrame(t *testing.T, raw []byte) bool {
	t.Helper()
	for len(raw) > 0 {
		packet, rest := splitCurrentGameServerUpperPacketAuto(t, raw)
		raw = rest
		if packet.Header.MsgID == uint16(dnfenum.CmdPacketGuildAllMemberList) {
			return true
		}
	}
	return false
}

func TestP1GuildRosterResponsePrecedesColdPeerPrimeSweep(t *testing.T) {
	f, repos, viewer := setupP1WeddingGuildRosterFixture(t)
	s := f.service
	ctx := context.Background()

	// This is the ordinary, non-wedding path. The blocked read below is the
	// same-channel peer cache build that used to run before the roster response.
	// The current native row hook materializes a missing menu actor directly from
	// the op140 row fields, so this cache sweep is not an op140 decode prerequisite.
	viewer.Stats["wedding_v1_phase"] = 0
	if err := dnfrepo.SaveCharacterFields(ctx, repos.Character, viewer, dnfrepo.CharacterFieldWedding); err != nil {
		t.Fatal(err)
	}
	f.target.channel.ID, f.target.connectionTownActorOwnerChannel = 11, 11

	started := make(chan struct{})
	release := make(chan struct{})
	hook := &blockingGuildRosterCharacterRepo{
		CharacterRepository: repos.Character,
		target:              "17",
		started:             started,
		release:             release,
	}
	repos.Character = hook
	s.repositoryProvider = func() (dnfrepo.Group, bool) { return repos, true }

	transport := &orderedGuildRosterConn{}
	f.target.conn = transport

	saveP1PrimeBudgets(t, 2*time.Second, 0, 2*time.Second, 0)
	bound, ok := s.boundGameSessionCharacterSnapshot(f.target)
	if !ok {
		t.Fatal("missing bound snapshot")
	}
	display, err := s.readCurrentGuildDisplay(ctx, bound, false, true)
	if err != nil {
		t.Fatal(err)
	}

	done := make(chan error, 1)
	go func() {
		var buildTime, writeTime time.Duration
		done <- s.sendCurrentGuildRosterDisplayShared(ctx, bound, display, uint16(dnfenum.CmdPacketGuildAllMemberList), true, false, "client", false, nil, &buildTime, &writeTime)
	}()

	select {
	case <-started:
	case <-time.After(time.Second):
		close(release)
		t.Fatal("cold peer profile read was not reached")
	}

	// The response must be on the wire while the optional peer preparation is
	// still blocked. A caller context of up to two seconds makes the failure
	// deterministic without relying on the production 1.5s priming budget.
	deadline := time.Now().Add(250 * time.Millisecond)
	for !containsGuildRosterFrame(t, transport.snapshot()) && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if !containsGuildRosterFrame(t, transport.snapshot()) {
		close(release)
		<-done
		t.Fatal("guild roster was held behind the cold peer prime sweep")
	}
	if got := countSilentMode8Frames(t, transport.snapshot()); got != 0 {
		close(release)
		<-done
		t.Fatalf("peer prime unexpectedly completed before release: %d mode8 frames", got)
	}

	close(release)
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("guild roster display failed after releasing peer read: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("guild roster display did not complete after releasing peer read")
	}

	raw := transport.snapshot()
	if !containsGuildRosterFrame(t, raw) {
		t.Fatal("guild roster frame missing after completion")
	}
	if got := countSilentMode8Frames(t, raw); got != 1 {
		t.Fatalf("same-channel peer priming must remain enabled, got %d mode8 frames", got)
	}
}

func TestP1GuildRosterPrimeRequiresARosterFrame(t *testing.T) {
	t.Run("presence_trylock_busy", func(t *testing.T) {
		f, _, _ := setupP1WeddingGuildRosterFixture(t)
		s := f.service
		ctx := context.Background()
		f.target.channel.ID, f.target.connectionTownActorOwnerChannel = 11, 11
		bound, ok := s.boundGameSessionCharacterSnapshot(f.target)
		if !ok {
			t.Fatal("missing bound snapshot")
		}
		display, err := s.readCurrentGuildDisplay(ctx, bound, false, true)
		if err != nil {
			t.Fatal(err)
		}

		f.targetConn.write.Reset()
		f.target.townMu.Lock()
		var buildTime, writeTime time.Duration
		err = s.sendCurrentGuildRosterDisplayShared(ctx, bound, display, uint16(dnfenum.CmdPacketGuildAllMemberList), true, false, "presence", false, nil, &buildTime, &writeTime)
		f.target.townMu.Unlock()
		if err != nil {
			t.Fatalf("optional busy-presence roster returned error: %v", err)
		}
		if got := countSilentMode8Frames(t, f.targetConn.write.Bytes()); got != 0 {
			t.Fatalf("skipped roster unexpectedly primed %d peers", got)
		}
	})

	t.Run("channel_exit", func(t *testing.T) {
		f, _, _ := setupP1WeddingGuildRosterFixture(t)
		s := f.service
		ctx := context.Background()
		f.target.channel.ID, f.target.connectionTownActorOwnerChannel = 11, 11
		bound, ok := s.boundGameSessionCharacterSnapshot(f.target)
		if !ok {
			t.Fatal("missing bound snapshot")
		}
		display, err := s.readCurrentGuildDisplay(ctx, bound, false, true)
		if err != nil {
			t.Fatal(err)
		}

		f.targetConn.write.Reset()
		f.target.townMu.Lock()
		f.target.channelExitAcknowledged = true
		f.target.townMu.Unlock()
		var buildTime, writeTime time.Duration
		if err = s.sendCurrentGuildRosterDisplayShared(ctx, bound, display, uint16(dnfenum.CmdPacketGuildAllMemberList), true, false, "client", false, nil, &buildTime, &writeTime); err != nil {
			t.Fatalf("exited roster returned error: %v", err)
		}
		if got := countSilentMode8Frames(t, f.targetConn.write.Bytes()); got != 0 {
			t.Fatalf("roster skipped after channel exit but primed %d peers", got)
		}
	})

	t.Run("stale_generation", func(t *testing.T) {
		f, _, _ := setupP1WeddingGuildRosterFixture(t)
		s := f.service
		ctx := context.Background()
		bound, ok := s.boundGameSessionCharacterSnapshot(f.target)
		if !ok {
			t.Fatal("missing bound snapshot")
		}
		display, err := s.readCurrentGuildDisplay(ctx, bound, false, true)
		if err != nil {
			t.Fatal(err)
		}

		s.mu.Lock()
		advanceGameSessionCharacterGeneration(f.target)
		s.mu.Unlock()
		f.targetConn.write.Reset()
		var buildTime, writeTime time.Duration
		if err = s.sendCurrentGuildRosterDisplayShared(ctx, bound, display, uint16(dnfenum.CmdPacketGuildAllMemberList), true, false, "client", false, nil, &buildTime, &writeTime); err != nil {
			t.Fatalf("stale roster returned error: %v", err)
		}
		if got := countSilentMode8Frames(t, f.targetConn.write.Bytes()); got != 0 {
			t.Fatalf("stale generation roster primed %d peers", got)
		}
	})
}

func TestP1GuildRosterPrimeKeepsOneBudgetAcrossWeddingAndSameChannel(t *testing.T) {
	f, repos, viewer := setupP1WeddingGuildRosterFixture(t)
	s := f.service
	ctx := context.Background()
	f.target.channel.ID, f.target.connectionTownActorOwnerChannel = 11, 11
	addP1GuildOnlinePeer(t, s, repos, viewer, 31, "31", "dnf:31", "Peer31", 38, 0)

	var blockedCalls atomic.Int32
	repos.Character = &p1HookCharRepo{
		CharacterRepository: repos.Character,
		onLoad: func(callCtx context.Context, id string) (bool, error) {
			if id != "17" && id != "31" {
				return false, nil
			}
			blockedCalls.Add(1)
			<-callCtx.Done()
			return true, callCtx.Err()
		},
	}
	s.repositoryProvider = func() (dnfrepo.Group, bool) { return repos, true }
	saveP1PrimeBudgets(t, 100*time.Millisecond, 0, 100*time.Millisecond, 0)

	bound, ok := s.boundGameSessionCharacterSnapshot(f.target)
	if !ok {
		t.Fatal("missing bound snapshot")
	}
	display, err := s.readCurrentGuildDisplay(ctx, bound, false, true)
	if err != nil {
		t.Fatal(err)
	}
	f.targetConn.write.Reset()
	var buildTime, writeTime time.Duration
	if err = s.sendCurrentGuildRosterDisplayShared(ctx, bound, display, uint16(dnfenum.CmdPacketGuildAllMemberList), true, false, "client", false, nil, &buildTime, &writeTime); err != nil {
		t.Fatalf("roster display failed: %v", err)
	}
	if got := blockedCalls.Load(); got != 1 {
		t.Fatalf("wedding and same-channel sweeps used %d separate budgets, want one shared budget", got)
	}
	if len(guildReply(t, f.target, uint16(dnfenum.CmdPacketGuildAllMemberList))) == 0 {
		t.Fatal("roster frame missing after exhausted shared priming budget")
	}
}
