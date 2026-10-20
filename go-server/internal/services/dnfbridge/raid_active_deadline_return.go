package dnfbridge

import (
	"context"
	"fmt"
	"time"

	"longheng.io/server/internal/modules/dnf/worldmap"
)

const currentRaidDeadlineReturnSource = "raid_phase_deadline_active_return"

// A return-only owner. It is not a clear, reward, admission or retry receipt.
// The runtime field is owned by its session event loop/dungeon mutex.
type currentRaidDeadlineReturn struct {
	Raid          *runtimeRaidState
	Battle        *runtimeRaidBattle
	Identity      boundGameSessionCharacter
	Runtime       *runtimeDungeonState
	InstanceID    string
	Phase         byte
	Attempt       uint64
	RunID         string
	DungeonID     uint32
	AccountID     string
	ChannelID     int
	ChannelType   byte
	Deadline      time.Time
	PartyInstance *dungeonPartyInstance
	Origin        currentTownPositionSnapshot
	Captured      bool
}

// The clock collects references under raidMu, never locks a peer's dungeon
// mutex there. Individual workers serialize through the owning session loop.
func (s *Service) queueCurrentRaidDeadlineReturns(now time.Time) {
	if s == nil || s.options.currentClientVersion != "official-24.9" || now.IsZero() {
		return
	}
	var owners []*currentRaidDeadlineReturn
	s.raidMu.Lock()
	for _, state := range s.raids {
		if state == nil || !state.Started || state.Battle == nil {
			continue
		}
		b := state.Battle
		if (b.Mode != 1 && b.Mode != 2) || !b.TimedOut || b.PhaseCompleted || b.InstanceID == "" || b.Deadline.IsZero() || now.Before(b.Deadline) {
			continue
		}
		attempt := uint64(1)
		if b.Anton != nil {
			attempt = b.Anton.Epoch()
		}
		for id, entry := range b.Participation {
			account := b.MemberAccounts[id]
			if id == 0 || entry.Runtime == nil || entry.Session == nil || entry.RunID == "" || entry.DungeonID == 0 || entry.Phase != b.Phase || entry.Attempt != attempt || account == "" || runtimeRaidMemberIndex(*state, id) < 0 || s.currentRaidDeadlineQueued[entry.Runtime] {
				continue
			}
			if s.currentRaidDeadlineQueued == nil {
				s.currentRaidDeadlineQueued = map[*runtimeDungeonState]bool{}
			}
			s.currentRaidDeadlineQueued[entry.Runtime] = true
			owners = append(owners, &currentRaidDeadlineReturn{Raid: state, Battle: b, Identity: boundGameSessionCharacter{session: entry.Session, character: id, generation: entry.Generation}, Runtime: entry.Runtime, InstanceID: b.InstanceID, Phase: b.Phase, Attempt: attempt, RunID: entry.RunID, DungeonID: entry.DungeonID, AccountID: account, ChannelID: b.ChannelID, ChannelType: b.ChannelType, Deadline: b.Deadline})
		}
	}
	s.raidMu.Unlock()
	for _, owner := range owners {
		go func(o *currentRaidDeadlineReturn) {
			defer func() {
				s.raidMu.Lock()
				delete(s.currentRaidDeadlineQueued, o.Runtime)
				s.raidMu.Unlock()
			}()
			ctx, cancel := context.WithTimeout(context.Background(), createWriteTimeout)
			defer cancel()
			err := s.callGameSession(ctx, o.Identity.session, currentRaidDeadlineReturnSource, func() error {
				return s.sendCurrentRaidDeadlineReturn(o)
			})
			if err != nil {
				s.logGameEvent(o.Identity.session, "game-raid-deadline-return-deferred", "instance_id", o.InstanceID, "run_id", o.RunID, "error", err)
			}
		}(owner)
	}
}

// Caller holds dungeon.mu. A timed-out phase may return its old active run;
// no ordinary admission/clear guard is relaxed to authorize this operation.
func (s *Service) currentRaidDeadlineReturnValid(o *currentRaidDeadlineReturn) bool {
	if o == nil || o.Raid == nil || o.Battle == nil || o.Runtime == nil || o.Identity.session == nil || !s.boundGameSessionCharacterCurrent(o.Identity) {
		return false
	}
	session, r := o.Identity.session, o.Runtime
	if session.dungeon.runtime != r || r.Session == nil || r.raidEntry == nil || r.Session.Snapshot().Run.Status != worldmap.DungeonRunActive || r.Session.Snapshot().Run.DungeonID != int64(o.DungeonID) || !dungeonRuntimeOwnsCharacter(r, o.Identity.character) || r.AccountID != o.AccountID || session.accountID != o.AccountID || session.channel.ID != o.ChannelID || session.channel.Type != o.ChannelType {
		return false
	}
	e := r.raidEntry
	if e.RaidKey != o.Raid.RaidKey || e.InstanceID != o.InstanceID || e.Phase != o.Phase || e.Attempt != o.Attempt || e.RunID != o.RunID || r.Dungeon.ID != int64(o.DungeonID) || e.ChannelID != o.ChannelID || e.ChannelType != o.ChannelType {
		return false
	}
	participant := false
	for _, id := range e.Participants {
		participant = participant || id == o.Identity.character
	}
	if !participant {
		return false
	}
	s.raidMu.Lock()
	defer s.raidMu.Unlock()
	state := s.raids[o.Raid.RaidKey]
	if state != o.Raid || state.Battle != o.Battle || !state.Started || runtimeRaidMemberIndex(*state, o.Identity.character) < 0 {
		return false
	}
	b := state.Battle
	if !b.TimedOut || b.PhaseCompleted || b.InstanceID != o.InstanceID || b.Phase != o.Phase || !b.Deadline.Equal(o.Deadline) || b.MemberAccounts[o.Identity.character] != o.AccountID || b.ChannelID != o.ChannelID || b.ChannelType != o.ChannelType {
		return false
	}
	if b.Anton != nil && b.Anton.Epoch() != o.Attempt {
		return false
	}
	entry, registered := b.Participation[o.Identity.character]
	if !registered || entry.Session != session || entry.Runtime != r || entry.Generation != o.Identity.generation || entry.RunID != o.RunID || entry.DungeonID != o.DungeonID || entry.Phase != o.Phase || entry.Attempt != o.Attempt {
		return false
	}
	if _, accepted := b.ClearedRuns[o.RunID]; accepted {
		return false
	}
	return true
}

func (s *Service) sendCurrentRaidDeadlineReturn(owner *currentRaidDeadlineReturn) error {
	if owner == nil || owner.Identity.session == nil {
		return nil
	}
	session, runtime := owner.Identity.session, owner.Runtime
	session.dungeon.mu.Lock()
	if !s.currentRaidDeadlineReturnValid(owner) || runtime.townReturnPending && runtime.townReturnOp24Sent {
		session.dungeon.mu.Unlock()
		return nil
	}
	session.dungeon.mu.Unlock()
	if !s.currentRaidAuthenticated(session) {
		return nil
	}
	// No other worker owns the ordering: all these writes run in this same
	// session callback, and no typed return is attempted after a failed write.
	s.raidMu.Lock()
	latest := cloneRuntimeRaidState(*owner.Raid)
	s.raidMu.Unlock()
	if err := s.sendCurrentRaidProgressProjection(session, latest, false, false); err != nil {
		return err
	}
	if err := s.sendCurrentAntonTerminalResult(session, latest); err != nil {
		return err
	}
	if err := s.sendCurrentAntonRewardFlowProjection(session, latest); err != nil {
		return err
	}
	session.dungeon.mu.Lock()
	if !s.currentRaidDeadlineReturnValid(owner) {
		session.dungeon.mu.Unlock()
		return nil
	}
	if runtime.townReturnPending && (runtime.raidDeadlineReturn == nil || runtime.townReturnSource != currentRaidDeadlineReturnSource || runtime.townReturnOp24Sent) {
		session.dungeon.mu.Unlock()
		return nil
	}
	if runtime.raidDeadlineReturn != nil {
		owner = runtime.raidDeadlineReturn
		if !s.currentRaidDeadlineReturnValid(owner) {
			session.dungeon.mu.Unlock()
			return nil
		}
	} else {
		owner.PartyInstance = runtime.partyInstance
		owner.Origin = runtime.townReturnOrigin
		owner.Captured = true
		runtime.raidDeadlineReturn = owner
	}
	session.dungeon.mu.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), createWriteTimeout)
	defer cancel()
	transition, err := s.prepareCurrentDungeonTownTransitionForSession(ctx, session, owner.Identity.character)
	if err != nil {
		return err
	}
	session.dungeon.mu.Lock()
	defer session.dungeon.mu.Unlock()
	if runtime.raidDeadlineReturn != owner || !s.currentRaidDeadlineReturnValid(owner) || runtime.partyInstance != owner.PartyInstance || runtime.townReturnOrigin != owner.Origin {
		return nil
	}
	return s.sendCurrentDungeonReturnToTownLocked(session, runtime, transition, 0, currentRaidDeadlineReturnSource)
}

func (s *Service) validateCurrentRaidDeadlineSceneCommit(session *gameSession, runtime *runtimeDungeonState) error {
	o := runtime.raidDeadlineReturn
	if o == nil {
		return nil
	}
	// Accepted completion remains on the existing result/return contract.
	if !o.Captured || o.Identity.session != session || o.Runtime != runtime || !s.currentRaidDeadlineReturnValid(o) || !runtime.townReturnOp24Sent || runtime.townReturnSource != currentRaidDeadlineReturnSource || runtime.townReturnOrigin != o.Origin || runtime.partyInstance != o.PartyInstance {
		return fmt.Errorf("raid deadline return owner changed before town confirmation")
	}
	return nil
}
