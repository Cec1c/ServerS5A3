package dnfbridge

import (
	"longheng.io/server/internal/modules/dnf/dnfenum"
	dnfproto "longheng.io/server/internal/modules/dnf/protocol"
)

func (s *Service) handleLegacyGamePacket(session *gameSession, raw []byte) error {
	packet, err := dnfproto.ParseLegacyGamePacket(raw)
	if err != nil {
		return err
	}
	body := normalizeLegacyGameBody(packet.Header.Type, packet.Body)
	runtimeName := runtimeCmdPacketName(packet.Header.Cmd, packet.Header.Type)
	runtimeKnown := runtimeCmdPacketKnown(packet.Header.Cmd, packet.Header.Type)
	s.logGamePacket(session, "RECV", "game-legacy", raw,
		"cmd", packet.Header.Cmd,
		"type", packet.Header.Type,
		"runtime_cmd_name", runtimeName,
		"runtime_cmd_known", runtimeKnown,
		"header_seq", packet.Header.Sequence,
		"body_len", len(packet.Body))
	s.logPacketEvent("game-legacy-meta",
		"conn_id", session.connID,
		"channel_id", session.channel.ID,
		"cmd", packet.Header.Cmd,
		"type", packet.Header.Type,
		"runtime_cmd_name", runtimeName,
		"runtime_cmd_known", runtimeKnown,
		"sequence", packet.Header.Sequence,
		"body_len", len(packet.Body),
		"dispatch_body_len", len(body))
	if len(body) != len(packet.Body) {
		s.logPacketEvent("game-legacy-body-normalized",
			"conn_id", session.connID,
			"channel_id", session.channel.ID,
			"type", packet.Header.Type,
			"runtime_cmd_name", runtimeName,
			"runtime_cmd_known", runtimeKnown,
			"raw_body_len", len(packet.Body),
			"dispatch_body_len", len(body))
	}
	sequence := session.guild.purchaseSequence.observe(packet.Header.Sequence)
	return s.handleGameCommandRequest(session, gameplayRequest{
		Opcode: packet.Header.Type, Classification: packet.Header.Cmd, Body: body, Sequence: &sequence,
	})
}

func normalizeLegacyGameBody(typ uint16, body []byte) []byte {
	if typ == uint16(dnfenum.CmdPacketUseVendingMachine) {
		return stripLegacyTransportTrailer(body, 10)
	}
	if typ == uint16(dnfenum.CmdPacketUseStackableAction) {
		return stripLegacyTransportTrailer(body, currentQuestClearCouponBodySize)
	}
	if normalized, ok := currentGameplayModules.NormalizeLegacy(typ, body); ok {
		return normalized
	}
	if dnfenum.GameType(typ) == dnfenum.GameTypeSelectCharacter {
		return stripLegacyCodecPrefix(body, 11)

	}
	return body
}

func stripLegacyBuySkillTransportTrailer(body []byte) []byte {
	const (
		headerLen  = 2
		entryLen   = 4
		tailLen    = 1
		trailerLen = 4
	)
	if len(body) < headerLen+tailLen+trailerLen {
		return body
	}
	semanticLen := headerLen + int(body[1])*entryLen + tailLen
	if len(body) != semanticLen+trailerLen {
		return body
	}
	return append([]byte(nil), body[:semanticLen]...)
}

func stripLegacyEmblemCompoundTransportTrailer(body []byte) []byte {
	if len(body) < 1+currentEmblemCompoundMinInputs*currentEmblemCompoundInputSize+4 {
		return body
	}
	count := int(body[0])
	semanticLength := 1 + count*currentEmblemCompoundInputSize
	if count < currentEmblemCompoundMinInputs || count > currentEmblemCompoundMaxInputs || len(body) != semanticLength+4 {
		return body
	}
	return append([]byte(nil), body[:semanticLength]...)
}

func stripLegacyCodecPrefix(body []byte, payloadLen int) []byte {
	if payloadLen <= 0 || len(body) != payloadLen+5 {
		return body
	}
	return append([]byte(nil), body[5:]...)
}

func stripLegacyTransportTrailer(body []byte, payloadLen int) []byte {
	const trailerLen = 4
	if payloadLen <= 0 || len(body) != payloadLen+trailerLen {
		return body
	}
	return append([]byte(nil), body[:payloadLen]...)
}

func normalizeLegacySetQuestTriggerBody(body []byte) []byte {
	const (
		opcodeEchoLow  = byte(dnfenum.CmdPacketSetQuestTrigger)
		opcodeEchoHigh = byte(uint16(dnfenum.CmdPacketSetQuestTrigger) >> 8)
	)
	if len(body) == currentSetQuestTriggerRequestBodySize+1 &&
		body[0] == opcodeEchoLow &&
		body[1] == opcodeEchoHigh &&
		body[len(body)-1] == 0 {
		return append([]byte(nil), body[:currentSetQuestTriggerRequestBodySize]...)
	}
	return stripLegacyTransportTrailer(body, currentSetQuestTriggerRequestBodySize)
}

func stripLegacyDieCharacterZeroTail(body []byte) []byte {
	const legacyTailLen = 2
	if len(body) != currentDungeonDieCharacterBodySize+legacyTailLen ||
		body[currentDungeonDieCharacterBodySize] != 0 ||
		body[currentDungeonDieCharacterBodySize+1] != 0 {
		return body
	}
	return append([]byte(nil), body[:currentDungeonDieCharacterBodySize]...)
}

func runtimeCmdPacketName(cmd byte, typ uint16) string {
	if dnfenum.GameCmd(cmd) != dnfenum.GameCmdCommand {
		return ""
	}
	return dnfenum.CmdPacketName(typ)
}

func runtimeCmdPacketKnown(cmd byte, typ uint16) bool {
	if dnfenum.GameCmd(cmd) != dnfenum.GameCmdCommand {
		return false
	}
	return dnfenum.IsKnownCmdPacket(typ)
}

func (s *Service) handleGameCommand(session *gameSession, cmd byte, typ uint16, body []byte) error {
	return s.handleGameCommandRequest(session, gameplayRequest{Opcode: typ, Classification: cmd, Body: body})
}

func (s *Service) handleGameCommandRequest(session *gameSession, request gameplayRequest) error {
	cmd, typ, body := request.Classification, request.Opcode, request.Body
	if dnfenum.GameCmd(cmd) != dnfenum.GameCmdCommand {
		return nil
	}
	if s.options.currentClientVersion == "official-24.9" && typ == official249PrevVillageCmdID {
		return s.handleCurrentPrevVillage(session, body)
	}
	if handled, err := currentGameplayModules.dispatchLegacyRequest(s, session, request); handled || err != nil {
		return err
	}
	if handled, err := s.handleCurrentLuckyStarItemRequest(session, request); handled || err != nil {
		return err
	}
	if handled, err := s.handleCurrentUserChannel(session, typ, 1, body); handled || err != nil {
		return err
	}
	if handled, err := s.handleCurrentEpicClaim(session, typ, 1, body); handled || err != nil {
		return err
	}
	if handled, err := s.handleCurrentOrdinaryAttendanceClaim(session, typ, 1, body); handled || err != nil {
		return err
	}
	if handled, err := s.handleCurrentWeeklyAttendanceClaim(session, typ, 1, body); handled || err != nil {
		return err
	}
	if handled, err := s.handleCurrentMonthlyAttendanceClaim(session, typ, 1, body); handled || err != nil {
		return err
	}
	if typ == 689 {
		if handled, err := s.handleCurrentSemesterCumulativeRequest(session, body); handled {
			return err
		}
	}
	if handled, err := s.handleCurrentAccountFriendCommand(session, typ, 1, body); handled || err != nil {
		return err
	}
	if handled, err := s.handleOfficialRepresentAccountCommand(session, typ, 1, body); handled || err != nil {
		return err
	}
	// 装扮属性调整箱（ENUM_CMDPACKET_AVATAR_OPTION_CHANGE，当前 EXE 名字表 = op459）。
	// 现场抓包（packet_log.20260926-022031.txt:14569/:14868/:15139）证明客户端以
	// `kind=game-legacy cmd=1 type=459 body_len=13` 上行，而服务端当时**没有任何回包**
	// ⇒ 操作者看到的"右键无反应"。
	//
	// 这里刻意**不**登记进 currentGameplayModules：那条注册表一旦命中就强制
	// handled=true，会让 DNFBRIDGE_AVATAR_OPTION_CHANGE 的 off 档无法真正回到
	// "控制流与本改动前逐字节一致"。本钩子在开关关闭时返回 handled=false，
	// 于是这条包继续走下面完全相同的分支（含末尾那条 unhandled 日志），
	// 一个下行字节都不发。详见 avatar_option_change.go 顶部的开关说明。
	wishBody := body
	if typ == currentEpicWishUpdateMsgID && len(body) > 0 && body[0] <= 5 {
		wishBody = stripLegacyTransportTrailer(body, 1+4*int(body[0]))
	}
	if handled, err := s.handleCurrentEpicWishCommand(session, typ, dnfproto.DefaultChannelClassification, wishBody); handled || err != nil {
		return err
	}
	if handled, err := s.handleAvatarOptionChangeCommand(session, typ, body); handled || err != nil {
		return err
	}
	// The account/character option family (op197/198/368/447) is a client push
	// with no request id to answer. It is owned here, before the game-command
	// switch, so the four opcodes stop falling through to
	// "unhandled latest game command" (game_option_settings.go).
	if handled, err := s.handleGameOptionCommand(session, typ, body); handled || err != nil {
		return err
	}
	// 强化直播（UPGRADE ROOM，576..591 家族）在这里收口：现场"凯莉 → 强化直播"
	// 点的就是 type=576，参考实现（86JP/A21/厂商 9.16）全都没有 handler，
	// 过去落进匿名 "unhandled latest game command" 桶。现在改成具名、带 body_hex
	// 与域分类的事件，默认仍不发包（与参考逐字节同口径），需要时可开
	// DNFBRIDGE_UPGRADE_ROOM_REPLY=echo_failure 做受控 A/B。见 upgrade_room.go。
	if handled, err := s.handleUpgradeRoomCommand(session, typ, body); handled || err != nil {
		return err
	}
	// Current bb428/82e BUFF emits1426/body6 (VA994D20). Keep the historical
	// FPS enum/default-off route unchanged; never forward malformed current bodies
	// into the historical loading gate. Do not renumber other opcodes.
	if handled, err := s.handleCurrentBuffSwap1426Command(session, typ, body); handled || err != nil {
		return err
	}
	// 技能换装背包（ENUM_CMDPACKET_SKILL_SWITCH_INVENTORY = 1428）也在这里收口。
	// 现场 82 条 RECV 全部 6 字节体、全部落进匿名 "unhandled latest game command"。
	// 默认档（DNFBRIDGE_SKILL_SWITCH_INVENTORY 未设）= 与今天逐字节相同：一个下行
	// 字节都不发，但改成具名 + 带 body_hex 与逐字段解码的事件，便于继续取证；
	// 显式写 record 才按既有 owner 落库。见 skill_switch_inventory.go。
	if handled, err := s.handleSkillSwitchInventoryCommand(session, typ, body); handled || err != nil {
		return err
	}
	switch dnfenum.GameType(typ) {
	case dnfenum.GameTypeLogin:
		// Current EXE evidence proves only the upper class1/op1 590/598-byte
		// request emitted after CHANNELINFO. Keep legacy-shaped op1 silent.
		s.logGameEvent(session, "game-legacy-endpoint-op1-ignored",
			"body_len", len(body),
			"reason", "legacy_endpoint_request_shape_not_proved")
		return nil
	case dnfenum.GameType(dnfenum.CmdPacketExit):
		// Current op3/one-byte channel-exit is structurally ambiguous with the
		// legacy frame and reaches this decoder in live traffic. It still owns
		// the same class1/op3 ACK as the upper route.
		return s.handleCurrentChannelExit(session, body, "legacy_plain_op3")
	case dnfenum.GameTypeGetUserInfo:
		if handled, err := s.handleCurrentPeerUserInfoRequest(session, body); handled || err != nil {
			return err
		}
		// The hidden-probe-deferred and old WSTR bootstrap experiments both
		// leave the current source-built upper-body-bypass client on a black
		// selector.  The user-verified visible role-select chain uses the
		// fixed15 character-list route for legacy GET_USERINFO; upper op8 keeps
		// the ordinary upper envelope.
		return s.sendLegacyGetUserInfoBootstrap(session)
	case dnfenum.GameTypeSelectCharacter:
		s.logInfo("dnfbridge latest select character received",
			"channel_id", session.channel.ID,
			"body_len", len(body))
		return s.sendSelectCharacterState(session, body)
	case dnfenum.GameTypeEnterSelectDungeon:
		s.logInfo("dnfbridge latest enter-select-dungeon received",
			"channel_id", session.channel.ID,
			"body_len", len(body))
		return s.sendEnterSelectDungeonState(session, "game_cmd_15", true, true)

	case dnfenum.GameTypeFinishLoading:
		return s.sendFinishLoadingStatus(session, body)
	case dnfenum.GameType(dnfenum.CmdPacketAgitWarInfo):
		// Live current-client traffic sends legacy type1345 with u32(2)
		// immediately after the first typed op24. It completes the deferred
		// scene tail and HUD gauge only. Replaying actor/finish-loading state at
		// this callback leaves the current EXE update loop with a null actor.
		if !isCurrentLegacyTownSceneReadyAck(body) {
			s.logPacketEvent("game-legacy-town-scene-ready-ack-deferred",
				"conn_id", session.connID,
				"channel_id", session.channel.ID,
				"type", typ,
				"body_len", len(body),
				"reason", "current_exe_type1345_u32_2_shape_required")
			return nil
		}
		s.logGameEvent(session, "game-legacy-town-scene-ready-ack-accepted",
			"type", typ,
			"body_len", len(body),
			"boundary", "after_typed_op24_deferred_tail_and_hud_gauge_only")
		// 登录引导窗口在这里开启（默认关闭的兜底开关专用；见
		// equip_login_wear_window.go）。13 次现场登录 op19 全部落在本 ack 之后 ≤2s。
		s.openEquipLoginWearWindow(session, "legacy_type1345_scene_ready_ack")
		return s.sendCurrentLegacyTownSceneReadyBoundary(session, "legacy_type1345_scene_ready_ack")
	case dnfenum.GameTypeContentsPlayInfo:
		return s.sendContentsPlayInfoState(session, body)
	case dnfenum.GameType(dnfenum.CmdPacketRequestBlacklist):
		return s.sendRequestBlacklistState(session, "legacy_120_request", body)
	case dnfenum.GameType(dnfenum.CmdPacketGuildAllMemberList):
		return s.sendVendorGuildMemberListState(session, "legacy_140_request")

	case dnfenum.GameType(dnfenum.CmdPacketFpsDevideCollect):
		return s.handleGameFpsDevideCollect(session, body)
	case dnfenum.GameType(dnfenum.CmdPacketInformNotice2nd), dnfenum.GameType(dnfenum.CmdPacketOverflowInfo):
		s.logPacketEvent("game-legacy-passive-report-deferred",
			"conn_id", session.connID,
			"channel_id", session.channel.ID,
			"type", typ,
			"runtime_cmd_name", runtimeCmdPacketName(cmd, typ),
			"body_len", len(body),
			"reason", "client_runtime_report_no_reference_s2c_ack")
		return nil
	case dnfenum.GameTypeCreateCharacter:
		return s.handleGameCreateCharacter(session, body)
	case dnfenum.GameTypeStaticsRuntimeTing:
		s.logInfo("dnfbridge latest statics runtime report received; no response",
			"type", typ,
			"runtime_cmd_name", runtimeCmdPacketName(cmd, typ),
			"body_len", len(body))
		return nil
	case dnfenum.GameTypeCheckName:
		return s.handleGameCheckName(session, body)
	case dnfenum.GameType(dnfenum.CmdPacketDeleteCharacter):
		// 选角界面删除角色（class1/op6）。默认 = 修复档：正确解析 + 厂商 ACK + 软删
		// （见 character_delete.go 与 _dist/rename-delete-diagnosis.md §3）。
		// DNFBRIDGE_CHARACTER_DELETE_ACK=legacy 时退回今天那条路：交给 default 的
		// aligned fallback（只判读、不回包），便于 A/B 对照与紧急止损。
		if characterDeleteAckLegacy() {
			s.logGameEvent(session, "game-delete-character-legacy-fallback",
				"type", typ,
				"switch", characterDeleteAckEnv,
				"body_len", len(body),
				"reason", "switch_set_to_legacy_silent_drop_no_reply")
			return s.handleGameLegacyFallbackCommand(session, cmd, typ, body)
		}
		return s.handleGameDeleteCharacter(session, body)
	case dnfenum.GameType(dnfenum.CmdPacketChangeCharacSlot):
		return s.handleChangeCharacterSlot(session, body, false)
	case dnfenum.GameType(dnfenum.CmdPacketChangeCharacName):
		// 选角界面「修改角色名」（class1/op135 ENUM_CMDPACKET_CHANGE_CHARAC_NAME）。
		// 默认 = 具名判读档：解析 + 具名事件 + **零回包**（S2C 形态未取证，且厂商
		// 参考实现也没有这条 handler，见 character_rename.go 与
		// _dist/rename-delete-diagnosis.md §2.4/§2.5/§2.7）。
		// DNFBRIDGE_CHARACTER_RENAME=legacy 时退回今天那条路：交给 default 的
		// aligned fallback（只判读、不回包），便于 A/B 对照与紧急止损。
		if characterRenameLegacy() {
			s.logGameEvent(session, "game-change-charac-name-legacy-fallback",
				"type", typ,
				"switch", characterRenameLegacyEnv,
				"body_len", len(body),
				"reason", "switch_set_to_legacy_silent_drop_no_reply")
			return s.handleGameLegacyFallbackCommand(session, cmd, typ, body)
		}
		return s.handleGameChangeCharacName(session, cmd, typ, body)
	case dnfenum.GameType(currentCmdRepresentNameDuplicateCheck):
		if s.options.currentClientVersion == "official-24.9" && len(body) == 0 {
			return s.handleCurrentStoryDigestAccepted(session, dnfproto.DefaultChannelClassification, body)
		}
		return s.handleRepresentAccountNameDuplicateCheck(session, body)
	case dnfenum.GameType(currentCmdUpdateRepresentAccountName), dnfenum.GameType(currentCmdChangeRepresentAccountName):
		return s.handleUpdateRepresentAccountName(session, body, typ)
	case dnfenum.GameType(dnfenum.CmdPacketStoryDigestUpdate):
		// The current client reaches bodyless op1445 through the legacy game
		// decoder even when the semantic command is a class-1 upper command.
		// Keep one owner for both transports so the accepted story digest is
		// advanced monotonically instead of replaying on every login.
		return s.handleCurrentStoryDigestAccepted(session, dnfproto.DefaultChannelClassification, body)

	default:
		return s.handleGameLegacyFallbackCommand(session, cmd, typ, body)
	}
}

// handleGameLegacyFallbackCommand 是 legacy 分发的兜底：先试 aligned 注册表，
// 未登记才打匿名桶日志并静默丢弃（不回任何字节）。
//
// 抽成函数只有一个理由：`DNFBRIDGE_CHARACTER_DELETE_ACK=legacy` 必须回到
// **今天这条完全相同的路**（同样的 aligned pending-module 证据 + 零回包），
// 而不是另写一份"看起来像"的静默分支。
func (s *Service) handleGameLegacyFallbackCommand(session *gameSession, cmd byte, typ uint16, body []byte) error {
	if handled, err := s.handleAlignedGameCommand(session, cmd, typ, body); handled || err != nil {
		return err
	}
	s.logInfo("dnfbridge unhandled latest game command",
		"cmd", cmd,
		"type", typ,
		"runtime_cmd_name", runtimeCmdPacketName(cmd, typ),
		"runtime_cmd_known", runtimeCmdPacketKnown(cmd, typ),
		"body_len", len(body))
	return nil
}

func isCurrentLegacyTownSceneReadyAck(body []byte) bool {
	return len(body) == 4 &&
		body[0] == 2 &&
		body[1] == 0 &&
		body[2] == 0 &&
		body[3] == 0
}
