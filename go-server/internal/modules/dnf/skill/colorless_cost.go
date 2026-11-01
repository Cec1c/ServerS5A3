package skill

import (
	dnfpvf "longheng.io/server/internal/modules/dnf/pvf"
	"math"
	"strings"
)

// 当前真实PVF的无色消耗首末值相同；不猜测未知变动规则，也不采用PVP覆盖。
func fixedColorlessCubeCount(doc *dnfpvf.Document) int64 {
	if doc == nil {
		return 0
	}
	mode := ""
	var result int64
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
			return 0
		}
		values := []int64{}
		for _, token := range doc.Tokens[section.Start:section.End] {
			if token.Kind != dnfpvf.TokenInt {
				return 0
			}
			values = append(values, token.Int)
		}
		if len(values)%3 != 0 {
			return 0
		}
		result = 0
		for i := 0; i < len(values); i += 3 {
			if values[i] != 3037 {
				continue
			}
			a, b := values[i+1], values[i+2]
			if a < 0 || a != b || a > math.MaxUint32-result {
				return 0
			}
			result += a
		}
	}
	return result
}
