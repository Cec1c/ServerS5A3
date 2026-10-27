package aikenupgrade

import (
	"context"
	"fmt"
	"math"
	"sort"
	"strings"
	"time"

	"longheng.io/server/internal/modules/dnf/itemlock"
	repo "longheng.io/server/internal/modules/dnf/repository"
)

// SetPlan is computed from the authoritative, transaction-local equipment.
// Target writes and material consumption commit together. Wire notifications
// belong to the caller and must never cause this transaction to be retried.
type SetPlan struct {
	Costs  []MaterialCost
	Record Record
}

type SetRules struct {
	Item  Rules
	Plan  func([]repo.ItemStack, []byte) (SetPlan, error)
	Write func(repo.ItemStack, int16, Record) (repo.ItemStack, error)
	// Existing recognizes only validated Aiken records. Replan accounts for
	// the prior per-character record; Write with an empty record retires it.
	Existing func(repo.ItemStack) (Record, bool, error)
	Replan   func([]repo.ItemStack, []byte, []Record) (SetPlan, error)
}

type SetCommand struct {
	AccountID, CharacterID string
	Request                SetRequest
	Rules                  SetRules
	Now                    time.Time
}

type SetResult struct {
	Record                             Record
	Changed                            map[int16]repo.ItemStack
	WarehouseChanged, EquipmentChanged bool
}

// ApplySet deliberately takes no caller-supplied costs or item definitions.
// Plan receives deep copies of the selected items under the asset lock.
func (o *Owner) ApplySet(ctx context.Context, c SetCommand) (SetResult, error) {
	var result SetResult
	r := c.Rules.Item
	if o == nil || o.assets == nil || strings.TrimSpace(c.AccountID) == "" || strings.TrimSpace(c.CharacterID) == "" ||
		c.Rules.Plan == nil || c.Rules.Write == nil || r.Validate == nil || r.SourcePolicy == nil || r.Expired == nil || r.SetCount == nil || r.CostPolicy == nil ||
		len(c.Request.Targets) < 3 || len(c.Request.Targets) > 6 || len(c.Request.Options) < 1 || len(c.Request.Options) > 4 {
		return result, ErrInvalid
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if c.Now.IsZero() {
		c.Now = time.Now()
	}
	options := append([]byte(nil), c.Request.Options...)
	seenOptions := map[byte]bool{}
	for _, option := range options {
		if option == 0 || seenOptions[option] {
			return result, ErrInvalid
		}
		seenOptions[option] = true
	}
	targets := append([]uint16(nil), c.Request.Targets...)
	seenTargets := map[uint16]bool{}
	for _, target := range targets {
		if target > math.MaxInt16 || seenTargets[target] {
			return result, ErrInvalid
		}
		seenTargets[target] = true
	}
	err := o.assets.WithinAccountCharacterAssets(ctx, c.AccountID, c.CharacterID, func(accounts repo.AccountInventoryRepository, chars repo.CharacterRepository, invs repo.InventoryRepository, equips repo.EquipmentRepository) error {
		ch, found, err := chars.Load(ctx, c.CharacterID)
		if err != nil {
			return err
		}
		if !found || ch.CharacterID != c.CharacterID || ch.AccountID != c.AccountID {
			return ErrInvalid
		}
		inv, found, err := invs.Load(ctx, c.CharacterID)
		if err != nil {
			return err
		}
		if !found || inv.CharacterID != c.CharacterID {
			return ErrInvalid
		}
		worn, wornFound, err := equips.Load(ctx, c.CharacterID)
		if err != nil {
			return err
		}
		if wornFound && worn.CharacterID != c.CharacterID {
			return ErrInvalid
		}
		account, accountFound, err := accounts.Load(ctx, c.AccountID)
		if err != nil {
			return err
		}
		if accountFound && account.AccountID != c.AccountID {
			return ErrInvalid
		}
		inv = repo.CloneInventory(inv)
		worn = repo.CloneEquipment(worn)
		if err := repo.BindInventoryContainerState(ctx, o.settings, &inv); err != nil {
			return err
		}
		endpoints := map[int64]int{}
		identities := map[string]int{}
		countIdentity := func(s repo.ItemStack) {
			if id := strings.TrimSpace(s.Extra["native_buff_instance"]); id != "" {
				identities[id]++
			}
		}
		for key, s := range inv.Slots {
			if l, n, ok := endpoint(key); ok && l == 0 {
				endpoints[n]++
			}
			countIdentity(s)
		}
		for _, s := range inv.Warehouse {
			countIdentity(s)
		}
		for _, s := range account.Slots {
			countIdentity(s)
		}
		for _, s := range worn.Entries {
			countIdentity(repo.ItemStack{Extra: s.Extra})
		}
		selected := make([]repo.ItemStack, 0, len(targets))
		for _, target := range targets {
			slot := int16(target)
			s, found := inv.Slots[fmt.Sprintf("0:%d", slot)]
			if !found || s.ItemID <= 0 || s.Count != 1 || endpoints[int64(slot)] != 1 || itemlock.IsLockedAt(s, c.Now) || r.Expired(s, c.Now) {
				return ErrInvalid
			}
			if id := strings.TrimSpace(s.Extra["native_buff_instance"]); id != "" && identities[id] != 1 {
				return ErrInvalid
			}
			if err := r.Validate(s); err != nil {
				return err
			}
			policy, err := r.SourcePolicy(s)
			if err != nil || !policy.ForInventory(&inv).Contains(0, slot) {
				return ErrInvalid
			}
			selected = append(selected, clone(s))
		}
		var previous []Record
		if c.Rules.Existing != nil {
			byKey := map[byte]Record{}
			collect := func(s repo.ItemStack) error {
				record, found, err := c.Rules.Existing(clone(s))
				if err != nil {
					return err
				}
				if !found {
					return nil
				}
				record.Selector = 0
				if old, exists := byKey[record.Record0]; exists && old != record {
					return ErrInvalid
				}
				byKey[record.Record0] = record
				return nil
			}
			for _, s := range inv.Slots {
				if err := collect(s); err != nil {
					return err
				}
			}
			for _, s := range inv.Warehouse {
				if err := collect(s); err != nil {
					return err
				}
			}
			for _, s := range worn.Entries {
				if err := collect(repo.ItemStack{ItemID: s.ItemID, Count: 1, Bind: s.Bind, ExpireAt: s.ExpireAt, RawEntry: s.RawEntry, Extra: s.Extra}); err != nil {
					return err
				}
			}
			for _, record := range byKey {
				previous = append(previous, record)
			}
			sort.Slice(previous, func(i, j int) bool { return previous[i].Record0 < previous[j].Record0 })
		}
		var plan SetPlan
		if c.Rules.Replan != nil {
			plan, err = c.Rules.Replan(selected, options, previous)
		} else {
			plan, err = c.Rules.Plan(selected, options)
		}
		if err != nil {
			return err
		}
		if len(plan.Costs) == 0 {
			return ErrInvalid
		}
		changed := map[int16]repo.ItemStack{}
		seenMaterials := map[int64]bool{}
		for _, cost := range plan.Costs {
			if cost.ItemID <= 0 || cost.ItemID > math.MaxUint32 || cost.Count < 0 || cost.Count > math.MaxInt32 || seenMaterials[cost.ItemID] {
				return ErrInvalid
			}
			seenMaterials[cost.ItemID] = true
			policy, err := r.CostPolicy(cost.ItemID)
			if err != nil {
				return err
			}
			policy = policy.ForInventory(&inv)
			slots := []int{}
			for key, s := range inv.Slots {
				if s.ItemID != cost.ItemID {
					continue
				}
				l, n, ok := endpoint(key)
				if !ok || l != 0 || !policy.Contains(0, int16(n)) {
					continue
				}
				if key != fmt.Sprintf("0:%d", n) || endpoints[n] != 1 || seenTargets[uint16(n)] {
					return ErrInvalid
				}
				if s.Count > 0 && !itemlock.IsLockedAt(s, c.Now) && !r.Expired(s, c.Now) {
					slots = append(slots, int(n))
				}
			}
			sort.Ints(slots)
			left := cost.Count
			for _, n := range slots {
				if left == 0 {
					break
				}
				key := fmt.Sprintf("0:%d", n)
				s := clone(inv.Slots[key])
				take := s.Count
				if take > left {
					take = left
				}
				left -= take
				s.Count -= take
				if s.Count == 0 {
					delete(inv.Slots, key)
					changed[int16(n)] = repo.ItemStack{}
				} else {
					s, err = r.SetCount(s, s.Count)
					if err != nil {
						return err
					}
					inv.Slots[key] = s
					changed[int16(n)] = s
				}
			}
			if left != 0 {
				return ErrMaterials
			}
		}
		warehouseChanged, equipmentChanged := false, false
		if c.Rules.Existing != nil {
			clear := func(s repo.ItemStack, slot int16) (repo.ItemStack, bool, error) {
				record, found, err := c.Rules.Existing(clone(s))
				if err != nil || !found || record.Record0 != plan.Record.Record0 {
					return s, false, err
				}
				after, err := c.Rules.Write(clone(s), slot, Record{})
				if err != nil {
					return s, false, err
				}
				if after.ItemID != s.ItemID || after.Count != s.Count || after.Bind != s.Bind || !after.ExpireAt.Equal(s.ExpireAt) {
					return s, false, ErrInvalid
				}
				return after, true, nil
			}
			for key, s := range inv.Slots {
				l, n, valid := endpoint(key)
				if !valid || l != 0 {
					continue
				}
				if seenTargets[uint16(n)] {
					continue
				}
				after, changedMember, err := clear(s, int16(n))
				if err != nil {
					return err
				}
				if changedMember {
					if key != fmt.Sprintf("0:%d", n) || endpoints[n] != 1 {
						return ErrInvalid
					}
					inv.Slots[key] = after
					changed[int16(n)] = clone(after)
				}
			}
			for key, s := range inv.Warehouse {
				after, changedMember, err := clear(s, 0)
				if err != nil {
					return err
				}
				if changedMember {
					inv.Warehouse[key] = after
					warehouseChanged = true
				}
			}
			for key, e := range worn.Entries {
				s := repo.ItemStack{ItemID: e.ItemID, Count: 1, Bind: e.Bind, ExpireAt: e.ExpireAt, RawEntry: e.RawEntry, Extra: e.Extra}
				after, changedMember, err := clear(s, e.SlotIndex)
				if err != nil {
					return err
				}
				if changedMember {
					e.RawEntry = after.RawEntry
					e.Extra = after.Extra
					worn.Entries[key] = e
					equipmentChanged = true
				}
			}
		}
		for _, target := range targets {
			slot := int16(target)
			key := fmt.Sprintf("0:%d", slot)
			before := inv.Slots[key]
			after, err := c.Rules.Write(clone(before), slot, plan.Record)
			if err != nil {
				return err
			}
			if after.ItemID != before.ItemID || after.Count != 1 || after.Bind != before.Bind || !after.ExpireAt.Equal(before.ExpireAt) {
				return ErrInvalid
			}
			inv.Slots[key] = after
			changed[slot] = clone(after)
		}
		inv.UpdatedAt = c.Now
		fields := []repo.InventoryField{repo.InventoryFieldSlots}
		if warehouseChanged {
			fields = append(fields, repo.InventoryFieldWarehouse)
		}
		if err := repo.SaveInventoryFields(ctx, invs, inv, fields...); err != nil {
			return err
		}
		if equipmentChanged {
			worn.UpdatedAt = c.Now
			if err := repo.SaveEquipmentFields(ctx, equips, worn, repo.EquipmentFieldEntries); err != nil {
				return err
			}
		}
		result = SetResult{Record: plan.Record, Changed: changed, WarehouseChanged: warehouseChanged, EquipmentChanged: equipmentChanged}
		return nil
	})
	if err != nil {
		return SetResult{}, err
	}
	return result, nil
}
