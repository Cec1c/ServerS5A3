package aikenupgrade

import (
	"context"
	"math"
	"strings"
	"time"

	repo "longheng.io/server/internal/modules/dnf/repository"
)

type InitialRepairCommand struct {
	AccountID, CharacterID string
	Now                    time.Time
	Catalog                func(int64) (*InitialCatalog, error)
	ReadState              func(repo.ItemStack) ([5]byte, error)
	WriteState             func(repo.ItemStack, int16, [5]byte) (repo.ItemStack, error)
	Roll                   func(uint32) (uint32, error)
}

// RepairInitialOptions repairs only historical dungeon-issued instances whose
// two options were both never initialized. Existing selections, grades, set
// records, locks, expiry and all other instance bytes remain authoritative.
func (o *Owner) RepairInitialOptions(ctx context.Context, c InitialRepairCommand) (int, error) {
	if o == nil || o.assets == nil || c.AccountID == "" || c.CharacterID == "" || c.Catalog == nil || c.ReadState == nil || c.WriteState == nil || c.Roll == nil {
		return 0, ErrInvalid
	}
	count := 0
	err := o.assets.WithinAccountCharacterAssets(ctx, c.AccountID, c.CharacterID, func(accounts repo.AccountInventoryRepository, chars repo.CharacterRepository, invs repo.InventoryRepository, equips repo.EquipmentRepository) error {
		ch, ok, err := chars.Load(ctx, c.CharacterID)
		if err != nil {
			return err
		}
		if !ok || ch.CharacterID != c.CharacterID || ch.AccountID != c.AccountID {
			return ErrInvalid
		}
		inv, invOK, err := invs.Load(ctx, c.CharacterID)
		if err != nil {
			return err
		}
		worn, wornOK, err := equips.Load(ctx, c.CharacterID)
		if err != nil {
			return err
		}
		account, accountOK, err := accounts.Load(ctx, c.AccountID)
		if err != nil {
			return err
		}
		if invOK && inv.CharacterID != c.CharacterID || wornOK && worn.CharacterID != c.CharacterID || accountOK && account.AccountID != c.AccountID {
			return ErrInvalid
		}
		inv, worn, account = repo.CloneInventory(inv), repo.CloneEquipment(worn), repo.CloneAccountInventory(account)
		// Historical repair uses the same persistent instance identity as the
		// upgrade owner. An identity repeated anywhere in the character/account
		// assets is ambiguous; skip every copy instead of randomly repairing only
		// whichever map entry happens to be visited first.
		identities := map[string]int{}
		noteIdentity := func(s repo.ItemStack) {
			if id := strings.TrimSpace(s.Extra["native_buff_instance"]); id != "" {
				identities[id]++
			}
		}
		for _, s := range inv.Slots {
			noteIdentity(s)
		}
		for _, s := range inv.Warehouse {
			noteIdentity(s)
		}
		for _, s := range account.Slots {
			noteIdentity(s)
		}
		for _, entry := range worn.Entries {
			noteIdentity(repo.ItemStack{Extra: entry.Extra})
		}
		repair := func(s repo.ItemStack, slot int16) (repo.ItemStack, bool, error) {
			if s.Count != 1 || s.Extra["source"] != "dungeon_pvf_drop_pickup" || len(s.RawEntry) != 119 {
				return s, false, nil
			}
			if id := strings.TrimSpace(s.Extra["native_buff_instance"]); id != "" && identities[id] != 1 {
				return s, false, nil
			}
			state, err := c.ReadState(s)
			if err != nil {
				return s, false, nil
			} // Conflicting carriers are not a migration target.
			if state[0] != 0 || state[1] != 0 || state[2] != 0 || state[4] > 3 {
				return s, false, nil
			}
			catalog, err := c.Catalog(s.ItemID)
			if err != nil {
				return s, false, err
			}
			if catalog == nil {
				return s, false, nil
			}
			rolled, err := catalog.Roll(c.Roll)
			if err != nil {
				return s, false, err
			}
			state[0], state[1] = rolled[0], rolled[1]
			s, err = c.WriteState(s, slot, state)
			return s, err == nil, err
		}
		repairMap := func(items map[string]repo.ItemStack, container int64) (bool, error) {
			changed := false
			// Textual aliases such as 0:9 and 00:09 address the same endpoint.
			// Count them per container before mutation so neither copy is rolled.
			endpoints := map[int64]int{}
			for key := range items {
				if list, slot, ok := endpoint(key); ok && list == container {
					endpoints[slot]++
				}
			}
			for key, item := range items {
				list, slot, valid := endpoint(key)
				if !valid || list != container || slot > math.MaxInt16 || endpoints[slot] != 1 {
					continue
				}
				after, done, err := repair(item, int16(slot))
				if err != nil {
					return false, err
				}
				if done {
					items[key] = after
					count++
					changed = true
				}
			}
			return changed, nil
		}
		slots, err := repairMap(inv.Slots, 0)
		if err != nil {
			return err
		}
		warehouse, err := repairMap(inv.Warehouse, 2)
		if err != nil {
			return err
		}
		accountChanged, err := repairMap(account.Slots, 12)
		if err != nil {
			return err
		}
		wornChanged := false
		wornSlots := map[int16]int{}
		for _, entry := range worn.Entries {
			if entry.SlotIndex >= 0 {
				wornSlots[entry.SlotIndex]++
			}
		}
		for key, entry := range worn.Entries {
			if entry.SlotIndex < 0 || wornSlots[entry.SlotIndex] != 1 {
				continue
			}
			stack := repo.ItemStack{ItemID: entry.ItemID, Count: 1, Bind: entry.Bind, ExpireAt: entry.ExpireAt, RawEntry: entry.RawEntry, Extra: entry.Extra}
			after, done, err := repair(stack, entry.SlotIndex)
			if err != nil {
				return err
			}
			if done {
				entry.RawEntry, entry.Extra = after.RawEntry, after.Extra
				worn.Entries[key] = entry
				count++
				wornChanged = true
			}
		}
		if slots || warehouse {
			inv.UpdatedAt = c.Now
			if err := invs.Save(ctx, inv); err != nil {
				return err
			}
		}
		if wornChanged {
			worn.UpdatedAt = c.Now
			if err := equips.Save(ctx, worn); err != nil {
				return err
			}
		}
		if accountChanged {
			account.UpdatedAt = c.Now
			if err := accounts.Save(ctx, account); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return 0, err
	}
	return count, nil
}
