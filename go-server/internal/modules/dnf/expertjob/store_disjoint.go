package expertjob

import (
	"context"
	"reflect"
	"strings"
	"time"

	r "longheng.io/server/internal/modules/dnf/repository"
)

type StoreDisjointAssets struct {
	Account                            *r.AccountRecord
	Warehouse                          *r.AccountInventoryRecord
	Requester, Owner                   *r.CharacterRecord
	RequesterInventory, OwnerInventory *r.InventoryRecord
}

type StoreDisjointCommand struct {
	AccountID, RequesterID, OwnerID, OwnerAccountID string
	UpdatedAt                                       time.Time
	Project                                         func(*StoreDisjointAssets) error
}

func DisjointStore(ctx context.Context, repos r.Group, command StoreDisjointCommand) error {
	unit, ok := repos.CharacterTrade.(r.DisjointerAssetUnitOfWork)
	if !ok || command.Project == nil {
		return ErrOwnerUnavailable
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if strings.TrimSpace(command.AccountID) == "" || command.RequesterID == "" || command.OwnerID == "" {
		return ErrAccountMismatch
	}
	if command.RequesterID != command.OwnerID && strings.TrimSpace(command.OwnerAccountID) == "" {
		return ErrAccountMismatch
	}
	now := command.UpdatedAt.UTC()
	if now.IsZero() {
		now = time.Now().UTC()
	}
	return unit.WithinDisjointerAssets(ctx, command.AccountID, command.RequesterID, command.OwnerID, func(ledger r.AccountRepository, characters r.CharacterRepository, inventories r.InventoryRepository, warehouse r.AccountInventoryRepository) error {
		a, found, err := ledger.Load(ctx, command.AccountID)
		if err != nil {
			return err
		}
		if !found || a.AccountID != command.AccountID {
			return ErrAccountMismatch
		}
		a = r.CloneAccount(a)
		beforeAccount := r.CloneAccount(a)
		ids := []string{command.RequesterID}
		if command.RequesterID != command.OwnerID {
			ids = append(ids, command.OwnerID)
		}
		ids, err = r.CharacterInventoryGroupIDs(ids)
		if err != nil {
			return err
		}
		cs, bags := map[string]*r.CharacterRecord{}, map[string]*r.InventoryRecord{}
		for _, id := range ids {
			c, exists, err := characters.Load(ctx, id)
			if err != nil {
				return err
			}
			if !exists || c.CharacterID != id {
				return ErrCharacterNotFound
			}
			if (id == command.RequesterID && c.AccountID != command.AccountID) || (id == command.OwnerID && command.OwnerAccountID != "" && c.AccountID != command.OwnerAccountID) {
				return ErrAccountMismatch
			}
			i, exists, err := inventories.Load(ctx, id)
			if err != nil {
				return err
			}
			if !exists || i.CharacterID != id {
				return ErrInventoryNotFound
			}
			if err := r.BindInventoryContainerState(ctx, repos.Settings, &i); err != nil {
				return err
			}
			c, i = r.CloneCharacter(c), r.CloneInventory(i)
			if i.Slots == nil {
				i.Slots = map[string]r.ItemStack{}
			}
			cs[id], bags[id] = &c, &i
		}
		w, _, err := warehouse.Load(ctx, command.AccountID)
		if err != nil {
			return err
		}
		w = r.CloneAccountInventory(w)
		w.AccountID = command.AccountID
		if w.Slots == nil {
			w.Slots = map[string]r.ItemStack{}
		}
		beforeWarehouse := r.CloneAccountInventory(w)
		assets := &StoreDisjointAssets{Account: &a, Warehouse: &w, Requester: cs[command.RequesterID], Owner: cs[command.OwnerID], RequesterInventory: bags[command.RequesterID], OwnerInventory: bags[command.OwnerID]}
		if err := command.Project(assets); err != nil {
			return err
		}
		if !reflect.DeepEqual(beforeAccount, a) {
			a.UpdatedAt = now
			if err := ledger.Save(ctx, a); err != nil {
				return err
			}
		}
		for _, id := range ids {
			cs[id].UpdatedAt, bags[id].UpdatedAt = now, now
			if err := r.SaveCharacterFields(ctx, characters, *cs[id], r.CharacterFieldStats); err != nil {
				return err
			}
			if err := r.SaveInventoryFields(ctx, inventories, *bags[id], r.InventoryFieldSlots); err != nil {
				return err
			}
		}
		if !reflect.DeepEqual(beforeWarehouse, w) {
			w.UpdatedAt = now
			if err := warehouse.Save(ctx, w); err != nil {
				return err
			}
		}
		return nil
	})
}
