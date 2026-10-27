package dnfbridge

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"longheng.io/server/internal/modules/dnf/dungeoncmd"
	"longheng.io/server/internal/modules/dnf/worldmap"
)

// The current client's actual warp-object producer selects one of four
// hatcheries at difficulty 2. This receipt preserves the active energy owner
// while the complete replacement scene is prepared; it is not a town exit.
type currentAntonHatcheryTransfer struct {
	Old          *runtimeDungeonState
	Character    uint16
	Generation   uint64
	Entry        currentRaidDungeonEntry
	Coordinate   worldmap.RoomCoordinate
	MapID        int64
	Target       uint32
	Room         *runtimeDungeonRoom
	Lifecycle    uint64
	ReturnOrigin currentTownPositionSnapshot
}

// Caller holds dungeon.mu. Nested locks follow the established binding order.
func (s *Service) captureCurrentAntonHatcheryTransferLocked(session *gameSession, request dungeoncmd.SelectDungeonRequest) *currentAntonHatcheryTransfer {
	if s == nil || session == nil || s.options.currentClientVersion != "official-24.9" || request.DungeonID < 221 || request.DungeonID > 224 || request.Difficulty != 2 || !s.currentRaidAuthenticated(session) {
		return nil
	}
	s.raidMu.Lock()
	defer s.raidMu.Unlock()
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.captureCurrentAntonHatcheryTransferWithOwnersLocked(session, request)
}

// Caller holds every affected dungeon.mu, raidMu, and Service.mu. No peer queue,
// packet write, repository transaction, or callback runs in this section.
func (s *Service) captureCurrentAntonHatcheryTransferWithOwnersLocked(session *gameSession, request dungeoncmd.SelectDungeonRequest) *currentAntonHatcheryTransfer {
	if s == nil || session == nil || s.options.currentClientVersion != "official-24.9" || request.Difficulty != 2 {
		return nil
	}
	old := session.dungeon.runtime
	if old == nil || old.Session == nil || old.Room == nil || old.raidEntry == nil || old.lifecycleToken == 0 || old.lifecycleToken != session.dungeon.runToken || old.Dungeon.ID != 218 || old.Request.DungeonID != 218 || old.Request.Difficulty != 2 || !session.postStartMapPlayerStateSent || old.deathStateSent || old.deathReturnWaiting || old.townReturnPending || !dungeonRuntimeOwnsCharacter(old, session.selectedCharacterID) {
		return nil
	}
	scene, ok := old.Session.Scene()
	if !ok || old.Session.Snapshot().Run.Status != worldmap.DungeonRunActive {
		return nil
	}
	target, mapID := uint32(0), int64(0)
	switch scene.Coordinate {
	case (worldmap.RoomCoordinate{X: 3, Y: 0}):
		target, mapID = 221, 19103
	case (worldmap.RoomCoordinate{X: 3, Y: 4}):
		target, mapID = 222, 19119
	case (worldmap.RoomCoordinate{X: 0, Y: 4}):
		target, mapID = 223, 19116
	case (worldmap.RoomCoordinate{X: 0, Y: 0}):
		target, mapID = 224, 19100
	}
	room := old.Room.Snapshot()
	if target == 0 || request.DungeonID != target || scene.Map.Map.ID != mapID || room.MapID != mapID || room.Coordinate != scene.Coordinate {
		return nil
	}
	e := old.raidEntry
	state := s.runtimeRaidByMemberLocked(session.selectedCharacterID)
	if s.gameSessions[session.selectedCharacterID] != session || !session.loginPacketSeen || !session.accountFromToken || state == nil || !state.Started || state.RaidKey != e.RaidKey || state.Battle == nil {
		return nil
	}
	b := state.Battle
	now := time.Now()
	if b.Anton != nil && (b.Anton.Phase() != int(b.Phase) || b.Anton.PhaseCleared() || b.Anton.PhaseFailed() || now.Before(b.Anton.LastTime())) {
		return nil
	}
	if b.Mode != 1 || b.Single || b.Phase != 1 || b.Anton == nil || b.InstanceID != e.InstanceID || e.Phase != b.Phase || e.Attempt != b.Anton.Epoch() || e.RunID == "" || b.PhaseCompleted || b.TimedOut || now.Before(b.EnterAfter) || !now.Before(b.Deadline) || b.ChannelID != e.ChannelID || b.ChannelType != e.ChannelType || session.channel.ID != e.ChannelID || session.channel.Type != e.ChannelType || b.MemberAccounts[session.selectedCharacterID] != old.AccountID || strings.TrimSpace(session.accountID) != old.AccountID {
		return nil
	}
	member := runtimeRaidMemberIndex(*state, session.selectedCharacterID)
	if member < 0 {
		return nil
	}
	seen := map[uint16]bool{}
	for _, id := range e.Participants {
		index := runtimeRaidMemberIndex(*state, id)
		if id == 0 || seen[id] || index < 0 || state.Members[index].GroupIndex != state.Members[member].GroupIndex || b.MemberAccounts[id] == "" {
			return nil
		}
		seen[id] = true
	}
	if !seen[session.selectedCharacterID] {
		return nil
	}
	open := false
	for _, d := range b.Dungeons {
		if d.ID == target && d.State == 0 {
			open = true
		}
	}
	if !open {
		return nil
	}
	open = false
	for _, d := range b.Anton.Dungeons() {
		if d.ID == target && d.State == 0 {
			open = true
		}
	}
	if !open {
		return nil
	}
	entry := *e
	entry.Participants = append([]uint16(nil), e.Participants...)
	return &currentAntonHatcheryTransfer{old, session.selectedCharacterID, session.characterGeneration, entry, scene.Coordinate, mapID, target, old.Room, old.lifecycleToken, old.townReturnOrigin}
}

func (s *Service) validateCurrentAntonHatcheryTransferWithOwnersLocked(session *gameSession, runtime *runtimeDungeonState) error {
	receipt := runtime.antonHatcheryTransfer
	if receipt == nil {
		return fmt.Errorf("Anton hatchery transfer receipt missing")
	}
	current := s.captureCurrentAntonHatcheryTransferWithOwnersLocked(session, runtime.Request)
	if current == nil || current.Old != receipt.Old || current.Room != receipt.Room || current.Lifecycle != receipt.Lifecycle || current.Character != receipt.Character || current.Generation != receipt.Generation || current.Entry.RunID != receipt.Entry.RunID || current.Entry.InstanceID != receipt.Entry.InstanceID || current.Entry.Phase != receipt.Entry.Phase || current.Entry.Attempt != receipt.Entry.Attempt || current.Coordinate != receipt.Coordinate || current.MapID != receipt.MapID || !sameRaidParticipants(current.Entry.Participants, receipt.Entry.Participants) {
		return fmt.Errorf("Anton hatchery transfer old owner changed")
	}
	if runtime.Session == nil || runtime.Dungeon.ID != int64(receipt.Target) || runtime.Request.DungeonID != receipt.Target || runtime.Session.Snapshot().Run.DungeonID != runtime.Dungeon.ID || !dungeonRuntimeOwnsCharacter(runtime, receipt.Character) || runtime.Character.AccountID != runtime.AccountID || runtime.AccountID != receipt.Old.AccountID || !runtime.Dungeon.Metadata.NoFatigue || runtime.epicEntry != nil || len(runtime.Dungeon.Metadata.RequiredItems) != 0 {
		return fmt.Errorf("Anton hatchery transfer target cost contract changed")
	}
	return nil
}

func (s *Service) commitCurrentAntonHatcheryTransferGroup(leader *gameSession, runtime *runtimeDungeonState, followers []*runtimePartyPreparedDungeonEntry) error {
	group := []*runtimePartyPreparedDungeonEntry{{session: leader, runtime: runtime}}
	group = append(group, followers...)
	if runtime == nil || runtime.antonHatcheryTransfer == nil || runtime.raidEntry == nil || len(group) != len(runtime.antonHatcheryTransfer.Entry.Participants) {
		return fmt.Errorf("Anton hatchery transfer frozen group incomplete")
	}
	seen := map[uint16]bool{}
	for _, member := range group {
		if member == nil || member.session == nil || member.runtime == nil || member.runtime.antonHatcheryTransfer == nil {
			return fmt.Errorf("Anton hatchery transfer member unavailable")
		}
		if !s.currentRaidAuthenticated(member.session) {
			return fmt.Errorf("Anton hatchery transfer member authentication changed")
		}
		id := member.runtime.antonHatcheryTransfer.Character
		if seen[id] {
			return fmt.Errorf("Anton hatchery transfer duplicate owner")
		}
		seen[id] = true
	}
	sort.Slice(group, func(i, j int) bool {
		return group[i].runtime.antonHatcheryTransfer.Character < group[j].runtime.antonHatcheryTransfer.Character
	})
	for _, member := range group {
		member.session.dungeon.mu.Lock()
	}
	defer func() {
		for i := len(group) - 1; i >= 0; i-- {
			group[i].session.dungeon.mu.Unlock()
		}
	}()
	err := func() error {
		s.raidMu.Lock()
		defer s.raidMu.Unlock()
		s.mu.Lock()
		defer s.mu.Unlock()
		base := runtime.antonHatcheryTransfer.Entry
		newEntry := runtime.raidEntry
		for _, member := range group {
			r := member.runtime
			if err := s.validateCurrentAntonHatcheryTransferWithOwnersLocked(member.session, r); err != nil {
				return err
			}
			oldEntry := r.antonHatcheryTransfer.Entry
			e := r.raidEntry
			if oldEntry.RunID != base.RunID || !sameRaidParticipants(oldEntry.Participants, base.Participants) || e == nil || e.RunID == "" || e.RunID == base.RunID || e.RunID != newEntry.RunID || e.RaidKey != base.RaidKey || e.InstanceID != base.InstanceID || e.Phase != base.Phase || e.Attempt != base.Attempt || e.ChannelID != base.ChannelID || e.ChannelType != base.ChannelType || !sameRaidParticipants(e.Participants, base.Participants) || r.Dungeon.ID != runtime.Dungeon.ID {
				return fmt.Errorf("Anton hatchery transfer prepared group changed")
			}
			if _, ok := r.Session.Scene(); !ok {
				return errDungeonWorldMapUnavailable
			}
		}
		// All checks complete before replacing even one member. The actual four
		// hatcheries have no fatigue or entry items, so there is no paid partial
		// commit to refund and no cross-account repository transaction here.
		for _, member := range group {
			r := member.runtime
			session := member.session
			session.dungeon.runToken = nextCurrentDungeonDeathGeneration(session.dungeon.runToken)
			r.lifecycleToken = session.dungeon.runToken
			session.dungeon.runtime = r
			member.committed = true
		}
		return nil
	}()
	if err != nil {
		return err
	}
	for _, member := range group {
		old := member.runtime.antonHatcheryTransfer.Old
		s.cancelCurrentDungeonDeathReturnLocked(member.session, old, "anton_hatchery_transfer_committed")
		s.cancelCurrentCrystalContractTimerLocked(member.session, old, "anton_hatchery_transfer_committed")
		s.cancelCurrentDungeonCardAutoFlipLocked(member.session, old, "anton_hatchery_transfer_committed")
		s.releasePartyDungeonInstance(old, member.session.selectedCharacterID, "anton_hatchery_transfer_committed")
		scene, _ := member.runtime.Session.Scene()
		s.adoptPartyDungeonInstance(member.session, member.runtime, scene)
	}
	return nil
}
