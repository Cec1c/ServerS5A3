package dnfbridge

import (
	"strconv"

	"longheng.io/server/internal/modules/dnf/dnfenum"
	"longheng.io/server/internal/modules/dnf/worldmap"
)

// 现用E7E080先清理体验对象、恢复保存对象，再经E7D7F0发送502或785。
// 现场两类提交均紧随7142的42/op24，早于后续op15；它确认已经发生的返回，
// 不允许在仍活动的体验图或其它副本中直接修改正式职业。
func (s *Service) confirmCurrentProfessionTrialTownReturn(session *gameSession) error {
	if session == nil || session.selectedCharacterID == 0 {
		return nil
	}
	session.dungeon.mu.Lock()
	runtime := session.dungeon.runtime
	if runtime == nil {
		pending := session.confirmedDungeonReturnStatePending
		session.dungeon.mu.Unlock()
		if pending {
			return s.ensureCurrentConfirmedDungeonReturnPlayerState(session, "转职请求重试已确认的回城资料")
		}
		return nil
	}
	activeTrial := false
	if runtime.Session != nil {
		run := runtime.Session.Snapshot().Run
		activeTrial = run.Status == worldmap.DungeonRunActive && run.DungeonID == 7142
	}
	confirmed := activeTrial && runtime.Dungeon.ID == 7142 &&
		runtime.Request.DungeonID == 7142 && runtime.Request.RuntimeState == 1 &&
		runtime.Request.RuntimeToken == 0xffff && runtime.partyInstance == nil &&
		runtime.Character.CharacterID == strconv.Itoa(int(session.selectedCharacterID)) &&
		runtime.AccountID == s.accountIDForSession(session) &&
		runtime.townReturnPending && runtime.townReturnOp24Sent &&
		runtime.townReturnRequestMsgID == uint16(dnfenum.CmdPacketGiveupGame) &&
		runtime.townReturnTransition.ActorObjectKey == session.selectedCharacterID
	session.dungeon.mu.Unlock()
	if !confirmed {
		return nil
	}
	return s.commitPendingDungeonReturnBeforeTownAction(
		session, "转职体验结束后提交正式转职", "转职提交确认7142体验已返回城镇", runtime,
	)
}
