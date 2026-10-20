package dnfbridge

import (
	"context"
	"fmt"

	pvp "longheng.io/server/internal/modules/dnf/pvp"
	repo "longheng.io/server/internal/modules/dnf/repository"
	"longheng.io/server/internal/modules/dnf/skillcmd"
)

// 任务奖励的普通技能快照不能覆盖仍在使用的道场技能。2674的原生领奖
// 会清理训练状态，因此在玩家作出复制/还原选择前恢复已持久化的模式0。
// 已收到1266模式1/2时不重开教学模式；PK频道仍投影PK两页，不能把关闭当作同意复制。
func (s *Service) sendCurrentPVPDojoQuestSkillRefresh(session *gameSession, character repo.CharacterRecord) (bool, error) {
	dojo := currentPVPDojoScene(session) && pvp.DojoSkillMode(character) == 0
	if !dojo && (session == nil || !isPVPChannel(session.channel)) {
		return false, nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), createWriteTimeout)
	defer cancel()
	repos, ok := s.repositoryGroup()
	if !ok || character.AccountID != session.accountID {
		return true, fmt.Errorf("道场领奖技能仓储或归属不符")
	}
	record, err := skillcmd.PrepareDojoSkills(ctx, repos, session.accountID, character.CharacterID)
	if err != nil {
		return true, err
	}
	record, _, err = s.syncCurrentSceneSkillPointLedger(ctx, skillcmd.DojoRepositories(repos, session.accountID, character.CharacterID), character, record)
	if err != nil {
		return true, err
	}
	backup, err := buildCurrentPVPDojoSkills(record)
	if err != nil {
		return true, err
	}
	commands := s.loadSkillCommandProjection(ctx, repos, character.CharacterID)
	body, _, err := buildCurrentSceneSkillInfoBody(record, record.Layouts[repo.ActiveSkillTree(record)], commands)
	if err != nil {
		return true, err
	}
	state := buildCurrentFinishLoadingCharacterStateBody(character, record.Points)
	if len(state) != currentFinishLoadingCharacterStateBodySize {
		return true, errCurrentFinishQuestAckShape
	}
	if err := s.sendGameUpperRawClass(session, currentDungeonCharacterStateMsgID, state, 0); err != nil {
		return true, err
	}
	if dojo {
		if err := s.sendGameUpperRawClass(session, currentPVPDojoSkillStateMsgID, []byte{0}, 0); err != nil {
			return true, err
		}
		if err := s.sendGameUpperRawClass(session, 1230, backup, 0); err != nil {
			return true, err
		}
	}
	s.logGameEvent(session, "道场领奖技能刷新", "char_id", session.selectedCharacterID,
		"技能来源", "已保存的道场两页", "总TP", record.Points.TotalTP, "剩余TP", record.Points.RemainingTP,
		"活动页", repo.ActiveSkillTree(record))
	return true, s.sendGameUpperRawClass(session, currentSkillInfoMsgID, body, 0)
}
