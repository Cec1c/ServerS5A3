package dnfbridge

import (
	"context"
	"fmt"

	repo "longheng.io/server/internal/modules/dnf/repository"
	"longheng.io/server/internal/modules/dnf/skillcmd"
)

// 频道决定技能数据来源；训练复制选择只决定是否作一次复制，不能永久关闭PK技能。
func (s *Service) currentChannelSkillRepositories(ctx context.Context, session *gameSession, character repo.CharacterRecord, base repo.Group) (repo.Group, error) {
	if session == nil || !isPVPChannel(session.channel) {
		return base, nil
	}
	if character.CharacterID != fmt.Sprint(session.selectedCharacterID) || character.AccountID != session.accountID {
		return repo.Group{}, fmt.Errorf("PK频道技能角色归属不符")
	}
	if _, err := skillcmd.PreparePVPSkills(ctx, base, character.AccountID, character.CharacterID); err != nil {
		return repo.Group{}, err
	}
	return skillcmd.PVPRepositories(base, character.AccountID, character.CharacterID), nil
}
