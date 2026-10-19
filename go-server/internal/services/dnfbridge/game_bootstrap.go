package dnfbridge

import (
	"context"
	"encoding/binary"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	"math/bits"
	"strconv"
	"strings"
	"time"

	"longheng.io/server/internal/modules/dnf/channelcatalog"
	"longheng.io/server/internal/modules/dnf/dnfenum"
	dnfproto "longheng.io/server/internal/modules/dnf/protocol"
)

// sendGameInitial sends the endpoint success only after the current client
// answers CHANNELINFO with its native class1/op1 request.
func (s *Service) sendGameInitial(session *gameSession) (mode string, bodyLen int, sent bool, err error) {
	body := s.buildLoginSuccess(session.channel)
	err = s.sendGameUpperSuccess(session, uint16(dnfenum.UpperMsgGameEndpoint), body)
	return gameInitialModeUpper, len(body), true, err
}

// sendGameConnectionBootstrap separates the launcher's all-port discovery
// probes from the one account-bound game connection. CHANNELINFO writes the
// current EXE's native scene-channel global, so it is valid only when the same
// connection can represent that channel in the u8 town actor owner field.
//
// 顺序契约（2026-09-25 香港现场取证，必须保持）：
// 客户端**只有**在收到 class0/op1 CHANNELINFO 通知后才会发 class1/op1 登录包，
// 而启动器令牌只出现在那个包里（account_login_token.go）。旧实现要求"账号已
// 绑定"才发通知，而账号绑定当时又只能靠来源 IP 猜 —— 于是 IP 猜不中的连接
// 直接收到主动端点成功包，客户端因此永远不发登录包，令牌永远读不到，会话永远
// 停在共享账号 dnf:1。现场 9/9 条落到 dnf:1 的会话都满足
// `request_body_len=0 reason=unbound_channel_discovery_probe` 且
// `RECV ENUM_CMDPACKET_LOGIN` 计数为 0（其中包括把 smcz/czfm 建到 dnf:1 的
// game-000197/000203）。
//
// 现在只要频道自洽就先发通知等登录包，账号在收到登录包后再绑定；这也让
// dnf:1 之类的"账号未定案"状态不再影响握手本身。
// 回退开关：DNFBRIDGE_GAME_PROACTIVE_ENDPOINT_WHEN_UNBOUND=1（见 options.go）。
func (s *Service) sendGameConnectionBootstrap(session *gameSession, now time.Time) error {
	if session == nil {
		return errResidentChannelUnavailable
	}
	accountBound := strings.TrimSpace(session.accountID) != ""
	ownerChannel, ownerErr := currentCommittedChannelOwner(session)
	if ownerErr == nil && (accountBound || !s.options.gameProactiveEndpointWhenUnbound) {
		if err := s.sendCurrentChannelResidentNotice(session, now); err != nil {
			return err
		}
		session.connectionTownActorOwnerChannel = ownerChannel
		session.townActorOwnerChannel = ownerChannel
		// 2026-09-27：会话真实频道 > 255（线上 508）时 owner 取的是 catalog 落点频道。
		// 这条**必须**留下具名事件：否则线上只能看到"owner=10 而客户端在 508"，
		// 判不出这是刻意回退还是又一个错值。见 channel_owner_range.go。
		if _, fits := currentOwnerChannelFieldValue(session.channel.ID); !fits {
			s.logOwnerChannelSubstitution(session, "game_connection_bootstrap", int(ownerChannel))
		}
		return nil
	}
	if ownerErr != nil {
		// 连落点频道都装不下（或落点不可用）：owner 字段无法表达本会话，
		// 只留下 context 0 并明确记录，绝不发截断值。
		s.logOwnerChannelUnrepresentable(session, "game_connection_bootstrap_endpoint_success", "session_owner_context_zero")
	}

	session.endpointHandshakeMu.Lock()
	defer session.endpointHandshakeMu.Unlock()
	if session.gameEndpointSuccessSent {
		return nil
	}
	mode, bodyLen, sent, err := s.sendGameInitial(session)
	if err != nil || !sent {
		return err
	}
	session.gameEndpointSuccessSent = true
	reason := "unbound_channel_discovery_probe"
	if accountBound {
		reason = "resident_channel_cannot_fit_current_exe_town_owner"
	}
	s.logGameEvent(session, "game-endpoint-success-sent",
		"mode", mode,
		"body_len", bodyLen,
		"request_body_len", 0,
		"target_channel", session.channel.ID,
		"resident_owner_error", ownerErr,
		"reason", reason)
	return nil
}

// handleCurrentGameEndpointRequest completes the current EXE's one-shot
// CHANNELINFO handshake. The class0/op1 notice makes the client send this
// class1/op1 request and arm its watchdog; the first class1/op1 success clears
// that watchdog. Later endpoint probes must remain write-silent.
func (s *Service) handleCurrentGameEndpointRequest(session *gameSession, classification byte, wireBodyLen int) error {
	if session == nil {
		return errResidentChannelUnavailable
	}
	session.endpointHandshakeMu.Lock()
	defer session.endpointHandshakeMu.Unlock()

	if classification != dnfproto.DefaultChannelClassification ||
		!isCurrentChannelReconnectDisplayProbeBodyLen(wireBodyLen) {
		s.logGameEvent(session, "game-endpoint-client-op1-ignored",
			"classification", classification,
			"body_len", wireBodyLen,
			"target_channel", session.channel.ID,
			"reason", "current_exe_endpoint_request_shape_mismatch")
		return nil
	}
	if !session.currentChannelResidentNoticeSent {
		s.logGameEvent(session, "game-endpoint-client-op1-ignored",
			"classification", classification,
			"body_len", wireBodyLen,
			"target_channel", session.channel.ID,
			"reason", "channelinfo_notice_not_committed")
		return nil
	}
	if session.gameEndpointSuccessSent {
		s.logGameEvent(session, "game-channel-stale-endpoint-probe-ignored",
			"body_len", wireBodyLen,
			"target_channel", session.channel.ID,
			"reason", "single_class1_op1_already_sent_after_client_request")
		return nil
	}

	mode, bodyLen, sent, err := s.sendGameInitial(session)
	if err != nil {
		return err
	}
	if !sent {
		return nil
	}
	session.gameEndpointSuccessSent = true
	s.logGameEvent(session, "game-endpoint-success-sent",
		"mode", mode,
		"body_len", bodyLen,
		"request_body_len", wireBodyLen,
		"target_channel", session.channel.ID,
		"reason", "current_exe_channelinfo_triggered_login_request")
	if err := s.sendCurrentAuctionServicesOpen(session); err != nil {
		return err
	}
	return nil
}

func encNoticeBody(body []byte) []byte {
	encoded := make([]byte, len(body))
	for i, b := range body {
		encoded[i] = bits.RotateLeft8(b, 2) ^ 0xd6
	}
	return encoded
}

func (s *Service) initialNoticeWireBody(body []byte) ([]byte, bool, string) {
	if s.gameUpperBodyCodecMode() == gameUpperBodyCodecPlain {
		return append([]byte(nil), body...), false, "csharp_initial_notice_plain"
	}
	return encNoticeBody(body), true, "csharp_initial_notice_rotl2_xor_d6"
}

// sendPreInitialBootstrap retains the historical option hook for tests and
// configuration compatibility. The live connection path sends CHANNELINFO
// directly and does not call this legacy write-silent hook.
func (s *Service) sendPreInitialBootstrap(_ *gameSession) error {
	return nil
}

func (s *Service) gamePreBootstrapMode() string {
	if s.options.gamePreBootstrap == "" {
		return gamePreBootstrapNone
	}
	return s.options.gamePreBootstrap
}

// sendPostInitialBootstrap 保留旧探针入口，但当前 C# 对齐链路禁止首包后主动补包。
func (s *Service) sendPostInitialBootstrap(session *gameSession, initialMode string, initialSent bool) error {
	// GET_USERINFO/SELECT_CHARACTER 后续初始化链是被动回包，不能在首包后主动推。
	return nil
}

func (s *Service) gamePostBootstrapMode() string {
	if s.options.gamePostBootstrap == "" {
		return gamePostBootstrapNone
	}
	return s.options.gamePostBootstrap
}

func (s *Service) gameUpperHeaderMode() string {
	if s.options.gameUpperHeader == "" {
		return gameUpperHeaderChannel13
	}
	return s.options.gameUpperHeader
}

func (s *Service) gameInitialMode() string {
	if s.options.gameInitialMode == "" {
		return gameInitialModeNotice
	}
	return s.options.gameInitialMode
}

func (s *Service) sendUpperClientCheckList(session *gameSession) error {
	var writer packetWriter
	writer.writeByte(1)
	writer.writeUint32(0)
	return s.sendGameUpperSecondaryRaw(session, uint16(dnfenum.UpperMsgClientCheck), writer.bytes())
}

func (s *Service) sendUpperGuardClear(session *gameSession) error {
	var writer packetWriter
	writer.writeUint32(1)
	writer.writeZero(8)
	writer.writeByte(0)
	writer.writeZero(12)
	writer.writeUint32(0)
	writer.writeUint32(0)
	writer.writeUint32(0)
	return s.sendGameUpperSecondaryRaw(session, uint16(dnfenum.UpperMsgGuardControl), writer.bytes())
}

func (s *Service) sendUpperEmptyRosterInit(session *gameSession) error {
	return s.sendGameUpperCharacterRosterRaw(session, buildCSharpEmptyRosterBody())
}

func buildCSharpEmptyRosterBody() []byte {
	return buildCSharpRosterBody(nil)
}

var emptyDprotoCallbackBody = []byte{0x01, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00}

type csharpSelectInitPacket struct {
	class       byte
	msgID       uint16
	marker      uint32
	file        string
	body        []byte
	occurrence  int
	kind        string
	bodyEncoded bool
	bodyCodec   string
}

var csharpUpperSelectInitTemplate = []csharpSelectInitPacket{
	{class: dnfproto.DefaultChannelClassification, msgID: 0x0004, kind: "select_ack"},
}

func expandedCSharpUpperSelectInitTemplate() []csharpSelectInitPacket {
	return append([]csharpSelectInitPacket(nil), csharpUpperSelectInitTemplate...)
}

func isLargeLongHengSceneRosterPacket(packet csharpSelectInitPacket, body []byte) bool {
	return packet.kind == csharpLongHengSceneBootstrapKind &&
		packet.msgID == uint16(dnfenum.UpperMsgCharacterRoster) &&
		len(body) > 10000
}

func deferredSelectSceneTailPackets() []csharpSelectInitPacket {
	out := make([]csharpSelectInitPacket, 0, len(longHengSceneBootstrapBeforeHudPackets))
	afterLargeRoster := false
	for _, packet := range longHengSceneBootstrapBeforeHudPackets {
		// The current acceptable-quest notification is rebuilt from PVF/DB state
		// at send time.  It must not depend on the old large-roster fixture marker:
		// the current bootstrap inserts dynamic userinfo rows instead, so that marker
		// is absent and would otherwise suppress op0x15 forever.  This function is
		// only reached after either dungeon op29 or the completed-character town
		// route has committed the selected actor, satisfying dword_52B5BB0.
		if packet.kind == currentAcceptableQuestListKind {
			out = append(out, packet)
			continue
		}
		if afterLargeRoster {
			out = append(out, packet)
			continue
		}
		if isLargeLongHengSceneRosterPacket(packet, packet.body) {
			afterLargeRoster = true
		}
	}
	// The old 24-record HUD phase is intentionally not carried into the current
	// deferred tail; its bodies do not match current op583 or op851.
	return out
}

func (s *Service) sendUpperGetUserInfoBootstrap(session *gameSession) error {
	// GET_USERINFO is the role-select phase and must remain passive. The current
	// represent/adventure-name state opens a modal, so it is deliberately sent
	// only after the selected town scene has completed its initialization.
	// The roster must never be gated on first-time registration.
	//
	// The current client can deliver its three-byte op8 request through the
	// legacy stream decoder. Record the semantic roster transition here, where
	// both upper and legacy op8 converge, so the following upper op4 is not
	// mistaken for a fresh channel-reconnect selection.
	session.rosterRequested = true
	session.representAccountNamePending = false
	if err := s.prepareGetUserInfoPassiveAccountState(session); err != nil {
		return err
	}
	return s.sendUpperGetUserInfoRosterBootstrap(session)
}

func (s *Service) sendLegacyGetUserInfoBootstrap(session *gameSession) error {
	return s.sendUpperGetUserInfoBootstrap(session)
}

func (s *Service) deferUpperGetUserInfoBootstrapUntilHiddenProbe(session *gameSession) error {
	if err := s.prepareGetUserInfoPassiveAccountState(session); err != nil {
		return err
	}
	session.pendingCharacterRosterBootstrap = true
	s.logGameEvent(session, "game-upper-getuserinfo-roster-deferred",
		"source", "legacy_getuserinfo",
		"reason", "wait_for_charac_view_hidden_info_probe")
	return nil
}

func (s *Service) prepareGetUserInfoPassiveAccountState(session *gameSession) error {
	session.selectorAdventureInfoPending = false
	session.selectorAdventureInfoSlot = 0
	repositories, ok := s.repositoryGroup()
	if ok && repositories.Account != nil {
		ctx, cancel := context.WithTimeout(context.Background(), createWriteTimeout)
		defer cancel()
		account, found, err := repositories.Account.Load(ctx, s.accountIDForSession(session))
		if err != nil {
			s.logGameEvent(session, "game-represent-account-name-state-load-failed",
				"account_id", s.accountIDForSession(session),
				"error", err)
			return err
		}
		name := ""
		if found {
			name = strings.TrimSpace(account.RepresentAccountName)
		}
		if name == "" {
			// The current client can surface this state packet as an account
			// protection/password-style modal over the selector.  GET_USERINFO
			// must stay a passive role-list response; represent/adventure-name
			// registration is handled only when the client explicitly submits one
			// of the represent-name commands.
			session.representAccountNamePending = false
			s.logGameEvent(session, "game-represent-account-name-state-passive-deferred",
				"account_id", s.accountIDForSession(session),
				"account_found", found,
				"source", "getuserinfo_before_roster",
				"reason", "do_not_open_registration_or_security_modal_during_getuserinfo")
		} else {
			if slot, ok := currentSelectorAdventureInfoSlot(account.Metadata); ok {
				session.selectorAdventureInfoPending = true
				session.selectorAdventureInfoSlot = uint16(slot)
			}
			s.logGameEvent(session, "game-represent-account-name-state-passive-deferred",
				"account_id", s.accountIDForSession(session),
				"account_found", found,
				"name_byte_len", len(name),
				"source", "getuserinfo_before_roster",
				"selector_adventure_info_pending", session.selectorAdventureInfoPending,
				"selector_adventure_info_slot", session.selectorAdventureInfoSlot,
				"reason", "registered_state_still_opens_current_client_registration_modal")
		}
	}
	return nil
}

func (s *Service) sendUpperGetUserInfoRosterBootstrap(session *gameSession) error {
	return s.sendUpperGetUserInfoRosterBootstrapEnvelope(session, "ordinary_upper")
}

func (s *Service) sendUpperGetUserInfoRosterBootstrapFixed16(session *gameSession) error {
	return s.sendUpperGetUserInfoRosterBootstrapEnvelope(session, "legacy_hidden_probe_fixed16_zero_prefix")
}

func (s *Service) sendUpperGetUserInfoRosterBootstrapEnvelope(session *gameSession, envelope string) error {
	body := s.buildUpperGetUserInfoRosterBody(session)
	s.logPacketEvent("game-upper-get-userinfo-bootstrap-send",
		"conn_id", session.connID,
		"channel_id", session.channel.ID,
		"source", "repository_roster_wstr",
		"envelope", envelope,
		"packet_count", 1,
		"body_len", len(body))
	if len(body) >= 15 {
		s.logGameEvent(session, "game-character-roster-prefix",
			"repository_count", body[1],
			"display_count", body[1],
			"selected_page", binary.LittleEndian.Uint16(body[7:9]),
			"entry_count", binary.LittleEndian.Uint16(body[13:15]),
			"prefix_len", 15)
	}
	switch envelope {
	case "legacy_hidden_probe_fixed16_zero_prefix":
		if err := s.sendGameUpperClass0Fixed16ZeroPrefix(session, uint16(dnfenum.UpperMsgCharacterRoster), body, "legacy_getuserinfo_hidden_probe_roster"); err != nil {
			return err
		}
	case "fixed15_character_list":
		if err := s.sendGameFixed15Route(
			session,
			byte(dnfenum.GameCmdNotice),
			uint16(dnfenum.GameTypeCharacterList),
			body,
			latestCharacterStateRoute,
		); err != nil {
			return err
		}
	default:
		if err := s.sendGameUpperSecondaryRaw(session, uint16(dnfenum.UpperMsgCharacterRoster), body); err != nil {
			return err
		}
	}

	noteEmptyRosterSlotProbe(session, body)
	s.rememberCurrentRenameRoster(session, session.currentRenameRosterPrepared)
	if repos, ok := s.repositoryGroup(); ok && repos.Character != nil {
		ctx, cancel := context.WithTimeout(context.Background(), createWriteTimeout)
		characters, err := s.listCharacters(ctx, repos, s.accountIDForSession(session))
		if err == nil {
			err = s.sendCurrentDarkKnightComboPreload(session, ctx, characters, "get_userinfo_roster")
		}
		cancel()
		if err != nil {
			return err
		}
	}
	// Current NoPack keeps the role-selector adventure name/level outside the
	// mode2 roster body. Do not append op1340 here: the client has not yet
	// acknowledged that the selector objects are ready, and the former
	// all-roster fan-out crashed the current executable. The verified op645
	// hidden-info probe flushes one remembered-slot snapshot later.
	return nil
}

func (s *Service) buildUpperGetUserInfoRosterBody(session *gameSession) []byte {
	session.currentRenameRosterPrepared = nil
	repos, ok := s.repositoryGroup()
	// 这里就是"玩家即将看到哪个角色表"的决定性时刻：账号如果仍然没识别出来，
	// 服务端就要用共享兜底账号的角色表回答 —— 同一台机器上换任何账号登录都会
	// 看到同一批角色。必须留下 WARN（不能只写包日志：包日志是缓冲写）。
	// 注意这条路径不依赖登录包是否来过，因此客户端"永远不发 class1/op1"时也能判读。
	if s.sessionUsesSharedAccount(session) {
		s.warnSharedAccountFallback(session)
	}
	accountID := s.accountIDForSession(session)
	if !ok || repos.Character == nil {
		s.logGameEvent(session, "game-upper-getuserinfo-roster-repository-missing", "account_id", accountID)
		return buildCSharpEmptyRosterBody()
	}
	ctx, cancel := context.WithTimeout(context.Background(), createWriteTimeout)
	defer cancel()

	characters, err := s.listCharacters(ctx, repos, accountID)
	if err != nil {
		s.logGameEvent(session, "game-upper-getuserinfo-roster-list-failed", "account_id", accountID, "error", err)
		return buildCSharpEmptyRosterBody()
	}
	// GET_USERINFO 是客户端请求触发的被动角色表；这里只从仓库构造 roster，不触发栏位扩展包。
	s.logGameEvent(session, "game-upper-getuserinfo-roster-built", "account_id", accountID, "count", len(characters))
	session.currentRenameRosterPrepared = append([]dnfrepo.CharacterRecord(nil), characters[:rosterCount(characters)]...)
	return s.buildCurrentRosterBody(characters)
}

func (s *Service) buildInitialLoginNotice(channel channelcatalog.Channel) []byte {
	var writer packetWriter
	// C# BuildInitialLoginNotice 只写旧首包字段。DOVE 抓到的 335 字节前缀
	// 是单次运行证据，不能作为所有频道/会话通用模板。
	writer.writeByte(1)
	writer.writeAsciiDstr(noticeChanName(channel))
	writer.writeInt32(0)
	writer.writeInt32(0)
	writer.writeByte(byte(s.channelAdvertiseID()))
	// owner/频道字节是 u8：装不下（>255）时写 context 0，绝不写 `byte(id)` 的截断值
	// （`byte(508)` = 252 会让客户端把 252 当成另一个真实频道）。见 channel_owner_range.go。
	channelField, _ := currentOwnerChannelFieldValue(channel.ID)
	writer.writeByte(channelField)
	writer.writeByte(0)
	writer.writeInt32(int(time.Now().Unix()))
	writer.writeInt32(1)
	writer.writeAsciiDstr(s.options.serverIP)
	writer.writeInt32(s.options.initialUDPPort1)
	writer.writeInt32(s.options.initialUDPPort2)
	writer.writeByte(0)
	writer.writeByte(0)
	writer.writeInt32(s.options.commandCount)
	writer.writeInt32(s.options.notificationCount)
	return writer.bytes()
}

func noticeChanName(channel channelcatalog.Channel) string {
	return dnfenum.ChannelNamePrefix + strconv.Itoa(channel.ID)
}

func (s *Service) buildLatestUpperInitBody(_ *gameSession, request []byte) []byte {
	requestValue := uint16(0)
	if len(request) >= 2 {
		requestValue = binary.LittleEndian.Uint16(request[:2])
	}
	channelOrArea := requestValue
	if channelOrArea == 0 {
		channelOrArea = uint16(dnfenum.LoginChannelServerIndex)
	}

	var writer packetWriter
	writer.writeUint32(1)
	writer.writeUint32(0)
	writer.writeUint16(channelOrArea)
	writer.writeUint16(0)
	writer.writeUint16(0)
	writer.writeUint16(0)
	writer.writeByte(0)
	writer.writeUint32(0)

	// 最新客户端固定读取 30 行表项；0xFFFF 是跳过该行的哨兵。
	for range 30 {
		writer.writeUint16(0xffff)
		writer.writeUint32(0)
	}

	writer.writeUint32(0)
	writer.writeByte(2)
	writer.writeUint16(channelOrArea)
	writer.writeUint16(0)
	writer.writeByte(0)
	writer.writeUint16(0)
	writer.writeByte(0)
	return writer.bytes()
}

func (s *Service) buildLoginSuccess(channel channelcatalog.Channel) []byte {
	var writer packetWriter
	writer.writeByte(1)
	writer.writeByte(1)
	writer.writeByte(channel.Type)
	writer.writeByte(1)
	writer.writeUint32(s.options.gameOuterToken)
	writer.writeAsciiDstr(s.options.serverIP)
	writer.writeInt32(channel.Port)
	writer.writeInt32(0)

	// Current NoPack upper_handle_msg1_game_endpoint reads this 24-byte account
	// state tail after the endpoint. Offsets 2 and 3 are useSecurityCard and
	// alreadyAuthentificatedSecurityCard. Keeping both true suppresses the
	// account-protection news modal without changing channel selection.
	var accountState [24]byte
	accountState[2] = 1
	accountState[3] = 1
	writer.writeBytes(accountState[:])
	return writer.bytes()
}

func buildAuctionServiceNotice(serviceType byte, state byte) []byte {
	return []byte{serviceType, state}
}

func zeroBody(count int) []byte {
	if count <= 0 {
		return nil
	}
	return make([]byte, count)
}
