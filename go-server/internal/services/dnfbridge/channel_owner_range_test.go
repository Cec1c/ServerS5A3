package dnfbridge

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"longheng.io/server/internal/modules/dnf/alignedcmd"
	"longheng.io/server/internal/modules/dnf/channelcatalog"
	"longheng.io/server/internal/modules/dnf/channelinfo"
	"longheng.io/server/internal/modules/dnf/dnfenum"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

// P9 回归：频道 id > 255（线上 [deathtower] 508）时，任何下发的 owner 频道字节都必须
// ≤255 且等于 catalog 落点频道，**绝不**允许出现 `byte(508)` = 252 —— 252 是另一个真实
// 频道号，客户端会拿它和自己的场景频道（508）比较，落进
// `mode0-owner-compare ... branch=remote-actor` 后访问违例崩溃
// （2026-09-27 18:32/18:35/18:41/18:44/18:46/18:52 六次，eax=ecx=0xFC）。
//
// 夹具模拟真实 channel_info.etc 顺序：先 [deathtower] 508（type 22，非落点），
// 再普通落点 11（type 11）—— `ResidentFor(508)` 因此给出 11。
const oversizedOwnerChannelFixture = `
[dungeon]
` + "`[deathtower]` `deathtower` 1 2" + `
[/dungeon]
[dungeon]
` + "`[granfloris]` `granfloris` 1 2" + `
[/dungeon]
[server]
1 508 ` + "`deathtower`" + ` 22 ` + "`[deathtower]`" + ` 0 0 11 ` + "`granfloris`" + ` 11 ` + "`[granfloris]`" + ` 0 0
[/server]
`

func oversizedOwnerTestService(t *testing.T) *Service {
	t.Helper()
	index, err := channelinfo.Parse([]byte(oversizedOwnerChannelFixture))
	if err != nil {
		t.Fatalf("parse oversized owner fixture: %v", err)
	}
	catalog, err := channelcatalog.New(index, channelcatalog.Options{ServerID: 1})
	if err != nil {
		t.Fatalf("build oversized owner catalog: %v", err)
	}
	return &Service{
		options: options{
			channelServerID:    1,
			channelAdvertiseID: 0,
			serverIP:           "42.240.165.245",
			initialUDPPort1:    defaultInitialUDPPort1,
			initialUDPPort2:    defaultInitialUDPPort2,
			commandCount:       defaultCommandCount,
			notificationCount:  defaultNotificationCount,
			gameUpperHeader:    gameUpperHeaderServer16,
			gameUpperBodyCodec: gameUpperBodyCodecPlain,
		},
		catalog: catalog,
	}
}

func oversizedOwnerTestSession(t *testing.T, service *Service, conn *bufferConn) *gameSession {
	t.Helper()
	deathtower, ok := service.currentCatalog().Channel(508)
	if !ok {
		t.Fatal("fixture is missing the real [deathtower] channel 508")
	}
	if deathtower.Group != "deathtower" || channelcatalog.IsResidentChannel(deathtower) {
		t.Fatalf("channel 508 fixture = %+v, want a non-resident [deathtower] channel", deathtower)
	}
	landing, ok := service.currentCatalog().Channel(11)
	if !ok || !channelcatalog.IsResidentChannel(landing) {
		t.Fatalf("fixture landing channel = %+v found=%t, want resident channel 11", landing, ok)
	}
	return &gameSession{
		conn:            conn,
		accountID:       "dnf:1",
		channel:         deathtower,
		residentChannel: landing,
	}
}

// 本 P9 的硬断言：频道 id > 255 时下发的 owner 频道必须 ≤255 **且等于落点频道**。
func TestOversizedChannelOwnerFallsBackToLandingChannelWithoutTruncation(t *testing.T) {
	service := oversizedOwnerTestService(t)
	conn := &bufferConn{}
	session := oversizedOwnerTestSession(t, service, conn)
	now := time.Unix(1_721_020_000, 0)

	owner, err := currentCommittedChannelOwner(session)
	if err != nil {
		t.Fatalf("committed owner for channel 508: %v", err)
	}
	if owner > currentOwnerChannelByteLimit {
		t.Fatalf("owner channel = %d, must fit the u8 owner field", owner)
	}
	if owner != 11 {
		t.Fatalf("owner channel = %d, want the catalog landing channel 11", owner)
	}
	if owner == byte(session.channel.ID) {
		t.Fatalf("owner channel = %d is byte(508) = %d: the truncated value is another real channel", owner, byte(session.channel.ID))
	}

	if err := service.sendGameConnectionBootstrap(session, now); err != nil {
		t.Fatalf("send oversized-channel bootstrap: %v", err)
	}
	packet, trailing := splitGameServerUpperPacketWithHeader(t, conn.write.Bytes(), service.gameUpperHeaderSize())
	if packet.Header.Classification != 0 || packet.Header.MsgID != 1 || packet.Header.Seq != 0 {
		t.Fatalf("oversized CHANNELINFO header = %+v, want class0/op1 seq0", packet.Header)
	}
	if len(trailing) != 0 {
		t.Fatalf("oversized bootstrap emitted trailing bytes: %x", trailing)
	}
	plain := decodeCurrentEXEChannelNoticeBody(packet.Body)
	varints, _ := consumeChannelInfoProto(t, plain[4:])
	if values := varints[8]; len(values) != 1 || values[0] != 11 {
		t.Fatalf("CHANNELINFO field8 = %v, want [11] (the same landing channel as the owner byte)", values)
	}
	// `byte(508)` 在本包内已经不可能再被写出来（常量转换会被编译器直接拒绝），
	// 所以这里用等价的字面量 252 表达"那个截断值"。
	const truncatedOversizedOwner = byte(508 % 256) // = 252
	if values := varints[8]; len(values) == 1 && values[0] == uint64(truncatedOversizedOwner) {
		t.Fatalf("CHANNELINFO advertises the truncated owner byte %d", truncatedOversizedOwner)
	}
	if session.connectionTownActorOwnerChannel != 11 || session.townActorOwnerChannel != 11 {
		t.Fatalf(
			"committed owner contexts = connection:%d current:%d, want 11/11",
			session.connectionTownActorOwnerChannel,
			session.townActorOwnerChannel,
		)
	}

	// 线上档（DNFBRIDGE_OWNER_CONTEXT_USE_CLIENT_CHANNEL=on）也必须退化成落点频道：
	// 这条分支是 2026-09-27 崩溃的直接来源（byte(508) = 252）。
	t.Setenv(ownerContextUseClientChannelEnv, "on")
	if got := currentTownActorOwnerContext(session); got != 11 {
		t.Fatalf("client-channel-aligned town owner = %d, want 11 (must not truncate 508 to 252)", got)
	}
	if got := currentConnectionTownActorOwnerContext(session); got != 11 {
		t.Fatalf("client-channel-aligned connection owner = %d, want 11", got)
	}
	if got := currentClientSelectedChannel(session); got != currentSceneObjectContext {
		t.Fatalf("u8 client channel projection = %d, want context 0 (unrepresentable, never 252)", got)
	}
	if got := currentClientSelectedChannelID(session); got != 508 {
		t.Fatalf("untruncated client channel = %d, want 508", got)
	}
}

// 回退必须是具名事件：connected_channel / substituted_channel / 调用点都要在同一行里。
func TestOversizedChannelSubstitutionWritesNamedEvent(t *testing.T) {
	service := oversizedOwnerTestService(t)
	logDir, err := os.MkdirTemp("", "dnfbridge-owner-substitution")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.RemoveAll(logDir) }()
	logPath := filepath.Join(logDir, "owner-substitution-packet-log.txt")
	logger, err := openPacketLogger(logPath)
	if err != nil {
		t.Fatal(err)
	}
	if logger == nil {
		t.Fatal("packet logger was not opened")
	}
	service.packetLog = logger

	conn := &bufferConn{}
	session := oversizedOwnerTestSession(t, service, conn)
	if err := service.sendGameConnectionBootstrap(session, time.Unix(1_721_020_000, 0)); err != nil {
		t.Fatalf("send oversized-channel bootstrap: %v", err)
	}
	if err := logger.Close(); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	logged := string(raw)
	for _, want := range []string{
		"kind=" + currentOwnerChannelSubstitutedEvent,
		"connected_channel=508",
		"substituted_channel=11",
		"resident_channel=11",
		"owner_channel_limit=255",
		"reason=" + currentOwnerChannelSubstitutionReason,
		"call_site=game_connection_bootstrap",
	} {
		if !strings.Contains(logged, want) {
			t.Fatalf("packet log is missing %q:\n%s", want, logged)
		}
	}
	// 公告那条路也要带"公告频道就是落点频道"的口径。
	if !strings.Contains(logged, "owner_channel_source=catalog_resident_landing") ||
		!strings.Contains(logged, "substituted_from_channel=508") {
		t.Fatalf("channel notice event does not record the landing substitution:\n%s", logged)
	}
	if !strings.Contains(logged, "call_site=game_current_channel_notice") {
		t.Fatalf("channel notice substitution event is missing:\n%s", logged)
	}
}

// 落点频道也装不下（或目录里没有落点）时必须**明确失败/回退 context 0**，不许发截断值。
func TestOversizedChannelWithoutLandingChannelFailsExplicitly(t *testing.T) {
	service := oversizedOwnerTestService(t)
	logDir, err := os.MkdirTemp("", "dnfbridge-owner-no-landing")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.RemoveAll(logDir) }()
	logger, err := openPacketLogger(filepath.Join(logDir, "no-landing-packet-log.txt"))
	if err != nil {
		t.Fatal(err)
	}
	service.packetLog = logger

	deathtower, _ := service.currentCatalog().Channel(508)
	conn := &bufferConn{}
	session := &gameSession{
		conn:      conn,
		accountID: "dnf:1",
		channel:   deathtower,
		// 目录不可用/没有落点时 residentChannel 就是连接频道本身（game.go 的既有写法）。
		residentChannel: deathtower,
	}
	if _, err := currentCommittedChannelOwner(session); err == nil {
		t.Fatal("committed owner for an unexpressible channel must fail explicitly")
	}
	if err := service.sendGameConnectionBootstrap(session, time.Unix(1_721_020_000, 0)); err != nil {
		t.Fatalf("send unexpressible-channel bootstrap: %v", err)
	}
	packet, trailing := splitGameServerUpperPacketWithHeader(t, conn.write.Bytes(), service.gameUpperHeaderSize())
	if packet.Header.Classification != 1 || packet.Header.MsgID != 1 || packet.Header.Seq != 0 {
		t.Fatalf("unexpressible bootstrap = %+v, want class1/op1 seq0 fallback", packet.Header)
	}
	if len(trailing) != 0 {
		t.Fatalf("unexpressible bootstrap emitted trailing bytes: %x", trailing)
	}
	if session.connectionTownActorOwnerChannel != currentSceneObjectContext ||
		session.townActorOwnerChannel != currentSceneObjectContext {
		t.Fatalf(
			"unexpressible owner contexts = connection:%d current:%d, want context 0",
			session.connectionTownActorOwnerChannel,
			session.townActorOwnerChannel,
		)
	}
	if err := logger.Close(); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(logDir, "no-landing-packet-log.txt"))
	if err != nil {
		t.Fatal(err)
	}
	logged := string(raw)
	if !strings.Contains(logged, "kind="+currentOwnerChannelSubstitutedEvent) ||
		!strings.Contains(logged, "reason="+currentOwnerChannelNoLandingReason) ||
		!strings.Contains(logged, "fallback=session_owner_context_zero") {
		t.Fatalf("unexpressible owner path is not recorded:\n%s", logged)
	}
}

// 转换点本身：1..255 逐字节不变，越界一律 ok=false（不再有静默截断）。
func TestOwnerChannelFieldValueNeverTruncates(t *testing.T) {
	for channelID := 1; channelID <= currentOwnerChannelByteLimit; channelID++ {
		got, ok := currentOwnerChannelFieldValue(channelID)
		if !ok || got != byte(channelID) {
			t.Fatalf("channel %d = (%d, %t), want (%d, true)", channelID, got, ok, byte(channelID))
		}
	}
	for _, channelID := range []int{-1, 0, 256, 261, 501, 508, 1024} {
		got, ok := currentOwnerChannelFieldValue(channelID)
		if ok {
			t.Fatalf("channel %d = (%d, true), want an explicit out-of-range failure", channelID, got)
		}
		if got != currentSceneObjectContext {
			t.Fatalf("channel %d out-of-range value = %d, want context 0", channelID, got)
		}
		if !currentOwnerChannelOversized(channelID) {
			t.Fatalf("channel %d must be reported as oversized", channelID)
		}
	}
	if currentOwnerChannelOversized(currentOwnerChannelByteLimit) {
		t.Fatalf("channel %d must still fit the owner field", currentOwnerChannelByteLimit)
	}
}

// op9 成员行的内联身份块判据必须用**未截断**的客户端频道：
// 客户端在 508 时任何 u8 记录字节都不可能等于它的场景频道 ⇒ 成员行必须一律带身份块。
func TestOversizedSessionOp9MemberRowsKeepCrossChannelIdentityBlock(t *testing.T) {
	service := oversizedOwnerTestService(t)
	session := oversizedOwnerTestSession(t, service, &bufferConn{})
	localChannelID := currentClientSelectedChannelID(session)
	if localChannelID != 508 {
		t.Fatalf("untruncated client channel = %d, want 508", localChannelID)
	}
	character := dnfrepo.CharacterRecord{CharacterID: "3", Name: "actorthree", Job: "14", Level: 90}
	members := []currentSceneOp9PartyMemberProjection{
		{State: alignedcmd.PartyMemberState{UserID: 1005, UserState: 1}, Name: "memberfive", Job: 1, Level: 90, Grow: 0},
	}
	body := buildCurrentSceneOp9ActorPartyDisplayBodyInContextWithSelectedChannel(
		3,
		character,
		true,
		"actorthree",
		11,
		localChannelID,
		members,
		0,
	)
	if !bytes.Contains(body, rosterNameBytes("memberfive")) {
		t.Fatalf("op9 member row lost its cross-channel identity block: % x", body)
	}
	if bytes.Contains(body, rosterNameBytes("actorthree")) == false {
		t.Fatalf("op9 actor record lost its own name block: % x", body)
	}
	// 反证（旧写法）：参照值被截断成 byte(508) = 252 时，只要 owner 字节恰好等于这个
	// 截断值，服务端就会误判成"同频道"而不写身份块 —— 客户端却一定会按跨频道读，
	// 于是行长度错位（2026-09-24 双开进图闪退的同一机理）。
	truncated := buildCurrentSceneOp9ActorPartyDisplayBodyInContextWithSelectedChannel(
		3,
		character,
		true,
		"actorthree",
		byte(session.channel.ID),
		int(byte(session.channel.ID)),
		members,
		0,
	)
	if bytes.Contains(truncated, rosterNameBytes("memberfive")) {
		t.Fatalf("counterfactual fixture is not exercising the truncation hazard: % x", truncated)
	}
}

// 目录闸门是可选的：默认档下发的频道目录逐字节不变（508 仍在），
// 只有显式打开 DNFBRIDGE_CHANNEL_DIRECTORY_OWNER_RANGE 才裁掉 >255 的频道。
func TestChannelDirectoryOwnerRangeGateIsOptIn(t *testing.T) {
	service := oversizedOwnerTestService(t)
	logDir, err := os.MkdirTemp("", "dnfbridge-owner-gate")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.RemoveAll(logDir) }()
	logger, err := openPacketLogger(filepath.Join(logDir, "owner-gate-packet-log.txt"))
	if err != nil {
		t.Fatal(err)
	}
	service.packetLog = logger

	defaultChannels := service.channelsForRequestMode("", false)
	if len(defaultChannels) != 2 {
		t.Fatalf("default directory count = %d (%+v), want the unchanged 2-channel fixture", len(defaultChannels), defaultChannels)
	}
	foundOversized := false
	for _, channel := range defaultChannels {
		if channel.ID == 508 {
			foundOversized = true
		}
	}
	if !foundOversized {
		t.Fatal("default directory must keep channel 508: the gate is opt-in")
	}

	t.Setenv(channelDirectoryOwnerRangeGateEnv, "u8")
	gatedChannels := service.channelsForRequestMode("", false)
	if len(gatedChannels) != 1 || gatedChannels[0].ID != 11 {
		t.Fatalf("gated directory = %+v, want only channel 11", gatedChannels)
	}
	for _, channel := range gatedChannels {
		if currentOwnerChannelOversized(channel.ID) {
			t.Fatalf("gated directory still carries channel %d", channel.ID)
		}
	}
	if err := logger.Close(); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(logDir, "owner-gate-packet-log.txt"))
	if err != nil {
		t.Fatal(err)
	}
	logged := string(raw)
	if !strings.Contains(logged, "kind="+channelDirectoryOwnerRangeGateEvent) ||
		!strings.Contains(logged, "dropped_channels=[508]") ||
		!strings.Contains(logged, "env="+channelDirectoryOwnerRangeGateEnv) {
		t.Fatalf("owner-range directory gate is not recorded:\n%s", logged)
	}
}

// 聊天通知里的频道字节同样不得截断：目标是 >255 的会话时下发落点频道。
func TestPeerChatOwnerChannelUsesLandingChannelForOversizedTarget(t *testing.T) {
	service, source, target, _, targetConn := newCurrentSocialPeerTest(t)
	target.channel = channelcatalog.Channel{ID: 508, Type: 22, Group: "deathtower", Name: "ch.508", Port: 10508}
	target.residentChannel = channelcatalog.Channel{ID: 11, Type: 11, Group: "granfloris", Name: "ch.11", Port: 10011}

	var request packetWriter
	request.writeByte(1)
	request.writeUint16(5)
	request.writeUint32(0)
	request.writeRawDstr([]byte("hello"))
	request.writeRawDstr(rosterNameBytes("来来来"))

	handled, err := service.handleAlignedGameCommand(source, byte(dnfenum.GameCmdCommand), currentPeerChatStateOpcode, request.bytes())
	if err != nil || !handled {
		t.Fatalf("peer chat handled=%t err=%v", handled, err)
	}
	targetState, targetRest := splitGameServerUpperPacket(t, targetConn.write.Bytes())
	if targetState.Header.MsgID != currentPeerChatStateOpcode {
		t.Fatalf("target chat state msg = %d, want %d", targetState.Header.MsgID, currentPeerChatStateOpcode)
	}
	chatNotice, trailing := splitGameServerUpperPacket(t, targetRest)
	if len(trailing) != 0 {
		t.Fatalf("target chat trailing bytes = % x", trailing)
	}
	wantNotice := buildCurrentPeerChatNoticeBody(1, 11, 1, rosterNameBytes("pouut"), []byte("hello"))
	if !bytes.Equal(chatNotice.Body, wantNotice) {
		t.Fatalf("target chat notice body = % x, want landing-channel body % x", chatNotice.Body, wantNotice)
	}
}
