package dnfbridge

import (
	"bytes"
	"encoding/binary"
	"testing"
	"time"

	"longheng.io/server/internal/modules/dnf/channelcatalog"
	"longheng.io/server/internal/modules/dnf/channelinfo"
	"longheng.io/server/internal/modules/dnf/dnfenum"
	dnfproto "longheng.io/server/internal/modules/dnf/protocol"
)

type pvpChannelInfoPacket struct {
	cmd   byte
	msgID uint16
	body  []byte
}

func splitPVPChannelInfoFixed15Packet(t *testing.T, data []byte) (pvpChannelInfoPacket, []byte) {
	t.Helper()
	packet, rest := splitGameServerUpperPacketWithHeader(t, data, dnfproto.ChannelHeaderSize)
	if packet.Header.Classification != 1 || packet.Header.MsgID != uint16(dnfenum.CmdPacketPVPChannelInfo) {
		t.Fatalf("决斗入口应答头=%+v", packet.Header)
	}
	return pvpChannelInfoPacket{cmd: 1, msgID: packet.Header.MsgID, body: packet.Body}, rest
}

func pvpChannelInfoTestService(t *testing.T) *Service {
	t.Helper()
	data := []byte("[dungeon]\n`[pvp]` `arena`\n[/dungeon]\n[server] 1\n" +
		"21 `normal` 1 `[cain]` 0 ``\n" +
		"501 `arena1` 24 `[pvp]` 0 ``\n[/server]\n")
	index, err := channelinfo.Parse(data)
	if err != nil {
		t.Fatalf("parse pvp channel fixture: %v", err)
	}
	catalog, err := channelcatalog.New(index, channelcatalog.Options{ServerID: 1})
	if err != nil {
		t.Fatalf("build pvp channel catalog: %v", err)
	}
	return &Service{catalog: catalog, options: options{gameUpperHeader: gameUpperHeaderChannel13, gameUpperBodyCodec: gameUpperBodyCodecPlain}}
}

func TestPVPChannelInfoReturnsCurrentHeaderSuccessOnLegacyAndUpperRoutes(t *testing.T) {
	for _, route := range []string{"legacy", "upper"} {
		t.Run(route, func(t *testing.T) {
			service := pvpChannelInfoTestService(t)
			conn := &bufferConn{}
			channel := channelcatalog.Channel{ServerID: 1, ID: 21, Type: 1, Group: "cain", Name: "ch.21", Port: 10021}
			session := &gameSession{conn: conn, channel: channel}
			if route == "legacy" {
				if err := service.handleGameCommand(session, byte(dnfenum.GameCmdCommand), uint16(dnfenum.CmdPacketPVPChannelInfo), nil); err != nil {
					t.Fatalf("handle legacy pvp info: %v", err)
				}
			} else {
				request, err := dnfproto.BuildChannelPacket(uint16(dnfenum.CmdPacketPVPChannelInfo), nil, 0, dnfproto.DefaultChannelClassification)
				if err != nil {
					t.Fatal(err)
				}
				if err := service.handleGameUpper(session, request); err != nil {
					t.Fatalf("handle upper pvp info: %v", err)
				}
			}
			response, rest := splitPVPChannelInfoFixed15Packet(t, conn.write.Bytes())
			if len(rest) != 0 || (len(response.body) != 30 || !bytes.Equal(response.body[:6], []byte{1, 0, 0, 0, 0, 2}) || binary.LittleEndian.Uint32(response.body[6:]) != 21 || binary.LittleEndian.Uint32(response.body[18:]) != 501 || binary.LittleEndian.Uint32(response.body[26:]) != 1) {
				t.Fatalf("pvp success body=%x rest=%x", response.body, rest)
			}
		})
	}
}

func TestPVPChannelInfoRejectsMalformedBodyAndUnavailableService(t *testing.T) {
	service := pvpChannelInfoTestService(t)
	conn := &bufferConn{}
	session := &gameSession{conn: conn, channel: channelcatalog.Channel{ID: 21}}
	if err := service.handlePVPChannelInfo(session, []byte{1}, "test"); err != nil {
		t.Fatalf("handle malformed pvp info: %v", err)
	}
	response, rest := splitPVPChannelInfoFixed15Packet(t, conn.write.Bytes())
	if len(rest) != 0 || !bytes.Equal(response.body, []byte{0, 0x15}) {
		t.Fatalf("malformed pvp response body=%x rest=%x", response.body, rest)
	}

	conn.write.Reset()
	service.catalog = nil
	if err := service.handlePVPChannelInfo(session, nil, "test"); err != nil {
		t.Fatalf("handle unavailable pvp info: %v", err)
	}
	response, rest = splitPVPChannelInfoFixed15Packet(t, conn.write.Bytes())
	if len(rest) != 0 || !bytes.Equal(response.body, []byte{0, 0x15}) {
		t.Fatalf("unavailable pvp response body=%x rest=%x", response.body, rest)
	}
}

func TestPVPChannelUsesResidentNoticeAndDedicatedLoginEnvironment(t *testing.T) {
	service := residentNoticeTestService(t)
	conn := &bufferConn{}
	pvp := channelcatalog.Channel{ServerID: 1, ID: 42, Type: 24, Group: "pvp", Name: "ch.42", Port: 10042}
	session := &gameSession{
		conn:            conn,
		accountID:       "dnf:1",
		channel:         pvp,
		residentChannel: pvp,
	}
	if err := service.sendGameConnectionBootstrap(session, testResidentNoticeTime()); err != nil {
		t.Fatalf("send pvp bootstrap: %v", err)
	}
	packet, rest := splitGameServerUpperPacketWithHeader(t, conn.write.Bytes(), service.gameUpperHeaderSize())
	if packet.Header.MsgID != 1 || packet.Header.Classification != 0 {
		t.Fatalf("pvp bootstrap notice = %+v", packet.Header)
	}
	noticeBody := decodeCurrentEXEChannelNoticeBody(packet.Body)
	if len(noticeBody) < 4 || int(binary.LittleEndian.Uint32(noticeBody[:4])) != len(noticeBody)-4 {
		t.Fatalf("pvp channel notice body=%x", packet.Body)
	}
	varints, _ := consumeChannelInfoProto(t, noticeBody[4:])
	if got := varints[8]; len(got) != 1 || got[0] != 42 {
		t.Fatalf("pvp channel field 8=%v, want [42]", got)
	}
	if len(rest) != 0 || !session.currentChannelResidentNoticeSent || session.gameEndpointSuccessSent {
		t.Fatalf("pvp bootstrap state notice=%t success=%t rest=%x", session.currentChannelResidentNoticeSent, session.gameEndpointSuccessSent, rest)
	}
	// A PVP channel takes the ordinary committed-owner path. It used to be
	// pinned to the neutral context 0 because the published ids were 501+ and
	// could not fit the u8 town-actor-owner field; that made every remote
	// co-presence actor unbuildable (sendTownRemoteActorState refuses an
	// uncommitted owner), so two characters in the same arena never saw each
	// other. The ids are inside the byte now, so the owner must be committed.
	if session.connectionTownActorOwnerChannel != byte(session.channel.ID) ||
		session.townActorOwnerChannel != byte(session.channel.ID) {
		t.Fatalf("pvp actor owner contexts = connection:%d current:%d, want %d",
			session.connectionTownActorOwnerChannel,
			session.townActorOwnerChannel,
			byte(session.channel.ID))
	}

	noticeWireLen := conn.write.Len()
	request, err := dnfproto.BuildChannelPacket(uint16(dnfenum.UpperMsgGameEndpoint), make([]byte, currentChannelReconnectDisplayProbeSize), 1, dnfproto.DefaultChannelClassification)
	if err != nil {
		t.Fatal(err)
	}
	if err := service.handleGameUpper(session, request); err != nil {
		t.Fatalf("handle pvp endpoint request: %v", err)
	}
	endpoint, trailing := splitGameServerUpperPacketWithHeader(t, conn.write.Bytes()[noticeWireLen:], service.gameUpperHeaderSize())
	if endpoint.Header.MsgID != uint16(dnfenum.UpperMsgGameEndpoint) || len(endpoint.Body) < 4 || endpoint.Body[3] != pvp.Type {
		t.Fatalf("pvp endpoint = %+v body=%x trailing=%x", endpoint.Header, endpoint.Body, trailing)
	}
	// 共存拍卖模块时，握手成功后允许且仅允许其普通入口通知。
	if len(trailing) > 0 {
		notice, rest := splitGameServerUpperPacketWithHeader(t, trailing, service.gameUpperHeaderSize())
		if notice.Header.Classification != 0 || notice.Header.MsgID != 183 || !bytes.Equal(notice.Body, []byte{0, 1}) {
			t.Fatalf("决斗握手后的未知通知=%+v body=%x", notice.Header, notice.Body)
		}
		trailing = rest
	}
	if len(trailing) != 0 {
		t.Fatalf("pvp endpoint trailing bytes=%x", trailing)
	}
}

func TestPVPLoginEnvironmentPreservesNativeChannelType(t *testing.T) {
	s := residentNoticeTestService(t)
	s.options.gameOuterToken = 1234
	for _, channelType := range []byte{8, 13, 24, 31, 35, 41} {
		channel := channelcatalog.Channel{ServerID: 1, ID: 501, Type: channelType, Group: "pvp", Port: 10501}
		body := upperSuccessBody(s.buildLoginSuccess(channel))
		if len(body) < 8 || body[3] != channelType || binary.LittleEndian.Uint32(body[5:]) != 1234 {
			t.Fatalf("channel type %d became login environment %x", channelType, body)
		}
	}
}

func testResidentNoticeTime() (now time.Time) {
	return time.Unix(1_721_020_000, 0)
}
