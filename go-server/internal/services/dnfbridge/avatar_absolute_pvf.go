package dnfbridge

import (
	"fmt"
	"strings"

	"longheng.io/server/internal/modules/dnf/jobmap"
	dnfpvf "longheng.io/server/internal/modules/dnf/pvf"
)

const currentAbsoluteAvatarMapPath = "etc/chn_absolute_bind_cube.etc"

type currentAbsoluteAvatarRule struct {
	Key           int64
	MaterialCount int
	MaterialGrade int64
	Part          string
}

// 梦想的套装键、件数取自道具action参数，材料品质取当前职业表upper grade。
// action第三项为客户端初始属性选项，不把它误作材料品质。
func loadCurrentAbsoluteAvatarRule(source dnfpvf.Source, cube dungeonDropItemDefinition, job byte, target uint32) (currentAbsoluteAvatarRule, error) {
	fail := func() (currentAbsoluteAvatarRule, error) {
		return currentAbsoluteAvatarRule{}, fmt.Errorf("梦想合成配置不完整或目标不属于当前道具及职业")
	}
	if source == nil || cube.Kind != dungeonDropItemStackable || normalizeDungeonDropStackableType(cube.StackableType) != "[etc]" || !strings.EqualFold(cube.ActionType, "[absolute bind cube]") {
		return fail()
	}
	text, err := source.ReadText(cube.PVFPath)
	if err != nil {
		return currentAbsoluteAvatarRule{}, err
	}
	doc, err := dnfpvf.Parse(cube.PVFPath, text)
	if err != nil {
		return currentAbsoluteAvatarRule{}, err
	}
	if !currentAbsoluteAvatarAllowsJob(doc, job) {
		return fail()
	}
	tokens, ok := doc.Section("action type")
	if !ok || len(tokens) != 6 || tokens[0].Kind != dnfpvf.TokenString || !strings.EqualFold(tokens[0].Value, "[absolute bind cube]") {
		return fail()
	}
	for _, t := range tokens[1:] {
		if t.Kind != dnfpvf.TokenInt {
			return fail()
		}
	}
	// 套装键0是PVF的有效声明（如天光云影），是否可用由对应分块与职业产出决定。
	if tokens[1].Int < 0 || tokens[2].Int <= 0 || tokens[2].Int > currentAbsoluteAvatarMaxMaterials || tokens[4].Int != 0 || tokens[5].Int != 0 {
		return fail()
	}
	rule := currentAbsoluteAvatarRule{Key: tokens[1].Int, MaterialCount: int(tokens[2].Int), MaterialGrade: -1}
	config, err := loadCurrentAvatarCompoundConfig(source, job)
	if err != nil {
		return currentAbsoluteAvatarRule{}, err
	}
	for _, v := range config.Variants {
		if !strings.HasPrefix(v.Path, currentAvatarCompoundCHNRoot+"/") {
			rule.MaterialGrade = v.UpperGrade
			break
		}
	}
	if rule.MaterialGrade < 0 {
		return fail()
	}
	tags := jobmap.QuestTags(int(job))
	if len(tags) == 0 {
		return fail()
	}
	text, err = source.ReadText(currentAbsoluteAvatarMapPath)
	if err != nil {
		return currentAbsoluteAvatarRule{}, err
	}
	doc, err = dnfpvf.Parse(currentAbsoluteAvatarMapPath, text)
	if err != nil {
		return currentAbsoluteAvatarRule{}, err
	}
	// 明确的区块状态机防止借用其它套装键或职业的产出。
	active, inPool, foundKey := false, false, false
	jobTag, part := "", ""
	seen := map[string]bool{}
	for _, section := range doc.Sections {
		ts := doc.Tokens[section.Start:section.End]
		switch strings.ToLower(strings.TrimSpace(section.Name)) {
		case "absolute bind cube":
			if len(ts) != 1 || ts[0].Kind != dnfpvf.TokenInt {
				return fail()
			}
			active = ts[0].Int == rule.Key
			inPool = false
			if active {
				if foundKey {
					return fail()
				}
				foundKey = true
			}
		case "absolute bind plus rare":
			if !active {
				continue
			}
			if inPool {
				return fail()
			}
			inPool = true
			for i := 0; i < len(ts); {
				if ts[i].Kind != dnfpvf.TokenString {
					return fail()
				}
				value := strings.ToLower(strings.TrimSpace(ts[i].Value))
				if strings.HasPrefix(value, "[") {
					jobTag = value
					i++
					continue
				}
				if i+1 >= len(ts) || ts[i+1].Kind != dnfpvf.TokenInt || jobTag == "" {
					return fail()
				}
				if jobTag == strings.ToLower(tags[0]) {
					if _, ok := currentAvatarCompoundPartEquipmentTypes[value]; !ok || seen[value] {
						return fail()
					}
					seen[value] = true
					id, ok := currentAvatarCompoundItemID(ts[i+1].Int)
					if !ok {
						return fail()
					}
					if id == target {
						if part != "" {
							return fail()
						}
						part = value
					}
				}
				i += 2
			}
		case "/absolute bind plus rare":
			if active {
				inPool = false
			}
		case "/absolute bind cube":
			active = false
			inPool = false
		}
	}
	if !foundKey || part == "" {
		return fail()
	}
	rule.Part = part
	return rule, nil
}

func currentAbsoluteAvatarAllowsJob(doc *dnfpvf.Document, job byte) bool {
	ts, found := doc.Section("usable job")
	if !found {
		return true
	}
	tags := jobmap.QuestTags(int(job))
	for _, t := range ts {
		if t.Kind != dnfpvf.TokenString {
			continue
		}
		if strings.EqualFold(t.Value, "[all]") {
			return true
		}
		for _, tag := range tags {
			if strings.EqualFold(t.Value, tag) {
				return true
			}
		}
	}
	return false
}
