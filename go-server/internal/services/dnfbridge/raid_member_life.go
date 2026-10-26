package dnfbridge

import (
	"fmt"
	"math"
	"time"

	"longheng.io/server/internal/modules/dnf/worldmap"
)

type currentRaidMemberRun struct {
	RunID       string
	CharacterID uint16
}
type currentRaidMemberLife struct {
	Phase   byte
	Attempt uint64
	Dead    bool
	Deaths  uint32
}

type currentRaidLifeUpdate struct {
	Changed     bool
	Projections []runtimeRaidState
}

// Caller owns only this session's dungeon mutex. The raid mutex protects the
// instance life journal, so duplicate reports or a replacement runtime cannot
// count a second death before a committed revive. No packet or DB write occurs
// under raidMu. Counts remain server-owned until the result flow freezes them.
func (s *Service) recordCurrentRaidCharacterLifeLocked(session *gameSession, runtime *runtimeDungeonState, dead bool, now time.Time) (update currentRaidLifeUpdate, err error) {
	if runtime == nil || runtime.raidEntry == nil {
		return update, nil
	}
	if session == nil || !s.currentRaidDungeonBindingRequired(session) || !s.currentRaidAuthenticated(session) ||
		session.dungeon.runtime != runtime || runtime.Session == nil || !dungeonRuntimeOwnsCharacter(runtime, session.selectedCharacterID) ||
		runtime.Session.Snapshot().Run.Status != worldmap.DungeonRunActive || runtime.townReturnPending || now.IsZero() {
		return update, fmt.Errorf("raid life requires current owned active dungeon")
	}
	if dead && (!runtime.deathReturnWaiting || !runtime.deathStateSent) {
		return update, fmt.Errorf("raid death has no accepted death state")
	}
	// A delayed global clock may not have processed an elapsed phase/reset
	// yet. Advance before checking the frozen attempt; queue these snapshots
	// only after the caller releases its dungeon mutex, including on rejection.
	var clockErrors []error
	update.Projections, clockErrors = s.advanceCurrentRaidClocks(now)
	for _, clockErr := range clockErrors {
		s.logWarn("raid life clock deferred", "error", clockErr)
	}
	s.raidMu.Lock()
	defer s.raidMu.Unlock()
	e := runtime.raidEntry
	state := s.runtimeRaidByMemberLocked(session.selectedCharacterID)
	if state == nil || !state.Started || state.Battle == nil || state.RaidKey != e.RaidKey {
		return update, fmt.Errorf("raid life instance unavailable")
	}
	b := state.Battle
	if b.Mode != 1 && b.Mode != 2 && b.FiendWar == nil {
		return update, nil
	}
	if b.Anton != nil && now.Before(b.Anton.LastTime()) {
		now = b.Anton.LastTime()
	}
	attempt := uint64(1)
	if b.Anton != nil {
		attempt = b.Anton.Epoch()
	}
	if b.InstanceID != e.InstanceID || b.Phase != e.Phase || b.Phase > 1 || attempt != e.Attempt ||
		e.RunID == "" || session.channel.ID != e.ChannelID || session.channel.Type != e.ChannelType ||
		b.MemberAccounts[session.selectedCharacterID] == "" || b.MemberAccounts[session.selectedCharacterID] != runtime.AccountID || runtime.AccountID != s.accountIDForSession(session) ||
		b.PhaseCompleted || b.TimedOut || now.Before(b.EnterAfter) || !now.Before(b.Deadline) {
		return update, fmt.Errorf("raid life binding/attempt/owner expired")
	}
	participant := false
	for _, id := range e.Participants {
		if id == session.selectedCharacterID {
			participant = true
		}
	}
	if !participant {
		return update, fmt.Errorf("raid life owner was not in frozen dungeon group")
	}
	if _, cleared := b.ClearedRuns[e.RunID]; cleared {
		return update, fmt.Errorf("raid life run already completed")
	}
	open := false
	for _, d := range b.Dungeons {
		if int64(d.ID) == runtime.Dungeon.ID && d.State == 0 {
			open = true
		}
	}
	if !open {
		return update, fmt.Errorf("raid life dungeon unavailable")
	}
	key := currentRaidMemberRun{RunID: e.RunID, CharacterID: session.selectedCharacterID}
	life, exists := b.MemberLives[key]
	if exists && (life.Phase != e.Phase || life.Attempt != e.Attempt) {
		return update, fmt.Errorf("raid life run identity conflicts with journal")
	}
	if life.Dead == dead {
		return update, nil
	}
	life.Phase, life.Attempt, life.Dead = e.Phase, e.Attempt, dead
	if dead {
		if b.PhaseDeaths[b.Phase] < math.MaxUint32 {
			b.PhaseDeaths[b.Phase]++
		}
		if life.Deaths < math.MaxUint32 {
			life.Deaths++
		}
	}
	if b.MemberLives == nil {
		b.MemberLives = map[currentRaidMemberRun]currentRaidMemberLife{}
	}
	b.MemberLives[key] = life
	update.Changed = true
	return update, nil
}
