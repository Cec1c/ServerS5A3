package dnfbridge

import (
	"context"
	"encoding/binary"
	"errors"
	"reflect"
	"testing"
	"time"

	"longheng.io/server/internal/modules/dnf/guild"
	"longheng.io/server/internal/modules/dnf/protocol"
	r "longheng.io/server/internal/modules/dnf/repository"
)

func guildEssenceSceneReadyFixture(t *testing.T) (*Service, *gameSession, r.Group) {
	t.Helper()
	s, p, repos := guildMileageFixture(t)
	guildEssenceBridgeCatalog(t, s)
	// The actor and non-guild HUD owners have already committed. Exercise the
	// real final type1345 -> guild HUD suffix, with unrelated replay lanes off.
	t.Setenv(loginActorGenerationRebuildEnv, "off")
	t.Setenv(loginEquipmentSceneReadyReplayEnv, loginEquipmentSceneReadyReplayBeforeTransitionOnly)
	t.Setenv(loginSwitchInventorySceneReadyReplayEnv, loginSwitchInventorySceneReadyReplayBeforeTransitionOnly)
	t.Setenv(buffSwapReapplyEnv, "off")
	p.initialTownRouteCharacterID = p.selectedCharacterID
	p.initialTownRouteStage = currentInitialTownRoutePlayerStateSent
	p.initialTownLegacySceneReadyAccepted = false
	p.initialTownGuildStateProjectionSent = false
	p.initialTownAdventureOverheadRefreshSent = true
	p.initialTownCombatPowerAffixesSent = true
	p.sceneBootstrapTailSent = true
	p.sceneBootstrapTailDeferred = false
	p.sceneBootstrapTailPostStage = currentDeferredSelectSceneTailComplete
	ctx, cancel := context.WithCancel(context.Background())
	if err := s.startGameSessionEvents(ctx, p); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		stop, done := context.WithTimeout(context.Background(), time.Second)
		defer done()
		if err := s.stopGameSessionEvents(stop, p); err != nil {
			t.Error(err)
		}
		cancel()
	})
	return s, p, repos
}

func callEssenceSceneReady(t *testing.T, s *Service, p *gameSession) error {
	t.Helper()
	return s.callGameSession(context.Background(), p, "game-stream-packet", func() error {
		return s.handleGameStreamPacket(p, protocol.LatestGameStreamPacket{Kind: protocol.LatestGameStreamLegacy, Data: buildLegacyTownSceneReadyAckPacket()})
	})
}

func callEssenceGuildRefresh(t *testing.T, s *Service, p *gameSession, seq uint16) error {
	t.Helper()
	wire, err := protocol.BuildChannelPacket(339, nil, seq, 1)
	if err != nil {
		t.Fatal(err)
	}
	packets, rest, skipped, err := protocol.SplitLatestGameStream(wire, 65535)
	if err != nil || len(packets) != 1 || len(rest) != 0 || skipped != 0 {
		t.Fatal(err)
	}
	return s.callGameSession(context.Background(), p, "game-stream-packet", func() error { return s.handleGameStreamPacket(p, packets[0]) })
}

func TestGuildEssenceLoginStorageFailureKeepsSceneReadyAndRefreshRetriesOnce(t *testing.T) {
	s, p, repos := guildEssenceSceneReadyFixture(t)
	ctx := context.Background()
	broken := repos
	broken.Guild = guildTextFailUoW{base: repos.Guild}
	s.repositoryProvider = func() (r.Group, bool) { return broken, true }
	before, _, _ := repos.Settings.Load(ctx, guild.RegistryScope)
	charBefore, _, _ := repos.Character.Load(ctx, "29")
	if err := callEssenceSceneReady(t, s, p); err != nil {
		t.Fatalf("optional income escaped scene-ready packet loop: %v", err)
	}
	if !p.initialTownLegacySceneReadyAccepted || !p.initialTownGuildStateProjectionSent || !p.sceneBootstrapTailSent || p.sceneBootstrapTailDeferred {
		t.Fatal("selection suffix did not complete")
	}
	guildReply(t, p, currentGuildAccountIndexOpcode) // Continuation after failed income.
	identity, ok := s.boundGameSessionCharacterSnapshot(p)
	if !ok || p.guildEssenceLoginRetry != identity {
		t.Fatal("missing generation-bound retry")
	}
	after, _, _ := repos.Settings.Load(ctx, guild.RegistryScope)
	charAfter, _, _ := repos.Character.Load(ctx, "29")
	if !reflect.DeepEqual(before, after) || !reflect.DeepEqual(charBefore, charAfter) {
		t.Fatal("failed income left balance/day marker")
	}
	// Another failed window refresh remains optional, keeps its retry, and
	// completes the ordinary NOTI70 response on the same live event queue.
	p.conn.(*bufferConn).write.Reset()
	if err := callEssenceGuildRefresh(t, s, p, 30); err != nil {
		t.Fatal(err)
	}
	guildReply(t, p, 70)
	assertNoEssenceNotice(t, p.conn.(*bufferConn).write.Bytes())
	if p.guildEssenceLoginRetry != identity {
		t.Fatal("failed retry was discarded")
	}
	s.repositoryProvider = func() (r.Group, bool) { return repos, true }
	p.conn.(*bufferConn).write.Reset()
	for seq := uint16(31); seq < 35; seq++ {
		if err := callEssenceGuildRefresh(t, s, p, seq); err != nil {
			t.Fatal(err)
		}
	}
	if p.guildEssenceLoginRetry != (boundGameSessionCharacter{}) {
		t.Fatal("completed retry retained")
	}
	reg, err := guild.Load(ctx, repos.Settings)
	if err != nil || reg.Guilds[1].Essence.AP != 10 {
		t.Fatal(reg, err)
	}
	actor, _, err := repos.Character.Load(ctx, "29")
	if err != nil || actor.Stats["guild_essence_day_ap"] != 10 || actor.Stats["guild_essence_day_login"] != 1 {
		t.Fatal(actor.Stats, err)
	}
	notices := 0
	for _, packet := range splitAllCurrentUpperPackets(t, p.conn.(*bufferConn).write.Bytes()) {
		if packet.Header.MsgID == 1284 {
			notices++
			if binary.LittleEndian.Uint32(packet.Body) != 10 {
				t.Fatal("wrong restored income")
			}
		}
		if packet.Header.MsgID == 1297 && binary.LittleEndian.Uint32(packet.Body[0x17:]) != 10 {
			t.Fatal("retry snapshot lost income")
		}
	}
	if notices != 1 {
		t.Fatal("retry issued duplicate income notices", notices)
	}
	// Even another explicit login owner invocation cannot award the same day.
	p.conn.(*bufferConn).write.Reset()
	if err := s.callGameSession(ctx, p, "login-replay", func() error {
		p.townMu.Lock()
		defer p.townMu.Unlock()
		s.projectOptionalGuildEssenceLoginLocked(p)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	assertNoEssenceNotice(t, p.conn.(*bufferConn).write.Bytes())
	t.Log("scene ready PASS; failed save rolled back; 4 later refreshes grant AP10 once, one notice, pending cleared")
}

func TestGuildEssenceLoginRetryDropsRetiredGeneration(t *testing.T) {
	s, p, repos := guildEssenceSceneReadyFixture(t)
	broken := repos
	broken.Guild = guildTextFailUoW{base: repos.Guild}
	s.repositoryProvider = func() (r.Group, bool) { return broken, true }
	if err := callEssenceSceneReady(t, s, p); err != nil {
		t.Fatal(err)
	}
	s.repositoryProvider = func() (r.Group, bool) { return repos, true }
	if err := s.callGameSession(context.Background(), p, "new-generation", func() error { s.mu.Lock(); p.characterGeneration++; s.mu.Unlock(); return nil }); err != nil {
		t.Fatal(err)
	}
	p.conn.(*bufferConn).write.Reset()
	if err := callEssenceGuildRefresh(t, s, p, 30); err != nil {
		t.Fatal(err)
	}
	reg, err := guild.Load(context.Background(), repos.Settings)
	if err != nil || reg.Guilds[1].Essence.AP != 0 || p.guildEssenceLoginRetry != (boundGameSessionCharacter{}) {
		t.Fatal("retired login retried", err)
	}
	assertNoEssenceNotice(t, p.conn.(*bufferConn).write.Bytes())
}

func TestGuildEssenceOptionalLanePreservesRequiredSceneWriteFailure(t *testing.T) {
	s, p, _ := guildEssenceSceneReadyFixture(t)
	want := errors.New("required guild model socket failure")
	p.conn = &failNthDungeonWriteConn{failAt: 1, err: want}
	if err := callEssenceSceneReady(t, s, p); !errors.Is(err, want) {
		t.Fatal("required write failure swallowed", err)
	}
	if p.initialTownGuildStateProjectionSent {
		t.Fatal("required projection marked complete")
	}
}

type essenceSnapshotFailSettings struct {
	r.SettingsRepository
	loads int
}

func (s *essenceSnapshotFailSettings) Load(ctx context.Context, scope string) (r.SettingsRecord, bool, error) {
	if scope == guild.RegistryScope {
		s.loads++
		// Membership and dungeon projection own the first two reads.
		if s.loads == 3 {
			return r.SettingsRecord{}, false, errors.New("optional essence snapshot load failed")
		}
	}
	return s.SettingsRepository.Load(ctx, scope)
}

func TestGuildEssenceOptionalSnapshotFailureKeepsLoginSuffix(t *testing.T) {
	s, p, repos := guildEssenceSceneReadyFixture(t)
	reads := &essenceSnapshotFailSettings{SettingsRepository: repos.Settings}
	broken := repos
	broken.Settings = reads
	s.repositoryProvider = func() (r.Group, bool) { return broken, true }
	if err := callEssenceSceneReady(t, s, p); err != nil {
		t.Fatal("optional snapshot interrupted login", err)
	}
	if reads.loads < 3 || !p.initialTownGuildStateProjectionSent {
		t.Fatal("snapshot failure was not exercised")
	}
	reg, err := guild.Load(context.Background(), repos.Settings)
	if err != nil || reg.Guilds[1].Essence.AP != 10 {
		t.Fatal(reg, err)
	}
	guildReply(t, p, currentGuildAccountIndexOpcode)
}

func TestGuildEssenceLoginProjectionFailureAfterCommitDoesNotGrantAgain(t *testing.T) {
	s, p, _ := guildEssenceSceneReadyFixture(t)
	if err := callEssenceSceneReady(t, s, p); err != nil {
		t.Fatal(err)
	}
	failAt, snapshots := 0, 0
	for i, packet := range splitAllCurrentUpperPackets(t, p.conn.(*bufferConn).write.Bytes()) {
		if packet.Header.MsgID == 1297 {
			snapshots++
			if snapshots == 2 {
				failAt = i + 1
				break
			}
		}
	}
	if failAt == 0 {
		t.Fatal("missing post-income snapshot")
	}
	s, p, repos := guildEssenceSceneReadyFixture(t)
	p.conn = &failNthDungeonWriteConn{failAt: failAt, err: errors.New("post-commit essence projection failed")}
	if err := callEssenceSceneReady(t, s, p); err != nil {
		t.Fatal(err)
	}
	if !p.initialTownGuildStateProjectionSent || p.guildEssenceLoginRetry.session != p {
		t.Fatal("lost post-commit retry")
	}
	p.conn = &bufferConn{}
	for seq := uint16(30); seq < 33; seq++ {
		if err := callEssenceGuildRefresh(t, s, p, seq); err != nil {
			t.Fatal(err)
		}
	}
	reg, err := guild.Load(context.Background(), repos.Settings)
	if err != nil || reg.Guilds[1].Essence.AP != 10 {
		t.Fatal("post-commit retry granted twice", err)
	}
	assertNoEssenceNotice(t, p.conn.(*bufferConn).write.Bytes())
	if p.guildEssenceLoginRetry != (boundGameSessionCharacter{}) {
		t.Fatal("projection retry not completed")
	}
}
