package dnfbridge

import (
	"context"
	"fmt"

	repo "longheng.io/server/internal/modules/dnf/repository"
)

// 已发列表缓存只保留身份，不能把它当作装备、转职等完整角色资料。
// 重新读取账号列表后按已经发出的顺序匹配；不重新排序或替换已失效的身份。
func (s *Service) currentTagTournamentLobbyCharacters(p *gameSession, sent []repo.CharacterRecord) ([]repo.CharacterRecord, error) {
	repositories, ok := s.repositoryGroup()
	if !ok {
		return nil, fmt.Errorf("帝国竞技场角色明细仓储不可用")
	}
	ctx, cancel := context.WithTimeout(context.Background(), createWriteTimeout)
	defer cancel()
	current, err := s.listCharacters(ctx, repositories, p.accountID)
	if err != nil {
		return nil, err
	}
	byID := make(map[string]repo.CharacterRecord, len(current))
	for _, ch := range current {
		byID[ch.CharacterID] = ch
	}
	rows := make([]repo.CharacterRecord, len(sent))
	for i, old := range sent {
		ch, found := byID[old.CharacterID]
		if !found || ch.AccountID != p.accountID || ch.Name != old.Name || ch.Slot != old.Slot || ch.Slot < 0 || ch.Stats["delete_flag"] != 0 || ch.Stats["delete_time"] != 0 {
			return nil, fmt.Errorf("帝国竞技场角色表身份已变更，需重新获取选角列表")
		}
		rows[i] = repo.CloneCharacter(ch)
	}
	return rows, nil
}
