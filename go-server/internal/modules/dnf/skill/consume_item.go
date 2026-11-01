package skill

import (
	dnfpvf "longheng.io/server/internal/modules/dnf/pvf"
	"strings"
)

// 现用技能文件的[consume item]逐项为物品ID、起始消耗、末级消耗；空的PVP覆盖不属于地下城任务。
func consumesColorlessCube(doc *dnfpvf.Document) bool {
	if doc == nil {
		return false
	}
	mode := ""
	uses := false
	for _, section := range doc.Sections {
		name := strings.ToLower(strings.TrimSpace(section.Name))
		switch name {
		case "dungeon", "pvp", "death tower", "warroom":
			mode = name
			continue
		case "/dungeon", "/pvp", "/death tower", "/warroom":
			mode = ""
			continue
		}
		if name != "consume item" || (mode != "" && mode != "dungeon") {
			continue
		}
		if section.Start < 0 || section.Start > section.End || section.End > len(doc.Tokens) {
			return false
		}
		var values []int64
		for _, token := range doc.Tokens[section.Start:section.End] {
			if token.Kind != dnfpvf.TokenInt {
				return false
			}
			values = append(values, token.Int)
		}
		uses = false // 地下城内显式配置覆盖通用配置，包括明确的空配置。
		if len(values)%3 != 0 {
			return false
		}
		for i := 0; i < len(values); i += 3 {
			if values[i] == 3037 && values[i+1] > 0 && values[i+2] > 0 {
				uses = true
			}
		}
	}
	return uses
}
