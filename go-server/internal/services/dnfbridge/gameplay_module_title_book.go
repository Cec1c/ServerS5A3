package dnfbridge

import "longheng.io/server/internal/modules/dnf/dnfenum"

func titleBookGameplayModule() gameplayModuleDefinition {
	putOpcode := uint16(dnfenum.CmdPacketTitleBookPut)
	getOpcode := uint16(dnfenum.CmdPacketTitleBookGet)
	handlePut := func(service *Service, session *gameSession, request gameplayRequest) error {
		return service.handleCurrentTitleBookPut(session, request.Body)
	}
	handleGet := func(service *Service, session *gameSession, request gameplayRequest) error {
		return service.handleCurrentTitleBookGet(session, request.Body)
	}
	return gameplayModuleDefinition{
		Name: "title-book",
		LegacyHandlers: map[uint16]gameplayHandler{
			putOpcode: handlePut,
			getOpcode: handleGet,
		},
		UpperHandlers: map[uint16]gameplayHandler{
			uint16(dnfenum.CmdPacketOtherUserTitleBookList): defaultClassGameplayHandler(
				"game-peer-title-book-blocked", "current_exe_peer_title_book_class_mismatch",
				func(service *Service, session *gameSession, body []byte) error {
					return service.handleCurrentPeerInspectionPage(session, body, false)
				},
			),
			currentPeerBuffInspectCommand: defaultClassGameplayHandler(
				"game-peer-buff-inspect-blocked", "current_exe_peer_buff_inspect_class_mismatch",
				func(service *Service, session *gameSession, body []byte) error {
					return service.handleCurrentPeerInspectionPage(session, body, true)
				},
			),
			putOpcode: defaultClassGameplayHandler(
				"game-title-book-put-blocked",
				"current_exe_title_book_command_class_mismatch",
				func(service *Service, session *gameSession, body []byte) error {
					return service.handleCurrentTitleBookPut(session, body)
				},
			),
			getOpcode: defaultClassGameplayHandler(
				"game-title-book-get-blocked",
				"current_exe_title_book_command_class_mismatch",
				func(service *Service, session *gameSession, body []byte) error {
					return service.handleCurrentTitleBookGet(session, body)
				},
			),
		},
	}
}
