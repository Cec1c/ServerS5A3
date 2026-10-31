package dnfbridge

// A committed raid transition may be broadcast by another member's command.
// Returning a recipient's socket error would close that command's healthy
// connection too. Retire only the failed stream (which may have a partial
// frame), keep the committed state, and continue delivering to other members.
func (s *Service) failRuntimeRaidRecipient(peer *gameSession, stage string, err error) {
	if peer == nil || err == nil {
		return
	}
	if peer.conn != nil {
		_ = peer.conn.Close()
	}
	s.logWarn("dnfbridge raid recipient projection failed",
		"conn_id", peer.connID, "channel_id", peer.channel.ID,
		"char_id", selectedCharacterID(peer), "stage", stage, "error", err)
}
