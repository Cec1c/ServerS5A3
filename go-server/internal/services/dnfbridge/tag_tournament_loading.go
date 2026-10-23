package dnfbridge

import "longheng.io/server/internal/modules/dnf/tagtournament"

// 首次29后的1402可能先于客户端场景绑定；1460EC0在配置/容器/当前对象
// 未就绪时直接丢弃，不会替服务端保留重试。首次37完成本体尾包后补投一次。
// 原生对已经出战的同一位置直接返回，成功过的早期通知不会再次切人或重置冷却。
func (s *Service) sendCurrentTagTournamentInitialRoleAfterLoading(p *gameSession) error {
	if p == nil || !s.isCurrentTagTournamentChannel(p) || p.tagTournamentBattle == nil {
		return nil
	}
	b := p.tagTournamentBattle
	prepared := b.prepared
	if prepared == nil || !prepared.entered || !prepared.initialTagLoadPending || p.dungeon.runtime != prepared.runtime || !s.boundGameSessionCharacterCurrent(b.identity) {
		return nil
	}
	phase := b.run.Snapshot().Phase
	if prepared.runtime.townReturnPending || phase != tagtournament.Lobby && phase != tagtournament.Fighting {
		return nil
	}
	if err := s.sendCurrentTagTournamentTagProjection(p, b); err != nil {
		return err
	}
	prepared.initialTagLoadPending = false
	s.logGameEvent(p, "帝国竞技场首次读条后出战资料已补投", "active_native_id", b.nativeRoleIDs[b.run.Snapshot().Active])
	return nil
}
