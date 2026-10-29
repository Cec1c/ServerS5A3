package dnfbridge

import "longheng.io/server/internal/modules/dnf/dnfenum"

func inventoryGameplayModule() gameplayModuleDefinition {
	moveOpcode := uint16(dnfenum.CmdPacketMoveItemspace)
	useStackableOpcode := uint16(dnfenum.CmdPacketUseStackable)
	durabilityOpcode := uint16(dnfenum.CmdPacketDecreaseDurability)
	durabilityHandler := func(service *Service, session *gameSession, request gameplayRequest) error {
		if !requireDefaultGameplayClassification(service, session, request, "game-durability-rejected", "classification_mismatch") {
			if session != nil {
				service.logInfo("dnfbridge durability request classification rejected",
					"conn_id", session.connID, "character_id", session.selectedCharacterID,
					"classification", request.Classification, "body_len", len(request.Body))
			}
			return nil
		}
		return service.handleCurrentDecreaseDurability(session, request)
	}
	return gameplayModuleDefinition{
		Name:           "inventory",
		LegacyHandlers: map[uint16]gameplayHandler{durabilityOpcode: durabilityHandler},
		UpperHandlers:  map[uint16]gameplayHandler{durabilityOpcode: durabilityHandler},
		LegacyNormalizers: map[uint16]gameplayLegacyNormalizer{
			uint16(dnfenum.CmdPacketChangeDeckInfo): func(body []byte) []byte { return stripLegacyTransportTrailer(body, 20) },
			moveOpcode: func(body []byte) []byte {
				return stripLegacyTransportTrailer(body, 28)
			},
			useStackableOpcode: func(body []byte) []byte {
				return stripLegacyTransportTrailer(body, 15)
			},
		},
	}
}
