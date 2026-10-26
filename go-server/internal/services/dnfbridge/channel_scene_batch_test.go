package dnfbridge

import (
	"bytes"
	"encoding/binary"
	"errors"
	"io"
	"testing"

	"longheng.io/server/internal/modules/dnf/channelcatalog"
	dnfproto "longheng.io/server/internal/modules/dnf/protocol"
)

type channelSceneWriteConn struct {
	bufferConn
	writes [][]byte
	closed bool
	short  bool
}

func TestSceneBatchPreservesDprotoEncodingAndControlOrder(t *testing.T) {
	s, p, _ := newChannelReconnectTest(t)
	s.options.currentClientVersion = "official-24.9"
	p.channelReconnect = true
	c := &channelSceneWriteConn{}
	p.conn = c
	provider := &testDprotoSession{}
	p.dproto = provider
	started, err := s.beginCurrentOrdinaryChannelSceneBatch(p)
	if !started || err != nil {
		t.Fatal(started, err)
	}
	var inputs [][]byte
	for _, op := range []uint16{22, 23} {
		inner, e := dnfproto.BuildChannelPacket(op, []byte{1, 2, 3}, 7, 0)
		if e != nil {
			t.Fatal(e)
		}
		inputs = append(inputs, inner)
		if e = s.writeGameUpperPacket(p, inner); e != nil {
			t.Fatal(e)
		}
	}
	control, err := dnfproto.BuildChannelPacket(dnfproto.DprotoServerControlOpcode, []byte{4, 5}, 8, 1)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.withGameWire(p, func() error { return s.writeDprotoProviderPacketsLocked(p, [][]byte{control}, "测试保护控制") }); err != nil {
		t.Fatal(err)
	}
	if len(c.writes) != 0 || len(provider.encodeInputs) != 2 {
		t.Fatal("保护层未按原时序编码或提前发送")
	}
	if err = s.finishCurrentOrdinaryChannelSceneBatch(p, nil); err != nil {
		t.Fatal(err)
	}
	if len(c.writes) != 1 {
		t.Fatal("已编码的报文没有连续提交")
	}
	wire := c.writes[0]
	for i, inner := range inputs {
		if !bytes.Equal(provider.encodeInputs[i], inner) {
			t.Fatal("保护层内层数据改变")
		}
		n := int(binary.LittleEndian.Uint32(wire[3:7]))
		packet, e := dnfproto.ParseChannelPacketUnchecked(wire[:n])
		if e != nil || packet.Header.MsgID != dnfproto.DprotoServerEnvelopeOpcode || !bytes.Equal(packet.Body, inner) {
			t.Fatal("保护信封、正文或顺序改变", e)
		}
		wire = wire[n:]
	}
	if !bytes.Equal(wire, control) {
		t.Fatal("保护控制报文顺序或字节发生变化")
	}
}

func (c *channelSceneWriteConn) Write(p []byte) (int, error) {
	c.writes = append(c.writes, append([]byte(nil), p...))
	if c.short {
		return len(p) - 1, nil
	}
	return c.bufferConn.Write(p)
}

func (c *channelSceneWriteConn) Close() error { c.closed = true; return nil }

func TestOrdinaryReconnectDoesNotPublishStreetAckBeforeCompleteRoom(t *testing.T) {
	s, p, _ := newChannelReconnectTest(t)
	s.options.currentClientVersion = "official-24.9"
	s.options.gameUpperBodyCodec = gameUpperBodyCodecPlain
	p.channelReconnect = true
	c := &channelSceneWriteConn{}
	p.conn = c
	if err := s.sendUpperCSharpSelectInit(p, []byte{0, 0}); err != nil {
		t.Fatal(err)
	}
	if len(c.writes) != 1 || c.closed || p.channelSceneBatch != nil {
		t.Fatal("普通换线把ACK与房间分开写入连接", len(c.writes), c.closed)
	}
	out := splitAllCurrentGameServerUpperPackets(t, c.writes[0])
	ack, area, inventory, skills, town := -1, -1, -1, -1, -1
	for i, packet := range out {
		if packet.Header.Classification == 1 && packet.Header.MsgID == 4 {
			ack = i
		}
		if packet.Header.Classification == 0 {
			switch packet.Header.MsgID {
			case 13:
				inventory = i
			case 19:
				skills = i
			case 23:
				area = i
				if len(packet.Body) != 10 || packet.Body[2] != 39 || packet.Body[3] != 2 {
					t.Fatal("房间不是本城镇赛丽亚", packet.Body)
				}
			}
		}
		if packetLooksLikeInitialTownTransition(packet) {
			town = i
		}
	}
	if ack < 0 || area <= ack || inventory < ack || skills < ack || town <= area || town <= inventory || town <= skills {
		t.Fatalf("合并字节流缺少完整选角/房间链或破坏角色资料在最终op24之前的顺序：ack=%d area=%d items=%d skills=%d room=%d", ack, area, inventory, skills, town)
	}
}

func TestSceneBatchFailureDoesNotSendPartialInitialization(t *testing.T) {
	for _, stage := range []string{"准备失败", "发送短写", "超过上限"} {
		t.Run(stage, func(t *testing.T) {
			s, p, _ := newChannelReconnectTest(t)
			s.options.currentClientVersion = "official-24.9"
			p.channelReconnect = true
			c := &channelSceneWriteConn{short: stage == "发送短写"}
			p.conn = c
			started, err := s.beginCurrentOrdinaryChannelSceneBatch(p)
			if !started || err != nil {
				t.Fatal(started, err)
			}
			if err = s.writeGameRawPacket(p, []byte{1, 2, 3}); err != nil {
				t.Fatal(err)
			}
			if len(c.writes) != 0 {
				t.Fatal("尚未完成就写出了不完整场景")
			}
			var original error
			if stage == "准备失败" {
				original = errors.New("模拟角色资料读取失败")
			}
			if stage == "超过上限" {
				p.wireMu.Lock()
				p.channelSceneBatch.wire = make([]byte, currentChannelSceneBatchLimit)
				p.wireMu.Unlock()
				if s.writeGameRawPacket(p, []byte{4}) == nil {
					t.Fatal("超限未拒绝")
				}
			}
			err = s.finishCurrentOrdinaryChannelSceneBatch(p, original)
			if err == nil || !c.closed || p.channelSceneBatch != nil {
				t.Fatal("失败批次未终止连接及清除状态", err, c.closed)
			}
			if stage == "发送短写" {
				if !errors.Is(err, io.ErrShortWrite) || len(c.writes) != 1 {
					t.Fatal(err, len(c.writes))
				}
			} else if len(c.writes) != 0 {
				t.Fatal("准备失败仍提交了部分字节流")
			}
		})
	}
}

func TestSceneBatchKeepsColdAndSpecialChannelTransportUnchanged(t *testing.T) {
	for _, row := range []struct {
		name      string
		channel   channelcatalog.Channel
		reconnect bool
	}{
		{"普通冷登录", channelcatalog.Channel{ID: 10, Type: 11, Group: "granfloris"}, false},
		{"传统决斗", channelcatalog.Channel{ID: 75, Type: 8, Group: "pvp"}, true},
		{"无双决斗", channelcatalog.Channel{ID: 192, Type: 24, Group: "pvp"}, true},
		{"帝国", channelcatalog.Channel{ID: 187, Type: 39, Group: "tagtournament"}, true},
		{"超时空", channelcatalog.Channel{ID: 184, Type: 42, Group: "fiendwar"}, true},
	} {
		t.Run(row.name, func(t *testing.T) {
			s, p, _ := newChannelReconnectTest(t)
			s.options.currentClientVersion = "official-24.9"
			p.channel = row.channel
			p.channelReconnect = row.reconnect
			c := &channelSceneWriteConn{}
			p.conn = c
			if started, err := s.beginCurrentOrdinaryChannelSceneBatch(p); started || err != nil {
				t.Fatal("改变了非普通换线的传输边界", started, err)
			}
			if err := s.writeGameRawPacket(p, []byte{1, 2, 3}); err != nil || len(c.writes) != 1 {
				t.Fatal("非普通换线被缓冲", err, len(c.writes))
			}
		})
	}
}
