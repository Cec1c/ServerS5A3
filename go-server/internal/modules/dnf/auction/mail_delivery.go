package auction

import (
	"context"
	"errors"
	repo "longheng.io/server/internal/modules/dnf/repository"
)

// 邮件入账与托管出账共用一个事务。发信成功但进程退出、客户端删信后再重试，
// 都只会看到已消耗的 outbox，不会再次生成装备或金币。
type outboxMailTransaction struct {
	assets         repo.MailboxAssetUnitOfWork
	key, recipient string
	delivery       repo.SystemMailDelivery
}

func (u outboxMailTransaction) WithinCharacterTrade(ctx context.Context, first, second string, apply func(repo.CharacterRepository, repo.InventoryRepository) error) error {
	if first != u.recipient || second != EscrowCharacterID {
		return ErrInvalidRequest
	}
	return u.assets.WithinMailboxAssets(ctx, EscrowCharacterID, u.recipient, func(c repo.CharacterRepository, i repo.InventoryRepository, m repo.MailboxRepository) error {
		escrow, found, err := i.Load(ctx, EscrowCharacterID)
		if err != nil || !found {
			return errors.Join(ErrUnavailable, err)
		}
		if _, pending := escrow.Slots[u.key]; !pending {
			return nil
		}
		if err := apply(c, i); err != nil {
			return err
		}
		_, err = repo.AppendSystemMail(ctx, m, u.delivery)
		return err
	})
}

func (o *Owner) DeliverItemMail(ctx context.Context, q ConfirmDeliveryCommand, d repo.SystemMailDelivery) error {
	if o == nil || o.repositories.MailboxAssets == nil {
		return ErrUnavailable
	}
	if d.RecipientCharacterID != q.BuyerID || d.Gold != q.Gold || len(d.Attachments) != 1 || d.Attachments[0].ItemID != q.ItemID || d.Attachments[0].Count != q.Count {
		return ErrInvalidRequest
	}
	recipient, found, err := o.repositories.Character.Load(ctx, q.BuyerID)
	if err != nil {
		return err
	}
	if !found || recipient.Stats["delete_flag"] != 0 {
		return ErrItemUnavailable
	}
	d.RecipientName = recipient.Name
	d.CompletionKey = q.DeliveryID

	copy := *o
	copy.repositories.CharacterTrade = outboxMailTransaction{assets: o.repositories.MailboxAssets, key: q.DeliveryID, recipient: q.BuyerID, delivery: d}
	return copy.ConfirmDeliveryMail(ctx, q)
}

func (o *Owner) DeliverPayoutMail(ctx context.Context, q ConfirmPayoutCommand, d repo.SystemMailDelivery) error {
	if o == nil || o.repositories.MailboxAssets == nil {
		return ErrUnavailable
	}
	if d.RecipientCharacterID != q.SellerID || d.Gold != q.Gold || len(d.Attachments) != 0 {
		return ErrInvalidRequest
	}
	recipient, found, err := o.repositories.Character.Load(ctx, q.SellerID)
	if err != nil {
		return err
	}
	if !found || recipient.Stats["delete_flag"] != 0 {
		return ErrItemUnavailable
	}
	d.RecipientName = recipient.Name
	d.CompletionKey = q.PayoutID

	copy := *o
	copy.repositories.CharacterTrade = outboxMailTransaction{assets: o.repositories.MailboxAssets, key: q.PayoutID, recipient: q.SellerID, delivery: d}
	return copy.ConfirmPayoutMail(ctx, q)
}
