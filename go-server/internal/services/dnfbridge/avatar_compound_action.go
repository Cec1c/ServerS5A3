package dnfbridge

import (
	"fmt"
	"path"
	"strings"

	dnfpvf "longheng.io/server/internal/modules/dnf/pvf"
)

// currentAvatarCompoundHasMasterAction 识别道具PVF自身声明的钻石合成动作。
// 现场旧活动道具10008512未列入职业表的material_master，但实际定义是
// [stackable type] [etc]、[action type] [bind cube] 5，现用客户端也发送类型5。
// 不以中文名称、请求尾字节或固定活动编号授予合成资格。
func currentAvatarCompoundHasMasterAction(source dnfpvf.Source, cube dungeonDropItemDefinition) (bool, error) {
	if cube.Kind != dungeonDropItemStackable || normalizeDungeonDropStackableType(cube.StackableType) != "[etc]" ||
		!strings.EqualFold(strings.TrimSpace(cube.ActionType), "[bind cube]") {
		return false, nil
	}
	text, err := source.ReadText(cube.PVFPath)
	if err != nil {
		return false, fmt.Errorf("读取合成器PVF %s: %w", cube.PVFPath, err)
	}
	document, err := dnfpvf.Parse(cube.PVFPath, text)
	if err != nil {
		return false, fmt.Errorf("解析合成器PVF %s: %w", cube.PVFPath, err)
	}
	tokens, found := document.Section("action type")
	return found && len(tokens) >= 2 && tokens[0].Kind == dnfpvf.TokenString &&
		strings.EqualFold(strings.TrimSpace(tokens[0].Value), "[bind cube]") &&
		tokens[1].Kind == dnfpvf.TokenInt && tokens[1].Int == 5, nil
}

// masterActionAllowsTarget 将动作类钻石限定到支持master的经典职业表及其
// 可选高级外观池；沿用该表已有的材料品质概率和额外产出规则。
func (v currentAvatarCompoundVariant) masterActionAllowsTarget(part string, target uint32) bool {
	if v.MasterMaterialID == 0 || path.Dir(v.Path) != "etc" {
		return false
	}
	for _, entry := range v.Parts[part].Upper {
		if entry.ItemID == target {
			return true
		}
	}
	return false
}
