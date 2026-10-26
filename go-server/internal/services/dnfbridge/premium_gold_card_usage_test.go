package dnfbridge

import (
	"bytes"
	"context"
	"reflect"
	"strconv"
	"testing"
	"time"

	"longheng.io/server/internal/modules/dnf/dnfenum"
	"longheng.io/server/internal/modules/dnf/premium"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

func preparePremiumPaidCardUsage(t *testing.T, used int64, oldDay bool) (*Service, *runtimeDungeonState, *gameSession, *bufferConn, dnfrepo.Group) {
	t.Helper()
	s, r, p, conn, repos := preparePaidCardPriceFlow(t, 100)
	ctx := context.Background()
	now := s.gameplayNow().UTC()
	account := dnfrepo.AccountRecord{AccountID: s.accountIDForSession(p), Metadata: map[string]string{}}
	premium.Upsert(&account, premium.DevilSlotType(premium.DevilSlotGoldCard), 86400, 1, now)
	premium.Upsert(&account, premium.DevilSlotType(premium.DevilSlotSevenBuff), 86400, 1, now)
	if err := repos.Account.Save(ctx, account); err != nil {
		t.Fatal(err)
	}
	character, _, _ := repos.Character.Load(ctx, "99")
	character.AccountID = account.AccountID
	usageDay := now
	if oldDay {
		usageDay = now.Add(-48 * time.Hour)
	}
	for i := int64(0); i < used; i++ {
		if !premium.TryConsumeDaily(&character, premium.DevilSlotGoldCard, usageDay) {
			t.Fatal("无法建立黄金卡牌次数夹具")
		}
	}
	if err := repos.Character.Save(ctx, character); err != nil {
		t.Fatal(err)
	}
	paid := &r.settlementCardRewardState.plan.Sides[dungeonCardSidePaid]
	paid.Cost = 0
	paid.ConsumePremiumDaily = true
	paid.PremiumDailySlot = premium.DevilSlotGoldCard
	r.settlementCardRewardState.premiumServiceBody = buildCurrentPremiumServiceDataBody(account, character, now)
	return s, r, p, conn, repos
}

func assertPremiumPaidCardUsagePacket(t *testing.T, wire []byte, repos dnfrepo.Group, want int64) {
	t.Helper()
	var premiumBody []byte
	count := 0
	for _, packet := range splitAllCurrentUpperPackets(t, wire) {
		if packet.Header.MsgID == uint16(dnfenum.CmdPacketPremiumService) {
			count++
			if packet.Header.Classification != 1 {
				t.Fatal("契约次数通知类别错误")
			}
			premiumBody = packet.Body
		}
	}
	account, _, err := repos.Account.Load(context.Background(), "account-1")
	if err != nil {
		t.Fatal(err)
	}
	character, _, err := repos.Character.Load(context.Background(), "99")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	if count != 1 || !bytes.Equal(premiumBody, buildCurrentPremiumServiceDataBody(account, character, now)) ||
		premium.DailyUsage(character, premium.DevilSlotGoldCard, now) != want {
		t.Fatalf("翻牌后未立即投影已保存的完整契约状态：包数=%d 正文=%x 期望次数=%d", count, premiumBody, want)
	}
}

func TestPremiumGoldCardFirstFlipImmediatelyRefreshesCommittedUsage(t *testing.T) {
	for _, scenario := range []struct {
		used, want int64
		oldDay     bool
	}{{0, 1, false}, {6, 7, false}, {9, 10, false}, {10, 1, true}} {
		t.Run(strconv.FormatInt(scenario.used, 10)+"_old_day_"+strconv.FormatBool(scenario.oldDay), func(t *testing.T) {
			s, r, p, conn, repos := preparePremiumPaidCardUsage(t, scenario.used, scenario.oldDay)
			if err := s.selectCurrentDungeonCardLocked(p, r, dungeonCardSidePaid, 1, "测试首次免费翻牌次数刷新"); err != nil {
				t.Fatal(err)
			}
			assertPaidCardAssets(t, repos, 125, 1)
			assertPremiumPaidCardUsagePacket(t, conn.write.Bytes(), repos, scenario.want)
			packets := splitAllCurrentUpperPackets(t, conn.write.Bytes())
			if len(packets) != 4 || packets[0].Header.MsgID != 71 || packets[1].Header.MsgID != 14 || packets[2].Header.MsgID != 14 || packets[3].Header.MsgID != 903 {
				t.Fatal("翻牌成功牌面、钱包、物品和契约次数通知顺序错误")
			}
			before, _, _ := repos.Character.Load(context.Background(), "99")
			bagBefore, _, _ := repos.Inventory.Load(context.Background(), "99")
			conn.write.Reset()
			if err := s.selectCurrentDungeonCardLocked(p, r, dungeonCardSidePaid, 1, "测试重复点击"); err != nil {
				t.Fatal(err)
			}
			after, _, _ := repos.Character.Load(context.Background(), "99")
			bagAfter, _, _ := repos.Inventory.Load(context.Background(), "99")
			if !reflect.DeepEqual(before, after) || !reflect.DeepEqual(bagBefore, bagAfter) || len(splitAllCurrentUpperPackets(t, conn.write.Bytes())) != 1 {
				t.Fatal("重复翻牌再次扣次、发奖或重发已完成的资产/契约通知")
			}
		})
	}
}

func TestPremiumGoldCardUsageNotificationFailureResumesWithoutGrantingAgain(t *testing.T) {
	for _, failAt := range []int{1, 2, 3, 4} {
		t.Run(strconv.Itoa(failAt), func(t *testing.T) {
			s, r, p, conn, repos := preparePremiumPaidCardUsage(t, 6, false)
			fault := &paidCardPriceFailConn{bufferConn: conn, failAt: failAt}
			p.conn = fault
			_ = s.selectCurrentDungeonCardLocked(p, r, dungeonCardSidePaid, 1, "测试付费翻牌通知写失败")
			assertPaidCardAssets(t, repos, 125, 1)
			character, _, _ := repos.Character.Load(context.Background(), "99")
			if fault.calls < failAt || r.settlementPaidCardGrant == nil || r.settlementCardSideRewardCommitted[dungeonCardSidePaid] ||
				premium.DailyUsage(character, premium.DevilSlotGoldCard, time.Now().UTC()) != 7 {
				t.Fatal("未到达失败点、次数未保存或错误推进通知游标")
			}
			// 失败后其它契约也消耗次数，续传必须读取当前完整状态，不能重发翻牌前冻结值。
			premium.TryConsumeDaily(&character, premium.DevilSlotSevenBuff, time.Now().UTC())
			if err := repos.Character.Save(context.Background(), character); err != nil {
				t.Fatal(err)
			}
			p.conn = conn
			if err := s.selectCurrentDungeonCardLocked(p, r, dungeonCardSideFree, 0, "测试重试前领取免费行"); err != nil {
				t.Fatal(err)
			}
			conn.write.Reset()
			if err := s.selectCurrentDungeonCardLocked(p, r, dungeonCardSidePaid, 1, "测试付费通知续传"); err != nil {
				t.Fatal(err)
			}
			assertPaidCardAssets(t, repos, 132, 1)
			assertPremiumPaidCardUsagePacket(t, conn.write.Bytes(), repos, 7)
			if !r.settlementCardSideRewardCommitted[dungeonCardSidePaid] {
				t.Fatal("续传后没有完成通知游标")
			}
		})
	}
}

func TestPremiumGoldCardFailedGrantDoesNotRefreshOrConsumeUsage(t *testing.T) {
	for _, used := range []int64{6, 10} {
		t.Run(strconv.FormatInt(used, 10), func(t *testing.T) {
			s, r, p, conn, repos := preparePremiumPaidCardUsage(t, used, false)
			if used == 6 {
				// 强制事务先尝试扣次，再因原余额不足回滚。
				r.settlementCardRewardState.plan.Sides[dungeonCardSidePaid].Cost = 101
			}
			_ = s.selectCurrentDungeonCardLocked(p, r, dungeonCardSidePaid, 1, "测试奖励事务失败")
			assertPaidCardAssets(t, repos, 100, 0)
			character, _, _ := repos.Character.Load(context.Background(), "99")
			packets := splitAllCurrentUpperPackets(t, conn.write.Bytes())
			if r.settlementPaidCardGrant != nil || len(packets) != 1 || packets[0].Header.MsgID != 71 ||
				premium.DailyUsage(character, premium.DevilSlotGoldCard, time.Now().UTC()) != used {
				t.Fatal("失败的奖励事务仍扣次或发送成功契约状态")
			}
		})
	}
}
