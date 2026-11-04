package drop

import (
	"fmt"
	"math"
	"strings"

	dnfpvf "longheng.io/server/internal/modules/dnf/pvf"
)

// DungeonMaterialSection is an optional ServerS5A3 extension in
// etc/independent_drop.etc, not a reinterpretation of a native drop record.
const DungeonMaterialSection = "servers5a3 dungeon material drop"
const DungeonMaterialProbabilityBound = 1000000

type DungeonMaterialRule struct {
	ItemID uint32
	// Normal, champion, super champion, boss; one personal item per hit.
	Probability [4]int
}

// ParseDungeonMaterialRules leaves archives without the extension unchanged.
// Rows are dungeon ID, material ID and four probabilities out of one million.
func ParseDungeonMaterialRules(doc *dnfpvf.Document) (map[int64][]DungeonMaterialRule, error) {
	rules := make(map[int64][]DungeonMaterialRule)
	if doc == nil {
		return rules, nil
	}
	starts, ends, start, end := 0, 0, -1, -1
	for i, token := range doc.Tokens {
		if token.Kind != dnfpvf.TokenSection {
			continue
		}
		switch strings.ToLower(strings.TrimSpace(token.Value)) {
		case DungeonMaterialSection:
			starts++
			start = i
		case "/" + DungeonMaterialSection:
			ends++
			end = i
		}
	}
	if starts == 0 && ends == 0 {
		return rules, nil
	}
	if starts != 1 || ends != 1 || end <= start || (end-start-1)%6 != 0 || end == start+1 {
		return nil, fmt.Errorf("%s: [%s] requires one closed block of six-integer rows", doc.Path, DungeonMaterialSection)
	}
	seen := make(map[[2]int64]bool)
	for i := start + 1; i < end; i += 6 {
		var v [6]int64
		for field := range v {
			token := doc.Tokens[i+field]
			if token.Kind != dnfpvf.TokenInt {
				return nil, fmt.Errorf("%s: [%s] line %d requires integers", doc.Path, DungeonMaterialSection, token.Line)
			}
			v[field] = token.Int
		}
		key := [2]int64{v[0], v[1]}
		if v[0] <= 0 || v[0] > math.MaxInt32 || v[1] <= 0 || v[1] > math.MaxUint32 || seen[key] {
			return nil, fmt.Errorf("%s: [%s] invalid or duplicate dungeon/material %v", doc.Path, DungeonMaterialSection, key)
		}
		seen[key] = true
		rule := DungeonMaterialRule{ItemID: uint32(v[1])}
		for rank := range rule.Probability {
			if v[rank+2] < 0 || v[rank+2] > DungeonMaterialProbabilityBound {
				return nil, fmt.Errorf("%s: [%s] probability outside 0..%d", doc.Path, DungeonMaterialSection, DungeonMaterialProbabilityBound)
			}
			rule.Probability[rank] = int(v[rank+2])
		}
		rules[v[0]] = append(rules[v[0]], rule)
	}
	return rules, nil
}
