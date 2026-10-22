// Package aikenupgrade owns the deterministic individual custom-grade transaction.
package aikenupgrade

import (
	"context"
	"errors"
	"fmt"
	"longheng.io/server/internal/modules/dnf/itemlock"
	"longheng.io/server/internal/modules/dnf/itemplacement"
	repo "longheng.io/server/internal/modules/dnf/repository"
	"sort"
	"strconv"
	"strings"
	"time"
)

var ErrInvalid = errors.New("aiken upgrade rejected")
var ErrMaterials = errors.New("aiken upgrade materials insufficient")

type MaterialCost struct {
	ItemID, Count int64
}

type Rules struct {
	CostPolicy     func(int64) (itemplacement.SlotPolicy, error)
	Material       int64
	Costs          [4]int64
	Options        map[byte]bool
	Validate       func(repo.ItemStack) error
	State          func(repo.ItemStack) ([5]byte, error)
	SourcePolicy   func(repo.ItemStack) (itemplacement.SlotPolicy, error)
	MaterialPolicy itemplacement.SlotPolicy
	Expired        func(repo.ItemStack, time.Time) bool
	SetCount       func(repo.ItemStack, int64) (repo.ItemStack, error)
	SetState       func(repo.ItemStack, int16, [5]byte) (repo.ItemStack, error)
}
type Command struct {
	AccountID, CharacterID string
	Slot                   int16
	Rules                  Rules
	Now                    time.Time
}
type Result struct {
	Before, After byte
	Changed       map[int16]repo.ItemStack
}
type Owner struct {
	assets   repo.AccountCharacterAssetUnitOfWork
	settings repo.SettingsRepository
}

func NewOwner(g repo.Group) (*Owner, error) {
	if g.AccountAssets == nil {
		return nil, ErrInvalid
	}
	return &Owner{g.AccountAssets, g.Settings}, nil
}
func clone(s repo.ItemStack) repo.ItemStack {
	return repo.CloneInventory(repo.InventoryRecord{Slots: map[string]repo.ItemStack{"x": s}}).Slots["x"]
}

// Match inventory projection's list/slot parsing, including historical textual
// aliases, without accepting trailing garbage or wrapping a list/slot number.
func endpoint(key string) (int64, int64, bool) {
	list, slot, ok := strings.Cut(key, ":")
	if !ok {
		return 0, 0, false
	}
	l, e := strconv.ParseInt(strings.TrimSpace(list), 10, 16)
	if e != nil || l < 0 || l > 255 {
		return 0, 0, false
	}
	n, e := strconv.ParseInt(strings.TrimSpace(slot), 10, 16)
	return l, n, e == nil && n >= 0 && n <= 32767
}

func (o *Owner) Upgrade(ctx context.Context, c Command) (Result, error) {
	var result Result
	r := c.Rules
	if o == nil || o.assets == nil || strings.TrimSpace(c.AccountID) == "" || strings.TrimSpace(c.CharacterID) == "" || r.Material <= 0 || r.Validate == nil || r.State == nil || r.SetState == nil || r.SourcePolicy == nil || r.Expired == nil || r.SetCount == nil {
		return result, ErrInvalid
	}
	if c.Now.IsZero() {
		c.Now = time.Now()
	}
	err := o.assets.WithinAccountCharacterAssets(ctx, c.AccountID, c.CharacterID, func(accounts repo.AccountInventoryRepository, chars repo.CharacterRepository, invs repo.InventoryRepository, equips repo.EquipmentRepository) error {
		ch, ok, err := chars.Load(ctx, c.CharacterID)
		if err != nil {
			return err
		}
		if !ok || ch.CharacterID != c.CharacterID || ch.AccountID != c.AccountID {
			return ErrInvalid
		}
		inv, ok, err := invs.Load(ctx, c.CharacterID)
		if err != nil {
			return err
		}
		if !ok || inv.CharacterID != c.CharacterID {
			return ErrInvalid
		}
		worn, _, err := equips.Load(ctx, c.CharacterID)
		if err != nil {
			return err
		}
		account, _, err := accounts.Load(ctx, c.AccountID)
		if err != nil {
			return err
		}
		inv = repo.CloneInventory(inv)
		if err := repo.BindInventoryContainerState(ctx, o.settings, &inv); err != nil {
			return err
		}
		key := fmt.Sprintf("0:%d", c.Slot)
		s, ok := inv.Slots[key]
		if !ok || s.Count != 1 || itemlock.IsLockedAt(s, c.Now) || r.Expired(s, c.Now) {
			return ErrInvalid
		}
		// An endpoint alias is ambiguous; existing persistent instance identities
		// must also be unique. Quality seeds and raw+0E are NOT unique IDs.
		identity := strings.TrimSpace(s.Extra["native_buff_instance"])
		duplicate := func(other repo.ItemStack) bool {
			return identity != "" && strings.TrimSpace(other.Extra["native_buff_instance"]) == identity
		}
		// Count every projected main-bag endpoint before filtering by item ID.
		// A different item or empty row can alias a material being consumed too.
		endpoints := map[int64]int{}
		for k := range inv.Slots {
			if l, n, ok := endpoint(k); ok && l == 0 {
				endpoints[n]++
			}
		}
		if endpoints[int64(c.Slot)] != 1 {
			return ErrInvalid
		}
		for k, x := range inv.Slots {
			if k == key {
				continue
			}
			if duplicate(x) {
				return ErrInvalid
			}
		}
		for _, x := range inv.Warehouse {
			if duplicate(x) {
				return ErrInvalid
			}
		}
		for _, x := range account.Slots {
			if duplicate(x) {
				return ErrInvalid
			}
		}
		for _, x := range worn.Entries {
			if duplicate(repo.ItemStack{Extra: x.Extra}) {
				return ErrInvalid
			}
		}
		if err := r.Validate(s); err != nil {
			return err
		}
		policy, err := r.SourcePolicy(s)
		if err != nil || !policy.ForInventory(&inv).Contains(0, c.Slot) {
			return ErrInvalid
		}
		state, err := r.State(s)
		if err != nil {
			return err
		}
		if state[4] >= 3 || !r.Options[state[0]] || !r.Options[state[1]] {
			return ErrInvalid
		}
		next := state[4] + 1
		cost := r.Costs[next]
		if cost <= 0 {
			return ErrInvalid
		}
		slots := []int{}
		seen := map[int]bool{}
		for k, x := range inv.Slots {
			var l, n int
			if _, e := fmt.Sscanf(k, "%d:%d", &l, &n); e != nil || l != 0 || n < 0 || n > 32767 || !r.MaterialPolicy.ForInventory(&inv).Contains(0, int16(n)) || x.ItemID != r.Material {
				continue
			}
			if k != fmt.Sprintf("0:%d", n) || seen[n] {
				return ErrInvalid
			}
			seen[n] = true
			if x.Count > 0 && !itemlock.IsLockedAt(x, c.Now) && !r.Expired(x, c.Now) {
				slots = append(slots, n)
			}
		}
		sort.Ints(slots)
		remaining := cost
		changed := map[int16]repo.ItemStack{}
		for _, n := range slots {
			if remaining == 0 {
				break
			}
			if endpoints[int64(n)] != 1 {
				return ErrInvalid
			}
			k := fmt.Sprintf("0:%d", n)
			x := clone(inv.Slots[k])
			take := x.Count
			if take > remaining {
				take = remaining
			}
			remaining -= take
			x.Count -= take
			if x.Count == 0 {
				delete(inv.Slots, k)
				changed[int16(n)] = repo.ItemStack{}
			} else {
				x, err = r.SetCount(x, x.Count)
				if err != nil {
					return err
				}
				inv.Slots[k] = x
				changed[int16(n)] = x
			}
		}
		if remaining != 0 {
			return ErrMaterials
		}
		before := state[4]
		state[4] = next
		s, err = r.SetState(s, c.Slot, state)
		if err != nil {
			return err
		}
		inv.Slots[key] = s
		changed[c.Slot] = s
		inv.UpdatedAt = c.Now
		if err := repo.SaveInventoryFields(ctx, invs, inv, repo.InventoryFieldSlots); err != nil {
			return err
		}
		result = Result{Before: before, After: next, Changed: changed}
		return nil
	})
	if err != nil {
		return Result{}, err
	}
	return result, nil
}
