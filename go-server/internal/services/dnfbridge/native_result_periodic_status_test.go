package dnfbridge

import (
	"bytes"
	"fmt"
	"testing"
	"time"

	onlineevent "longheng.io/server/internal/modules/dnf/onlineevent"
)

// 现场21件炼金提取在第5、6件后被1386、561拆开。覆盖三个原生入口、
// 两种已配套头及保护层，要求完整保留应答和活动状态的原始发送顺序。
func TestNativeResultPeriodicStatusDoesNotSplit21Requests(t *testing.T) {
	t.Setenv("DNFBRIDGE_NATIVE_RESULT_DELIVERY", "on")
	for _, op := range []uint16{26, 235, 416} {
		for _, header := range []string{gameUpperHeaderChannel13, gameUpperHeaderServer16} {
			for _, protected := range []bool{false, true} {
				t.Run(fmt.Sprintf("入口%d/%s/保护%t", op, header, protected), func(t *testing.T) {
					s, p, _ := newChannelReconnectTest(t)
					s.options.currentClientVersion, s.options.gameUpperHeader = "official-24.9", header
					p.selectedCharacterID = 19
					c := &channelSceneWriteConn{}
					p.conn = c
					provider := &testDprotoSession{}
					if protected {
						p.dproto = provider
					}
					type expectedPacket struct {
						op    uint16
						class byte
						body  []byte
					}
					var expected []expectedPacket
					send := func(op uint16, body []byte, class byte) {
						t.Helper()
						expected = append(expected, expectedPacket{op, class, append([]byte(nil), body...)})
						if err := s.sendGameUpperRawClass(p, op, body, class); err != nil {
							t.Fatal(err)
						}
					}
					for i := byte(0); i < 21; i++ {
						slot := i + 10
						body := []byte{2, 120, 0, 0, slot, 0}
						if op == 26 {
							body = currentDisjointItemTestBody(int16(slot), 0, -1, 317)
						} else if op == 235 {
							body = []byte{19, 0, slot, 0, 0}
						}
						if err := s.prepareNativeResultDelivery(p, nativeResultTestPacket(op, body)); err != nil {
							t.Fatal(err)
						}
						send(op, []byte{1, 0, slot, 0, 0}, 1)
						if i == 4 {
							send(1386, buildCurrentOrdinaryAttendanceBody(onlineevent.OrdinarySnapshot{Days: 15, Claimed: 11, CanClaim: true}), 0)
						}
						if i == 5 {
							send(currentPCRoomTimePointStateID, buildCurrentPCRoomTimePointBody(onlineevent.PCRoomTimePointSnapshot{Seconds: 13680, Total: 11, DailyMask: 15, PeriodMask: 3}), 0)
						}
						if len(c.writes) != 0 {
							t.Fatalf("第%d件后被定时状态拆开结果，写出%d次", i+1, len(c.writes))
						}
					}
					if err := s.finishNativeResultDelivery(p, "连续请求处理完毕"); err != nil {
						t.Fatal(err)
					}
					if len(c.writes) != 1 {
						t.Fatal("结果没有一次集中写出", len(c.writes))
					}
					wire := c.write.Bytes()
					for i, want := range expected {
						inner, rest := wire, []byte(nil)
						if protected {
							outer, trailing := splitCurrentGameServerUpperPacketAuto(t, wire)
							inner, rest = outer.Body, trailing
						}
						packet, trailing := splitGameServerUpperPacketWithHeader(t, inner, s.gameUpperHeaderSize())
						if !protected {
							rest = trailing
						}
						if packet.Header.MsgID != want.op || packet.Header.Classification != want.class || !bytes.Equal(packet.Body, want.body) {
							t.Fatal("结果或活动状态顺序、内容改变", i, packet, want)
						}
						wire = rest
					}
					if len(wire) != 0 || protected && len(provider.encodeInputs) != len(expected) {
						t.Fatal("消息或保护层编码次数改变")
					}
				})
			}
		}
	}
}

func TestNativeResultPeriodicStatusOutsideBatchIsImmediate(t *testing.T) {
	for _, op := range []uint16{1386, currentPCRoomTimePointStateID} {
		s, p, _ := newChannelReconnectTest(t)
		c := &channelSceneWriteConn{}
		p.conn = c
		body := buildCurrentOrdinaryAttendanceBody(onlineevent.OrdinarySnapshot{})
		if op == currentPCRoomTimePointStateID {
			body = buildCurrentPCRoomTimePointBody(onlineevent.PCRoomTimePointSnapshot{})
		}
		if err := s.sendGameUpperRawClass(p, op, body, 0); err != nil || len(c.writes) != 1 {
			t.Fatal("没有分解时活动状态被延迟", op, err)
		}
	}
}

func TestNativeResultPeriodicStatusRejectsOtherShape(t *testing.T) {
	for _, mode := range []string{"签到应答", "在线应答", "签到短体", "在线长体", "单件停滞四秒"} {
		t.Run(mode, func(t *testing.T) {
			s, p, _ := newChannelReconnectTest(t)
			s.options.currentClientVersion = "official-24.9"
			p.selectedCharacterID = 19
			c := &channelSceneWriteConn{}
			p.conn = c
			if err := s.prepareNativeResultDelivery(p, nativeResultTestPacket(416, []byte{2, 120, 0, 0, 10, 0})); err != nil {
				t.Fatal(err)
			}
			if err := s.sendGameUpperRawClass(p, 416, []byte{1}, 1); err != nil {
				t.Fatal(err)
			}
			op, class := uint16(1386), byte(0)
			body := buildCurrentOrdinaryAttendanceBody(onlineevent.OrdinarySnapshot{})
			switch mode {
			case "签到应答":
				class = 1
			case "在线应答":
				op, class, body = currentPCRoomTimePointStateID, 1, buildCurrentPCRoomTimePointBody(onlineevent.PCRoomTimePointSnapshot{})
			case "签到短体":
				body = body[:8]
			case "在线长体":
				op, body = currentPCRoomTimePointStateID, make([]byte, 18)
			case "单件停滞四秒":
				p.nativeResultDelivery.lastProgress = time.Now().Add(-4 * time.Second)
			}
			if err := s.sendGameUpperRawClass(p, op, body, class); err != nil || len(c.writes) != 2 || p.nativeResultDelivery != nil {
				t.Fatal("不符合边界的通知被延迟", mode, len(c.writes), err)
			}
		})
	}
}
