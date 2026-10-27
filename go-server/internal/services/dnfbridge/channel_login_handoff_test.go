package dnfbridge

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"io"
	"strconv"
	"testing"
	"time"

	"longheng.io/server/internal/modules/dnf/channelcatalog"
	"longheng.io/server/internal/modules/dnf/channelinfo"
	dnfproto "longheng.io/server/internal/modules/dnf/protocol"
)

func firstLoginChannelFixture(t *testing.T, target int) (*Service, *gameSession) {
	t.Helper()
	s, p := coldChannelLandingFixture(t, channelcatalog.Channel{})
	index, err := channelinfo.Parse([]byte("[server]\n1 " +
		"10 `普通10` 11 `[elven_guard]` 0 0 " +
		"11 `普通11` 11 `[elven_guard]` 0 0 " +
		"21 `普通21` 11 `[behemoth]` 0 0 " +
		"75 `决斗` 8 `[pvp]` 0 0 " +
		"23 `安图恩` 23 `[anton_raid]` 0 0 " +
		"181 `卢克` 32 `[luke_raid]` 0 0 " +
		"184 `超时空` 42 `[fiendwar]` 0 0 " +
		"187 `帝国` 39 `[tagtournament]` 0 0\n[/server]\n"))
	if err != nil {
		t.Fatal(err)
	}
	s.catalog, err = channelcatalog.New(index, channelcatalog.Options{})
	if err != nil {
		t.Fatal(err)
	}
	p.channel, _ = s.catalog.Channel(target)
	p.residentChannel, _ = s.catalog.Channel(10)
	p.rosterRequested, p.gameEndpointSuccessSent = false, false
	p.characterGeneration = 1
	p.loginChannelFlowKey = sha256.Sum256([]byte("合成登录会话"))
	return s, p
}

func assertFirstChannelLoginSuccess(t *testing.T, s *Service, p *gameSession, channel channelcatalog.Channel) {
	t.Helper()
	packets := splitAllCurrentGameServerUpperPackets(t, p.conn.(*bufferConn).write.Bytes())
	count := 0
	for _, packet := range packets {
		if packet.Header.Classification == 1 && packet.Header.MsgID == 1 {
			count++
			// 本组验证换线保留目标；专用玩法的类型投影沿用原LOGIN编码器。
			// 例如超时空42→45由其独立MR及专属协议回归负责。
			wantType := s.buildLoginSuccess(channel)[2]
			if len(packet.Body) < 16 || packet.Body[3] != wantType {
				t.Fatalf("首次LOGIN类型不符：频道%d 正文%x", channel.ID, packet.Body)
			}
			ipLen := int(binary.LittleEndian.Uint32(packet.Body[9:13]))
			portOffset := 13 + ipLen
			if portOffset+4 > len(packet.Body) || binary.LittleEndian.Uint32(packet.Body[portOffset:portOffset+4]) != uint32(channel.Port) {
				t.Fatal("首次LOGIN没有采用同一目标频道的端口")
			}
		}
		if packet.Header.MsgID == 2 && packet.Header.Classification == 0 || packetLooksLikeInitialTownTransition(packet) {
			t.Fatal("LOGIN阶段提前发送角色列表或场景")
		}
	}
	if count != 1 || !p.gameEndpointSuccessSent {
		t.Fatalf("首次LOGIN成功应答数量=%d 状态=%t", count, p.gameEndpointSuccessSent)
	}
}

func TestFirstSpecialLoginCorrectsBeforeAnySuccess(t *testing.T) {
	for _, id := range []int{187, 23, 181} {
		t.Run(strconv.Itoa(id), func(t *testing.T) {
			s, p := firstLoginChannelFixture(t, id)
			if err := s.handleCurrentGameEndpointRequest(p, 1, currentObservedChannelReconnectDisplayProbeSize); err != nil {
				t.Fatal(err)
			}
			packets := splitAllCurrentGameServerUpperPackets(t, p.conn.(*bufferConn).write.Bytes())
			if len(packets) != 1 || p.gameEndpointSuccessSent || p.channel.ID != 10 || p.channel.Type != 11 || p.coldChannelLanding == nil || !p.coldChannelLanding.waiting || p.connectionTownActorOwnerChannel != 10 || p.townActorOwnerChannel != 10 {
				t.Fatal("首次成功前没有统一普通频道、类型和owner", p.channel, len(packets))
			}
			assertCurrentChannelNoticePacket(t, packets[0], 0, 10)
			// 客户端响应目标CHANNELINFO后才发唯一一次普通LOGIN成功。
			if err := s.handleCurrentGameEndpointRequest(p, 1, currentObservedChannelReconnectDisplayProbeSize); err != nil {
				t.Fatal(err)
			}
			ordinary, _ := s.catalog.Channel(10)
			assertFirstChannelLoginSuccess(t, s, p, ordinary)
			if p.coldChannelLanding.waiting || p.rosterRequested {
				t.Fatal("提前代客户端请求了角色列表或未结束握手")
			}
			before := append([]byte(nil), p.conn.(*bufferConn).write.Bytes()...)
			if err := s.handleCurrentGameEndpointRequest(p, 1, currentObservedChannelReconnectDisplayProbeSize); err != nil || !bytes.Equal(before, p.conn.(*bufferConn).write.Bytes()) {
				t.Fatal("重复LOGIN重放成功", err)
			}
			if err := s.sendUpperGetUserInfoBootstrap(p); err != nil {
				t.Fatal(err)
			}
			roster := false
			for _, packet := range splitAllCurrentGameServerUpperPackets(t, p.conn.(*bufferConn).write.Bytes()[len(before):]) {
				roster = roster || packet.Header.MsgID == 2 && packet.Header.Classification == 0
			}
			if !roster {
				t.Fatal("真实GET_USERINFO未恢复普通选角")
			}
		})
	}
}

func TestDirectFiendWarLoginDoesNotColdRedirect(t *testing.T) {
	s, p := firstLoginChannelFixture(t, 184)
	if err := s.handleCurrentGameEndpointRequest(p, 1, currentObservedChannelReconnectDisplayProbeSize); err != nil {
		t.Fatal(err)
	}
	if p.channel.ID != 184 || p.channel.Type != 42 || p.coldChannelLanding != nil || p.channelExitHandoff != nil {
		t.Fatalf("direct FiendWar LOGIN was treated as stale special channel: %+v cold=%+v handoff=%+v", p.channel, p.coldChannelLanding, p.channelExitHandoff)
	}
	assertFirstChannelLoginSuccess(t, s, p, p.channel)
	p.rosterRequested = true
	if changed, err := s.redirectCurrentColdSpecialChannel(p, 0); changed || err != nil {
		t.Fatalf("direct FiendWar roster request was redirected: changed=%t err=%v", changed, err)
	}
}

func TestFirstLoginDispatchValidatesTokenBeforeChannelCorrection(t *testing.T) {
	s, p := firstLoginChannelFixture(t, 187)
	p.loginPacketSeen, p.accountFromToken, p.loginChannelFlowKey = false, false, [32]byte{}
	token := "de509f65000000000000000000000001" // 仅合成夹具。
	s.loginTokens.remember(token, p.accountID, time.Now().Add(time.Hour))
	body := make([]byte, currentObservedChannelReconnectDisplayProbeSize)
	copy(body, buildClientLoginBody(token))
	raw, err := dnfproto.BuildChannelPacket(1, body, 1, 1)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.handleGameUpper(p, raw); err != nil {
		t.Fatal(err)
	}
	if !p.accountFromToken || p.loginChannelFlowKey != sha256.Sum256([]byte(token)) || p.channel.ID != 10 || p.gameEndpointSuccessSent {
		t.Fatal("真实LOGIN分发未在认证后、成功前更正")
	}
	if err := s.handleGameUpper(p, raw); err != nil {
		t.Fatal(err)
	}
	ordinary, _ := s.catalog.Channel(10)
	assertFirstChannelLoginSuccess(t, s, p, ordinary)
}

func TestFirstChannelLoginKeepsExplicitTargetsAndPVP(t *testing.T) {
	for _, id := range []int{10, 21, 75, 23, 181, 184, 187} {
		t.Run(strconv.Itoa(id), func(t *testing.T) {
			s, p := firstLoginChannelFixture(t, id)
			ordinary, _ := s.catalog.Channel(21)
			previous := &gameSession{channel: ordinary, accountID: p.accountID, selectedCharacterID: 29, loginPacketSeen: true, accountFromToken: true, loginChannelFlowKey: p.loginChannelFlowKey}
			s.rememberCurrentAuthenticatedChannelHandoff(previous)
			target := p.channel
			if err := s.handleCurrentGameEndpointRequest(p, 1, currentObservedChannelReconnectDisplayProbeSize); err != nil {
				t.Fatal(err)
			}
			assertFirstChannelLoginSuccess(t, s, p, target)
			if p.channel.ID != id || p.channelExitHandoff == nil || p.coldChannelLanding != nil {
				t.Fatal("主动换线目标被改写", id, p.channel)
			}
			// 即使随后请求角色列表，也不能把明确进入帝国判成冷登录。
			p.rosterRequested = true
			if changed, err := s.redirectCurrentColdSpecialChannel(p, 0); changed || err != nil {
				t.Fatal("主动换线被后续GET_USERINFO重判", changed, err)
			}
		})
	}
	s, p := firstLoginChannelFixture(t, 75)
	if err := s.handleCurrentGameEndpointRequest(p, 1, currentObservedChannelReconnectDisplayProbeSize); err != nil || p.channel.ID != 75 {
		t.Fatal("没有交接记录的既有决斗路径发生变化", err)
	}
	assertFirstChannelLoginSuccess(t, s, p, p.channel)
}

func TestEmpireSameChannelReturnUsesCarriedOrdinarySource(t *testing.T) {
	s, p := firstLoginChannelFixture(t, 187)
	ordinary, _ := s.catalog.Channel(21)
	key, _ := currentChannelHandoffKey(p)
	s.channelHandoffs = map[currentAuthenticatedChannelHandoffKey]*currentAuthenticatedChannelHandoff{
		key: {source: ordinary, ordinary: ordinary, expiresAt: s.gameplayNow().Add(time.Minute)},
	}
	if err := s.handleCurrentGameEndpointRequest(p, 1, currentObservedChannelReconnectDisplayProbeSize); err != nil {
		t.Fatal(err)
	}
	p.selectedCharacterID = 29
	// 帝国返回按钮现用EXIT正文只有01，没有目标；真实现场随后回连同一187。
	if err := s.handleCurrentChannelExit(p, []byte{1}, "test"); err != nil {
		t.Fatal(err)
	}
	_, next := firstLoginChannelFixture(t, 187)
	if err := s.handleCurrentGameEndpointRequest(next, 1, currentObservedChannelReconnectDisplayProbeSize); err != nil {
		t.Fatal(err)
	}
	if next.channel.ID != 21 || next.gameEndpointSuccessSent || next.channelExitHandoff == nil {
		t.Fatal("帝国同频道回接没有返回进入前的普通来源", next.channel)
	}
	if err := s.handleCurrentGameEndpointRequest(next, 1, currentObservedChannelReconnectDisplayProbeSize); err != nil {
		t.Fatal(err)
	}
	assertFirstChannelLoginSuccess(t, s, next, ordinary)
}

func TestChannelHandoffRestoresAfterColdRedirectWriteFailure(t *testing.T) {
	s, p := firstLoginChannelFixture(t, 187)
	ordinary, _ := s.catalog.Channel(21)
	key, _ := currentChannelHandoffKey(p)
	s.channelHandoffs = map[currentAuthenticatedChannelHandoffKey]*currentAuthenticatedChannelHandoff{
		key: {source: p.channel, ordinary: ordinary, expiresAt: s.gameplayNow().Add(time.Minute)},
	}
	p.conn = &firstChannelFailedWriteConn{}
	if err := s.handleCurrentGameEndpointRequest(p, 1, currentObservedChannelReconnectDisplayProbeSize); err == nil {
		t.Fatal("合成短写没有中止频道更正")
	}
	if p.coldChannelLanding == nil {
		t.Fatal("失败前没有进入频道更正")
	}
	entry := s.takeCurrentAuthenticatedChannelHandoff(p)
	if entry == nil || entry.ordinary.ID != ordinary.ID {
		t.Fatalf("失败后没有恢复原普通来源：%+v", entry)
	}
	if s.takeCurrentAuthenticatedChannelHandoff(p) != nil {
		t.Fatal("恢复后的交接仍可重复消费")
	}
}

func TestChannelHandoffRestoresWhenCorrectedLoginWriteFails(t *testing.T) {
	s, p := firstLoginChannelFixture(t, 187)
	ordinary, _ := s.catalog.Channel(21)
	key, _ := currentChannelHandoffKey(p)
	s.channelHandoffs = map[currentAuthenticatedChannelHandoffKey]*currentAuthenticatedChannelHandoff{
		key: {source: p.channel, ordinary: ordinary, expiresAt: s.gameplayNow().Add(time.Minute)},
	}
	if err := s.handleCurrentGameEndpointRequest(p, 1, currentObservedChannelReconnectDisplayProbeSize); err != nil {
		t.Fatal(err)
	}
	if p.coldChannelLanding == nil || p.channel.ID != ordinary.ID {
		t.Fatalf("首次LOGIN没有进入更正后的普通频道：%+v", p.channel)
	}
	p.conn = &firstChannelFailedWriteConn{}
	if err := s.handleCurrentGameEndpointRequest(p, 1, currentObservedChannelReconnectDisplayProbeSize); err == nil {
		t.Fatal("更正后的LOGIN写失败没有返回错误")
	}
	entry := s.takeCurrentAuthenticatedChannelHandoff(p)
	if entry == nil || entry.ordinary.ID != ordinary.ID {
		t.Fatalf("新LOGIN失败后没有恢复原普通来源：%+v", entry)
	}
}

func TestChannelHandoffRestoresAfterExplicitTargetLoginFailure(t *testing.T) {
	s, p := firstLoginChannelFixture(t, 187)
	ordinary, _ := s.catalog.Channel(21)
	key, _ := currentChannelHandoffKey(p)
	s.channelHandoffs = map[currentAuthenticatedChannelHandoffKey]*currentAuthenticatedChannelHandoff{
		key: {source: ordinary, ordinary: ordinary, expiresAt: s.gameplayNow().Add(time.Minute)},
	}
	p.conn = &firstChannelFailedWriteConn{}
	if err := s.handleCurrentGameEndpointRequest(p, 1, currentObservedChannelReconnectDisplayProbeSize); err == nil {
		t.Fatal("显式目标LOGIN写失败没有返回错误")
	}
	if p.coldChannelLanding != nil || p.channel.ID != 187 {
		t.Fatalf("显式目标被误改成冷登录回落：%+v", p.channel)
	}
	entry := s.takeCurrentAuthenticatedChannelHandoff(p)
	if entry == nil || entry.source.ID != ordinary.ID || entry.ordinary.ID != ordinary.ID {
		t.Fatalf("显式目标失败后没有恢复原交接：%+v", entry)
	}
}

func TestChannelHandoffIsAuthenticatedExpiringSingleUseAndIsolated(t *testing.T) {
	s, p := firstLoginChannelFixture(t, 21)
	p.selectedCharacterID = 29
	now := time.Unix(1792396800, 0)
	s.gameplayTimers = &processGameplayTimeQueue{now: func() time.Time { return now }}
	for _, invalid := range []string{"未认证", "未LOGIN", "未选角色", "无摘要", "空账号", "旧配套"} {
		t.Run(invalid, func(t *testing.T) {
			account, authenticated, seen, id, flow, version := p.accountID, p.accountFromToken, p.loginPacketSeen, p.selectedCharacterID, p.loginChannelFlowKey, s.options.currentClientVersion
			switch invalid {
			case "未认证":
				p.accountFromToken = false
			case "未LOGIN":
				p.loginPacketSeen = false
			case "未选角色":
				p.selectedCharacterID = 0
			case "无摘要":
				p.loginChannelFlowKey = [32]byte{}
			case "空账号":
				p.accountID = ""
			case "旧配套":
				s.options.currentClientVersion = "90cn"
			}
			s.rememberCurrentAuthenticatedChannelHandoff(p)
			if len(s.channelHandoffs) != 0 {
				t.Fatal("无权登记的连接建立了换线交接")
			}
			p.accountID, p.accountFromToken, p.loginPacketSeen, p.selectedCharacterID, p.loginChannelFlowKey, s.options.currentClientVersion = account, authenticated, seen, id, flow, version
		})
	}
	for _, mismatch := range []string{"其他账号", "其他启动令牌", "过期"} {
		t.Run(mismatch, func(t *testing.T) {
			cancel := s.rememberCurrentAuthenticatedChannelHandoff(p)
			probe := &gameSession{accountID: p.accountID, accountFromToken: true, loginPacketSeen: true, loginChannelFlowKey: p.loginChannelFlowKey}
			switch mismatch {
			case "其他账号":
				probe.accountID += "-other"
			case "其他启动令牌":
				probe.loginChannelFlowKey[0]++
			case "过期":
				now = now.Add(currentAuthenticatedChannelHandoffLifetime)
			}
			if s.takeCurrentAuthenticatedChannelHandoff(probe) != nil {
				t.Fatal("跨账号、启动器令牌或有效期继承了换线")
			}
			cancel()
		})
	}
	cancelOld := s.rememberCurrentAuthenticatedChannelHandoff(p)
	s.rememberCurrentAuthenticatedChannelHandoff(p)
	cancelOld() // 旧写失败不能撤销另一次新EXIT。
	if entry := s.takeCurrentAuthenticatedChannelHandoff(p); entry == nil || entry.ordinary.ID != 21 {
		t.Fatal("新交接被旧撤销覆盖")
	}
	if s.takeCurrentAuthenticatedChannelHandoff(p) != nil {
		t.Fatal("交接被重复消费")
	}
	p.conn = &firstChannelFailedWriteConn{}
	if err := s.handleCurrentChannelExit(p, []byte{1}, "test"); err == nil {
		t.Fatal("写失败被吞掉")
	}
	if s.takeCurrentAuthenticatedChannelHandoff(p) != nil {
		t.Fatal("EXIT写失败后仍允许继承换线")
	}
}

type firstChannelFailedWriteConn struct{ bufferConn }

func (*firstChannelFailedWriteConn) Write([]byte) (int, error) { return 0, io.ErrClosedPipe }

func TestFirstLoginPreservesActiveRaidReconnectAndRejectsInvalidSeat(t *testing.T) {
	for _, invalid := range []string{"有效", "过期", "其他账号", "其他实例", "其他频道", "失败", "结束"} {
		t.Run(invalid, func(t *testing.T) {
			s, p := firstLoginChannelFixture(t, 181)
			now := s.gameplayNow()
			state := &runtimeRaidState{RaidKey: 1, Started: true, Members: []runtimeRaidMemberState{{CharID: 29}}, Battle: &runtimeRaidBattle{InstanceID: "test", ChannelID: 181, ChannelType: 32, MemberAccounts: map[uint16]string{29: p.accountID}}}
			seat := currentRaidDisconnectSeat{Raid: state, RaidKey: 1, CharacterID: 29, AccountID: p.accountID, InstanceID: "test", DisconnectedAt: now.Add(-time.Minute), ExpiresAt: now.Add(time.Minute)}
			switch invalid {
			case "过期":
				seat.ExpiresAt = now
			case "其他账号":
				seat.AccountID += "-other"
			case "其他实例":
				seat.InstanceID += "-other"
			case "其他频道":
				state.Battle.ChannelID++
			case "失败":
				state.Battle.TimedOut = true
			case "结束":
				state.Battle.PhaseCompleted = true
			}
			s.raids = map[uint32]*runtimeRaidState{1: state}
			s.currentRaidDisconnects = map[uint16]currentRaidDisconnectSeat{29: seat}
			if err := s.handleCurrentGameEndpointRequest(p, 1, currentObservedChannelReconnectDisplayProbeSize); err != nil {
				t.Fatal(err)
			}
			if invalid == "有效" {
				if p.channel.ID != 181 || p.coldChannelLanding != nil {
					t.Fatal("有效团本断线席位被普通回落覆盖")
				}
				assertFirstChannelLoginSuccess(t, s, p, p.channel)
			} else if p.channel.ID != 10 || p.gameEndpointSuccessSent {
				t.Fatal("无效团本断线席位提前接受了特殊类型", invalid)
			}
		})
	}
}

func TestFirstLoginCorrectionBlocksStaleSelectionAndChangedAccount(t *testing.T) {
	s, p := firstLoginChannelFixture(t, 187)
	if err := s.handleCurrentGameEndpointRequest(p, 1, currentObservedChannelReconnectDisplayProbeSize); err != nil {
		t.Fatal(err)
	}
	before := append([]byte(nil), p.conn.(*bufferConn).write.Bytes()...)
	if err := s.sendUpperCSharpSelectInit(p, []byte{0, 0}); err != nil {
		t.Fatal(err)
	}
	if err := s.sendUpperGetUserInfoRosterBootstrapFixed16(p); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, p.conn.(*bufferConn).write.Bytes()) || p.selectedCharacterID != 0 {
		t.Fatal("旧选角越过首次LOGIN更正")
	}
	p.accountID += "-other"
	if err := s.handleCurrentGameEndpointRequest(p, 1, currentObservedChannelReconnectDisplayProbeSize); err == nil || !bytes.Equal(before, p.conn.(*bufferConn).write.Bytes()) {
		t.Fatal("更正中接受其他账号", err)
	}
}
