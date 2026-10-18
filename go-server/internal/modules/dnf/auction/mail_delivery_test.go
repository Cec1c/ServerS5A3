package auction

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	repo "longheng.io/server/internal/modules/dnf/repository"
	memory "longheng.io/server/internal/modules/dnf/repository/memory"
)

type rejectOutboxCommit struct{ repo.MailboxAssetUnitOfWork }

func (u rejectOutboxCommit) WithinMailboxAssets(ctx context.Context, asset, recipient string, f func(repo.CharacterRepository, repo.InventoryRepository, repo.MailboxRepository) error) error {
	return u.MailboxAssetUnitOfWork.WithinMailboxAssets(ctx, asset, recipient, func(c repo.CharacterRepository, i repo.InventoryRepository, m repo.MailboxRepository) error {
		if err := f(c, i, m); err != nil {
			return err
		}
		return errors.New("模拟邮件与托管事务提交前失败")
	})
}

func TestAuctionOutboxAndMailCommitTogetherAndNeverReplayAfterDeletion(t *testing.T) {
	for _, kind := range []string{"装备", "成交款"} {
		t.Run(kind, func(t *testing.T) {
			ctx := context.Background()
			r := memory.NewMemoryGroup()
			raw := make([]byte, 119)
			raw[13] = 1
			raw[6] = 37
			item := repo.ItemStack{ItemID: 1234, Count: 1, RawEntry: raw, Extra: map[string]string{"item_kind": "equipment", "seal_flag": "1", "quality_seed": "83721", "enchant_id": "991"}}
			seedAuctionCharacter(t, ctx, r, "seller", "seller-account", "卖家", 50000, map[string]repo.ItemStack{"0:9": item})
			seedAuctionCharacter(t, ctx, r, "buyer", "buyer-account", "买家", 50000, nil)
			o, err := NewOwner(r)
			if err != nil {
				t.Fatal(err)
			}
			_, err = o.Register(ctx, RegisterCommand{ListingID: 71, SellerID: "seller", AccountID: "seller-account", SellerName: "卖家", ListType: 0, SourceSlot: 9, ExpectedID: 1234, Count: 1, UnitPrice: 1000, StartPrice: -1, Deposit: 10000, FeePercent: 5, Duration: time.Hour, ListingLimit: 5})
			if err != nil {
				t.Fatal(err)
			}
			_, err = o.Buy(ctx, BuyCommand{BuyerID: "buyer", AccountID: "buyer-account", ListingID: 71, ExpectedItemID: 1234, Count: 1, Money: 1000, WholeListing: true})
			if err != nil {
				t.Fatal(err)
			}
			beforeBag, _, _ := r.Inventory.Load(ctx, EscrowCharacterID)
			beforeWallet, _, _ := r.Character.Load(ctx, EscrowCharacterID)
			recipient := "buyer"
			var delivery func(*Owner) error
			if kind == "装备" {
				pending, err := o.PendingDeliveries(ctx, "buyer")
				if err != nil || len(pending) != 1 {
					t.Fatal(pending, err)
				}
				d := pending[0]
				q := ConfirmDeliveryCommand{DeliveryID: d.ID, BuyerID: d.BuyerID, ListingID: d.ListingID, ItemID: d.Item.ItemID, Count: d.Item.Count, Gold: d.Gold}
				mail := repo.SystemMailDelivery{RecipientCharacterID: recipient, Title: "拍卖所得物品", Source: "auction_purchase", Gold: d.Gold, Attachments: []repo.MailAttachment{{ItemID: item.ItemID, Count: item.Count, RawEntry: append([]byte(nil), item.RawEntry...), Extra: item.Extra}}}
				delivery = func(owner *Owner) error { return owner.DeliverItemMail(ctx, q, mail) }
			} else {
				recipient = "seller"
				pending, err := o.PendingPayouts(ctx, recipient)
				if err != nil || len(pending) != 1 {
					t.Fatal(pending, err)
				}
				d := pending[0]
				q := ConfirmPayoutCommand{PayoutID: d.ID, SellerID: d.SellerID, Gold: d.Gold}
				mail := repo.SystemMailDelivery{RecipientCharacterID: recipient, Title: "拍卖成交款", Source: "auction_sale_proceeds", Gold: d.Gold}
				delivery = func(owner *Owner) error { return owner.DeliverPayoutMail(ctx, q, mail) }
			}
			failed := *o
			failed.repositories.MailboxAssets = rejectOutboxCommit{r.MailboxAssets}
			if err := delivery(&failed); err == nil {
				t.Fatal("模拟提交失败却成功")
			}
			bag, _, _ := r.Inventory.Load(ctx, EscrowCharacterID)
			wallet, _, _ := r.Character.Load(ctx, EscrowCharacterID)
			mail, _, _ := r.Mailbox.Load(ctx, recipient)
			if !reflect.DeepEqual(bag, beforeBag) || !reflect.DeepEqual(wallet, beforeWallet) || len(mail.Mails) != 0 {
				t.Fatal("回滚遗留邮件或托管出账")
			}
			if err := delivery(o); err != nil {
				t.Fatal(err)
			}
			mail, _, _ = r.Mailbox.Load(ctx, recipient)
			if len(mail.Mails) != 1 {
				t.Fatal("邮件没有一次入账", mail)
			}
			for _, m := range mail.Mails {
				if kind == "装备" {
					a := m.Attachments[0]
					if !reflect.DeepEqual(a.RawEntry, item.RawEntry) || !reflect.DeepEqual(a.Extra, item.Extra) {
						t.Fatal("装备实例属性丢失")
					}
				} else if m.Gold != 10950 {
					t.Fatal("成交款未保留手续费和保证金", m.Gold)
				}
			}
			afterBag, _, _ := r.Inventory.Load(ctx, EscrowCharacterID)
			afterWallet, _, _ := r.Character.Load(ctx, EscrowCharacterID)
			// 模拟玩家已领取并删信；幂等真源是已消耗 outbox，不依赖邮件还在列表中。
			mail.Mails = map[string]repo.MailRecord{}
			if err := r.Mailbox.Save(ctx, mail); err != nil {
				t.Fatal(err)
			}
			for retry := 0; retry < 3; retry++ {
				if err := delivery(o); err != nil {
					t.Fatal(err)
				}
			}
			bag, _, _ = r.Inventory.Load(ctx, EscrowCharacterID)
			wallet, _, _ = r.Character.Load(ctx, EscrowCharacterID)
			mail, _, _ = r.Mailbox.Load(ctx, recipient)
			if !reflect.DeepEqual(bag, afterBag) || !reflect.DeepEqual(wallet, afterWallet) || len(mail.Mails) != 0 {
				t.Fatal("删信后重试重复发放资产")
			}
		})
	}
}
