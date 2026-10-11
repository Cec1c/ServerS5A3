package dnfbridge

// Current class0/1228 (0373F7D0) consumes one byte and records GetTickCount
// through 01A54A70. The byte is discarded; it is not a reward or counter.
// 01A54A90 rejects the raid directory while that timestamp is -1, then for
// 3000 ms after this notice. Without it, "please try again later" never ends.
// The selected town bootstrap is the lifecycle owner, including reconnect;
// ordinary movement and directory requests must not restart the cooldown.
const currentRaidChannelReadyMsgID uint16 = 1228

func (s *Service) sendCurrentRaidChannelReady(session *gameSession, source string) error {
	if s == nil || session == nil || session.selectedCharacterID == 0 || s.options.currentClientVersion != "official-24.9" {
		return nil
	}
	if _, supported := currentRaidRouteForChannel(int(session.channel.Type)); !supported {
		return nil
	}
	if err := s.sendGameUpperRawClass(session, currentRaidChannelReadyMsgID, []byte{0}, 0); err != nil {
		return err
	}
	s.logGameEvent(session, "game-current-raid-channel-ready", "source", source,
		"char_id", session.selectedCharacterID, "msg_id", currentRaidChannelReadyMsgID,
		"classification", 0, "body_len", 1, "cooldown_ms", 3000)
	return nil
}
