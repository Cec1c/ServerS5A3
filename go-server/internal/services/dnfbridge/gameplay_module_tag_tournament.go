package dnfbridge

import dnfproto "longheng.io/server/internal/modules/dnf/protocol"

func tagTournamentGameplayModule() gameplayModuleDefinition {
	handlers := map[uint16]gameplayHandler{
		currentTagTournamentWeeklyClaimOpcode: func(s *Service, p *gameSession, q gameplayRequest) error {
			if q.Classification != dnfproto.DefaultChannelClassification || !s.isCurrentTagTournamentChannel(p) {
				return nil
			}
			return s.handleCurrentTagTournamentWeeklyClaim(p, q.Body)
		},
		currentTagTournamentTagOpcode: func(s *Service, p *gameSession, q gameplayRequest) error {
			if q.Classification != dnfproto.DefaultChannelClassification || !s.isCurrentTagTournamentChannel(p) {
				return nil
			}
			return s.handleCurrentTagTournamentTag(p, q.Body)
		},
		currentTagTournamentDeathOpcode: func(s *Service, p *gameSession, q gameplayRequest) error {
			if q.Classification != dnfproto.DefaultChannelClassification || !s.isCurrentTagTournamentChannel(p) {
				return nil
			}
			return s.handleCurrentTagTournamentDeath(p, q.Body)
		},
		currentTagTournamentEnterOpcode: func(s *Service, p *gameSession, q gameplayRequest) error {
			if q.Classification != dnfproto.DefaultChannelClassification || !s.isCurrentTagTournamentChannel(p) {
				return nil
			}
			return s.handleCurrentTagTournamentEnter(p, q.Body)
		},
		currentTagTournamentRosterListOpcode: func(s *Service, p *gameSession, q gameplayRequest) error {
			if q.Classification != dnfproto.DefaultChannelClassification || !s.isCurrentTagTournamentChannel(p) {
				return nil
			}
			return s.handleCurrentTagTournamentRosterList(p, q.Body)
		},
		currentTagTournamentSetRosterOpcode: func(s *Service, p *gameSession, q gameplayRequest) error {
			if q.Classification != dnfproto.DefaultChannelClassification || !s.isCurrentTagTournamentChannel(p) {
				return nil
			}
			return s.handleCurrentTagTournamentSetRoster(p, q.Body)
		},
	}
	// 现用客户端1457/body4经13字节头进入legacy收包路径；upper入口
	// 同样复用账号、会话、等级及已发名单的完整保存校验。
	return gameplayModuleDefinition{Name: "tag-tournament", LegacyHandlers: handlers, UpperHandlers: handlers}
}
