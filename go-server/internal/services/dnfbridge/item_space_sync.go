package dnfbridge

import (
	"context"
	"strconv"
	"strings"

	"longheng.io/server/internal/modules/dnf/dnfenum"
)

// currentItemSpaceSyncOpcode is the current client's one-byte item-space sync
// request. The native success consumer only clears the two pending maps keyed
// by 19; it does not inspect bytes after the leading success byte.
const currentItemSpaceSyncOpcode = uint16(dnfenum.CmdPacketSyncItemSpace)

// handleCurrentItemSpaceSync acknowledges the current client's post-command
// item-space barrier. It is intentionally read-only: no inventory snapshot is
// replayed and no player record is written. The ACK must remain inline with the
// session reader so every earlier command has returned and committed before
// this response is serialized.
func (s *Service) handleCurrentItemSpaceSync(session *gameSession, body []byte) error {
	if s == nil || session == nil {
		return nil
	}
	reject := func(reason string) error {
		s.logGameEvent(session, "game-item-space-sync-rejected", "reason", reason)
		return nil
	}
	if s.options.currentClientVersion != "official-24.9" {
		return reject("historical_client")
	}
	// The live v126 request is exactly one FF byte. Do not broaden this into a
	// tolerant reader: no other request shape has native success evidence.
	if len(body) != 1 || body[0] != 0xFF {
		return reject("native_request_shape")
	}
	if !session.loginPacketSeen || !session.accountFromToken {
		return reject("source_authentication")
	}
	accountID := strings.TrimSpace(session.accountID)
	if accountID == "" {
		return reject("source_authentication")
	}
	identity, bound := s.boundGameSessionCharacterSnapshot(session)
	if !bound {
		return reject("selected_character_unbound")
	}
	repositories, ok := s.repositoryGroup()
	if !ok || repositories.Account == nil || repositories.Character == nil {
		return reject("repository_unavailable")
	}

	ctx, cancel := context.WithTimeout(context.Background(), createWriteTimeout)
	defer cancel()
	account, found, err := repositories.Account.Load(ctx, accountID)
	if err != nil {
		return reject("account_load_failed")
	}
	if !found || strings.TrimSpace(account.AccountID) != accountID {
		return reject("account_owner_unverified")
	}
	characterID := strconv.Itoa(int(identity.character))
	character, found, err := repositories.Character.Load(ctx, characterID)
	if err != nil {
		return reject("character_load_failed")
	}
	if !found || character.CharacterID != characterID || strings.TrimSpace(character.AccountID) != accountID {
		return reject("character_owner_unverified")
	}
	// The repository reads above may yield to a channel reconnect or character
	// reselection. Never acknowledge a barrier for the superseded session owner.
	if !s.boundGameSessionCharacterCurrent(identity) {
		return reject("binding_changed")
	}

	if err := s.sendGameUpperSuccess(session, currentItemSpaceSyncOpcode, nil); err != nil {
		return err
	}
	s.logGameEvent(session, "game-item-space-sync-acked",
		"account_id", accountID,
		"char_id", identity.character,
		"body_hex", "ff",
		"ack_body_hex", "01")
	return nil
}
