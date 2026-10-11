package dnfbridge

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"longheng.io/server/internal/modules/dnf/channelcatalog"
	"longheng.io/server/internal/modules/dnf/dnfenum"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	dnfrepomemory "longheng.io/server/internal/modules/dnf/repository/memory"
)

// 本文件钉住「查看他人信息时角色 ID / 职业文本位置过于靠下」这一轮的服务端改动：
// 「看别人」这条链（GET_USERINFO mode=3）在 mode3 详情体**之前**多下一条被查看角色
// 自己的 USERINFO subtype0 身份体（uid + 名字 + 职业 + 成长 + 等级），回退档逐字节回到
// 改动前（只有 mode3 那一条）。依据与取舍见 peer_userinfo_identity_carrier.go 文件头。
//
// 夹具刻意用运维截图里那对角色：被查看方 job=14 grow_type=0x21（= 圣职者二觉"炽天使"）、
// 等级 90 —— 现场 2026-09-28T13:40:58.734Z 那条 1342 字节 mode3 体就是发给查看方 6、
// 被查看方 3（test2 / job 14 / grow 33）的。

// peerUserInfoIdentityCarrierFixture 造一个"看别人"夹具：请求方角色 1（viewer）、
// 被查看角色 5（test2），两人同频道同区域。返回服务、请求方会话、请求方连接缓冲。
func peerUserInfoIdentityCarrierFixture(t *testing.T) (*Service, *gameSession, *bufferConn) {
	t.Helper()
	repositories := dnfrepomemory.NewMemoryGroup()
	ctx := context.Background()
	for _, account := range []dnfrepo.AccountRecord{
		{AccountID: "dnf:1", State: "active"},
		{AccountID: "dnf:2", State: "active"},
	} {
		if err := repositories.Account.Save(ctx, account); err != nil {
			t.Fatal(err)
		}
	}
	viewer := dnfrepo.CharacterRecord{CharacterID: "1", AccountID: "dnf:1", Name: "viewer", Job: "0", Level: 90}
	// 被查看方：与现场 test2 同口径（job=14 / grow_type=0x21 / level=90）。
	target := dnfrepo.CharacterRecord{
		CharacterID: "5", AccountID: "dnf:2", Name: "test2", Job: "14", Level: 90,
		Stats: map[string]int64{"grow_type": 33},
	}
	for _, character := range []dnfrepo.CharacterRecord{viewer, target} {
		if err := repositories.Character.Save(ctx, character); err != nil {
			t.Fatal(err)
		}
	}
	sourceConn := &bufferConn{}
	channel := channelcatalog.Channel{ID: 42}
	source := &gameSession{conn: sourceConn, channel: channel, accountID: "dnf:1", selectedCharacterID: 1, townActorOwnerChannel: 42}
	peer := &gameSession{conn: &bufferConn{}, channel: channel, accountID: "dnf:2", selectedCharacterID: 5, townActorOwnerChannel: 42}
	service := &Service{
		options:             options{gameUpperHeader: gameUpperHeaderServer16, gameUpperBodyCodec: gameUpperBodyCodecPlain},
		onlinePlayers:       newOnlinePlayerManager(),
		adventureGroupTable: loadAdventureGroupTestTables(t),
		repositoryProvider:  func() (dnfrepo.Group, bool) { return repositories, true },
	}
	service.bindGameSessionCharacter(source, 1)
	service.bindGameSessionCharacter(peer, 5)
	service.onlinePlayers.EnterArea(&onlinePlayerInfo{CharacterID: 1, TownID: 1, AreaID: 2, Session: source})
	service.onlinePlayers.EnterArea(&onlinePlayerInfo{CharacterID: 5, TownID: 1, AreaID: 2, Session: peer})
	return service, source, sourceConn
}

// requestPeerUserInfo 跑一次"查看他人信息"请求（角色 1 看角色 5），返回包日志文本。
func requestPeerUserInfo(t *testing.T, service *Service, source *gameSession) string {
	t.Helper()
	logPath := filepath.Join(t.TempDir(), "packet.log")
	logger, err := openPacketLogger(logPath)
	if err != nil {
		t.Fatal(err)
	}
	service.packetLog = logger
	if err := service.handleGameCommand(
		source,
		byte(dnfenum.GameCmdCommand),
		uint16(dnfenum.GameTypeGetUserInfo),
		[]byte{5, 0, currentPeerUserInfoMode},
	); err != nil {
		t.Fatal(err)
	}
	if err := logger.Close(); err != nil {
		t.Fatal(err)
	}
	logged, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	return string(logged)
}

// peerUserInfoIdentityLogLine 从 packet log 里取这条身份载体判读事件。
func peerUserInfoIdentityLogLine(t *testing.T, logged string) string {
	t.Helper()
	for _, candidate := range strings.Split(logged, "\n") {
		if strings.Contains(candidate, "kind=game-peer-userinfo-identity-carrier ") {
			return candidate
		}
	}
	t.Fatalf("identity-carrier judgement event missing from packet log\n%s", logged)
	return ""
}

// TestPeerUserInfoIdentityCarrierIsSentBeforeMode3Detail 钉住修复档（默认）的帧序与身份字段：
// 第 1 帧 = 被查看角色自己的 USERINFO subtype0（uid=5 / name=test2 / job=14 / grow=0x21 / level=90），
// 第 2 帧 = 原来的 mode3 详情体。
func TestPeerUserInfoIdentityCarrierIsSentBeforeMode3Detail(t *testing.T) {
	if !peerUserInfoIdentityCarrierEnabled() {
		t.Fatal("default must be the fixed behaviour")
	}
	service, source, conn := peerUserInfoIdentityCarrierFixture(t)
	logged := requestPeerUserInfo(t, service, source)

	identity, rest := splitLongHengGameServerUpperPacket(t, conn.write.Bytes())
	if identity.Header.Classification != 0 || identity.Header.MsgID != uint16(dnfenum.CmdPacketSetUDPIPPort) {
		t.Fatalf("identity carrier header=%+v", identity.Header)
	}
	if identity.Body[0] != 0 {
		t.Fatalf("identity carrier subtype byte = %#x, want 0 (USERINFO subtype0)", identity.Body[0])
	}
	uid, name, job, grow, level, ok := currentPeerUserInfoIdentityForLog(identity.Body)
	if !ok {
		t.Fatalf("identity carrier body is not a subtype0 identity record: %x", identity.Body[:min(len(identity.Body), 24)])
	}
	if uid != 5 || name != "test2" || job != 14 || grow != 0x21 || level != 90 {
		t.Fatalf("identity carrier = uid=%d name=%q job=%d grow=%#x level=%d, want 5/test2/14/0x21/90",
			uid, name, job, grow, level)
	}
	detail, rest := splitLongHengGameServerUpperPacket(t, rest)
	if len(rest) != 0 {
		t.Fatalf("peer userinfo must be exactly identity carrier + mode3 detail, trailing=%d", len(rest))
	}
	if detail.Body[0] != currentPeerUserInfoMode {
		t.Fatalf("second frame is not the mode3 detail body: subtype=%#x", detail.Body[0])
	}

	line := peerUserInfoIdentityLogLine(t, logged)
	// -v 时把这条判读事件原样打出来，便于把"线上该 grep 什么"直接抄进报告。
	t.Log(line)
	for _, want := range []string{
		"env=" + peerUserInfoIdentityCarrierEnv,
		"switch=" + peerUserInfoIdentityCarrierOn,
		"sent=true",
		"uid=5",
		"name=test2",
		"job=14",
		"grow_type=33",
		"level=90",
		"parsed=true",
		"order=subtype0_identity_then_mode3_detail",
	} {
		if !strings.Contains(line, want) {
			t.Fatalf("identity judgement line missing %q\n%s", want, line)
		}
	}
	if !strings.Contains(logged, "identity_carrier_sent=true") {
		t.Fatalf("mode3 response event must report the carrier\n%s", logged)
	}
}

// TestPeerUserInfoIdentityCarrierLegacyKeepsTheDownlinkBytes 钉住回退档的**字节**判据：
// legacy 档只发 mode3 一条，而且那一条的字节与修复档的第二帧**逐字节相同**
// ⇒ 本开关不碰 mode3 详情体的任何一个字节，回退只少一条包。
func TestPeerUserInfoIdentityCarrierLegacyKeepsTheDownlinkBytes(t *testing.T) {
	t.Setenv(peerUserInfoIdentityCarrierEnv, peerUserInfoIdentityCarrierLegacy)
	if peerUserInfoIdentityCarrierEnabled() {
		t.Fatal("legacy must roll the carrier back")
	}
	legacyService, legacySource, legacyConn := peerUserInfoIdentityCarrierFixture(t)
	legacyLogged := requestPeerUserInfo(t, legacyService, legacySource)
	legacyDetail, rest := splitLongHengGameServerUpperPacket(t, legacyConn.write.Bytes())
	if len(rest) != 0 {
		t.Fatalf("legacy档 must send exactly one frame, trailing=%d", len(rest))
	}
	if legacyDetail.Body[0] != currentPeerUserInfoMode {
		t.Fatalf("legacy档 first frame subtype=%#x, want mode3", legacyDetail.Body[0])
	}
	legacyLine := peerUserInfoIdentityLogLine(t, legacyLogged)
	if !strings.Contains(legacyLine, "sent=false") || !strings.Contains(legacyLine, "switch="+peerUserInfoIdentityCarrierLegacy) {
		t.Fatalf("legacy judgement line must report the rollback\n%s", legacyLine)
	}
	if !strings.Contains(legacyLogged, "identity_carrier_sent=false") {
		t.Fatalf("legacy mode3 response event must report no carrier\n%s", legacyLogged)
	}

	t.Setenv(peerUserInfoIdentityCarrierEnv, "")
	fixService, fixSource, fixConn := peerUserInfoIdentityCarrierFixture(t)
	requestPeerUserInfo(t, fixService, fixSource)
	_, fixRest := splitLongHengGameServerUpperPacket(t, fixConn.write.Bytes())
	fixDetail, rest := splitLongHengGameServerUpperPacket(t, fixRest)
	if len(rest) != 0 {
		t.Fatalf("fix档 must send exactly two frames, trailing=%d", len(rest))
	}
	if !bytes.Equal(legacyDetail.Body, fixDetail.Body) {
		t.Fatalf("mode3 detail body changed with the carrier switch:\n legacy=%x\n fix   =%x",
			legacyDetail.Body[:min(len(legacyDetail.Body), 32)], fixDetail.Body[:min(len(fixDetail.Body), 32)])
	}
	if len(fixConn.write.Bytes()) <= len(legacyConn.write.Bytes()) {
		t.Fatal("fix档 must write the extra identity carrier frame")
	}
}

// TestPeerUserInfoIdentityCarrierSwitchRejectsUnknownValue 与同包既有开关同口径：
// 只有显式写回退字面量才回退，写错的字串按修复档走。
func TestPeerUserInfoIdentityCarrierSwitchRejectsUnknownValue(t *testing.T) {
	t.Setenv(peerUserInfoIdentityCarrierEnv, "maybe")
	if !peerUserInfoIdentityCarrierEnabled() {
		t.Fatal("unknown value must keep the fixed behaviour")
	}
	if mode := peerUserInfoIdentityCarrierMode(); mode != peerUserInfoIdentityCarrierOn {
		t.Fatalf("judgement mode = %q, want %q", mode, peerUserInfoIdentityCarrierOn)
	}
	for _, raw := range []string{"legacy", "off", "0", "false", "no", "disable", "disabled", "none"} {
		t.Setenv(peerUserInfoIdentityCarrierEnv, raw)
		if peerUserInfoIdentityCarrierEnabled() {
			t.Fatalf("%q must roll the carrier back", raw)
		}
		if mode := peerUserInfoIdentityCarrierMode(); mode != peerUserInfoIdentityCarrierLegacy {
			t.Fatalf("judgement mode for %q = %q, want %q", raw, mode, peerUserInfoIdentityCarrierLegacy)
		}
	}
	t.Setenv(peerUserInfoIdentityCarrierEnv, "ON")
	if !peerUserInfoIdentityCarrierEnabled() {
		t.Fatal("value parsing must be case-insensitive")
	}
	if peerUserInfoIdentityCarrierAllPeersEnabled() {
		t.Fatal("on must not relax the co-presence clone gate")
	}
	for _, raw := range []string{"all_peers", "all-peers", "all", "peers", "every_peer", "ALL_PEERS"} {
		t.Setenv(peerUserInfoIdentityCarrierEnv, raw)
		if !peerUserInfoIdentityCarrierEnabled() || !peerUserInfoIdentityCarrierAllPeersEnabled() {
			t.Fatalf("%q must enable the all_peers experiment", raw)
		}
		if mode := peerUserInfoIdentityCarrierMode(); mode != peerUserInfoIdentityCarrierAllPeers {
			t.Fatalf("judgement mode for %q = %q, want %q", raw, mode, peerUserInfoIdentityCarrierAllPeers)
		}
	}
}

// TestPeerIdentityAllPeersCarrierRelaxesTheCloneGate 钉住 all_peers 实验档的**唯一**作用点：
// 同屏/队友投影里"没有克隆行的角色"也能拿到他自己的 subtype0；默认档与回退档下这道窄门
// 与改动前完全一致（默认档不打开实验、回退档两条链都关）。
//
// 这也是"客户端缺身份载体"这一族假设的最后一块实验拼图：现场已经证明载体在、位置不动
// （`_dist/peer-info-text-position.md` §9），本档只用来把这件事坐实到每个角色。
func TestPeerIdentityAllPeersCarrierRelaxesTheCloneGate(t *testing.T) {
	t.Setenv(cloneAvatarAppearanceEnv, cloneAvatarAppearanceOn)
	t.Setenv(cloneAvatarRowClearIDEnv, cloneAvatarAppearanceOn)
	t.Setenv(currentPeerAppearanceSubtype0Env, cloneAvatarAppearanceOn)

	service, leader, follower, _ := peerSubtype0Fixture(t)
	record := peerAppearanceMemberRecord()
	record.CharacterID = "100"
	character := peerAppearanceCharacter(record)
	// 窄门的判据是真实 mode0 体里 19 字节外观表的 LinkItemId 行数：这份没有克隆行。
	plainBody := peerAppearanceMode0Body(t, peerAppearanceLeaderRecord())
	if currentSceneObjectAppearanceBodyHasCloneLink(plainBody) {
		t.Fatal("fixture mode0 body must not carry clone links")
	}

	// 默认档（未设置）：窄门仍在 —— 没有克隆行的角色一个字节都不多。
	t.Setenv(peerUserInfoIdentityCarrierEnv, "")
	if peerUserInfoIdentityCarrierAllPeersEnabled() {
		t.Fatal("default must not relax the clone gate")
	}
	if body := service.peerActorUserInfoSubtype0BodyForProjection(
		context.Background(), leader, follower, character, true, 100, character.Name, plainBody,
	); body != nil {
		t.Fatalf("default档 must keep the clone gate, got %d bytes", len(body))
	}

	// all_peers 实验档：没有克隆行也发，而且发的是该角色自己的 subtype0 身份体。
	t.Setenv(peerUserInfoIdentityCarrierEnv, peerUserInfoIdentityCarrierAllPeers)
	body := service.peerActorUserInfoSubtype0BodyForProjection(
		context.Background(), leader, follower, character, true, 100, character.Name, plainBody,
	)
	if len(body) == 0 {
		t.Fatal("all_peers档 must build the peer subtype0 for a peer without clone rows")
	}
	if uid, name, _, _, _, ok := currentPeerUserInfoIdentityForLog(body); !ok || uid != 100 || name != character.Name {
		t.Fatalf("all_peers carrier = uid=%d name=%q ok=%t, want 100/%q", uid, name, ok, character.Name)
	}
	if reason := currentPeerActorSubtype0ReasonText(true, body, 0); reason !=
		"identity_carrier_env_all_peers_sends_peer_subtype0_without_clone_rows" {
		t.Fatalf("all_peers reason = %q", reason)
	}

	// 回退档：两条链都关（身份体不构造，理由串回到 switch_off）。
	t.Setenv(peerUserInfoIdentityCarrierEnv, peerUserInfoIdentityCarrierLegacy)
	if peerUserInfoIdentityCarrierEnabled() || peerUserInfoIdentityCarrierAllPeersEnabled() {
		t.Fatal("legacy must disable both carriers")
	}
	if body := service.peerActorUserInfoSubtype0BodyForProjection(
		context.Background(), leader, follower, character, true, 100, character.Name, plainBody,
	); body != nil {
		t.Fatalf("legacy档 must not build the peer subtype0, got %d bytes", len(body))
	}
	if reason := currentPeerActorSubtype0ReasonText(false, nil, 0); reason != "switch_off" {
		t.Fatalf("legacy reason = %q, want switch_off", reason)
	}

	// 默认档下"没有克隆行"的理由串必须还是原来那一串（既有现场判读口径不变）。
	t.Setenv(peerUserInfoIdentityCarrierEnv, "")
	if reason := currentPeerActorSubtype0ReasonText(true, nil, 0); reason !=
		"peer_has_no_clone_avatar_row_so_the_first_carrier_is_sufficient" {
		t.Fatalf("default reason = %q", reason)
	}
}
