package dnfbridge

import (
	"bytes"
	"context"
	"fmt"
	"strings"

	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

// 当前客户端收到完整技能页op19时会清理技能动作；蛇腹剑的清理回调
// 会卸下33号专用武器，但仍有效的附加状态可能阻止逐帧逻辑重新挂载。
// 仅对已学习蛇腹剑的女鬼剑，在入图角色绑定后、op27前同步技能页；
// 完成读条时核对本次准备、角色和完整包体，避免重复清理专用武器。
type currentSnakeDungeonSkillInfo struct {
	characterID string
	body        []byte
}

func (s *Service) prepareCurrentSnakeDungeonSkillInfo(
	session *gameSession, ctx context.Context, character dnfrepo.CharacterRecord, hasCharacter bool, source string,
) error {
	session.snakeDungeonSkillInfo = nil
	if !hasCharacter || strings.TrimSpace(character.Job) != "11" {
		return nil
	}
	// 未学习者不增加技能目录加载或初始化写入；这里仅核对持久化的技能31。
	repos, available := s.repositoryGroup()
	if !available || repos.Skill == nil {
		return nil
	}
	record, found, err := repos.Skill.Load(ctx, character.CharacterID)
	if err != nil {
		return fmt.Errorf("读取蛇腹剑入图技能状态：%w", err)
	}
	state := record.Skills[31]
	if !found || !state.Enabled || state.Level <= 0 {
		return nil
	}
	projection, ready, err := s.buildCurrentSceneSkillInfoProjection(session, ctx, character, source)
	if err != nil || !ready {
		return err
	}
	if projection.learnedLevels[31] <= 0 {
		return nil
	}
	if err := s.sendCurrentSceneSkillInfoProjection(session, projection, source+"_蛇腹剑加载前技能同步"); err != nil {
		return err
	}
	session.snakeDungeonSkillInfo = &currentSnakeDungeonSkillInfo{
		characterID: projection.characterID,
		body:        append([]byte(nil), projection.body...),
	}
	s.logGameEvent(session, "蛇腹剑入图技能前置",
		"角色", projection.characterID, "技能", 31, "包编号", currentSkillInfoMsgID,
		"顺序", "角色mode1绑定后、op27加载前", "包体长度", len(projection.body))
	return nil
}

func (s *Service) sendCurrentFinishLoadingSkillInfo(
	session *gameSession, ctx context.Context, character dnfrepo.CharacterRecord, source string,
) error {
	projection, ready, err := s.buildCurrentSceneSkillInfoProjection(session, ctx, character, source)
	if err != nil || !ready {
		return err
	}
	prepared := session.snakeDungeonSkillInfo
	if session.preDungeonContextPlayerStateSent && prepared != nil &&
		projection.job == 11 && projection.learnedLevels[31] > 0 &&
		prepared.characterID == projection.characterID && bytes.Equal(prepared.body, projection.body) {
		s.logGameEvent(session, "蛇腹剑入图保留被动武器",
			"角色", projection.characterID, "技能", 31, "包编号", currentSkillInfoMsgID,
			"原因", "本次加载前已发送相同完整技能页，避免加载后清理专用武器")
	} else if err := s.sendCurrentSceneSkillInfoProjection(session, projection, source); err != nil {
		return err
	}
	return s.sendSkillSwitchInventoryProjection(session, source+"_skill_switch_inventory")
}
