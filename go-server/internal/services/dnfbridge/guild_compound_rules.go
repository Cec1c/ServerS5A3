package dnfbridge

import (
	"time"

	"longheng.io/server/internal/modules/dnf/guildcompound"
	repo "longheng.io/server/internal/modules/dnf/repository"
)

func currentGuildCompoundRules(c *pvfDungeonDropCatalog, now time.Time) (guildcompound.Rules, error) {
	if c == nil || c.source == nil {
		return guildcompound.Rules{}, errDungeonDropSourceRequired
	}
	catalog, err := guildcompound.LoadCompoundCatalog(c.source)
	if err != nil {
		return guildcompound.Rules{}, err
	}
	return guildcompound.Rules{
		Catalog: catalog,
		Resolve: func(id uint32) (guildcompound.MaterialDefinition, error) {
			return currentGuildCompoundDefinition(c, id)
		},
		Candidates: func(kind guildcompound.CompoundKind, rarity uint32) ([]uint32, error) {
			return currentGuildCompoundCandidatePool(c, kind, rarity)
		},
		UpdateStack: currentGuildCompoundUpdateStack,
		Expired:     currentDungeonDiscardStackExpired,
		Grant: func(inv *repo.InventoryRecord, id uint32) (int16, error) {
			definition, err := c.ResolveItem(id)
			if err != nil {
				return -1, err
			}
			if !definition.ExpirationDate.IsZero() && !definition.ExpirationDate.After(now) {
				return -1, guildcompound.ErrInvalid
			}
			return currentGuildCompoundGrant(c, inv, id)
		},
	}, nil
}
