package dnfbridge

import "longheng.io/server/internal/modules/dnf/dnfenum"

func inventoryGameplayModule() gameplayModuleDefinition {
	moveOpcode := uint16(dnfenum.CmdPacketMoveItemspace)
	useStackableOpcode := uint16(dnfenum.CmdPacketUseStackable)
	return gameplayModuleDefinition{
		Name: "inventory",
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
