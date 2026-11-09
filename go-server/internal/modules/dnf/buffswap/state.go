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

// MaxSkillGroups bounds the number of independently persisted Buff skills on
// one character. Reaching the limit rejects only creation of a new group;
// existing groups remain readable and writable.
const MaxSkillGroups = 256

// SelectedGroupCount is the native manager's fixed group count. The current
// client addresses groups 0 and 1; no other group is valid for registration.
const SelectedGroupCount = 2

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
	WireProfile    string                      `json:"wire_profile,omitempty"`
	Skill          uint16                      `json:"skill"`
	Slots          map[byte]Binding            `json:"slots"`
	SkillSlots     map[uint16]map[byte]Binding `json:"skill_slots,omitempty"`
	SelectedSkills map[byte]uint16             `json:"selected_skills,omitempty"`
	CurrentGroup   byte                        `json:"current_group,omitempty"`
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
	if state.CurrentGroup >= SelectedGroupCount {
		return State{}, false, ErrInvalid
	}
	for group, skill := range state.SelectedSkills {
		if group >= SelectedGroupCount || skill == 0 {
			return State{}, false, ErrInvalid
		}
	}
	if err := validateSlotSet(state.WireProfile, state.Skill, state.Slots); err != nil {
		return State{}, false, err
	}
	if state.SkillSlots != nil {
		if len(state.SkillSlots) > MaxSkillGroups {
			return State{}, false, ErrInvalid
		}
		for skill, slots := range state.SkillSlots {
			if err := validateSlotSet(state.WireProfile, skill, slots); err != nil {
				return State{}, false, err
			}
		}
		// The top-level fields are the active persisted view. Reconcile that
		// view into the active group so instance migration write-backs cannot
		// be overwritten by a stale group copy. Other groups remain untouched.
		_, activeFound := state.SkillSlots[state.Skill]
		if state.WireProfile == CurrentWireProfile && state.Skill != 0 && (activeFound || len(state.Slots) > 0) {
			if !activeFound && len(state.SkillSlots) >= MaxSkillGroups {
				return State{}, false, ErrInvalid
			}
			state.SkillSlots[state.Skill] = cloneBindingMap(state.Slots)
		}
	}
	return state, true, nil
}

func validateSlotSet(wireProfile string, skill uint16, slots map[byte]Binding) error {
	for slot, b := range slots {
		if slot >= SlotCount {
			return ErrInvalid
		}
		c := Command{Skill: skill, Slot: slot, List: b.List, Index: b.Index}
		valid := c.valid() && b.List <= 3
		if wireProfile == CurrentWireProfile {
			valid = c.validCurrent() && (b.List <= 3 || b.List == 7)
		}
		if !valid || b.ItemID <= 0 {
			return ErrInvalid
		}
		if err := validateBindingIdentity(b); err != nil {
			return err
		}
	}
	return nil
}

func cloneBindingMap(source map[byte]Binding) map[byte]Binding {
	if source == nil {
		return nil
	}
	result := make(map[byte]Binding, len(source))
	for slot, binding := range source {
		result[slot] = binding
	}
	return result
}

func cloneSkillSlotMap(source map[uint16]map[byte]Binding) map[uint16]map[byte]Binding {
	if source == nil {
		return nil
	}
	result := make(map[uint16]map[byte]Binding, len(source))
	for skill, slots := range source {
		result[skill] = cloneBindingMap(slots)
	}
	return result
}

func cloneSelectedSkills(source map[byte]uint16) map[byte]uint16 {
	if source == nil {
		return nil
	}
	result := make(map[byte]uint16, len(source))
	for group, skill := range source {
		result[group] = skill
	}
	return result
}

func ensureSkillSlots(state *State) {
	if state == nil || state.SkillSlots != nil {
		return
	}
	state.SkillSlots = make(map[uint16]map[byte]Binding, 1)
	if len(state.Slots) > 0 {
		state.SkillSlots[state.Skill] = cloneBindingMap(state.Slots)
	}
}

// ensureSelectedSkills migrates a pre-group record only into native group 0.
// Group 1 never inherits that legacy selection or its equipment tree.
func ensureSelectedSkills(state *State) {
	if state == nil || state.SelectedSkills != nil {
		return
	}
	state.SelectedSkills = make(map[byte]uint16, 1)
	if state.Skill != 0 {
		state.SelectedSkills[0] = state.Skill
	}
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
	return ApplyCurrentWithWornResolver(ctx, repos, characterID, accountID, c, nil)
}

// ApplyCurrentGroup is ApplyCurrentGroupWithWornResolver without a caller
// supplied equipment projection. Group must be native group 0 or 1.
func ApplyCurrentGroup(ctx context.Context, repos repo.Group, characterID, accountID string, group byte, c Command) error {
	return ApplyCurrentGroupWithWornResolver(ctx, repos, characterID, accountID, group, c, nil)
}

// WornSlotResolver is a pure client projection rule. Resolve the wire slot
// against the locked owner snapshot before allocating a physical identity.
// Persistent bindings continue to store repository positions, never wire aliases.
type WornSlotResolver func(repo.EquipmentEntry) (uint64, bool)

func ApplyCurrentWithWornResolver(ctx context.Context, repos repo.Group, characterID, accountID string, c Command, resolve WornSlotResolver) error {
	return repo.WithinBuffOwnedSnapshot(ctx, repos, characterID, accountID, func(tx repo.Group) error {
		resolved, err := resolveCurrentWornSlot(ctx, tx, characterID, c, resolve)
		if err != nil {
			return err
		}
		return applyRegistration(ctx, tx, characterID, accountID, resolved, CurrentWireProfile)
	})
}

// ApplyCurrentGroupWithWornResolver binds one native client group to exactly
// one skill tree in the same owned transaction as the six-byte registration.
// Skill zero cancels only this group; every other group and every registered
// skill tree remains persisted.
func ApplyCurrentGroupWithWornResolver(ctx context.Context, repos repo.Group, characterID, accountID string, group byte, c Command, resolve WornSlotResolver) error {
	if group >= SelectedGroupCount {
		return ErrInvalid
	}
	return repo.WithinBuffOwnedSnapshot(ctx, repos, characterID, accountID, func(tx repo.Group) error {
		resolved, err := resolveCurrentWornSlot(ctx, tx, characterID, c, resolve)
		if err != nil {
			return err
		}
		return applyCurrentGroupRegistration(ctx, tx, characterID, accountID, group, resolved)
	})
}

func resolveCurrentWornSlot(ctx context.Context, tx repo.Group, characterID string, c Command, resolve WornSlotResolver) (Command, error) {
	if c.List != 3 || resolve == nil {
		return c, nil
	}
	if !c.validCurrent() || tx.Equipment == nil {
		return Command{}, ErrInvalid
	}
	worn, found, err := tx.Equipment.Load(ctx, characterID)
	if err != nil {
		return Command{}, err
	}
	if !found || worn.CharacterID != characterID {
		return Command{}, ErrInvalid
	}
	matches := 0
	var storage uint16
	for key, entry := range worn.Entries {
		if entry.ItemID <= 0 {
			continue
		}
		index, ok := resolve(entry)
		if !ok || index != uint64(c.Index) {
			continue
		}
		if entry.SlotIndex < 0 || entry.SlotIndex >= SlotCount || key != strconv.Itoa(int(entry.SlotIndex)) {
			return Command{}, ErrInvalid
		}
		matches++
		storage = uint16(entry.SlotIndex)
	}
	if matches != 1 {
		return Command{}, ErrInvalid
	}
	c.Index = storage
	return c, nil
}

func applyRegistration(ctx context.Context, repos repo.Group, characterID, accountID string, c Command, wireProfile string) error {
	valid := c.valid()
	if wireProfile == CurrentWireProfile {
		valid = c.validCurrent()
	}
	if !valid {
		return ErrInvalid
	}
	if wireProfile == CurrentWireProfile {
		return applyCurrentRegistration(ctx, repos, characterID, accountID, c)
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
		for _, slots := range s.SkillSlots {
			for _, binding := range slots {
				if binding.List == 7 {
					return fmt.Errorf("%w: historical profile cannot retain current pet binding", ErrInvalid)
				}
			}
		}
	}
	s.Skill = c.Skill
	s.WireProfile = wireProfile
	return saveState(ctx, repos, characterID, s, wireProfile)
}

// applyCurrentRegistration keeps one slot set per current-client Buff skill.
// The top-level State.Skill/Slots pair remains the active view used by all
// existing projections. SkillSlots retains the inactive sets.
func applyCurrentRegistration(ctx context.Context, repos repo.Group, characterID, accountID string, c Command) error {
	s, _, err := Load(ctx, repos, characterID, accountID)
	if err != nil {
		return err
	}
	// Ungrouped current and historical rows belong only to the skill that was
	// persisted with them. A later selection of another skill starts empty
	// instead of copying this active view forward.
	ensureSkillSlots(&s)

	if c.Skill == 0 {
		// Skill zero is the current-client disable state, not a real group.
		// Preserve every registered skill and expose an empty active view.
		s.Skill = 0
		s.Slots = make(map[byte]Binding)
		s.WireProfile = CurrentWireProfile
		return saveState(ctx, repos, characterID, s, CurrentWireProfile)
	}

	group, groupFound := s.SkillSlots[c.Skill]
	if c.Slot < SlotCount {
		next := cloneBindingMap(group)
		if next == nil {
			next = make(map[byte]Binding)
		}
		if c.List == 44 {
			delete(next, c.Slot)
			if len(next) == 0 {
				delete(s.SkillSlots, c.Skill)
			} else {
				s.SkillSlots[c.Skill] = next
			}
		} else {
			if !groupFound && len(s.SkillSlots) >= MaxSkillGroups {
				return fmt.Errorf("%w: skill group limit reached", ErrInvalid)
			}
			binding, err := registerOwnedBinding(ctx, repos, characterID, c.List, c.Index)
			if err != nil {
				return err
			}
			next[c.Slot] = binding
			s.SkillSlots[c.Skill] = next
		}
	}

	s.Slots = cloneBindingMap(s.SkillSlots[c.Skill])
	if s.Slots == nil {
		s.Slots = make(map[byte]Binding)
	}
	s.Skill = c.Skill
	s.WireProfile = CurrentWireProfile
	return saveState(ctx, repos, characterID, s, CurrentWireProfile)
}

func applyCurrentGroupRegistration(ctx context.Context, repos repo.Group, characterID, accountID string, group byte, c Command) error {
	if group >= SelectedGroupCount {
		return ErrInvalid
	}
	if c.Skill == 0 && c.Slot < SlotCount {
		return ErrInvalid
	}
	valid := c.validCurrent()
	if !valid {
		return ErrInvalid
	}
	s, _, err := Load(ctx, repos, characterID, accountID)
	if err != nil {
		return err
	}
	ensureSkillSlots(&s)
	ensureSelectedSkills(&s)

	if c.Skill == 0 {
		delete(s.SelectedSkills, group)
		s.CurrentGroup = group
		s.Skill = 0
		s.Slots = make(map[byte]Binding)
		s.WireProfile = CurrentWireProfile
		return saveState(ctx, repos, characterID, s, CurrentWireProfile)
	}

	tree, treeFound := s.SkillSlots[c.Skill]
	if c.Slot < SlotCount {
		next := cloneBindingMap(tree)
		if next == nil {
			next = make(map[byte]Binding)
		}
		if c.List == 44 {
			delete(next, c.Slot)
			if len(next) == 0 {
				delete(s.SkillSlots, c.Skill)
			} else {
				s.SkillSlots[c.Skill] = next
			}
		} else {
			if !treeFound && len(s.SkillSlots) >= MaxSkillGroups {
				return fmt.Errorf("%w: skill group limit reached", ErrInvalid)
			}
			binding, err := registerOwnedBinding(ctx, repos, characterID, c.List, c.Index)
			if err != nil {
				return err
			}
			next[c.Slot] = binding
			s.SkillSlots[c.Skill] = next
		}
	}

	s.SelectedSkills[group] = c.Skill
	s.CurrentGroup = group
	s.Slots = cloneBindingMap(s.SkillSlots[c.Skill])
	if s.Slots == nil {
		s.Slots = make(map[byte]Binding)
	}
	s.Skill = c.Skill
	s.WireProfile = CurrentWireProfile
	return saveState(ctx, repos, characterID, s, CurrentWireProfile)
}

func saveState(ctx context.Context, repos repo.Group, characterID string, s State, wireProfile string) error {
	// Persist independent maps: callers may continue using either the active
	// view or a skill group after this function returns.
	s.Slots = cloneBindingMap(s.Slots)
	s.SkillSlots = cloneSkillSlotMap(s.SkillSlots)
	s.SelectedSkills = cloneSelectedSkills(s.SelectedSkills)
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

// Resolve follows the registered physical instance in native lists0..3 (plus
// current-profile7). Legacy bindings without an instance are resolved only
// when their template is unique. A stored coordinate is not enough to select
// one of two same-template instances: doing so would make an old registration
// jump to the wrong physical item. Missing or ambiguous bindings stay
// persisted but are omitted from projection.
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
	byTemplate := make(map[int64][]location)
	for loc, itemID := range all {
		byTemplate[itemID] = append(byTemplate[itemID], loc)
	}
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
		candidates := byTemplate[b.ItemID]
		if len(candidates) == 1 {
			candidate := candidates[0]
			b.List, b.Index = candidate.list, candidate.index
			if b.Instance == "" && identities[candidate] != "" {
				b.Instance = identities[candidate]
				b.SourceKind = SourceCurrentInstance
			}
			out.Slots[slot] = b
		}
	}
	return out, len(state.Slots) - len(out.Slots), nil
}
