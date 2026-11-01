package dnfbridge

import (
	"context"
	"encoding/binary"
	"errors"
	bp "longheng.io/server/internal/modules/dnf/battlepass"
	"longheng.io/server/internal/modules/dnf/onlineevent"
	repo "longheng.io/server/internal/modules/dnf/repository"
	"reflect"
	"testing"
	"time"
)

func TestBattlePassEntryGameRouteAndSwitch(t *testing.T) {
	for _, enabled := range []bool{false, true} {
		t.Run(map[bool]string{false: "关闭", true: "开启"}[enabled], func(t *testing.T) {
			s, x, activity, conn := playerStatsGameFixture(t)
			g, _ := s.repositoryProvider()
			s.activityControls, _ = onlineevent.NewControls(context.Background(), g.Settings, []onlineevent.ControlDefinition{{ID: 2520, Name: "战令", DefaultEnabled: enabled}})
			c := &bp.Catalog{Missions: map[uint32]bp.Mission{1: {ID: 1, Target: 1, Kind: 17}}, Daily: [][4]uint32{{1, 1, 1, 1}}, Weekly: [][4]uint32{{1, 1, 1, 1}}}
			for i := 0; i < 18; i++ {
				c.Season = append(c.Season, 1)
			}
			for i := 1; i < 80; i++ {
				c.Experience[i] = 1300
			}
			c.Rewards[0][1] = []bp.Reward{{ItemID: 15, Count: 1}}
			s.battlePassCatalog = c
			s.battlePassFingerprint = "entry-fixture"
			s.gameplayTimers = &processGameplayTimeQueue{now: func() time.Time { return time.Date(2026, 10, 31, 10, 0, 0, 0, time.FixedZone("CN", 28800)) }}
			if e := g.Account.Save(context.Background(), repo.AccountRecord{AccountID: "a", Metadata: map[string]string{"keep": "yes"}}); e != nil {
				t.Fatal(e)
			}
			before, _, _ := g.Account.Load(context.Background(), "a")
			body := []byte{'B', 'P', 'E', '1', 1, 0, 0, 0, 9, 0, 0, 0}
			for _, bad := range [][]byte{body[:11], append(append([]byte(nil), body...), 0), []byte("BPE1\x00\x00\x00\x00\x09\x00\x00\x00")} {
				battlePassPreviewCall(t, s, x, 1, bad)
			}
			battlePassPreviewCall(t, s, x, 0, body)
			if len(conn.packets) != 0 || activity.reads.Load() != 0 {
				t.Fatal("无效请求进入业务")
			}
			battlePassPreviewCall(t, s, x, 1, body)
			select {
			case packet := <-conn.packets:
				flags := byte(0)
				if enabled {
					flags = 3
				}
				if len(packet) != 40 || packet[0] != 1 || binary.LittleEndian.Uint16(packet[1:3]) != battlePassGameOpcode || string(packet[16:20]) != "BPN1" || packet[28] != 0 || packet[36] != flags || binary.LittleEndian.Uint32(packet[32:]) != 4 {
					t.Fatalf("入口回包不符%x", packet)
				}
			case <-time.After(4 * time.Second):
				t.Fatal("入口无应答")
			}
			after, _, _ := g.Account.Load(context.Background(), "a")
			if !reflect.DeepEqual(before, after) {
				t.Fatal("入口查询写入账号")
			}
			if !enabled && activity.reads.Load() != 0 {
				t.Fatal("关闭后仍读取行为统计")
			}
			if !enabled {
				identity, bound, _ := s.playerStatsGameIdentity(x)
				_, _, e := s.settleBattlePassGame(context.Background(), x, identity, bound, gameBattlePassRequest{action: 4, target: 100})
				if !errors.Is(e, bp.ErrUnavailable) {
					t.Fatal("关闭后购买未拒绝", e)
				}
			}
			battlePassPreviewCall(t, s, x, 1, body)
			select {
			case packet := <-conn.packets:
				if packet[28] != 2 {
					t.Fatal("入口节流无效")
				}
			case <-time.After(time.Second):
				t.Fatal("节流无应答")
			}
		})
	}
}
