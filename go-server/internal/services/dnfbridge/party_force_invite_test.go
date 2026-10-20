package dnfbridge

import (
	"bytes"
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"longheng.io/server/internal/modules/dnf/party"
)

// 本文件钉住"服务端代发真实组队邀请"（party_force_invite.go）：
//   - 邀请**只发给被邀请方**（class0/op7，与真实邀请同一个构造函数、同一份字节）；
//   - 发之前必须 `RecordInvite` 登记，否则被邀请方的 op11 不被承认为"接受"；
//   - 任一端不在线/不是权威会话 → 具名拒绝，且**一个字节都不发**；
//   - 开关 off（默认）→ 不启动循环、不消费命令文件、不写结果文件、不发包。
//
// 脚手架复用 party_force_trigger_test.go 的 partyForceHarness。

// TestPartyForceInviteSendsOp7NoticeToInviteeOnly 是本文件的决定性用例：
// 邀请提示必须**逐字节**等于真实路径的 `party.BuildRequestPeerNotice(邀请方, 请求)`，
// 且**只出现在被邀请方的连接上**（邀请方一个字节都不该收到）。
func TestPartyForceInviteSendsOp7NoticeToInviteeOnly(t *testing.T) {
	h := newPartyForceHarness(t)

	ok, reason, bodyLen := h.service.forceInviteParty(1001, 1002, 0, [3]uint32{})
	if !ok {
		t.Fatalf("force invite rejected: reason=%s", reason)
	}

	// 默认（mode=0）的真实样本是 17 字节体：u16 邀请方 | u8 mode | u32 v0 | u16 v1 | u16 0 | u16 0 | u32 v2。
	want := party.BuildRequestPeerNotice(1001, party.RequestPeerRequest{TargetID: 1002, Mode: 0})
	if bodyLen != len(want) {
		t.Fatalf("notice body_len=%d, want %d", bodyLen, len(want))
	}
	if len(want) != 17 {
		t.Fatalf("mode-0 notice must be the 17-byte vendor shape, got %d", len(want))
	}

	inviteeStream := h.memberConn.write.Bytes()
	if !bytes.Contains(inviteeStream, want) {
		t.Fatalf("invitee stream does not carry the verbatim op7 notice body %x", want)
	}
	if ids := upperMsgIDs(t, inviteeStream); !containsMsgID(ids, 0x0007) {
		t.Fatalf("invitee packet stream %v is missing op7", ids)
	}
	if h.leaderConn.write.Len() != 0 {
		t.Fatalf("the inviter must receive nothing from an outbound invite, got %d bytes", h.leaderConn.write.Len())
	}

	// 登记必须真的落进 manager（否则被邀请方点"接受"发来的 op11 会被判为无邀请）。
	memberIdentity, ok := h.service.boundGameSessionCharacterSnapshot(h.member)
	if !ok {
		t.Fatal("invitee session is not authoritative")
	}
	leaderIdentity, ok := h.service.boundGameSessionCharacterSnapshot(h.leader)
	if !ok {
		t.Fatal("inviter session is not authoritative")
	}
	manager := h.service.runtimePartyManagerForService()
	if manager == nil {
		t.Fatal("runtime party manager unavailable")
	}
	if _, found := manager.ConsumeInvite(
		memberIdentity.character, memberIdentity.generation,
		leaderIdentity.character, leaderIdentity.generation, 0,
	); !found {
		t.Fatal("the invite was sent but never recorded in the runtime party manager")
	}
}

// TestPartyForceInviteRejectionsSendNothing 覆盖四条拒绝路径：都必须具名拒绝且零发包。
func TestPartyForceInviteRejectionsSendNothing(t *testing.T) {
	cases := []struct {
		name     string
		inviter  uint16
		invitee  uint16
		reason   string
		wantZero bool
	}{
		{name: "zero-id", inviter: 0, invitee: 1002, reason: partyForceInviteReasonZeroID, wantZero: true},
		{name: "same-character", inviter: 1001, invitee: 1001, reason: partyForceInviteReasonSameCharacter, wantZero: true},
		{name: "inviter-offline", inviter: 9999, invitee: 1002, reason: partyForceInviteReasonInviterNotOnline, wantZero: true},
		{name: "invitee-offline", inviter: 1001, invitee: 9999, reason: partyForceInviteReasonInviteeNotOnline, wantZero: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := newPartyForceHarness(t)
			ok, reason, bodyLen := h.service.forceInviteParty(tc.inviter, tc.invitee, 0, [3]uint32{})
			if ok {
				t.Fatalf("force invite must be rejected for %s", tc.name)
			}
			if reason != tc.reason {
				t.Fatalf("reason = %q, want %q", reason, tc.reason)
			}
			if bodyLen != 0 {
				t.Fatalf("rejected invite must report body_len=0, got %d", bodyLen)
			}
			if tc.wantZero {
				if h.memberConn.write.Len() != 0 || h.leaderConn.write.Len() != 0 {
					t.Fatalf("rejected invite must not send a byte: leader=%d member=%d",
						h.leaderConn.write.Len(), h.memberConn.write.Len())
				}
			}
		})
	}
}

// TestPartyForceInviteCommandParsing 钉住命令文件语法：缺省 mode=0、三个值=0，非法输入报错。
func TestPartyForceInviteCommandParsing(t *testing.T) {
	inviter, invitee, mode, values, err := parsePartyForceInviteCommand("1001 1002\n")
	if err != nil {
		t.Fatalf("plain command must parse: %v", err)
	}
	if inviter != 1001 || invitee != 1002 || mode != 0 || values != [3]uint32{} {
		t.Fatalf("plain command defaults wrong: %d %d %d %v", inviter, invitee, mode, values)
	}
	if _, _, _, _, err := parsePartyForceInviteCommand("1001"); err == nil {
		t.Fatal("a single field must be rejected")
	}
	if _, _, _, _, err := parsePartyForceInviteCommand("x 1002"); err == nil {
		t.Fatal("a non-numeric inviter id must be rejected")
	}

	// 真实样本里三个值随邀请方不同而不同（30/364 与 401503/2292），因此必须可由命令文件指定。
	inviter, invitee, mode, values, err = parsePartyForceInviteCommand("3 4 0 30 364 0")
	if err != nil {
		t.Fatalf("sample-shaped command must parse: %v", err)
	}
	if inviter != 3 || invitee != 4 || mode != 0 || values != [3]uint32{30, 364, 0} {
		t.Fatalf("sample-shaped command parsed wrong: %d %d %d %v", inviter, invitee, mode, values)
	}
}

// TestPartyForceInviteTriggerLoopConsumesCommandFileAndWritesResult 覆盖真正上线的那条路：
// 开关 on 时轮询循环自己发现命令文件 → 发提示 → 写结果行 → 删命令文件。
func TestPartyForceInviteTriggerLoopConsumesCommandFileAndWritesResult(t *testing.T) {
	h := newPartyForceHarness(t)
	h.service.options.partyForceInvite = partyForceOn
	h.service.options.partyForceInviteCommandPath = h.commandPath
	h.service.options.partyForceInviteResultPath = h.resultPath
	h.service.options.partyForceInviteInterval = 50 * time.Millisecond

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	h.service.startPartyForceInviteTrigger(ctx)
	h.writeCommand(t, "1001 1002\n")

	deadline := time.Now().Add(5 * time.Second)
	var result string
	for time.Now().Before(deadline) {
		raw, err := os.ReadFile(h.resultPath)
		if err == nil && strings.TrimSpace(string(raw)) != "" {
			result = strings.TrimSpace(string(raw))
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if result == "" {
		t.Fatal("result file was never written")
	}
	if !strings.HasPrefix(result, "ok inviter=1001 invitee=1002") {
		t.Fatalf("result line = %q, want `ok inviter=1001 invitee=1002 …`", result)
	}
	if !strings.Contains(result, "body_len=17") {
		t.Fatalf("result line = %q, want the 17-byte mode-0 body length", result)
	}
	if _, err := os.Stat(h.commandPath); !os.IsNotExist(err) {
		t.Fatalf("command file must be consumed, stat err = %v", err)
	}
	// 重复写同一内容必须被记忆跳过（只发一次）。
	h.writeCommand(t, "1001 1002\n")
	time.Sleep(200 * time.Millisecond)
	ids := upperMsgIDs(t, h.memberConn.write.Bytes())
	count := 0
	for _, id := range ids {
		if id == 0x0007 {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("a repeated identical command must be skipped, op7 count=%d", count)
	}
}

// TestPartyForceInviteDisabledDoesNothing 钉住默认档（零值 = off）零副作用：
// 不启动循环、不消费命令文件、不写结果文件、不发一个字节。
func TestPartyForceInviteDisabledDoesNothing(t *testing.T) {
	h := newPartyForceHarness(t)
	// 脚手架没有设置 partyForceInvite（零值 = off）。
	h.writeCommand(t, "1001 1002\n")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	h.service.startPartyForceInviteTrigger(ctx)
	time.Sleep(300 * time.Millisecond)

	if _, err := os.Stat(h.commandPath); err != nil {
		t.Fatalf("off mode must not consume the command file, stat err = %v", err)
	}
	if _, err := os.Stat(h.resultPath); !os.IsNotExist(err) {
		t.Fatalf("off mode must not write the result file, stat err = %v", err)
	}
	if h.memberConn.write.Len() != 0 || h.leaderConn.write.Len() != 0 {
		t.Fatalf("off mode must not send a byte: leader=%d member=%d",
			h.leaderConn.write.Len(), h.memberConn.write.Len())
	}
	// 零值 Service 的档位读取也必须是 off。
	if got := (&Service{}).forceInvite(); got != partyForceOff {
		t.Fatalf("zero-value forceInvite() = %v, want off", got)
	}
}

// registerForceInvitePresence 把两个角色登记进城镇在线表（同城同区同频道），
// 并让被邀请方落在指定 instance 桶：`instance != 0` 就是"每个角色一份"的私有落点房
// （登录落点的赛丽亚房间），此时两端**不算同一个桶**（town_copresence 的口径）。
func registerForceInvitePresence(h *partyForceHarness, inviteeInstance uint32) {
	h.service.onlinePlayers = newOnlinePlayerManager()
	h.service.onlinePlayers.EnterArea(&onlinePlayerInfo{
		CharacterID: 1001, TownID: 38, AreaID: 0, Instance: 0, Session: h.leader,
	})
	h.service.onlinePlayers.EnterArea(&onlinePlayerInfo{
		CharacterID: 1002, TownID: 38, AreaID: 0, Instance: inviteeInstance, Session: h.member,
	})
}

// TestPartyForceInviteRefusesWhenTheInviteeCannotSeeTheInviter 是本文件关于"邀请没反应"
// 的决定性用例：两端都有存在记录、但被邀请方还在自己的私有落点房里（分桶键不同）时，
// 客户端的场景里没有邀请者的 actor，原生 op7 处理器拿不到目标 ⇒ 框画不出来。
// 代发路径必须**具名拒绝且一个字节都不发**，绝不写 `ok` 骗现场。
func TestPartyForceInviteRefusesWhenTheInviteeCannotSeeTheInviter(t *testing.T) {
	h := newPartyForceHarness(t)
	logger, _ := newPeerEndpointTestLogger(t)
	h.service.packetLog = logger
	t.Cleanup(func() { _ = logger.Close() })
	registerForceInvitePresence(h, 4242)

	ok, reason, bodyLen := h.service.forceInviteParty(1001, 1002, 0, [3]uint32{})
	if ok {
		t.Fatal("an invite whose target cannot see the inviter must not report ok")
	}
	if reason != partyForceInviteReasonNotCoPresent {
		t.Fatalf("reason = %q, want %q", reason, partyForceInviteReasonNotCoPresent)
	}
	if bodyLen != 0 {
		t.Fatalf("refused invite must report body_len=0, got %d", bodyLen)
	}
	if h.memberConn.write.Len() != 0 || h.leaderConn.write.Len() != 0 {
		t.Fatalf("refused invite must not send a byte: leader=%d member=%d",
			h.leaderConn.write.Len(), h.memberConn.write.Len())
	}
	// 被拒时**不登记**邀请：否则被邀请方事后发来的 op11 会被当成"接受了一个没弹过框的邀请"。
	inviteeIdentity, bound := h.service.boundGameSessionCharacterSnapshot(h.member)
	if !bound {
		t.Fatal("invitee session is not authoritative")
	}
	inviterIdentity, bound := h.service.boundGameSessionCharacterSnapshot(h.leader)
	if !bound {
		t.Fatal("inviter session is not authoritative")
	}
	if _, found := h.service.runtimePartyManagerForService().ConsumeInvite(
		inviteeIdentity.character, inviteeIdentity.generation,
		inviterIdentity.character, inviterIdentity.generation, 0,
	); found {
		t.Fatal("a refused invite must not be registered in the runtime party manager")
	}
	// 事件必须自带两边的分桶键，一次复测就能判读是哪一种不同桶。
	log := testPacketLogText(t, h.service)
	for _, want := range []string{
		"game-force-invite-rejected",
		partyForceInviteReasonNotCoPresent,
		"inviter_area=38/0/ch",
		"invitee_area=38/0/ch",
		"inst4242",
	} {
		if !logLineContaining(log, "game-force-invite-rejected", want) {
			t.Fatalf("rejection event is missing %q:\n%s", want, log)
		}
	}
}

// TestPartyForceInviteSendsWhenBothShareTheInviterTownBucket 是上面那条的对照：
// 两端在同一个桶（同城同区同频道、instance 0）时，闸门必须放行，邀请提示逐字节照发。
func TestPartyForceInviteSendsWhenBothShareTheInviterTownBucket(t *testing.T) {
	h := newPartyForceHarness(t)
	registerForceInvitePresence(h, 0)

	ok, reason, bodyLen := h.service.forceInviteParty(1001, 1002, 0, [3]uint32{})
	if !ok {
		t.Fatalf("a co-present invite must be accepted: reason=%s", reason)
	}
	if bodyLen != 17 {
		t.Fatalf("notice body_len=%d, want the 17-byte mode-0 shape", bodyLen)
	}
	want := party.BuildRequestPeerNotice(1001, party.RequestPeerRequest{TargetID: 1002, Mode: 0})
	if !bytes.Contains(h.memberConn.write.Bytes(), want) {
		t.Fatalf("invitee stream does not carry the verbatim op7 notice body %x", want)
	}
	if ids := upperMsgIDs(t, h.memberConn.write.Bytes()); !containsMsgID(ids, 0x0007) {
		t.Fatalf("invitee packet stream %v is missing op7", ids)
	}
	if h.leaderConn.write.Len() != 0 {
		t.Fatalf("the inviter must receive nothing from an outbound invite, got %d bytes", h.leaderConn.write.Len())
	}
}

// TestPartyForceInviteCoPresenceRollbackRestoresLegacySend 钉住一键回退档
// （`DNFBRIDGE_PARTY_FORCE_INVITE_CO_PRESENCE=off`）：不做同屏判据、照发，逐字节回到改动前。
func TestPartyForceInviteCoPresenceRollbackRestoresLegacySend(t *testing.T) {
	h := newPartyForceHarness(t)
	registerForceInvitePresence(h, 4242)
	h.service.options.partyForceInviteCoPresence = partyForceInviteCoPresenceOff

	ok, reason, bodyLen := h.service.forceInviteParty(1001, 1002, 0, [3]uint32{})
	if !ok {
		t.Fatalf("the rollback mode must send anyway: reason=%s", reason)
	}
	if bodyLen != 17 {
		t.Fatalf("rollback notice body_len=%d, want 17", bodyLen)
	}
	if ids := upperMsgIDs(t, h.memberConn.write.Bytes()); !containsMsgID(ids, 0x0007) {
		t.Fatalf("rollback invitee packet stream %v is missing op7", ids)
	}
}

// TestOnlineGameSessionAndTownPresenceCanDivergeWhileBothSocketsStayLive 钉住本次排查的
// 目的地判据（这是**证据**，不是改动）：同一个角色握着两条活传输时，
//
//   - `onlineGameSession(charID)`（= 角色目录 `s.gameSessions[charID]`，game_sessions.go:373）
//     返回目录 owner 那条；目录在"旧会话仍是城镇存在 owner"时**刻意不换手**
//     （game_sessions.go:73-79 的早退分支，另见 online_player_manager_test.go 的既有用例）；
//   - `onlinePlayers.SessionForCharacter(charID)`（存在表 owner，game_sessions.go:355 的
//     交接优先对象 = "客户端真正在用的那条"）可以指向**另一条**。
//
// 两条都活、都在用时，按角色 ID 推送的包（邀请提示 op7、op9/op153 名册）只会走目录那条。
// 因此"代发邀请的提示发到了哪条 socket"这一问必须由现场日志回答，而不是由访问器名字回答：
// 本文件的闸门（`invitee_outside_inviter_area`）与 party_online.go 的
// `PeerInSameArea` 才是两个入口**共同**的前置条件。
func TestOnlineGameSessionAndTownPresenceCanDivergeWhileBothSocketsStayLive(t *testing.T) {
	service := &Service{
		onlinePlayers: newOnlinePlayerManager(),
		gameSessions:  make(map[uint16]*gameSession),
	}
	first := &gameSession{conn: &bufferConn{}, connID: "game-000001"}
	second := &gameSession{conn: &bufferConn{}, connID: "game-000002"}
	// 两条都是**活传输**（现场：game.go:294 在连接建立时登记）。
	service.registerLiveGameSession(first)
	service.registerLiveGameSession(second)
	service.bindGameSessionCharacter(first, 7)
	// 第一条会话先登记城镇存在（登录/进城那条路由自己发布的）。
	service.onlinePlayers.EnterArea(&onlinePlayerInfo{CharacterID: 7, TownID: 38, AreaID: 0, Session: first})
	// 第二条活传输在第一条仍是存在 owner 时绑定：目录不接管。
	service.bindGameSessionCharacter(second, 7)
	if got, ok := service.onlineGameSession(7); !ok || got != first {
		t.Fatalf("character directory = %p, want the first session %p", got, first)
	}
	// 第二条随后跑完自己的进城路由并发布存在（现场事件
	// `game-town-presence-published source=channel_reconnect_town_actor_route_complete`）。
	service.onlinePlayers.EnterArea(&onlinePlayerInfo{CharacterID: 7, TownID: 38, AreaID: 0, Session: second})
	if got := service.onlinePlayers.SessionForCharacter(7); got != second {
		t.Fatalf("town presence owner = %p, want the second session %p", got, second)
	}
	if got, ok := service.onlineGameSession(7); !ok || got != first {
		t.Fatalf("character directory = %p, want the first session %p (divergence disappeared)", got, first)
	}
	// 两条都活着：所以"发到目录那条"并不等于"发到了客户端正在用的那条"。
	if got := service.liveSameCharacterConnIDs(7, nil); got != "game-000001,game-000002" {
		t.Fatalf("live same-character conns = %q, want both sessions", got)
	}
}

// TestPartyForceInviteCoPresenceModeReading 钉住开关归一化：只有明确的关闭词才是回退档。
func TestPartyForceInviteCoPresenceModeReading(t *testing.T) {
	for _, value := range []string{"", "require_same_area", "on", "1", "true"} {
		if got := readPartyForceInviteCoPresenceMode(value); got != partyForceInviteCoPresenceRequireSameArea {
			t.Fatalf("value %q reads as %v, want require_same_area", value, got)
		}
	}
	for _, value := range []string{"off", "OFF", "0", "false", "no", "legacy"} {
		if got := readPartyForceInviteCoPresenceMode(value); got != partyForceInviteCoPresenceOff {
			t.Fatalf("value %q reads as %v, want off", value, got)
		}
	}
	// 零值 Service 的档位读取必须是默认档（require_same_area），但零值 Service 没有
	// onlinePlayers ⇒ 判据不足 ⇒ 永远不拦（对探针/测试字面量逐字节无影响）。
	if got := (&Service{}).forceInviteCoPresence(); got != partyForceInviteCoPresenceRequireSameArea {
		t.Fatalf("zero-value forceInviteCoPresence() = %v, want require_same_area", got)
	}
	if (&Service{}).partyForceInviteAreaMismatch(1001, 1002) {
		t.Fatal("a zero-value Service has no presence records, so the gate must not fire")
	}
}
