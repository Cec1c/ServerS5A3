package cargo

import (
	"context"
	"errors"
	"math"
	"strconv"
	"strings"
	"time"

	"longheng.io/server/internal/modules/dnf/itemlock"
	"longheng.io/server/internal/modules/dnf/itemplacement"
	"longheng.io/server/internal/modules/dnf/jobmap"
	repo "longheng.io/server/internal/modules/dnf/repository"
)

var ErrUpgradeTicket = errors.New("container upgrade ticket conditions not met")

// These action numbers are the matched client's parsed PVF action values.
const (
	UpgradeTicketInventory uint32 = 40
	UpgradeTicketPersonal  uint32 = 45
	UpgradeTicketAccount   uint32 = 148
)

type UpgradeTicketDefinition struct {
	ItemID       int64
	PVFPath      string
	Action       uint32
	MinimumLevel int
	Jobs, Places []string
	AccountTiers []int64
}
type UpgradeTicketCommand struct {
	AccountID, CharacterID string
	Slot                   int16
	Definition             UpgradeTicketDefinition
	Now                    time.Time
	InitialSettings        repo.SettingsRecord
	Revalidate             func() bool
	Expired                func(repo.ItemStack, time.Time) bool
	SetCount               func(repo.ItemStack, int64) (repo.ItemStack, error)
}
type UpgradeTicketResult struct {
	Action        uint32
	Before, After uint16
	Remaining     int64
}

// ApplyUpgradeTicket uses the existing account/character/inventory/settings UOW
// directly. It is not a shop purchase and never charges or projects a wallet.
// The native request has no nonce: one remaining ticket permits one new use;
// retries after the final ticket is gone are rejected under the same lock.
func ApplyUpgradeTicket(ctx context.Context, g repo.Group, c UpgradeTicketCommand) (UpgradeTicketResult, error) {
	d := c.Definition
	if g.CeraShopAssets == nil || c.AccountID == "" || c.CharacterID == "" || c.Slot < 0 || c.Slot > 288 || c.Now.IsZero() || c.Revalidate == nil || c.Expired == nil || c.SetCount == nil ||
		d.ItemID <= 0 || d.ItemID > math.MaxUint32 || d.PVFPath == "" || d.MinimumLevel < 0 || len(d.Jobs) == 0 ||
		(d.Action != UpgradeTicketPersonal && d.Action != UpgradeTicketInventory && d.Action != UpgradeTicketAccount) {
		return UpgradeTicketResult{}, ErrUpgradeTicket
	}
	if d.Action == UpgradeTicketAccount {
		prev := int64(0)
		for _, v := range d.AccountTiers {
			if v <= prev || v > 240 || (v != 1 && v%8 != 0) {
				return UpgradeTicketResult{}, ErrUpgradeTicket
			}
			prev = v
		}
		if len(d.AccountTiers) < 2 || d.AccountTiers[0] != 1 {
			return UpgradeTicketResult{}, ErrUpgradeTicket
		}
	}
	scope := repo.CharacterContainerStateScope(c.CharacterID)
	var result UpgradeTicketResult
	err := g.CeraShopAssets.WithinCeraShopAssets(ctx, c.AccountID, c.CharacterID, scope, func(accounts repo.AccountRepository, characters repo.CharacterRepository, bags repo.InventoryRepository, _ repo.EquipmentRepository, settings repo.SettingsRepository) error {
		if accounts == nil || characters == nil || bags == nil || settings == nil || !c.Revalidate() {
			return ErrUpgradeTicket
		}
		a, found, e := accounts.Load(ctx, c.AccountID)
		if e != nil {
			return e
		}
		if !found || a.AccountID != c.AccountID {
			return ErrUpgradeTicket
		}
		ch, found, e := characters.Load(ctx, c.CharacterID)
		if e != nil {
			return e
		}
		if !found || ch.CharacterID != c.CharacterID || ch.AccountID != c.AccountID || ch.Level < d.MinimumLevel || ch.Level < 1 || ch.Stats["delete_flag"] != 0 {
			return ErrUpgradeTicket
		}
		job, e := strconv.Atoi(ch.Job)
		if e != nil || !jobmap.Valid(job) {
			return ErrUpgradeTicket
		}
		allowed := false
		for _, tag := range d.Jobs {
			allowed = allowed || tag == "[all]" || jobmap.MatchesQuestTag(tag, job)
		}
		if !allowed {
			return ErrUpgradeTicket
		}
		bag, found, e := bags.Load(ctx, c.CharacterID)
		if e != nil {
			return e
		}
		if !found || bag.CharacterID != c.CharacterID {
			return ErrUpgradeTicket
		}
		key := "0:" + strconv.Itoa(int(c.Slot))
		stack, found := bag.Slots[key]
		if !found || stack.ItemID != d.ItemID || stack.Count <= 0 || stack.Count > math.MaxInt32 || itemlock.IsLockedAt(stack, c.Now) || c.Expired(stack, c.Now) {
			return ErrUpgradeTicket
		}
		if kind := strings.TrimSpace(stack.Extra["item_kind"]); kind != "" && kind != "stackable" {
			return ErrUpgradeTicket
		}
		if path := strings.TrimSpace(stack.Extra["pvf_path"]); path != "" && !strings.EqualFold(strings.ReplaceAll(path, "\\", "/"), d.PVFPath) {
			return ErrUpgradeTicket
		}
		state, found, e := settings.Load(ctx, scope)
		if e != nil {
			return e
		}
		if !found {
			state = repo.CloneSettings(c.InitialSettings)
			if state.Scope != scope {
				return ErrUpgradeTicket
			}
		}
		if state.Scope != scope {
			return ErrUpgradeTicket
		}
		typed, e := repo.CharacterContainerStateFromSettings(state, c.CharacterID)
		if e != nil {
			return e
		}
		if !itemplacement.SlotOpen(0, c.Slot, &typed) {
			return ErrUpgradeTicket
		}
		a = repo.CloneAccount(a)
		bag = repo.CloneInventory(bag)
		state = repo.CloneSettings(state)
		result.Action = d.Action
		switch d.Action {
		case UpgradeTicketPersonal:
			result.Before = typed.PersonalCargoSlotCount
			if result.Before >= 264 {
				return ErrCargoMaxLevel
			}
			result.After = result.Before + 16
			state.Values["personal_cargo_list_param16"] = strconv.Itoa(int(result.After))
		case UpgradeTicketInventory:
			result.Before = typed.MainSlotCount
			if result.Before >= 24 {
				return ErrCargoMaxLevel
			}
			result.After = result.Before + 8
			state.Values["main_list_param16"] = strconv.Itoa(int(result.After))
		case UpgradeTicketAccount:
			before, e := strconv.ParseInt(a.Metadata["account_cargo_level"], 10, 64)
			// Current account cargo projection and native create gate use the
			// persisted positive capacity. Legacy rows may lack the redundant flag.
			if e != nil || before <= 0 {
				return ErrCargoNotCreated
			}
			after := int64(0)
			for i, v := range d.AccountTiers {
				if v == before && i+1 < len(d.AccountTiers) {
					after = d.AccountTiers[i+1]
					break
				}
			}
			if after == 0 {
				return ErrCargoMaxLevel
			}
			result.Before = uint16(before)
			result.After = uint16(after)
			a.Metadata["account_cargo_level"] = strconv.FormatInt(after, 10)
			a.Metadata["account_cargo_created"] = "true"
			a.UpdatedAt = c.Now
		}
		result.Remaining = stack.Count - 1
		if result.Remaining == 0 {
			delete(bag.Slots, key)
		} else {
			updated, err := c.SetCount(bag.Slots[key], result.Remaining)
			if err != nil {
				return err
			}
			bag.Slots[key] = updated
		}
		bag.UpdatedAt = c.Now
		// Save the debit first; any subsequent save error must roll it back in UOW.
		if e = repo.SaveInventoryFields(ctx, bags, bag, repo.InventoryFieldSlots); e != nil {
			return e
		}
		if d.Action == UpgradeTicketAccount {
			if e = accounts.Save(ctx, a); e != nil {
				return e
			}
		} else {
			state.Values["source"] = "current_exe_container_upgrade_ticket"
			state.Values["container_ticket_last_item"] = strconv.FormatInt(d.ItemID, 10)
			state.Values["container_ticket_last_from"] = strconv.Itoa(int(result.Before))
			state.Values["container_ticket_last_to"] = strconv.Itoa(int(result.After))
			state.UpdatedAt = c.Now
			if e = settings.Save(ctx, state); e != nil {
				return e
			}
		}
		if !c.Revalidate() {
			return ErrUpgradeTicket
		}
		return nil
	})
	if err != nil {
		return UpgradeTicketResult{}, err
	}
	return result, nil
}
