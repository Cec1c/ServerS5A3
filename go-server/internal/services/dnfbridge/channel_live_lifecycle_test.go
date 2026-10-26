package dnfbridge

import (
	"context"
	"encoding/binary"
	"testing"

	"longheng.io/server/internal/modules/dnf/channelcatalog"
	dnfproto "longheng.io/server/internal/modules/dnf/protocol"
)

func TestLiveColdEmpireSessionInitialGenerationRedirectsBeforeLegacyRoster(t *testing.T) {
	s, p := coldChannelLandingFixture(t, channelcatalog.Channel{ID: 187, Type: 39, Group: "tagtournament", Port: 10187})
	p.rosterRequested = false
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := s.startGameSessionEvents(ctx, p); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.stopGameSessionEvents(context.Background(), p) })
	if p.characterGeneration != 1 || p.selectedCharacterID != 0 {
		t.Fatal("必须使用真实启动的首次会话世代", p.characterGeneration, p.selectedCharacterID)
	}
	frame := make([]byte, dnfproto.LegacyGameHeaderSize+3)
	frame[0] = 1
	binary.LittleEndian.PutUint16(frame[1:3], 8)
	binary.LittleEndian.PutUint32(frame[3:7], uint32(len(frame)))
	binary.LittleEndian.PutUint16(frame[11:13], 34)
	packets, rest, skipped, err := dnfproto.SplitLatestGameStream(frame, 65535)
	if err != nil || len(packets) != 1 || len(rest) != 0 || skipped != 0 || packets[0].Kind != dnfproto.LatestGameStreamLegacy {
		t.Fatal("现场13字节头及3字节GET_USERINFO未走真实legacy拆流", err, packets, rest, skipped)
	}
	if err = s.callGameSession(ctx, p, "复查真实帝国冷登录", func() error { return s.handleGameStreamPacket(p, packets[0]) }); err != nil {
		t.Fatal(err)
	}
	out := splitAllCurrentGameServerUpperPackets(t, p.conn.(*bufferConn).write.Bytes())
	if p.channel.ID != 10 || p.coldChannelLanding == nil || !p.coldChannelLanding.waiting || len(out) != 1 || out[0].Header.MsgID != 1 || out[0].Header.Classification != 0 {
		t.Fatalf("首次真实世代被误判成已选角，帝国冷登录仍发送旧频道角色列表：世代=%d 频道=%d 包数=%d", p.characterGeneration, p.channel.ID, len(out))
	}
}

func TestLiveColdPVPDoesNotAddEmpireRehandshake(t *testing.T) {
	for _, channel := range []channelcatalog.Channel{{ID: 75, Type: 8, Group: "pvp"}, {ID: 192, Type: 24, Group: "pvp"}} {
		s, p := coldChannelLandingFixture(t, channel)
		p.characterGeneration = 1
		if changed, err := s.redirectCurrentColdSpecialChannel(p, 0); changed || err != nil || p.coldChannelLanding != nil || p.channel.ID != channel.ID || p.channel.Type != channel.Type {
			t.Fatal("决斗既有路径被帝国冷登录修复改变", changed, err)
		}
	}
}
