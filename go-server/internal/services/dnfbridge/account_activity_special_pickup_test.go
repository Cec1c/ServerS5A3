package dnfbridge

import (
	"context"
	"fmt"
	"testing"

	repo "longheng.io/server/internal/modules/dnf/repository"
)

// 从专用奖励登记到真实拾取事务验证，而不是只构造一条品质4事件。
func TestAccountActivitySpecialLootPickupEndToEnd(t *testing.T) {
	for _, id := range []int64{316, 7539, 6063, 6064, 6065} {
		t.Run(fmt.Sprint(id), func(t *testing.T) {
			s, runtime, repositories := prepareCurrentEpicPieceDropTest(t)
			source := s.dungeonMonsterTable.source.(bridgePVFSource)
			source["equipment/weapon/test_drop.equ"] += "[rarity]\n4\n"
			catalog, err := newPVFDungeonDropCatalog(source)
			if err != nil {
				t.Fatal(err)
			}
			runtime.Dungeon.ID = id
			scene, ok := runtime.Session.Scene()
			if !ok {
				t.Fatal("缺少当前房间")
			}
			wires, err := s.registerHellPartyRewardDrops(runtime, scene,
				hellPartyRewardRequest{dropCatalog: catalog},
				[]hellPartyRewardItem{{ItemID: 9001, Amount: 1}, {ItemID: 9001, Amount: 1}}, 99, 99)
			if err != nil || len(wires) != 2 {
				t.Fatalf("专用奖励登记失败：%d %v", len(wires), err)
			}
			query := repo.ActivityQuery{AccountID: epicPieceBookTestAccountID}
			assertCount := func(want int64) {
				t.Helper()
				summary, err := repositories.Activity.Summary(context.Background(), query)
				if err != nil || summary.Metrics[repo.ActivityEpic] != want {
					t.Fatalf("副本%d史诗统计：%+v，期望%d，错误%v", id, summary, want, err)
				}
			}
			assertCount(0)
			session := &gameSession{selectedCharacterID: 99, conn: &bufferConn{}, dungeon: dungeonSessionState{runtime: runtime}}
			for index, wire := range wires {
				for repeat := 0; repeat < 2; repeat++ {
					if err := s.handleCurrentDungeonPickup(session, currentDungeonPickupTestBody(wire.SceneObjectKey)); err != nil {
						t.Fatal(err)
					}
					assertCount(int64(index + 1))
				}
			}
			events, err := repositories.Activity.Events(context.Background(), query)
			if err != nil || len(events) != 2 {
				t.Fatalf("专用掉落事件数量错误：%v %v", events, err)
			}
			for _, event := range events {
				if event.DungeonID != id || event.CharacterID != "99" || event.ItemID != 9001 || event.Metric != repo.ActivityEpic {
					t.Fatalf("专用掉落归属错误：%+v", event)
				}
			}
		})
	}
}
