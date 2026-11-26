package dnfbridge

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"longheng.io/server/internal/modules/dnf/dnfenum"
	dnfguild "longheng.io/server/internal/modules/dnf/guild"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

type p2HookCharacterRepo struct {
	dnfrepo.CharacterRepository
	onLoad     func(ctx context.Context, id string) (bool, error)
	onIdentity func(ctx context.Context, id string) (dnfrepo.CharacterIdentity, bool, bool, error)
}

func (r *p2HookCharacterRepo) Load(ctx context.Context, id string) (dnfrepo.CharacterRecord, bool, error) {
	if r.onLoad != nil {
		if intercepted, err := r.onLoad(ctx, id); intercepted {
			return dnfrepo.CharacterRecord{}, false, err
		}
	}
	return r.CharacterRepository.Load(ctx, id)
}

func (r *p2HookCharacterRepo) LoadCharacterIdentity(ctx context.Context, id string) (dnfrepo.CharacterIdentity, bool, error) {
	if r.onIdentity != nil {
		identity, found, intercepted, err := r.onIdentity(ctx, id)
		if intercepted {
			return identity, found, err
		}
	}
	return dnfrepo.LoadCharacterIdentity(ctx, r.CharacterRepository, id)
}

func p2GuildMemberAccessForTest(t *testing.T, s *Service, viewer, actor *gameSession, guildID uint32) currentGuildMemberProfileAccess {
	t.Helper()
	vBound, ok := s.boundGameSessionCharacterSnapshot(viewer)
	if !ok {
		t.Fatal("viewer is not bound")
	}
	aBound, ok := s.boundGameSessionCharacterSnapshot(actor)
	if !ok {
		t.Fatal("actor is not bound")
	}
	return currentGuildMemberProfileAccess{
		viewer:        vBound,
		actor:         aBound,
		viewerAccount: s.accountIDForSession(viewer),
		actorAccount:  s.accountIDForSession(actor),
		viewerChannel: currentTownPresenceChannelID(viewer),
		request: currentGuildMemberProfileRequest{
			channel:   byte(currentTownPresenceChannelID(actor)),
			character: aBound.character,
			directory: s.currentSceneActorRoute(),
		},
		guildID: guildID,
	}
}

func p2Mode8ContainsUint32LE(body []byte, value uint32) bool {
	var needle [4]byte
	binary.LittleEndian.PutUint32(needle[:], value)
	return bytes.Contains(body, needle[:])
}

func TestP2GuildMode8InvalidateRetiresInflightBuildAndDropsStalePublish(t *testing.T) {
	f, repos, viewer1Rec := setupP1WeddingGuildRosterFixture(t)
	s := f.service
	ctx := context.Background()

	viewer1Rec.Stats["wedding_v1_phase"] = 0
	if err := dnfrepo.SaveCharacterFields(ctx, repos.Character, viewer1Rec, dnfrepo.CharacterFieldWedding); err != nil {
		t.Fatal(err)
	}
	f.target.channel.ID, f.target.connectionTownActorOwnerChannel = 11, 11
	viewer2, _ := addP1GuildOnlinePeer(t, s, repos, viewer1Rec, 31, "31", "dnf:3", "viewer2", 38, 1)

	reg, err := dnfguild.Load(ctx, repos.Settings)
	if err != nil {
		t.Fatal(err)
	}
	g, _, ok := reg.GuildForCharacter("17")
	if !ok {
		t.Fatal("missing guild for actor 17")
	}
	actor17 := s.gameSessions[17]
	if actor17 == nil {
		t.Fatal("missing online session for actor 17")
	}

	origCharRepo := repos.Character
	staleRec, found, err := origCharRepo.Load(ctx, "17")
	if err != nil || !found {
		t.Fatalf("load actor 17: found=%v err=%v", found, err)
	}
	staleRec.Name = "stale17"
	if err := origCharRepo.Save(ctx, staleRec); err != nil {
		t.Fatal(err)
	}

	enteredFirst := make(chan struct{})
	releaseFirst := make(chan struct{})
	var loads atomic.Int32
	repos.Character = &p1HookCharRepo{
		CharacterRepository: origCharRepo,
		onLoad: func(loadCtx context.Context, id string) (bool, error) {
			if id == "17" && loads.Add(1) == 1 {
				close(enteredFirst)
				<-releaseFirst
			}
			return false, nil
		},
	}
	s.repositoryProvider = func() (dnfrepo.Group, bool) { return repos, true }

	access1 := p2GuildMemberAccessForTest(t, s, f.target, actor17, g.ID)
	access2 := p2GuildMemberAccessForTest(t, s, viewer2, actor17, g.ID)

	type buildRes struct {
		body []byte
		ok   bool
		err  error
	}
	ch1 := make(chan buildRes, 1)
	ch2 := make(chan buildRes, 1)

	go func() {
		body, _, _, _, ok, err := s.loadOrBuildGuildCacheOnlyMode8(ctx, access1, access1.request.channel)
		ch1 <- buildRes{body: body, ok: ok, err: err}
	}()
	<-enteredFirst

	// Start a second concurrent caller that joins the active flight.
	go func() {
		body, _, _, _, ok, err := s.loadOrBuildGuildCacheOnlyMode8(ctx, access2, access2.request.channel)
		ch2 <- buildRes{body: body, ok: ok, err: err}
	}()
	// Give viewer2 a brief moment to attach to the in-flight entry before invalidating.
	time.Sleep(15 * time.Millisecond)

	// Update character 17 to fresh name and invalidate cache while flight #1 is still in DB.
	freshRec := dnfrepo.CloneCharacter(staleRec)
	freshRec.Name = "fresh17"
	if err := origCharRepo.Save(ctx, freshRec); err != nil {
		t.Fatal(err)
	}
	s.onlinePlayers.InvalidateMode0Cache(17)

	s.onlinePlayers.guildMode8.mu.Lock()
	inflightAfterInvalidate := len(s.onlinePlayers.guildMode8.inflight)
	s.onlinePlayers.guildMode8.mu.Unlock()
	if inflightAfterInvalidate != 0 {
		t.Fatalf("InvalidateMode0Cache must retire and remove in-flight builds, got %d remaining", inflightAfterInvalidate)
	}

	close(releaseFirst)

	res1 := <-ch1
	res2 := <-ch2
	if res1.err != nil || !res1.ok || res2.err != nil || !res2.ok {
		t.Fatalf("expected both callers to rebuild fresh cleanly: res1=(ok=%v err=%v) res2=(ok=%v err=%v)", res1.ok, res1.err, res2.ok, res2.err)
	}
	if !bytes.Contains(res1.body, []byte("fresh17")) || bytes.Contains(res1.body, []byte("stale17")) {
		t.Fatalf("builder received stale pre-invalidation bytes")
	}
	if !bytes.Contains(res2.body, []byte("fresh17")) || bytes.Contains(res2.body, []byte("stale17")) {
		t.Fatalf("waiter received stale pre-invalidation bytes")
	}
	if loads.Load() < 2 {
		t.Fatalf("expected at least 2 Character.Load calls (retired + rebuilt), got %d", loads.Load())
	}

	// Send-time invalidation guard: if InvalidateMode0Cache runs while sender waits on wireMu,
	// the stale cached frame is dropped before writing any bytes to the socket.
	f.targetConn.write.Reset()
	f.target.wireMu.Lock()
	sendDone := make(chan struct {
		delivered bool
		err       error
	}, 1)
	go func() {
		delivered, _, _, _, sendErr := s.sendCurrentGuildMemberProfileWithPolicyDeliveredWithWriteCtx(ctx, ctx, access1, true)
		sendDone <- struct {
			delivered bool
			err       error
		}{delivered: delivered, err: sendErr}
	}()
	time.Sleep(15 * time.Millisecond)
	s.onlinePlayers.InvalidateMode0Cache(17)
	f.target.wireMu.Unlock()

	sendRes := <-sendDone
	if sendRes.err != nil || sendRes.delivered {
		t.Fatalf("invalidated frame must be dropped before socket write, got delivered=%v err=%v", sendRes.delivered, sendRes.err)
	}
	if f.targetConn.write.Len() != 0 {
		t.Fatalf("expected 0 bytes written after send-time invalidation, got %d", f.targetConn.write.Len())
	}
}

func TestP2GuildMode8LightweightTTLAndViewerDedupeDirectoryTTL(t *testing.T) {
	f, repos, viewer1Rec := setupP1WeddingGuildRosterFixture(t)
	s := f.service
	ctx := context.Background()

	viewer1Rec.Stats["wedding_v1_phase"] = 0
	if err := dnfrepo.SaveCharacterFields(ctx, repos.Character, viewer1Rec, dnfrepo.CharacterFieldWedding); err != nil {
		t.Fatal(err)
	}
	f.target.channel.ID, f.target.connectionTownActorOwnerChannel = 11, 11
	viewer2, viewer2Conn := addP1GuildOnlinePeer(t, s, repos, viewer1Rec, 31, "31", "dnf:3", "viewer2", 38, 1)
	reg, err := dnfguild.Load(ctx, repos.Settings)
	if err != nil {
		t.Fatal(err)
	}
	g, _, ok := reg.GuildForCharacter("17")
	if !ok {
		t.Fatal("missing guild for actor 17")
	}
	actor17 := s.gameSessions[17]
	access17 := p2GuildMemberAccessForTest(t, s, f.target, actor17, g.ID)

	const (
		t0            int64  = 1_900_000_000
		nameTagItemID uint32 = 490001234
	)
	var fakeNowUnix atomic.Int64
	fakeNowUnix.Store(t0)

	origExpireFn := currentNameTagExpireNowFn
	currentNameTagExpireNowFn = func() int64 { return fakeNowUnix.Load() }
	t.Cleanup(func() { currentNameTagExpireNowFn = origExpireFn })

	s.onlinePlayers.guildMode8.nowFn = func() time.Time { return time.Unix(fakeNowUnix.Load(), 0).UTC() }
	t.Cleanup(func() { s.onlinePlayers.guildMode8.nowFn = nil })

	// Seal heavy inventory state that lightweight mode8 must continue to ignore.
	if err := repos.Equipment.Save(ctx, dnfrepo.EquipmentRecord{
		CharacterID: "17",
		Entries: map[string]dnfrepo.EquipmentEntry{
			"12": {SlotIndex: 12, ItemID: 10001},
		},
	}); err != nil {
		t.Fatal(err)
	}
	c17, found, err := repos.Character.Load(ctx, "17")
	if err != nil || !found {
		t.Fatalf("load 17: found=%v err=%v", found, err)
	}
	if c17.Stats == nil {
		c17.Stats = map[string]int64{}
	}
	c17.Stats["name_tag_item_id"] = int64(nameTagItemID)
	c17.Stats["name_tag_expire_time"] = t0 + 3
	if err := repos.Character.Save(ctx, c17); err != nil {
		t.Fatal(err)
	}

	charCounter := &p0CountingCharRepo{CharacterRepository: repos.Character}
	repos.Character = charCounter
	s.repositoryProvider = func() (dnfrepo.Group, bool) { return repos, true }

	// At t=t0: viewer 1 receives lightweight mode8. The omitted name-tag payload
	// leaves no dynamic expiry to clamp, so both layers use their normal TTLs.
	f.targetConn.write.Reset()
	if err := s.handleCurrentGuildMemberListForOpcode(f.target, uint16(dnfenum.CmdPacketGuildAllMemberList)); err != nil {
		t.Fatal(err)
	}
	if p2Mode8ContainsUint32LE(f.targetConn.write.Bytes(), nameTagItemID) {
		t.Fatalf("lightweight mode8 unexpectedly carried name-tag item %d", nameTagItemID)
	}
	if got := countSilentMode8Frames(t, f.targetConn.write.Bytes()); got == 0 {
		t.Fatal("expected at least one lightweight mode8 prime")
	}
	loadsAfterInitial := charCounter.fullLoadsFor("17")
	if loadsAfterInitial == 0 {
		t.Fatalf("expected cold Character.Load for actor 17, got 0")
	}
	wantCacheExpiry := time.Unix(t0, 0).UTC().Add(guildMode8CacheTTL)
	wantViewerExpiry := time.Unix(t0, 0).UTC().Add(guildPrimedPeerTTL)
	f.target.guild.mu.Lock()
	primed17 := f.target.guild.weddingPrimedPeers[17]
	f.target.guild.mu.Unlock()
	if !primed17.expiresAt.Equal(wantViewerExpiry) {
		t.Fatalf("viewer dedupe expiry = %v, want %v", primed17.expiresAt, wantViewerExpiry)
	}
	s.onlinePlayers.guildMode8.mu.Lock()
	var cachedBody []byte
	for k, entry := range s.onlinePlayers.guildMode8.entries {
		if k.characterID == 17 {
			cachedBody = append([]byte(nil), entry.body...)
			if !entry.expiresAt.Equal(wantCacheExpiry) {
				s.onlinePlayers.guildMode8.mu.Unlock()
				t.Fatalf("guildMode8Cache expiry = %v, want %v", entry.expiresAt, wantCacheExpiry)
			}
			if !entry.dynamicExpiresAt.IsZero() {
				s.onlinePlayers.guildMode8.mu.Unlock()
				t.Fatalf("lightweight mode8 dynamic expiry = %v, want zero", entry.dynamicExpiresAt)
			}
		}
	}
	s.onlinePlayers.guildMode8.mu.Unlock()
	if len(cachedBody) == 0 {
		t.Fatal("expected cached mode8 entry for actor 17")
	}
	if p2Mode8ContainsUint32LE(cachedBody, nameTagItemID) {
		t.Fatalf("cached lightweight mode8 unexpectedly carried name-tag item %d", nameTagItemID)
	}

	// At t=t0+2s (still before expiry): viewer 2 hits guildMode8Cache (0 extra DB load),
	// and viewer 1 hits viewer dedupe (0 extra mode8 frames).
	fakeNowUnix.Store(t0 + 2)
	viewer2Conn.write.Reset()
	if err := s.handleCurrentGuildMemberListForOpcode(viewer2, uint16(dnfenum.CmdPacketGuildAllMemberList)); err != nil {
		t.Fatal(err)
	}
	if charCounter.fullLoadsFor("17") != loadsAfterInitial {
		t.Fatalf("expected cache hit at t0+2s (0 extra loads), got %d want %d", charCounter.fullLoadsFor("17"), loadsAfterInitial)
	}
	f.targetConn.write.Reset()
	if err := s.handleCurrentGuildMemberListForOpcode(f.target, uint16(dnfenum.CmdPacketGuildAllMemberList)); err != nil {
		t.Fatal(err)
	}
	if got := countSilentMode8Frames(t, f.targetConn.write.Bytes()); got != 0 {
		t.Fatalf("expected viewer dedupe hit at t0+2s, got %d mode8 frames", got)
	}

	// At t=t0+3s both the lightweight cache and the viewer dedupe are still valid.
	fakeNowUnix.Store(t0 + 3)
	f.targetConn.write.Reset()
	if err := s.handleCurrentGuildMemberListForOpcode(f.target, uint16(dnfenum.CmdPacketGuildAllMemberList)); err != nil {
		t.Fatal(err)
	}
	if charCounter.fullLoadsFor("17") != loadsAfterInitial {
		t.Fatalf("mode8 rebuilt before TTL expiry: got loads=%d (before=%d)", charCounter.fullLoadsFor("17"), loadsAfterInitial)
	}
	if got := countSilentMode8Frames(t, f.targetConn.write.Bytes()); got != 0 {
		t.Fatalf("expected viewer dedupe hit before TTL, got %d mode8 frames", got)
	}

	// At the exact 10s cache TTL, a direct rebuild must bypass the old entry and
	// still omit the name tag. This avoids using viewer dedupe as a false cache hit.
	fakeNowUnix.Store(t0 + int64(guildMode8CacheTTL/time.Second))
	rebuilt, _, _, dynamicExpiry, ok, err := s.loadOrBuildGuildCacheOnlyMode8(ctx, access17, access17.request.channel)
	if err != nil || !ok {
		t.Fatalf("rebuild at cache TTL failed: ok=%v err=%v", ok, err)
	}
	if !dynamicExpiry.IsZero() {
		t.Fatalf("lightweight rebuild dynamic expiry = %v, want zero", dynamicExpiry)
	}
	if p2Mode8ContainsUint32LE(rebuilt, nameTagItemID) {
		t.Fatalf("rebuilt lightweight mode8 unexpectedly carried name-tag item %d", nameTagItemID)
	}
	if charCounter.fullLoadsFor("17") <= loadsAfterInitial {
		t.Fatalf("expected rebuild at exact cache TTL, got loads=%d (before=%d)", charCounter.fullLoadsFor("17"), loadsAfterInitial)
	}

	// Verify weddingPrimedPeer matches() requires directory equality and respects TTL.
	basePrime := weddingPrimedPeer{
		viewerChar:    29,
		viewerGen:     1,
		actorSession:  s.gameSessions[17],
		actorGen:      1,
		actorChannel:  11,
		directory:     0,
		guildID:       1,
		appearanceRev: 0,
		expiresAt:     time.Unix(t0+63, 0).UTC(),
	}
	if !basePrime.matches(basePrime, time.Unix(t0+62, 0).UTC()) {
		t.Fatal("expected matching prime before TTL")
	}
	diffDir := basePrime
	diffDir.directory = 2
	if basePrime.matches(diffDir, time.Unix(t0+62, 0).UTC()) {
		t.Fatal("expected directory mismatch to invalidate viewer dedupe")
	}
	if basePrime.matches(basePrime, time.Unix(t0+63, 0).UTC()) {
		t.Fatal("expected TTL expiry to invalidate viewer dedupe")
	}

	// End-to-end viewer dedupe TTL expiry: advance past guildPrimedPeerTTL.
	fakeNowUnix.Store(t0 + 3 + int64(guildPrimedPeerTTL/time.Second) + 1)
	f.targetConn.write.Reset()
	if err := s.handleCurrentGuildMemberListForOpcode(f.target, uint16(dnfenum.CmdPacketGuildAllMemberList)); err != nil {
		t.Fatal(err)
	}
	if got := countSilentMode8Frames(t, f.targetConn.write.Bytes()); got != 2 {
		t.Fatalf("expected both peers (17 and 31) to re-prime after guildPrimedPeerTTL expiry, got %d mode8 frames", got)
	}
}

func TestP2GuildMode8AtomicColdStartCoalescingAndInflightCap(t *testing.T) {
	f, repos, viewer1Rec := setupP1WeddingGuildRosterFixture(t)
	s := f.service
	ctx := context.Background()

	viewer1Rec.Stats["wedding_v1_phase"] = 0
	if err := dnfrepo.SaveCharacterFields(ctx, repos.Character, viewer1Rec, dnfrepo.CharacterFieldWedding); err != nil {
		t.Fatal(err)
	}
	f.target.channel.ID, f.target.connectionTownActorOwnerChannel = 11, 11
	peer31, _ := addP1GuildOnlinePeer(t, s, repos, viewer1Rec, 31, "31", "dnf:3", "peer31", 38, 1)
	peer33, _ := addP1GuildOnlinePeer(t, s, repos, viewer1Rec, 33, "33", "dnf:4", "peer33", 38, 1)

	reg, err := dnfguild.Load(ctx, repos.Settings)
	if err != nil {
		t.Fatal(err)
	}
	g, _, ok := reg.GuildForCharacter("17")
	if !ok {
		t.Fatal("missing guild for actor 17")
	}
	actor17 := s.gameSessions[17]

	// Part 1: 16 concurrent cold-start callers for actor 17 coalesce into exactly 1 DB load.
	origCharRepo := repos.Character
	var char17Loads atomic.Int32
	firstEntered := make(chan struct{})
	release17 := make(chan struct{})
	repos.Character = &p1HookCharRepo{
		CharacterRepository: origCharRepo,
		onLoad: func(loadCtx context.Context, id string) (bool, error) {
			if id == "17" {
				if char17Loads.Add(1) == 1 {
					close(firstEntered)
					<-release17
				}
			}
			return false, nil
		},
	}
	s.repositoryProvider = func() (dnfrepo.Group, bool) { return repos, true }

	access17 := p2GuildMemberAccessForTest(t, s, f.target, actor17, g.ID)
	const callers = 16
	var wg sync.WaitGroup
	errs := make(chan error, callers)
	for i := 0; i < callers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			body, _, _, _, builtOK, loadErr := s.loadOrBuildGuildCacheOnlyMode8(ctx, access17, access17.request.channel)
			if loadErr != nil {
				errs <- loadErr
				return
			}
			if !builtOK || len(body) == 0 {
				errs <- errors.New("empty mode8 build")
			}
		}()
	}
	<-firstEntered
	time.Sleep(15 * time.Millisecond)
	close(release17)
	wg.Wait()
	close(errs)
	for e := range errs {
		t.Fatalf("concurrent cold build failed: %v", e)
	}
	if got := char17Loads.Load(); got != 1 {
		t.Fatalf("expected atomic lookup+inflight coalescing to perform exactly 1 cold build, got %d", got)
	}

	// Part 2: inflight map respects guildMode8CacheMaxInflight cap while excess callers bypass cleanly.
	s.onlinePlayers.InvalidateMode0Cache(17)
	origMaxInflight := guildMode8CacheMaxInflight
	guildMode8CacheMaxInflight = 2
	t.Cleanup(func() { guildMode8CacheMaxInflight = origMaxInflight })

	enter17 := make(chan struct{})
	enter31 := make(chan struct{})
	releaseCap := make(chan struct{})
	var seen17, seen31 atomic.Bool
	var maxObservedInflight atomic.Int32

	repos.Character = &p1HookCharRepo{
		CharacterRepository: origCharRepo,
		onLoad: func(loadCtx context.Context, id string) (bool, error) {
			s.onlinePlayers.guildMode8.mu.Lock()
			n := int32(len(s.onlinePlayers.guildMode8.inflight))
			s.onlinePlayers.guildMode8.mu.Unlock()
			for {
				prev := maxObservedInflight.Load()
				if n <= prev || maxObservedInflight.CompareAndSwap(prev, n) {
					break
				}
			}
			switch id {
			case "17":
				if seen17.CompareAndSwap(false, true) {
					close(enter17)
					<-releaseCap
				}
			case "31":
				if seen31.CompareAndSwap(false, true) {
					close(enter31)
					<-releaseCap
				}
			}
			return false, nil
		},
	}
	s.repositoryProvider = func() (dnfrepo.Group, bool) { return repos, true }

	access31 := p2GuildMemberAccessForTest(t, s, f.target, peer31, g.ID)
	access33 := p2GuildMemberAccessForTest(t, s, f.target, peer33, g.ID)

	var capWG sync.WaitGroup
	capWG.Add(2)
	go func() {
		defer capWG.Done()
		_, _, _, _, _, _ = s.loadOrBuildGuildCacheOnlyMode8(ctx, access17, access17.request.channel)
	}()
	go func() {
		defer capWG.Done()
		_, _, _, _, _, _ = s.loadOrBuildGuildCacheOnlyMode8(ctx, access31, access31.request.channel)
	}()
	<-enter17
	<-enter31

	// 3rd distinct cold key while inflight == 2 must take bypass path and still succeed.
	body33, _, _, _, ok33, err33 := s.loadOrBuildGuildCacheOnlyMode8(ctx, access33, access33.request.channel)
	if err33 != nil || !ok33 || len(body33) == 0 {
		t.Fatalf("bypass build for peer 33 failed: ok=%v len=%d err=%v", ok33, len(body33), err33)
	}
	close(releaseCap)
	capWG.Wait()

	if maxObserved := maxObservedInflight.Load(); maxObserved > 2 {
		t.Fatalf("inflight map exceeded cap 2, observed %d", maxObserved)
	}
}

func TestP2GuildMode8LightweightBuildFailuresAreNotCached(t *testing.T) {
	t.Run("character_load_failure_not_cached", func(t *testing.T) {
		f, repos, viewer1Rec := setupP1WeddingGuildRosterFixture(t)
		s := f.service
		ctx := context.Background()
		viewer1Rec.Stats["wedding_v1_phase"] = 0
		_ = dnfrepo.SaveCharacterFields(ctx, repos.Character, viewer1Rec, dnfrepo.CharacterFieldWedding)
		f.target.channel.ID, f.target.connectionTownActorOwnerChannel = 11, 11
		reg, _ := dnfguild.Load(ctx, repos.Settings)
		g, _, _ := reg.GuildForCharacter("17")
		access := p2GuildMemberAccessForTest(t, s, f.target, s.gameSessions[17], g.ID)

		injectedErr := errors.New("lightweight character load transient db failure")
		var failLoad atomic.Bool
		failLoad.Store(true)
		repos.Character = &p2HookCharacterRepo{
			CharacterRepository: repos.Character,
			onLoad: func(ctx context.Context, id string) (bool, error) {
				if failLoad.Load() && id == "17" {
					return true, injectedErr
				}
				return false, nil
			},
		}
		s.repositoryProvider = func() (dnfrepo.Group, bool) { return repos, true }

		if _, _, _, _, ok, err := s.loadOrBuildGuildCacheOnlyMode8(ctx, access, access.request.channel); !errors.Is(err, injectedErr) || ok {
			t.Fatalf("expected lightweight character load error %v, got ok=%v err=%v", injectedErr, ok, err)
		}
		s.onlinePlayers.guildMode8.mu.Lock()
		cached := len(s.onlinePlayers.guildMode8.entries)
		s.onlinePlayers.guildMode8.mu.Unlock()
		if cached != 0 {
			t.Fatalf("failed lightweight build must never populate guildMode8Cache, got %d entries", cached)
		}

		// Recovery: once DB is healthy, build succeeds and caches.
		failLoad.Store(false)
		if body, _, _, _, ok, err := s.loadOrBuildGuildCacheOnlyMode8(ctx, access, access.request.channel); err != nil || !ok || len(body) == 0 {
			t.Fatalf("healthy rebuild failed: ok=%v len=%d err=%v", ok, len(body), err)
		}
	})

	t.Run("guild_projection_failure_not_cached", func(t *testing.T) {
		f, repos, viewer1Rec := setupP1WeddingGuildRosterFixture(t)
		s := f.service
		ctx := context.Background()
		viewer1Rec.Stats["wedding_v1_phase"] = 0
		_ = dnfrepo.SaveCharacterFields(ctx, repos.Character, viewer1Rec, dnfrepo.CharacterFieldWedding)
		f.target.channel.ID, f.target.connectionTownActorOwnerChannel = 11, 11
		reg, _ := dnfguild.Load(ctx, repos.Settings)
		g, _, _ := reg.GuildForCharacter("17")
		access := p2GuildMemberAccessForTest(t, s, f.target, s.gameSessions[17], g.ID)

		injectedErr := errors.New("lightweight guild projection transient timeout")
		var failProjection atomic.Bool
		failProjection.Store(true)
		repos.Character = &p2HookCharacterRepo{
			CharacterRepository: repos.Character,
			onIdentity: func(ctx context.Context, id string) (dnfrepo.CharacterIdentity, bool, bool, error) {
				if failProjection.Load() && id == "17" {
					return dnfrepo.CharacterIdentity{}, false, true, injectedErr
				}
				return dnfrepo.CharacterIdentity{}, false, false, nil
			},
		}
		s.repositoryProvider = func() (dnfrepo.Group, bool) { return repos, true }

		if _, _, _, _, ok, err := s.loadOrBuildGuildCacheOnlyMode8(ctx, access, access.request.channel); !errors.Is(err, injectedErr) || ok {
			t.Fatalf("expected lightweight guild projection error %v, got ok=%v err=%v", injectedErr, ok, err)
		}
		s.onlinePlayers.guildMode8.mu.Lock()
		cached := len(s.onlinePlayers.guildMode8.entries)
		s.onlinePlayers.guildMode8.mu.Unlock()
		if cached != 0 {
			t.Fatalf("failed guild projection must not populate guildMode8Cache, got %d entries", cached)
		}
		failProjection.Store(false)
		if body, _, _, _, ok, err := s.loadOrBuildGuildCacheOnlyMode8(ctx, access, access.request.channel); err != nil || !ok || len(body) == 0 {
			t.Fatalf("healthy projection rebuild failed: ok=%v len=%d err=%v", ok, len(body), err)
		}
	})

	t.Run("canceled_ctx_during_lightweight_build_not_cached", func(t *testing.T) {
		f, repos, viewer1Rec := setupP1WeddingGuildRosterFixture(t)
		s := f.service
		ctx := context.Background()
		viewer1Rec.Stats["wedding_v1_phase"] = 0
		_ = dnfrepo.SaveCharacterFields(ctx, repos.Character, viewer1Rec, dnfrepo.CharacterFieldWedding)
		f.target.channel.ID, f.target.connectionTownActorOwnerChannel = 11, 11
		reg, _ := dnfguild.Load(ctx, repos.Settings)
		g, _, _ := reg.GuildForCharacter("17")
		access := p2GuildMemberAccessForTest(t, s, f.target, s.gameSessions[17], g.ID)

		buildCtx, cancel := context.WithCancel(ctx)
		repos.Character = &p2HookCharacterRepo{
			CharacterRepository: repos.Character,
			onLoad: func(callCtx context.Context, charID string) (bool, error) {
				if charID == "17" {
					// Cancel after the lightweight build starts, then let the normal
					// repository read complete so the final ctx fence must drop it.
					cancel()
					return false, nil
				}
				return false, nil
			},
		}
		s.repositoryProvider = func() (dnfrepo.Group, bool) { return repos, true }

		if _, _, _, _, ok, err := s.loadOrBuildGuildCacheOnlyMode8(buildCtx, access, access.request.channel); !errors.Is(err, context.Canceled) || ok {
			t.Fatalf("expected context.Canceled on canceled lightweight build, got ok=%v err=%v", ok, err)
		}
		s.onlinePlayers.guildMode8.mu.Lock()
		cached := len(s.onlinePlayers.guildMode8.entries)
		s.onlinePlayers.guildMode8.mu.Unlock()
		if cached != 0 {
			t.Fatalf("canceled lightweight build must never populate guildMode8Cache, got %d entries", cached)
		}
	})
}
