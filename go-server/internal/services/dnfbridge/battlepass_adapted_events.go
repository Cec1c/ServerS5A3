package dnfbridge

import (
	"context"
	"fmt"
	repo "longheng.io/server/internal/modules/dnf/repository"
	"strconv"
)

// 普通副本必须已有最终房间成功结算；ID来自现用dungeon.lst，排除教学、试炼及入口假图。
func currentBattlePassAdaptedDungeonKind(r *runtimeDungeonState) uint32 {
	if !currentBattlePassDungeonClear(r) {
		return 0
	}
	switch r.Dungeon.ID {
	case 316:
		return 24 // MetroCenter/Hell/Hell.dgn 魔界裂缝
	case 5097, 3570:
		return 8 // 艾肯单人/组队遗落的神殿
	case 7100:
		return 44 // 异次元裂缝
	}
	if r.Dungeon.ID >= 7165 && r.Dungeon.ID <= 7179 {
		return 44
	} // 现用异次元裂缝各难度的实际地下城
	return 0
}

// 只由已验证的原生商城点击/成功公会场景入场调用，客户端不能上报任意任务进度。
func (s *Service) recordBattlePassAcceptedEvent(session *gameSession, kind uint32, param int64) error {
	identity, bound, ok := s.playerStatsGameIdentity(session)
	if !ok {
		return nil
	}
	g, ok := s.repositoryGroup()
	if !ok || g.CharacterAssets == nil {
		return fmt.Errorf("战令行为缺少角色资产事务")
	}
	ctx, cancel := context.WithTimeout(session.ctx, createWriteTimeout)
	defer cancel()
	id := strconv.Itoa(int(bound.character))
	now := s.gameplayNow()
	return g.CharacterAssets.WithinCharacterAssets(ctx, id, func(chars repo.CharacterRepository, _ repo.InventoryRepository, _ repo.EquipmentRepository) error {
		if session.loginRevoked.Load() || !s.boundGameSessionCharacterCurrent(bound) {
			return fmt.Errorf("战令行为会话已变化")
		}
		ch, found, err := chars.Load(ctx, id)
		if err != nil {
			return err
		}
		if !found || ch.AccountID != identity.AccountID {
			return fmt.Errorf("战令行为归属不符")
		}
		ch = repo.CloneCharacter(ch)
		repo.AddBattlePassCounter(&ch, kind, param, 1, now)
		return repo.SaveCharacterFields(ctx, chars, ch, repo.CharacterFieldStats)
	})
}
