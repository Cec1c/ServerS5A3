package dnfbridge

import "encoding/binary"

// The fixed native90 client sends channel:u8 + character:u16 for op889.
// Its class1 01CFDD50 success reader consumes u32/u16/u8/u8 after success.
// Keep every live account, channel, presence and current-membership check.
func (s *Service) handleNative90RaidPeerCheck(session *gameSession, op uint16, body []byte) error {
	if len(body) != 3 || session == nil || !s.currentRaidAuthenticated(session) || session.channel.ID <= 0 || session.channel.ID > 255 || body[0] != byte(session.channel.ID) {
		return s.sendRaidCommandFailure(session, op)
	}
	source, bound := s.boundGameSessionCharacterSnapshot(session)
	target, online := s.onlineGameSessionCharacterSnapshot(binary.LittleEndian.Uint16(body[1:3]))
	if !bound || !online || !s.currentRaidAuthenticated(target.session) || !s.currentRaidInvitationPeers(source, target) {
		return s.sendRaidCommandFailure(session, op)
	}
	s.raidMu.Lock()
	valid := s.runtimeRaidByMemberLocked(target.character) == nil && s.currentRaidInvitationPeers(source, target)
	s.raidMu.Unlock()
	if !valid {
		return s.sendRaidCommandFailure(session, op)
	}
	bodyOut := make([]byte, 9)
	bodyOut[0] = 1
	s.logGameEvent(session, "game-native90-raid-peer-check-accepted", "target_char_id", target.character, "channel_id", session.channel.ID)
	return s.sendGameUpperRawClassCodec(session, op, bodyOut, 1, true)
}
