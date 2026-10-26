package dnfbridge

import (
	"bytes"
	"context"
	"errors"
	"reflect"
	"testing"

	"longheng.io/server/internal/modules/dnf/dnfenum"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

func preparePaidCardPriceFlow(t *testing.T, balance int64) (*Service, *runtimeDungeonState, *gameSession, *bufferConn, dnfrepo.Group) {
	t.Helper()
	s, r, p, conn, _ := prepareCompletedSettlementRuntime(t)
	repos, _ := s.repositoryGroup()
	c, found, err := repos.Character.Load(context.Background(), "99")
	if err != nil || !found {
		t.Fatalf("读取测试角色：found=%t err=%v", found, err)
	}
	c.Stats["gold"] = balance
	if err := repos.Character.Save(context.Background(), c); err != nil {
		t.Fatal(err)
	}
	if err := repos.Inventory.Save(context.Background(), dnfrepo.InventoryRecord{CharacterID: "99", Slots: map[string]dnfrepo.ItemStack{}}); err != nil {
		t.Fatal(err)
	}
	plan, err := newDungeonCardRewardPlan(dungeonCardPlanIdentity{
		CharacterID: "99", DungeonID: r.Dungeon.ID, MazeIndex: r.MazeIndex, RunSeed: r.Seed,
	}, "翻牌费用流程测试", dungeonCardRewardBundle{Gold: 7}, dungeonCardRewardBundle{
		Cost: 30, Gold: 25,
		Items: []dungeonCardItemReward{{ItemID: 5001, Count: 1, SlotStart: 9, SlotEnd: 64, Extra: map[string]string{"item_kind": "equipment"}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	r.settlementCardRewardState, err = newDungeonCardState(plan)
	if err != nil {
		t.Fatal(err)
	}
	r.settlementCardLayoutSent = true
	r.advanceSettlementPhase(currentDungeonSettlementPhaseCardsRevealed)
	conn.write.Reset()
	return s, r, p, conn, repos
}

func assertPaidCardAssets(t *testing.T, repos dnfrepo.Group, balance int64, items int) {
	t.Helper()
	c, _, err := repos.Character.Load(context.Background(), "99")
	if err != nil {
		t.Fatal(err)
	}
	bag, _, err := repos.Inventory.Load(context.Background(), "99")
	if err != nil {
		t.Fatal(err)
	}
	itemCount := int64(0)
	for _, stack := range bag.Slots {
		if stack.ItemID == 5001 {
			itemCount += stack.Count
		}
	}
	if c.Stats["gold"] != balance || itemCount != int64(items) {
		t.Fatalf("翻牌资产不符：金币=%d 期望=%d 物品=%+v 期望数量=%d", c.Stats["gold"], balance, bag.Slots, items)
	}
}

func TestPaidCardPriceChargesOnceAndPreservesRewardFace(t *testing.T) {
	s, r, p, conn, repos := preparePaidCardPriceFlow(t, 100)
	if err := s.selectCurrentDungeonCardLocked(p, r, dungeonCardSidePaid, 1, "收费测试"); err != nil {
		t.Fatal(err)
	}
	assertPaidCardAssets(t, repos, 95, 1)
	op71, rest := splitGameServerUpperPacket(t, conn.write.Bytes())
	if op71.Header.MsgID != uint16(dnfenum.CmdPacketSelectCard) || op71.Header.Classification != currentDungeonCardResponseClass {
		t.Fatalf("成功牌面类别错误：%+v", op71.Header)
	}
	slot := decodeCurrentDungeonOp71SlotsForTest(t, op71.Body)[1]
	if slot.StateA != 0xff || slot.StateB != 0 || len(slot.Rewards) != 2 ||
		slot.Rewards[0] != (currentDungeonOp71RewardTuple{ValueA: 0, ValueB: 25}) ||
		slot.Rewards[1] != (currentDungeonOp71RewardTuple{ValueA: 5001, ValueB: 1}) {
		t.Fatalf("付费牌面未保留原奖励：%+v", slot)
	}
	gold, rest := splitGameServerUpperPacket(t, rest)
	_, rest = splitGameServerUpperPacket(t, rest)
	if !bytes.Equal(gold.Body, buildCurrentGoldStateBody(95)) || len(rest) != 0 {
		t.Fatal("收费成功未按原顺序投影实际钱包及物品")
	}
	before, _, _ := repos.Character.Load(context.Background(), "99")
	bagBefore, _, _ := repos.Inventory.Load(context.Background(), "99")
	conn.write.Reset()
	if err := s.selectCurrentDungeonCardLocked(p, r, dungeonCardSidePaid, 1, "重复点击"); err != nil {
		t.Fatal(err)
	}
	after, _, _ := repos.Character.Load(context.Background(), "99")
	bagAfter, _, _ := repos.Inventory.Load(context.Background(), "99")
	_, rest = splitGameServerUpperPacket(t, conn.write.Bytes())
	if !reflect.DeepEqual(before, after) || !reflect.DeepEqual(bagBefore, bagAfter) || len(rest) != 0 {
		t.Fatal("重复点击再次修改资产或重复投影已完成资产通知")
	}
}

func TestPaidCardInsufficientGoldRestoresUnselectedAndAllowsAnotherCard(t *testing.T) {
	s, r, p, conn, repos := preparePaidCardPriceFlow(t, 22)
	if err := s.selectCurrentDungeonCardLocked(p, r, dungeonCardSideFree, 0, "先翻免费牌"); err != nil {
		t.Fatal(err)
	}
	assertPaidCardAssets(t, repos, 29, 0)
	conn.write.Reset()
	if err := s.selectCurrentDungeonCardLocked(p, r, dungeonCardSidePaid, 1, "差1金币"); err != nil {
		t.Fatal(err)
	}
	assertPaidCardAssets(t, repos, 29, 0)
	op71, rest := splitGameServerUpperPacket(t, conn.write.Bytes())
	slots := decodeCurrentDungeonOp71SlotsForTest(t, op71.Body)
	if len(rest) != 0 || slots[0].StateA == 0xff || r.settlementCardSideSelectionKnown[dungeonCardSidePaid] || r.settlementPaidCardGrant != nil {
		t.Fatal("拒付时丢失免费牌、保留了付费选择或发送了资产变更")
	}
	for _, slot := range slots {
		if slot.StateB != 0xff {
			t.Fatal("金币不足仍显示付费牌已领取")
		}
	}
	c, _, _ := repos.Character.Load(context.Background(), "99")
	c.Stats["gold"] = 30
	if err := repos.Character.Save(context.Background(), c); err != nil {
		t.Fatal(err)
	}
	conn.write.Reset()
	if err := s.selectCurrentDungeonCardLocked(p, r, dungeonCardSidePaid, 2, "足额后重新选牌"); err != nil {
		t.Fatal(err)
	}
	assertPaidCardAssets(t, repos, 25, 1)
	if !r.settlementCardSideRewardCommitted[dungeonCardSidePaid] {
		t.Fatal("拒付后不能换牌完成收费")
	}
}

type paidCardPriceFailConn struct {
	*bufferConn
	failAt, calls int
}

func (c *paidCardPriceFailConn) Write(p []byte) (int, error) {
	c.calls++
	if c.calls == c.failAt {
		return 0, errors.New("翻牌价格测试：通知写入失败")
	}
	return c.bufferConn.Write(p)
}

func TestPaidCardPricePacketFailuresResumeWithoutChargingAgain(t *testing.T) {
	for _, failAt := range []int{1, 2, 3} {
		t.Run(string(rune('0'+failAt)), func(t *testing.T) {
			s, r, p, conn, repos := preparePaidCardPriceFlow(t, 100)
			fault := &paidCardPriceFailConn{bufferConn: conn, failAt: failAt}
			p.conn = fault
			_ = s.selectCurrentDungeonCardLocked(p, r, dungeonCardSidePaid, 1, "付费通知失败")
			if fault.calls < failAt || r.settlementPaidCardGrant == nil || r.settlementCardSideRewardCommitted[dungeonCardSidePaid] {
				t.Fatal("未到达预期失败点或错误提交了全部通知")
			}
			assertPaidCardAssets(t, repos, 95, 1)
			p.conn = conn
			conn.write.Reset()
			// 付费回执之后免费牌又增加7，续传必须投影102而非旧的95。
			if err := s.selectCurrentDungeonCardLocked(p, r, dungeonCardSideFree, 0, "重试前先翻免费牌"); err != nil {
				t.Fatal(err)
			}
			conn.write.Reset()
			if err := s.selectCurrentDungeonCardLocked(p, r, dungeonCardSidePaid, 1, "续传付费牌"); err != nil {
				t.Fatal(err)
			}
			assertPaidCardAssets(t, repos, 102, 1)
			_, rest := splitGameServerUpperPacket(t, conn.write.Bytes())
			gold, rest := splitGameServerUpperPacket(t, rest)
			_, rest = splitGameServerUpperPacket(t, rest)
			if !bytes.Equal(gold.Body, buildCurrentGoldStateBody(102)) || len(rest) != 0 || !r.settlementCardSideRewardCommitted[dungeonCardSidePaid] {
				t.Fatal("付费通知重试覆盖了新钱包、丢失物品投影或未完成提交")
			}
		})
	}
}

func TestPaidCardPriceReassignedCardResumesOriginalRequestAfterAckFailure(t *testing.T) {
	s, r, p, conn, repos := preparePaidCardPriceFlow(t, 100)
	r.partyInstance = &dungeonPartyInstance{}
	r.partyInstance.cardReservations[dungeonCardSidePaid][1] = 123
	p.conn = &paidCardPriceFailConn{bufferConn: conn, failAt: 1}
	_ = s.selectCurrentDungeonCardLocked(p, r, dungeonCardSidePaid, 1, "队伍改派后应答失败")
	assertPaidCardAssets(t, repos, 95, 1)
	if r.settlementCardSideMember[dungeonCardSidePaid] != 2 {
		t.Fatal("测试未触发牌位改派")
	}
	p.conn = conn
	conn.write.Reset()
	if err := s.selectCurrentDungeonCardLocked(p, r, dungeonCardSidePaid, 1, "重发客户端原请求"); err != nil {
		t.Fatal(err)
	}
	assertPaidCardAssets(t, repos, 95, 1)
	if conn.write.Len() == 0 || !r.settlementCardSideRewardCommitted[dungeonCardSidePaid] {
		t.Fatal("已收费改派牌位未能按原请求续传")
	}
}
