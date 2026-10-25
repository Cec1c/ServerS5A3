package dnfbridge

import (
	"context"
	"sort"
	"strconv"

	aiken "longheng.io/server/internal/modules/dnf/aikenupgrade"
	repo "longheng.io/server/internal/modules/dnf/repository"
)

// Snapshot uses persisted set metadata, never a template's name or an
// arbitrary item tail as permission to manufacture a set record.
func (s *Service) sendCurrentAikenSetSnapshot(ctx context.Context, g *gameSession) error {
	if g == nil || g.selectedCharacterID == 0 {
		return nil
	}
	repos, ok := s.repositoryGroup()
	if !ok || repos.AccountAssets == nil {
		return nil
	}
	id := strconv.Itoa(int(g.selectedCharacterID))
	account := s.accountIDForSession(g)
	records := map[byte]aiken.Record{}
	var validate func(repo.ItemStack) error
	err := repos.AccountAssets.WithinAccountCharacterAssets(ctx, account, id, func(_ repo.AccountInventoryRepository, chars repo.CharacterRepository, invs repo.InventoryRepository, equips repo.EquipmentRepository) error {
		ch, found, err := chars.Load(ctx, id)
		if err != nil {
			return err
		}
		if !found {
			return nil
		}
		if ch.AccountID != account || ch.CharacterID != id {
			return aiken.ErrInvalid
		}
		inv, _, err := invs.Load(ctx, id)
		if err != nil {
			return err
		}
		worn, _, err := equips.Load(ctx, id)
		if err != nil {
			return err
		}
		collect := func(stack repo.ItemStack) error {
			if stack.Extra["custom_ability_set_options"] == "" {
				return nil
			}
			if validate == nil {
				catalog, err := s.currentPVFItemCatalog()
				if err != nil {
					return err
				}
				rules, err := currentAikenSetRules(catalog)
				if err != nil {
					return err
				}
				validate = rules.Item.Validate
			}
			if stack.Count != 1 {
				return aiken.ErrInvalid
			}
			if err := validate(stack); err != nil {
				return err
			}
			r, found, err := currentAikenReadSet(stack)
			if err != nil {
				return err
			}
			if !found {
				return nil
			}
			if old, exists := records[r.Record0]; exists && old != r {
				return aiken.ErrInvalid
			}
			records[r.Record0] = r
			return nil
		}
		for _, stack := range inv.Slots {
			if err := collect(stack); err != nil {
				return err
			}
		}
		for _, stack := range inv.Warehouse {
			if err := collect(stack); err != nil {
				return err
			}
		}
		for _, e := range worn.Entries {
			if err := collect(repo.ItemStack{ItemID: e.ItemID, Count: 1, RawEntry: e.RawEntry, Extra: e.Extra}); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return err
	}
	keys := make([]int, 0, len(records))
	for key := range records {
		keys = append(keys, int(key))
	}
	sort.Ints(keys)
	for _, key := range keys {
		r := records[byte(key)]
		r.Selector = uint32(g.selectedCharacterID)
		if err := s.sendGameUpperRawClass(g, 1278, r.Encode(), 0); err != nil {
			return err
		}
	}
	return nil
}
