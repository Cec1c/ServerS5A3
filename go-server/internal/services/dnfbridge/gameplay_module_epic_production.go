package dnfbridge

import "longheng.io/server/internal/modules/dnf/dnfenum"

// The pinned official-24.9/v103 image (SHA256 6418373c7cfbacbc...) writes
// 1415..1419 for these commands and registers matching class1 replies.
// The generated NoPack enum describes another command table. Preserve it for
// other profiles; do not shift unrelated commands or state notifications.
func (s *Service) currentEpicProductionCommandID(command dnfenum.CmdPacket) uint16 {
	if s.options.currentClientVersion == "official-24.9" {
		switch command {
		case dnfenum.CmdPacketEpicProductionStartFinish:
			return 1415
		case dnfenum.CmdPacketEpicProductionChangeItem:
			return 1416
		case dnfenum.CmdPacketEpicProductionProcess:
			return 1417
		case dnfenum.CmdPacketEpicProductionMaterialCompound:
			return 1418
		case dnfenum.CmdPacketEpicProductionAbilityOption:
			return 1419
		}
	}
	return uint16(command)
}

func epicProductionGameplayModule() gameplayModuleDefinition {
	operations := []struct {
		command dnfenum.CmdPacket
		handle  func(*Service, *gameSession, []byte) error
	}{
		{dnfenum.CmdPacketEpicProductionStartFinish, (*Service).handleCurrentEpicProductionStart},
		{dnfenum.CmdPacketEpicProductionChangeItem, (*Service).handleCurrentEpicProductionChange},
		{dnfenum.CmdPacketEpicProductionProcess, (*Service).handleCurrentEpicProductionProcess},
		{dnfenum.CmdPacketEpicProductionMaterialCompound, (*Service).handleCurrentEpicProductionCompound},
		{dnfenum.CmdPacketEpicProductionAbilityOption, (*Service).handleCurrentEpicProductionAbility},
	}
	// The two command families overlap. Select by profile, never by ambiguous
	// body length or unconditional aliases that can run another operation.
	handle := func(service *Service, session *gameSession, request gameplayRequest) error {
		for _, operation := range operations {
			if request.Opcode == service.currentEpicProductionCommandID(operation.command) {
				return operation.handle(service, session, request.Body)
			}
		}
		service.logGameEvent(session, "game-epic-production-profile-unhandled",
			"opcode", request.Opcode, "client_version", service.options.currentClientVersion)
		return nil
	}
	upper := func(service *Service, session *gameSession, request gameplayRequest) error {
		if !requireDefaultGameplayClassification(service, session, request,
			"game-upper-epic-production-blocked", "epic_production_command_class_mismatch") {
			return nil
		}
		return handle(service, session, request)
	}
	module := gameplayModuleDefinition{Name: "epic-production",
		LegacyHandlers: make(map[uint16]gameplayHandler), UpperHandlers: make(map[uint16]gameplayHandler)}
	for _, opcode := range []uint16{1415, 1416, 1417, 1418, 1419, 1420, 1421} {
		module.LegacyHandlers[opcode] = handle
		module.UpperHandlers[opcode] = upper
	}
	return module
}
