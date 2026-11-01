package dnfbridge

import (
	"bytes"
	"context"
	"encoding/binary"
	"os"
	"testing"
	"time"

	bp "longheng.io/server/internal/modules/dnf/battlepass"
	repo "longheng.io/server/internal/modules/dnf/repository"
	platformpvf "longheng.io/server/internal/platform/pvf"
)

// 从游戏实际分派入口进入，而非直接调用战令解码器或业务处理函数。
func TestBattlePassCompactRoute(t *testing.T) {
	path := os.Getenv("BATTLEPASS_REAL_PVF")
	if path == "" {
		t.Skip("需显式提供配套PVF")
	}
	a, err := platformpvf.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	catalog, err := bp.Load(a)
	if err != nil {
		t.Fatal(err)
	}
	digest := catalog.WireDigest()
	t.Setenv("DNFBRIDGE_BATTLEPASS_START", "2026-10-29T06:00:00+08:00")
	t.Setenv("DNFBRIDGE_BATTLEPASS_SEASON", "20261029")
	request := make([]byte, 64)
	copy(request, "BPQ3")
	binary.LittleEndian.PutUint32(request[4:], 1)
	binary.LittleEndian.PutUint32(request[8:], 4)
	copy(request[32:], digest[:])
	setup := func(t *testing.T) (*Service, *gameSession, *playerStatsObservedActivity, *playerStatsWireConn) {
		s, x, activity, conn := playerStatsGameFixture(t)
		s.battlePassCatalog = catalog
		s.battlePassFingerprint = "分派专项"
		s.gameplayTimers = &processGameplayTimeQueue{now: func() time.Time { return time.Date(2026, 10, 30, 21, 40, 0, 0, time.FixedZone("CN", 28800)) }}
		g, _ := s.repositoryProvider()
		if e := g.Account.Save(context.Background(), repo.AccountRecord{AccountID: "a"}); e != nil {
			t.Fatal(e)
		}
		return s, x, activity, conn
	}
	receive := func(t *testing.T, c *playerStatsWireConn, magic string, status byte, size int) []byte {
		t.Helper()
		select {
		case packet := <-c.packets:
			if len(packet) != 36+size || packet[0] != 1 || binary.LittleEndian.Uint16(packet[1:3]) != battlePassGameOpcode || int(binary.LittleEndian.Uint32(packet[3:7])) != len(packet) || packet[15] != 0 || string(packet[16:20]) != magic || binary.LittleEndian.Uint32(packet[20:24]) != 1 || binary.LittleEndian.Uint32(packet[24:28]) != 4 || packet[28] != status || binary.LittleEndian.Uint32(packet[32:36]) != uint32(size) {
				t.Fatalf("真实分派回包不符：%x", packet[:min(len(packet), 48)])
			}
			return packet[36:]
		case <-time.After(4 * time.Second):
			t.Fatal("游戏入口没有返回战令状态")
		}
		return nil
	}
	for _, version := range []string{"BPQ2", "BPQ3"} {
		t.Run(version, func(t *testing.T) {
			s, x, activity, conn := setup(t)
			body := append([]byte(nil), request...)
			copy(body, version)
			size, magic := 360, "BPR3"
			if version == "BPQ2" {
				body = body[:32]
				size = 10240
				magic = "BPR2"
			}
			battlePassPreviewCall(t, s, x, 1, body)
			data := receive(t, conn, magic, 0, size)
			if binary.LittleEndian.Uint32(data[4:]) != 20261029 || binary.LittleEndian.Uint32(data[20:]) != 1 || binary.LittleEndian.Uint32(data[28:]) != 1300 || binary.LittleEndian.Uint32(data[44:]) != 26 || activity.reads.Load() != 3 {
				t.Fatal("未执行完整战令状态查询")
			}
			if version == "BPQ3" && (string(data[:4]) != "BPA6" || !bytes.Equal(data[96:128], digest[:]) || binary.LittleEndian.Uint16(data[128:]) != uint16(catalog.Daily[1][0])) {
				t.Fatal("紧凑快照或当日任务不符")
			}
			if !x.battlePassLast.IsZero() {
				t.Fatal("业务请求误入预览分支")
			}
		})
	}
	t.Run("配置不一致", func(t *testing.T) {
		s, x, activity, conn := setup(t)
		body := append([]byte(nil), request...)
		body[32] ^= 1
		battlePassPreviewCall(t, s, x, 1, body)
		receive(t, conn, "BPR3", 10, 0)
		if activity.reads.Load() != 0 {
			t.Fatal("配置拒绝前读取了玩家进度")
		}
	})
	t.Run("错误分类和长度", func(t *testing.T) {
		s, x, activity, conn := setup(t)
		battlePassPreviewCall(t, s, x, 0, request)
		for _, n := range []int{0, 3, 4, 32, 63, 65} {
			body := make([]byte, n)
			copy(body, request)
			battlePassPreviewCall(t, s, x, 1, body)
		}
		if len(conn.packets) != 0 || activity.reads.Load() != 0 || !x.battlePassBusiness.last.IsZero() {
			t.Fatal("错误请求进入业务查询")
		}
	})
	t.Logf("真实游戏入口已覆盖BPQ2/BPQ3，BPA6=360字节，配置摘要=%x；内存仓储，未访问实际玩家数据库", digest)
}
