// Package buffswap owns the native current-client buff registration state.
package buffswap

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	repo "longheng.io/server/internal/modules/dnf/repository"
)

const SlotCount = 33

// CurrentWireProfile is proven by bb428/82e VA994D20 and VA998890.
const CurrentWireProfile = "current1426-1368"

var ErrInvalid = errors.New("invalid native buff-swap registration")

// Command follows DNF.exe RVA 3C9FA0: word skill, byte equipment slot,
// byte source list, word source slot. The first word is NOT a sequence prefix.
type Command struct {
	Skill uint16
	Slot  byte
	List  byte
	Index uint16
}

func Parse(body []byte) (Command, error) {
	if len(body) != 6 {
		return Command{}, ErrInvalid
	}
	c := Command{binary.LittleEndian.Uint16(body), body[2], body[3], binary.LittleEndian.Uint16(body[4:])}
	if !c.valid() {
		return Command{}, ErrInvalid
	}
	return c, nil
}

// ParseCurrent reads the current1426 producer's six bytes. Its null-list value
// is45; normalize only that exact sentinel to the historical internal value44.
// Ordinary item bindings retain the same repository location semantics.
func ParseCurrent(body []byte) (Command, error) {
	if len(body) != 6 {
		return Command{}, ErrInvalid
	}
	c := Command{binary.LittleEndian.Uint16(body), body[2], body[3], binary.LittleEndian.Uint16(body[4:])}
	if c.List == 45 && c.Index == 0xffff {
		c.List = 44
	} else if c.List > 3 && c.List != 7 {
		return Command{}, ErrInvalid
	}
	if !c.validCurrent() {
		return Command{}, ErrInvalid
	}
	return c, nil
}

func (c Command) validCurrent() bool {
	return c.valid() || (c.Slot < SlotCount && c.List == 7 && c.Index < 0x8000)
}

func (c Command) valid() bool {
	if c.Slot > SlotCount {
		return false
	}
	if c.List == 44 && c.Index == 0xffff {
		return true
	}
	return c.Slot < SlotCount && c.List <= 3 && c.Index < 0x8000 && (c.List != 3 || c.Index < SlotCount)
}

type Binding struct {
	List          byte   `json:"list"`
	Index         uint16 `json:"index"`
	ItemID        int64  `json:"item_id"`
	Instance      string `json:"instance,omitempty"`
	SourceKind    string `json:"source_kind,omitempty"`
	LegacyItemUID int64  `json:"legacy_item_uid,omitempty"`
}

type State struct {
	WireProfile string           `json:"wire_profile,omitempty"`
	Skill       uint16           `json:"skill"`
	Slots       map[byte]Binding `json:"slots"`
}

func Scope(characterID string) string {
	return "character:" + strings.TrimSpace(characterID) + ":buff_swap_native"
}

func checkOwner(ctx context.Context, repos repo.Group, characterID, accountID string) error {
	if repos.Character == nil || repos.Settings == nil || strings.TrimSpace(characterID) == "" || strings.TrimSpace(accountID) == "" {
		return errors.New("buff-swap owner unavailable")
	}
	c, found, err := repos.Character.Load(ctx, characterID)
	if err != nil {
		return err
	}
	if !found || c.AccountID != accountID {
		return errors.New("buff-swap character ownership mismatch")
	}
	return nil
}

func Load(ctx context.Context, repos repo.Group, characterID, accountID string) (State, bool, error) {
	if err := checkOwner(ctx, repos, characterID, accountID); err != nil {
		return State{}, false, err
	}
	r, found, err := repos.Settings.Load(ctx, Scope(characterID))
	if err != nil || !found {
		return State{}, false, err
	}
	var state State
	if err := json.Unmarshal([]byte(r.Values["state"]), &state); err != nil {
		return State{}, false, err
	}
	if state.WireProfile != "" && state.WireProfile != CurrentWireProfile {
		return State{}, false, ErrInvalid
	}
	for slot, b := range state.Slots {
		c := Command{Skill: state.Skill, Slot: slot, List: b.List, Index: b.Index}
		valid := c.valid() && b.List <= 3
		if state.WireProfile == CurrentWireProfile {
			valid = c.validCurrent() && (b.List <= 3 || b.List == 7)
		}
		if !valid || b.ItemID <= 0 {
			return State{}, false, ErrInvalid
		}
		if err := validateBindingIdentity(b); err != nil {
			return State{}, false, err
		}
	}
	return state, true, nil
}

type location struct {
	list  byte
	index uint16
}

func items(ctx context.Context, repos repo.Group, characterID string) (map[location]int64, error) {
	if repos.Inventory == nil || repos.Equipment == nil {
		return nil, errors.New("buff-swap item repositories unavailable")
	}
	result := make(map[location]int64)
	if repos.Inventory != nil {
		r, _, err := repos.Inventory.Load(ctx, characterID)
		if err != nil {
			return nil, err
		}
		for key, item := range r.Slots {
			parts := strings.Split(key, ":")
			if len(parts) != 2 {
				continue
			}
			list, e1 := strconv.ParseUint(parts[0], 10, 8)
			index, e2 := strconv.ParseUint(parts[1], 10, 16)
			if e1 == nil && e2 == nil && (list <= 2 || list == 7) && index < 0x8000 && item.ItemID > 0 && (item.Count > 0 || (list == 1 && item.Count == 0)) {
				result[location{byte(list), uint16(index)}] = item.ItemID
			}
		}
	}
	if repos.Equipment != nil {
		r, _, err := repos.Equipment.Load(ctx, characterID)
		if err != nil {
			return nil, err
		}
		for key, item := range r.Entries {
			index, err := strconv.ParseUint(key, 10, 16)
			if err == nil && index < SlotCount && item.ItemID > 0 {
				result[location{3, uint16(index)}] = item.ItemID
			}
		}
	}
	return result, nil
}

// Apply saves explicit registration/removal/skill-selection through Settings.
// Old heuristic skillswitch rows are intentionally not migrated.
func Apply(ctx context.Context, repos repo.Group, characterID, accountID string, c Command) error {
	return repo.WithinBuffOwnedSnapshot(ctx, repos, characterID, accountID, func(tx repo.Group) error { return applyRegistration(ctx, tx, characterID, accountID, c, "") })
}

// ApplyCurrent records wire provenance in the same transaction as registration.
// This survives skill-only selection, item removal and later cold Load/Resolve.
func ApplyCurrent(ctx context.Context, repos repo.Group, characterID, accountID string, c Command) error {
	return repo.WithinBuffOwnedSnapshot(ctx, repos, characterID, accountID, func(tx repo.Group) error {
		return applyRegistration(ctx, tx, characterID, accountID, c, CurrentWireProfile)
	})
}

func applyRegistration(ctx context.Context, repos repo.Group, characterID, accountID string, c Command, wireProfile string) error {
	valid := c.valid()
	if wireProfile == CurrentWireProfile {
		valid = c.validCurrent()
	}
	if !valid {
		return ErrInvalid
	}
	s, _, err := Load(ctx, repos, characterID, accountID)
	if err != nil {
		return err
	}
	if s.Slots == nil {
		s.Slots = make(map[byte]Binding)
	}
	if c.Slot < SlotCount {
		if c.List == 44 {
			delete(s.Slots, c.Slot)
		} else {
			binding, err := registerOwnedBinding(ctx, repos, characterID, c.List, c.Index)
			if err != nil {
				return err
			}
			s.Slots[c.Slot] = binding
		}
	}
	if wireProfile != CurrentWireProfile {
		for _, binding := range s.Slots {
			if binding.List == 7 {
				return fmt.Errorf("%w: historical profile cannot retain current pet binding", ErrInvalid)
			}
		}
	}
	s.Skill = c.Skill
	s.WireProfile = wireProfile
	raw, err := json.Marshal(s)
	if err != nil {
		return err
	}
	source := "current_exe_3C9FA0"
	if wireProfile == CurrentWireProfile {
		source = "current_exe_994D20"
	}
	return repos.Settings.Save(ctx, repo.SettingsRecord{Scope: Scope(characterID), Values: map[string]string{"state": string(raw), "source": source}, UpdatedAt: time.Now().UTC()})
}

// Resolve uses the registered position when it still holds that item. After
// wearing/removing/moving it, follow a unique matching item in native lists0..3 (plus current-profile7).
// Missing or ambiguous bindings stay persisted but are omitted from projection.
func Resolve(ctx context.Context, repos repo.Group, characterID string, state State) (State, int, error) {
	for _, b := range state.Slots {
		if b.SourceKind == SourceLegacyUID || b.LegacyItemUID != 0 {
			return State{}, 0, repo.ErrBuffOwnedSnapshotUnavailable
		}
	}
	all, err := items(ctx, repos, characterID)
	if err != nil {
		return State{}, 0, err
	}
	if state.WireProfile != CurrentWireProfile {
		// Historical1428 bindings cannot migrate into a current-only pet list.
		for loc := range all {
			if loc.list == 7 {
				delete(all, loc)
			}
		}
	}
	identities, err := itemInstances(ctx, repos, characterID)
	if err != nil {
		return State{}, 0, err
	}
	out := State{WireProfile: state.WireProfile, Skill: state.Skill, Slots: make(map[byte]Binding)}
	for slot, b := range state.Slots {
		if b.Instance != "" {
			matches := 0
			var candidate location
			for loc := range all {
				if identities[loc] == b.Instance {
					matches++
					candidate = loc
				}
			}
			if matches == 1 && all[candidate] == b.ItemID {
				b.List, b.Index = candidate.list, candidate.index
				out.Slots[slot] = b
			}
			continue
		}
		if all[location{b.List, b.Index}] == b.ItemID {
			out.Slots[slot] = b
			continue
		}
		var candidate location
		matches := 0
		for loc, id := range all {
			if id == b.ItemID {
				candidate = loc
				matches++
			}
		}
		if matches == 1 {
			b.List, b.Index = candidate.list, candidate.index
			out.Slots[slot] = b
		}
	}
	return out, len(state.Slots) - len(out.Slots), nil
}
