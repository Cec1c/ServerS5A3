package dnfbridge

import (
	"bytes"
	"log/slog"
	"strings"
	"testing"
	"time"

	"longheng.io/server/internal/modules/dnf/channelcatalog"
	dnfproto "longheng.io/server/internal/modules/dnf/protocol"
)

// 本文件钉死 2026-09-25「不同账号登录看到同一个角色」事故的账号判定口径：
//
//	① 启动器登录令牌 → ② 本地客户端进程号登记（令牌可覆盖）→ ③ 共享兜底账号 + WARN
//
// 按来源 IP 判定账号的整条通道已删除（见 client_account_ip.go / repository_access.go）。
// 现场证据（香港 /opt/dnf90/logs/packet_log.txt，服务器进程 2026-09-25 05:58 起）：
// 9 条落到 dnf:1 的会话全部满足 session_account_bound=false、
// request_body_len=0、reason=unbound_channel_discovery_probe，且
// RECV ENUM_CMDPACKET_LOGIN 计数为 0（含把 smcz/czfm 建到 dnf:1 的
// game-000197 / game-000203）；它们的来源 IP 是加速器出口 175.43.105.6，
// 而该玩家四个账号的 last_login_ip 都是 203.198.28.63。

type accountResolutionTestService struct {
	service *Service
	logs    *bytes.Buffer
}

func newAccountResolutionTestService(t *testing.T) accountResolutionTestService {
	t.Helper()
	logs := &bytes.Buffer{}
	service := &Service{
		logger:         slog.New(slog.NewTextHandler(logs, &slog.HandlerOptions{Level: slog.LevelDebug})),
		clientAccounts: newClientAccountRegistry(),
	}
	// 香港现网口径：共享兜底账号 = dnf:1。
	service.options.accountID = "dnf:1"
	return accountResolutionTestService{service: service, logs: logs}
}

func (f accountResolutionTestService) warnings() string {
	var out []string
	for _, line := range strings.Split(f.logs.String(), "\n") {
		if strings.Contains(line, "level=WARN") {
			out = append(out, line)
		}
	}
	return strings.Join(out, "\n")
}

// 用例 1：有令牌时账号来自令牌，且绝不走任何"按来源 IP"的猜测。
// 会话带着一个与任何启动器记录都不匹配的 clientIP，账号仍必须等于令牌账号。
func TestAccountResolutionPrefersLoginTokenAndNeverUsesClientIP(t *testing.T) {
	f := newAccountResolutionTestService(t)
	token, err := newLoginToken()
	if err != nil {
		t.Fatal(err)
	}
	f.service.loginTokens.remember(token, "dnf:10013", time.Now().Add(time.Hour))

	session := &gameSession{
		connID:   "game-000197",
		clientIP: "203.198.28.63",
		channel:  channelcatalog.Channel{ID: 253, Type: 40},
	}
	if !f.service.bindGameSessionAccountFromLoginToken(
		session, dnfproto.DefaultChannelClassification, buildClientLoginBody(token)) {
		t.Fatal("login token was not recognised")
	}
	if got := f.service.accountIDForSession(session); got != "dnf:10013" {
		t.Fatalf("account=%q, want the token account dnf:10013", got)
	}
	if session.sharedAccountFallbackReason != "" {
		t.Fatalf("shared fallback reason=%q, want empty", session.sharedAccountFallbackReason)
	}
	if strings.Contains(f.warnings(), "fell back to shared account") {
		t.Fatalf("token-bound session warned about the shared account:\n%s", f.warnings())
	}
	// 令牌账号与来源 IP 没有任何关系：换一个 IP 结果必须一样。
	session.clientIP = "175.43.105.6"
	if got := f.service.accountIDForSession(session); got != "dnf:10013" {
		t.Fatalf("account after client IP change=%q, want dnf:10013", got)
	}
}

// 用例 2：令牌缺失但本地客户端进程号有登记 → 用进程号登记（不是 IP 绑定）。
func TestAccountResolutionFallsBackToClientPIDBindingNotClientIP(t *testing.T) {
	f := newAccountResolutionTestService(t)
	if err := f.service.RegisterClientAccount(4242, "dnf:10015"); err != nil {
		t.Fatal(err)
	}
	session := &gameSession{
		connID:         "game-000203",
		clientPID:      4242,
		clientIP:       "203.198.28.63",
		accountID:      "dnf:10015",
		accountFromPID: true,
	}
	if got := f.service.accountIDForSession(session); got != "dnf:10015" {
		t.Fatalf("account=%q, want the PID-registered dnf:10015", got)
	}
	// 同一条会话如果再拿到令牌，令牌仍然压倒进程号登记。
	token, err := newLoginToken()
	if err != nil {
		t.Fatal(err)
	}
	f.service.loginTokens.remember(token, "dnf:10017", time.Now().Add(time.Hour))
	if !f.service.bindGameSessionAccountFromLoginToken(
		session, dnfproto.DefaultChannelClassification, buildClientLoginBody(token)) {
		t.Fatal("login token was not recognised")
	}
	if got := f.service.accountIDForSession(session); got != "dnf:10017" {
		t.Fatalf("account=%q, want the token account dnf:10017", got)
	}
}

// 用例 3：两者都缺 → 落到共享兜底账号，并且必须留下可判读的 WARN。
// 这里复现现场形态：客户端**从未**发过 class1/op1 登录包（loginPacketSeen=false），
// 因此角色表构建那一刻就要告警，而不是等一个永远不会来的登录包。
func TestAccountResolutionWithoutTokenWarnsAndUsesSharedAccount(t *testing.T) {
	f := newAccountResolutionTestService(t)
	session := &gameSession{
		connID:   "game-000197",
		clientIP: "175.43.105.6",
		channel:  channelcatalog.Channel{ID: 253, Type: 40},
	}
	if got := f.service.accountIDForSession(session); got != "dnf:1" {
		t.Fatalf("account=%q, want the shared fallback dnf:1", got)
	}
	// 登录包还没来：这时不告警（避免给所有会话误报），但角色表构建会兜住。
	if strings.Contains(f.warnings(), "fell back to shared account") {
		t.Fatalf("warned before the account decision was final:\n%s", f.warnings())
	}
	f.service.warnSharedAccountFallback(session)
	warn := f.warnings()
	for _, want := range []string{
		"game account fell back to shared account",
		"account_id=dnf:1",
		"token_found=false",
		"login_packet_seen=false",
		"client_ip=175.43.105.6",
		"reason=" + sharedAccountReasonNoLoginPacket,
	} {
		if !strings.Contains(warn, want) {
			t.Fatalf("WARN line missing %q:\n%s", want, warn)
		}
	}
	// 每条会话只写一次，避免刷日志。
	f.service.warnSharedAccountFallback(session)
	f.service.warnSharedAccountFallback(session)
	if got := strings.Count(f.warnings(), "fell back to shared account"); got != 1 {
		t.Fatalf("shared-account WARN count=%d, want exactly 1", got)
	}
}

// 用例 4：登录包来过了、但包里没有令牌 → 原因必须是 no_login_token（判读用）。
func TestAccountResolutionLoginPacketWithoutTokenReportsNoTokenReason(t *testing.T) {
	f := newAccountResolutionTestService(t)
	session := &gameSession{
		connID:          "game-000300",
		clientIP:        "175.43.105.6",
		channel:         channelcatalog.Channel{ID: 251, Type: 40},
		loginPacketSeen: true,
	}
	if got := f.service.accountIDForSession(session); got != "dnf:1" {
		t.Fatalf("account=%q, want dnf:1", got)
	}
	warn := f.warnings()
	if !strings.Contains(warn, "fell back to shared account") ||
		!strings.Contains(warn, "reason="+sharedAccountReasonNoLoginToken) {
		t.Fatalf("unexpected WARN:\n%s", warn)
	}
}

// 用例 5：验收 2026-09-25 现场（未绑定会话 + 频道自洽）时握手必须走
// "先发 CHANNELINFO 通知、等客户端交出登录包"这条链 —— 这是令牌能被读到的唯一前提。
// 旧行为（未绑定直接发主动 class1/op1 成功包）会把令牌永远挡在门外，
// 只能通过回退开关 DNFBRIDGE_GAME_PROACTIVE_ENDPOINT_WHEN_UNBOUND=1 复现。
func TestGameBootstrapWaitsForLoginPacketWhenAccountIsUnknown(t *testing.T) {
	service := residentNoticeTestService(t)
	channel := channelcatalog.Channel{
		ServerID: 1,
		ID:       253,
		Type:     40,
		Group:    "crack",
		Name:     "ch.253",
		Port:     10253,
	}
	conn := &bufferConn{}
	session := &gameSession{
		conn:            conn,
		connID:          "game-000197",
		clientIP:        "175.43.105.6",
		channel:         channel,
		residentChannel: channel,
	}
	if err := service.sendGameConnectionBootstrap(session, time.Unix(1_758_800_000, 0)); err != nil {
		t.Fatalf("bootstrap: %v", err)
	}
	if !session.currentChannelResidentNoticeSent {
		t.Fatal("unbound session did not receive the CHANNELINFO notice, so the client can never send its login token")
	}
	if session.gameEndpointSuccessSent {
		t.Fatal("unbound session was answered with the proactive endpoint success, which suppresses the client login packet")
	}
	packet, trailing := splitGameServerUpperPacketWithHeader(t, conn.write.Bytes(), service.gameUpperHeaderSize())
	if packet.Header.Classification != 0 || packet.Header.MsgID != 1 {
		t.Fatalf("bootstrap packet=%+v, want the class0/op1 CHANNELINFO notice", packet.Header)
	}
	if len(trailing) != 0 {
		t.Fatalf("bootstrap emitted extra wire bytes: %x", trailing)
	}
}

// 用例 6：回退开关必须逐字节恢复旧行为（未绑定 → 主动 class1/op1 端点成功包）。
func TestGameBootstrapRollbackSwitchRestoresProactiveEndpointForUnboundSession(t *testing.T) {
	service := residentNoticeTestService(t)
	service.options.gameProactiveEndpointWhenUnbound = true
	channel := channelcatalog.Channel{
		ServerID: 1,
		ID:       251,
		Type:     40,
		Group:    "crack",
		Name:     "ch.251",
		Port:     10251,
	}
	conn := &bufferConn{}
	session := &gameSession{
		conn:            conn,
		connID:          "game-000198",
		channel:         channel,
		residentChannel: channel,
	}
	if err := service.sendGameConnectionBootstrap(session, time.Unix(1_758_800_000, 0)); err != nil {
		t.Fatalf("bootstrap: %v", err)
	}
	if session.currentChannelResidentNoticeSent {
		t.Fatal("rollback switch still sent the CHANNELINFO notice")
	}
	if !session.gameEndpointSuccessSent {
		t.Fatal("rollback switch did not send the proactive endpoint success")
	}
}

// 用例 7：新进程生命周期（含 OS PID 复用）必须丢掉上一个进程的令牌证明，
// 否则新账号会继承旧账号（"不同账号看到同一批角色"的 PID 版本）。
func TestClientAccountPIDReuseDoesNotInheritTokenProvenAccount(t *testing.T) {
	f := newAccountResolutionTestService(t)
	if err := f.service.RegisterClientAccount(777, "dnf:10013"); err != nil {
		t.Fatal(err)
	}
	// 令牌证明与登记一致：不是"纠正"，不改账号、不告警。
	if f.service.confirmClientAccountPID(777, "dnf:10013") {
		t.Fatal("confirmClientAccountPID reported a change for an identical account")
	}
	if strings.Contains(f.warnings(), "overrode the client PID account binding") {
		t.Fatalf("identical confirmation must not warn:\n%s", f.warnings())
	}
	// 同一个进程生命周期内的重复登记：令牌证明必须保留。
	if err := f.service.RegisterClientAccount(777, "dnf:10013"); err != nil {
		t.Fatal(err)
	}
	if got, ok := f.service.registeredClientAccount(777); !ok || got != "dnf:10013" {
		t.Fatalf("pid binding=%q ok=%t, want dnf:10013", got, ok)
	}
	// 新进程生命周期拿到同一个 PID：不得再返回旧账号。
	if err := f.service.RegisterClientAccount(777, "dnf:10017"); err != nil {
		t.Fatal(err)
	}
	if got, ok := f.service.registeredClientAccount(777); !ok || got != "dnf:10017" {
		t.Fatalf("pid binding after PID reuse=%q ok=%t, want dnf:10017", got, ok)
	}
}

// 用例 8：按客户端进程号的登记被令牌纠正时必须留 WARN（这是"登记串号"的现场判读串）。
func TestStaleClientPIDBindingCorrectionIsWarned(t *testing.T) {
	f := newAccountResolutionTestService(t)
	if err := f.service.RegisterClientAccount(888, "dnf:10013"); err != nil {
		t.Fatal(err)
	}
	token, err := newLoginToken()
	if err != nil {
		t.Fatal(err)
	}
	f.service.loginTokens.remember(token, "dnf:10017", time.Now().Add(time.Hour))
	session := &gameSession{
		connID:         "game-000400",
		clientPID:      888,
		accountID:      "dnf:10013",
		accountFromPID: true,
	}
	if !f.service.bindGameSessionAccountFromLoginToken(
		session, dnfproto.DefaultChannelClassification, buildClientLoginBody(token)) {
		t.Fatal("login token was not recognised")
	}
	if !strings.Contains(f.warnings(), "login token overrode the client PID account binding") {
		t.Fatalf("missing PID-correction WARN:\n%s", f.warnings())
	}
}
