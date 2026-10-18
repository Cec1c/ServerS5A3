package darkknightcombo

import (
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"

	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	dnfskill "longheng.io/server/internal/modules/dnf/skill"
)

var fixedComboRootPathRE = regexp.MustCompile(`(?i)(^|/)comboset([0-5])\.skl$`)

const (
	ordinaryQuickSlotEnd   = 6
	extensionQuickSlotFrom = 198
	extensionQuickSlotEnd  = 204
	nonShortcutSlotFrom    = 6
	nonShortcutSlotEnd     = 198
	preferredTreeSlotFrom  = 150
)

func IsShortcutSlot(slot int) bool {
	return (slot >= 0 && slot < ordinaryQuickSlotEnd) ||
		(slot >= extensionQuickSlotFrom && slot < extensionQuickSlotEnd)
}

// IsExclusiveComboSlot reports the six ASDFGH coordinates owned by the
// Dark Knight combo manager. Ordinary skills remain allowed in the separate
// QWERTY extension coordinates (198..203).
func IsExclusiveComboSlot(slot int) bool {
	return slot >= 0 && slot < ordinaryQuickSlotEnd
}

// NormalizeFixedComboRootSlots installs the learned PVF combo-set roots in the
// six ASDFGH coordinates owned by the Dark Knight combat HUD.  The current
// client does not synthesize these roots from the combo-editor notification:
// they must be ordinary op19 skill rows at slots 0..5.  Historical ordinary
// occupants are preserved in QWERTY (198..203) first and only overflow into a
// non-shortcut skill-tree coordinate.
func NormalizeFixedComboRootSlots(
	catalog *dnfskill.Table,
	states map[int64]dnfrepo.SkillState,
	existing dnfrepo.SkillLayout,
) (dnfrepo.SkillLayout, int, error) {
	if catalog == nil {
		return nil, 0, fmt.Errorf("dark knight combo root catalog is unavailable")
	}
	layout := make(dnfrepo.SkillLayout, len(existing))
	for slot, skillID := range existing {
		layout[slot] = skillID
	}

	rootsBySlot := make(map[int]uint16, ordinaryQuickSlotEnd)
	rootSkillIDs := make(map[uint16]struct{}, ordinaryQuickSlotEnd)
	for rawSkillID, state := range states {
		if !state.Enabled || state.Level <= 0 || rawSkillID <= 0 || rawSkillID > 0xffff {
			continue
		}
		skillID := uint16(rawSkillID)
		definition, found := catalog.Find(Job, skillID)
		if !found {
			continue
		}
		slot, root := fixedComboRootSlot(definition.Path)
		if !root {
			continue
		}
		if existingRoot, duplicate := rootsBySlot[slot]; duplicate && existingRoot != skillID {
			return nil, 0, fmt.Errorf("dark knight combo slot %d has duplicate PVF roots %d and %d", slot, existingRoot, skillID)
		}
		rootsBySlot[slot] = skillID
		rootSkillIDs[skillID] = struct{}{}
	}
	// 没有已学习的真实PVF连招根时，保留原有初始技能布局规则。
	if len(rootsBySlot) == 0 {
		return layout, 0, nil
	}

	// Remove every stale/wrong coordinate for a learned combo root before
	// displacing ordinary ASDFGH occupants.  This also heals duplicate roots.
	for slot, skillID := range layout {
		if _, root := rootSkillIDs[skillID]; root {
			delete(layout, slot)
		}
	}

	moved := 0
	for slot := 0; slot < ordinaryQuickSlotEnd; slot++ {
		skillID, occupied := layout[slot]
		if !occupied {
			continue
		}
		to := firstFreeSlot(layout, extensionQuickSlotFrom, extensionQuickSlotEnd)
		if to < 0 {
			to = firstFreeNonShortcutSlot(layout)
		}
		if to < 0 {
			return nil, 0, fmt.Errorf("cannot move ordinary dark knight skill %d from combo root slot %d", skillID, slot)
		}
		delete(layout, slot)
		layout[to] = skillID
		moved++
	}
	for slot, skillID := range rootsBySlot {
		layout[slot] = skillID
	}
	return layout, moved, nil
}

func fixedComboRootSlot(skillPath string) (int, bool) {
	normalized := strings.ReplaceAll(strings.TrimSpace(skillPath), `\`, "/")
	match := fixedComboRootPathRE.FindStringSubmatch(normalized)
	if len(match) != 3 {
		return 0, false
	}
	slot, err := strconv.Atoi(match[2])
	return slot, err == nil && slot >= 0 && slot < ordinaryQuickSlotEnd
}

// CleanAutoComboQuickSlots removes native combo candidates from shortcut
// coordinates while preserving every learned skill in a non-shortcut tree
// coordinate. Root skills are always excluded and saved children take
// priority even when their current PVF classification is incomplete.
func CleanAutoComboQuickSlots(
	catalog *dnfskill.Table,
	states map[int64]dnfrepo.SkillState,
	existing dnfrepo.SkillLayout,
	rootSkillIDs map[uint16]struct{},
	savedChildSkillIDs map[uint16]struct{},
) (dnfrepo.SkillLayout, int, error) {
	return cleanQuickSlots(existing, func(skillID uint16) bool {
		if _, root := rootSkillIDs[skillID]; root {
			return false
		}
		if _, saved := savedChildSkillIDs[skillID]; saved {
			return true
		}
		state := states[int64(skillID)]
		if !state.Enabled || state.Level <= 0 || catalog == nil {
			return false
		}
		definition, found := catalog.Find(Job, skillID)
		if !found || !definition.Active || definition.IsTPSkill() || definition.SkillClass < 0 || definition.SkillClass > 3 {
			return false
		}
		path := strings.ToLower(strings.ReplaceAll(strings.TrimSpace(definition.Path), `\`, "/"))
		path = strings.TrimPrefix(path, "skill/")
		return strings.HasPrefix(path, "demonicswordman/") && !strings.Contains(path, "/notapplicable/")
	})
}

// CleanSavedChildrenQuickSlots performs the second half of an auto-combo
// commit. A manual combo save must not call this function.
func CleanSavedChildrenQuickSlots(
	existing dnfrepo.SkillLayout,
	rootSkillIDs map[uint16]struct{},
	savedChildSkillIDs map[uint16]struct{},
) (dnfrepo.SkillLayout, int, error) {
	return cleanQuickSlots(existing, func(skillID uint16) bool {
		if _, root := rootSkillIDs[skillID]; root {
			return false
		}
		_, saved := savedChildSkillIDs[skillID]
		return saved
	})
}

func cleanQuickSlots(existing dnfrepo.SkillLayout, shouldMove func(uint16) bool) (dnfrepo.SkillLayout, int, error) {
	layout := make(dnfrepo.SkillLayout, len(existing))
	for slot, skillID := range existing {
		layout[slot] = skillID
	}
	shortcutSlots := make([]int, 0, len(layout))
	for slot, skillID := range layout {
		if IsShortcutSlot(slot) && shouldMove(skillID) {
			shortcutSlots = append(shortcutSlots, slot)
		}
	}
	sort.Ints(shortcutSlots)
	moved := 0
	for _, from := range shortcutSlots {
		skillID, occupied := layout[from]
		if !occupied {
			continue
		}
		to := firstFreeNonShortcutSlot(layout)
		if to < 0 {
			return nil, 0, fmt.Errorf("cannot move dark knight combo skill %d from shortcut %d", skillID, from)
		}
		delete(layout, from)
		layout[to] = skillID
		moved++
	}
	return layout, moved, nil
}

func firstFreeNonShortcutSlot(layout dnfrepo.SkillLayout) int {
	for slot := preferredTreeSlotFrom; slot < nonShortcutSlotEnd; slot++ {
		if _, occupied := layout[slot]; !occupied {
			return slot
		}
	}
	for slot := nonShortcutSlotFrom; slot < nonShortcutSlotEnd; slot++ {
		if _, occupied := layout[slot]; !occupied {
			return slot
		}
	}
	return -1
}

func firstFreeSlot(layout dnfrepo.SkillLayout, start int, end int) int {
	for slot := start; slot < end; slot++ {
		if _, occupied := layout[slot]; !occupied {
			return slot
		}
	}
	return -1
}
