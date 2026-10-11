package dnfbridge

import (
	"context"
	"fmt"
	"longheng.io/server/internal/modules/dnf/pvf"
	"longheng.io/server/internal/modules/dnf/raid"
	"longheng.io/server/internal/modules/dnf/worldmap"
	"strings"
)

func currentLukeDungeonCoinLimit(d worldmap.Dungeon) (uint32, error) {
	var limit uint32
	found := false
	for _, sec := range d.SourceSections {
		if !strings.EqualFold(strings.TrimSpace(sec.Name), "coin limit") {
			continue
		}
		if found || len(sec.Tokens) != 1 || sec.Tokens[0].Kind != pvf.TokenInt || sec.Tokens[0].Int < 0 || sec.Tokens[0].Int > 255 {
			return 0, fmt.Errorf("invalid actual Luke dungeon coin limit")
		}
		found = true
		limit = uint32(sec.Tokens[0].Int)
	}
	return limit, nil
}

// Caller holds this session's dungeon.mu. Keep the exact shared raid owner
// fixed through the existing asset transaction; no quota is reserved/refunded.
// raidMu and Service.mu are held during DB work, so slow DB work can delay other
// raids/sessions. The inherited 3s context is cooperative, not a hard preemption
// guarantee for arbitrary repositories or an uncertain SQL commit result.
func (s *Service) consumeCurrentDungeonReviveCoinWithLukeCaps(ctx context.Context, session *gameSession, r *runtimeDungeonState, char uint16) (currentDungeonReviveCoinConsumeResult, error) {
	empty := currentDungeonReviveCoinConsumeResult{}
	if s.options.currentClientVersion != "official-24.9" || session == nil {
		return s.consumeCurrentDungeonReviveCoin(ctx, char)
	}
	// Preserve ordinary and Anton behavior. A frozen Luke entry still requires
	// its owner even if the current session's channel was subsequently changed.
	isLuke := session.channel.Type == 32 || r != nil && r.raidEntry != nil && r.raidEntry.ChannelType == 32
	if !isLuke {
		return s.consumeCurrentDungeonReviveCoin(ctx, char)
	}
	identity, bound := s.boundGameSessionCharacterSnapshot(session)
	if !bound || identity.character != char || !s.currentRaidAuthenticated(session) || r == nil || r.raidEntry == nil || r.Session == nil || r.Session.Snapshot().Run.Status != worldmap.DungeonRunActive || !dungeonRuntimeOwnsCharacter(r, char) || r.townReturnPending || !r.deathReturnWaiting || !r.deathStateSent || !session.postStartMapPlayerStateSent {
		return empty, fmt.Errorf("Luke coin requires accepted death in current committed owned run")
	}
	mapLimit, e := currentLukeDungeonCoinLimit(r.Dungeon)
	if e != nil {
		return empty, e
	}
	now := s.gameplayNow()
	s.raidMu.Lock()
	defer s.raidMu.Unlock()
	state := s.runtimeRaidByMemberLocked(char)
	entry := r.raidEntry
	if state == nil || !state.Started || state.Battle == nil || state.RaidKey != entry.RaidKey {
		return empty, fmt.Errorf("Luke coin raid owner missing")
	}
	b := state.Battle
	if b.Single || b.Mode != 2 || b.Anton == nil || !b.Anton.IsLuke() || b.InstanceID != entry.InstanceID || b.Phase != entry.Phase || b.Phase > 1 || b.Anton.Epoch() != entry.Attempt || entry.RunID == "" || b.ChannelID != entry.ChannelID || b.ChannelType != entry.ChannelType || session.channel.ID != entry.ChannelID || session.channel.Type != entry.ChannelType || b.MemberAccounts[char] != r.AccountID || r.AccountID != s.accountIDForSession(session) || b.PhaseCompleted || b.TimedOut || now.Before(b.EnterAfter) || !now.Before(b.Deadline) || now.Before(b.Anton.LastTime()) {
		return empty, fmt.Errorf("Luke coin binding expired")
	}
	participant := false
	for _, id := range entry.Participants {
		participant = participant || id == char
	}
	if !participant {
		return empty, fmt.Errorf("Luke coin participant not frozen")
	}
	if _, done := b.ClearedRuns[entry.RunID]; done {
		return empty, fmt.Errorf("Luke coin run completed")
	}
	open := false
	for _, d := range b.Dungeons {
		open = open || int64(d.ID) == r.Dungeon.ID && d.State == 0
	}
	if !open || !(&raid.LukeProgression{AntonProgression: b.Anton}).HasEnteredRun(entry.RunID, uint32(r.Dungeon.ID)) {
		return empty, fmt.Errorf("Luke coin dungeon unavailable")
	}
	life, found := b.MemberLives[currentRaidMemberRun{RunID: entry.RunID, CharacterID: char}]
	if !found || !life.Dead || life.Phase != entry.Phase || life.Attempt != entry.Attempt {
		return empty, fmt.Errorf("Luke coin has no accepted owned death journal")
	}
	phaseLimit := b.Configuration.PhaseCoinLimits[b.Phase]
	if phaseLimit > 0 && b.PhaseCoins[b.Phase] >= phaseLimit || mapLimit > 0 && b.RunCoins[entry.RunID] >= mapLimit {
		return empty, fmt.Errorf("Luke revive coin limit reached")
	}
	if mapLimit > 0 && len(b.RunCoins) >= 4096 {
		if _, exists := b.RunCoins[entry.RunID]; !exists {
			return empty, fmt.Errorf("Luke coin run journal limit")
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.gameSessions[char] != session || session.selectedCharacterID != identity.character || session.characterGeneration != identity.generation || session.events != nil && !session.events.active.Load() {
		return empty, fmt.Errorf("Luke coin canonical session changed")
	}
	coinCtx, cancel := context.WithDeadline(ctx, b.Deadline)
	defer cancel()
	result, e := s.consumeCurrentDungeonReviveCoin(coinCtx, char)
	if e != nil || !result.Consumed && !result.FreeRevive {
		return result, e
	}
	// Phase scope survives re-entry; shared run scope resets by a new run ID.
	// Counts are changed only after the physical asset UOW reports success.
	if phaseLimit > 0 {
		b.PhaseCoins[b.Phase]++
	}
	if mapLimit > 0 {
		if b.RunCoins == nil {
			b.RunCoins = map[string]uint32{}
		}
		b.RunCoins[entry.RunID]++
	}
	return result, nil
}
