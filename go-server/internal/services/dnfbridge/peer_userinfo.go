package dnfbridge

import (
	"context"
	"encoding/binary"
	"strconv"

	"longheng.io/server/internal/modules/dnf/dnfenum"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

const currentPeerUserInfoMode = byte(3)

// handleCurrentPeerUserInfoRequest owns the current-client three-byte
// GET_USERINFO request emitted by the town interaction menu. The same legacy
// opcode also owns the role-selector bootstrap, so only the exact mode-3 shape
// is consumed here.
func (s *Service) handleCurrentPeerUserInfoRequest(session *gameSession, body []byte) (bool, error) {
	if len(body) != 3 || body[2] != currentPeerUserInfoMode {
		return false, nil
	}
	targetID := binary.LittleEndian.Uint16(body[:2])
	sourceID := selectedCharacterID(session)
	if sourceID == 0 || targetID == 0 || targetID == sourceID ||
		s.onlinePlayers == nil {
		s.logGameEvent(session, "game-peer-userinfo-request-rejected",
			"source_char_id", sourceID,
			"target_char_id", targetID,
			"mode", body[2],
			"reason", "invalid_or_not_in_same_area")
		return true, nil
	}
	targetSession, online := s.onlineGameSession(targetID)
	if !online {
		s.logGameEvent(session, "game-peer-userinfo-request-rejected",
			"source_char_id", sourceID,
			"target_char_id", targetID,
			"mode", body[2],
			"reason", "target_offline")
		return true, nil
	}

	ctx, cancel := context.WithTimeout(context.Background(), createWriteTimeout)
	defer cancel()
	dungeonPeer := s.sessionHasActiveDungeonRuntime(session) || s.sessionHasActiveDungeonRuntime(targetSession)
	ownerContext := currentTownRemoteActorOwnerContext(session, targetID)
	if dungeonPeer {
		// Mode3 must use the receiver's retained native connection context. Scene
		// owner zero selects the auxiliary map, whose dungeon appearance actors
		// need not own initialized stat objects; the active actor table does.
		ownerContext = currentConnectionTownActorOwnerContext(session)
		if s.options.currentClientVersion != "official-24.9" || ownerContext == 0 ||
			!s.currentDungeonPeerUserInfoAllowed(ctx, session, targetSession, sourceID, targetID) {
			if s.currentCrossSceneProfileMembership(session, targetSession, sourceID, targetID) {
				return true, s.sendCurrentCrossScenePeerProfile(ctx, session, targetSession, sourceID, targetID)
			}
			s.logGameEvent(session, "game-peer-userinfo-request-rejected",
				"source_char_id", sourceID, "target_char_id", targetID,
				"mode", body[2], "reason", "invalid_active_dungeon_peer_or_connection_context")
			return true, nil
		}
	} else if !s.onlinePlayers.PeerInSameArea(sourceID, targetID) {
		if s.currentCrossSceneProfileMembership(session, targetSession, sourceID, targetID) {
			return true, s.sendCurrentCrossScenePeerProfile(ctx, session, targetSession, sourceID, targetID)
		}
		s.logGameEvent(session, "game-peer-userinfo-request-rejected",
			"source_char_id", sourceID, "target_char_id", targetID,
			"mode", body[2], "reason", "invalid_or_not_in_same_area")
		return true, nil
	}

	viewerBound, _ := s.boundGameSessionCharacterSnapshot(session)
	actorBound, _ := s.boundGameSessionCharacterSnapshot(targetSession)
	loadedID, charName, character, found := s.characterForEnter(ctx, targetSession, targetID)
	if !found || loadedID != targetID {
		s.logGameEvent(session, "game-peer-userinfo-request-rejected",
			"source_char_id", sourceID,
			"target_char_id", targetID,
			"mode", body[2],
			"reason", "target_repository_record_unavailable")
		return true, nil
	}
	var legacyRepo dnfrepo.LegacyUserInfoRepository
	if repositories, ok := s.repositoryGroup(); ok {
		legacyRepo = repositories.LegacyUserInfo
	}
	// 「看自己」那条链在登录/选角时先收一条自己的 USERINFO subtype0（uid+名字+职业+成长），
	// 「看别人」这条链此前只有 mode3 详情体、没有任何身份字段。修复档先补发目标自己的
	// subtype0 身份体，让两个窗口拿到同一类身份载体；回退档不构造这条体，下发逐字节不变。
	// 依据与取舍见 peer_userinfo_identity_carrier.go 文件头。
	identityBody := s.peerUserInfoIdentityCarrierBodyForInspect(
		ctx,
		session,
		targetSession,
		character,
		true,
		targetID,
		charName,
	)
	identityUID, identityName, identityJob, identityGrow, identityLevel, identityOK :=
		currentPeerUserInfoIdentityForLog(identityBody)
	identitySent := len(identityBody) != 0
	s.logGameEvent(session, "game-peer-userinfo-identity-carrier",
		"source_char_id", sourceID,
		"target_char_id", targetID,
		"mode", body[2],
		"env", peerUserInfoIdentityCarrierEnv,
		"switch", peerUserInfoIdentityCarrierMode(),
		"sent", identitySent,
		"body_len", len(identityBody),
		"uid", identityUID,
		"name", identityName,
		"job", identityJob,
		"grow_type", identityGrow,
		"level", identityLevel,
		"parsed", identityOK,
		"order", "subtype0_identity_then_mode3_detail",
		"body_source", peerUserInfoIdentityCarrierSource)
	if identitySent {
		if !s.sendPeerUserInfoSubtype0Appearance(session, identityBody, peerUserInfoIdentityCarrierSource, targetID) {
			// 身份载体发送失败只记事件、不阻断既有 mode3 详情体（与队友外观载体同口径）。
			s.logGameEvent(session, "game-peer-userinfo-identity-carrier-send-failed",
				"source_char_id", sourceID,
				"target_char_id", targetID,
				"body_len", len(identityBody))
		}
	}
	ctx = context.WithValue(ctx, nativePeerProfileSkillsKey{}, strconv.Itoa(int(targetID)))
	if buffSwapReapplyEnabled() {
		ctx = context.WithValue(ctx, nativePeerBuffInspectKey{}, strconv.Itoa(int(targetID)))
	}
	response := s.buildCurrentSelectedUserInfoMode3BodyInContext(
		ctx,
		targetSession,
		legacyRepo,
		character,
		true,
		targetID,
		ownerContext,
	)
	if buffSwapReapplyEnabled() {
		buffBody := s.buildNativePeerBuffCarrier(ctx, targetSession, targetID, response)
		if len(buffBody) > 0 {
			if err := s.sendGameUpperRawClass(session, uint16(dnfenum.CmdPacketSetUDPIPPort), buffBody, 0); err != nil {
				return true, err
			}
			s.logGameEvent(session, "game-peer-buff-swap-native-sent", "source_char_id", sourceID, "target_char_id", targetID, "body_len", len(buffBody))
		}
	}
	s.logGameEvent(session, "game-peer-userinfo-response-send",
		"source_char_id", sourceID,
		"target_char_id", targetID,
		"mode", body[2],
		"msg_id", uint16(dnfenum.CmdPacketSetUDPIPPort),
		"classification", 0,
		"body_len", len(response),
		"owner_context", ownerContext,
		"dungeon_peer", dungeonPeer,
		"identity_carrier_sent", identitySent,
		"identity_carrier_body_len", len(identityBody),
		"identity_carrier_switch", peerUserInfoIdentityCarrierMode(),
		// 默认关（DNFBRIDGE_PEER_USERINFO_BODY_HEX）时为空串 ⇒ 逐字节等于现状。
		// 打开后记下这份 body 的 hex，用于对比"t3 闪退侧(1028B) vs 正常侧(2098B)"缺了哪一段。
		"body_hex", peerUserInfoBodyHexForLog(response),
		"body_source", "current_exe_mode3_peer_userinfo")
	err := s.sendGameUpperRawClass(
		session,
		uint16(dnfenum.CmdPacketSetUDPIPPort),
		response,
		0,
	)
	if err == nil {
		s.rememberCurrentPeerProfile(viewerBound, actorBound, ownerContext)
	}
	return true, err
}
