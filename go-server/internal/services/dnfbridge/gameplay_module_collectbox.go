package dnfbridge

// 收集箱（COLLECT_BOX_WINDOW）的 gameplay 模块。
//
// 修复前的实现把 op1021/1022/1023/1024 全部当"未实现"丢掉（只记
// game-collectbox-deferred），于是操作者开窗时服务端一个字节都不回，客户端把
// ui/collectbox/collectbox.xui 里三个文本控件的默认占位串
// STR:test string1/2/3（第三个红字）原样显示 —— 这就是「收集箱错误」的现场。
//
// 现在分两档：
//   - op1021（开窗/选箱）按参考实现应答；
//   - op1022（把宝珠放进箱子）在 DNFBRIDGE_COLLECTBOX_MUTATION=reference（默认）
//     时按参考实现受理并落库，见 collectbox_add.go；
//   - op1023/op1024 仍只观测（请求体形态未取证，不发明应答）。
func collectboxGameplayModule() gameplayModuleDefinition {
	legacy := make(map[uint16]gameplayHandler, len(currentCollectboxGameplayOpcodes))
	upper := make(map[uint16]gameplayHandler, len(currentCollectboxGameplayOpcodes))
	for _, opcode := range currentCollectboxGameplayOpcodes {
		opcode := opcode
		handler := func(service *Service, session *gameSession, request gameplayRequest) error {
			if !service.collectboxEnabled() {
				return service.logCurrentCollectboxDeferred(session, request.Opcode, request.Body)
			}
			switch opcode {
			case currentCollectboxSelectMsgID:
				return service.handleCurrentCollectboxSelect(session, request.Body)
			case currentCollectboxAddItemMsgID:
				return service.handleCurrentCollectboxAdd(session, request.Body)
			case currentCollectboxRemoveItemMsgID:
				return service.handleCurrentCollectboxRemoveDisabled(session, request.Body)
			default:
				return service.logCurrentCollectboxObservedMutation(session, request)
			}
		}
		legacy[opcode] = handler
		upper[opcode] = defaultClassGameplayHandler(
			"game-collectbox-command-class-blocked",
			"current_exe_collectbox_command_class_mismatch",
			func(service *Service, session *gameSession, body []byte) error {
				return handler(service, session, gameplayRequest{Opcode: opcode, Body: body})
			},
		)
	}
	return gameplayModuleDefinition{
		Name:           "collectbox",
		LegacyHandlers: legacy,
		UpperHandlers:  upper,
	}
}
