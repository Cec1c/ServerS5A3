package dnfbridge

import (
	"bytes"
	"encoding/binary"
	"testing"
	"time"
)

func battlePassPreviewCall(t *testing.T, s *Service, x *gameSession, class byte, body []byte) {
	t.Helper()
	playerStatsEdit(t, s, x, func() {
		if err := s.handleGameCommand(x, class, battlePassGameOpcode, body); err != nil {
			t.Error(err)
		}
	})
}

func TestBattlePassPreviewTransport(t *testing.T) {
	s, x, activity, c := playerStatsGameFixture(t)
	request := []byte("BPQ1\x78\x56\x34\x12\x09\x00\x00\x00")
	battlePassPreviewCall(t, s, x, 1, request)
	select {
	case packet := <-c.packets:
		want := append([]byte("BPR1"), request[4:]...)
		want = append(want, 1, 0, 0, 0, 0, 0, 0, 0)
		if len(packet) != 36 || packet[0] != 1 || binary.LittleEndian.Uint16(packet[1:3]) != battlePassGameOpcode || binary.LittleEndian.Uint32(packet[3:7]) != 36 || packet[15] != 0 || !bytes.Equal(packet[16:], want) {
			t.Fatalf("战令未开放回包格式错误：%x", packet)
		}
	case <-time.After(time.Second):
		t.Fatal("未收到战令预览回包")
	}
	for i := 0; i < 10; i++ {
		battlePassPreviewCall(t, s, x, 1, request)
	}
	if len(c.packets) != 0 || activity.reads.Load() != 0 {
		t.Fatal("预览重复点击产生额外回包或仓储访问")
	}
	// 与现有统计限流独立，战令预览不占用统计工作状态。
	if !x.playerStats.last.IsZero() || x.playerStats.busy.Load() {
		t.Fatal("战令预览干扰统计查询")
	}
	playerStatsEdit(t, s, x, func() { x.battlePassLast = time.Time{} })
	battlePassPreviewCall(t, s, x, 1, request)
	if len(c.packets) != 1 {
		t.Fatal("限流到期后未恢复")
	}
}

func TestBattlePassPreviewRejectsMalformedAndIdentity(t *testing.T) {
	request := []byte("BPQ1\x78\x56\x34\x12\x09\x00\x00\x00")
	s, x, _, c := playerStatsGameFixture(t)
	for cut := 0; cut < len(request); cut++ {
		battlePassPreviewCall(t, s, x, 1, request[:cut])
	}
	battlePassPreviewCall(t, s, x, 1, append(append([]byte(nil), request...), 0))
	for _, at := range []int{0, 4, 8} {
		bad := append([]byte(nil), request...)
		clear(bad[at : at+4])
		battlePassPreviewCall(t, s, x, 1, bad)
	}
	battlePassPreviewCall(t, s, x, 0, request)
	if len(c.packets) != 0 || !x.battlePassLast.IsZero() {
		t.Fatal("错误格式或类别进入战令状态查询")
	}
	for _, condition := range []string{"未认证", "无账号", "被替换", "无角色", "无代次"} {
		t.Run(condition, func(t *testing.T) {
			s, x, _, c := playerStatsGameFixture(t)
			playerStatsEdit(t, s, x, func() {
				switch condition {
				case "未认证":
					x.accountFromToken = false
				case "无账号":
					x.accountID = ""
				case "被替换":
					x.loginRevoked.Store(true)
				case "无角色":
					x.selectedCharacterID = 0
				case "无代次":
					x.characterGeneration = 0
				}
				if err := s.handleBattlePassPreview(x, request); err != nil {
					t.Error(err)
				}
			})
			if len(c.packets) != 0 {
				t.Fatal("无效身份收到预览回包")
			}
		})
	}
}
