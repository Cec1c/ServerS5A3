package dnfbridge

import (
	"fmt"
	"testing"
	"time"

	onlineevent "longheng.io/server/internal/modules/dnf/onlineevent"
)

func TestNativeResultLongBatchDoesNotSplitAtFourSeconds(t *testing.T) {
	for _, op := range []uint16{26, 235, 416} {
		for _, count := range []int{24, 127} {
			t.Run(fmt.Sprintf("入口%d/件数%d", op, count), func(t *testing.T) {
				s, p, _ := newChannelReconnectTest(t)
				s.options.currentClientVersion = "official-24.9"
				p.selectedCharacterID = 19
				c := &channelSceneWriteConn{}
				p.conn = c
				for i := 0; i < count; i++ {
					body := []byte{2, 44, 1, 0, byte(i + 10), 0}
					if op == 26 {
						body = currentDisjointItemTestBody(int16(i+10), 0, -1, 317)
					}
					if op == 235 {
						body = []byte{19, 0, byte(i + 10), 0, 0}
					}
					if err := s.prepareNativeResultDelivery(p, nativeResultTestPacket(op, body)); err != nil {
						t.Fatal(err)
					}
					// 不睡眠：重现现场第15件前总准备时间已超过4秒，且请求仍连续。
					p.nativeResultDelivery.started = time.Now().Add(-6 * time.Second)
					if err := s.sendGameUpperRawClass(p, op, []byte{1, byte(i)}, 1); err != nil {
						t.Fatal(err)
					}
					if i == 14 {
						if err := s.sendGameUpperRawClass(p, 1386, buildCurrentOrdinaryAttendanceBody(onlineevent.OrdinarySnapshot{}), 0); err != nil {
							t.Fatal(err)
						}
						if err := s.sendGameUpperRawClass(p, currentPCRoomTimePointStateID, buildCurrentPCRoomTimePointBody(onlineevent.PCRoomTimePointSnapshot{}), 0); err != nil {
							t.Fatal(err)
						}
					}
					if len(c.writes) != 0 {
						t.Fatal("连续请求仅因总时间被拆开", i, len(c.writes))
					}
				}
				if err := s.finishNativeResultDelivery(p, "已到达请求处理完毕"); err != nil {
					t.Fatal(err)
				}
				if len(c.writes) != 1 || p.nativeResultDelivery != nil {
					t.Fatal("连续批次未一次发出")
				}
				wire, acknowledgements, statuses := c.write.Bytes(), 0, 0
				for len(wire) > 0 {
					packet, rest := splitGameServerUpperPacketWithHeader(t, wire, s.gameUpperHeaderSize())
					wire = rest
					if packet.Header.Classification == 1 && packet.Header.MsgID == op {
						if len(packet.Body) != 2 || int(packet.Body[1]) != acknowledgements {
							t.Fatal("逐件应答顺序改变")
						}
						acknowledgements++
					} else if packet.Header.Classification == 0 && (packet.Header.MsgID == 1386 || packet.Header.MsgID == currentPCRoomTimePointStateID) {
						statuses++
					} else {
						t.Fatal("多出未知结果", packet.Header)
					}
				}
				if acknowledgements != count || statuses != 2 {
					t.Fatal("应答或状态数量改变", acknowledgements, statuses)
				}
			})
		}
	}
}

func TestNativeResultStalledItemStillReleasesPreparedResults(t *testing.T) {
	s, p, _ := newChannelReconnectTest(t)
	s.options.currentClientVersion = "official-24.9"
	p.selectedCharacterID = 19
	c := &channelSceneWriteConn{}
	p.conn = c
	packet := nativeResultTestPacket(416, []byte{2, 44, 1, 0, 10, 0})
	if err := s.prepareNativeResultDelivery(p, packet); err != nil {
		t.Fatal(err)
	}
	if err := s.sendGameUpperRawClass(p, 416, []byte{1}, 1); err != nil {
		t.Fatal(err)
	}
	p.nativeResultDelivery.lastProgress = time.Now().Add(-5 * time.Second)
	if err := s.prepareNativeResultDelivery(p, packet); err != nil {
		t.Fatal(err)
	}
	if len(c.writes) != 1 || p.nativeResultDelivery == nil || p.nativeResultDelivery.requests != 1 {
		t.Fatal("单件停滞后没有释放已准备结果")
	}
}
