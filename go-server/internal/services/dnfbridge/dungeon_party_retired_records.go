package dnfbridge

import (
	"os"
	"strings"

	"longheng.io/server/internal/modules/dnf/alignedcmd"
)

const dungeonPartyRetiredRecordClearEnv = "DNFBRIDGE_PARTY_DUNGEON_RETIRED_RECORD_CLEAR"

// A client can retain one dungeon-context party record for every projected
// member, including itself. Clearing only its town-context record leaves the
// old dungeon roster available to later actor interaction lookups. Retire all
// records belonging to this exact disband snapshot without redrawing actors.
func (s *Service) clearDisbandedDungeonPartyRecords(receiver *gameSession, members []alignedcmd.PartyMemberState, leaderID uint16, peerRemovalAlreadySent bool, source string) error {
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
	seen := make(map[uint16]bool, len(members))
	for _, member := range members {
		key := member.UserID
		if key == 0 || seen[key] || (key == leaderID && peerRemovalAlreadySent) {
			continue
		}
		seen[key] = true
		if !s.boundGameSessionCharacterCurrent(identity) {
			return nil
		}
		body := buildCurrentSceneOp9ActorRemovalBodyInContext(currentSceneActorObjectKey(key), currentSceneObjectContext, s.currentSceneActorRoute())
		if err := s.sendGameUpperRawClass(receiver, 9, body, 0); err != nil {
			return err
		}
		s.logGameEvent(receiver, "game-party-dungeon-retired-record-clear",
			"source", source, "receiver_char_id", identity.character, "record_char_id", key,
			"owner_context", currentSceneObjectContext, "route", s.currentSceneActorRoute(),
			"msg_id", 9, "classification", 0, "switch", dungeonPartyRetiredRecordClearEnv)
	}
	return nil
}
