package dnfbridge

import (
	"context"
	"fmt"

	"longheng.io/server/internal/modules/dnf/tagtournament"
	"longheng.io/server/internal/modules/dnf/worldmap"
)

// 帝国结果页也发送72/{1,2}，没有普通69/70/71翻牌门。只承接本连接已确认
// 通关且收尾已发送的挑战；回城沿原24与真实城镇确认提交，不伪造42请求。
func (s *Service) handleCurrentTagTournamentResultAction(p *gameSession, state, option byte) error {
	b, err := s.currentTagTournamentBattleFor(p)
	if err != nil {
		return err
	}
	focus := state == 2 && option <= 2
	if b.prepared == nil || !b.prepared.entered || p.dungeon.runtime != b.prepared.runtime || b.run.Snapshot().Phase != tagtournament.Finished || (!focus && (state != 1 || option != 2)) {
		s.logGameEvent(p, "帝国竞技场结果页操作已拒绝", "state", state, "option", option)
		return nil
	}
	if err := s.flushCurrentTagTournamentSettlement(p, b); err != nil {
		return err
	}
	if !b.completionSent {
		return fmt.Errorf("帝国竞技场通关通知尚未成功发送")
	}
	if focus {
		return s.sendGameUpperRawClass(p, 72, buildCurrentDungeonOp72SuccessBody(state, option), currentDungeonCardResponseClass)
	}
	ctx, cancel := context.WithTimeout(context.Background(), createWriteTimeout)
	transition, err := s.prepareCurrentDungeonTownTransitionForSession(ctx, p, p.selectedCharacterID)
	cancel()
	if err != nil {
		return err
	}
	if err = s.closeCurrentTagTournamentForReturn(p); err != nil {
		return err
	}
	p.dungeon.mu.Lock()
	runtime := p.dungeon.runtime
	if runtime != b.prepared.runtime || p.tagTournamentBattle != b || !s.boundGameSessionCharacterCurrent(b.identity) {
		p.dungeon.mu.Unlock()
		return fmt.Errorf("帝国竞技场结果页返回期间会话或地图已替换")
	}
	// IgnoreDefaultDungeonClear隔离普通翻牌资产；帝国领域终局仍须完成
	// 地图生命周期，才能走完整mode0/mode1/24回城而非中途退出的单独24。
	if runtime.Session.Snapshot().Run.Status == worldmap.DungeonRunActive {
		if err = runtime.Session.Complete(); err != nil {
			p.dungeon.mu.Unlock()
			return err
		}
	}
	if runtime.Session.Snapshot().Run.Status != worldmap.DungeonRunCompleted {
		p.dungeon.mu.Unlock()
		return fmt.Errorf("帝国竞技场通关回城缺少已清空地图的完成状态")
	}
	if !runtime.settlementCardExitAckSent {
		if err = s.sendGameUpperRawClass(p, 72, buildCurrentDungeonOp72SuccessBody(state, option), currentDungeonCardResponseClass); err != nil {
			p.dungeon.mu.Unlock()
			return err
		}
		runtime.settlementCardExitAckSent = true
	}
	p.dungeon.mu.Unlock()
	s.logGameEvent(p, "帝国竞技场结果页返回城镇已接受", "cleared", b.run.Snapshot().Cleared, "request_msg_id", 72)
	return s.sendCurrentCompletedDungeonReturnToTown(p, runtime, transition, 72, "帝国通关结果页完整返回休息室")
}
