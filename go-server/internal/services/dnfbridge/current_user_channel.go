package dnfbridge

import (
	"context"
	"strconv"
	"strings"
)

const currentUserChannelOpcode = uint16(804)

// v96 SHA82e0: 03549532 emits u8 character server group + DSTR name.
// Class1 03539F50 reads u8 group, DSTR name, u8 directory server, u8 channel.
// These are distinct identities: directory vtable 048995EC/+50 reads +4,
// and /+58 reads +70. The directory advertises channelAdvertiseID, not the
// channel_info.etc source server ID. No UID or synthetic position is supplied.
func (s *Service) handleCurrentUserChannel(session *gameSession, op uint16, classification byte, body []byte) (bool, error) {
	if s.options.currentClientVersion != "official-24.9" || op != currentUserChannelOpcode {
		return false, nil
	}
	if classification != 1 {
		return true, nil
	}
	// 035516ED displays the generic lookup failure for error 0, and the
	// offline-specific lookup failure for 100. Failure callbacks read no tail.
	fail := func(code byte, reason string) (bool, error) {
		s.logGameEvent(session, "game-user-channel-rejected", "reason", reason)
		return true, s.sendGameUpperFailure(session, op, code)
	}
	request, err := decodeCurrentCharacterFriendMutation(291, body)
	if err != nil {
		return fail(0, "native_request_shape")
	}
	identity, bound := s.boundGameSessionCharacterSnapshot(session)
	if !bound || !session.loginPacketSeen || !session.accountFromToken || strings.TrimSpace(session.accountID) == "" {
		return fail(0, "source_authentication")
	}
	repositories, available := s.repositoryGroup()
	if !available || repositories.Character == nil {
		return fail(0, "repository_unavailable")
	}
	ctx, cancel := context.WithTimeout(context.Background(), createWriteTimeout)
	defer cancel()
	sourceID := strconv.Itoa(int(identity.character))
	source, found, err := repositories.Character.Load(ctx, sourceID)
	if err != nil || !found || source.CharacterID != sourceID || source.AccountID != s.accountIDForSession(session) || !s.boundGameSessionCharacterCurrent(identity) {
		return fail(0, "source_owner_unverified")
	}
	// This repository owns only the local character server group. Never alias a
	// cross-server request to a same-name character in the local repository.
	// Read the repository value directly: shared roster helpers deliberately
	// normalize negative values to zero, which would alias this query's owner.
	sourceGroup := source.Stats["server_group_id"]
	if sourceGroup < 0 || sourceGroup >= 250 || int64(request.ServerGroup) != sourceGroup {
		return fail(0, "foreign_character_server")
	}
	id, exists, err := repositories.Character.FindIDByName(ctx, request.Name)
	if err != nil || !exists {
		return fail(0, "target_missing")
	}
	target, found, err := repositories.Character.Load(ctx, id)
	key, parseErr := strconv.ParseUint(id, 10, 16)
	targetGroup := target.Stats["server_group_id"]
	if err != nil || !found || parseErr != nil || key == 0 || target.CharacterID != id || target.Name != request.Name || targetGroup < 0 || targetGroup >= 250 || targetGroup != int64(request.ServerGroup) {
		return fail(0, "target_identity_unverified")
	}
	peer, online := s.onlineGameSessionCharacterSnapshot(uint16(key))
	if !online || !peer.session.loginPacketSeen || !peer.session.accountFromToken || s.accountIDForSession(peer.session) != target.AccountID || !s.boundGameSessionCharacterCurrent(peer) {
		return fail(100, "target_offline")
	}
	channel := peer.session.channel
	directoryServer := s.channelAdvertiseID()
	if directoryServer < 0 || directoryServer >= 250 || channel.ID <= 0 || channel.ID > 255 {
		return fail(0, "native_position_unrepresentable")
	}
	catalog := s.currentCatalog()
	if catalog == nil {
		return fail(0, "directory_unavailable")
	}
	entry, exists := catalog.ForPort(channel.Port)
	if !exists || entry.ID != channel.ID || entry.ServerID != channel.ServerID {
		return fail(0, "target_channel_not_in_directory")
	}
	if !s.boundGameSessionCharacterCurrent(identity) || !s.boundGameSessionCharacterCurrent(peer) {
		return fail(0, "binding_changed")
	}
	var w packetWriter
	w.writeByte(request.ServerGroup)
	w.writeRawDstr(rosterNameBytes(target.Name))
	w.writeByte(byte(directoryServer))
	w.writeByte(byte(channel.ID))
	s.logGameEvent(session, "game-user-channel-resolved", "target_char_id", id, "character_server", request.ServerGroup, "directory_server", directoryServer, "channel", channel.ID)
	return true, s.sendGameUpperSuccess(session, op, w.bytes())
}
