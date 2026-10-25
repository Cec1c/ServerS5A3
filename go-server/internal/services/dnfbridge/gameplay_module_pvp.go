package dnfbridge

import dnfproto "longheng.io/server/internal/modules/dnf/protocol"

func pvpGameplayModule() gameplayModuleDefinition {
	h := func(s *Service, session *gameSession, q gameplayRequest) error {
		if q.Classification != dnfproto.DefaultChannelClassification {
			return nil
		}
		switch q.Opcode {
		case 1266:
			return s.handleCurrentPVPDojoSkillMode(session, q.Body)
		case 1267:
			return s.handleCurrentPVPTrainingReward(session, q.Body)
		case 870:
			return s.handleCheckPVPPrivateCharacter(session, q.Body)
		case 195:
			return s.handlePVPChannelInfo(session, q.Body, "gameplay")
		case 50:
			return s.handleMakePVPRoom(session, q.Body, "gameplay")
		case 51:
			return s.handleEnterPVPRoom(session, q.Body)
		case 52:
			return s.handleSetPVPSeatState(session, q.Body, "gameplay")
		case 53:
			return s.handleSetPVPReadyState(session, q.Body)
		case 54:
			return s.handleSetPVPTeamMode(session, q.Body, "gameplay")
		case 55:
			return s.handleCurrentPVPDeath(session, q.Body)
		case 56:
			return s.handleCurrentPVPTimeout(session, q.Body)
		case 57:
			return s.handleCurrentPVPResultEnd(session, q.Body)
		case 59:
			return s.handleSetPVPMapIndex(session, q.Body, "gameplay")
		case 375:
			return s.handleCurrentPVPRecord(session, q.Body)
		case 1271:
			return s.handleCurrentPVPFatigueUsage(session, q.Body)
		case 1272:
			return s.handleCurrentPVPArcadeResult(session, q.Body)
		case 1285:
			return s.handleCurrentPVPArcadeRoundInfo(session, q.Body)
		case 520:
			return s.handleCurrentPVPMatching(session, q.Opcode, q.Body)
		case 521:
			return s.handleCurrentPVPMatching(session, q.Opcode, q.Body)
		case 570:
			return s.handleCurrentPVPMatching(session, q.Opcode, q.Body)
		case 298:
			return s.handleCurrentPVPMatchSignal(session, q.Opcode, q.Body)
		case 299:
			return s.handleCurrentPVPMatchSignal(session, q.Opcode, q.Body)
		case 376:
			return s.handleCurrentPVPMissionReport(session, q.Opcode, q.Body)
		case 377:
			return s.handleCurrentPVPMissionReport(session, q.Opcode, q.Body)
		case 876:
			return s.handleCurrentPVPMissionReport(session, q.Opcode, q.Body)
		case 877:
			return s.handleCurrentPVPMissionReport(session, q.Opcode, q.Body)
		case 874:
			return s.handleCurrentPVPTotalOrder(session, q.Body)
		case 871:
			return s.handlePVPTotalTeam(session, q.Opcode, q.Body)
		case 872:
			return s.handlePVPTotalTeam(session, q.Opcode, q.Body)
		case 873:
			return s.handlePVPTotalTeam(session, q.Opcode, q.Body)
		}
		return nil
	}
	handlers := map[uint16]gameplayHandler{}
	for _, op := range []uint16{1266, 1267, 870, 195, 50, 51, 52, 53, 54, 55, 56, 57, 59, 375, 1271, 1272, 1285, 520, 521, 570, 298, 299, 376, 377, 876, 877, 874, 871, 872, 873} {
		handlers[op] = h
	}
	legacy := map[uint16]gameplayHandler{}
	for op := range handlers {
		legacy[op] = func(s *Service, p *gameSession, q gameplayRequest) error {
			q.Classification = dnfproto.DefaultChannelClassification
			return h(s, p, q)
		}
	}
	return gameplayModuleDefinition{Name: "pvp", LegacyHandlers: legacy, UpperHandlers: handlers,
		LegacyNormalizers: map[uint16]gameplayLegacyNormalizer{
			1266: func(body []byte) []byte { return stripLegacyTransportTrailer(body, 1) },
			377:  func(body []byte) []byte { return stripLegacyTransportTrailer(body, 3) },
		}}
}
