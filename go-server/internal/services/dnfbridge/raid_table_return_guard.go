package dnfbridge

// reconcileRaidTableReturnBeforeTownCommit 在客户端已经收到 op24 并回发城镇落地包时，
// 处理战况表回城期间并发发生的团本/队伍状态变动。
// 一旦 op24 已下发，客户端已经离开副本切回城镇，此时绝不能因并发状态漂移返回致命错误
// 导致服务端主动断开队友 TCP 连接（“网络连接中断”）。
func (s *Service) reconcileRaidTableReturnBeforeTownCommitLocked(
	session *gameSession,
	runtime *runtimeDungeonState,
	source string,
) (allowCommit bool, rejectReason string) {
	if runtime == nil {
		return true, ""
	}
	isRaidTable := runtime.townReturnSource == currentRaidTableReturnSource ||
		runtime.townReturnSource == currentRaidTablePartyReturnSource
	if isRaidTable && !s.currentRaidTableActiveReturnReady(session, runtime) {
		if !runtime.townReturnOp24Sent {
			return false, "raid table return owner changed before town confirmation"
		}
		s.logGameEvent(session, "game-raid-table-return-confirmed-after-owner-change",
			"char_id", session.selectedCharacterID,
			"source", source,
			"return_source", runtime.townReturnSource,
			"reason", "op24_already_sent_downgrade_instead_of_disconnect")
		runtime.raidTablePartyReturn = nil
		return true, ""
	}
	if runtime.raidTablePartyReturn != nil && !s.currentRaidTablePartyReturnMatches(session, runtime, runtime.raidTablePartyReturn) {
		if !runtime.townReturnOp24Sent {
			return false, "raid table party changed before town confirmation"
		}
		s.logGameEvent(session, "game-raid-table-return-confirmed-after-party-change",
			"char_id", session.selectedCharacterID,
			"source", source,
			"leader_char_id", runtime.raidTablePartyReturn.leader.character,
			"reason", "op24_already_sent_downgrade_instead_of_disconnect")
		runtime.raidTablePartyReturn = nil
	}
	return true, ""
}
