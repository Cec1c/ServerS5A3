package dnfbridge

import (
	"context"
	"time"

	r "longheng.io/server/internal/modules/dnf/repository"
	"longheng.io/server/internal/modules/dnf/wedding"
)

func (s *Service) currentWeddingTreeDelivery(ctx context.Context, now time.Time) func(r.MailboxRepository, r.CharacterRecord, wedding.TreeReward, string) (string, error) {
	return func(boxes r.MailboxRepository, c r.CharacterRecord, reward wedding.TreeReward, key string) (string, error) {
		if reward.ItemID <= 0 || reward.ItemID > 0xffffffff || reward.Count <= 0 || reward.Count > 0x7fffffff {
			return "", wedding.ErrItem
		}
		catalog, e := s.currentPVFItemCatalog()
		if e != nil {
			return "", e
		}
		definition, e := catalog.ResolveItem(uint32(reward.ItemID))
		if e != nil {
			return "", e
		}
		definition, e = currentPVFItemDefinitionForGrantAt(definition, now)
		if e != nil {
			return "", e
		}
		if definition.Kind != dungeonDropItemStackable || !definition.ExpirationDate.IsZero() && !definition.ExpirationDate.After(now) {
			return "", wedding.ErrItem
		}
		identity := currentPVFGrantIdentity(definition)
		return r.AppendSystemMail(ctx, boxes, r.SystemMailDelivery{
			RecipientCharacterID: c.CharacterID, RecipientName: c.Name, Title: "爱情树收获奖励", Body: "爱情树结出了礼物，请查收附件。",
			Source: "wedding_tree_harvest", CompletionKey: key, CreatedAt: now,
			Attachments: []r.MailAttachment{{ItemID: reward.ItemID, Count: reward.Count, Bind: identity.Bind, ExpireAt: definition.ExpirationDate, RawEntry: identity.RawEntry, Extra: identity.Extra}},
		})
	}
}

func (s *Service) currentWeddingCapsuleGrant(ctx context.Context, owner wedding.Owner, itemID int64) wedding.AssetGrant {
	return func(inventory *r.InventoryRecord, id, count int64, at time.Time) error {
		if id != itemID || id <= 0 || id > 0xffffffff || count <= 0 || count > 0xffffffff {
			return wedding.ErrItem
		}
		catalog, e := s.currentPVFItemCatalog()
		if e != nil {
			return e
		}
		definition, e := catalog.ResolveItem(uint32(id))
		if e != nil {
			return e
		}
		definition, e = currentPVFItemDefinitionForGrantAt(definition, at)
		if e != nil {
			return e
		}
		if definition.Kind != dungeonDropItemStackable {
			return wedding.ErrItem
		}
		// The lifecycle UOW has already bound both actual container records on
		// its scoped Settings repository. Never re-read base settings here.
		_, e = grantCurrentCeraShopProductAt(inventory, definition, uint32(count), at)
		return e
	}
}
