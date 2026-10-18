package dnfbridge

import (
	"encoding/binary"
	"fmt"
	"sort"

	"google.golang.org/protobuf/encoding/protowire"
	darkcombo "longheng.io/server/internal/modules/dnf/darkknightcombo"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

const (
	currentSkillInfoMsgID       = 19
	currentSkillInfoMessageType = 19
	currentSkillInfoTreeIndex   = 0
	currentSkillInfoTreeCount   = 2
)

func buildCurrentSceneSkillTree(record dnfrepo.SkillRecord, layout dnfrepo.SkillLayout, commands map[uint16][]byte) ([]byte, []int, error) {
	return buildCurrentSceneSkillTreeWithCombo(record, layout, commands, nil)
}
func buildCurrentSceneSkillTreeWithCombo(record dnfrepo.SkillRecord, layout dnfrepo.SkillLayout, commands map[uint16][]byte, roots map[uint16][]uint16) ([]byte, []int, error) {
	slots := make([]int, 0, len(layout))
	for slot, skillID := range layout {
		state, ok := record.Skills[int64(skillID)]
		if !ok || state.Level <= 0 {
			continue
		}
		if slot < 0 || slot > 0x7fffffff {
			return nil, nil, fmt.Errorf("current skill slot out of int32 range: slot=%d skill=%d", slot, skillID)
		}
		slots = append(slots, slot)
	}
	sort.Ints(slots)

	var tree []byte
	tree = appendProtoVarint(tree, 1, uint64(nonNegativeInt(record.Points.RemainingSP)))
	tree = appendProtoVarint(tree, 2, uint64(nonNegativeInt(record.Points.RemainingTP)))
	activeSlots := make([]int, 0, currentSkillQuickSlotCount)
	for _, slot := range slots {
		skillID := layout[slot]
		state := record.Skills[int64(skillID)]
		wireSlot, err := currentSkillSlotToWire(slot)
		if err != nil {
			return nil, nil, err
		}
		if state.Level > 0x7fffffff {
			return nil, nil, fmt.Errorf("current skill level out of client rank range: skill=%d level=%d", skillID, state.Level)
		}
		var skill []byte
		skill = appendProtoVarint(skill, 1, uint64(wireSlot))
		skill = appendProtoVarint(skill, 2, uint64(skillID))
		// sub_2666AE0 preserves the wire rank unless a current actor mapping
		// explicitly replaces it. Persisted learned levels therefore stay direct.
		skill = appendProtoVarint(skill, 3, uint64(state.Level))
		// Current EXE skill field 4 is the optional per-skill command-customization
		// byte vector also applied by op331. It is persisted by the skill owner
		// under character:<id>:skill_commands; without it every login drops the
		// player's custom direction+attack command back to the PVF default
		// (operator report "技能操作指令无法更改"). Absent field = PVF default.
		if children, isRoot := roots[skillID]; isRoot {
			for _, child := range children {
				skill = appendProtoVarint(skill, 4, uint64(child))
			}
		} else if command := commands[skillID]; len(command) > 0 {
			skill = appendProtoBytes(skill, 4, command)
		}
		tree = appendProtoBytes(tree, 3, skill)
		if wireSlot < currentSkillQuickSlotCount {
			activeSlots = append(activeSlots, wireSlot)
		}
	}

	return tree, activeSlots, nil
}

func buildCurrentSceneSkillInfoBody(record dnfrepo.SkillRecord, layout dnfrepo.SkillLayout, commands map[uint16][]byte) ([]byte, []int, error) {
	return buildCurrentSceneSkillInfoBodyWithCombo(record, layout, commands, nil)
}
func buildCurrentSceneSkillInfoBodyWithCombo(record dnfrepo.SkillRecord, layout dnfrepo.SkillLayout, commands map[uint16][]byte, pages []darkcombo.Page) ([]byte, []int, error) {
	active := dnfrepo.ActiveSkillTree(record)
	var message []byte
	message = appendProtoVarint(message, 1, currentSkillInfoMessageType)
	var activeSlots []int
	for index := 0; index < currentSkillInfoTreeCount; index++ {
		profile := record
		pageLayout := layout
		if index != active {
			profile.Skills, profile.Points = record.Trees.Other.Skills, record.Trees.Other.Points
			pageLayout = record.Layouts[index]
		}

		roots := make(map[uint16][]uint16)
		for _, page := range pages {
			if int(page.Index) == index {
				for _, root := range page.Roots {
					roots[root.SkillID] = root.Children
				}
			}
		}
		tree, slots, err := buildCurrentSceneSkillTreeWithCombo(profile, pageLayout, commands, roots)
		if err != nil {
			return nil, nil, err
		}
		message = appendProtoBytes(message, 2, tree)
		if index == active {
			activeSlots = slots
		}
	}
	body := make([]byte, 4, 4+len(message))
	binary.LittleEndian.PutUint32(body, uint32(len(message)))
	body = append(body, message...)
	return body, activeSlots, nil
}

func appendProtoVarint(dst []byte, field protowire.Number, value uint64) []byte {
	dst = protowire.AppendTag(dst, field, protowire.VarintType)
	return protowire.AppendVarint(dst, value)
}

func appendProtoBytes(dst []byte, field protowire.Number, value []byte) []byte {
	dst = protowire.AppendTag(dst, field, protowire.BytesType)
	return protowire.AppendBytes(dst, value)
}

func nonNegativeInt(value int) int {
	if value < 0 {
		return 0
	}
	return value
}
