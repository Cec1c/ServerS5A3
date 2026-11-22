package dnfbridge

import (
	"context"
	"net"
	"sync/atomic"
	"testing"
	"time"

	"longheng.io/server/internal/modules/dnf/dnfenum"
	dnfguild "longheng.io/server/internal/modules/dnf/guild"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	"longheng.io/server/internal/modules/dnf/repository/memory"
	"longheng.io/server/internal/modules/dnf/wedding"
	"longheng.io/server/internal/platform/eventloop"
)

type countingGuildPresenceUOW struct {
	dnfrepo.GuildUnitOfWork
	dnfrepo.GuildReadUnitOfWork
	withinGuildCalls    atomic.Int32
	withinPresenceCalls atomic.Int32
	withinReadCalls     atomic.Int32
}

func (u *countingGuildPresenceUOW) WithinGuild(ctx context.Context, owners []dnfrepo.GuildOwner, apply func(dnfrepo.Group) error) error {
	u.withinGuildCalls.Add(1)
	return u.GuildUnitOfWork.WithinGuild(ctx, owners, apply)
}

func (u *countingGuildPresenceUOW) WithinGuildPresence(ctx context.Context, owner dnfrepo.GuildOwner, apply func(dnfrepo.Group) error) error {
	u.withinPresenceCalls.Add(1)
	if p, ok := u.GuildUnitOfWork.(dnfrepo.GuildPresenceUnitOfWork); ok {
		return p.WithinGuildPresence(ctx, owner, apply)
	}
	return u.GuildUnitOfWork.WithinGuild(ctx, []dnfrepo.GuildOwner{owner}, apply)
}

func (u *countingGuildPresenceUOW) WithinGuildRead(ctx context.Context, apply func(dnfrepo.GuildReadView) error) error {
	u.withinReadCalls.Add(1)
	return u.GuildReadUnitOfWork.WithinGuildRead(ctx, apply)
}

func TestPhase2PersistGuildPresenceSkipsWriteLockViaAuthoritativeGuildRead(t *testing.T) {
	s, _, repos, now := guildPresenceFixture(t)
	if err := repos.Character.Save(context.Background(), dnfrepo.CharacterRecord{
		CharacterID: "99",
		AccountID:   "a99",
		Name:        "SoloChar",
		Level:       90,
		Stats:       map[string]int64{"delete_flag": 0},
	}); err != nil {
		t.Fatal(err)
	}
	counter := &countingGuildPresenceUOW{
		GuildUnitOfWork:     repos.Guild,
		GuildReadUnitOfWork: repos.Guild.(dnfrepo.GuildReadUnitOfWork),
	}
	repos.Guild = counter
	s.repositoryProvider = func() (dnfrepo.Group, bool) { return repos, true }

	// 1. 非公会成员：必须经由权威 WithinGuildRead 预检并跳过全局写锁事务。
	s.persistCurrentGuildPresence(currentGuildPresenceObservation{
		owner: dnfrepo.GuildOwner{CharacterID: "99", AccountID: "a99"},
		at:    now.Add(time.Minute),
	})
	if counter.withinReadCalls.Load() != 1 {
		t.Fatalf("expected 1 authoritative WithinGuildRead pre-check, got %d", counter.withinReadCalls.Load())
	}
	if counter.withinPresenceCalls.Load() != 0 || counter.withinGuildCalls.Load() != 0 {
		t.Fatalf("guildless character unexpectedly acquired guild write lock: presence=%d guild=%d",
			counter.withinPresenceCalls.Load(), counter.withinGuildCalls.Load())
	}

	// 2. 模拟普通只读 Settings 副本落后（返回空注册表），但主读 WithinGuildRead 有该成员：
	// 预检必须信任 WithinGuildRead 而不是普通 Settings 副本，仍执行权威写事务。
	staleReplica := memory.NewMemoryGroup()
	primarySettings := repos.Settings
	repos.Settings = staleReplica.Settings
	s.repositoryProvider = func() (dnfrepo.Group, bool) { return repos, true }

	s.gameSessions = map[uint16]*gameSession{}
	s.persistCurrentGuildPresence(currentGuildPresenceObservation{
		owner: dnfrepo.GuildOwner{CharacterID: "2", AccountID: "a2"},
		at:    now.Add(2 * time.Minute),
	})
	if counter.withinPresenceCalls.Load() != 1 {
		t.Fatalf("guild member with lagging ordinary Settings expected 1 WithinGuildPresence call, got %d", counter.withinPresenceCalls.Load())
	}
	repos.Settings = primarySettings
	if got := guildPresenceLastSeen(t, repos, "2"); !got.Equal(now.Add(2 * time.Minute)) {
		t.Fatalf("guild member LastOnlineAt=%v want %v", got, now.Add(2*time.Minute))
	}
}

func TestPhase2WeddingPrimePreservedForReservedViewerOnPresenceAndMembership(t *testing.T) {
	for _, source := range []string{"presence", "membership"} {
		t.Run(source, func(t *testing.T) {
			f, repos := guildMemberProfileFixture(t)
			s := f.service
			ctx := context.Background()

			coherent := memory.NewMemoryGroup()
			for _, id := range []string{"29", "17"} {
				c, found, err := repos.Character.Load(ctx, id)
				if err != nil || !found {
					t.Fatalf("load char %s: found=%v err=%v", id, found, err)
				}
				if err := coherent.Character.Save(ctx, c); err != nil {
					t.Fatal(err)
				}
			}
			registry, found, err := repos.Settings.Load(ctx, dnfrepo.GuildRegistryScope)
			if err != nil || !found {
				t.Fatalf("load registry: found=%v err=%v", found, err)
			}
			if err := coherent.Settings.Save(ctx, registry); err != nil {
				t.Fatal(err)
			}
			viewer, _, _ := coherent.Character.Load(ctx, "29")
			viewer.Stats["wedding_v1_phase"] = int64(wedding.Reserved)
			viewer.Stats["wedding_v1_partner"] = 30
			viewer.Stats["wedding_v1_ceremony_at"] = s.gameplayNow().Unix() + 300
			viewer.Stats["wedding_v1_ceremony_channel"] = int64(currentTownPresenceChannelID(f.target))
			viewer.Stats["wedding_v1_ceremony_area"] = 8
			if err := dnfrepo.SaveCharacterFields(ctx, coherent.Character, viewer, dnfrepo.CharacterFieldWedding); err != nil {
				t.Fatal(err)
			}
			repos.Character, repos.Settings, repos.Guild = coherent.Character, coherent.Settings, coherent.Guild
			s.repositoryProvider = func() (dnfrepo.Group, bool) { return repos, true }

			bound, ok := s.boundGameSessionCharacterSnapshot(f.target)
			if !ok {
				t.Fatal("missing bound snapshot")
			}
			display, err := s.readCurrentGuildDisplay(ctx, bound, false, true)
			if err != nil || display == nil {
				t.Fatalf("readCurrentGuildDisplay failed: g=%v err=%v", display, err)
			}
			f.targetConn.write.Reset()
			var buildTime, writeTime time.Duration
			if err := s.sendCurrentGuildRosterDisplayShared(ctx, bound, display, uint16(dnfenum.CmdPacketGuildAllMemberList), true, true, source, false, nil, &buildTime, &writeTime); err != nil {
				t.Fatalf("roster send (%s) failed: %v", source, err)
			}
			prime, roster := -1, -1
			for wire, idx := f.targetConn.write.Bytes(), 0; len(wire) > 0; idx++ {
				pkt, rest := splitCurrentGameServerUpperPacketAuto(t, wire)
				wire = rest
				if pkt.Header.Classification == 0 && pkt.Header.MsgID == 2 && len(pkt.Body) > 0 && pkt.Body[0] == 8 {
					prime = idx
				}
				if pkt.Header.MsgID == uint16(dnfenum.CmdPacketGuildAllMemberList) {
					roster = idx
				}
			}
			if prime < 0 || roster <= prime {
				t.Fatalf("source=%s did not send silent mode8 profile before roster140: prime=%d roster=%d", source, prime, roster)
			}
		})
	}
}

type blockingWriteConn struct {
	*bufferConn
	blockCh chan struct{}
	entered chan struct{}
}

func (c *blockingWriteConn) Write(p []byte) (int, error) {
	select {
	case c.entered <- struct{}{}:
	default:
	}
	<-c.blockCh
	return c.bufferConn.Write(p)
}

func (c *blockingWriteConn) SetWriteDeadline(time.Time) error { return nil }
func (c *blockingWriteConn) LocalAddr() net.Addr              { return c.bufferConn.LocalAddr() }
func (c *blockingWriteConn) RemoteAddr() net.Addr             { return c.bufferConn.RemoteAddr() }

func TestPhase2BroadcastCurrentGuildMembersDispatchesAsyncPreservingMembershipSemantics(t *testing.T) {
	s, peers, repos, _ := guildPresenceFixture(t)
	// 即使开启 5s presence 合并延迟、最近刚刷新过、且处于副本状态，
	// membership 广播仍必须立即在接收方事件循环执行且不阻塞发起方。
	s.guildPresenceCoalesceDelay = 5 * time.Second
	peers[0].guild.lastPresenceRefreshAt = time.Now()
	peers[0].guild.memberListRequested = true
	peers[0].dungeon.runtime = &runtimeDungeonState{}

	blockedConn := &blockingWriteConn{
		bufferConn: &bufferConn{},
		blockCh:    make(chan struct{}),
		entered:    make(chan struct{}, 1),
	}
	peers[0].conn = blockedConn

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	peers[0].ctx = ctx
	if err := s.startGameSessionEvents(ctx, peers[0]); err != nil {
		t.Fatal(err)
	}
	defer func() {
		close(blockedConn.blockCh)
		stopTestGameSessionEvents(t, s, peers[0])
	}()

	reg, err := dnfguild.Load(context.Background(), repos.Settings)
	if err != nil {
		t.Fatal(err)
	}
	g := reg.Guilds[1]

	done := make(chan struct{})
	go func() {
		s.broadcastCurrentGuildMembers(g, 2)
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(500 * time.Millisecond):
		t.Fatal("broadcastCurrentGuildMembers blocked caller on slow peer socket")
	}

	select {
	case <-blockedConn.entered:
	case <-time.After(time.Second):
		t.Fatal("peer event loop did not execute dispatched membership refresh immediately (even in dungeon)")
	}
}

func TestPhase2BroadcastCurrentGuildMembersRecoversAfterQueueFull(t *testing.T) {
	s, peers, repos, _ := guildPresenceFixture(t)
	peer := peers[0]
	peer.guild.memberListRequested = true
	peer.conn.(*bufferConn).write.Reset()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	peer.ctx = ctx
	if err := s.startGameSessionEvents(ctx, peer); err != nil {
		t.Fatal(err)
	}
	defer stopTestGameSessionEvents(t, s, peer)

	// 占住事件循环并填满队列，制造确定性的 ErrQueueFull。
	entered := make(chan struct{})
	release := make(chan struct{})
	defer func() {
		select {
		case <-release:
		default:
			close(release)
		}
	}()
	if err := s.tryPostGameSessionCharacterEvent(peer, "hold-loop", peer.selectedCharacterID, peer.characterGeneration, func() error {
		close(entered)
		<-release
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	<-entered
	filled := false
	for i := 0; i < 4096; i++ {
		err := s.tryPostGameSessionCharacterEvent(peer, "fill-queue", peer.selectedCharacterID, peer.characterGeneration, func() error { return nil })
		if err != nil {
			if err == eventloop.ErrQueueFull {
				filled = true
				break
			}
			t.Fatalf("unexpected error while filling queue: %v", err)
		}
	}
	if !filled {
		t.Fatal("failed to fill peer event queue")
	}

	reg, err := dnfguild.Load(context.Background(), repos.Settings)
	if err != nil {
		t.Fatal(err)
	}
	g := reg.Guilds[1]

	// 队列满时发起方必须立即返回，并保留待重试令牌。
	done := make(chan struct{})
	go func() {
		s.broadcastCurrentGuildMembers(g, 2)
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(500 * time.Millisecond):
		t.Fatal("broadcastCurrentGuildMembers blocked when peer queue was full")
	}

	peer.guild.mu.Lock()
	hasRetryToken := peer.guild.membershipRefresh != nil && peer.guild.membershipRefresh.timer != nil
	peer.guild.mu.Unlock()
	if !hasRetryToken {
		t.Fatal("expected retry token to be armed after ErrQueueFull")
	}

	// 释放并排空队列，等待重试令牌执行完成并清空（避免并发读取 bufferConn）。
	close(release)
	deadline := time.Now().Add(2 * time.Second)
	recovered := false
	for time.Now().Before(deadline) {
		peer.guild.mu.Lock()
		doneToken := peer.guild.membershipRefresh == nil
		peer.guild.mu.Unlock()
		if doneToken {
			recovered = true
			break
		}
		time.Sleep(15 * time.Millisecond)
	}
	if !recovered {
		t.Fatal("queued membership refresh retry token did not finish after queue drained")
	}
	drained := make(chan struct{})
	if err := s.tryPostGameSessionCharacterEvent(peer, "after-recover-fence", peer.selectedCharacterID, peer.characterGeneration, func() error {
		close(drained)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	select {
	case <-drained:
	case <-time.After(time.Second):
		t.Fatal("event loop did not drain after membership recovery")
	}
	gotRoster := false
	for _, id := range collectUpperMsgIDs(t, peer.conn.(*bufferConn).write.Bytes()) {
		if id == uint16(dnfenum.CmdPacketGuildAllMemberList) {
			gotRoster = true
			break
		}
	}
	if !gotRoster {
		t.Fatal("queued membership refresh did not write opcode 140 after queue drained")
	}
}

func TestPhase2BroadcastCurrentGuildMembersRecoversAfterRetriesExhausted(t *testing.T) {
	origDelay := guildMembershipRetryBaseDelay
	guildMembershipRetryBaseDelay = 5 * time.Millisecond
	defer func() { guildMembershipRetryBaseDelay = origDelay }()

	s, peers, repos, _ := guildPresenceFixture(t)
	peer := peers[0]
	peer.guild.memberListRequested = true
	peer.conn.(*bufferConn).write.Reset()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	peer.ctx = ctx
	if err := s.startGameSessionEvents(ctx, peer); err != nil {
		t.Fatal(err)
	}
	defer stopTestGameSessionEvents(t, s, peer)

	entered := make(chan struct{})
	release := make(chan struct{})
	defer func() {
		select {
		case <-release:
		default:
			close(release)
		}
	}()
	if err := s.tryPostGameSessionCharacterEvent(peer, "hold-loop-until-exhausted", peer.selectedCharacterID, peer.characterGeneration, func() error {
		close(entered)
		<-release
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	<-entered
	filled := false
	for i := 0; i < 4096; i++ {
		err := s.tryPostGameSessionCharacterEvent(peer, "fill-queue-exhausted", peer.selectedCharacterID, peer.characterGeneration, func() error { return nil })
		if err != nil {
			if err == eventloop.ErrQueueFull {
				filled = true
				break
			}
			t.Fatalf("unexpected error while filling queue: %v", err)
		}
	}
	if !filled {
		t.Fatal("failed to fill peer event queue")
	}

	reg, err := dnfguild.Load(context.Background(), repos.Settings)
	if err != nil {
		t.Fatal(err)
	}
	// 仅发起一次成员广播，保持队列满直至 5 次主动退避全部耗尽。
	s.broadcastCurrentGuildMembers(reg.Guilds[1], 2)

	exhaustDeadline := time.Now().Add(2 * time.Second)
	exhausted := false
	for time.Now().Before(exhaustDeadline) {
		peer.guild.mu.Lock()
		p := peer.guild.membershipRefresh
		if p != nil && p.exhausted && p.retries == maxGuildMembershipRefreshRetries && p.timer == nil && !p.queued && peer.guild.membershipRefreshPending.Load() {
			exhausted = true
		}
		peer.guild.mu.Unlock()
		if exhausted {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if !exhausted {
		t.Fatal("membership refresh did not reach exhausted-retained state while queue stayed full")
	}

	// 不再发起任何新的成员广播，仅释放队列，验证事件循环释放槽位后自动补发 opcode 140。
	close(release)
	recoverDeadline := time.Now().Add(2 * time.Second)
	recovered := false
	for time.Now().Before(recoverDeadline) {
		peer.guild.mu.Lock()
		doneToken := peer.guild.membershipRefresh == nil && !peer.guild.membershipRefreshPending.Load()
		peer.guild.mu.Unlock()
		if doneToken {
			recovered = true
			break
		}
		time.Sleep(15 * time.Millisecond)
	}
	if !recovered {
		t.Fatal("exhausted membership refresh did not recover after queue drained")
	}
	drained := make(chan struct{})
	if err := s.tryPostGameSessionCharacterEvent(peer, "after-exhausted-recover-fence", peer.selectedCharacterID, peer.characterGeneration, func() error {
		close(drained)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	select {
	case <-drained:
	case <-time.After(time.Second):
		t.Fatal("event loop did not drain after exhausted membership recovery")
	}
	gotRoster := false
	for _, id := range collectUpperMsgIDs(t, peer.conn.(*bufferConn).write.Bytes()) {
		if id == uint16(dnfenum.CmdPacketGuildAllMemberList) {
			gotRoster = true
			break
		}
	}
	if !gotRoster {
		t.Fatal("exhausted membership refresh did not deliver opcode 140 after queue recovered")
	}
}

func TestPhase2BroadcastCurrentGuildMembersNoLostWakeupWhenQueueDrainsBeforePending(t *testing.T) {
	origDelay := guildMembershipRetryBaseDelay
	origHook := testGuildMembershipBeforeExhaustHook
	guildMembershipRetryBaseDelay = 5 * time.Millisecond
	beforePublish := make(chan struct{})
	allowPublish := make(chan struct{})
	var hookOnce atomic.Bool
	testGuildMembershipBeforeExhaustHook = func() {
		if hookOnce.CompareAndSwap(false, true) {
			close(beforePublish)
			<-allowPublish
		}
	}
	defer func() {
		guildMembershipRetryBaseDelay = origDelay
		testGuildMembershipBeforeExhaustHook = origHook
		select {
		case <-allowPublish:
		default:
			close(allowPublish)
		}
	}()

	s, peers, repos, _ := guildPresenceFixture(t)
	peer := peers[0]
	peer.guild.memberListRequested = true
	peer.conn.(*bufferConn).write.Reset()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	peer.ctx = ctx
	if err := s.startGameSessionEvents(ctx, peer); err != nil {
		t.Fatal(err)
	}
	defer stopTestGameSessionEvents(t, s, peer)

	entered := make(chan struct{})
	release := make(chan struct{})
	defer func() {
		select {
		case <-release:
		default:
			close(release)
		}
	}()
	if err := s.tryPostGameSessionCharacterEvent(peer, "hold-before-lost-wakeup", peer.selectedCharacterID, peer.characterGeneration, func() error {
		close(entered)
		<-release
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	<-entered

	filledCount := 0
	filled := false
	for i := 0; i < 4096; i++ {
		err := s.tryPostGameSessionCharacterEvent(peer, "fill-lost-wakeup", peer.selectedCharacterID, peer.characterGeneration, func() error { return nil })
		if err != nil {
			if err == eventloop.ErrQueueFull {
				filled = true
				break
			}
			t.Fatalf("unexpected error while filling queue: %v", err)
		}
		filledCount++
	}
	if !filled {
		t.Fatal("failed to fill peer event queue")
	}

	reg, err := dnfguild.Load(context.Background(), repos.Settings)
	if err != nil {
		t.Fatal(err)
	}
	// 仅发起一次成员广播，等待最后一次退避收到 ErrQueueFull 且尚未发布 pending。
	s.broadcastCurrentGuildMembers(reg.Guilds[1], 2)
	select {
	case <-beforePublish:
	case <-time.After(2 * time.Second):
		t.Fatal("did not reach pre-pending-publish barrier after final ErrQueueFull")
	}

	// 在 pending 发布前完全排空队列，且不投递任何额外普通事件。
	expectedDrained := uint64(1 + filledCount)
	close(release)
	drainDeadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(drainDeadline) {
		if peer.guild.eventLoopCompletedSeq.Load() >= expectedDrained {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if got := peer.guild.eventLoopCompletedSeq.Load(); got != expectedDrained {
		t.Fatalf("expected queue to drain exactly %d events before pending publish, got %d", expectedDrained, got)
	}
	if peer.guild.membershipRefreshPending.Load() {
		t.Fatal("membershipRefreshPending was already true before releasing pre-publish barrier")
	}

	// 放行 pending 发布；之后不再发任何新广播或普通事件，也必须自动补发并执行 opcode 140。
	close(allowPublish)
	recoverDeadline := time.Now().Add(2 * time.Second)
	recovered := false
	for time.Now().Before(recoverDeadline) {
		peer.guild.mu.Lock()
		doneToken := peer.guild.membershipRefresh == nil && !peer.guild.membershipRefreshPending.Load()
		peer.guild.mu.Unlock()
		if doneToken && peer.guild.eventLoopCompletedSeq.Load() >= expectedDrained+1 {
			recovered = true
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if !recovered {
		t.Fatalf("lost wakeup: refresh did not recover without new events (seq=%d want>=%d)",
			peer.guild.eventLoopCompletedSeq.Load(), expectedDrained+1)
	}
	gotRoster := false
	for _, id := range collectUpperMsgIDs(t, peer.conn.(*bufferConn).write.Bytes()) {
		if id == uint16(dnfenum.CmdPacketGuildAllMemberList) {
			gotRoster = true
			break
		}
	}
	if !gotRoster {
		t.Fatal("recovered membership refresh did not write opcode 140")
	}
}

func TestPhase2BroadcastCurrentGuildMembersDiscardsStaleCharacterAfterSwitch(t *testing.T) {
	s, peers, repos, _ := guildPresenceFixture(t)
	peer := peers[0]
	peer.guild.memberListRequested = true
	peer.conn.(*bufferConn).write.Reset()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	peer.ctx = ctx
	if err := s.startGameSessionEvents(ctx, peer); err != nil {
		t.Fatal(err)
	}
	defer stopTestGameSessionEvents(t, s, peer)

	entered := make(chan struct{})
	release := make(chan struct{})
	if err := s.tryPostGameSessionCharacterEvent(peer, "hold-before-switch", peer.selectedCharacterID, peer.characterGeneration, func() error {
		close(entered)
		<-release
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	<-entered

	reg, err := dnfguild.Load(context.Background(), repos.Settings)
	if err != nil {
		t.Fatal(err)
	}
	s.broadcastCurrentGuildMembers(reg.Guilds[1], 2)

	// 排队后切换角色世代，旧角色的 membership 刷新必须被丢弃且不得写出任何字节。
	s.mu.Lock()
	peer.characterGeneration++
	s.mu.Unlock()
	resetCurrentGuildPresenceRefresh(peer, false)

	close(release)
	drained := make(chan struct{})
	if err := s.tryPostGameSessionCharacterEvent(peer, "after-switch-fence", peer.selectedCharacterID, peer.characterGeneration, func() error {
		close(drained)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	select {
	case <-drained:
	case <-time.After(time.Second):
		t.Fatal("event loop did not drain after switch")
	}
	if n := peer.conn.(*bufferConn).write.Len(); n != 0 {
		t.Fatalf("stale membership refresh wrote %d bytes after character switch", n)
	}
}

type countingSettingsLoadRepo struct {
	dnfrepo.SettingsRepository
	loads atomic.Int32
}

func (r *countingSettingsLoadRepo) Load(ctx context.Context, scope string) (dnfrepo.SettingsRecord, bool, error) {
	if scope == dnfrepo.GuildRegistryScope {
		r.loads.Add(1)
	}
	return r.SettingsRepository.Load(ctx, scope)
}

func TestPhase2HandleCurrentGuildRecommendReadsDirectoryOnce(t *testing.T) {
	s, peers, repos, _ := guildPresenceFixture(t)
	counted := &countingSettingsLoadRepo{SettingsRepository: repos.Settings}
	repos.Settings = counted
	s.repositoryProvider = func() (dnfrepo.Group, bool) { return repos, true }

	if err := s.handleCurrentGuildRecommend(peers[0], nil); err != nil {
		t.Fatalf("handleCurrentGuildRecommend failed: %v", err)
	}
	if got := counted.loads.Load(); got != 1 {
		t.Fatalf("expected 1 GuildRegistryScope read in handleCurrentGuildRecommend, got %d", got)
	}
}

func collectUpperMsgIDs(t *testing.T, raw []byte) []uint16 {
	t.Helper()
	var ids []uint16
	for len(raw) > 0 {
		pkt, rest := splitCurrentGameServerUpperPacketAuto(t, raw)
		ids = append(ids, pkt.Header.MsgID)
		raw = rest
	}
	return ids
}
