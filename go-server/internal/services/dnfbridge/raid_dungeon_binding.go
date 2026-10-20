package dnfbridge

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"time"

	"longheng.io/server/internal/modules/dnf/raid"

	"longheng.io/server/internal/modules/dnf/dnfenum"
)

type currentRaidDungeonEntry struct {
	RaidKey     uint32
	InstanceID  string
	Phase       byte
	Attempt     uint64
	ChannelID   int
	ChannelType byte
	// One random run identity is shared by the party's prepared runtimes.
	// Every participant's boss completion therefore reduces this run once.
	RunID        string
	Participants []uint16
	// This admitted mode stays with the map after global raid progress changes.
	teamMode        bool
	singleAntonMode bool
}

// Team raids and single Anton do not create floor items or gold. Keep this rule on the frozen
// entry, so an expired or retired battle cannot restore ordinary map drops.
func currentTeamRaidGroundDropsDisabled(runtime *runtimeDungeonState) bool {
	return runtime != nil && runtime.raidEntry != nil && (runtime.raidEntry.teamMode || runtime.raidEntry.singleAntonMode)
}

func (s *Service) currentRaidDungeonBindingRequired(session *gameSession) bool {
	return s.options.currentClientVersion == "official-24.9" && session != nil &&
		(session.channel.Type == dnfenum.RaidChannelType || session.channel.Type == dnfenum.HiddenRaidType)
}

// Prepared raid entries remain raid-owned even if the current channel changed
// after selection. An ordinary, unbound dungeon without an active raid keeps
// its existing entry path.
func (s *Service) currentRaidDungeonEntryBindingRequired(session *gameSession, runtime *runtimeDungeonState) bool {
	if s.options.currentClientVersion != "official-24.9" || session == nil {
		return false
	}
	if s.currentRaidDungeonBindingRequired(session) || (runtime != nil && runtime.raidEntry != nil) {
		return true
	}
	s.raidMu.Lock()
	defer s.raidMu.Unlock()
	state := s.runtimeRaidByMemberLocked(session.selectedCharacterID)
	return state != nil && state.Started && state.Battle != nil
}

// The leader freezes the complete participant set before committing any member.
// A follower's first pass through the op28 barrier consumes that same binding;
// freezing it again as a one-member group would reject every multiplayer run.
func (s *Service) prepareCurrentRaidDungeonEntry(session *gameSession, runtime *runtimeDungeonState, prepared *runtimePartyPreparedDungeonEntry, followers []*runtimePartyPreparedDungeonEntry) error {
	if prepared == nil {
		return s.freezeCurrentRaidDungeonGroupEntry(session, runtime, followers)
	}
	if prepared.session != session || prepared.runtime != runtime || len(followers) != 0 {
		return fmt.Errorf("prepared raid follower owner changed")
	}
	return s.validateCurrentRaidDungeonEntry(session, runtime)
}

// This preflight runs before any follower/runtime/entry-item commit. It freezes
// all prepared members together, using the admitted raid accounts and actual
// party group, rather than a subsequent lookup of whichever raid they joined.
func (s *Service) freezeCurrentRaidDungeonGroupEntry(session *gameSession, runtime *runtimeDungeonState, followers []*runtimePartyPreparedDungeonEntry) error {
	if !s.currentRaidDungeonEntryBindingRequired(session, runtime) {
		return nil
	}
	s.raidMu.Lock()
	defer s.raidMu.Unlock()
	state := s.runtimeRaidByMemberLocked(session.selectedCharacterID)
	if state == nil || !state.Started || state.Battle == nil {
		return fmt.Errorf("raid entry instance unavailable")
	}
	b := state.Battle
	if b.ChannelID != session.channel.ID || b.ChannelType != session.channel.Type {
		return fmt.Errorf("raid entry frozen admission channel mismatch")
	}
	now := time.Now()
	if b.PhaseCompleted || b.TimedOut || now.Before(b.EnterAfter) || !now.Before(b.Deadline) {
		return fmt.Errorf("raid entry instance is not active")
	}
	memberIndex := runtimeRaidMemberIndex(*state, session.selectedCharacterID)
	if memberIndex < 0 {
		return fmt.Errorf("raid entry member unavailable")
	}
	targets := []*runtimePartyPreparedDungeonEntry{{session: session, runtime: runtime}}
	targets = append(targets, followers...)
	if b.Single && len(targets) != 1 {
		return fmt.Errorf("single raid entry includes party followers")
	}
	attempt := uint64(1)
	if b.Anton != nil {
		attempt = b.Anton.Epoch()
	}
	seen := map[uint16]bool{}
	for _, target := range targets {
		if target == nil || target.session == nil || target.runtime == nil || !s.currentRaidAuthenticated(target.session) {
			return fmt.Errorf("raid entry prepared owner unavailable")
		}
		peer, r := target.session, target.runtime
		if err := validateCurrentLukeSanctumMazePreparation(state, r); err != nil {
			return err
		}
		id := peer.selectedCharacterID
		index := runtimeRaidMemberIndex(*state, id)
		if seen[id] || index < 0 || state.Members[index].GroupIndex != state.Members[memberIndex].GroupIndex ||
			peer.channel.ID != session.channel.ID || peer.channel.Type != session.channel.Type ||
			b.MemberAccounts[id] == "" || b.MemberAccounts[id] != s.accountIDForSession(peer) ||
			r.AccountID != b.MemberAccounts[id] || !dungeonRuntimeOwnsCharacter(r, id) || r.Dungeon.ID != runtime.Dungeon.ID {
			return fmt.Errorf("raid entry prepared group/account mismatch")
		}
		seen[id] = true
		if !s.currentLukeRouteAllowedLocked(b, id, uint32(r.Dungeon.ID)) {
			return fmt.Errorf("Luke prepared member belongs to the other route")
		}
		open := false
		for _, d := range b.Dungeons {
			if int64(d.ID) == r.Dungeon.ID && d.State == 0 {
				open = true
				break
			}
		}
		if !open {
			return fmt.Errorf("raid entry dungeon is not open")
		}
		if r.raidEntry != nil && (r.raidEntry.RaidKey != state.RaidKey || r.raidEntry.InstanceID != b.InstanceID || r.raidEntry.Phase != b.Phase || r.raidEntry.Attempt != attempt || r.raidEntry.RunID == "" || r.raidEntry.ChannelID != session.channel.ID || r.raidEntry.ChannelType != session.channel.Type) {
			return fmt.Errorf("raid entry was prepared for another instance/attempt")
		}
	}
	var entry currentRaidDungeonEntry
	if runtime.raidEntry != nil {
		entry = *runtime.raidEntry
	} else {
		var run [16]byte
		if _, err := rand.Read(run[:]); err != nil {
			return err
		}
		entry = currentRaidDungeonEntry{RaidKey: state.RaidKey, InstanceID: b.InstanceID, Phase: b.Phase, Attempt: attempt, RunID: hex.EncodeToString(run[:]), ChannelID: session.channel.ID, ChannelType: session.channel.Type}
		for _, target := range targets {
			entry.Participants = append(entry.Participants, target.session.selectedCharacterID)
		}
	}
	if len(entry.Participants) != len(targets) {
		return fmt.Errorf("raid prepared participant set changed")
	}
	for _, id := range entry.Participants {
		if !seen[id] {
			return fmt.Errorf("raid prepared participant owner changed")
		}
	}
	for _, target := range targets {
		if existing := target.runtime.raidEntry; existing != nil &&
			(existing.RunID != entry.RunID || !sameRaidParticipants(existing.Participants, entry.Participants)) {
			return fmt.Errorf("raid prepared runtimes belong to different runs")
		}
	}
	if b.Mode == 2 && !b.Single {
		if !currentRaidHasTwoPhaseProgram(b) {
			return fmt.Errorf("Luke reservation program missing")
		}
		if now.Before(b.Anton.LastTime()) {
			now = b.Anton.LastTime()
		}
		next, err := (&raid.LukeProgression{AntonProgression: b.Anton}).ReserveDungeon(entry.RunID, uint32(runtime.Dungeon.ID), entry.Attempt, now)
		if err != nil {
			return err
		}
		b.Anton = next.AntonProgression
	}
	entry.teamMode = !b.Single
	entry.singleAntonMode = b.Single && b.Mode == 1
	for _, target := range targets {
		copy := entry
		copy.Participants = append([]uint16(nil), entry.Participants...)
		target.runtime.raidEntry = &copy
		if b.Mode == 1 && !b.Single {
			freezeCurrentAntonMapReturnOwner(target.session, target.runtime)
		}
	}
	return nil
}

// Called again before commit: the original preflight cannot authorize a new
// instance, a phase reset, a replacement authenticated owner or an expired run.
func (s *Service) validateCurrentRaidDungeonEntry(session *gameSession, runtime *runtimeDungeonState) error {
	if !s.currentRaidDungeonEntryBindingRequired(session, runtime) {
		return nil
	}
	if runtime == nil || runtime.raidEntry == nil || !s.currentRaidAuthenticated(session) {
		return fmt.Errorf("raid entry binding unavailable")
	}
	s.raidMu.Lock()
	defer s.raidMu.Unlock()
	state := s.runtimeRaidByMemberLocked(session.selectedCharacterID)
	entry := runtime.raidEntry
	if state == nil || !state.Started || state.Battle == nil || state.RaidKey != entry.RaidKey {
		return fmt.Errorf("raid entry instance was replaced")
	}
	b := state.Battle
	attempt := uint64(1)
	if b.Anton != nil {
		attempt = b.Anton.Epoch()
	}
	now := time.Now()
	if b.InstanceID != entry.InstanceID || b.Phase != entry.Phase || attempt != entry.Attempt ||
		b.ChannelID != entry.ChannelID || b.ChannelType != entry.ChannelType ||
		session.channel.ID != entry.ChannelID || session.channel.Type != entry.ChannelType ||
		entry.RunID == "" || b.MemberAccounts[session.selectedCharacterID] == "" ||
		b.MemberAccounts[session.selectedCharacterID] != runtime.AccountID || runtime.AccountID != s.accountIDForSession(session) ||
		!dungeonRuntimeOwnsCharacter(runtime, session.selectedCharacterID) || b.PhaseCompleted || b.TimedOut || now.Before(b.EnterAfter) || !now.Before(b.Deadline) {
		return fmt.Errorf("raid entry binding/account/attempt expired")
	}
	memberIndex := runtimeRaidMemberIndex(*state, session.selectedCharacterID)
	if memberIndex < 0 || len(entry.Participants) == 0 {
		return fmt.Errorf("raid entry participant owner unavailable")
	}
	seen := map[uint16]bool{}
	for _, id := range entry.Participants {
		index := runtimeRaidMemberIndex(*state, id)
		if seen[id] || index < 0 || state.Members[index].GroupIndex != state.Members[memberIndex].GroupIndex || b.MemberAccounts[id] == "" {
			return fmt.Errorf("raid entry frozen participant group changed")
		}
		seen[id] = true
	}
	if !seen[session.selectedCharacterID] || (b.Single && len(entry.Participants) != 1) {
		return fmt.Errorf("raid entry does not contain its runtime owner")
	}
	if !s.currentLukeRouteAllowedLocked(b, session.selectedCharacterID, uint32(runtime.Dungeon.ID)) {
		return fmt.Errorf("Luke committed member belongs to the other route")
	}
	for _, d := range b.Dungeons {
		if int64(d.ID) == runtime.Dungeon.ID && d.State == 0 {
			return nil
		}
	}
	return fmt.Errorf("raid entry dungeon became unavailable")
}
