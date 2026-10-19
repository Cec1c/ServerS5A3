package dnfbridge

import (
	"bytes"
	"context"
	"path/filepath"
	"testing"
	"time"

	"longheng.io/server/internal/modules/dnf/channelcatalog"
	"longheng.io/server/internal/modules/dnf/raid"
)

func coldChannelLandingFixture(t *testing.T, channel channelcatalog.Channel) (*Service, *gameSession) {
	t.Helper()
	s, p, _ := newChannelReconnectTest(t)
	s.options.currentClientVersion = "official-24.9"
	s.options.gameUpperBodyCodec = gameUpperBodyCodecPlain
	s.options.channelInfoFile = filepath.Join("testdata", "channel_info.etc")
	catalog, script, err := s.loadChannelAssets()
	if err != nil {
		t.Fatal(err)
	}
	s.catalog, s.channelScript = catalog, script
	p.channel, p.residentChannel = channel, channel
	p.accountID = s.accountID()
	p.loginPacketSeen, p.accountFromToken, p.rosterRequested = true, true, true
	p.gameEndpointSuccessSent, p.currentChannelResidentNoticeSent = true, true
	return s, p
}

func TestColdSpecialLoginRehandshakesOrdinaryBeforeRosterAndSelection(t *testing.T) {
	for _, channel := range []channelcatalog.Channel{
		{ID: 187, Type: 39, Group: "tagtournament", Port: 10187},
		{ID: 23, Type: 23, Group: "anton_raid", Port: 10023},
		{ID: 181, Type: 32, Group: "luke_raid", Port: 10181},
		{ID: 184, Type: 42, Group: "fiendwar", Port: 10184},
	} {
		t.Run(channel.Group, func(t *testing.T) {
			s, p := coldChannelLandingFixture(t, channel)
			repos, _ := s.repositoryGroup()
			before, _, err := repos.Character.Load(context.Background(), "29")
			if err != nil {
				t.Fatal(err)
			}
			if err := s.sendUpperGetUserInfoBootstrap(p); err != nil {
				t.Fatal(err)
			}
			packets := splitAllCurrentGameServerUpperPackets(t, p.conn.(*bufferConn).write.Bytes())
			if len(packets) != 1 || packets[0].Header.MsgID != 1 || packets[0].Header.Classification != 0 || p.channel.ID != 10 || p.coldChannelLanding == nil || !p.coldChannelLanding.waiting {
				t.Fatal("冷登录没有在角色列表前转入普通频道", p.channel, len(packets))
			}
			assertCurrentChannelNoticePacket(t, packets[0], 0, 10)
			previous := append([]byte(nil), p.conn.(*bufferConn).write.Bytes()...)
			if err := s.sendUpperCSharpSelectInit(p, []byte{0, 0}); err != nil {
				t.Fatal(err)
			}
			if err := s.sendUpperGetUserInfoRosterBootstrapFixed16(p); err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(previous, p.conn.(*bufferConn).write.Bytes()) || p.selectedCharacterID != 0 || p.characterGeneration != 0 {
				t.Fatal("更正未完成时旧选角或隐藏列表越过了握手")
			}
			p.conn.(*bufferConn).write.Reset()
			if err := s.handleCurrentGameEndpointRequest(p, 1, currentChannelReconnectDisplayProbeSize); err != nil {
				t.Fatal(err)
			}
			packets = splitAllCurrentGameServerUpperPackets(t, p.conn.(*bufferConn).write.Bytes())
			if len(packets) < 2 || packets[0].Header.MsgID != 1 || packets[0].Header.Classification != 1 || packets[0].Body[3] != 11 || p.coldChannelLanding.waiting || !p.gameEndpointSuccessSent || p.connectionTownActorOwnerChannel != 10 {
				t.Fatalf("新LOGIN未先提交普通端点与owner：包数=%d 首包=%+v 等待=%t 成功=%t owner=%d", len(packets), packets[0], p.coldChannelLanding.waiting, p.gameEndpointSuccessSent, p.connectionTownActorOwnerChannel)
			}
			roster := false
			for _, packet := range packets[1:] {
				if packet.Header.MsgID == 2 && packet.Header.Classification == 0 {
					roster = true
				}
				if packetLooksLikeInitialTownTransition(packet) || packet.Header.MsgID == currentTownActorSceneSnapshotMsgID {
					t.Fatal("选角前提前构造了城镇")
				}
			}
			if !roster {
				t.Fatal("普通握手完成后未恢复角色列表")
			}
			p.conn.(*bufferConn).write.Reset()
			if err := s.handleCurrentGameEndpointRequest(p, 1, currentChannelReconnectDisplayProbeSize); err != nil || p.conn.(*bufferConn).write.Len() != 0 {
				t.Fatal("重复LOGIN重播握手或角色列表", err)
			}
			after, _, _ := repos.Character.Load(context.Background(), "29")
			if after.Stats["town_id"] != before.Stats["town_id"] || after.Stats["pos_x"] != before.Stats["pos_x"] {
				t.Fatal("握手更正直接修改了角色落点")
			}
		})
	}
}

func TestColdChannelCorrectionRejectsForeignLoginAndDoesNotRestartAfterSelection(t *testing.T) {
	s, p := coldChannelLandingFixture(t, channelcatalog.Channel{ID: 187, Type: 39, Group: "tagtournament", Port: 10187})
	if changed, err := s.redirectCurrentColdSpecialChannel(p, 0); !changed || err != nil {
		t.Fatal(changed, err)
	}
	p.conn.(*bufferConn).write.Reset()
	p.accountID = "foreign"
	if err := s.handleCurrentGameEndpointRequest(p, 1, currentChannelReconnectDisplayProbeSize); err == nil || p.conn.(*bufferConn).write.Len() != 0 || !p.coldChannelLanding.waiting {
		t.Fatal("更正中接受了另一个账号", err)
	}
	s, p = coldChannelLandingFixture(t, channelcatalog.Channel{ID: 187, Type: 39, Group: "tagtournament", Port: 10187})
	p.characterGeneration = 1 // 与真实startGameSessionEvents一致。
	s.bindGameSessionCharacter(p, 29)
	p.selectedCharacterID = 0 // 同一连接返回选角页不属于重新启动登录。
	if p.characterGeneration <= 1 {
		t.Fatal("必须使用角色绑定后的实际世代")
	}
	if changed, err := s.redirectCurrentColdSpecialChannel(p, 0); changed || err != nil || p.conn.(*bufferConn).write.Len() != 0 {
		t.Fatal("已进入过场景的连接重播了频道握手", changed, err)
	}
}

func TestManualSpecialChannelSwitchKeepsSelectedTarget(t *testing.T) {
	s, p := coldChannelLandingFixture(t, channelcatalog.Channel{ID: 187, Type: 39, Group: "tagtournament", Port: 10187})
	p.rosterRequested, p.channelReconnect = false, true
	if changed, err := s.redirectCurrentColdSpecialChannel(p, 29); changed || err != nil || p.channel.ID != 187 || p.conn.(*bufferConn).write.Len() != 0 {
		t.Fatal("主动换入帝国被误当重新登录", changed, err)
	}
}

func TestColdRaidLoginPreservesOnlyExactUnexpiredActiveSeat(t *testing.T) {
	for _, bad := range []string{"有效", "第一阶段休整", "过期", "其他账号", "其他实例", "其他频道", "已失败", "已通关", "未开团", "选择其他角色"} {
		t.Run(bad, func(t *testing.T) {
			s, p := coldChannelLandingFixture(t, channelcatalog.Channel{ID: 184, Type: 42, Group: "fiendwar", Port: 10184})
			now := s.gameplayNow()
			state := &runtimeRaidState{RaidKey: 1, Started: true, Members: []runtimeRaidMemberState{{CharID: 29}}, Battle: &runtimeRaidBattle{InstanceID: "1234", ChannelID: 184, ChannelType: 42, MemberAccounts: map[uint16]string{29: p.accountID}}}
			s.raids = map[uint32]*runtimeRaidState{1: state}
			seat := currentRaidDisconnectSeat{Raid: state, RaidKey: 1, CharacterID: 29, AccountID: p.accountID, InstanceID: "1234", DisconnectedAt: now.Add(-time.Minute), ExpiresAt: now.Add(time.Minute)}
			id := uint16(29)
			switch bad {
			case "第一阶段休整":
				state.Battle.Mode, state.Battle.PhaseCompleted = 1, true
				state.Battle.Anton = &raid.AntonProgression{}
				state.Battle.Configuration.PhaseLimitSeconds = []uint32{100, 100}
			case "过期":
				seat.ExpiresAt = now
			case "其他账号":
				seat.AccountID = "foreign"
			case "其他实例":
				seat.InstanceID = "old"
			case "其他频道":
				state.Battle.ChannelID++
			case "已失败":
				state.Battle.TimedOut = true
			case "已通关":
				state.Battle.PhaseCompleted = true
			case "未开团":
				state.Started = false
			case "选择其他角色":
				id++
			}
			s.currentRaidDisconnects = map[uint16]currentRaidDisconnectSeat{29: seat}
			changed, err := s.redirectCurrentColdSpecialChannel(p, id)
			if err != nil || changed != (bad != "有效" && bad != "第一阶段休整") {
				t.Fatal("断线席位判定不符", bad, changed, err)
			}
		})
	}
}
