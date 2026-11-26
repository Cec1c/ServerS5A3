package dnfbridge

import (
	"context"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"longheng.io/server/internal/modules/dnf/channelcatalog"
	"longheng.io/server/internal/modules/dnf/dnfenum"
	dnfguild "longheng.io/server/internal/modules/dnf/guild"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

type p0CountingCharRepo struct {
	dnfrepo.CharacterRepository
	mu       sync.Mutex
	fullByID map[string]int
	narrow   int
	identity int
}

func (r *p0CountingCharRepo) Load(ctx context.Context, id string) (dnfrepo.CharacterRecord, bool, error) {
	r.mu.Lock()
	if r.fullByID == nil {
		r.fullByID = make(map[string]int)
	}
	r.fullByID[id]++
	r.mu.Unlock()
	return r.CharacterRepository.Load(ctx, id)
}

func (r *p0CountingCharRepo) fullLoadsFor(id string) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.fullByID[id]
}

func (r *p0CountingCharRepo) LoadCharacterIdentity(ctx context.Context, id string) (dnfrepo.CharacterIdentity, bool, error) {
	r.mu.Lock()
	r.identity++
	r.mu.Unlock()
	return dnfrepo.LoadCharacterIdentity(ctx, r.CharacterRepository, id)
}

func (r *p0CountingCharRepo) LoadCharacterMirrorStats(ctx context.Context, id string, keys ...string) (dnfrepo.CharacterMirrorStats, bool, error) {
	r.mu.Lock()
	r.narrow++
	r.mu.Unlock()
	return dnfrepo.LoadCharacterMirrorStats(ctx, r.CharacterRepository, id, keys...)
}

type p0CountingSkillRepo struct {
	dnfrepo.SkillRepository
	mu        sync.Mutex
	loadsByID map[string]int
}

func (r *p0CountingSkillRepo) Load(ctx context.Context, id string) (dnfrepo.SkillRecord, bool, error) {
	r.mu.Lock()
	if r.loadsByID == nil {
		r.loadsByID = make(map[string]int)
	}
	r.loadsByID[id]++
	r.mu.Unlock()
	if r.SkillRepository == nil {
		return dnfrepo.SkillRecord{}, false, nil
	}
	return r.SkillRepository.Load(ctx, id)
}

func (r *p0CountingSkillRepo) loadsFor(id string) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.loadsByID[id]
}

type p0CountingSettingsRepo struct {
	dnfrepo.SettingsRepository
	buffSwapLoads atomic.Int64
}

func (r *p0CountingSettingsRepo) Load(ctx context.Context, scope string) (dnfrepo.SettingsRecord, bool, error) {
	if strings.Contains(scope, "buff_swap") {
		r.buffSwapLoads.Add(1)
	}
	return r.SettingsRepository.Load(ctx, scope)
}

func TestP0GuildMode8CacheReusesAcrossViewersInvalidatesAndEnforcesLiveValidation(t *testing.T) {
	f, repos, viewer1Rec := setupP1WeddingGuildRosterFixture(t)
	s := f.service
	ctx := context.Background()

	// Disable wedding reservation on viewer 1 so this exercises ordinary guild roster priming.
	viewer1Rec.Stats["wedding_v1_phase"] = 0
	if err := dnfrepo.SaveCharacterFields(ctx, repos.Character, viewer1Rec, dnfrepo.CharacterFieldWedding); err != nil {
		t.Fatal(err)
	}

	// Add a second online guild viewer (char 31, account dnf:3) on the same channel (11).
	viewer2Rec := viewer1Rec
	viewer2Rec.CharacterID = "31"
	viewer2Rec.AccountID = "dnf:3"
	viewer2Rec.Name = "viewer2"
	viewer2Rec.Stats = map[string]int64{}
	if err := repos.Character.Save(ctx, viewer2Rec); err != nil {
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
	if _, err := reg.JoinDirect(g.ID, viewer2Rec, s.gameplayNow()); err != nil {
		t.Fatal(err)
	}
	if err := dnfguild.Save(ctx, repos.Settings, reg, s.gameplayNow()); err != nil {
		t.Fatal(err)
	}
	viewer2Conn := &bufferConn{}
	viewer2 := &gameSession{
		conn:                            viewer2Conn,
		connID:                          "guild-viewer-2",
		accountID:                       "dnf:3",
		channel:                         channelcatalog.Channel{ID: 11},
		residentChannel:                 channelcatalog.Channel{ID: 11},
		selectedCharacterID:             31,
		characterGeneration:             1,
		townSceneReadyCharacterID:       31,
		townActorOwnerChannel:           11,
		connectionTownActorOwnerChannel: 11,
	}
	s.registerLiveGameSession(viewer2)
	s.mu.Lock()
	s.gameSessions[31] = viewer2
	s.mu.Unlock()

	f.target.channel.ID, f.target.connectionTownActorOwnerChannel = 11, 11

	charCounter := &p0CountingCharRepo{CharacterRepository: repos.Character}
	skillCounter := &p0CountingSkillRepo{SkillRepository: repos.Skill}
	settingsCounter := &p0CountingSettingsRepo{SettingsRepository: repos.Settings}
	repos.Character = charCounter
	repos.Skill = skillCounter
	repos.Settings = settingsCounter
	s.repositoryProvider = func() (dnfrepo.Group, bool) { return repos, true }

	// 1. Viewer 1 opens guild roster -> cold-builds actor 17 and viewer 31 once each.
	f.targetConn.write.Reset()
	if err := s.handleCurrentGuildMemberListForOpcode(f.target, uint16(dnfenum.CmdPacketGuildAllMemberList)); err != nil {
		t.Fatalf("viewer1 roster failed: %v", err)
	}
	if got := countSilentMode8Frames(t, f.targetConn.write.Bytes()); got != 2 {
		t.Fatalf("viewer1 expected 2 mode8 frames (actor 17 + viewer 31), got %d", got)
	}
	if charCounter.fullLoadsFor("17") != 1 || charCounter.fullLoadsFor("31") != 1 {
		t.Fatalf("expected 1 cold full Character.Load each for 17 and 31, got 17=%d 31=%d",
			charCounter.fullLoadsFor("17"), charCounter.fullLoadsFor("31"))
	}
	// Lightweight cacheOnly mode8 skips equipment, skill, and buff_swap loads completely.
	if skillCounter.loadsFor("17") != 0 || settingsCounter.buffSwapLoads.Load() != 0 {
		t.Fatalf("lightweight cacheOnly mode8 priming expected skill(17)=0 buffSwap=0, got skill=%d buffSwap=%d",
			skillCounter.loadsFor("17"), settingsCounter.buffSwapLoads.Load())
	}

	// 2. Viewer 2 opens guild roster -> primes actor 17 from guildMode8Cache (0 extra full/skill loads!) and cold-builds viewer 29.
	beforeV2Char29 := charCounter.fullLoadsFor("29")
	viewer2Conn.write.Reset()
	if err := s.handleCurrentGuildMemberListForOpcode(viewer2, uint16(dnfenum.CmdPacketGuildAllMemberList)); err != nil {
		t.Fatalf("viewer2 roster failed: %v", err)
	}
	if got := countSilentMode8Frames(t, viewer2Conn.write.Bytes()); got != 2 {
		t.Fatalf("viewer2 expected 2 mode8 frames (actor 17 + viewer 29), got %d", got)
	}
	if charCounter.fullLoadsFor("17") != 1 || skillCounter.loadsFor("17") != 0 {
		t.Fatalf("cached actor 17 should not repeat full/skill loads for viewer2, got char=%d skill=%d",
			charCounter.fullLoadsFor("17"), skillCounter.loadsFor("17"))
	}
	if charCounter.fullLoadsFor("29") != beforeV2Char29+1 {
		t.Fatalf("cold peer 29 expected +1 full Character.Load, got delta=%d", charCounter.fullLoadsFor("29")-beforeV2Char29)
	}

	// 3. Appearance revision bump on actor 17 invalidates cache -> rebuilds once.
	actorSession := s.gameSessions[17]
	actorSession.crossSceneAppearanceMu.Lock()
	actorSession.crossSceneAppearanceRevision++
	actorSession.crossSceneAppearanceMu.Unlock()
	f.targetConn.write.Reset()
	if err := s.handleCurrentGuildMemberListForOpcode(f.target, uint16(dnfenum.CmdPacketGuildAllMemberList)); err != nil {
		t.Fatalf("viewer1 roster after appearance bump failed: %v", err)
	}
	if got := countSilentMode8Frames(t, f.targetConn.write.Bytes()); got != 1 {
		t.Fatalf("expected 1 rebuilt mode8 frame for actor 17, got %d", got)
	}
	if charCounter.fullLoadsFor("17") != 2 {
		t.Fatalf("appearance bump should rebuild actor 17 once, got full=%d want=2", charCounter.fullLoadsFor("17"))
	}

	// 4. Real Inspect (cacheOnly=false) still loads peer skill page in addition to skillTreeWireIndex.
	skillBeforeInspect := skillCounter.loadsFor("17")
	f.targetConn.write.Reset()
	inspectReq := []byte{11, 17, 0, 0, 1}
	if err := s.handleGameCommand(f.target, byte(dnfenum.GameCmdCommand), currentGuildMemberProfileOpcode, inspectReq); err != nil {
		t.Fatalf("real inspect failed: %v", err)
	}
	if delta := skillCounter.loadsFor("17") - skillBeforeInspect; delta < 2 {
		t.Fatalf("real inspect (cacheOnly=false) should load peer skill page + tree index (delta>=2), got delta=%d", delta)
	}

	// 5. Deleting actor 17 while cached must STILL be rejected at live validation.
	resetCurrentGuildPresenceRefresh(viewer2, false)
	c17, _, err := charCounter.CharacterRepository.Load(ctx, "17")
	if err != nil {
		t.Fatal(err)
	}
	c17.Stats["delete_flag"] = 1
	if err := charCounter.CharacterRepository.Save(ctx, c17); err != nil {
		t.Fatal(err)
	}
	viewer2Conn.write.Reset()
	if err := s.handleCurrentGuildMemberListForOpcode(viewer2, uint16(dnfenum.CmdPacketGuildAllMemberList)); err != nil {
		t.Fatalf("viewer2 roster after actor deletion failed: %v", err)
	}
	// Only viewer 29 should be primed; deleted actor 17 must NOT be sent even though cached.
	if got := countSilentMode8Frames(t, viewer2Conn.write.Bytes()); got != 1 {
		t.Fatalf("expected only 1 mode8 frame (viewer 29) after actor 17 deleted, got %d", got)
	}
}

func TestP0GuildSameChannelPrimePaginationSkippedAndBudgetBounded(t *testing.T) {
	f, repos, viewer := setupP1WeddingGuildRosterFixture(t)
	s := f.service
	ctx := context.Background()

	viewer.Stats["wedding_v1_phase"] = 0
	if err := dnfrepo.SaveCharacterFields(ctx, repos.Character, viewer, dnfrepo.CharacterFieldWedding); err != nil {
		t.Fatal(err)
	}
	f.target.channel.ID, f.target.connectionTownActorOwnerChannel = 11, 11

	// 1. Pagination continuation (CmdPacketGuildAllMemberListNext, full=false) must NOT prime peers.
	f.targetConn.write.Reset()
	if err := s.handleCurrentGuildMemberListForOpcode(f.target, uint16(dnfenum.CmdPacketGuildAllMemberListNext)); err != nil {
		t.Fatalf("pagination guild roster failed: %v", err)
	}
	if got := countSilentMode8Frames(t, f.targetConn.write.Bytes()); got != 0 {
		t.Fatalf("pagination guild roster must not emit mode8 primes, got %d", got)
	}

	// 2. Budget guard: if ctx remaining deadline is below guildSameChannelPrimeMinCtxHeadway,
	// priming stops cleanly without touching peers or canceling ctx.
	bound, ok := s.boundGameSessionCharacterSnapshot(f.target)
	if !ok {
		t.Fatal("missing bound snapshot")
	}
	display, err := s.readCurrentGuildDisplay(ctx, bound, false, true)
	if err != nil {
		t.Fatal(err)
	}
	tightCtx, cancel := context.WithTimeout(ctx, 200*time.Millisecond)
	defer cancel()
	f.targetConn.write.Reset()
	s.primeCurrentSameChannelGuildPartyPeers(tightCtx, bound, display)
	if tightCtx.Err() != nil {
		t.Fatalf("tight budget check should stop before deadline expires, got ctx.Err()=%v", tightCtx.Err())
	}
	if got := countSilentMode8Frames(t, f.targetConn.write.Bytes()); got != 0 {
		t.Fatalf("tight deadline below headway must skip priming cleanly, got %d frames", got)
	}
}
