package dnfbridge

import (
	"context"
	"os"
	"reflect"
	"testing"
	"time"

	pvp "longheng.io/server/internal/modules/dnf/pvp"
	repo "longheng.io/server/internal/modules/dnf/repository"
	platformpvf "longheng.io/server/internal/platform/pvf"
)

func TestRealPVFDailyVarietyAndItemRewardTransaction(t *testing.T) {
	path := os.Getenv("DNFBRIDGE_REAL_PVF_SMOKE")
	if path == "" {
		t.Skip("需要现用真实PVF")
	}
	a, err := platformpvf.LoadArchive(platformpvf.Options{Path: path})
	if err != nil {
		t.Fatal(err)
	}
	c, err := pvp.Load(a)
	if err != nil {
		t.Fatal(err)
	}
	if c.Pools[2][0].ID != 131 || c.Pools[2][0].Target != 2 || c.Pools[2][0].Parameter != 3 || c.Pools[2][2].ID != 133 || c.Pools[2][2].Target != 1 || c.Pools[2][2].Parameter != 3 {
		t.Fatal("现用每日连胜/姿态数值改变")
	}
	s := pvpRoomTestService(t)
	s.pvpDailyCatalog = c
	s.pvfItemCatalog, err = newPVFDungeonDropCatalog(a)
	if err != nil {
		t.Fatal(err)
	}
	r, _ := s.repositoryGroup()
	ctx := context.Background()
	now := s.gameplayNow()
	seen := [4]map[uint16]bool{{}, {}, {}, {}}
	for day := 0; day < 40; day++ {
		at := now.Add(time.Duration(day) * 24 * time.Hour)
		state, err := pvp.Prepare(ctx, r, c, "pvp-29", "29", at)
		if err != nil {
			t.Fatal(err)
		}
		retry, err := pvp.Prepare(ctx, r, c, "pvp-29", "29", at.Add(time.Second))
		if err != nil || !reflect.DeepEqual(state.Rows, retry.Rows) {
			t.Fatal("同日重登重抽任务", err)
		}
		for tier, row := range state.Rows {
			seen[tier][row.ID] = true
		}
	}
	for tier := 0; tier < 3; tier++ {
		if len(seen[tier]) < 2 {
			t.Fatal("前三个每日池未变化", tier, seen)
		}
	}
	if !reflect.DeepEqual(seen[3], map[uint16]bool{134: true}) {
		t.Fatal("第四槽超出现用PVF任务池", seen[3])
	}
	state, err := pvp.Prepare(ctx, r, c, "pvp-29", "29", now)
	if err != nil {
		t.Fatal(err)
	}
	state.Character.Stats["pvp_daily_3_progress"] = 3
	if err := r.Character.Save(ctx, state.Character); err != nil {
		t.Fatal(err)
	}
	if err := r.Inventory.Save(ctx, repo.InventoryRecord{CharacterID: "29"}); err != nil {
		t.Fatal(err)
	}
	g := &gameSession{conn: &bufferConn{}, accountID: "pvp-29", selectedCharacterID: 29}
	for retry := 0; retry < 2; retry++ {
		reward, err := s.grantCurrentPVPDailyItems(g, c)
		if err != nil {
			t.Fatal(err)
		}
		var count int64
		for _, stack := range reward.Items {
			if stack.ItemID != 10092947 {
				t.Fatal("发放了错误物品", stack)
			}
			count += stack.Count
		}
		if count != 100 || reward.Character.Stats["pvp_daily_3_paid"] != 1 || len(reward.Granted) != 1-retry {
			t.Fatal("真实物品入库/防重失败", reward)
		}
		g.conn.(*bufferConn).write.Reset()
		if err := s.sendCurrentPVPTotalRewards(g, pvp.TotalRewards{Character: reward.Character, Items: reward.Items}); err != nil {
			t.Fatal(err)
		}
		packet, rest := splitCurrentGameServerUpperPacketAuto(t, g.conn.(*bufferConn).write.Bytes())
		if packet.Header.Classification != 0 || packet.Header.MsgID != 14 || len(rest) != 0 {
			t.Fatal("物品绝对库存通知错误")
		}
	}
	t.Logf("前三池40天命中=%v，第四池仅134；真实10092947×100按PVF入库并防重。仅操作隔离内存，未直接改线上进度。", seen)
}
