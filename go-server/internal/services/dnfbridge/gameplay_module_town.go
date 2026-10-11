package dnfbridge

import "longheng.io/server/internal/modules/dnf/dnfenum"

func townGameplayModule() gameplayModuleDefinition {
	positionOpcode := uint16(dnfenum.CmdPacketSetUserPosition)
	areaOpcode := uint16(dnfenum.CmdPacketSetUserArea)
	teleportOpcode := uint16(dnfenum.CmdPacketTeleport)
	soloTeleportOpcode := uint16(dnfenum.CmdPacketSoloTelepoart)
	return gameplayModuleDefinition{
		Name: "town",
		LegacyHandlers: map[uint16]gameplayHandler{
			positionOpcode: func(service *Service, session *gameSession, request gameplayRequest) error {
				return service.handleTownSetUserPosition(session, request.Body)
			},
			soloTeleportOpcode: func(service *Service, session *gameSession, request gameplayRequest) error {
				return service.handleCurrentSoloTeleport(session, request.Body)
			},
			// 传送药水的 op237 在**当前客户端的 legacy 通道**上到达。现场证据
			// （2026-09-25 香港封包日志，操作者右键"瞬间移动药水"2600014 两次）：
			//
			//	RECV kind=game-legacy conn_id=game-000016 pkt_seq=342 cmd=1 type=237
			//	  runtime_cmd_name=ENUM_CMDPACKET_TELEPORT body_len=13 len=26
			//	  hex=01ed001a00000095b15be6560006004eac270000270074093001
			//	  → slot=6 itemCode=0x0027AC4E(2600014) town=0x27(39) x=2420 y=304
			//
			// 此后服务端**一个字节都没回**（既没有 op237 ACK，也没有
			// `game-town-teleport-potion-*` 事件），下一条上行就是客户端自己发的 op36
			// ⇒ 玩家看到"点了没反应"。真因是这里：op237 只注册进 UpperHandlers，
			// legacy 注册表没有 237（DispatchLegacy 查不到就静默返回 false），
			// 而当前 EXE 走的是 legacy 通道。与 op470 同形，两条通道都注册。
			teleportOpcode: func(service *Service, session *gameSession, request gameplayRequest) error {
				return service.handleTeleportPotion(session, request.Body)
			},
		},
		UpperHandlers: map[uint16]gameplayHandler{
			positionOpcode: defaultClassGameplayHandler(
				"game-town-set-user-position-blocked",
				"current_exe_op35_command_class_mismatch",
				func(service *Service, session *gameSession, body []byte) error {
					return service.handleTownSetUserPosition(session, body)
				},
			),
			areaOpcode: defaultClassGameplayHandler(
				"game-town-set-user-area-blocked",
				"current_exe_op36_command_class_mismatch",
				func(service *Service, session *gameSession, body []byte) error {
					return service.handleTownSetUserArea(session, body)
				},
			),
			teleportOpcode: defaultClassGameplayHandler(
				"game-town-teleport-potion-blocked",
				"current_exe_op237_command_class_mismatch",
				func(service *Service, session *gameSession, body []byte) error {
					return service.handleTeleportPotion(session, body)
				},
			),
			soloTeleportOpcode: defaultClassGameplayHandler(
				"game-town-solo-teleport-blocked",
				"current_exe_op470_command_class_mismatch",
				func(service *Service, session *gameSession, body []byte) error {
					return service.handleCurrentSoloTeleport(session, body)
				},
			),
		},
	}
}
