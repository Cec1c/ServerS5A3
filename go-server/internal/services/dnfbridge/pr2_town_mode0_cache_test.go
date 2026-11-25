package dnfbridge

import (
	"bytes"
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"longheng.io/server/internal/modules/dnf/alignedcmd"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	"longheng.io/server/internal/platform/db"
)

type pr2CountingEquipStore struct {
	db.Store[dnfrepo.EquipmentRecord]
	loads  atomic.Int64
	onLoad func(id string)
}

func (s *pr2CountingEquipStore) Load(ctx context.Context, id string) (dnfrepo.EquipmentRecord, bool, error) {
	s.loads.Add(1)
	if s.onLoad != nil {
		s.onLoad(id)
	}
	return s.Store.Load(ctx, id)
}

type pr2CountingCharRepo struct {
	dnfrepo.CharacterRepository
	loads atomic.Int64
}

func (r *pr2CountingCharRepo) Load(ctx context.Context, id string) (dnfrepo.CharacterRecord, bool, error) {
	r.loads.Add(1)
	return r.CharacterRepository.Load(ctx, id)
}

func newPR2TownObserver(t *testing.T, s *Service, template *gameSession, charID uint16) (*gameSession, *bufferConn) {
	t.Helper()
	conn := &bufferConn{}
	obs := &gameSession{
		conn:                            conn,
		connID:                          fmt.Sprintf("pr2-obs-%d", charID),
		channel:                         template.channel,
		residentChannel:                 template.residentChannel,
		accountID:                       fmt.Sprintf("pr2-acc-%d", charID),
		selectedCharacterID:             charID,
		townActorOwnerChannel:           template.townActorOwnerChannel,
		connectionTownActorOwnerChannel: template.connectionTownActorOwnerChannel,
	}
	s.bindGameSessionCharacter(obs, charID)
	s.registerLiveGameSession(obs)
	t.Cleanup(func() { s.unregisterLiveGameSession(obs) })
	s.onlinePlayers.EnterArea(&onlinePlayerInfo{
		CharacterID: charID,
		AccountID:   obs.accountID,
		Session:     obs,
		ChannelID:   template.channel.ID,
		TownID:      38,
		AreaID:      2,
	})
	return obs, conn
}

func TestPR2TownMode0CacheCoalesces50ConcurrentObserversToSingleBuild(t *testing.T) {
	t.Setenv(townDisplayOnlyEnv, "on")
	baseRepos, s, observer0, actorSession, _, _ := newTownCoPresenceTestService(t)
	s.options.currentClientVersion = "official-24.9"

	enteredFirstLoad := make(chan struct{}, 1)
	releaseFirstLoad := make(chan struct{})
	var firstHook sync.Once
	equipProbe := &pr2CountingEquipStore{
		Store: baseRepos.Equipment,
		onLoad: func(id string) {
			firstHook.Do(func() {
				enteredFirstLoad <- struct{}{}
				<-releaseFirstLoad
			})
		},
	}
	charProbe := &pr2CountingCharRepo{CharacterRepository: baseRepos.Character}
	repos := baseRepos
	repos.Equipment = equipProbe
	repos.Character = charProbe
	s.repositoryProvider = func() (dnfrepo.Group, bool) { return repos, true }

	// Register 50 observers in the exact same town area as actorSession (1002).
	const observerCount = 50
	observers := make([]*gameSession, observerCount)
	conns := make([]*bufferConn, observerCount)
	for i := 0; i < observerCount; i++ {
		obs, conn := newPR2TownObserver(t, s, observer0, uint16(3000+i))
		observers[i] = obs
		conns[i] = conn
	}
	actor, found := s.onlinePlayers.PlayerForCharacter(actorSession.selectedCharacterID)
	if !found {
		t.Fatal("actor missing from onlinePlayers")
	}

	const source = "town_copresence_actor_state_newcomer_to_existing"
	errs := make(chan error, observerCount)
	var wg sync.WaitGroup
	start := make(chan struct{})
	for i := 0; i < observerCount; i++ {
		wg.Add(1)
		go func(obs *gameSession) {
			defer wg.Done()
			<-start
			errs <- s.sendTownRemoteActorState(obs, actor, source)
		}(observers[i])
	}
	close(start)

	// Wait until the singleflight leader enters Equipment.Load, let peers pile onto inflight, then release.
	<-enteredFirstLoad
	time.Sleep(15 * time.Millisecond)
	close(releaseFirstLoad)

	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("concurrent town display-only send failed: %v", err)
		}
	}

	if got := equipProbe.loads.Load(); got != 1 {
		t.Fatalf("50 concurrent observers triggered %d Equipment.Load calls, want 1", got)
	}
	// A single mode0 construction calls Character.Load at most twice (characterForEnter + builder);
	// 50 concurrent observers must execute only one build (<=2 total, never ~50-100).
	if got := charProbe.loads.Load(); got == 0 || got > 2 {
		t.Fatalf("50 concurrent observers triggered %d Character.Load calls, want 1..2 (single coalesced build)", got)
	}

	// All 50 observers must have received identical mode0 + op9 frames (no mode1).
	refFrames := townDisplayFrames(t, conns[0].write.Bytes())
	requireTownPartyActorHPFrameIDs(t, refFrames, 2, 9)
	if len(refFrames[0].body) == 0 || refFrames[0].body[0] != 0 {
		t.Fatal("first frame is not mode0")
	}
	for i := 1; i < observerCount; i++ {
		gotFrames := townDisplayFrames(t, conns[i].write.Bytes())
		if len(gotFrames) != len(refFrames) {
			t.Fatalf("observer %d frame count=%d want %d", i, len(gotFrames), len(refFrames))
		}
		for f := range refFrames {
			if gotFrames[f].msgID != refFrames[f].msgID || !bytes.Equal(gotFrames[f].body, refFrames[f].body) {
				t.Fatalf("observer %d frame %d differed from observer 0", i, f)
			}
		}
	}
}

func TestPR2TownMode0CacheTTLAndActiveInvalidations(t *testing.T) {
	t.Setenv(townDisplayOnlyEnv, "on")
	baseRepos, s, observer, actorSession, observerConn, _ := newTownCoPresenceTestService(t)
	s.options.currentClientVersion = "official-24.9"

	now := time.Date(2026, 11, 24, 18, 0, 0, 0, time.UTC)
	var nowMu sync.Mutex
	s.onlinePlayers.mode0Now = func() time.Time {
		nowMu.Lock()
		defer nowMu.Unlock()
		return now
	}
	advance := func(d time.Duration) {
		nowMu.Lock()
		now = now.Add(d)
		nowMu.Unlock()
	}

	equipProbe := &pr2CountingEquipStore{Store: baseRepos.Equipment}
	repos := baseRepos
	repos.Equipment = equipProbe
	s.repositoryProvider = func() (dnfrepo.Group, bool) { return repos, true }

	actor, _ := s.onlinePlayers.PlayerForCharacter(actorSession.selectedCharacterID)
	const source = "town_copresence_actor_state_newcomer_to_existing"

	sendAndExpectLoads := func(step string, want int64) {
		t.Helper()
		observerConn.write.Reset()
		if err := s.sendTownRemoteActorState(observer, actor, source); err != nil {
			t.Fatalf("%s: sendTownRemoteActorState: %v", step, err)
		}
		if got := equipProbe.loads.Load(); got != want {
			t.Fatalf("%s: Equipment.Load=%d want %d", step, got, want)
		}
	}

	// 1. Initial miss -> 1 load; immediate repeat within TTL -> cache hit (still 1 load).
	sendAndExpectLoads("initial_miss", 1)
	advance(2 * time.Second)
	sendAndExpectLoads("within_ttl_hit", 1)

	// 2. TTL expiry (5s) -> rebuilds cleanly (2 loads).
	advance(4 * time.Second)
	sendAndExpectLoads("after_ttl_expiry", 2)

	// 3. Equipment/avatar/pet post-action invalidates immediately -> 3 loads.
	_ = s.sendAlignedPostAction(actorSession, alignedcmd.Result{
		Operation:         "move_item",
		EquipmentMoveFact: &alignedcmd.EquipmentMoveFact{Changed: true},
	}, alignedcmd.PostActionRefreshSelectedWearContainers)
	sendAndExpectLoads("after_wear_move_invalidation", 3)

	// 4. Cross-scene appearance invalidation (creature/emotion/divorce) -> 4 loads.
	invalidateCurrentCrossSceneAppearance(actorSession)
	sendAndExpectLoads("after_cross_scene_appearance_invalidation", 4)

	// 5. Wedding peer info broadcast invalidates immediately -> 5 loads.
	if actorIdentity, ok := s.boundGameSessionCharacterSnapshot(actorSession); ok {
		_ = s.broadcastCurrentWeddingPeerBody(actorIdentity, make([]byte, 14))
	}
	sendAndExpectLoads("after_wedding_invalidation", 5)

	// 6. Party leave / clearRuntimePartyProjection invalidates immediately -> 6 loads.
	if actorIdentity, ok := s.boundGameSessionCharacterSnapshot(actorSession); ok {
		s.clearRuntimePartyProjection(actorIdentity.character, actorIdentity.generation)
	}
	sendAndExpectLoads("after_party_clear_invalidation", 6)

	// 7. LeaveArea + re-enter invalidates cached entry -> 7 loads.
	s.onlinePlayers.LeaveArea(actorSession.selectedCharacterID)
	s.onlinePlayers.EnterArea(&onlinePlayerInfo{
		CharacterID: actorSession.selectedCharacterID,
		AccountID:   actorSession.accountID,
		Session:     actorSession,
		ChannelID:   actorSession.channel.ID,
		TownID:      38,
		AreaID:      2,
	})
	actor, _ = s.onlinePlayers.PlayerForCharacter(actorSession.selectedCharacterID)
	sendAndExpectLoads("after_leave_and_reenter_invalidation", 7)
}

func TestPR2TownMode0CacheMidBuildInvalidationDoesNotPoisonCache(t *testing.T) {
	t.Setenv(townDisplayOnlyEnv, "on")
	baseRepos, s, observer, actorSession, _, _ := newTownCoPresenceTestService(t)
	s.options.currentClientVersion = "official-24.9"

	var firstBuild atomic.Bool
	equipProbe := &pr2CountingEquipStore{
		Store: baseRepos.Equipment,
		onLoad: func(string) {
			if firstBuild.CompareAndSwap(false, true) {
				// Simulate player swapping weapon mid-build.
				s.invalidateTownMode0Actor(actorSession)
			}
		},
	}
	repos := baseRepos
	repos.Equipment = equipProbe
	s.repositoryProvider = func() (dnfrepo.Group, bool) { return repos, true }

	actor, _ := s.onlinePlayers.PlayerForCharacter(actorSession.selectedCharacterID)
	const source = "town_copresence_actor_state_newcomer_to_existing"

	if err := s.sendTownRemoteActorState(observer, actor, source); err != nil {
		t.Fatalf("first send failed: %v", err)
	}
	if equipProbe.loads.Load() != 1 {
		t.Fatalf("expected 1 load on first send, got %d", equipProbe.loads.Load())
	}
	// Because appearance changed mid-build, the stale snapshot must NOT have been cached.
	if err := s.sendTownRemoteActorState(observer, actor, source); err != nil {
		t.Fatalf("second send failed: %v", err)
	}
	if equipProbe.loads.Load() != 2 {
		t.Fatalf("mid-build invalidated snapshot poisoned cache: loads=%d want 2", equipProbe.loads.Load())
	}
	// Third send with stable appearance now hits the newly cached entry.
	if err := s.sendTownRemoteActorState(observer, actor, source); err != nil {
		t.Fatalf("third send failed: %v", err)
	}
	if equipProbe.loads.Load() != 2 {
		t.Fatalf("expected cache hit on third send, loads=%d want 2", equipProbe.loads.Load())
	}
}

func TestPR2TownDisplayOnlyDifferentPartiesStayMode0AndUpgradeOnJoin(t *testing.T) {
	t.Setenv(townDisplayOnlyEnv, "on")
	_, s, observer, actorSession, observerConn, _ := newTownCoPresenceTestService(t)
	s.options.currentClientVersion = "official-24.9"
	startTownPeerTest(t, s, observer, nil)
	startTownPeerTest(t, s, actorSession, nil)

	// Put observer (1001) in party A with third player 3101, and actor (1002) in party B with 3102.
	peerA, _ := newPR2TownObserver(t, s, observer, 3101)
	peerB, _ := newPR2TownObserver(t, s, observer, 3102)
	if _, _, joined := s.createOrJoinManagedRuntimeParty(peerA, observer); !joined {
		t.Fatal("failed to create party A")
	}
	if _, _, joined := s.createOrJoinManagedRuntimeParty(peerB, actorSession); !joined {
		t.Fatal("failed to create party B")
	}
	drainTownPeerTest(t, s, observer)
	observerConn.write.Reset()

	actor, _ := s.onlinePlayers.PlayerForCharacter(actorSession.selectedCharacterID)
	const source = "town_copresence_actor_state_newcomer_to_existing"
	if !s.currentTownDisplayOnlyProjection(observer, actor, source) {
		t.Fatal("different multiplayer parties must remain display-only mode0")
	}
	if err := s.sendTownRemoteActorState(observer, actor, source); err != nil {
		t.Fatalf("different-party mode0 send failed: %v", err)
	}
	frames := townDisplayFrames(t, observerConn.write.Bytes())
	requireTownPartyActorHPFrameIDs(t, frames, 2, 9)
	if len(frames[0].body) == 0 || frames[0].body[0] != 0 {
		t.Fatal("expected mode0 frame for non-same-party town actor")
	}

	// Now actor joins observer's party: upgradeTownDisplayPeersForParty must promote actor to full mode1.
	state, _, joined := s.createOrJoinManagedRuntimeParty(actorSession, observer)
	if !joined {
		t.Fatal("failed to join observer party")
	}
	if s.currentTownDisplayOnlyProjection(observer, actor, source) {
		t.Fatal("same multiplayer party must use full mode1 projection")
	}
	drainTownPeerTest(t, s, observer)
	observerConn.write.Reset()
	if err := s.sendRuntimePartyRosterLocal(observer, state, "pr2_same_party_promotion"); err != nil {
		t.Fatalf("sendRuntimePartyRosterLocal: %v", err)
	}
	requireTownDisplayAuditPromotionBeforeHUD(t, townDisplayFrames(t, observerConn.write.Bytes()))
}

func TestPR2TownMode0CacheBoundedCapacity(t *testing.T) {
	m := newOnlinePlayerManager()
	for i := 0; i < townMode0CacheMaxEntries+64; i++ {
		key := townMode0CacheKey{characterID: uint16(i + 1), generation: 1, channelID: 10, remoteOwner: 10}
		m.storeMode0Cache(key, townMode0CacheEntry{accountID: "acc", mode0Body: []byte{0}})
	}
	m.mode0CacheMu.Lock()
	size := len(m.mode0Cache)
	m.mode0CacheMu.Unlock()
	if size > townMode0CacheMaxEntries {
		t.Fatalf("mode0Cache exceeded bound: size=%d max=%d", size, townMode0CacheMaxEntries)
	}
}
