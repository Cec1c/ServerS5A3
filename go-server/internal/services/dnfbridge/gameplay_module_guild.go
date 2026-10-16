package dnfbridge

import "longheng.io/server/internal/modules/dnf/dnfenum"

func guildGameplayModule() gameplayModuleDefinition {
	handlers := map[uint16]func(*Service, *gameSession, []byte) error{
		currentGuildHongbaoListOpcode:                       (*Service).handleCurrentGuildHongbaoList,
		currentGuildHongbaoPointsOpcode:                     (*Service).handleCurrentGuildHongbaoPoints,
		currentGuildMarkOpcode:                              (*Service).handleCurrentGuildMark,
		currentGuildAuraOpcode:                              (*Service).handleCurrentGuildAura,
		currentGuildCargoOpcode:                             (*Service).handleCurrentGuildCargo,
		currentGuildCargoHistoryOpcode:                      (*Service).handleCurrentGuildCargoHistory,
		currentGuildInviteOpcode:                            (*Service).handleCurrentGuildInvitation,
		currentGuildInviteReplyOpcode:                       (*Service).handleCurrentGuildInvitationReply,
		uint16(dnfenum.CmdPacketSetSubGuildMaster):          (*Service).handleCurrentGuildMemberGrade,
		uint16(dnfenum.CmdPacketChangeGuildGradeName):       (*Service).handleCurrentGuildGradeName,
		uint16(dnfenum.CmdPacketChangeGuildGrade):           (*Service).handleCurrentGuildGradePermissions,
		uint16(dnfenum.CmdPacketModifyGuildPromoteMsg):      (*Service).handleCurrentGuildSettings,
		uint16(dnfenum.CmdPacketNotifyMessageToGuild):       (*Service).handleCurrentGuildNotice,
		uint16(dnfenum.CmdPacketWriteGuildMemberMemo):       (*Service).handleCurrentGuildMemberMemo,
		uint16(dnfenum.CmdPacketCheckJoinGuild):             (*Service).handleCurrentGuildJoinCheck,
		uint16(dnfenum.CmdPacketRequestJoinGuild):           (*Service).handleCurrentGuildApply,
		uint16(dnfenum.CmdPacketCancelJoinGuild):            (*Service).handleCurrentGuildCancelApply,
		uint16(dnfenum.CmdPacketGuildJoinList):              (*Service).handleCurrentGuildApplicationList,
		uint16(dnfenum.CmdPacketApproveJoinGuild):           (*Service).handleCurrentGuildApprove,
		uint16(dnfenum.CmdPacketDenyJoinGuild):              (*Service).handleCurrentGuildDeny,
		uint16(dnfenum.CmdPacketCallGuildCreateRight):       (*Service).handleCurrentGuildCreate,
		uint16(dnfenum.CmdPacketCheckGuildNameDouble):       (*Service).handleCurrentGuildNameCheck,
		uint16(dnfenum.CmdPacketCheckGuildCreatePromoteMsg): (*Service).handleCurrentGuildPromoterCheck,
		uint16(dnfenum.CmdPacketRequestGuildCreatePermit):   (*Service).handleCurrentGuildCreatePermit,
		uint16(dnfenum.CmdPacketReplyGuildCreatePermit):     (*Service).handleCurrentGuildCreatePermitReply,
		uint16(dnfenum.CmdPacketCancelGuildCreate):          (*Service).handleCurrentGuildCreateCancel,
		uint16(dnfenum.CmdPacketGuildInfo):                  (*Service).handleCurrentGuildInfo,
		uint16(dnfenum.CmdPacketReqGuildMileageHistory):     (*Service).handleCurrentGuildMileageHistory,
		currentGuildAccountIndexOpcode:                      (*Service).handleCurrentGuildAccountList,
		uint16(dnfenum.CmdPacketReqGuildInfoOfMyChars):      (*Service).handleCurrentGuildAccountList,
		currentGuildSearchOpcode:                            (*Service).handleCurrentGuildSearch,
		uint16(dnfenum.CmdPacketReqRecommendGuild):          (*Service).handleCurrentGuildRecommend,
		uint16(dnfenum.CmdPacketReqGuildSerchForJoin):       (*Service).handleCurrentGuildDetail,
		uint16(dnfenum.CmdPacketJoinGuildInfo):              (*Service).handleCurrentGuildApplicationNotice,
	}
	for _, op := range []dnfenum.CmdPacket{dnfenum.CmdPacketGuildMemerList, dnfenum.CmdPacketGuildMemerListNext, dnfenum.CmdPacketGuildAllMemberList, dnfenum.CmdPacketGuildAllMemberListNext} {
		op := uint16(op)
		handlers[op] = func(s *Service, session *gameSession, _ []byte) error {
			return s.handleCurrentGuildMemberListForOpcode(session, op)
		}
	}
	for _, op := range []uint16{currentGuildGradesOpcode, currentGuildGradesNextOpcode} {
		op := op
		handlers[op] = func(s *Service, session *gameSession, _ []byte) error { return s.handleCurrentGuildGrades(session, op) }
	}
	legacy := map[uint16]gameplayHandler{}
	upper := map[uint16]gameplayHandler{}
	for opcode, route := range handlers {
		opcode, route := opcode, route
		run := func(s *Service, session *gameSession, body []byte) error {
			// The selector asks for its empty roster before a character is bound.
			if session.selectedCharacterID == 0 && opcode == uint16(dnfenum.CmdPacketGuildAllMemberList) {
				return s.sendVendorGuildMemberListState(session, "guild_unselected")
			}
			if _, err := s.currentGuildOwnedSelectedCharacter(session); err != nil {
				if opcode == currentGuildHongbaoListOpcode || opcode == currentGuildHongbaoPointsOpcode {
					return s.sendCurrentGuildHongbaoFailure(session, opcode)
				}
				if opcode == currentGuildCargoOpcode || opcode == currentGuildCargoHistoryOpcode {
					return s.sendGameUpperFailure(session, opcode, 8)
				}
				if opcode == uint16(dnfenum.CmdPacketSetSubGuildMaster) {
					return s.sendGameUpperFailure(session, opcode, 3)
				}
				if opcode == uint16(dnfenum.CmdPacketNotifyMessageToGuild) || opcode == uint16(dnfenum.CmdPacketWriteGuildMemberMemo) || opcode == uint16(dnfenum.CmdPacketModifyGuildPromoteMsg) {
					return s.sendGameUpperFailure(session, opcode, 34)
				}
				return s.rejectCurrentGuildRequest(session, opcode, body)
			}
			s.logGameEvent(session, "game-guild-request", "opcode", opcode, "body_len", len(body))
			return route(s, session, body)
		}
		legacy[opcode] = func(s *Service, session *gameSession, request gameplayRequest) error {
			return run(s, session, request.Body)
		}
		upper[opcode] = defaultClassGameplayHandler("game-guild-class-rejected", "command_class_mismatch", run)
	}
	upper[currentGuildPurchaseOpcode] = (*Service).handleCurrentGuildPurchase
	legacy[currentGuildPurchaseOpcode] = (*Service).handleCurrentGuildPurchase
	upper[currentGuildContractOpcode] = (*Service).handleCurrentGuildContract
	legacy[currentGuildContractOpcode] = (*Service).handleCurrentGuildContract
	return gameplayModuleDefinition{Name: "guild", LegacyHandlers: legacy, UpperHandlers: upper}
}
