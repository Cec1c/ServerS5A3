package dnfbridge

import (
	"context"
	"testing"
	"time"

	auction "longheng.io/server/internal/modules/dnf/auction"
	repo "longheng.io/server/internal/modules/dnf/repository"
	memory "longheng.io/server/internal/modules/dnf/repository/memory"
)

func TestAuctionRestartSettlesOfflineExpiryAndUndeliveredSale(t *testing.T) {
	for _, mode := range []string{"到期返还", "成交未投递"} {
		t.Run(mode, func(t *testing.T) {
			ctx := context.Background()
			r := memory.NewMemoryGroup()
			seedCurrentAuctionBridgeCharacter(t, ctx, r, "10", "seller", "卖家", 50000, map[string]repo.ItemStack{"0:9": {ItemID: 6001, Count: 2}})
			seedCurrentAuctionBridgeCharacter(t, ctx, r, "11", "buyer", "买家", 50000, nil)
			o, err := auction.NewOwner(r)
			if err != nil {
				t.Fatal(err)
			}
			duration := time.Nanosecond
			if mode == "成交未投递" {
				duration = time.Hour
			}
			_, err = o.Register(ctx, auction.RegisterCommand{ListingID: 3, SellerID: "10", AccountID: "seller", SellerName: "卖家", ListType: 0, SourceSlot: 9, ExpectedID: 6001, Count: 2, UnitPrice: 50, StartPrice: -1, Duration: duration, ListingLimit: 5, Deposit: 10000, FeePercent: 5})
			if err != nil {
				t.Fatal(err)
			}
			if mode == "成交未投递" {
				_, err = o.Buy(ctx, auction.BuyCommand{BuyerID: "11", AccountID: "buyer", ListingID: 3, ExpectedItemID: 6001, Count: 2, Money: 100, WholeListing: true})
				if err != nil {
					t.Fatal(err)
				}
			}
			// 模拟服务端重启，仅保留仓储，不保留任何会话或内存拍卖状态。
			s := &Service{repositoryProvider: func() (repo.Group, bool) { return r, true }}
			pending, err := s.maintainCurrentAuctionLocked(ctx)
			if err != nil || pending {
				t.Fatal("离线托管未结清", pending, err)
			}
			recipient := "10"
			if mode == "成交未投递" {
				recipient = "11"
			}
			m, found, err := r.Mailbox.Load(ctx, recipient)
			if err != nil || !found || len(m.Mails) != 1 {
				t.Fatal("离线物品邮件缺失", m, err)
			}
			for _, letter := range m.Mails {
				if len(letter.Attachments) != 1 || letter.Attachments[0].Count != 2 {
					t.Fatal("物品邮件错误", letter)
				}
			}
			seller, _, _ := r.Mailbox.Load(ctx, "10")
			for _, letter := range seller.Mails {
				expected := int64(10000)
				if mode == "成交未投递" {
					expected = 10095
				}
				if letter.Gold != expected {
					t.Fatal("保证金或成交款错误", letter.Gold, expected)
				}
			}
			for retry := 0; retry < 2; retry++ {
				if pending, err := s.maintainCurrentAuctionLocked(ctx); err != nil || pending {
					t.Fatal(pending, err)
				}
			}
			after, _, _ := r.Mailbox.Load(ctx, recipient)
			if len(after.Mails) != 1 {
				t.Fatal("重启重试重复发信")
			}
		})
	}
}

func TestAuctionClockEmptyLedgerAndCancelledRunDoNotCreateOrMutateAssets(t *testing.T) {
	r := memory.NewMemoryGroup()
	q := newFakeCurrentDungeonDeathTimerQueue()
	s := &Service{gameplayTimers: q, repositoryProvider: func() (repo.Group, bool) { return r, true }}
	ctx, cancel := context.WithCancel(context.Background())
	s.startCurrentAuctionClock(ctx)
	if q.scheduleCount != 1 {
		t.Fatal("启动未登记单一恢复检查")
	}
	q.tasks[0].callback(time.Now())
	if q.scheduleCount != 1 {
		t.Fatal("空托管持续轮询")
	}
	if _, found, err := r.Character.Load(ctx, auction.EscrowCharacterID); err != nil || found {
		t.Fatal("空托管制造了系统角色", found, err)
	}
	cancel()
	q.tasks[0].callback(time.Now())
	if q.scheduleCount != 1 {
		t.Fatal("已停止服务重新安排结算")
	}
}
