package dnfbridge

import "longheng.io/server/internal/modules/dnf/dnfenum"

func skillGameplayModule() gameplayModuleDefinition {
	layoutOpcode := uint16(dnfenum.CmdPacketChangeSkillslot)
	buyOpcode := uint16(dnfenum.CmdPacketBuySkill)
	resetOpcode := uint16(dnfenum.CmdPacketSkillInit)
	return gameplayModuleDefinition{

		Name: "skill",
		LegacyHandlers: map[uint16]gameplayHandler{
			uint16(dnfenum.CmdPacketComboSkillInfo): func(s *Service, g *gameSession, r gameplayRequest) error {
				return s.handleCurrentDarkKnightComboSkillInfo(g, r.Body)
			},
			uint16(dnfenum.CmdPacketComboSkillExtensionQuickSlotReset): func(s *Service, g *gameSession, r gameplayRequest) error {
				return s.handleCurrentDarkKnightComboQuickSlotReset(g, r.Body)
			},
		},
		UpperHandlers: map[uint16]gameplayHandler{
			uint16(dnfenum.CmdPacketComboSkillInfo):                    defaultClassGameplayHandler("game-dark-knight-combo-class-blocked", "连招保存分类不符", (*Service).handleCurrentDarkKnightComboSkillInfo),
			uint16(dnfenum.CmdPacketComboSkillExtensionQuickSlotReset): defaultClassGameplayHandler("game-dark-knight-reset-class-blocked", "连招重置分类不符", (*Service).handleCurrentDarkKnightComboQuickSlotReset),
		},
		LegacyNormalizers: map[uint16]gameplayLegacyNormalizer{
			uint16(dnfenum.CmdPacketComboSkillInfo):                    normalizeLegacyDarkKnightComboSkillInfoBody,
			uint16(dnfenum.CmdPacketComboSkillExtensionQuickSlotReset): func(body []byte) []byte { return stripLegacyTransportTrailer(body, 2) },
			layoutOpcode: func(body []byte) []byte {
				return stripLegacyTransportTrailer(body, 8)
			},
			buyOpcode:   stripLegacyBuySkillTransportTrailer,
			resetOpcode: func(body []byte) []byte { return stripLegacyTransportTrailer(body, 2) },
		},
	}
}
