package dnfbridge

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"longheng.io/server/internal/modules/dnf/party"
)

// 本文件实现**外部可触发的「服务端代发组队邀请」**，只用于自动化测试。
//
// ## 为什么需要它
//
// 现场（2026-09-26 凌晨）已经查明：组队 P2P 的启动权在**客户端**手里 —— 只有
// "客户端自己发起的邀请 → 被邀请方接受"这条链路才会让**被邀请方**也开口发 P2P 首包；
// 服务端单方面推名册（`party_force_trigger.go`）只会让**队长**发一次首包，队员永远沉默，
// 而对向腿的入向在队员的 NAT 上没开 ⇒ 死锁（详见 `_HK部署记录与验收报告.md` §36）。
//
// 要复现真实链路就必须让被邀请方的客户端**弹出真实的邀请对话框**并点"接受"。
// 而"在城里右键对方角色弹出交互菜单"这一步在本机客户端上无法稳定自动化
// （客户端开着点击移动，点角色会变成走过去；TP 也会间歇吞输入）。
//
// 于是把"邀请"这一半搬到服务端：**按与真实邀请完全相同的出口**给被邀请方发
// `class0/op7` 邀请提示，让它的客户端弹出原生对话框；剩下只需要在被邀请方
// **点一下"接受"**（一次点击，而不是一串坐标猜测），它就会自己发 op11 接受。
//
// ## 与真实路径的同构性（逐行对齐 `handleOnlineRequestPeer`）
//
// 真实邀请的服务端出口是两件事，本文件**两步都按原样做**：
//
//	① manager.RecordInvite(invitee, inviteeGen, inviter, inviterGen, partyID, mode)
//	   —— 没有这一步，被邀请方点"接受"发来的 op11 不会被承认；
//	② noticeBody := party.BuildRequestPeerNotice(sourceID, parsed)
//	   sendGameUpperRawClassCodec(targetSession, 0x0007, noticeBody, 0, true)
//	   —— 与真实路径同一个构造函数、同一个 opcode、同一个 classification/编解码参数。
//
// 现场字节对照（真实邀请，`packet_log` 历史样本，17 字节 body）：
//
//	SEND msg_id=7 plain_body_len=17
//	     hex=…0003 00 1e000000 6c01 0000 0000 00000000   ← 邀请方=3, mode=0, v0=30, v1=364
//	     hex=…0002 00 5f1e0600 f408 0000 0000 00000000   ← 邀请方=2, mode=0, v0=401503, v1=2292
//
// 三个值（Value0/Value1/Value2）在真实样本里随邀请方不同而不同、含义未定，因此本文件
// 让它们**可由命令文件指定**（默认 0），便于在不重新部署的前提下做单变量实验。
//
// ## 目标会话（与真实路径同一个访问器）
//
// 目的地解析与真实路径**逐行同构**：`handleOnlineRequestPeer` 用
// `onlineGameSessionCharacterSnapshot(target)`（= 角色目录 `s.gameSessions[charID]`，
// game_sessions.go:400），本文件用 `onlineGameSession(invitee)`（同一个 map，
// game_sessions.go:373）。两处都会在"同角色同时握着两条活传输"时指向目录 owner 那条；
// 因此本文件不做**第二种**取会话方式（那会让代发路径与真实路径分叉）。
//
// ## ③ 同屏前置（与真实路径同一道闸门，**本文件唯一的新增判据**）
//
// 真实路径在 `onlinePlayers.PeerInSameArea(source, target)` 为假时**直接返回**
// （party_online.go:128-136，`game-peer-request-target-outside-area`）：被邀请方的
// 客户端场景里没有邀请者的 actor，原生 op7 处理器拿不到目标，框根本画不出来。
// 代发路径此前没有这道闸门 ⇒ 服务端照发、结果行还写 `ok`，客户端一个像素都不变
// （现场症状正是"日志说发出去了、客户端没反应"；2026-09-26 本机 `90CN_protocol.log`
// 六次 S2C msg=7 全部到达，而只有"被邀请方已走出私有落点房"的那三次弹框并回 op11）。
//
// 判据只认服务端**能证明**的相反事实：两条存在记录都在册、而城镇分桶键不同
// （`town/area/channel/instance`；登录落点的赛丽亚房间是"每角色一份"的私有实例，
// 见 town_copresence_actor_resend.go 的 `area_instance`）。任一端没有存在记录时判据
// 不足 ⇒ 保持改动前行为。回退：`DNFBRIDGE_PARTY_FORCE_INVITE_CO_PRESENCE=off`。
//
// ## 协议（命令文件）
//
//	`DNFBRIDGE_PARTY_FORCE_INVITE_CMD`（默认 `force_invite.cmd`）第一行有效：
//	  `<inviter_char_id> <invitee_char_id> [mode] [value0] [value1] [value2]`
//	结果文件 `…_RESULT`（默认 `force_invite.result`）：
//	  `ok inviter=… invitee=… mode=… body_len=…` 或 `rejected reason=<枚举> …`
//
// ## 安全边界
//
//   - 开关 `DNFBRIDGE_PARTY_FORCE_INVITE` **默认 off**：off 档下不启动 goroutine、
//     不读命令文件、不写结果文件、不发一个字节（`startPartyForceInviteTrigger` 直接 return）。
//   - 只对**在线且权威**的两个角色生效（与 `party_force_trigger.go` 同一套校验）；
//   - 只走真实路径既有的两个原语，不新增任何协议字节、不改任何既有 handler；
//   - 命令文件里出现的每个结果都写进结果文件与具名事件，绝不静默。
const partyForceInviteEnv = "DNFBRIDGE_PARTY_FORCE_INVITE"

const (
	partyForceInviteCommandPathEnv = "DNFBRIDGE_PARTY_FORCE_INVITE_CMD"
	partyForceInviteResultPathEnv  = "DNFBRIDGE_PARTY_FORCE_INVITE_RESULT"
	partyForceInviteIntervalEnv    = "DNFBRIDGE_PARTY_FORCE_INVITE_INTERVAL_MS"
	// partyForceInviteCoPresenceEnv 是"同屏前置"那道闸门的 A/B 开关：默认 require_same_area
	// （两端都有存在记录却不同桶 ⇒ 具名拒绝、零发包），`off` = 逐字节回到改动前。
	partyForceInviteCoPresenceEnv = "DNFBRIDGE_PARTY_FORCE_INVITE_CO_PRESENCE"
)

const (
	defaultPartyForceInviteCommandPath = "force_invite.cmd"
	defaultPartyForceInviteResultPath  = "force_invite.result"
	defaultPartyForceInviteIntervalMS  = 1000
	minPartyForceInviteIntervalMS      = 100
)

// 失败原因枚举（写进结果文件与 `game-force-invite-rejected` 事件）。
const (
	partyForceInviteReasonBadCommand       = "bad_command"
	partyForceInviteReasonZeroID           = "zero_character_id"
	partyForceInviteReasonSameCharacter    = "same_character"
	partyForceInviteReasonInviterNotOnline = "inviter_not_online"
	partyForceInviteReasonInviteeNotOnline = "invitee_not_online"
	partyForceInviteReasonInviterNotAuth   = "inviter_not_authoritative_session"
	partyForceInviteReasonInviteeNotAuth   = "invitee_not_authoritative_session"
	partyForceInviteReasonNotRecorded      = "invite_not_recorded"
	// partyForceInviteReasonNotCoPresent：两端都有城镇存在记录、但分桶键不同
	// （不同频道 / 不同城镇区域 / 被邀请方还在"每角色一份"的私有落点房里）。
	// 这一档不是"服务端发不出去"，而是"发过去客户端也画不出那个框"。
	partyForceInviteReasonNotCoPresent     = "invitee_outside_inviter_area"
	partyForceInviteReasonSendFailedPrefix = "notice_send_failed:"
)

// partyForceInviteCoPresenceMode 是 DNFBRIDGE_PARTY_FORCE_INVITE_CO_PRESENCE 的归一化取值。
type partyForceInviteCoPresenceMode int

const (
	// partyForceInviteCoPresenceRequireSameArea 是默认档（也是零值）：只有服务端**能证明**
	// 两端不在同一个城镇分桶里时才拒绝，所以"存在记录缺失"的测试/探针 Service 字面量
	// （没有 onlinePlayers 或没有登记过）行为与改动前逐字节相同。
	partyForceInviteCoPresenceRequireSameArea partyForceInviteCoPresenceMode = iota
	// partyForceInviteCoPresenceOff：不做这道判据，照发（改动前的行为）。
	partyForceInviteCoPresenceOff
)

func readPartyForceInviteCoPresenceMode(value string) partyForceInviteCoPresenceMode {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "off", "0", "false", "no", "legacy":
		return partyForceInviteCoPresenceOff
	default:
		return partyForceInviteCoPresenceRequireSameArea
	}
}

// partyForceInviteRuntime 记忆已处理过的命令内容（防删除失败时重复发送）。
type partyForceInviteRuntime struct {
	mu   sync.Mutex
	last string
}

func (s *Service) partyForceInviteState() *partyForceInviteRuntime {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.partyForceInviteRuntime == nil {
		s.partyForceInviteRuntime = &partyForceInviteRuntime{}
	}
	return s.partyForceInviteRuntime
}

// forceInvite 返回当前 Service 的归一化档位（零值 options 的测试/探针路径 = off）。
func (s *Service) forceInvite() partyForceMode {
	if s == nil {
		return partyForceOff
	}
	return s.options.partyForceInvite
}

// forceInviteCoPresence 返回"同屏前置"闸门的归一化档位（零值 = require_same_area）。
func (s *Service) forceInviteCoPresence() partyForceInviteCoPresenceMode {
	if s == nil {
		return partyForceInviteCoPresenceRequireSameArea
	}
	return s.options.partyForceInviteCoPresence
}

// partyForceInviteAreaMismatch 只在**两端都有存在记录**、并且管理器的
// `PeerInSameArea` 判否（不同频道 / 不同城镇区域 / 不同私有实例）时返回 true。
// 任一端没有存在记录 ⇒ 判据不足 ⇒ 返回 false，保持改动前的发包行为。
func (s *Service) partyForceInviteAreaMismatch(inviterID, inviteeID uint16) bool {
	if s == nil || s.onlinePlayers == nil {
		return false
	}
	if _, registered := s.townPresenceAreaText(inviterID); !registered {
		return false
	}
	if _, registered := s.townPresenceAreaText(inviteeID); !registered {
		return false
	}
	return !s.onlinePlayers.PeerInSameArea(inviterID, inviteeID)
}

// startPartyForceInviteTrigger 在开关打开时起一个轮询 goroutine；off 档直接返回（零副作用）。
func (s *Service) startPartyForceInviteTrigger(ctx context.Context) {
	if s == nil || ctx == nil || !s.forceInvite().enabled() {
		return
	}
	commandPath := strings.TrimSpace(s.options.partyForceInviteCommandPath)
	resultPath := strings.TrimSpace(s.options.partyForceInviteResultPath)
	interval := s.options.partyForceInviteInterval
	if commandPath == "" || interval <= 0 {
		return
	}
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		s.pollPartyForceInviteCommand(commandPath, resultPath)
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				s.pollPartyForceInviteCommand(commandPath, resultPath)
			}
		}
	}()
}

func (s *Service) pollPartyForceInviteCommand(commandPath, resultPath string) {
	raw, err := os.ReadFile(commandPath)
	if err != nil {
		return
	}
	text := strings.TrimSpace(string(raw))
	if text == "" {
		return
	}
	state := s.partyForceInviteState()
	state.mu.Lock()
	duplicate := text == state.last
	if !duplicate {
		state.last = text
	}
	state.mu.Unlock()
	if duplicate {
		return
	}
	// 先删文件再执行：命令只处理一次。
	_ = os.Remove(commandPath)

	inviter, invitee, mode, values, parseErr := parsePartyForceInviteCommand(text)
	if parseErr != nil {
		s.logPacketEvent("game-force-invite-rejected",
			"reason", partyForceInviteReasonBadCommand,
			"command", text,
			"detail", parseErr.Error())
		s.writePartyForceResult(resultPath, "rejected reason="+partyForceInviteReasonBadCommand)
		return
	}
	ok, reason, bodyLen := s.forceInviteParty(inviter, invitee, mode, values)
	if !ok {
		s.writePartyForceResult(resultPath, fmt.Sprintf(
			"rejected reason=%s inviter=%d invitee=%d mode=%d", reason, inviter, invitee, mode))
		return
	}
	s.writePartyForceResult(resultPath, fmt.Sprintf(
		"ok inviter=%d invitee=%d mode=%d body_len=%d", inviter, invitee, mode, bodyLen))
}

// parsePartyForceInviteCommand 解析
// `<inviter> <invitee> [mode] [value0] [value1] [value2]`；缺省 mode=0、三个值=0。
func parsePartyForceInviteCommand(text string) (uint16, uint16, byte, [3]uint32, error) {
	fields := strings.Fields(text)
	if len(fields) < 2 {
		return 0, 0, 0, [3]uint32{}, fmt.Errorf("want `<inviter_id> <invitee_id> [mode] [v0] [v1] [v2]`, got %d field(s)", len(fields))
	}
	inviter, err := strconv.ParseUint(fields[0], 10, 16)
	if err != nil {
		return 0, 0, 0, [3]uint32{}, fmt.Errorf("inviter id %q is not a decimal character id", fields[0])
	}
	invitee, err := strconv.ParseUint(fields[1], 10, 16)
	if err != nil {
		return 0, 0, 0, [3]uint32{}, fmt.Errorf("invitee id %q is not a decimal character id", fields[1])
	}
	mode := byte(0)
	if len(fields) >= 3 {
		parsedMode, modeErr := strconv.ParseUint(fields[2], 10, 8)
		if modeErr != nil {
			return 0, 0, 0, [3]uint32{}, fmt.Errorf("mode %q is not a decimal byte", fields[2])
		}
		mode = byte(parsedMode)
	}
	var values [3]uint32
	for index := 0; index < 3 && len(fields) >= 4+index; index++ {
		parsedValue, valueErr := strconv.ParseUint(fields[3+index], 10, 32)
		if valueErr != nil {
			return 0, 0, 0, [3]uint32{}, fmt.Errorf("value%d %q is not a decimal uint32", index, fields[3+index])
		}
		values[index] = uint32(parsedValue)
	}
	return uint16(inviter), uint16(invitee), mode, values, nil
}

// forceInviteParty 按真实邀请的服务端出口，把邀请提示发给被邀请方。
// 返回值：(是否成功, 失败原因, 提示体长度)。
func (s *Service) forceInviteParty(inviterID, inviteeID uint16, mode byte, values [3]uint32) (bool, string, int) {
	if inviterID == 0 || inviteeID == 0 {
		s.logPacketEvent("game-force-invite-rejected",
			"reason", partyForceInviteReasonZeroID,
			"inviter_char_id", inviterID,
			"invitee_char_id", inviteeID)
		return false, partyForceInviteReasonZeroID, 0
	}
	if inviterID == inviteeID {
		s.logPacketEvent("game-force-invite-rejected",
			"reason", partyForceInviteReasonSameCharacter,
			"inviter_char_id", inviterID,
			"invitee_char_id", inviteeID)
		return false, partyForceInviteReasonSameCharacter, 0
	}
	inviter, online := s.onlineGameSession(inviterID)
	if !online {
		s.logPacketEvent("game-force-invite-rejected",
			"reason", partyForceInviteReasonInviterNotOnline,
			"inviter_char_id", inviterID,
			"invitee_char_id", inviteeID)
		return false, partyForceInviteReasonInviterNotOnline, 0
	}
	inviterIdentity, inviterCurrent := s.boundGameSessionCharacterSnapshot(inviter)
	if !inviterCurrent {
		s.logGameEvent(inviter, "game-force-invite-rejected",
			"reason", partyForceInviteReasonInviterNotAuth,
			"inviter_char_id", inviterID,
			"invitee_char_id", inviteeID,
			"character_generation", inviter.characterGeneration)
		return false, partyForceInviteReasonInviterNotAuth, 0
	}
	invitee, online := s.onlineGameSession(inviteeID)
	if !online {
		s.logGameEvent(inviter, "game-force-invite-rejected",
			"reason", partyForceInviteReasonInviteeNotOnline,
			"inviter_char_id", inviterID,
			"invitee_char_id", inviteeID)
		return false, partyForceInviteReasonInviteeNotOnline, 0
	}
	inviteeIdentity, inviteeCurrent := s.boundGameSessionCharacterSnapshot(invitee)
	if (mode == 0 || mode == 12) && (s.currentFiendWarAutomaticPartyOnly(inviter) || s.currentFiendWarAutomaticPartyOnly(invitee)) {
		return false, "fiendwar_automatic_encounter_party_only", 0
	}
	if !inviteeCurrent {
		s.logGameEvent(invitee, "game-force-invite-rejected",
			"reason", partyForceInviteReasonInviteeNotAuth,
			"inviter_char_id", inviterID,
			"invitee_char_id", inviteeID,
			"character_generation", invitee.characterGeneration)
		return false, partyForceInviteReasonInviteeNotAuth, 0
	}

	// ③ 同屏前置（与真实路径同一道闸门；见文件头）。判据只认"两端都有存在记录却不同桶"。
	if s.forceInviteCoPresence() == partyForceInviteCoPresenceRequireSameArea &&
		s.partyForceInviteAreaMismatch(inviterID, inviteeID) {
		inviterArea, _ := s.townPresenceAreaText(inviterID)
		inviteeArea, _ := s.townPresenceAreaText(inviteeID)
		s.logGameEvent(inviter, "game-force-invite-rejected",
			"reason", partyForceInviteReasonNotCoPresent,
			"inviter_char_id", inviterID,
			"invitee_char_id", inviteeID,
			"mode", mode,
			"inviter_area", inviterArea,
			"invitee_area", inviteeArea,
			"reason_detail", "invitee_is_not_in_the_inviters_town_presence_bucket_so_its_client_has_no_inviter_actor_and_cannot_render_the_native_prompt",
			"rollback", partyForceInviteCoPresenceEnv+"=off")
		return false, partyForceInviteReasonNotCoPresent, 0
	}

	// ① 登记邀请（与真实路径同一个原语；不登记则被邀请方的 op11 不被承认）。
	invitePartyID := uint16(0)
	recorded := false
	if manager := s.runtimePartyManagerForService(); manager != nil {
		if snapshot, found := manager.SnapshotByUser(inviterIdentity.character, inviterIdentity.generation); found {
			invitePartyID = snapshot.ID
		}
		recorded = manager.RecordInvite(
			inviteeIdentity.character,
			inviteeIdentity.generation,
			inviterIdentity.character,
			inviterIdentity.generation,
			invitePartyID,
			mode,
		)
	}
	if !recorded {
		s.logGameEvent(invitee, "game-force-invite-rejected",
			"reason", partyForceInviteReasonNotRecorded,
			"inviter_char_id", inviterID,
			"invitee_char_id", inviteeID,
			"party_id", invitePartyID)
		return false, partyForceInviteReasonNotRecorded, 0
	}

	// ② 发提示（与真实路径同一个构造函数与 opcode）。
	request := party.RequestPeerRequest{
		TargetID: inviteeIdentity.character,
		Mode:     mode,
		Value0:   values[0],
		Value1:   uint16(values[1]),
		Value2:   values[2],
	}
	noticeBody := party.BuildRequestPeerNotice(inviterIdentity.character, request)
	s.logGameEvent(invitee, "game-force-invite-requested",
		"inviter_char_id", inviterID,
		"invitee_char_id", inviteeID,
		"mode", mode,
		"party_id", invitePartyID,
		"body_len", len(noticeBody),
		"source", "party_force_invite_command_file")
	if err := s.sendGameUpperRawClassCodec(invitee, 0x0007, noticeBody, 0, true); err != nil {
		reason := partyForceInviteReasonSendFailedPrefix + err.Error()
		s.logGameEvent(invitee, "game-force-invite-rejected",
			"reason", reason,
			"inviter_char_id", inviterID,
			"invitee_char_id", inviteeID)
		return false, reason, 0
	}
	s.logGameEvent(invitee, "game-force-invite-sent",
		"inviter_char_id", inviterID,
		"invitee_char_id", inviteeID,
		"mode", mode,
		"party_id", invitePartyID,
		"body_len", len(noticeBody),
		"meaning", "the_invitee_client_should_now_show_the_native_party_invite_dialog")
	return true, "", len(noticeBody)
}
