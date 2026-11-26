package dnfbridge

import (
	"context"
	"encoding/binary"
	"errors"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"longheng.io/server/internal/modules/dnf/guild"
	"longheng.io/server/internal/modules/dnf/pvf"
	"longheng.io/server/internal/modules/dnf/quest"
	r "longheng.io/server/internal/modules/dnf/repository"
)

type p3CountingSettingsRepo struct {
	base           r.SettingsRepository
	guildLoads     atomic.Int64
	questPendLoads atomic.Int64
	allLoads       atomic.Int64
}

type p3InvalidatingMirrorRepo struct {
	r.CharacterRepository
	onMirror func(id string)
}

func (repo *p3InvalidatingMirrorRepo) LoadCharacterMirrorStats(
	ctx context.Context,
	id string,
	keys ...string,
) (r.CharacterMirrorStats, bool, error) {
	if repo.onMirror != nil {
		repo.onMirror(id)
	}
	return r.LoadCharacterMirrorStats(ctx, repo.CharacterRepository, id, keys...)
}

func (c *p3CountingSettingsRepo) Load(ctx context.Context, scope string) (r.SettingsRecord, bool, error) {
	c.allLoads.Add(1)
	if scope == guild.RegistryScope {
		c.guildLoads.Add(1)
	}
	if strings.HasSuffix(scope, ":quest_material:pending") {
		c.questPendLoads.Add(1)
	}
	return c.base.Load(ctx, scope)
}

func (c *p3CountingSettingsRepo) Save(ctx context.Context, record r.SettingsRecord) error {
	return c.base.Save(ctx, record)
}

type p3PrepareSyncUOW struct {
	base    r.QuestMaterialPreparationUnitOfWork
	entered chan struct{}
	release chan struct{}
	once    sync.Once
}

func (u *p3PrepareSyncUOW) WithinQuestMaterialPreparation(ctx context.Context, ids []string, apply func(r.Group) error) error {
	return u.base.WithinQuestMaterialPreparation(ctx, ids, func(tx r.Group) error {
		u.once.Do(func() {
			close(u.entered)
			<-u.release
		})
		return apply(tx)
	})
}

func TestP3GuildEssencePeerRefreshCoalesceSingleReadAndGuards(t *testing.T) {
	s, peers, repos := guildHallFixture(t)
	s.options.guildEssenceEnabled = true
	s.guildPresenceCoalesceDelay = 40 * time.Millisecond

	counting := &p3CountingSettingsRepo{base: repos.Settings}
	repos.Settings = counting
	s.repositoryProvider = func() (r.Group, bool) { return repos, true }

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	for _, id := range []uint16{30, 31, 32, 33} {
		p := peers[id]
		if err := s.startGameSessionEvents(ctx, p); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			stop, stopCancel := context.WithTimeout(context.Background(), time.Second)
			defer stopCancel()
			if err := s.stopGameSessionEvents(stop, p); err != nil {
				t.Error(err)
			}
		})
	}

	// Peer 33 is in a dungeon: must not be enqueued or touch repository at all.
	peers[33].dungeon.mu.Lock()
	peers[33].dungeon.runtime = &runtimeDungeonState{}
	peers[33].dungeon.mu.Unlock()

	// Seed AP = 41 and fire first broadcast: peer 30 receives immediately with 1 HallView read (not 2).
	reg, err := guild.Load(ctx, repos.Settings)
	if err != nil {
		t.Fatal(err)
	}
	reg.Guilds[1].Essence.AP = 41
	if err := guild.Save(ctx, repos.Settings, reg, s.gameplayNow()); err != nil {
		t.Fatal(err)
	}
	counting.guildLoads.Store(0)

	s.queueCurrentGuildEssenceRefresh(1, 29)
	if err := s.callGameSession(ctx, peers[30], "drain-first", func() error { return nil }); err != nil {
		t.Fatal(err)
	}
	// Expect exactly 2 guild Loads total: 1 in queueCurrentGuildEssenceRefresh + 1 in peer 30's ReadHallView.
	// Before optimization this was 1 (queue) + 2 (peer 30 double-read) + 1 (peer 33 wasted in-dungeon read) = 4.
	if got := counting.guildLoads.Load(); got != 2 {
		t.Fatalf("first broadcast guild loads=%d want 2 (1 queue + 1 peer30 ReadHallView)", got)
	}
	body := guildReply(t, peers[30], 1297)
	if len(body) != 0x895 || binary.LittleEndian.Uint32(body[0x17:]) != 41 {
		t.Fatalf("peer 30 first AP=%d want 41", binary.LittleEndian.Uint32(body[0x17:]))
	}
	if peers[33].conn.(*bufferConn).write.Len() != 0 {
		t.Fatal("in-dungeon peer 33 received essence snapshot")
	}
	if peers[31].conn.(*bufferConn).write.Len() != 0 || peers[32].conn.(*bufferConn).write.Len() != 0 {
		t.Fatal("cross-guild peer received essence snapshot")
	}

	// Now burst 5 consecutive updates within the coalesce window: peer 30 should coalesce into 1 follow-up with AP=55.
	peers[30].conn.(*bufferConn).write.Reset()
	counting.guildLoads.Store(0)
	for ap := uint32(51); ap <= 55; ap++ {
		reg.Guilds[1].Essence.AP = ap
		if err := guild.Save(ctx, repos.Settings, reg, s.gameplayNow()); err != nil {
			t.Fatal(err)
		}
		s.queueCurrentGuildEssenceRefresh(1, 29)
	}
	// Wait past the 40ms coalesce window and drain peer 30's event loop.
	time.Sleep(90 * time.Millisecond)
	if err := s.callGameSession(ctx, peers[30], "drain-coalesced", func() error { return nil }); err != nil {
		t.Fatal(err)
	}
	// 5 calls to queueCurrentGuildEssenceRefresh (5 loads) + 1 coalesced peer30 execution (1 load) = 6 (vs 15 before).
	if got := counting.guildLoads.Load(); got != 6 {
		t.Fatalf("burst guild loads=%d want 6 (5 queue + 1 coalesced peer30)", got)
	}
	frames := splitUpperFrames(t, peers[30].conn.(*bufferConn).write.Bytes())
	count1297 := 0
	var lastAP uint32
	for _, f := range frames {
		if f.msgID == 1297 {
			count1297++
			lastAP = binary.LittleEndian.Uint32(f.body[0x17:])
		}
	}
	if count1297 != 1 || lastAP != 55 {
		t.Fatalf("coalesced 1297 count=%d lastAP=%d want count=1 lastAP=55", count1297, lastAP)
	}

	// Frozen practice fast-path: zero guild Loads when peer 30 has frozenPractice for its current identity.
	identity30, ok := s.boundGameSessionCharacterSnapshot(peers[30])
	if !ok {
		t.Fatal("peer 30 not bound")
	}
	peers[30].guild.placement.mu.Lock()
	peers[30].guild.placement.frozenPractice = identity30
	peers[30].guild.placement.mu.Unlock()
	peers[30].conn.(*bufferConn).write.Reset()
	counting.guildLoads.Store(0)
	if err := s.writeCurrentGuildEssenceSnapshotForGuild(peers[30], 1); err != nil {
		t.Fatal(err)
	}
	if got := counting.guildLoads.Load(); got != 0 {
		t.Fatalf("frozen practice performed %d guild loads, want 0", got)
	}
	if peers[30].conn.(*bufferConn).write.Len() != 0 {
		t.Fatal("frozen practice sent essence snapshot")
	}
	peers[30].guild.placement.mu.Lock()
	peers[30].guild.placement.frozenPractice = boundGameSessionCharacter{}
	peers[30].guild.placement.mu.Unlock()

	// Entering dungeon during coalesce timer cancels before DB read.
	peers[30].conn.(*bufferConn).write.Reset()
	counting.guildLoads.Store(0)
	s.queueCurrentGuildEssenceRefresh(1, 29) // executes immediately (elapsed >= 40ms)
	if err := s.callGameSession(ctx, peers[30], "drain-pre-dungeon", func() error { return nil }); err != nil {
		t.Fatal(err)
	}
	peers[30].conn.(*bufferConn).write.Reset()
	counting.guildLoads.Store(0)
	s.queueCurrentGuildEssenceRefresh(1, 29) // arms 40ms coalesce timer
	peers[30].dungeon.mu.Lock()
	peers[30].dungeon.runtime = &runtimeDungeonState{}
	peers[30].dungeon.mu.Unlock()
	time.Sleep(80 * time.Millisecond)
	if err := s.callGameSession(ctx, peers[30], "drain-post-dungeon", func() error { return nil }); err != nil {
		t.Fatal(err)
	}
	// Only the 1 queueCurrentGuildEssenceRefresh directory read should have occurred; peer 30 did 0 DB reads.
	if got := counting.guildLoads.Load(); got != 1 {
		t.Fatalf("dungeon-during-coalesce guild loads=%d want 1", got)
	}
	if peers[30].conn.(*bufferConn).write.Len() != 0 {
		t.Fatal("peer entering dungeon during coalesce still received snapshot")
	}

	// Reset/close clears pending essence token cleanly.
	peers[30].dungeon.mu.Lock()
	peers[30].dungeon.runtime = nil
	peers[30].dungeon.mu.Unlock()
	s.guildPresenceCoalesceDelay = 5 * time.Second
	s.queueCurrentGuildEssenceRefresh(1, 29)
	resetCurrentGuildPresenceRefresh(peers[30], true)
	peers[30].guild.mu.Lock()
	leftover := peers[30].guild.essenceRefresh
	peers[30].guild.mu.Unlock()
	if leftover != nil {
		t.Fatal("resetCurrentGuildPresenceRefresh left non-nil essenceRefresh token")
	}
}

func TestP3GuildDungeonPeerRefreshCoalesceSingleReadAndInDungeonDelivery(t *testing.T) {
	s, peers, repos := guildHallFixture(t)
	s.guildPresenceCoalesceDelay = 40 * time.Millisecond

	counting := &p3CountingSettingsRepo{base: repos.Settings}
	repos.Settings = counting
	s.repositoryProvider = func() (r.Group, bool) { return repos, true }

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	for _, id := range []uint16{30, 31, 32, 33} {
		p := peers[id]
		if err := s.startGameSessionEvents(ctx, p); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			stop, stopCancel := context.WithTimeout(context.Background(), time.Second)
			defer stopCancel()
			if err := s.stopGameSessionEvents(stop, p); err != nil {
				t.Error(err)
			}
		})
	}

	// Keep peer 33 in a dungeon: NOTI741 must still be delivered to in-dungeon peers!
	peers[33].dungeon.mu.Lock()
	peers[33].dungeon.runtime = &runtimeDungeonState{}
	peers[33].dungeon.mu.Unlock()

	reg, err := guild.Load(ctx, repos.Settings)
	if err != nil {
		t.Fatal(err)
	}
	cycle, err := reg.DungeonPolicy.Cycle(s.gameplayNow())
	if err != nil {
		t.Fatal(err)
	}
	reg.Guilds[1].Dungeon = &guild.DungeonProgress{CycleKey: cycle.Key, StartUnix: cycle.StartUnix, EndUnix: cycle.EndUnix, Counts: map[uint32]uint32{8500: 12}}
	if err := guild.Save(ctx, repos.Settings, reg, s.gameplayNow()); err != nil {
		t.Fatal(err)
	}
	counting.guildLoads.Store(0)

	s.queueCurrentGuildDungeonRefresh(1, 29)
	for _, id := range []uint16{30, 31, 32, 33} {
		if err := s.callGameSession(ctx, peers[id], "drain-first-dungeon", func() error { return nil }); err != nil {
			t.Fatal(err)
		}
	}
	// 1 queue + 1 peer30 + 1 peer33 = 3 guild loads (was 5 before optimization).
	if got := counting.guildLoads.Load(); got != 3 {
		t.Fatalf("first dungeon broadcast guild loads=%d want 3", got)
	}
	for _, id := range []uint16{30, 33} {
		body := guildReply(t, peers[id], 741)
		if len(body) != 25 || binary.LittleEndian.Uint32(body[13:17]) != 1 || binary.LittleEndian.Uint32(body[17:21]) != 8500 || binary.LittleEndian.Uint32(body[21:25]) != 12 {
			t.Fatalf("peer %d first 741 len=%d body=%x want dungeon 8500 count=12", id, len(body), body)
		}
	}
	if peers[31].conn.(*bufferConn).write.Len() != 0 || peers[32].conn.(*bufferConn).write.Len() != 0 {
		t.Fatal("cross-guild peer received NOTI741")
	}

	// Burst 4 updates within coalesce window: collapses into 1 follow-up with count=24.
	peers[30].conn.(*bufferConn).write.Reset()
	peers[33].conn.(*bufferConn).write.Reset()
	counting.guildLoads.Store(0)
	for c := uint32(21); c <= 24; c++ {
		reg.Guilds[1].Dungeon.Counts[8500] = c
		if err := guild.Save(ctx, repos.Settings, reg, s.gameplayNow()); err != nil {
			t.Fatal(err)
		}
		s.queueCurrentGuildDungeonRefresh(1, 29)
	}
	time.Sleep(90 * time.Millisecond)
	for _, id := range []uint16{30, 33} {
		if err := s.callGameSession(ctx, peers[id], "drain-coalesced-dungeon", func() error { return nil }); err != nil {
			t.Fatal(err)
		}
		frames := splitUpperFrames(t, peers[id].conn.(*bufferConn).write.Bytes())
		count741 := 0
		var lastCount uint32
		for _, f := range frames {
			if f.msgID == 741 {
				count741++
				if len(f.body) != 25 || binary.LittleEndian.Uint32(f.body[13:17]) != 1 || binary.LittleEndian.Uint32(f.body[17:21]) != 8500 {
					t.Fatalf("peer %d coalesced 741 malformed len=%d body=%x", id, len(f.body), f.body)
				}
				lastCount = binary.LittleEndian.Uint32(f.body[21:25])
			}
		}
		if count741 != 1 || lastCount != 24 {
			t.Fatalf("peer %d coalesced 741 count=%d last=%d want 1/24", id, count741, lastCount)
		}
	}
	// 4 queue + 1 peer30 + 1 peer33 = 6 loads (vs 20 before optimization).
	if got := counting.guildLoads.Load(); got != 6 {
		t.Fatalf("burst dungeon broadcast guild loads=%d want 6", got)
	}

	// Reset clears dungeonRefresh token cleanly.
	s.guildPresenceCoalesceDelay = 5 * time.Second
	s.queueCurrentGuildDungeonRefresh(1, 29)
	resetCurrentGuildPresenceRefresh(peers[30], true)
	peers[30].guild.mu.Lock()
	leftover := peers[30].guild.dungeonRefresh
	peers[30].guild.mu.Unlock()
	if leftover != nil {
		t.Fatal("resetCurrentGuildPresenceRefresh left non-nil dungeonRefresh token")
	}
}

type p3PerCallGuildCharReader struct {
	base   r.GuildCharacterReader
	onLoad func(ctx context.Context, ids []string)
}

func (c p3PerCallGuildCharReader) LoadGuildCharacters(ctx context.Context, ids []string) (map[string]r.GuildCharacter, error) {
	if c.onLoad != nil {
		c.onLoad(ctx, ids)
	}
	return c.base.LoadGuildCharacters(ctx, ids)
}

type p3ConcurrentVersionReadUOW struct {
	r.GuildUnitOfWork
	r.GuildReadUnitOfWork
	namespace  *r.GuildSnapshotNamespace
	onCharLoad func(ctx context.Context, ids []string)
}

func (u *p3ConcurrentVersionReadUOW) WithinGuildRead(ctx context.Context, fn func(r.GuildReadView) error) error {
	return u.GuildReadUnitOfWork.WithinGuildRead(ctx, func(v r.GuildReadView) error {
		v.Settings = presenceVersionSettings{v.Settings, u.namespace}
		if u.onCharLoad != nil {
			v.Characters = p3PerCallGuildCharReader{base: v.Characters, onLoad: u.onCharLoad}
		}
		return fn(v)
	})
}

func TestP3GuildPresenceNonHydratedMetaAndOnlineCountShare(t *testing.T) {
	s, peers, repos := guildRosterLargeFixture(t, 6)
	repos.Guild = &p3ConcurrentVersionReadUOW{
		GuildUnitOfWork:     repos.Guild,
		GuildReadUnitOfWork: repos.Guild.(r.GuildReadUnitOfWork),
		namespace:           &r.GuildSnapshotNamespace{},
	}
	s.repositoryProvider = func() (r.Group, bool) { return repos, true }
	shared := &guildPresenceReadCache{}

	sources := make([]boundGameSessionCharacter, 0, len(peers))
	for _, p := range peers {
		b, ok := s.boundGameSessionCharacterSnapshot(p)
		if !ok {
			t.Fatal("peer not bound")
		}
		sources = append(sources, b)
	}

	ctx := context.Background()
	g0, err := s.readCurrentGuildDisplayShared(ctx, sources[0], false, false, shared)
	if err != nil || g0 == nil {
		t.Fatalf("first non-hydrated read failed: g=%v err=%v", g0, err)
	}
	g1, err := s.readCurrentGuildDisplayShared(ctx, sources[1], false, false, shared)
	if err != nil || g1 == nil {
		t.Fatalf("second non-hydrated read failed: g=%v err=%v", g1, err)
	}
	if g0 != g1 {
		t.Fatal("non-hydrated peers on same revision did not reuse shared immutable metaGuild pointer")
	}
	wantOnline := len(s.currentGuildOnlineChannels(g0, nil))
	c0, t0 := shared.sharedOnlineCount(s, g0)
	c1, t1 := shared.sharedOnlineCount(s, g1)
	if c0 != wantOnline || c1 != wantOnline || !t0.Equal(t1) {
		t.Fatalf("sharedOnlineCount mismatch: got (%d,%v) (%d,%v) want %d", c0, t0, c1, t1, wantOnline)
	}

	// Concurrent stress across hydrated + non-hydrated peers sharing the same cache.
	var wg sync.WaitGroup
	errs := make(chan error, len(sources)*4)
	for round := 0; round < 4; round++ {
		for idx, src := range sources {
			src := src
			hydrate := idx%2 == 0
			wg.Add(1)
			go func() {
				defer wg.Done()
				g, err := s.readCurrentGuildDisplayShared(ctx, src, false, hydrate, shared)
				if err != nil || g == nil || g.ID != g0.ID {
					errs <- err
					return
				}
				cnt, _ := shared.sharedOnlineCount(s, g)
				if cnt != wantOnline {
					errs <- context.DeadlineExceeded
				}
			}()
		}
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatalf("concurrent shared read error: %v", err)
	}
}

func TestP3QuestMaterialCleanInboxFastPathAndConcurrentPrepareRace(t *testing.T) {
	t.Run("pvf_negative_kills_and_safety_interval", func(t *testing.T) {
		s, ps, repos, key := partyMaterialFixture(t, 1, 0, false, false, 100)
		// Replace quest catalog with a PVF-negative quest so kills do not run Prepare.
		source := bridgePVFSource{
			quest.DefaultList:       "1 `unrelated.qst`\n",
			"n_quest/unrelated.qst": "[type]\n`[seeking]`\n[monster reward item]\n9999 -1 -1 600001 1 100 100\n[/monster reward item]\n",
		}
		index, err := pvf.Build(context.Background(), source, pvf.BuildOptions{Lists: []string{quest.DefaultList}})
		if err != nil {
			t.Fatal(err)
		}
		s.questCatalog, err = quest.Load(context.Background(), index)
		if err != nil {
			t.Fatal(err)
		}
		counting := &p3CountingSettingsRepo{base: repos.Settings}
		repos.Settings = counting
		s.repositoryProvider = func() (r.Group, bool) { return repos, true }

		q := newFakeCurrentDungeonDeathTimerQueue()
		s.gameplayTimers = q
		p := ps[0]
		p.connID = "clean-inbox-test"
		if err := s.armQuestMaterialRecovery(p); err != nil {
			t.Fatal(err)
		}

		// First kill: session starts dirty, so recoverQuestMaterialsIfNeeded hits DB once and marks clean.
		if err := partyMaterialDeath(s, p, key); err != nil {
			t.Fatal(err)
		}
		if got := counting.questPendLoads.Load(); got != 1 {
			t.Fatalf("first kill pending loads=%d want 1", got)
		}

		// Subsequent clean calls (monster kill fast-path) hit DB 0 times.
		for i := 0; i < 5; i++ {
			if err := s.recoverQuestMaterialsIfNeeded(p, 0); err != nil {
				t.Fatal(err)
			}
		}
		if got := counting.questPendLoads.Load(); got != 1 {
			t.Fatalf("clean kill fast-path performed extra pending loads: got=%d want 1", got)
		}

		// 5s timer ticks within 60s safety interval hit DB 0 times.
		for tick := 0; tick < 5; tick++ {
			q.now = q.now.Add(5 * time.Second)
			q.fire(q.task(t, tick), false)
		}
		if got := counting.questPendLoads.Load(); got != 1 {
			t.Fatalf("timer ticks within safety window performed extra pending loads: got=%d want 1", got)
		}

		// Advance past 60s safety interval -> timer performs exactly 1 safety-net DB check.
		q.now = q.now.Add(40 * time.Second)
		q.fire(q.task(t, 5), false)
		if got := counting.questPendLoads.Load(); got != 2 {
			t.Fatalf("timer tick after 60s safety window loads=%d want 2", got)
		}

		// Character rebind marks dirty -> next call checks DB immediately.
		s.bindGameSessionCharacter(p, p.selectedCharacterID)
		if err := s.recoverQuestMaterialsIfNeeded(p, 0); err != nil {
			t.Fatal(err)
		}
		if got := counting.questPendLoads.Load(); got != 3 {
			t.Fatalf("after rebind loads=%d want 3", got)
		}
	})

	t.Run("concurrent_prepare_prevents_false_clean", func(t *testing.T) {
		s, ps, repos, key := partyMaterialFixture(t, 1, 0, false, false, 100)
		p := ps[0]
		// Confirm initially clean before Prepare starts.
		if err := s.recoverQuestMaterials(p); err != nil {
			t.Fatal(err)
		}
		syncUOW := &p3PrepareSyncUOW{
			base:    repos.QuestMaterialPlan,
			entered: make(chan struct{}),
			release: make(chan struct{}),
		}
		repos.QuestMaterialPlan = syncUOW
		s.repositoryProvider = func() (r.Group, bool) { return repos, true }

		prepare, err := s.prepareQuestMaterialDeath(p, p.dungeon.runtime, key)
		if err != nil || prepare == nil {
			t.Fatalf("prepareQuestMaterialDeath err=%v prepareNil=%t", err, prepare == nil)
		}

		prepDone := make(chan error, 1)
		go func() { prepDone <- prepare() }()

		// Wait until Prepare is inside its transaction (before commit).
		<-syncUOW.entered

		// Run Recover concurrently while Prepare is in-flight: DB still looks empty!
		if err := s.recoverQuestMaterials(p); err != nil {
			t.Fatal(err)
		}

		// Release Prepare so it commits the pending quest material drop.
		close(syncUOW.release)
		if err := <-prepDone; err != nil {
			t.Fatal(err)
		}

		// Because Prepare overlapped Recover, session MUST NOT be marked clean!
		// Calling recoverQuestMaterialsIfNeeded(p, 0) must hit DB and deliver item 600001.
		if err := s.recoverQuestMaterialsIfNeeded(p, 0); err != nil {
			t.Fatal(err)
		}
		inv, _, err := repos.Inventory.Load(context.Background(), strconv.Itoa(int(p.selectedCharacterID)))
		if err != nil {
			t.Fatal(err)
		}
		if got := currentQuestDropHeldCount(inv, 600001); got != 1 {
			t.Fatalf("item 600001 count=%d want 1 after concurrent Prepare/Recover", got)
		}
	})

	t.Run("late_recover_after_prepare_commit_cannot_mark_clean", func(t *testing.T) {
		s, ps, repos, key := partyMaterialFixture(t, 1, 0, false, false, 100)
		p := ps[0]
		readEmpty := make(chan struct{})
		releaseRecover := make(chan struct{})
		origSettings := repos.Settings
		pauseRepo := &p3PauseAfterEmptyPendingSettingsRepo{
			base:           origSettings,
			readEmpty:      readEmpty,
			releaseRecover: releaseRecover,
		}
		repos.Settings = pauseRepo
		s.repositoryProvider = func() (r.Group, bool) { return repos, true }

		// Start Recover; it reads empty pending from DB and pauses BEFORE returning
		// to markCleanIfUnchanged.
		recoverDone := make(chan error, 1)
		go func() { recoverDone <- s.recoverQuestMaterials(p) }()
		<-readEmpty

		// Run Prepare to FULL completion (beginPrepare -> commit -> endPrepare)
		// while the older Recover is still paused with its stale empty observation.
		repos.Settings = origSettings
		s.repositoryProvider = func() (r.Group, bool) { return repos, true }
		prepare, err := s.prepareQuestMaterialDeath(p, p.dungeon.runtime, key)
		if err != nil || prepare == nil {
			t.Fatalf("prepareQuestMaterialDeath err=%v prepareNil=%t", err, prepare == nil)
		}
		if err := prepare(); err != nil {
			t.Fatal(err)
		}

		// Now let the older Recover return and attempt markCleanIfUnchanged AFTER
		// endPrepare already completed. Epoch mismatch must reject marking clean.
		close(releaseRecover)
		if err := <-recoverDone; err != nil {
			t.Fatal(err)
		}

		if err := s.recoverQuestMaterialsIfNeeded(p, 0); err != nil {
			t.Fatal(err)
		}
		inv, _, err := repos.Inventory.Load(context.Background(), strconv.Itoa(int(p.selectedCharacterID)))
		if err != nil {
			t.Fatal(err)
		}
		if got := currentQuestDropHeldCount(inv, 600001); got != 1 {
			t.Fatalf("item 600001 count=%d want 1 after late-returning Recover", got)
		}
	})
}

type p3PauseAfterEmptyPendingSettingsRepo struct {
	base           r.SettingsRepository
	readEmpty      chan struct{}
	releaseRecover chan struct{}
	once           sync.Once
}

func (p *p3PauseAfterEmptyPendingSettingsRepo) Load(ctx context.Context, scope string) (r.SettingsRecord, bool, error) {
	rec, found, err := p.base.Load(ctx, scope)
	if strings.HasSuffix(scope, ":quest_material:pending") {
		p.once.Do(func() {
			close(p.readEmpty)
			<-p.releaseRecover
		})
	}
	return rec, found, err
}

func (p *p3PauseAfterEmptyPendingSettingsRepo) Save(ctx context.Context, record r.SettingsRecord) error {
	return p.base.Save(ctx, record)
}

func TestP3B1ConcurrentSameGuildSettlementBroadcastNoDeadlock(t *testing.T) {
	s, peers, _ := guildHallFixture(t)
	s.options.guildEssenceEnabled = true

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	for _, id := range []uint16{30, 33} {
		p := peers[id]
		if err := s.startGameSessionEvents(ctx, p); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			stop, stopCancel := context.WithTimeout(context.Background(), time.Second)
			defer stopCancel()
			if err := s.stopGameSessionEvents(stop, p); err != nil {
				t.Error(err)
			}
		})
	}

	peers[30].dungeon.runtime = &runtimeDungeonState{}
	peers[33].dungeon.runtime = &runtimeDungeonState{}

	// Both same-guild sessions hold their own dungeon.mu behind a barrier and
	// concurrently broadcast essence income (mirroring concurrent handleDungeonSetPlayResult).
	var locked sync.WaitGroup
	locked.Add(2)
	start := make(chan struct{})
	done := make(chan struct{}, 2)
	releaseLocks := make(chan struct{})

	runSender := func(self *gameSession, skip uint16) {
		self.dungeon.mu.Lock()
		locked.Done()
		<-start
		s.queueCurrentGuildEssenceRefresh(1, skip)
		done <- struct{}{}
		<-releaseLocks
		self.dungeon.mu.Unlock()
	}

	go runSender(peers[30], 30)
	go runSender(peers[33], 33)

	locked.Wait()
	close(start)

	for i := 0; i < 2; i++ {
		select {
		case <-done:
		case <-time.After(2 * time.Second):
			close(releaseLocks)
			t.Fatal("deadlock: concurrent same-guild essence broadcasts blocked on peer dungeon.mu")
		}
	}
	close(releaseLocks)

	for _, id := range []uint16{30, 33} {
		if err := s.callGameSession(ctx, peers[id], "drain-b1", func() error { return nil }); err != nil {
			t.Fatal(err)
		}
		if peers[id].conn.(*bufferConn).write.Len() != 0 {
			t.Fatalf("in-dungeon peer %d received unexpected 1297 bytes=%d", id, peers[id].conn.(*bufferConn).write.Len())
		}
	}
}

func TestP3P1WireWaitDepartureAndRebindDropStaleGuildSnapshots(t *testing.T) {
	t.Run("leave_during_wire_wait", func(t *testing.T) {
		s, peers, repos := guildHallFixture(t)
		s.options.guildEssenceEnabled = true
		p := peers[30]

		ctx := context.Background()
		reg, err := guild.Load(ctx, repos.Settings)
		if err != nil {
			t.Fatal(err)
		}
		reg.Guilds[1].Essence.AP = 444
		cycle, err := reg.DungeonPolicy.Cycle(s.gameplayNow())
		if err != nil {
			t.Fatal(err)
		}
		reg.Guilds[1].Dungeon = &guild.DungeonProgress{CycleKey: cycle.Key, StartUnix: cycle.StartUnix, EndUnix: cycle.EndUnix, Counts: map[uint32]uint32{8500: 9}}
		if err := guild.Save(ctx, repos.Settings, reg, s.gameplayNow()); err != nil {
			t.Fatal(err)
		}

		counting := &p3CountingSettingsRepo{base: repos.Settings}
		repos.Settings = counting
		s.repositoryProvider = func() (r.Group, bool) { return repos, true }

		// Deterministically wait until BOTH writers captured bound identity and
		// reached lockGameWire while wireMu is held, then remove peer 30 from guild 1.
		reached := make(chan string, 2)
		testBeforeGuildPeerWireLock = func(kind string) { reached <- kind }
		defer func() { testBeforeGuildPeerWireLock = nil }()

		p.wireMu.Lock()
		errCh := make(chan error, 2)
		go func() { errCh <- s.writeCurrentGuildEssenceSnapshotForGuild(p, 1) }()
		go func() { errCh <- s.writeCurrentGuildDungeonSnapshotForGuild(p, 1) }()
		<-reached
		<-reached
		if got := counting.guildLoads.Load(); got != 0 {
			p.wireMu.Unlock()
			t.Fatalf("expected 0 guild reads while blocked on wireMu, got %d", got)
		}

		delete(reg.Guilds[1].Members, "30")
		if err := guild.Save(ctx, repos.Settings, reg, s.gameplayNow()); err != nil {
			p.wireMu.Unlock()
			t.Fatal(err)
		}
		p.wireMu.Unlock()

		for i := 0; i < 2; i++ {
			if err := <-errCh; err != nil {
				t.Fatalf("snapshot write after wire wait returned err=%v", err)
			}
		}
		if got := counting.guildLoads.Load(); got != 2 {
			t.Fatalf("expected 2 post-wait authoritative guild reads, got %d", got)
		}
		if got := p.conn.(*bufferConn).write.Len(); got != 0 {
			t.Fatalf("departed member received %d stale wire bytes after wireMu wait", got)
		}
	})

	t.Run("rebind_during_wire_wait", func(t *testing.T) {
		s, peers, repos := guildHallFixture(t)
		s.options.guildEssenceEnabled = true
		p := peers[30]

		counting := &p3CountingSettingsRepo{base: repos.Settings}
		repos.Settings = counting
		s.repositoryProvider = func() (r.Group, bool) { return repos, true }

		reached := make(chan string, 2)
		testBeforeGuildPeerWireLock = func(kind string) { reached <- kind }
		defer func() { testBeforeGuildPeerWireLock = nil }()

		p.wireMu.Lock()
		errCh := make(chan error, 2)
		go func() { errCh <- s.writeCurrentGuildEssenceSnapshotForGuild(p, 1) }()
		go func() { errCh <- s.writeCurrentGuildDungeonSnapshotForGuild(p, 1) }()
		<-reached
		<-reached

		s.mu.Lock()
		p.characterGeneration++
		s.mu.Unlock()
		p.wireMu.Unlock()

		for i := 0; i < 2; i++ {
			if err := <-errCh; err != nil {
				t.Fatalf("rebound snapshot write returned err=%v", err)
			}
		}
		if got := counting.guildLoads.Load(); got != 0 {
			t.Fatalf("expected 0 guild reads after generation rebind, got %d", got)
		}
		if got := p.conn.(*bufferConn).write.Len(); got != 0 {
			t.Fatalf("rebound generation received %d stale wire bytes after wireMu wait", got)
		}
	})

	t.Run("repo_unavailable_returns_error", func(t *testing.T) {
		s, peers, _ := guildHallFixture(t)
		s.repositoryProvider = nil
		err := s.writeCurrentGuildDungeonSnapshotForGuild(peers[30], 1)
		if !errors.Is(err, guild.ErrRepositoryUnavailable) {
			t.Fatalf("expected ErrRepositoryUnavailable, got %v", err)
		}
	})
}

func TestP3P2OutOfOrderGuildSwitchPreservesNewGuildRefresh(t *testing.T) {
	s, peers, repos := guildHallFixture(t)
	s.options.guildEssenceEnabled = true
	s.guildPresenceCoalesceDelay = 40 * time.Millisecond

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	p := peers[30]
	if err := s.startGameSessionEvents(ctx, p); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		stop, stopCancel := context.WithTimeout(context.Background(), time.Second)
		defer stopCancel()
		if err := s.stopGameSessionEvents(stop, p); err != nil {
			t.Error(err)
		}
	})

	// Move peer 30 from guild 1 to guild 2 with distinct AP=777 and dungeon count=19.
	reg, err := guild.Load(ctx, repos.Settings)
	if err != nil {
		t.Fatal(err)
	}
	m := reg.Guilds[1].Members["30"]
	delete(reg.Guilds[1].Members, "30")
	reg.Guilds[2].Members["30"] = m
	reg.Guilds[2].Essence.AP = 777
	cycle, err := reg.DungeonPolicy.Cycle(s.gameplayNow())
	if err != nil {
		t.Fatal(err)
	}
	reg.Guilds[2].Dungeon = &guild.DungeonProgress{CycleKey: cycle.Key, StartUnix: cycle.StartUnix, EndUnix: cycle.EndUnix, Counts: map[uint32]uint32{8500: 19}}
	if err := guild.Save(ctx, repos.Settings, reg, s.gameplayNow()); err != nil {
		t.Fatal(err)
	}

	peer, ok := s.boundGameSessionCharacterSnapshot(p)
	if !ok {
		t.Fatal("peer 30 not bound")
	}
	// Deliver new guild 2 refresh first, then delayed old guild 1 refresh second.
	// Hold last refresh timestamps inside coalesce window so both coalesce into one token.
	now := s.guildRosterNow()
	p.guild.mu.Lock()
	p.guild.lastEssenceRefreshAt = now
	p.guild.lastDungeonRefreshAt = now
	p.guild.mu.Unlock()

	s.requestCurrentGuildEssenceRefresh(peer, 2)
	s.requestCurrentGuildEssenceRefresh(peer, 1)
	s.requestCurrentGuildDungeonRefresh(peer, 2)
	s.requestCurrentGuildDungeonRefresh(peer, 1)

	// Directly verify both requests coalesced into the same token preserving guild 2.
	p.guild.mu.Lock()
	if tok := p.guild.essenceRefresh; tok == nil || len(tok.guildIDs) != 2 || tok.guildIDs[0] != 2 || tok.guildIDs[1] != 1 {
		p.guild.mu.Unlock()
		t.Fatalf("essence token did not merge out-of-order guild IDs: %+v", tok)
	}
	if tok := p.guild.dungeonRefresh; tok == nil || len(tok.guildIDs) != 2 || tok.guildIDs[0] != 2 || tok.guildIDs[1] != 1 {
		p.guild.mu.Unlock()
		t.Fatalf("dungeon token did not merge out-of-order guild IDs: %+v", tok)
	}
	p.guild.mu.Unlock()

	time.Sleep(90 * time.Millisecond)
	if err := s.callGameSession(ctx, p, "drain-switch", func() error { return nil }); err != nil {
		t.Fatal(err)
	}
	frames := splitUpperFrames(t, p.conn.(*bufferConn).write.Bytes())
	var gotAP, gotDungeon uint32
	for _, f := range frames {
		if f.msgID == 1297 && len(f.body) >= 0x1b {
			gotAP = binary.LittleEndian.Uint32(f.body[0x17:0x1b])
		}
		if f.msgID == 741 && len(f.body) == 25 {
			gotDungeon = binary.LittleEndian.Uint32(f.body[21:25])
		}
	}
	if gotAP != 777 {
		t.Fatalf("delayed old-guild request dropped new-guild 1297: got AP=%d want 777", gotAP)
	}
	if gotDungeon != 19 {
		t.Fatalf("delayed old-guild request dropped new-guild 741: got count=%d want 19", gotDungeon)
	}
}

func TestP3P2ColdStartConcurrentMetaGuildConvergesOnSinglePointer(t *testing.T) {
	s, peers, repos := guildRosterLargeFixture(t, 6)
	var coldMissed sync.WaitGroup
	coldMissed.Add(len(peers))
	releasePublish := make(chan struct{})
	var charLoads atomic.Int32

	// Race-free per-call UOW wrapper that holds all 6 readers inside LoadGuildCharacters
	// (AFTER all 6 missed metaGuild and cloned their own RosterProjection, BEFORE any
	// reader publishes shared.metaGuild).
	repos.Guild = &p3ConcurrentVersionReadUOW{
		GuildUnitOfWork:     repos.Guild,
		GuildReadUnitOfWork: repos.Guild.(r.GuildReadUnitOfWork),
		namespace:           &r.GuildSnapshotNamespace{},
		onCharLoad: func(_ context.Context, ids []string) {
			if len(ids) == 1 && charLoads.Add(1) <= int32(len(peers)) {
				coldMissed.Done()
				<-releasePublish
			}
		},
	}
	s.repositoryProvider = func() (r.Group, bool) { return repos, true }

	sources := make([]boundGameSessionCharacter, 0, len(peers))
	for _, p := range peers {
		b, ok := s.boundGameSessionCharacterSnapshot(p)
		if !ok {
			t.Fatal("peer not bound")
		}
		sources = append(sources, b)
	}

	shared := &guildPresenceReadCache{}
	ptrs := make([]*guild.Guild, len(sources))
	errs := make([]error, len(sources))
	var wg sync.WaitGroup
	for i, src := range sources {
		i, src := i, src
		wg.Add(1)
		go func() {
			defer wg.Done()
			ptrs[i], errs[i] = s.readCurrentGuildDisplayShared(context.Background(), src, false, false, shared)
		}()
	}
	// Wait until ALL 6 readers have deterministically missed the cold cache.
	coldMissed.Wait()
	close(releasePublish)
	wg.Wait()

	for i := range sources {
		if errs[i] != nil || ptrs[i] == nil {
			t.Fatalf("cold-start reader %d failed: g=%v err=%v", i, ptrs[i], errs[i])
		}
		if ptrs[i] != ptrs[0] {
			t.Fatalf("cold-start reader %d returned distinct metaGuild pointer instead of converging on published pointer", i)
		}
	}

	// Verify Unix epoch (0) timestamp is cached validly without re-sampling.
	var fakeNow atomic.Int64
	fakeNow.Store(0)
	s.gameplayTimers = &processGameplayTimeQueue{now: func() time.Time { return time.Unix(fakeNow.Load(), 0).UTC() }}
	c0, t0 := shared.sharedOnlineCount(s, ptrs[0])
	if !t0.Equal(time.Unix(0, 0).UTC()) {
		t.Fatalf("expected Unix epoch sample time, got %v", t0)
	}
	fakeNow.Store(12345)
	c1, t1 := shared.sharedOnlineCount(s, ptrs[1])
	if c1 != c0 || !t1.Equal(time.Unix(0, 0).UTC()) {
		t.Fatalf("expected cached Unix epoch sample to be reused without resampling: c=(%d,%d) t=(%v,%v)", c0, c1, t0, t1)
	}
}

type p3SlowDelaySettingsRepo struct {
	base  r.SettingsRepository
	delay time.Duration
}

func (d *p3SlowDelaySettingsRepo) Load(ctx context.Context, scope string) (r.SettingsRecord, bool, error) {
	if scope == guild.RegistryScope && d.delay > 0 {
		time.Sleep(d.delay)
	}
	return d.base.Load(ctx, scope)
}

func (d *p3SlowDelaySettingsRepo) Save(ctx context.Context, record r.SettingsRecord) error {
	return d.base.Save(ctx, record)
}

type p3SlowDeadlineConn struct {
	*bufferConn
	mu         sync.Mutex
	deadline   time.Time
	lastBudget time.Duration
	writeDelay time.Duration
	closed     atomic.Bool
}

func (c *p3SlowDeadlineConn) SetWriteDeadline(t time.Time) error {
	c.mu.Lock()
	c.deadline = t
	if !t.IsZero() {
		c.lastBudget = time.Until(t)
	}
	c.mu.Unlock()
	return nil
}

func (c *p3SlowDeadlineConn) Write(p []byte) (int, error) {
	if c.writeDelay > 0 {
		time.Sleep(c.writeDelay)
	}
	c.mu.Lock()
	d := c.deadline
	c.mu.Unlock()
	if !d.IsZero() && time.Now().After(d) {
		return 0, context.DeadlineExceeded
	}
	return c.bufferConn.Write(p)
}

func (c *p3SlowDeadlineConn) Close() error {
	c.closed.Store(true)
	return c.bufferConn.Close()
}

func TestP3P1GuildPeerWriteUsesIndependentTransportBudget(t *testing.T) {
	s, peers, repos := guildHallFixture(t)
	s.options.guildEssenceEnabled = true
	p := peers[30]

	ctx := context.Background()
	reg, err := guild.Load(ctx, repos.Settings)
	if err != nil {
		t.Fatal(err)
	}
	reg.Guilds[1].Essence.AP = 888
	cycle, err := reg.DungeonPolicy.Cycle(s.gameplayNow())
	if err != nil {
		t.Fatal(err)
	}
	reg.Guilds[1].Dungeon = &guild.DungeonProgress{CycleKey: cycle.Key, StartUnix: cycle.StartUnix, EndUnix: cycle.EndUnix, Counts: map[uint32]uint32{8500: 7}}
	if err := guild.Save(ctx, repos.Settings, reg, s.gameplayNow()); err != nil {
		t.Fatal(err)
	}

	// Shrink storage/prep context to 70ms, spend 50ms in DB load (leaving ~20ms),
	// and spend 45ms in TCP Write (total 95ms > 70ms prep timeout). With an
	// independent 30s transport write budget, both writes must succeed without
	// closing the connection.
	testGuildContextTimeout = 70 * time.Millisecond
	defer func() { testGuildContextTimeout = 0 }()

	repos.Settings = &p3SlowDelaySettingsRepo{base: repos.Settings, delay: 50 * time.Millisecond}
	s.repositoryProvider = func() (r.Group, bool) { return repos, true }

	slowConn := &p3SlowDeadlineConn{bufferConn: p.conn.(*bufferConn), writeDelay: 45 * time.Millisecond}
	p.conn = slowConn

	if err := s.writeCurrentGuildEssenceSnapshotForGuild(p, 1); err != nil {
		t.Fatalf("essence write failed despite independent transport budget: %v", err)
	}
	slowConn.mu.Lock()
	essenceBudget := slowConn.lastBudget
	slowConn.mu.Unlock()
	if essenceBudget < 20*time.Second {
		t.Fatalf("essence write budget=%v want >= 20s independent transport budget", essenceBudget)
	}
	if slowConn.closed.Load() {
		t.Fatal("essence write closed healthy player connection")
	}

	slowConn.mu.Lock()
	slowConn.lastBudget = 0
	slowConn.mu.Unlock()

	if err := s.writeCurrentGuildDungeonSnapshotForGuild(p, 1); err != nil {
		t.Fatalf("dungeon write failed despite independent transport budget: %v", err)
	}
	slowConn.mu.Lock()
	dungeonBudget := slowConn.lastBudget
	slowConn.mu.Unlock()
	if dungeonBudget < 20*time.Second {
		t.Fatalf("dungeon write budget=%v want >= 20s independent transport budget", dungeonBudget)
	}
	if slowConn.closed.Load() {
		t.Fatal("dungeon write closed healthy player connection")
	}
	frames := splitUpperFrames(t, slowConn.bufferConn.write.Bytes())
	saw1297, saw741 := false, false
	for _, f := range frames {
		if f.msgID == 1297 && len(f.body) >= 0x1b && binary.LittleEndian.Uint32(f.body[0x17:0x1b]) == 888 {
			saw1297 = true
		}
		if f.msgID == 741 && len(f.body) == 25 && binary.LittleEndian.Uint32(f.body[21:25]) == 7 {
			saw741 = true
		}
	}
	if !saw1297 || !saw741 {
		t.Fatalf("missing expected frames after near-timeout prep: saw1297=%v saw741=%v", saw1297, saw741)
	}
}

func TestP3P2Mode8CacheInvalidationDuringFinalValidationDropsFrame(t *testing.T) {
	f, repos, viewer1Rec := setupP1WeddingGuildRosterFixture(t)
	s := f.service
	ctx := context.Background()

	viewer1Rec.Stats["wedding_v1_phase"] = 0
	if err := r.SaveCharacterFields(ctx, repos.Character, viewer1Rec, r.CharacterFieldWedding); err != nil {
		t.Fatal(err)
	}
	f.target.channel.ID, f.target.connectionTownActorOwnerChannel = 11, 11

	reg, err := guild.Load(ctx, repos.Settings)
	if err != nil {
		t.Fatal(err)
	}
	g, _, ok := reg.GuildForCharacter("17")
	if !ok {
		t.Fatal("missing guild for actor 17")
	}
	actor17 := s.gameSessions[17]
	access17 := p2GuildMemberAccessForTest(t, s, f.target, actor17, g.ID)

	// Invalidate the lightweight cache during the final
	// validateCurrentGuildMemberProfileAccess DB read. Lightweight mode8 has no
	// name-tag dynamic expiry, but the send-time guard must still drop this frame.
	origCharRepo := repos.Character
	var invalidated atomic.Bool
	repos.Character = &p3InvalidatingMirrorRepo{
		CharacterRepository: origCharRepo,
		onMirror: func(id string) {
			if id == "17" {
				invalidated.Store(true)
			}
		},
	}
	s.repositoryProvider = func() (r.Group, bool) { return repos, true }

	f.targetConn.write.Reset()
	frames := []currentGuildMemberProfileFrame{{opcode: 2, body: []byte{8, 1, 2, 3}, class: 0}}
	written, closed, err := s.sendCurrentGuildMemberProfileFramesDeliveredWithWriteCtxAndGuard(ctx, ctx, access17, actor17.crossSceneAppearanceRevision, frames, func() bool {
		return !invalidated.Load()
	})
	if err != nil || written || closed {
		t.Fatalf("expected invalidated frame drop: written=%v closed=%v err=%v", written, closed, err)
	}
	if got := f.targetConn.write.Len(); got != 0 {
		t.Fatalf("expected 0 wire bytes for mode8 invalidated during final DB validation, got %d", got)
	}
}
