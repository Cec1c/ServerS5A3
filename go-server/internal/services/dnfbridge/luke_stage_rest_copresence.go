package dnfbridge

// Luke's stage-rest return can arrive after the dungeon actor generation has
// already been published. The client then sends its ordinary op37 request for
// the town scene, but returnTownFinishLoadingAckOnly deliberately keeps that
// request to an empty ACK so the dungeon progression snapshot is not replayed.
//
// The SET_USER_AREA broadcasts happened before this ACK, while the receiving
// client was still loading the stage-rest town scene. The normal finish-loading
// tail is skipped by the ACK-only branch, so its town co-presence resend also
// never runs. Peers remain invisible until another SET_USER_AREA (walking out
// and back) installs their actors into the now-ready scene.
//
// Keep the ACK-only wire contract and reuse the existing idempotent town resend
// after that ACK. This only targets the peer scene; it neither replays op37 nor
// changes dungeon/raid state.
func (s *Service) resendTownCopresenceAfterReturnFinishLoadingAck(session *gameSession, source string) {
	if s == nil || s.onlinePlayers == nil || session == nil || !session.returnTownFinishLoadingAckOnly {
		return
	}
	s.resendTownCopresencePeerActorsAfterFinishLoading(session, source)
}
