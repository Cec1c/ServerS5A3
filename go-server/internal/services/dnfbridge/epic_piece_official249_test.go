package dnfbridge

import (
	"context"
	"reflect"
	"testing"

	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

// 2026-11-04 现场（90CN 玩家报障「史诗碎片掉落进了背包装备栏」）之后，这条尾巴
// 从"只恢复哨兵槽 64"改成"重建整个 list 0"。
//
// 为什么单槽不够：op39 的目的槽位 64 就是装备页最后一格，客户端原生 NOTI39 会把地面
// 对象真的插进这一格。原来那条 op14 只把 64 号槽按仓库恢复一次 —— 该槽本来有装备时
// 能盖掉碎片堆，空槽时发的是 `itemID = MaxUint32` 的空行标记，客户端并不会因此删掉
// 自己造出来的那一堆。全库 54 个角色里只有 4 个的 `0:64` 有东西，所以绝大多数人看到
// 的都是残留。改成整容器重建后与槽位占用无关：客户端按服务端快照重排整页。
func TestOfficialEpicPiecePickupResyncsMainContainerWithoutGrantOrOverwrite(t *testing.T) {
	for _, occupied := range []bool{false, true} {
		service, runtime, repos := prepareCurrentEpicPieceDropTest(t)
		service.options.currentClientVersion = "official-24.9"
		inventory, _, _ := repos.Inventory.Load(context.Background(), "99")
		if occupied {
			inventory.Slots = make(map[string]dnfrepo.ItemStack)
			inventory.Slots["0:64"] = dnfrepo.ItemStack{ItemID: 9001, Count: 1}
		}
		if err := repos.Inventory.Save(context.Background(), inventory); err != nil {
			t.Fatal(err)
		}
		session := &gameSession{conn: &bufferConn{}, selectedCharacterID: 99, dungeon: dungeonSessionState{runtime: runtime}}
		key := seedCurrentDungeonPickupDrop(t, service, runtime, epicPieceBookTestPieceA, currentSceneActorObjectKey(99))
		for i := 0; i < 2; i++ {
			session.conn.(*bufferConn).write.Reset()
			if err := service.handleCurrentDungeonPickup(session, currentDungeonPickupTestBody(key)); err != nil {
				t.Fatal(err)
			}
			packets := splitAllGameServerUpperPackets(t, session.conn.(*bufferConn).write.Bytes())
			if len(packets) != 3 ||
				packets[0].Header.MsgID != 39 ||
				packets[1].Header.MsgID != 13 ||
				packets[2].Header.MsgID != 1149 {
				t.Fatalf("occupied=%v packets=%+v", occupied, packets)
			}
			after, _, _ := repos.Inventory.Load(context.Background(), "99")
			if !reflect.DeepEqual(inventory.Slots, after.Slots) {
				t.Fatal("book-only pickup modified bag")
			}
			account, _, _ := repos.Account.Load(context.Background(), epicPieceBookTestAccountID)
			if dnfrepo.AccountEpicPieceBalances(account)[epicPieceBookTestPieceA] != 1 {
				t.Fatal("replay credited twice")
			}
		}
	}
}
