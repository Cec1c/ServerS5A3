package dnfbridge

import (
	"context"
	"errors"
	"io"
	"net"
	"sort"

	"longheng.io/server/internal/modules/dnf/party"
)

// Profile priming supplements the established roster/transport sequence. A
// failed cosmetic projection must not undo committed membership or disconnect
// the actor whose wear operation already succeeded. The next roster or wear
// refresh gets a new batch and retries; failures remain visible in normal logs.
func (s *Service) sendCurrentCrossSceneProfileRefresh(batch *currentCrossSceneProfileBatch, viewer, actor *gameSession, owner byte, reason string) {
	if err := batch.send(viewer, actor, owner); err != nil {
		s.logGameEvent(viewer, "game-peer-userinfo-profile-refresh-failed",
			"source_char_id", selectedCharacterID(viewer), "target_char_id", selectedCharacterID(actor),
			"owner_context", owner, "reason", reason, "error_kind", currentCrossSceneProfileErrorKind(err))
	}
}

// Keep normal logs actionable without copying database or transport errors,
// which can contain addresses, account identifiers or payload fragments.
func currentCrossSceneProfileErrorKind(err error) string {
	switch {
	case errors.Is(err, context.Canceled):
		return "cancelled"
	case errors.Is(err, context.DeadlineExceeded):
		return "deadline"
	case errors.Is(err, io.ErrClosedPipe), errors.Is(err, net.ErrClosed):
		return "connection_closed"
	default:
		return "prepare_or_write"
	}
}

func (s *Service) sendCurrentPartyCrossSceneProfiles(snapshot party.RuntimePartySnapshot) {
	if s.options.currentClientVersion != "official-24.9" || len(snapshot.Members) < 2 {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), createWriteTimeout)
	defer cancel()
	batch := newCurrentCrossSceneProfileBatch(s, ctx)
	for _, member := range snapshot.Members {
		viewer, ok := s.onlineGameSessionCharacterSnapshot(member.UserID)
		if !ok || viewer.generation != member.SessionGeneration {
			continue
		}
		for _, peer := range snapshot.Members {
			actor, ok := s.onlineGameSessionCharacterSnapshot(peer.UserID)
			if !ok || actor.generation != peer.SessionGeneration || actor.character == viewer.character {
				continue
			}
			s.sendCurrentCrossSceneProfileRefresh(batch, viewer.session, actor.session,
				currentConnectionTownActorOwnerContext(viewer.session), "party_roster")
		}
	}
}

func (s *Service) currentCrossSceneRaidPair(state runtimeRaidState, viewer, actor *gameSession) bool {
	viewerID, actorID := selectedCharacterID(viewer), selectedCharacterID(actor)
	if state.RaidKey == 0 || viewerID == 0 || actorID == 0 || viewerID == actorID ||
		byte(state.RaidKey>>16) != currentConnectionTownActorOwnerContext(viewer) ||
		byte(state.RaidKey>>24) != s.currentSceneActorRoute() {
		return false
	}
	s.raidMu.Lock()
	defer s.raidMu.Unlock()
	live := s.runtimeRaidByMemberLocked(viewerID)
	return live != nil && live.RaidKey == state.RaidKey &&
		runtimeRaidMemberIndex(*live, actorID) >= 0 &&
		s.raidPresenceContext(viewer, *live, viewer.accountID) && s.raidPresenceContext(actor, *live, actor.accountID)
}

func (s *Service) sendCurrentRaidCrossSceneProfiles(batch *currentCrossSceneProfileBatch, viewer *gameSession, state runtimeRaidState) {
	for _, member := range state.Members {
		actor, ok := s.onlineGameSession(member.CharID)
		if !ok || !s.currentCrossSceneRaidPair(state, viewer, actor) {
			continue
		}
		s.sendCurrentCrossSceneProfileRefresh(batch, viewer, actor, byte(state.RaidKey>>16), "raid_roster")
	}
}

func (s *Service) broadcastCurrentCrossSceneAppearance(actor *gameSession) {
	if s == nil || s.options.currentClientVersion != "official-24.9" {
		return
	}
	identity, ok := s.boundGameSessionCharacterSnapshot(actor)
	if !ok {
		return
	}
	ids := make(map[uint16]bool)
	if snapshot, found := s.runtimePartyManagerForService().SnapshotByUser(identity.character, 0); found {
		for _, member := range snapshot.Members {
			ids[member.UserID] = true
		}
	}
	if raid, found := s.runtimeRaidForMember(identity.character); found {
		for _, member := range raid.Members {
			ids[member.CharID] = true
		}
	}
	delete(ids, identity.character)
	ordered := make([]int, 0, len(ids))
	for id := range ids {
		ordered = append(ordered, int(id))
	}
	sort.Ints(ordered)
	ctx, cancel := context.WithTimeout(context.Background(), createWriteTimeout)
	defer cancel()
	batch := newCurrentCrossSceneProfileBatch(s, ctx)
	for _, id := range ordered {
		if !s.boundGameSessionCharacterCurrent(identity) {
			return
		}
		viewer, ok := s.onlineGameSession(uint16(id))
		if ok {
			s.sendCurrentCrossSceneProfileRefresh(batch, viewer, actor,
				currentConnectionTownActorOwnerContext(viewer), "equipment_appearance")
		}
	}
}
