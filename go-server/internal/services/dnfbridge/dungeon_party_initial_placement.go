package dnfbridge

import (
	"fmt"

	"longheng.io/server/internal/modules/dnf/dnfenum"
	"longheng.io/server/internal/modules/dnf/worldmap"
)

// Current EXE NOTI120 at 01ff2ec0 reads a slot, obtains its dungeon actor
// (+4f8), and registers that actor in the room at 01ff3097. It does not loop
// over the party. Publish each peer's descriptor before its first placement;
// a missing actor makes the native handler return without registering it.
// This is initial entry only: NOTI120 also restores HP/MP, so actor refreshes
// and arbitrary later peer replays must never call this helper.
func (s *Service) sendInitialDungeonActorPlacements(session *gameSession, runtime *runtimeDungeonState, scene worldmap.DungeonRoomScene, source string) error {
	localPlacement := func() error {
		return s.sendGameUpperRawClass(session, uint16(dnfenum.CmdPacketRequestBlacklist), buildCurrentSceneActorPlacementBody(), 0)
	}
	prepareOrSendLocal := func() error {
		if plan := session.partyLoadingPrepared; plan != nil {
			plan.activation = append(plan.activation, localPlacement)
			return nil
		}
		return localPlacement()
	}
	if s.options.currentClientVersion != "official-24.9" || runtime == nil || runtime.partyInstance == nil || s.options.partyDungeonLoadingBarrier != partyDungeonLoadingBarrierVendor {
		return prepareOrSendLocal()
	}
	identity, bound := s.boundGameSessionCharacterSnapshot(session)
	if !bound {
		return fmt.Errorf("initial party placement lost character binding")
	}
	snapshot, found := s.runtimePartyManagerForService().SnapshotByUser(identity.character, identity.generation)
	if !found || len(snapshot.Members) == 0 {
		return prepareOrSendLocal()
	}
	state := snapshot.StateFor(identity.character)
	membership := s.currentPartyProjectionMembershipGuard(identity, &state)
	var peers []boundGameSessionCharacter
	var slots []byte
	var roomGuards []func() bool
	for _, member := range snapshot.Members {
		peer, online := s.onlineGameSessionCharacterSnapshot(member.UserID)
		if !online || peer.generation != member.SessionGeneration || member.Slot >= 8 {
			return fmt.Errorf("initial party placement member changed")
		}
		peer.session.dungeon.mu.Lock()
		pr := peer.session.dungeon.runtime
		valid := pr != nil && pr.Session != nil && pr.partyInstance == runtime.partyInstance && !pr.townReturnPending
		if valid {
			ps, present := pr.Session.Scene()
			valid = present && ps.Coordinate == scene.Coordinate && ps.Map.Map.ID == scene.Map.Map.ID
		}
		epoch := peer.session.dungeonPlacementEpoch.Load()
		peer.session.dungeon.mu.Unlock()
		roomGuards = append(roomGuards, func() bool {
			return s.boundGameSessionCharacterCurrent(peer) && peer.session.dungeonPlacementEpoch.Load() == epoch
		})
		if !valid {
			return fmt.Errorf("initial party placement member is outside shared room")
		}
		peers = append(peers, peer)
		slots = append(slots, member.Slot)
	}
	roomCurrent := func() bool {
		if !membership() {
			return false
		}
		for _, guard := range roomGuards {
			if !guard() {
				return false
			}
		}
		return true
	}
	for _, peer := range peers {
		if peer.session == session {
			continue
		}
		if !membership() || !s.boundGameSessionCharacterCurrent(peer) {
			return fmt.Errorf("initial party placement membership changed")
		}
		if err := s.sendDungeonPartyPeerActorStateWithInitialRoomGuard(session, peer.session, source+"_before_initial_party_placement", roomCurrent); err != nil {
			return err
		}
	}
	activate := func() error {
		return s.withGameWire(session, func() error {
			if !membership() || !s.boundGameSessionCharacterCurrent(identity) {
				return fmt.Errorf("initial party placement became stale")
			}
			for _, guard := range roomGuards {
				if !guard() {
					return fmt.Errorf("initial party placement room changed")
				}
			}
			for _, slot := range slots {
				if err := s.sendGameUpperRawClassCodecLocked(session, uint16(dnfenum.CmdPacketRequestBlacklist), []byte{slot, 0}, 0, true); err != nil {
					return err
				}
			}
			s.logGameEvent(session, "game-party-initial-slot-placement",
				"char_id", identity.character, "party_id", snapshot.ID,
				"slots", slots, "slot_count", len(slots), "map_id", scene.Map.Map.ID,
				"source", source, "peer_profiles_before_placement", true)
			return nil
		})
	}
	if plan := session.partyLoadingPrepared; plan != nil {
		plan.activation = append(plan.activation, activate)
		return nil
	}
	return activate()
}

func (s *Service) currentLocalDungeonPlacementBody(session *gameSession) []byte {
	body := buildCurrentSceneActorPlacementBody()
	if s.options.currentClientVersion != "official-24.9" || session == nil {
		return body
	}
	session.dungeon.mu.Lock()
	r := session.dungeon.runtime
	active := r != nil && r.partyInstance != nil && !r.townReturnPending
	session.dungeon.mu.Unlock()
	if active {
		for _, member := range runtimePartyMembers(s.runtimePartyStateForSession(session)) {
			if member.UserID == session.selectedCharacterID && member.Slot < 8 {
				body[0] = member.Slot
				break
			}
		}
	}
	return body
}
