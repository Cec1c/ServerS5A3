package dnfbridge

import (
	"longheng.io/server/internal/modules/dnf/dnfenum"
)

// experienceItemGameplayModule owns the character-progression consumable route.
//
// Current NoPack's generated name table registers ENUM_CMDPACKET_INCREASE_STATUS
// as id 30; the client sends the 2-byte main-inventory slot and the class1/op30
// response carries the success record. The route is registered for both the
// legacy transport body and the upper transport, mirroring the op535/op338
// premium routes, because the transport that carries op30 on the current client
// has not been captured yet. The handler itself pins the exact body length, so a
// mis-shaped request can never mutate anything.
func experienceItemGameplayModule() gameplayModuleDefinition {
	opcode := uint16(dnfenum.CmdPacketIncreaseStatus)
	handler := func(service *Service, session *gameSession, body []byte) error {
		return service.handleCurrentExperienceItemUse(session, body)
	}
	return gameplayModuleDefinition{
		Name: "experience-item",
		LegacyHandlers: map[uint16]gameplayHandler{
			opcode: func(service *Service, session *gameSession, request gameplayRequest) error {
				return handler(service, session, request.Body)
			},
		},
		UpperHandlers: map[uint16]gameplayHandler{
			opcode: defaultClassGameplayHandler(
				"game-increase-status-request-blocked",
				"current_exe_op30_command_class_mismatch",
				handler,
			),
		},
	}
}
