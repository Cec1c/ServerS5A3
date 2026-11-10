package dnfbridge

import (
	"context"
	"errors"
	"fmt"
	r "longheng.io/server/internal/modules/dnf/repository"
	"longheng.io/server/internal/modules/dnf/wedding"
	"time"
)

func (s *Service) currentWeddingCeremonyDelivery(owner wedding.Owner, kind byte, now time.Time) (wedding.CeremonyDelivery, error) {
	gift, ok := owner.Rules.CeremonyGifts[kind]
	if !ok || gift.ItemID <= 0 || gift.ItemID > 0xffffffff || gift.Count <= 0 || gift.Count > 0xffffffff {
		return nil, wedding.ErrItem
	}
	catalog, e := s.currentPVFItemCatalog()
	if e != nil {
		return nil, e
	}
	definition, e := catalog.ResolveItem(uint32(gift.ItemID))
	if e != nil {
		return nil, e
	}
	definition, e = currentPVFItemDefinitionForGrantAt(definition, now)
	if e != nil {
		return nil, e
	}
	if definition.Kind != dungeonDropItemStackable {
		return nil, wedding.ErrItem
	}
	return func(ctx context.Context, g r.Group, bag *r.InventoryRecord, requested wedding.Gift, at int64) error {
		if requested != gift || at <= 0 {
			return wedding.ErrState
		}
		trial := r.CloneInventory(*bag)
		if e := r.BindInventoryContainerState(ctx, g.Settings, &trial); e != nil {
			return e
		}
		if _, e := grantCurrentCeraShopProductAt(&trial, definition, uint32(gift.Count), now); e == nil {
			*bag = trial
			return nil
		} else if !errors.Is(e, errDungeonPickupInventoryFull) {
			return e
		}
		c, found, e := g.Character.Load(ctx, bag.CharacterID)
		if e != nil {
			return e
		}
		if !found || c.CharacterID != bag.CharacterID {
			return wedding.ErrOwnership
		}
		identity := currentPVFGrantIdentity(definition)
		_, e = r.AppendSystemMail(ctx, g.Mailbox, r.SystemMailDelivery{RecipientCharacterID: c.CharacterID, RecipientName: c.Name, Title: "婚礼奖励", Body: "物品栏空间不足，婚礼奖励已通过邮件送达。", Source: "wedding_ceremony_gift", CompletionKey: fmt.Sprintf("wedding:%d:%s", at, c.CharacterID), CreatedAt: now, Attachments: []r.MailAttachment{{ItemID: gift.ItemID, Count: gift.Count, Bind: identity.Bind, ExpireAt: definition.ExpirationDate, RawEntry: identity.RawEntry, Extra: identity.Extra}}})
		return e
	}, nil
}
