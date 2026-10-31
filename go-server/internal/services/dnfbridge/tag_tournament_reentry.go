package dnfbridge

import (
	"context"
	"fmt"

	"longheng.io/server/internal/modules/dnf/dungeoncmd"
	"longheng.io/server/internal/modules/dnf/tagtournament"
)

// 已付费客户端不会再发1458；在15打开选择器之前恢复双角色，保持首次
// 购买的1389 -> 27 -> 29顺序。若等到16才创建，27已经切换了场景上下文。
// 只处理休息室里经过校验的当日资格，不购买门票，也不替客户端发送1458。
func (s *Service) restoreCurrentTagTournamentBeforeSelect(p *gameSession) error {
	if p == nil || !s.isCurrentTagTournamentChannel(p) || p.dungeon.runtime != nil {
		return nil
	}
	if _, bound := s.boundGameSessionCharacterSnapshot(p); !bound || !p.accountFromToken || !p.loginPacketSeen {
		return nil
	}
	if b := p.tagTournamentBattle; b != nil {
		if b.prepared != nil && b.run.Snapshot().Phase == tagtournament.Lobby && !b.prepared.creationSent {
			return s.sendCurrentTagTournamentEntry(p, b)
		}
		return nil
	}
	owner, err := s.currentTagTournamentOwner()
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), currentRepositorySnapshotTimeout)
	defer cancel()
	view, err := owner.Lobby(ctx, p.accountID)
	if err != nil {
		return err
	}
	if !view.Registered || !view.Day.Paid {
		return nil
	}
	b, err := s.prepareCurrentTagTournamentEntry(p, []byte{byte(view.Day.Mode)}, true)
	if err != nil {
		return err
	}
	if err := s.sendCurrentTagTournamentEntry(p, b); err != nil {
		return err
	}
	s.logGameEvent(p, "帝国竞技场已付费重入在选图前恢复双角色", "mode", b.plan.Mode, "service_day", b.plan.ServiceDay)
	return nil
}

// 当天已付费时，客户端跳过1458购买并直接发16。重新登录、换频道或退出
// 上一场后，须按持久化资格重建当前连接的完整挑战，不能要求重新购买。
func (s *Service) resumeCurrentTagTournamentEntry(p *gameSession, request dungeoncmd.SelectDungeonRequest) (*currentTagTournamentBattle, error) {
	_, bound := s.boundGameSessionCharacterSnapshot(p)
	if !bound || !s.isCurrentTagTournamentChannel(p) || !p.accountFromToken || !p.loginPacketSeen || p.tagTournamentBattle != nil || p.dungeon.runtime != nil {
		return nil, fmt.Errorf("帝国竞技场已付费重入会话无效或已有挑战")
	}
	if request.Difficulty != 0 || request.EntryOption != 0 || request.SelectionMode != 0 || request.RuntimeState != 0 || request.SpecialMode != 0 {
		return nil, fmt.Errorf("帝国竞技场重入选图状态无效")
	}
	if err := validateCurrentDungeonEntryRequestForModes(request, currentDungeonEntryStrictValidation, currentDungeonEntrySelectionModeStrict); err != nil {
		return nil, err
	}
	owner, err := s.currentTagTournamentOwner()
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), currentRepositorySnapshotTimeout)
	defer cancel()
	view, err := owner.Lobby(ctx, p.accountID)
	if err != nil {
		return nil, err
	}
	if !view.Registered || !view.Day.Paid {
		return nil, fmt.Errorf("帝国竞技场重入缺少当日已付费队伍")
	}
	dungeon, ok := s.tagTournamentCatalog.Dungeon(view.Day.Mode)
	if !ok || dungeon.ID != request.DungeonID {
		return nil, fmt.Errorf("帝国竞技场重入地下城与当日锁定模式不符")
	}
	return s.prepareCurrentTagTournamentEntry(p, []byte{byte(view.Day.Mode)}, true)
}
