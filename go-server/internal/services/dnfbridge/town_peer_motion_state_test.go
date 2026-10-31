package dnfbridge

import (
	"bytes"
	"encoding/binary"
	"testing"
)

// 现场报障：「在城镇看其他玩家的移动速度不正确 对方自己的视角看很正常速度很快
// 我的视角看对方走的很慢不正常」。
//
// 根因是城镇同屏广播 noti0x16 的**最后一个 u16**（厂商口径里的 motionState）：
//
//	厂商 ServerS4A21-master/Server/DfoServer/Network/Handlers/TownHandler.cs:174/184
//	  var motionState = BitConverter.ToUInt16(body, 5);              // C2S op35 body[5:7]
//	  TownAreaNotificationBuilder.BuildUserPosition(snap, motionState);  // S2C noti0x16 body[7:9]
//
// 我们此前把它解析进 currentTownSetUserPositionRequest.OpaqueScaledU16 之后就丢掉了，
// 广播时恒写 currentTownInitialMovementRate（100）。观者客户端**只能**从这一条 0x0016
// 学到同屏 actor 的移动速率（op24 AREA_USERS 与 op23 noti0x17 的布局里都没有这个字段），
// 于是观者侧永远按 100 走，而移动方自己走的是本机速度 —— 两边因此不对称。
//
// 本文件钉四件事：
//  ① 开关档位（默认 = 透传客户端值；legacy/0/off/false/no = 恒写 100）；
//  ② legacy 档下 0x0016 的字节与改动前**逐字节相同**（可回退性）；
//  ③ 同屏广播确实把移动方 op35 的 body[5:7] 原样透传给观者，且只差 body[7:9] 两个字节；
//  ④ 服务端自己的坐标投影（op36 过图收尾 / 登录落点，两端都只发给本人）仍用 100，
//     不受本开关影响 —— 那条路径上没有客户端上报过这个字段。

func TestTownPeerMotionStateSwitchValues(t *testing.T) {
	for _, value := range []string{"", "on", "ON", "client", "1", "true", "yes", "nonempty"} {
		t.Setenv(townPeerMotionStateEnv, value)
		if !currentTownPeerMotionStateFromClient() {
			t.Fatalf("value %q must forward the client motion state (default = fixed)", value)
		}
		if got := currentTownPeerMotionStateMode(); got != "client_op35_motion_state" {
			t.Fatalf("value %q mode=%q want client_op35_motion_state", value, got)
		}
	}
	for _, value := range []string{"legacy", "LEGACY", "  legacy  ", "0", "off", "false", "no"} {
		t.Setenv(townPeerMotionStateEnv, value)
		if currentTownPeerMotionStateFromClient() {
			t.Fatalf("value %q must revert to the constant 100", value)
		}
		if got := currentTownPeerMotionStateMode(); got != "legacy_constant_100" {
			t.Fatalf("value %q mode=%q want legacy_constant_100", value, got)
		}
	}
}

// 0x0016 的编码点：motionState 只改 body[7:9]，其余 7 个字节与旧实现完全一致。
func TestTownUserPositionNotificationBodyMotionStateOnlyChangesTheTrailingU16(t *testing.T) {
	// 改动前那条断言（initial_town_location_notifications_test.go:9-12）的逐字节值。
	legacy := []byte{29, 0, 218, 1, 234, 0, 5, 100, 0}
	if got := buildCurrentTownUserPositionNotificationBody(29, 474, 234, 5); !bytes.Equal(got, legacy) {
		t.Fatalf("legacy noti16 body=%x want=%x", got, legacy)
	}
	if got := buildCurrentTownUserPositionNotificationBodyWithMotionState(
		29, 474, 234, 5, currentTownInitialMovementRate,
	); !bytes.Equal(got, legacy) {
		t.Fatalf("explicit-100 noti16 body=%x want=%x", got, legacy)
	}
	// 225 = 现场实测的 op35 body[5:7] 之一（packet_log.txt:23047 char_id=6 opaque_scaled_u16=225）。
	live := buildCurrentTownUserPositionNotificationBodyWithMotionState(29, 474, 234, 5, 225)
	if !bytes.Equal(live[:7], legacy[:7]) {
		t.Fatalf("motion state leaked into body[0:7]: %x", live)
	}
	if got := binary.LittleEndian.Uint16(live[7:9]); got != 225 {
		t.Fatalf("body[7:9]=%d want 225 (body=%x)", got, live)
	}
}

type townPeerMotionStateFixture struct {
	service     *Service
	mover       *gameSession
	observer    *gameSession
	observerBuf *bufferConn
	moverBuf    *bufferConn
}

// newTownPeerMotionStateFixture 把移动方（角色 29）与观者（角色 17）登记进**同一个**
// (town, area, channel, instance) 桶：38/2 同频道 19，正是现场"城镇里互相看得见"的场景。
func newTownPeerMotionStateFixture(t *testing.T) townPeerMotionStateFixture {
	t.Helper()
	service, mover, _ := newTownMoveTest(t)
	service.onlinePlayers = newOnlinePlayerManager()

	channel := mover.channel
	observer := &gameSession{
		conn:                            &bufferConn{},
		connID:                          "town-peer-motion-state-observer",
		channel:                         channel,
		residentChannel:                 channel,
		connectionTownActorOwnerChannel: byte(channel.ID),
		townActorOwnerChannel:           byte(channel.ID),
		selectedCharacterID:             17,
		townSceneReadyCharacterID:       17,
	}
	startTownPeerTest(t, service, mover, &onlinePlayerInfo{
		CharacterID: 29, AccountID: "dnf:1", Name: "mover", TownID: 38, AreaID: 2,
		PositionX: 450, PositionY: 234, Direction: 5, AreaState: 3, Session: mover,
	})
	startTownPeerTest(t, service, observer, &onlinePlayerInfo{
		CharacterID: 17, AccountID: "dnf:2", Name: "observer", TownID: 38, AreaID: 2,
		PositionX: 400, PositionY: 200, Direction: 5, AreaState: 3, Session: observer,
	})
	mover.townMu.Lock()
	setCurrentTownPositionSceneLocked(mover, 29, 38, 2)
	mover.townMu.Unlock()
	observerBuf := observer.conn.(*bufferConn)
	moverBuf := mover.conn.(*bufferConn)
	observerBuf.write.Reset()
	moverBuf.write.Reset()
	return townPeerMotionStateFixture{
		service: service, mover: mover, observer: observer,
		observerBuf: observerBuf, moverBuf: moverBuf,
	}
}

// 观者收到的 0x0016：默认档 = 移动方 op35 的 body[5:7]；legacy 档 = 逐字节回到 100。
func TestTownPeerMoveBroadcastCarriesTheMoverMotionState(t *testing.T) {
	for _, test := range []struct {
		name        string
		switchValue string
		motionState uint16
		wantState   uint16
	}{
		// 现场 packet_log.txt 里 op35 的 opaque_scaled_u16 实测取值：135/155/205/225/285。
		{name: "default_forwards_live_client_value", switchValue: "", motionState: 225, wantState: 225},
		{name: "default_forwards_second_live_value", switchValue: "", motionState: 205, wantState: 205},
		{name: "legacy_reverts_to_constant_100", switchValue: "legacy", motionState: 225, wantState: 100},
		{name: "legacy_off_reverts_to_constant_100", switchValue: "off", motionState: 285, wantState: 100},
		// 厂商也是原样回填（不加 "0 就回落" 的兜底），这里把这个口径钉死。
		{name: "zero_is_forwarded_verbatim_like_the_vendor", switchValue: "", motionState: 0, wantState: 0},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Setenv(townPeerMotionStateEnv, test.switchValue)
			fixture := newTownPeerMotionStateFixture(t)
			if err := fixture.service.handleTownSetUserPosition(
				fixture.mover,
				buildTownPositionRequest(809, 410, 5, test.motionState),
			); err != nil {
				t.Fatalf("op35 report: %v", err)
			}
			drainTownPeerTest(t, fixture.service, fixture.observer)
			drainTownPeerTest(t, fixture.service, fixture.mover)

			packet, rest := splitCurrentGameServerUpperPacketAuto(t, fixture.observerBuf.write.Bytes())
			if packet.Header.MsgID != currentTownUserPositionNotificationMsgID ||
				packet.Header.Classification != 0 {
				t.Fatalf("observer frame header=%+v", packet.Header)
			}
			want := buildCurrentTownUserPositionNotificationBodyWithMotionState(
				29, 809, 410, 5, test.wantState,
			)
			if !bytes.Equal(packet.Body, want) {
				t.Fatalf("observer noti16 body=%x want=%x", packet.Body, want)
			}
			if len(rest) != 0 {
				t.Fatalf("observer received %d extra bytes after the move frame: %x", len(rest), rest)
			}
			// 移动方自己不该收到这条广播（观者视角的问题不能靠回发给本人掩盖）。
			if fixture.moverBuf.write.Len() != 0 {
				t.Fatalf("mover must not receive its own move broadcast: %x", fixture.moverBuf.write.Bytes())
			}
		})
	}
}

// 修复档与 legacy 档在同一输入下的差异**只有** body[7:9]：这就是"逐字节可回退"的含义。
func TestTownPeerMoveBroadcastFixDiffersFromLegacyByExactlyTwoBytes(t *testing.T) {
	collect := func(switchValue string) []byte {
		t.Setenv(townPeerMotionStateEnv, switchValue)
		fixture := newTownPeerMotionStateFixture(t)
		if err := fixture.service.handleTownSetUserPosition(
			fixture.mover,
			buildTownPositionRequest(809, 410, 5, 225),
		); err != nil {
			t.Fatalf("op35 report: %v", err)
		}
		drainTownPeerTest(t, fixture.service, fixture.observer)
		drainTownPeerTest(t, fixture.service, fixture.mover)
		packet, _ := splitCurrentGameServerUpperPacketAuto(t, fixture.observerBuf.write.Bytes())
		return append([]byte(nil), packet.Body...)
	}
	fixed := collect("")
	reverted := collect("legacy")
	if len(fixed) != len(reverted) {
		t.Fatalf("length changed: fixed=%d legacy=%d", len(fixed), len(reverted))
	}
	if !bytes.Equal(fixed[:7], reverted[:7]) {
		t.Fatalf("bytes other than body[7:9] changed: fixed=%x legacy=%x", fixed, reverted)
	}
	if binary.LittleEndian.Uint16(fixed[7:9]) != 225 || binary.LittleEndian.Uint16(reverted[7:9]) != 100 {
		t.Fatalf("trailing u16 fixed=%d legacy=%d want 225/100",
			binary.LittleEndian.Uint16(fixed[7:9]), binary.LittleEndian.Uint16(reverted[7:9]))
	}
	if bytes.Equal(fixed, reverted) {
		t.Fatal("the fix is not observable: fixed and legacy bytes are identical")
	}
}

// 服务端自己的坐标投影（op36 过图收尾 / 登录落点，两端都只发给本人）不受本开关影响：
// 那条路径上没有客户端上报过 motionState，100 是唯一已知的安全值。
func TestServerOwnTownPositionProjectionIgnoresThePeerMotionStateSwitch(t *testing.T) {
	for _, switchValue := range []string{"", "legacy"} {
		t.Setenv(townPeerMotionStateEnv, switchValue)
		service, session, _ := newTownMoveTest(t)
		connection := session.conn.(*bufferConn)
		connection.write.Reset()
		if err := service.sendCurrentTownLocationNotifications(
			session,
			29,
			38,
			2,
			currentSceneTransitionRow{
				ObjectOrResourceKey: 29,
				Value1:              474,
				Value2:              234,
				Value3:              5,
				Value4:              3,
			},
			"test_own_projection",
		); err != nil {
			t.Fatalf("switch %q projection: %v", switchValue, err)
		}
		packet, _ := splitCurrentGameServerUpperPacketAuto(t, connection.write.Bytes())
		want := []byte{29, 0, 218, 1, 234, 0, 5, 100, 0}
		if packet.Header.MsgID != currentTownUserPositionNotificationMsgID ||
			!bytes.Equal(packet.Body, want) {
			t.Fatalf("switch %q self projection noti16 header=%+v body=%x want=%x",
				switchValue, packet.Header, packet.Body, want)
		}
	}
}
