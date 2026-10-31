package dnfbridge

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"math"
	"strconv"
	"time"

	"longheng.io/server/internal/modules/dnf/dnfenum"
	"longheng.io/server/internal/modules/dnf/raid"
	"longheng.io/server/internal/modules/dnf/raid/fiendwar"
)

// The online raid owns these values. Notifications are projections of this
// instance, never substitutes for an admission transaction or a cleared map.
type runtimeRaidBattle struct {
	FiendWar               *fiendwar.FiendWarBattle
	FiendWarConfig         *fiendwar.FiendWarConfig
	FiendWarEndedAt        time.Time
	FiendWarMaps           map[string]map[int64]bool
	FiendWarEncounters     map[int]currentFiendWarEncounter
	FiendWarRetreatRuns    map[string]bool
	FiendWarClearCounts    map[uint16]uint32
	FiendWarAdmissionClaim currentFiendWarAdmissionClaim
	LukeMemberRoutes       map[uint16]currentLukeMemberRoute
	LukeSanctumMazeGroup   *currentLukeSanctumMazeGroup
	InstanceID             string
	Mode                   byte
	ChannelID              int
	ChannelType            byte
	Single                 bool
	Phase                  byte
	StartedAt              time.Time
	EnterAfter             time.Time
	Deadline               time.Time
	Configuration          raid.BattleConfiguration
	Dungeons               []raid.DungeonState
	SingleSequence         []uint32
	SingleIndex            int
	// Frozen from the single catalog and symbol table before admission cost.
	SingleHPSymbols map[uint32]uint32
	// Shared literal/symbol/timer executor; Mode 2 must carry an actual Luke
	// program. The legacy field name does not select Anton's game rules.
	Anton          *raid.AntonProgression
	AntonRewards   *raid.AntonRewardConfiguration
	MemberAccounts map[uint16]string
	MemberGroups   map[uint16]byte
	ClearedRuns    map[string]currentRaidClearedRun
	// Return-only receipts for terminal maps invalidated by the phase-zero timer.
	// They never count as clears or authorize another dungeon selection.
	ExpiredCompletedRuns map[string]currentRaidClearedRun
	RunDepartures        map[string]map[uint16]bool
	Participation        map[uint16]currentRaidParticipation
	MemberClears         map[uint16]uint32
	MemberLives          map[currentRaidMemberRun]currentRaidMemberLife
	PhaseDeaths          [2]uint32
	PhaseCoins           [2]uint32
	RunCoins             map[string]uint32
	ProgressVersion      uint64
	PhaseCompleted       bool
	PhaseCompletedAt     time.Time
	TimedOut             bool
	RewardCardReady      [2]map[uint16]bool
	RewardCards          [2]map[uint16]*currentAntonRewardCard
	AntonFlowConfig      raid.AntonFlowConfiguration
	RewardFlow           [2]*currentAntonRewardFlow
}

func cloneRuntimeRaidBattle(b *runtimeRaidBattle) *runtimeRaidBattle {
	if b == nil {
		return nil
	}
	v := *b
	v.FiendWar = b.FiendWar.Clone()
	v.FiendWarMaps = map[string]map[int64]bool{}
	for run, rows := range b.FiendWarMaps {
		v.FiendWarMaps[run] = map[int64]bool{}
		for id, done := range rows {
			v.FiendWarMaps[run][id] = done
		}
	}
	v.FiendWarEncounters = map[int]currentFiendWarEncounter{}
	v.FiendWarRetreatRuns = map[string]bool{}
	for run, retreat := range b.FiendWarRetreatRuns {
		v.FiendWarRetreatRuns[run] = retreat
	}
	for area, e := range b.FiendWarEncounters {
		e.Participants = append([]uint16(nil), e.Participants...)
		v.FiendWarEncounters[area] = e
	}
	v.FiendWarAdmissionClaim.Participants = append([]uint16(nil), b.FiendWarAdmissionClaim.Participants...)
	v.FiendWarAdmissionClaim.Members = append([]currentFiendWarAdmissionMember(nil), b.FiendWarAdmissionClaim.Members...)
	v.FiendWarClearCounts = make(map[uint16]uint32, len(b.FiendWarClearCounts))
	for id, n := range b.FiendWarClearCounts {
		v.FiendWarClearCounts[id] = n
	}
	v.LukeMemberRoutes = make(map[uint16]currentLukeMemberRoute, len(b.LukeMemberRoutes))
	for id, route := range b.LukeMemberRoutes {
		v.LukeMemberRoutes[id] = route
	}
	if b.LukeSanctumMazeGroup != nil {
		group := *b.LukeSanctumMazeGroup
		group.Configuration = group.Configuration.Clone()
		v.LukeSanctumMazeGroup = &group
	}
	v.Configuration.PhaseCoinLimits = make(map[byte]uint32, len(b.Configuration.PhaseCoinLimits))
	for k, n := range b.Configuration.PhaseCoinLimits {
		v.Configuration.PhaseCoinLimits[k] = n
	}
	v.RunCoins = make(map[string]uint32, len(b.RunCoins))
	for k, n := range b.RunCoins {
		v.RunCoins[k] = n
	}
	v.Participation = make(map[uint16]currentRaidParticipation, len(b.Participation))
	for id, entry := range b.Participation {
		v.Participation[id] = entry
	}
	v.Configuration.PhaseLimitSeconds = append([]uint32(nil), b.Configuration.PhaseLimitSeconds...)
	v.Configuration.PhaseDungeons = make([][]raid.DungeonState, len(b.Configuration.PhaseDungeons))
	for i := range b.Configuration.PhaseDungeons {
		v.Configuration.PhaseDungeons[i] = append([]raid.DungeonState(nil), b.Configuration.PhaseDungeons[i]...)
	}
	v.Dungeons = append([]raid.DungeonState(nil), b.Dungeons...)
	v.SingleSequence = append([]uint32(nil), b.SingleSequence...)
	v.SingleHPSymbols = make(map[uint32]uint32, len(b.SingleHPSymbols))
	for dungeon, symbol := range b.SingleHPSymbols {
		v.SingleHPSymbols[dungeon] = symbol
	}
	v.Anton = b.Anton.Clone()
	if b.AntonRewards != nil {
		configuration := b.AntonRewards.Clone()
		v.AntonRewards = &configuration
	}
	v.MemberAccounts = make(map[uint16]string, len(b.MemberAccounts))
	v.MemberGroups = make(map[uint16]byte, len(b.MemberGroups))
	for id, group := range b.MemberGroups {
		v.MemberGroups[id] = group
	}
	for id, account := range b.MemberAccounts {
		v.MemberAccounts[id] = account
	}
	v.ClearedRuns = make(map[string]currentRaidClearedRun, len(b.ClearedRuns))
	for id, run := range b.ClearedRuns {
		run.Participants = append([]uint16(nil), run.Participants...)
		v.ClearedRuns[id] = run
	}
	v.ExpiredCompletedRuns = make(map[string]currentRaidClearedRun, len(b.ExpiredCompletedRuns))
	for id, run := range b.ExpiredCompletedRuns {
		run.Participants = append([]uint16(nil), run.Participants...)
		v.ExpiredCompletedRuns[id] = run
	}
	v.MemberClears = make(map[uint16]uint32, len(b.MemberClears))
	v.RunDepartures = make(map[string]map[uint16]bool, len(b.RunDepartures))
	for run, members := range b.RunDepartures {
		v.RunDepartures[run] = map[uint16]bool{}
		for id, left := range members {
			v.RunDepartures[run][id] = left
		}
	}
	for id, count := range b.MemberClears {
		v.MemberClears[id] = count
	}
	v.MemberLives = make(map[currentRaidMemberRun]currentRaidMemberLife, len(b.MemberLives))
	for key, life := range b.MemberLives {
		v.MemberLives[key] = life
	}
	for phase := range b.RewardCards {
		v.RewardFlow[phase] = cloneCurrentAntonRewardFlow(b.RewardFlow[phase])
		v.RewardCardReady[phase] = make(map[uint16]bool, len(b.RewardCardReady[phase]))
		for id, ready := range b.RewardCardReady[phase] {
			v.RewardCardReady[phase][id] = ready
		}
		v.RewardCards[phase] = make(map[uint16]*currentAntonRewardCard, len(b.RewardCards[phase]))
		for id, card := range b.RewardCards[phase] {
			if card != nil {
				copy := *card
				copy.Plan = cloneCurrentAntonRewardPlan(card.Plan)
				v.RewardCards[phase][id] = &copy
			}
		}
	}
	return &v
}

// Caller holds raidMu. Resolve every dependency before spending materials, so
// missing PVF links, exhausted weekly limits or an invalid owner cannot charge
// a raid which has no usable battle instance.
func (s *Service) prepareCurrentRaidBattle(session *gameSession, state runtimeRaidState) (*runtimeRaidBattle, error) {
	if s.options.currentClientVersion != "official-24.9" {
		return nil, nil
	}
	if !s.currentRaidAuthenticated(session) {
		return nil, fmt.Errorf("raid battle authenticated owner unavailable")
	}
	if isCurrentFiendWarChannel(session) {
		return s.prepareCurrentFiendWarBattle(session, state)
	}
	path := ""
	switch session.channel.Type {
	case dnfenum.RaidChannelType:
		if state.RouteOrRaidType != 0 {
			return nil, fmt.Errorf("raid route/channel mismatch")
		}
		path = "etc/raid/anton.etc"
	case dnfenum.HiddenRaidType:
		if state.RouteOrRaidType != 1 {
			return nil, fmt.Errorf("raid route/channel mismatch")
		}
		path = "etc/raid/luke/luke.etc"
	default:
		return nil, fmt.Errorf("raid battle channel unsupported")
	}
	s.initialEquipmentMu.Lock()
	archive, err := s.initialEquipmentArchiveLocked()
	s.initialEquipmentMu.Unlock()
	if err != nil {
		return nil, err
	}
	config, err := raid.LoadBattleConfiguration(archive, path)
	if err != nil {
		return nil, err
	}
	policy, err := currentRaidWeeklyPolicy()
	if err != nil {
		return nil, err
	}
	repos, ok := s.repositoryGroup()
	if !ok || repos.Character == nil {
		return nil, fmt.Errorf("raid battle character repository unavailable")
	}
	ctx, cancel := context.WithTimeout(context.Background(), createWriteTimeout)
	defer cancel()
	now := time.Now()
	mode := state.RouteOrRaidType + 1
	leaderUsed := -1
	memberAccounts := make(map[uint16]string, len(state.Members))
	memberGroups := make(map[uint16]byte, len(state.Members))
	for _, member := range state.Members {
		peer, found := s.onlineGameSession(member.CharID)
		if member.CharID == session.selectedCharacterID {
			peer, found = session, true
		}
		if !found || !s.currentRaidAuthenticated(peer) || peer.channel.ID != session.channel.ID || peer.channel.Type != session.channel.Type {
			return nil, fmt.Errorf("raid battle member is not authenticated in this channel: %d", member.CharID)
		}
		character, found, err := repos.Character.Load(ctx, strconv.Itoa(int(member.CharID)))
		if err != nil {
			return nil, err
		}
		if !found || character.AccountID != s.accountIDForSession(peer) {
			return nil, fmt.Errorf("raid battle member/account mismatch: %d", member.CharID)
		}
		memberAccounts[member.CharID] = character.AccountID
		memberGroups[member.CharID] = member.GroupIndex
		used, err := raid.WeeklyClears(character, mode, now, policy)
		if err != nil {
			return nil, err
		}
		if used >= config.WeeklyClearLimit {
			return nil, raid.ErrWeeklyClearLimit
		}
		if member.CharID == state.LeaderID {
			leaderUsed = used
		}
	}
	if leaderUsed < 0 {
		return nil, fmt.Errorf("raid battle leader missing")
	}
	single := state.TailFlag == 1
	if state.CurrentLayout {
		single = state.CurrentUIFlags[0] == 1
	}
	b := &runtimeRaidBattle{Mode: mode, ChannelID: session.channel.ID, ChannelType: session.channel.Type, Single: single, StartedAt: now, Configuration: config,
		MemberAccounts: memberAccounts,
		MemberGroups:   memberGroups,
		EnterAfter:     now.Add(time.Duration(config.StartDelaySeconds) * time.Second),
		Dungeons:       append([]raid.DungeonState(nil), config.PhaseDungeons[0]...)}
	if single {
		if len(state.Members) != 1 {
			return nil, fmt.Errorf("single raid requires exactly one member")
		}
		if _, err := raid.ValidateSingleRaidDependenciesForRoute(archive, state.RouteOrRaidType); err != nil {
			return nil, fmt.Errorf("raid dungeon single catalog unavailable: %w", err)
		}
		b.SingleSequence, err = raid.LoadSingleRaidSequence(archive, state.RouteOrRaidType, leaderUsed)
		if err != nil {
			return nil, err
		}
		if mode == 1 {
			b.SingleHPSymbols, err = raid.LoadSingleAntonHPSymbols(archive, config.WeeklyClearLimit)
			if err != nil {
				return nil, fmt.Errorf("raid dungeon single HP catalog unavailable: %w", err)
			}
		}
		b.Dungeons = make([]raid.DungeonState, len(b.SingleSequence))
		for i, id := range b.SingleSequence {
			status := byte(1)
			if i == 0 {
				status = 0
			}
			b.Dungeons[i] = raid.DungeonState{ID: id, State: status}
		}
	}
	var dependencyIDs []uint32
	if single {
		dependencyIDs = b.SingleSequence
	} else {
		for _, phase := range config.PhaseDungeons {
			for _, dungeon := range phase {
				dependencyIDs = append(dependencyIDs, dungeon.ID)
			}
		}
	}
	if err := raid.ValidateDungeonDependencies(archive, dependencyIDs); err != nil {
		return nil, err
	}
	b.Deadline = b.EnterAfter.Add(time.Duration(config.PhaseLimitSeconds[0]) * time.Second)
	if b.Deadline.Unix() > math.MaxUint32 {
		return nil, fmt.Errorf("raid battle deadline exceeds native timestamp")
	}
	var instance [16]byte
	if _, err := rand.Read(instance[:]); err != nil {
		return nil, err
	}
	b.InstanceID = hex.EncodeToString(instance[:])
	if single && mode == 1 {
		flow, err := raid.LoadAntonFlowConfiguration(archive)
		if err != nil {
			return nil, err
		}
		catalog, err := newPVFDungeonDropCatalog(archive)
		if err != nil {
			return nil, err
		}
		rewards, err := loadCurrentSingleAntonRewards(catalog, b.SingleSequence, now)
		if err != nil {
			return nil, err
		}
		b.AntonFlowConfig, b.AntonRewards = flow, &rewards
	}
	if single && mode == 2 {
		// Single Luke uses one terminal sequence. These root movie settings
		// drive the existing automatic-card policy; no normal phase is started.
		flow, err := raid.LoadLukeFlowConfiguration(archive)
		if err != nil {
			return nil, err
		}
		catalog, err := newPVFDungeonDropCatalog(archive)
		if err != nil {
			return nil, err
		}
		rewards, err := loadCurrentSingleLukeRewards(catalog, b.SingleSequence, now)
		if err != nil {
			return nil, err
		}
		b.AntonFlowConfig, b.AntonRewards = flow, &rewards
	}
	if !single && mode == 1 {
		flowConfiguration, err := raid.LoadAntonFlowConfiguration(archive)
		if err != nil {
			return nil, err
		}
		b.AntonFlowConfig = flowConfiguration
		rewardConfiguration, err := raid.LoadAntonRewardConfiguration(archive)
		if err != nil {
			return nil, err
		}
		rewardCatalog, err := newPVFDungeonDropCatalog(archive)
		if err != nil {
			return nil, err
		}
		if err := validateCurrentAntonRewardDependencies(rewardCatalog, rewardConfiguration, now); err != nil {
			return nil, err
		}
		b.AntonRewards = &rewardConfiguration
		program, err := raid.LoadAntonProgram(archive)
		if err != nil {
			return nil, err
		}
		b.Anton, err = program.BeginPhase(0, b.EnterAfter, binary.LittleEndian.Uint64(instance[:8]))
		if err != nil {
			return nil, err
		}
		b.Dungeons = b.Anton.Dungeons()
	}
	if !single && mode == 2 {
		flow, err := raid.LoadLukeFlowConfiguration(archive)
		if err != nil {
			return nil, err
		}
		rewards, err := raid.LoadLukeRewardConfiguration(archive)
		if err != nil {
			return nil, err
		}
		catalog, err := newPVFDungeonDropCatalog(archive)
		if err != nil {
			return nil, err
		}
		if err := validateCurrentAntonRewardDependencies(catalog, rewards, now); err != nil {
			return nil, err
		}
		program, err := raid.LoadLukeProgram(archive)
		if err != nil {
			return nil, err
		}
		progress, err := program.BeginPhase(b.EnterAfter, binary.LittleEndian.Uint64(instance[:8]))
		if err != nil {
			return nil, err
		}
		b.Anton, b.AntonRewards, b.AntonFlowConfig = progress.AntonProgression, &rewards, flow
		b.Dungeons = progress.Dungeons()
	}
	return b, nil
}

func (s *Service) sendCurrentRaidBattleState(session *gameSession, state runtimeRaidState) error {
	if s.options.currentClientVersion != "official-24.9" {
		return nil
	}
	b := state.Battle
	if b == nil || !state.Started || !s.currentRaidAuthenticated(session) || runtimeRaidMemberIndex(state, session.selectedCharacterID) < 0 {
		return fmt.Errorf("raid battle projection owner unavailable")
	}
	if b.MemberAccounts[session.selectedCharacterID] != s.accountIDForSession(session) {
		return fmt.Errorf("raid battle frozen account mismatch")
	}
	if b.FiendWar != nil {
		session.raidProjection.mu.Lock()
		defer session.raidProjection.mu.Unlock()
		return s.sendCurrentFiendWarProjectionLocked(session, state, true, false)
	}
	session.raidProjection.mu.Lock()
	defer session.raidProjection.mu.Unlock()
	if session.raidProjection.failed {
		return fmt.Errorf("raid projection transport previously failed")
	}
	var err error
	state, err = s.latestCurrentRaidProjectionState(session, state)
	if err != nil {
		return err
	}
	b = state.Battle
	identity := currentRaidProjectionIdentity{InstanceID: b.InstanceID, Phase: b.Phase, CharID: session.selectedCharacterID, Generation: session.characterGeneration}
	if session.raidProjection.initialized && session.raidProjection.identity == identity && session.raidProjection.startTransitionSent {
		return s.sendCurrentRaidProgressProjectionLocked(session, state, false, false)
	}
	if b.Phase >= byte(len(b.Configuration.PhaseLimitSeconds)) {
		return fmt.Errorf("raid battle phase clock unavailable")
	}
	if b.Single && b.Mode == 1 {
		// Validate the entire HP projection before sending the first start
		// packet; a broken frozen owner cannot leave a partial start on wire.
		if _, err := currentSingleAntonHPBody(b); err != nil {
			return err
		}
	}
	infection := uint32(0)
	if b.Anton != nil {
		infection = b.Anton.InfectionDungeon()
	}
	mapBody, err := currentRaidDungeonProjectionBody(b, infection)
	if err != nil {
		return err
	}
	seconds := int64(time.Until(b.Deadline).Seconds())
	if seconds < 0 {
		seconds = 0
	}
	duration := currentRaidNativeDuration(b.Mode, b.Deadline, b.Configuration.PhaseLimitSeconds[b.Phase]+b.Configuration.StartDelaySeconds, time.Now())
	for _, packet := range []struct {
		opcode uint16
		body   []byte
	}{
		{585, mapBody},
		{587, []byte{2, b.Phase}},
		{596, raid.BuildTimerBody(0, 0, uint32(b.Deadline.Unix()), duration)},
		{597, raid.BuildRemainingTimeBody(0, uint32(seconds))},
	} {
		if err := s.sendGameUpperRawClassCodec(session, packet.opcode, packet.body, 0, true); err != nil {
			session.raidProjection.failed = true
			if session.conn != nil {
				_ = session.conn.Close()
			}
			return err
		}
	}
	// The 585 above is already the full map for this identity. Seed the delta
	// owner so the progress projection below does not publish it a second time
	// in the same round and re-trigger the native state-3 prompt.
	if err := session.raidProjection.recordCurrentRaidMapProjection(identity, mapBody); err != nil {
		return err
	}
	if b.Anton != nil || (b.Single && b.Mode == 1) {
		if err := s.sendCurrentRaidProgressProjectionLocked(session, state, b.ProgressVersion == 0, false); err != nil {
			return err
		}
	} else {
		session.raidProjection.identity, session.raidProjection.version, session.raidProjection.initialized = identity, b.ProgressVersion, true
	}
	session.raidProjection.startTransitionSent = true
	s.logGameEvent(session, "game-raid-battle-state-sent", "raid_key", state.RaidKey, "mode", b.Mode,
		"single", b.Single, "phase", b.Phase, "dungeon_count", len(b.Dungeons), "remaining_seconds", seconds)
	return nil
}

func (s *Service) currentRaidDungeonSelectionAllowed(session *gameSession, dungeonID uint32) bool {
	if s.options.currentClientVersion != "official-24.9" || session == nil ||
		(session.channel.Type != dnfenum.RaidChannelType && session.channel.Type != dnfenum.HiddenRaidType && !isCurrentFiendWarChannel(session)) {
		return true
	}
	if !s.currentRaidAuthenticated(session) {
		return false
	}
	s.raidMu.Lock()
	defer s.raidMu.Unlock()
	state := s.runtimeRaidByMemberLocked(session.selectedCharacterID)
	if state == nil || !state.Started || state.Battle == nil {
		return false
	}
	b := state.Battle
	now := time.Now()
	if b.ChannelID != session.channel.ID || b.ChannelType != session.channel.Type ||
		b.MemberAccounts[session.selectedCharacterID] != s.accountIDForSession(session) {
		return false
	}
	if b.PhaseCompleted || b.TimedOut || now.Before(b.EnterAfter) || !now.Before(b.Deadline) {
		return false
	}
	if b.FiendWar != nil && !currentFiendWarSelectionAllowed(b, session.selectedCharacterID, int(dungeonID)) {
		return false
	}
	if !s.currentLukeRouteAllowedLocked(b, session.selectedCharacterID, dungeonID) {
		return false
	}
	for _, dungeon := range b.Dungeons {
		if dungeon.ID == dungeonID {
			return dungeon.State == 0
		}
	}
	return false
}
