package dnfbridge

import (
	"os"
	"strings"

	"longheng.io/server/internal/modules/dnf/alignedcmd"
)

const dungeonPartyRetiredRecordClearEnv = "DNFBRIDGE_PARTY_DUNGEON_RETIRED_RECORD_CLEAR"

// A surviving dungeon actor can retain a pre-entry town-context association.
// Retire this same membership snapshot there before removing dungeon records.
// Requires the paired client helper to preserve the living local actor while
// clearing its current-channel record in an ordinary dungeon.
func (s *Service) clearDungeonSurvivorTownPartyRecords(receiver *gameSession, members []alignedcmd.PartyMemberState, source string, retained ...alignedcmd.PartyState) error {
	if s == nil || s.options.currentClientVersion != "official-24.9" {
		return nil
	}
	switch strings.ToLower(strings.TrimSpace(os.Getenv(dungeonPartyRetiredRecordClearEnv))) {
	case "off", "0", "false", "legacy":
		return nil
	}
	identity, ok := s.boundGameSessionCharacterSnapshot(receiver)
	if !ok {
		return nil
	}
	var expected *alignedcmd.PartyState
	if len(retained) > 0 {
		expected = &retained[0]
	}
	current := s.currentPartyRetirementGuard(receiver, expected)
	// Entry resets townActorOwnerChannel to the dungeon namespace. The client
	// keeps its selected channel; match the paired native channel guard.
	owner := currentClientSelectedChannel(receiver)
	if owner == currentSceneObjectContext {
		return nil // The following dungeon-context pass already covers this key.
	}
	ordered := make([]uint16, 0, len(members))
	for _, member := range members {
		if member.UserID != identity.character {
			ordered = append(ordered, member.UserID)
		}
	}
	for _, member := range members {
		if member.UserID == identity.character {
			ordered = append(ordered, member.UserID)
		}
	}
	seen := make(map[uint16]bool, len(ordered))
	for _, key := range ordered {
		if key == 0 || seen[key] {
			continue
		}
		seen[key] = true
		if !current() {
			return nil
		}
		body := buildCurrentSceneOp9ActorRemovalBodyInContext(currentSceneActorObjectKey(key), owner, s.currentSceneActorRoute())
		sent, err := s.sendRetiredPartyPacket(receiver, 9, body, expected, current, false)
		if err != nil {
			return err
		}
		if !sent {
			return nil
		}
		s.logGameEvent(receiver, "game-party-dungeon-retired-town-record-clear",
			"source", source, "receiver_char_id", identity.character, "record_char_id", key, "owner_context", owner)
	}
	return nil
}

// A client can retain one dungeon-context party record for every projected
// member, including itself. Clearing only its town-context record leaves the
// old dungeon roster available to later actor interaction lookups. Retire all
// records belonging to this exact disband snapshot without redrawing actors.
func (s *Service) clearDisbandedDungeonPartyRecords(receiver *gameSession, members []alignedcmd.PartyMemberState, leaderID uint16, peerRemovalAlreadySent bool, source string, retained ...alignedcmd.PartyState) error {
	if s == nil || s.options.currentClientVersion != "official-24.9" {
		return nil
	}
	switch strings.ToLower(strings.TrimSpace(os.Getenv(dungeonPartyRetiredRecordClearEnv))) {
	case "off", "0", "false", "legacy":
		return nil
	}
	identity, ok := s.boundGameSessionCharacterSnapshot(receiver)
	if !ok {
		return nil
	}
	var expected *alignedcmd.PartyState
	if len(retained) > 0 {
		expected = &retained[0]
	}
	current := s.currentPartyRetirementGuard(receiver, expected)
	seen := make(map[uint16]bool, len(members))
	// A receiver can be bound to any projected member's dungeon record. The
	// native self-slot setter clears that backpointer even when it belongs to
	// another record. Retiring self first can therefore hide the local departure
	// from every subsequent NOTI9 handler and leave network peers alive. Clear
	// the other records first, so the bound record observes its local transition
	// before the receiver's own record is retired. Do not reorder the snapshot.
	ordered := make([]alignedcmd.PartyMemberState, 0, len(members))
	for _, member := range members {
		if member.UserID != identity.character {
			ordered = append(ordered, member)
		}
	}
	for _, member := range members {
		if member.UserID == identity.character {
			ordered = append(ordered, member)
		}
	}
	for _, member := range ordered {
		key := member.UserID
		if key == 0 || seen[key] || (key == leaderID && peerRemovalAlreadySent) {
			continue
		}
		seen[key] = true
		if !current() {
			return nil
		}
		body := buildCurrentSceneOp9ActorRemovalBodyInContext(currentSceneActorObjectKey(key), currentSceneObjectContext, s.currentSceneActorRoute())
		sent, err := s.sendRetiredPartyPacket(receiver, 9, body, expected, current, false)
		if err != nil {
			return err
		}
		if !sent {
			return nil
		}
		s.logGameEvent(receiver, "game-party-dungeon-retired-record-clear",
			"source", source, "receiver_char_id", identity.character, "record_char_id", key,
			"owner_context", currentSceneObjectContext, "route", s.currentSceneActorRoute(),
			"msg_id", 9, "classification", 0, "switch", dungeonPartyRetiredRecordClearEnv)
	}
	return nil
}
