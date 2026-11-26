package dnfbridge

import (
	"bytes"
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"longheng.io/server/internal/modules/dnf/channelcatalog"
	"longheng.io/server/internal/modules/dnf/dnfenum"
	dnfguild "longheng.io/server/internal/modules/dnf/guild"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

type p1HookCharRepo struct {
	dnfrepo.CharacterRepository
	onLoad func(ctx context.Context, id string) (bool, error)
}

func (r *p1HookCharRepo) Load(ctx context.Context, id string) (dnfrepo.CharacterRecord, bool, error) {
	if r.onLoad != nil {
		if intercepted, err := r.onLoad(ctx, id); intercepted {
			return dnfrepo.CharacterRecord{}, false, err
		}
	}
	return r.CharacterRepository.Load(ctx, id)
}

func (r *p1HookCharRepo) LoadCharacterIdentity(ctx context.Context, id string) (dnfrepo.CharacterIdentity, bool, error) {
	return dnfrepo.LoadCharacterIdentity(ctx, r.CharacterRepository, id)
}

func (r *p1HookCharRepo) LoadCharacterMirrorStats(ctx context.Context, id string, keys ...string) (dnfrepo.CharacterMirrorStats, bool, error) {
	return dnfrepo.LoadCharacterMirrorStats(ctx, r.CharacterRepository, id, keys...)
}

func (r *p1HookCharRepo) LoadCharacterWedding(ctx context.Context, id string) (dnfrepo.CharacterWeddingSnapshot, bool, error) {
	return dnfrepo.LoadCharacterWedding(ctx, r.CharacterRepository, id)
}

func saveP1PrimeBudgets(t *testing.T, maxDur, minHeadway, perPeer, backoff time.Duration) {
	t.Helper()
	origMax := guildSameChannelPrimeMaxDuration
	origHead := guildSameChannelPrimeMinCtxHeadway
	origPeer := guildSameChannelPrimePerPeerMaxDuration
	origBackoff := guildSameChannelPrimeFailBackoff
	guildSameChannelPrimeMaxDuration = maxDur
	guildSameChannelPrimeMinCtxHeadway = minHeadway
	guildSameChannelPrimePerPeerMaxDuration = perPeer
	guildSameChannelPrimeFailBackoff = backoff
	t.Cleanup(func() {
		guildSameChannelPrimeMaxDuration = origMax
		guildSameChannelPrimeMinCtxHeadway = origHead
		guildSameChannelPrimePerPeerMaxDuration = origPeer
		guildSameChannelPrimeFailBackoff = origBackoff
	})
}

func addP1GuildOnlinePeer(t *testing.T, s *Service, repos dnfrepo.Group, template dnfrepo.CharacterRecord, charID uint16, charIDStr, accountID, name string, townID, areaID byte) (*gameSession, *bufferConn) {
	t.Helper()
	ctx := context.Background()
	rec := template
	rec.CharacterID = charIDStr
	rec.AccountID = accountID
	rec.Name = name
	rec.Stats = map[string]int64{}
	if err := repos.Character.Save(ctx, rec); err != nil {
		t.Fatal(err)
	}
	reg, err := dnfguild.Load(ctx, repos.Settings)
	if err != nil {
		t.Fatal(err)
	}
	g, _, ok := reg.GuildForCharacter("17")
	if !ok {
		t.Fatal("missing guild for actor 17")
	}
	if _, err := reg.JoinDirect(g.ID, rec, s.gameplayNow()); err != nil {
		t.Fatal(err)
	}
	if err := dnfguild.Save(ctx, repos.Settings, reg, s.gameplayNow()); err != nil {
		t.Fatal(err)
	}
	conn := &bufferConn{}
	sess := &gameSession{
		conn:                            conn,
		connID:                          "p1-peer-" + charIDStr,
		accountID:                       accountID,
		channel:                         channelcatalog.Channel{ID: 11},
		residentChannel:                 channelcatalog.Channel{ID: 11},
		selectedCharacterID:             charID,
		characterGeneration:             1,
		townSceneReadyCharacterID:       charID,
		townActorOwnerChannel:           11,
		connectionTownActorOwnerChannel: 11,
	}
	s.registerLiveGameSession(sess)
	s.mu.Lock()
	s.gameSessions[charID] = sess
	s.mu.Unlock()
	if s.onlinePlayers != nil {
		s.onlinePlayers.EnterArea(&onlinePlayerInfo{
			CharacterID: charID,
			AccountID:   accountID,
			Name:        name,
			Session:     sess,
			ChannelID:   11,
			TownID:      townID,
			AreaID:      areaID,
		})
	}
	return sess, conn
}

func TestP1GuildMemberMemoRefreshDoesNotSelfWaitOnReservation(t *testing.T) {
	f, repos, viewer := setupP1WeddingGuildRosterFixture(t)
	s := f.service
	ctx := context.Background()

	viewer.Stats["wedding_v1_phase"] = 0
	if err := dnfrepo.SaveCharacterFields(ctx, repos.Character, viewer, dnfrepo.CharacterFieldWedding); err != nil {
		t.Fatal(err)
	}
	f.target.channel.ID, f.target.connectionTownActorOwnerChannel = 11, 11
	f.target.guild.memberListRequested = true
	f.targetConn.write.Reset()

	memo := "P1备注不自锁"
	done := make(chan error, 1)
	started := time.Now()
	go func() {
		done <- s.handleCurrentGuildMemberMemo(f.target, guildWireString(memo))
	}()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("handleCurrentGuildMemberMemo failed: %v", err)
		}
	case <-time.After(1500 * time.Millisecond):
		t.Fatal("handleCurrentGuildMemberMemo self-waited on viewer character asset reservation")
	}
	if elapsed := time.Since(started); elapsed > 1200*time.Millisecond {
		t.Fatalf("expected fast memo refresh without reservation self-wait, took %v", elapsed)
	}

	raw := f.targetConn.write.Bytes()
	if !bytes.Equal(guildReply(t, f.target, uint16(dnfenum.CmdPacketWriteGuildMemberMemo)), []byte{1}) {
		t.Fatal("missing memo success ACK")
	}
	if got := countSilentMode8Frames(t, raw); got != 1 {
		t.Fatalf("expected 1 silent mode8 prime frame for unprimed peer 17 during memo refresh, got %d", got)
	}
	if !bytes.Contains(guildReply(t, f.target, uint16(dnfenum.CmdPacketGuildAllMemberList)), rosterNameBytes(memo)) {
		t.Fatal("roster refresh missing updated memo")
	}
}

func TestP1GuildPrimeBudgetBoundsBlockingRepoAndHeldReservation(t *testing.T) {
	t.Run("blocking_repo", func(t *testing.T) {
		f, repos, viewer := setupP1WeddingGuildRosterFixture(t)
		s := f.service
		ctx := context.Background()

		viewer.Stats["wedding_v1_phase"] = 0
		if err := dnfrepo.SaveCharacterFields(ctx, repos.Character, viewer, dnfrepo.CharacterFieldWedding); err != nil {
			t.Fatal(err)
		}
		f.target.channel.ID, f.target.connectionTownActorOwnerChannel = 11, 11

		var blockedCalls atomic.Int32
		hookRepo := &p1HookCharRepo{
			CharacterRepository: repos.Character,
			onLoad: func(callCtx context.Context, id string) (bool, error) {
				if id != "17" {
					return false, nil
				}
				blockedCalls.Add(1)
				<-callCtx.Done()
				return true, callCtx.Err()
			},
		}
		repos.Character = hookRepo
		s.repositoryProvider = func() (dnfrepo.Group, bool) { return repos, true }

		saveP1PrimeBudgets(t, 120*time.Millisecond, 400*time.Millisecond, 120*time.Millisecond, 5*time.Second)

		bound, ok := s.boundGameSessionCharacterSnapshot(f.target)
		if !ok {
			t.Fatal("missing bound snapshot")
		}
		display, err := s.readCurrentGuildDisplay(ctx, bound, false, true)
		if err != nil {
			t.Fatal(err)
		}

		parentCtx, cancel := context.WithTimeout(ctx, 1500*time.Millisecond)
		defer cancel()
		f.targetConn.write.Reset()
		var buildTime, writeTime time.Duration
		started := time.Now()
		err = s.sendCurrentGuildRosterDisplayShared(parentCtx, bound, display, uint16(dnfenum.CmdPacketGuildAllMemberList), true, true, "client", false, nil, &buildTime, &writeTime)
		elapsed := time.Since(started)
		if err != nil {
			t.Fatalf("roster display should succeed after priming budget exit, got err=%v", err)
		}
		if elapsed > 600*time.Millisecond {
			t.Fatalf("blocking repo exceeded priming budget: elapsed=%v", elapsed)
		}
		if parentCtx.Err() != nil {
			t.Fatalf("parent ctx should retain headway after priming budget exit, got %v", parentCtx.Err())
		}
		if blockedCalls.Load() == 0 {
			t.Fatal("expected blocking repo to be invoked")
		}
		if f.target.channelExitWireClosed {
			t.Fatal("budget timeout before socket write must not close viewer transport")
		}
		if len(guildReply(t, f.target, uint16(dnfenum.CmdPacketGuildAllMemberList))) == 0 {
			t.Fatal("expected guild roster frame to be delivered after priming budget exit")
		}
		if !s.hasIncompleteGuildRosterPrime(bound, display) {
			t.Fatal("expected incomplete priming flag to be recorded for continuation")
		}
	})

	t.Run("held_reservation", func(t *testing.T) {
		f, repos, viewer := setupP1WeddingGuildRosterFixture(t)
		s := f.service
		ctx := context.Background()

		viewer.Stats["wedding_v1_phase"] = 0
		if err := dnfrepo.SaveCharacterFields(ctx, repos.Character, viewer, dnfrepo.CharacterFieldWedding); err != nil {
			t.Fatal(err)
		}
		f.target.channel.ID, f.target.connectionTownActorOwnerChannel = 11, 11

		actorBound, ok := s.onlineGameSessionCharacterSnapshot(17)
		if !ok {
			t.Fatal("missing online snapshot for actor 17")
		}
		releaseHeld, err := s.reserveCurrentGuildParticipants(ctx, []boundGameSessionCharacter{actorBound})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(releaseHeld)

		saveP1PrimeBudgets(t, 120*time.Millisecond, 400*time.Millisecond, 120*time.Millisecond, 5*time.Second)

		bound, ok := s.boundGameSessionCharacterSnapshot(f.target)
		if !ok {
			t.Fatal("missing bound snapshot")
		}
		display, err := s.readCurrentGuildDisplay(ctx, bound, false, true)
		if err != nil {
			t.Fatal(err)
		}

		parentCtx, cancel := context.WithTimeout(ctx, 1500*time.Millisecond)
		defer cancel()
		f.targetConn.write.Reset()
		var buildTime, writeTime time.Duration
		started := time.Now()
		err = s.sendCurrentGuildRosterDisplayShared(parentCtx, bound, display, uint16(dnfenum.CmdPacketGuildAllMemberList), true, true, "client", false, nil, &buildTime, &writeTime)
		elapsed := time.Since(started)
		if err != nil {
			t.Fatalf("roster display should succeed when peer reservation is held, got err=%v", err)
		}
		if elapsed > 600*time.Millisecond {
			t.Fatalf("held reservation exceeded priming budget: elapsed=%v", elapsed)
		}
		if parentCtx.Err() != nil {
			t.Fatalf("parent ctx should retain headway when reservation wait hits budget, got %v", parentCtx.Err())
		}
		if f.target.channelExitWireClosed {
			t.Fatal("reservation wait budget exit must not close viewer transport")
		}
		if len(guildReply(t, f.target, uint16(dnfenum.CmdPacketGuildAllMemberList))) == 0 {
			t.Fatal("expected guild roster frame to be delivered after reservation budget exit")
		}
	})
}

func TestP1GuildPrimeTruncatedContinuationResumesOnPaginationAndDefersFailingPeer(t *testing.T) {
	t.Run("truncated_full_resumes_on_next_and_enables_cross_area_invite", func(t *testing.T) {
		f, repos, viewer := setupP1WeddingGuildRosterFixture(t)
		s := f.service
		ctx := context.Background()

		viewer.Stats["wedding_v1_phase"] = 0
		if err := dnfrepo.SaveCharacterFields(ctx, repos.Character, viewer, dnfrepo.CharacterFieldWedding); err != nil {
			t.Fatal(err)
		}
		f.target.channel.ID, f.target.connectionTownActorOwnerChannel = 11, 11
		if s.onlinePlayers != nil {
			s.onlinePlayers.EnterArea(&onlinePlayerInfo{
				CharacterID: 29,
				AccountID:   f.target.accountID,
				Name:        viewer.Name,
				Session:     f.target,
				ChannelID:   11,
				TownID:      1,
				AreaID:      0,
			})
		}

		// Add a second online same-channel peer (char 31, different town area) so
		// sorted members are [17, 29(viewer), 31].
		peer31, _ := addP1GuildOnlinePeer(t, s, repos, viewer, 31, "31", "dnf:3", "peer31", 2, 1)

		// Let peer 17 succeed immediately, then stall peer 31 on first pass until
		// the priming budget deadline fires deterministically.
		var blocked31 atomic.Bool
		hookRepo := &p1HookCharRepo{
			CharacterRepository: repos.Character,
			onLoad: func(callCtx context.Context, id string) (bool, error) {
				if id == "31" && blocked31.CompareAndSwap(false, true) {
					<-callCtx.Done()
					return true, callCtx.Err()
				}
				return false, nil
			},
		}
		repos.Character = hookRepo
		s.repositoryProvider = func() (dnfrepo.Group, bool) { return repos, true }

		saveP1PrimeBudgets(t, 80*time.Millisecond, 200*time.Millisecond, 80*time.Millisecond, 10*time.Millisecond)

		// 1. Initial full roster gets truncated after peer 17.
		f.targetConn.write.Reset()
		if err := s.handleCurrentGuildMemberListForOpcode(f.target, uint16(dnfenum.CmdPacketGuildAllMemberList)); err != nil {
			t.Fatalf("initial full guild roster failed: %v", err)
		}
		if got := countSilentMode8Frames(t, f.targetConn.write.Bytes()); got != 1 {
			t.Fatalf("expected truncated first pass to prime only peer 17 (1 frame), got %d", got)
		}
		f.target.guild.mu.Lock()
		_, has17 := f.target.guild.weddingPrimedPeers[17]
		_, has31Before := f.target.guild.weddingPrimedPeers[31]
		incompleteBefore := f.target.guild.sameChannelPrimeIncomplete
		f.target.guild.mu.Unlock()
		if !has17 || has31Before || !incompleteBefore {
			t.Fatalf("unexpected post-truncation state: has17=%v has31=%v incomplete=%v", has17, has31Before, incompleteBefore)
		}

		// 2. Restore normal budget (after short 10ms retry backoff) and paginate ONLY via CmdPacketGuildAllMemberListNext.
		time.Sleep(20 * time.Millisecond)
		guildSameChannelPrimeMaxDuration = 1500 * time.Millisecond
		guildSameChannelPrimePerPeerMaxDuration = 500 * time.Millisecond
		f.targetConn.write.Reset()
		if err := s.handleCurrentGuildMemberListForOpcode(f.target, uint16(dnfenum.CmdPacketGuildAllMemberListNext)); err != nil {
			t.Fatalf("pagination continuation failed: %v", err)
		}
		if got := countSilentMode8Frames(t, f.targetConn.write.Bytes()); got != 1 {
			t.Fatalf("expected pagination continuation to resume and prime peer 31 (1 frame), got %d", got)
		}
		f.target.guild.mu.Lock()
		_, has31After := f.target.guild.weddingPrimedPeers[31]
		incompleteAfter := f.target.guild.sameChannelPrimeIncomplete
		f.target.guild.mu.Unlock()
		if !has31After || incompleteAfter {
			t.Fatalf("expected peer 31 primed and continuation complete, got has31=%v incomplete=%v", has31After, incompleteAfter)
		}

		// 3. Subsequent pagination once complete must emit 0 mode8 frames.
		f.targetConn.write.Reset()
		if err := s.handleCurrentGuildMemberListForOpcode(f.target, uint16(dnfenum.CmdPacketGuildAllMemberListNext)); err != nil {
			t.Fatalf("second pagination failed: %v", err)
		}
		if got := countSilentMode8Frames(t, f.targetConn.write.Bytes()); got != 0 {
			t.Fatalf("completed pagination should emit 0 mode8 frames, got %d", got)
		}

		// 4. Cross-area invite target (peer 31) is eligible.
		if !s.canCurrentCrossAreaPartyPeerRequest(f.target, peer31, 29, 31, 0) {
			t.Fatal("expected cross-area party invite to primed back-page peer 31 to be permitted")
		}
	})

	t.Run("persistently_failing_front_peer_defers_and_primes_later_peer", func(t *testing.T) {
		f, repos, viewer := setupP1WeddingGuildRosterFixture(t)
		s := f.service
		ctx := context.Background()

		viewer.Stats["wedding_v1_phase"] = 0
		if err := dnfrepo.SaveCharacterFields(ctx, repos.Character, viewer, dnfrepo.CharacterFieldWedding); err != nil {
			t.Fatal(err)
		}
		f.target.channel.ID, f.target.connectionTownActorOwnerChannel = 11, 11
		addP1GuildOnlinePeer(t, s, repos, viewer, 31, "31", "dnf:3", "peer31", 2, 1)

		var peer17Attempts atomic.Int32
		hookRepo := &p1HookCharRepo{
			CharacterRepository: repos.Character,
			onLoad: func(callCtx context.Context, id string) (bool, error) {
				if id == "17" {
					peer17Attempts.Add(1)
					return true, errors.New("persistent front peer load error")
				}
				return false, nil
			},
		}
		repos.Character = hookRepo
		s.repositoryProvider = func() (dnfrepo.Group, bool) { return repos, true }

		saveP1PrimeBudgets(t, 500*time.Millisecond, 200*time.Millisecond, 100*time.Millisecond, 5*time.Second)

		f.targetConn.write.Reset()
		if err := s.handleCurrentGuildMemberListForOpcode(f.target, uint16(dnfenum.CmdPacketGuildAllMemberList)); err != nil {
			t.Fatalf("guild roster failed with failing front peer: %v", err)
		}
		if got := countSilentMode8Frames(t, f.targetConn.write.Bytes()); got != 1 {
			t.Fatalf("expected later peer 31 to still be primed (1 frame), got %d", got)
		}
		f.target.guild.mu.Lock()
		_, has31 := f.target.guild.weddingPrimedPeers[31]
		_, backedOff17 := f.target.guild.sameChannelPrimeRetryAfter[17]
		f.target.guild.mu.Unlock()
		if !has31 || !backedOff17 {
			t.Fatalf("expected peer 31 primed and peer 17 deferred on backoff, got has31=%v backedOff17=%v", has31, backedOff17)
		}

		// Immediate follow-up while peer 17 is in backoff must NOT hammer peer 17 again.
		beforeAttempts := peer17Attempts.Load()
		f.targetConn.write.Reset()
		if err := s.handleCurrentGuildMemberListForOpcode(f.target, uint16(dnfenum.CmdPacketGuildAllMemberListNext)); err != nil {
			t.Fatalf("follow-up pagination failed: %v", err)
		}
		if peer17Attempts.Load() != beforeAttempts {
			t.Fatalf("backed-off front peer 17 should not be retried immediately: before=%d after=%d", beforeAttempts, peer17Attempts.Load())
		}
	})
}
