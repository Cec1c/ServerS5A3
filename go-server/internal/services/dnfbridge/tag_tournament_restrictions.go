package dnfbridge

import (
	"encoding/binary"

	"longheng.io/server/internal/modules/dnf/dnfenum"
	"longheng.io/server/internal/modules/dnf/inventory"
	dnfproto "longheng.io/server/internal/modules/dnf/protocol"
	"longheng.io/server/internal/modules/dnf/tagtournament"
)

// 在两种分发查表之前拦截，包含仍由aligned fallback处理的op19/28/29等。
// 只有已建立的帝国比赛生效，普通频道和休息室不改变原路由。
func (s *Service) rejectCurrentTagTournamentMutation(p *gameSession, request gameplayRequest) (bool, error) {
	if s == nil || p == nil || request.Classification != dnfproto.DefaultChannelClassification || !s.isCurrentTagTournamentChannel(p) || p.tagTournamentBattle == nil || p.tagTournamentBattle.run == nil {
		return false, nil
	}
	b := p.tagTournamentBattle
	phase := b.run.Snapshot().Phase
	if !b.run.BattleLocked() && !(b.prepared != nil && (phase == tagtournament.Lobby && !b.prepared.entered || b.prepared.entered && p.dungeon.runtime == b.prepared.runtime)) {
		return false, nil
	}
	// 现用Buff换装登记是1426，历史枚举仍把SkillSwitchInventory指向1428。
	// 此档无1426成功/失败读取器，沿用原处理器拒绝时的静默返回。
	// 开关未启用时1426仍可能是旧加载边界，保留该路由。
	if request.Opcode == currentBuffSwapRegistrationID && buffSwapReapplyEnabled() {
		s.logGameEvent(p, "帝国竞技场比赛中Buff换装登记已拒绝", "opcode", request.Opcode)
		return true, nil
	}
	if request.Opcode == currentSaveGameOption2Opcode || request.Opcode == currentSaveCharacterOptionOpcode {
		// 这些保存没有应答，且仍以原登录角色为拥有者；比赛中不能把
		// 备用角色当前使用的配置写回原登录角色。
		s.logGameEvent(p, "帝国竞技场比赛中角色配置保存已拒绝", "opcode", request.Opcode)
		return true, nil
	}
	op := dnfenum.CmdPacket(request.Opcode)
	switch op {
	case dnfenum.CmdPacketUseRightOfChangeGrowType:
		s.logGameEvent(p, "帝国竞技场比赛中转职书已拒绝", "opcode", request.Opcode)
		return true, s.sendGameUpperFailure(p, request.Opcode, 1)
	case dnfenum.CmdPacketReGrowupChange:
		s.logGameEvent(p, "帝国竞技场比赛中技能栏转职已拒绝", "opcode", request.Opcode)
		return true, s.sendGameUpperSuccess(p, request.Opcode, []byte{99, 0, 0, 0, 0})
	case dnfenum.CmdPacketRequestEventServerLevelUp:
		s.logGameEvent(p, "帝国竞技场比赛中升级券已拒绝", "opcode", request.Opcode)
		return true, s.sendGameUpperFailure(p, request.Opcode, currentLevelUpTicketFailureCode)
	case dnfenum.CmdPacketDecreaseDurability:
		// 帝国不消耗耐久，不能把当前本体装备当作备用角色扣耐久。
		return true, nil
	case dnfenum.CmdPacketUseStackable, dnfenum.CmdPacketUseSharedEffectItem:
		return true, s.answerRejectedUseStackableOpcode(p, op, request.Body, "帝国竞技场比赛进行中，禁止普通消耗品")
	case dnfenum.CmdPacketUseStackableAction:
		// 变身、任务券和字体等动作走515，不能套用44/251的失败正文。
		// 当前515失败读取只需要槽位、列表及一字节动作；不足以读取时只拒绝。
		s.logGameEvent(p, "帝国竞技场比赛中动作道具已拒绝", "opcode", request.Opcode)
		if len(request.Body) < 11 {
			return true, nil
		}
		return true, s.sendGameUpperRawClass(p, request.Opcode, inventory.BuildStackableActionFailure(int16(binary.LittleEndian.Uint16(request.Body)), request.Body[2], byte(binary.LittleEndian.Uint32(request.Body[7:11])), 1), dnfproto.DefaultChannelClassification)
	case dnfenum.CmdPacketMoveItemspace, dnfenum.CmdPacketChangeSkillslot,
		dnfenum.CmdPacketBuySkill, dnfenum.CmdPacketSkillInit, dnfenum.CmdPacketChangeAnotherSkillTree,
		dnfenum.CmdPacketSkillCommandCustomizing, dnfenum.CmdPacketSkillCommandAllDefault,
		dnfenum.CmdPacketSkillQuickSlotSort, dnfenum.CmdPacketSkillSwitchInventory,
		dnfenum.CmdPacketChangeDeckInfo,
		dnfenum.CmdPacketComboSkillInfo, dnfenum.CmdPacketComboSkillExtensionQuickSlotReset,
		dnfenum.CmdPacket(currentTagTournamentSetRosterOpcode):
		s.logGameEvent(p, "帝国竞技场比赛中装备、技能或组队变更已拒绝", "opcode", request.Opcode)
		return true, s.sendGameUpperFailure(p, request.Opcode, 1)
	}
	return false, nil
}
